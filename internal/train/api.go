package train

import (
	"context"
	"slices"
	"strings"
	"time"
)

// PR is a pull request as the train sees it.
type PR struct {
	Number    int
	URL       string
	HeadSHA   string
	BaseSHA   string
	Open      bool
	Merged    bool
	MergeSHA  string
	AutoMerge bool // auto-merge is enabled
	Labels    []string
	// Fork: the head branch lives in another repo (or a deleted one), so anyone could have
	// named it like release-please's; the train never merges such a PR.
	Fork bool
}

// Check is a check run, or a commit status mapped onto one (Name is the context).
type Check struct {
	ID         int64
	Name       string
	Status     string // queued, in_progress, completed
	Conclusion string // success, failure, skipped, ...
	StartedAt  time.Time
}

// Run is a GitHub Actions workflow run.
type Run struct {
	ID         int64
	Path       string // .github/workflows/release.yml
	HeadBranch string // the tag, for a release event
	Status     string
	Conclusion string
	URL        string
}

// Issue is a tracking issue.
type Issue struct {
	Number int
	Title  string
	Body   string
	URL    string
	Open   bool
	Labels []string
}

// Comment is an issue comment.
type Comment struct {
	ID     int64
	Author string
	Body   string
}

// Repos is what the train reads and changes in the manifest's repos (as ccf-release-bot, with
// a token scoped to them).
type Repos interface {
	DefaultBranch(ctx context.Context, repo string) (branch, sha string, err error)
	// File returns nil, nil when path doesn't exist at ref.
	File(ctx context.Context, repo, ref, path string) ([]byte, error)
	// OpenPR returns the oldest open PR whose head branch, in the repo itself (not a fork), starts
	// with headPrefix, or nil.
	OpenPR(ctx context.Context, repo, headPrefix string) (*PR, error)
	PR(ctx context.Context, repo string, number int) (*PR, error)
	Checks(ctx context.Context, repo, sha string) ([]Check, error)
	// Merge squash-merges the PR if its head is still sha.
	Merge(ctx context.Context, repo string, number int, sha string) error
	// Runs returns the workflow runs of event at head commit sha.
	Runs(ctx context.Context, repo, event, sha string) ([]Run, error)
	TagsAt(ctx context.Context, repo, sha string) ([]string, error)
	Release(ctx context.Context, repo, tag string) (body, url string, err error)
	RerunFailed(ctx context.Context, repo string, runID int64) error
	// EnsureIssue opens an issue unless an open one with the same title exists.
	EnsureIssue(ctx context.Context, repo, title, body string) (url string, err error)
}

// Tracker holds the tracking issues (the workflows repo, with GITHUB_TOKEN).
type Tracker interface {
	// Issues returns the issues with label, open and closed.
	Issues(ctx context.Context, label string) ([]Issue, error)
	CreateIssue(ctx context.Context, title, body string, labels []string) (Issue, error)
	// EditIssue sets the body and labels, and closes the issue when closed is true.
	EditIssue(ctx context.Context, number int, body string, labels []string, closed bool) error
	// Comments returns the comments with an ID above after, oldest first.
	Comments(ctx context.Context, number int, after int64) ([]Comment, error)
	Comment(ctx context.Context, number int, body string) (url string, err error)
}

// Members checks org roles.
type Members interface {
	IsOrgAdmin(ctx context.Context, user string) (bool, error)
}

// BumpResult is what a ccf-bump run did.
type BumpResult struct {
	PR      int  // the PR it opened or updated; 0 if none
	Changed bool // it found pins to move (with --dry-run, the PR it would open)
	Output  string
}

// Bumper runs ccf-bump --mode train --pr for one repo with the --set versions.
type Bumper interface {
	Bump(ctx context.Context, manifestPath, repo string, sets map[string]string, dryRun bool) (BumpResult, error)
}

// Checks states.
const (
	ChecksGreen   = "green"
	ChecksPending = "pending"
	ChecksFailing = "failing"
)

// The checks that gate a train merge (docs/train.md, "What gates a merge"). Only these can make a
// merge wait or hold; every other check on the PR (preview, CodeRabbit, osv-scanner, notify, the
// jobs ci / required sums up) is ignored, so a cancelled preview run never blocks a release.
const (
	// CICheck is the `required` job of each repo's `ci` caller, required on every PR the train
	// merges. cmd/train's --required-check overrides it.
	CICheck = "ci / required"
	// ReleaseCheck is the `release-checks` caller's job, required on release-please PRs too.
	ReleaseCheck = "release-checks / release-checks"
)

// failed are the conclusions that fail a check. cancelled is not one: the run was superseded (a
// caller's concurrency group cancels an older run when a newer one starts) or stopped by hand, so
// the check waits for a newer run. skipped, neutral and success pass.
var failed = map[string]bool{"failure": true, "timed_out": true, "action_required": true, "startup_failure": true}

// waitFor are the conclusions that leave a check pending: a newer run is expected.
var waitFor = map[string]bool{"cancelled": true, "stale": true}

// EvaluateChecks reduces a commit's required checks to green, pending or failing (with the names
// of the pending or failing ones); checks not in required are ignored. A commit can have several
// runs of a check (a re-run, a run for another event such as labeled, or one that superseded a
// cancelled run): a check with any run queued or in progress is pending, else its newest run (by
// start time, then ID) decides, and a cancelled newest run is pending too. A required check that
// never reported on the commit is pending, not green.
func EvaluateChecks(checks []Check, required []string) (state, detail string) {
	latest, running := map[string]Check{}, map[string]bool{}
	for _, c := range checks {
		if !slices.Contains(required, c.Name) {
			continue
		}
		running[c.Name] = running[c.Name] || c.Status != "completed"
		l, ok := latest[c.Name]
		if !ok || c.StartedAt.After(l.StartedAt) || c.StartedAt.Equal(l.StartedAt) && c.ID > l.ID {
			latest[c.Name] = c
		}
	}
	var failing, pending []string
	for _, name := range slices.Compact(slices.Sorted(slices.Values(required))) {
		l, ok := latest[name]
		switch {
		case !ok, running[name], waitFor[l.Conclusion]:
			pending = append(pending, name)
		case failed[l.Conclusion]:
			failing = append(failing, name)
		}
	}
	switch {
	case len(failing) > 0:
		return ChecksFailing, strings.Join(failing, ", ")
	case len(pending) > 0:
		return ChecksPending, strings.Join(pending, ", ")
	}
	return ChecksGreen, ""
}
