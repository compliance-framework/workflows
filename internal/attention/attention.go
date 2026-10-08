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

// Message is the Slack message for d (mrkdwn): a header, then each repo's PRs as
// "<url|repo#n> title · reasons · age". runURL, when set, links the run.
func Message(d Digest, now time.Time, runURL string) string {
	var b strings.Builder
	n := len(d.Items)
	if n == 1 {
		b.WriteString(":raising_hand: 1 PR needs a human\n")
	} else {
		fmt.Fprintf(&b, ":raising_hand: %d PRs need a human\n", n)
	}
	repo := ""
	for _, it := range d.Items {
		if it.PR.Repo != repo {
			repo = it.PR.Repo
			fmt.Fprintf(&b, "*%s*\n", escape(repo))
		}
		fmt.Fprintf(&b, "• <%s|%s#%d> %s · %s · %s\n", escape(it.PR.URL), escape(it.PR.Repo), it.PR.Number,
			escape(it.PR.Title), escape(strings.Join(it.Reasons, ", ")), Age(now.Sub(it.PR.Created)))
	}
	if len(d.Failed) > 0 {
		where := make([]string, len(d.Failed))
		for i, f := range d.Failed {
			where[i] = escape(f.Where)
		}
		fmt.Fprintf(&b, "Could not read: %s\n", strings.Join(where, ", "))
	}
	if runURL != "" {
		fmt.Fprintf(&b, "<%s|run>\n", escape(runURL))
	}
	return strings.TrimRight(b.String(), "\n")
}

// escape escapes the characters Slack mrkdwn treats as control characters.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
