// Package automerge merges a bot's green PRs through the API as the token's identity: `ccf-bump
// merge` (ccf-bump/* branches) and `renovate-config merge` (renovate/* and image-scan/* branches). GitHub's native
// auto-merge never applies the ccf-review bypass of ccf-release-bot, so a bot PR with a green
// required check would otherwise wait for a person (docs/ccf-bump.md, docs/renovate.md).
package automerge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/bump"
)

// PollInterval is how often a pass with Wait looks at PRs whose required check is pending.
const PollInterval = 30 * time.Second

// GitHub is what a merge pass needs from the API; *bump.GitHub implements it.
type GitHub interface {
	OpenPRs(ctx context.Context, repo string) ([]bump.PR, error)
	PullRequest(ctx context.Context, repo string, number int) (*bump.PR, error)
	CheckRuns(ctx context.Context, repo, sha, name string) ([]bump.CheckRun, error)
	Merge(ctx context.Context, repo string, number int, sha, title string) error
}

// CommitAuthors lists the author login of each commit of a PR ("" for a commit GitHub can't link
// to an account); *bump.GitHub implements it.
type CommitAuthors interface {
	PRCommitAuthors(ctx context.Context, repo string, number int) ([]string, error)
}

// Merger is one merge pass: it merges the open PRs labelled Label, from a branch of
// the repo itself (not a fork), opened by Author and not labelled needs-human, once the newest
// run of Required on the head succeeded and GitHub reports the PR mergeable.
type Merger struct {
	GH             GitHub
	Tool           string   // names the tool in ::error:: lines, e.g. "ccf-bump"
	Owner          string   // the repos' owner
	Label          string   // e.g. bump.AutomergeLabel
	BranchPrefixes []string // a PR's branch starts with one of these, e.g. "ccf-bump/"
	Author         string   // the bot's login (the token's identity)
	Required       string   // the check that must pass on the head, e.g. "ci / required"
	Wait           time.Duration
	DryRun         bool
	// Commits, when set, also requires every commit of the PR to be authored by Author, so a PR
	// someone else pushed to is left for a person (Renovate's own "modified branch" rule).
	Commits CommitAuthors
	Out     io.Writer
	Warn    func(format string, args ...any) // a PR it leaves: failing, conflicting, refused
	Now     func() time.Time
	Sleep   func(time.Duration)
}

// candidate is a PR the pass may merge; why says what it waits for while pending.
type candidate struct {
	repo string
	pr   bump.PR
	why  string
}

func (c *candidate) String() string { return fmt.Sprintf("%s#%d", c.repo, c.pr.Number) }

// MergeAll merges the green candidate PRs of repos. Listing or reading a PR failing fails the
// pass (the other repos still run); a PR it leaves (failing, conflicting, refused) is a warning.
func (m *Merger) MergeAll(ctx context.Context, repos []string) error {
	if m.Wait < 0 {
		return errors.New("--wait must not be negative")
	}
	var failed []error
	fail := func(err error) {
		fmt.Fprintln(m.Out, "::error::"+m.Tool+": "+err.Error())
		failed = append(failed, err)
	}
	var pending []*candidate
	for _, r := range repos {
		cs, err := m.candidates(ctx, r)
		if err != nil {
			fail(fmt.Errorf("%s: list open PRs: %w", r, err))
			continue
		}
		pending = append(pending, cs...)
	}
	deadline := m.Now().Add(m.Wait)
	for {
		var still []*candidate
		for _, c := range pending {
			waiting, err := m.tryMerge(ctx, c)
			if err != nil {
				fail(fmt.Errorf("%s: %w", c, err))
			} else if waiting {
				still = append(still, c)
			}
		}
		pending = still
		left := deadline.Sub(m.Now())
		if len(pending) == 0 || m.DryRun || left <= 0 {
			break
		}
		fmt.Fprintf(m.Out, "%d PR(s) pending; checking again in %s\n", len(pending), min(PollInterval, left))
		m.Sleep(min(PollInterval, left))
	}
	for _, c := range pending {
		fmt.Fprintf(m.Out, "%s: still pending (%s); a later merge pass merges it\n", c, c.why)
	}
	return errors.Join(failed...)
}

// candidates lists repo's open PRs the pass may merge. Other bots' and people's PRs never qualify.
func (m *Merger) candidates(ctx context.Context, repo string) ([]*candidate, error) {
	prs, err := m.GH.OpenPRs(ctx, repo)
	if err != nil {
		return nil, err
	}
	var out []*candidate
	for _, p := range prs {
		c := &candidate{repo: repo, pr: p}
		switch {
		case !p.HasLabel(m.Label):
		case !slices.ContainsFunc(m.BranchPrefixes, func(b string) bool { return strings.HasPrefix(p.Head.Ref, b) }) || p.Head.Repo == nil ||
			!strings.EqualFold(p.Head.Repo.FullName, m.Owner+"/"+repo) || !strings.EqualFold(p.User.Login, m.Author):
			fmt.Fprintf(m.Out, "%s: labelled %s, but not a %s* branch of the repo opened by %s; left alone\n", c, m.Label, strings.Join(m.BranchPrefixes, "* or "), m.Author)
		case p.HasLabel(bump.NeedsHumanLabel):
			fmt.Fprintf(m.Out, "%s: labelled %s; left for a person\n", c, bump.NeedsHumanLabel)
		default:
			out = append(out, c)
		}
	}
	return out, nil
}

// tryMerge merges c once its head's required check succeeded and GitHub reports it mergeable. It
// reports whether c is still pending (a run of the check not concluded, or mergeability not
// computed yet).
func (m *Merger) tryMerge(ctx context.Context, c *candidate) (bool, error) {
	runs, err := m.GH.CheckRuns(ctx, c.repo, c.pr.Head.SHA, m.Required)
	if err != nil {
		return false, fmt.Errorf("check %s: %w", m.Required, err)
	}
	// Every run counts: an earlier run's success doesn't merge while a newer one (e.g. started by
	// the labeled event) is running, and GitHub would refuse the merge anyway.
	running, check := bump.RequiredCheck(runs)
	switch {
	case running != nil:
		c.why = m.Required + " " + running.Status
		return true, nil
	case check == nil:
		c.why = m.Required + " not started"
		return true, nil
	case check.Conclusion != "success":
		// notify-failure.yml reports the failure; the attention digest lists the PR.
		m.Warn("%s: %s concluded %s at %s; left open (%s)", c, m.Required, check.Conclusion, short(c.pr.Head.SHA), check.URL)
		return false, nil
	}
	pr, err := m.GH.PullRequest(ctx, c.repo, c.pr.Number)
	if err != nil {
		return false, err
	}
	switch {
	case pr.State != "open": // merged (e.g. by the train or the bot itself) or closed meanwhile
		fmt.Fprintf(m.Out, "%s: no longer open\n", c)
		return false, nil
	case !pr.HasLabel(m.Label) || pr.HasLabel(bump.NeedsHumanLabel): // relabelled meanwhile
		fmt.Fprintf(m.Out, "%s: labels changed (%s removed or %s added); left open\n", c, m.Label, bump.NeedsHumanLabel)
		return false, nil
	case pr.Head.SHA != c.pr.Head.SHA: // pushed meanwhile: check the new head
		c.pr, c.why = *pr, "new head "+short(pr.Head.SHA)
		return true, nil
	case pr.Mergeable == nil:
		c.why = "GitHub is computing mergeability"
		return true, nil
	case !*pr.Mergeable || pr.MergeableState == "dirty":
		m.Warn("%s: conflicts with its base; left open", c)
		return false, nil
	}
	if m.Commits != nil {
		authors, err := m.Commits.PRCommitAuthors(ctx, c.repo, c.pr.Number)
		if err != nil {
			return false, fmt.Errorf("list commits: %w", err)
		}
		if !allBy(authors, m.Author) {
			fmt.Fprintf(m.Out, "%s: not every commit is authored by %s; left for a person\n", c, m.Author)
			return false, nil
		}
	}
	title := fmt.Sprintf("%s (#%d)", c.pr.Title, c.pr.Number)
	if m.DryRun {
		fmt.Fprintf(m.Out, "%s: dry run: would merge %q\n", c, title)
		return false, nil
	}
	if err := m.GH.Merge(ctx, c.repo, c.pr.Number, c.pr.Head.SHA, title); bump.ExpectsCheck(err) {
		c.why = "GitHub still expects " + m.Required // a run started after the listing
		return true, nil
	} else if err != nil {
		m.Warn("%s: merge refused: %v", c, err)
		return false, nil
	}
	fmt.Fprintf(m.Out, "%s: merged %q\n", c, title)
	return false, nil
}

// allBy reports whether there is at least one author and every one is login.
func allBy(authors []string, login string) bool {
	for _, a := range authors {
		if !strings.EqualFold(a, login) {
			return false
		}
	}
	return len(authors) > 0
}

func short(sha string) string { return sha[:min(7, len(sha))] }
