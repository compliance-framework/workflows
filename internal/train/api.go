package train

import (
	"context"
	"maps"
	"slices"
	"strings"
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
}

// Check is a check run, or a commit status mapped onto one (Name is the context).
type Check struct {
	ID         int64
	Name       string
	Status     string // queued, in_progress, completed
	Conclusion string // success, failure, skipped, ...
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
	// OpenPR returns the open PR whose head branch starts with headPrefix, or nil.
	OpenPR(ctx context.Context, repo, headPrefix string) (*PR, error)
	PR(ctx context.Context, repo string, number int) (*PR, error)
	// BehindBy returns how many commits base has that head lacks.
	BehindBy(ctx context.Context, repo, base, head string) (int, error)
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

// Slack posts to the releases channel.
type Slack interface {
	// Post posts text, as a reply in thread threadTS unless it is empty, and returns its ts.
	Post(ctx context.Context, channel, text, threadTS string) (ts string, err error)
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

var failed = map[string]bool{"failure": true, "timed_out": true, "cancelled": true, "action_required": true, "startup_failure": true}

// EvaluateChecks reduces a commit's checks to green, pending or failing (with the failing
// names), using the latest check of each name. required must be present and pass.
func EvaluateChecks(checks []Check, required string) (state, detail string) {
	latest := map[string]Check{}
	for _, c := range checks {
		if l, ok := latest[c.Name]; !ok || c.ID > l.ID {
			latest[c.Name] = c
		}
	}
	var failing, pending []string
	for _, name := range slices.Sorted(maps.Keys(latest)) {
		c := latest[name]
		switch {
		case c.Status != "completed":
			pending = append(pending, name)
		case failed[c.Conclusion]:
			failing = append(failing, name)
		}
	}
	if _, ok := latest[required]; required != "" && !ok {
		pending = append(pending, required)
	}
	switch {
	case len(failing) > 0:
		return ChecksFailing, strings.Join(failing, ", ")
	case len(pending) > 0:
		return ChecksPending, strings.Join(pending, ", ")
	}
	return ChecksGreen, ""
}
