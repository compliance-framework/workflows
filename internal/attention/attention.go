// Package attention finds the open PRs of the manifest's repos that wait for a person, for the
// weekly attention digest (cmd/attention-digest, docs/attention.md).
package attention

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/release"
	"github.com/compliance-framework/workflows/internal/slackkit"
)

const (
	// NeedsHumanLabel marks a bot PR waiting for a person (Renovate and ccf-bump add it).
	NeedsHumanLabel = "needs-human"
	// ReleaseBranchPrefix is the head branch prefix of release-please PRs.
	ReleaseBranchPrefix = "release-please--"
	// ReleaseManifest is release-please's manifest, which version-guard compares.
	ReleaseManifest = ".release-please-manifest.json"
)

// BotBranchPrefixes are the head branch prefixes of bot PRs, whoever opened them.
var BotBranchPrefixes = []string{"renovate/", "ccf-bump/", ReleaseBranchPrefix}

// PR is an open pull request.
type PR struct {
	Repo    string
	Number  int
	Title   string
	URL     string
	Author  string
	HeadRef string
	HeadSHA string
	BaseSHA string
	Labels  []string
	Created time.Time
}

// Client reads the repos (read-only).
type Client interface {
	OpenPRs(ctx context.Context, owner, repo string) ([]PR, error)
	// FailedCheck reports whether the latest run of the check name on sha failed.
	FailedCheck(ctx context.Context, owner, repo, sha, name string) (bool, error)
	// File returns path at ref, or nil when it doesn't exist.
	File(ctx context.Context, owner, repo, ref, path string) ([]byte, error)
}

// Config are the rules' settings.
type Config struct {
	Owner          string
	BotLogin       string        // the release bot, which opens Renovate, ccf-bump and release PRs
	RequiredChecks []string      // the checks every PR must pass, e.g. "ci / required"
	ReleaseCheck   string        // the release-checks check, e.g. "release-checks / release-checks"
	StaleAfter     time.Duration // a bot PR open longer needs a human (release-please PRs excepted)
	Now            time.Time
}

// Item is a PR that needs a human, and why.
type Item struct {
	PR      PR
	Reasons []string
}

// Failure is a repo or PR that could not be read.
type Failure struct {
	Where string // repo, or repo#n
	Err   error
}

// Digest is what Collect found.
type Digest struct {
	Items  []Item // grouped by repo in the order given, then by PR number
	Failed []Failure
}

// IsBot reports whether pr was opened by the release bot or from a bot branch.
func (c Config) IsBot(pr PR) bool {
	return pr.Author == c.BotLogin || slices.ContainsFunc(BotBranchPrefixes, func(p string) bool { return strings.HasPrefix(pr.HeadRef, p) })
}

// Collect reads every repo's open PRs and keeps those that need a human: labelled needs-human;
// or a bot PR (IsBot) whose required check failed, or that is open longer than StaleAfter (but a
// release-please PR, which waits for the monthly train by design); or a release-please PR whose
// release check failed (version-guard: a major increase without release:major-approved, or
// another release check).
func Collect(ctx context.Context, c Client, cfg Config, repos []string) Digest {
	var d Digest
	for _, repo := range repos {
		prs, err := c.OpenPRs(ctx, cfg.Owner, repo)
		if err != nil {
			d.Failed = append(d.Failed, Failure{Where: repo, Err: err})
			continue
		}
		slices.SortFunc(prs, func(a, b PR) int { return a.Number - b.Number })
		for _, pr := range prs {
			pr.Repo = repo
			reasons, err := cfg.reasons(ctx, c, pr)
			if err != nil {
				d.Failed = append(d.Failed, Failure{Where: fmt.Sprintf("%s#%d", repo, pr.Number), Err: err})
			}
			if len(reasons) > 0 {
				d.Items = append(d.Items, Item{PR: pr, Reasons: reasons})
			}
		}
	}
	return d
}

func (cfg Config) reasons(ctx context.Context, c Client, pr PR) ([]string, error) {
	var out []string
	if slices.Contains(pr.Labels, NeedsHumanLabel) {
		out = append(out, "labelled "+NeedsHumanLabel)
	}
	if !cfg.IsBot(pr) {
		return out, nil
	}
	var errs []error
	if strings.HasPrefix(pr.HeadRef, ReleaseBranchPrefix) && cfg.ReleaseCheck != "" {
		failed, err := c.FailedCheck(ctx, cfg.Owner, pr.Repo, pr.HeadSHA, cfg.ReleaseCheck)
		switch {
		case err != nil:
			errs = append(errs, err)
		case failed:
			out = append(out, cfg.releaseReason(ctx, c, pr))
		}
	}
	for _, name := range cfg.RequiredChecks {
		failed, err := c.FailedCheck(ctx, cfg.Owner, pr.Repo, pr.HeadSHA, name)
		if err != nil {
			errs = append(errs, err)
		} else if failed {
			out = append(out, name+" failed")
		}
	}
	// A release-please PR stays open until the monthly train merges it: its age says nothing.
	if age := cfg.Now.Sub(pr.Created); age > cfg.StaleAfter && !strings.HasPrefix(pr.HeadRef, ReleaseBranchPrefix) {
		out = append(out, fmt.Sprintf("open over %s", Age(cfg.StaleAfter)))
	}
	if len(errs) > 0 {
		return out, errs[0]
	}
	return out, nil
}

// releaseReason says why a release PR's release check failed: version-guard's major increase
// without the approval label, or the check in general (a dependency that isn't final, a module
// path, or one the manifests can't tell).
func (cfg Config) releaseReason(ctx context.Context, c Client, pr PR) string {
	general := cfg.ReleaseCheck + " failed"
	if slices.Contains(pr.Labels, release.MajorApprovedLabel) {
		return general
	}
	read := func(ref string) (map[string]string, error) {
		b, err := c.File(ctx, cfg.Owner, pr.Repo, ref, ReleaseManifest)
		if err != nil || b == nil {
			return map[string]string{}, err
		}
		return release.ParseManifest(b)
	}
	base, err := read(pr.BaseSHA)
	if err != nil {
		return general
	}
	head, err := read(pr.HeadSHA)
	if err != nil {
		return general
	}
	if majors, err := release.MajorIncreases(base, head); err == nil && len(majors) > 0 {
		return "needs " + release.MajorApprovedLabel + " (" + strings.Join(majors, ", ") + ")"
	}
	return general
}

// Age is d in whole days, or hours under a day: "12d", "5h", "<1h".
func Age(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return "<1h"
}

// title is the digest's headline: "N PRs need a human".
func title(n int) string {
	if n == 1 {
		return "1 PR needs a human"
	}
	return fmt.Sprintf("%d PRs need a human", n)
}

// line is one PR of the digest (mrkdwn): "<url|owner/repo#n> title · reasons · age".
func line(it Item, owner string, now time.Time) string {
	return fmt.Sprintf("<%s|%s/%s#%d> %s · %s · %s", escape(it.PR.URL), escape(owner), escape(it.PR.Repo), it.PR.Number,
		escape(it.PR.Title), escape(strings.Join(it.Reasons, ", ")), Age(now.Sub(it.PR.Created)))
}

// byRepo groups the items' lines by repo, in the items' order.
func byRepo(d Digest, owner string, now time.Time) (repos []string, lines map[string][]string) {
	lines = map[string][]string{}
	for _, it := range d.Items {
		if _, ok := lines[it.PR.Repo]; !ok {
			repos = append(repos, it.PR.Repo)
		}
		lines[it.PR.Repo] = append(lines[it.PR.Repo], line(it, owner, now))
	}
	return repos, lines
}

// Message is the digest as mrkdwn text, for the log and dry runs: the title, then each repo's
// PRs. runURL, when set, links the run.
func Message(d Digest, owner string, now time.Time, runURL string) string {
	var b strings.Builder
	b.WriteString(":raising_hand: " + title(len(d.Items)) + "\n")
	repos, lines := byRepo(d, owner, now)
	for _, repo := range repos {
		fmt.Fprintf(&b, "*%s/%s*\n• %s\n", escape(owner), escape(repo), strings.Join(lines[repo], "\n• "))
	}
	if len(d.Failed) > 0 {
		b.WriteString(failedLine(d) + "\n")
	}
	if runURL != "" {
		fmt.Fprintf(&b, "<%s|run>\n", escape(runURL))
	}
	return strings.TrimRight(b.String(), "\n")
}

// The card's limits: Slack takes 50 blocks and 3000 characters per section. A repo whose lines
// don't fit one section continues in the next; past maxSections, the rest share the last one
// (which Section truncates).
const (
	maxSections    = 40
	maxSectionText = 2800
)

// Card is the digest's Slack card: the header "N PRs need a human", one compact section per
// repo with a line per PR, what could not be read, and a link to the run; an amber bar.
func Card(d Digest, owner string, now time.Time, runURL string) slackkit.Message {
	repos, lines := byRepo(d, owner, now)
	var sections []string
	for _, repo := range repos {
		cur := fmt.Sprintf("*%s/%s*", escape(owner), escape(repo))
		for _, l := range lines[repo] {
			if len(cur)+len(l) > maxSectionText {
				sections, cur = append(sections, cur), ""
			}
			cur = strings.TrimPrefix(cur+"\n• "+l, "\n")
		}
		sections = append(sections, cur)
	}
	if len(sections) > maxSections {
		sections = append(sections[:maxSections-1], strings.Join(sections[maxSections-1:], "\n"))
	}
	var blocks []slackkit.Block
	for _, text := range sections {
		blocks = append(blocks, slackkit.Section(text))
	}
	var failed, run string
	if len(d.Failed) > 0 {
		failed = ":warning: " + failedLine(d)
	}
	if runURL != "" {
		run = slackkit.LinkTo(runURL, "Digest run")
	}
	blocks = append(blocks, slackkit.Context(failed, run)...)
	refs := make([]string, len(d.Items))
	for i, it := range d.Items {
		refs[i] = fmt.Sprintf("%s/%s#%d", owner, it.PR.Repo, it.PR.Number)
	}
	return slackkit.Message{
		Text:   title(len(d.Items)) + ": " + escape(strings.Join(refs, ", ")),
		Header: ":raising_hand: " + title(len(d.Items)),
		Blocks: blocks,
		Color:  slackkit.ColorAmber,
	}
}

func failedLine(d Digest) string {
	where := make([]string, len(d.Failed))
	for i, f := range d.Failed {
		where[i] = escape(f.Where)
	}
	return "Could not read: " + strings.Join(where, ", ")
}

// escape escapes the characters Slack mrkdwn treats as control characters.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
