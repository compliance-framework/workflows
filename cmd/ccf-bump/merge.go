package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/bump"
)

// pollInterval is how often merge --wait looks at PRs whose required check is pending.
const pollInterval = 30 * time.Second

// candidate is a PR merge may merge; why says what it waits for while pending.
type candidate struct {
	repo string
	pr   bump.PR
	why  string
}

func (c *candidate) String() string { return fmt.Sprintf("%s#%d", c.repo, c.pr.Number) }

// mergeAll merges the green ccf-bump:automerge PRs of repos as the token's identity, so the
// ccf-review bypass applies (GitHub's native auto-merge never applies it; docs/ccf-bump.md).
// Listing or reading a PR failing fails the run; a PR it leaves (failing, conflicting, refused)
// is a warning.
func (b *bumper) mergeAll(ctx context.Context, repos []string) error {
	if b.o.wait < 0 {
		return errors.New("--wait must not be negative")
	}
	var failed []error
	fail := func(err error) {
		fmt.Fprintln(b.e.stdout, "::error::ccf-bump: "+err.Error())
		failed = append(failed, err)
	}
	var pending []*candidate
	for _, r := range repos {
		cs, err := b.candidates(ctx, r)
		if err != nil {
			fail(fmt.Errorf("%s: list open PRs: %w", r, err))
			continue
		}
		pending = append(pending, cs...)
	}
	deadline := b.e.now().Add(b.o.wait)
	for {
		var still []*candidate
		for _, c := range pending {
			waiting, err := b.tryMerge(ctx, c)
			if err != nil {
				fail(fmt.Errorf("%s: %w", c, err))
			} else if waiting {
				still = append(still, c)
			}
		}
		pending = still
		left := deadline.Sub(b.e.now())
		if len(pending) == 0 || b.o.dryRun || left <= 0 {
			break
		}
		fmt.Fprintf(b.e.stdout, "%d PR(s) pending; checking again in %s\n", len(pending), min(pollInterval, left))
		b.e.sleep(min(pollInterval, left))
	}
	for _, c := range pending {
		fmt.Fprintf(b.e.stdout, "%s: still pending (%s); a later merge pass merges it\n", c, c.why)
	}
	return errors.Join(failed...)
}

// candidates lists repo's open PRs that merge may merge: labelled ccf-bump:automerge, from a
// ccf-bump/* branch of repo itself (not a fork), opened by --author (the release bot), and not
// labelled needs-human. Renovate, release-please and people's PRs never qualify.
func (b *bumper) candidates(ctx context.Context, repo string) ([]*candidate, error) {
	prs, err := b.e.gh.OpenPRs(ctx, repo)
	if err != nil {
		return nil, err
	}
	var out []*candidate
	for _, p := range prs {
		c := &candidate{repo: repo, pr: p}
		switch {
		case !p.HasLabel(bump.AutomergeLabel):
		case !strings.HasPrefix(p.Head.Ref, "ccf-bump/") || p.Head.Repo == nil ||
			!strings.EqualFold(p.Head.Repo.FullName, b.o.owner+"/"+repo) || !strings.EqualFold(p.User.Login, b.o.author):
			fmt.Fprintf(b.e.stdout, "%s: labelled %s, but not a ccf-bump/* branch of the repo opened by %s; left alone\n", c, bump.AutomergeLabel, b.o.author)
		case p.HasLabel(bump.NeedsHumanLabel):
			fmt.Fprintf(b.e.stdout, "%s: labelled %s; left for a person\n", c, bump.NeedsHumanLabel)
		default:
			out = append(out, c)
		}
	}
	return out, nil
}

// tryMerge merges c once its head's required check succeeded and GitHub reports it mergeable. It
// reports whether c is still pending (a run of the check not concluded, or mergeability not
// computed yet).
func (b *bumper) tryMerge(ctx context.Context, c *candidate) (bool, error) {
	runs, err := b.e.gh.CheckRuns(ctx, c.repo, c.pr.Head.SHA, b.o.required)
	if err != nil {
		return false, fmt.Errorf("check %s: %w", b.o.required, err)
	}
	// Every run counts: an earlier run's success doesn't merge while a newer one (e.g. started by
	// the labeled event) is running, and GitHub would refuse the merge anyway.
	running, check := bump.RequiredCheck(runs)
	switch {
	case running != nil:
		c.why = b.o.required + " " + running.Status
		return true, nil
	case check == nil:
		c.why = b.o.required + " not started"
		return true, nil
	case check.Conclusion != "success":
		// notify-failure.yml reports the failure; the attention digest lists the PR.
		b.warn("%s: %s concluded %s at %s; left open (%s)", c, b.o.required, check.Conclusion, short(c.pr.Head.SHA), check.URL)
		return false, nil
	}
	pr, err := b.e.gh.PullRequest(ctx, c.repo, c.pr.Number)
	if err != nil {
		return false, err
	}
	switch {
	case pr.State != "open": // merged (e.g. by the train) or closed meanwhile
		fmt.Fprintf(b.e.stdout, "%s: no longer open\n", c)
		return false, nil
	case !pr.HasLabel(bump.AutomergeLabel) || pr.HasLabel(bump.NeedsHumanLabel): // relabelled meanwhile
		fmt.Fprintf(b.e.stdout, "%s: labels changed (%s removed or %s added); left open\n", c, bump.AutomergeLabel, bump.NeedsHumanLabel)
		return false, nil
	case pr.Head.SHA != c.pr.Head.SHA: // pushed meanwhile: check the new head
		c.pr, c.why = *pr, "new head "+short(pr.Head.SHA)
		return true, nil
	case pr.Mergeable == nil:
		c.why = "GitHub is computing mergeability"
		return true, nil
	case !*pr.Mergeable || pr.MergeableState == "dirty":
		b.warn("%s: conflicts with its base; left open", c)
		return false, nil
	}
	title := fmt.Sprintf("%s (#%d)", c.pr.Title, c.pr.Number)
	if b.o.dryRun {
		fmt.Fprintf(b.e.stdout, "%s: dry run: would merge %q\n", c, title)
		return false, nil
	}
	if err := b.e.gh.Merge(ctx, c.repo, c.pr.Number, c.pr.Head.SHA, title); bump.ExpectsCheck(err) {
		c.why = "GitHub still expects " + b.o.required // a run started after the listing
		return true, nil
	} else if err != nil {
		b.warn("%s: merge refused: %v", c, err)
		return false, nil
	}
	fmt.Fprintf(b.e.stdout, "%s: merged %q\n", c, title)
	return false, nil
}

func short(sha string) string { return sha[:min(7, len(sha))] }
