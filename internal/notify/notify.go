// Package notify decides whether a failed CI run should be reported to Slack, builds the
// dedupe key and the message, and posts it. The notify-failure reusable workflow drives it
// through cmd/notify.
//
// A failed run is reported when:
//   - (a) it is for a pull request opened by ReleaseBotLogin, or for a branch whose name
//     starts with one of AutomationBranchPrefixes (a pull request or a push);
//   - (b) it is for a push to the default branch, and the previous completed run of the
//     same workflow on that branch passed (or there is none).
//
// One message is sent per repo + commit + workflow (see DedupeKey).
package notify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ReleaseBotLogin is the author of release PRs (release-please, ccf-bump).
const ReleaseBotLogin = "ccf-release-bot[bot]"

// AutomationBranchPrefixes are the branch name prefixes of dependency-bump branches.
var AutomationBranchPrefixes = []string{"renovate/", "ccf-bump/"}

// Reason says why a run is reported. The empty Reason means it is not.
type Reason string

const (
	ReasonNone             Reason = ""
	ReasonReleaseBotPR     Reason = "release-bot-pr"
	ReasonAutomationBranch Reason = "automation-branch"
	ReasonDefaultBranch    Reason = "default-branch-broken"
)

// ParseReason checks that s is a known non-empty Reason.
func ParseReason(s string) (Reason, error) {
	switch r := Reason(s); r {
	case ReasonReleaseBotPR, ReasonAutomationBranch, ReasonDefaultBranch:
		return r, nil
	}
	return ReasonNone, fmt.Errorf("unknown reason %q", s)
}

// Run describes the workflow run being reported, from the caller's github context.
type Run struct {
	Repo          string // owner/name
	Workflow      string // the caller workflow's name
	EventName     string
	SHA           string // the commit under test: the PR head, or the pushed commit
	Branch        string // the PR head branch, or the pushed branch
	DefaultBranch string
	ServerURL     string
	RunID         string
	RunNumber     int
	RunAttempt    string
	Actor         string

	PRNumber int
	PRTitle  string
	PRURL    string
	PRAuthor string
}

// event is the subset of the webhook payload that Run needs.
type event struct {
	After       string `json:"after"`
	PullRequest *struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
}

// RunFromEnv builds a Run from the GITHUB_* variables of a workflow step and the event
// payload at GITHUB_EVENT_PATH.
func RunFromEnv(getenv func(string) string) (Run, error) {
	r := Run{
		Repo:       getenv("GITHUB_REPOSITORY"),
		Workflow:   getenv("GITHUB_WORKFLOW"),
		EventName:  getenv("GITHUB_EVENT_NAME"),
		SHA:        getenv("GITHUB_SHA"),
		ServerURL:  strings.TrimRight(getenv("GITHUB_SERVER_URL"), "/"),
		RunID:      getenv("GITHUB_RUN_ID"),
		RunAttempt: getenv("GITHUB_RUN_ATTEMPT"),
		Actor:      getenv("GITHUB_ACTOR"),
	}
	if r.ServerURL == "" {
		r.ServerURL = "https://github.com"
	}
	for name, v := range map[string]string{
		"GITHUB_REPOSITORY": r.Repo,
		"GITHUB_WORKFLOW":   r.Workflow,
		"GITHUB_EVENT_NAME": r.EventName,
		"GITHUB_SHA":        r.SHA,
		"GITHUB_RUN_ID":     r.RunID,
	} {
		if v == "" {
			return Run{}, fmt.Errorf("%s is not set", name)
		}
	}
	if n := getenv("GITHUB_RUN_NUMBER"); n != "" {
		num, err := strconv.Atoi(n)
		if err != nil {
			return Run{}, fmt.Errorf("GITHUB_RUN_NUMBER: %w", err)
		}
		r.RunNumber = num
	}

	var ev event
	if path := getenv("GITHUB_EVENT_PATH"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Run{}, fmt.Errorf("reading event payload: %w", err)
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return Run{}, fmt.Errorf("parsing event payload: %w", err)
		}
	}
	r.DefaultBranch = ev.Repository.DefaultBranch
	if r.DefaultBranch == "" {
		r.DefaultBranch = "main"
	}

	if pr := ev.PullRequest; pr != nil {
		r.PRNumber = pr.Number
		r.PRTitle = pr.Title
		r.PRURL = pr.HTMLURL
		r.PRAuthor = pr.User.Login
		r.Branch = pr.Head.Ref
		// GITHUB_SHA is the test merge commit for pull_request; report the PR head instead,
		// so every run for one pushed commit shares a dedupe key.
		if pr.Head.SHA != "" {
			r.SHA = pr.Head.SHA
		}
	} else if ref, ok := strings.CutPrefix(getenv("GITHUB_REF"), "refs/heads/"); ok {
		r.Branch = ref
	}
	return r, nil
}

// IsPullRequest reports whether the run was triggered by a pull request event.
func (r Run) IsPullRequest() bool {
	return r.EventName == "pull_request" || r.EventName == "pull_request_target"
}

// RunURL links to the run's current attempt.
func (r Run) RunURL() string {
	u := fmt.Sprintf("%s/%s/actions/runs/%s", r.ServerURL, r.Repo, r.RunID)
	if r.RunAttempt != "" && r.RunAttempt != "1" {
		u += "/attempts/" + r.RunAttempt
	}
	return u
}

// PreviousConclusion returns the conclusion of the last completed run of the run's
// workflow on its branch before it, or "" if there is none.
type PreviousConclusion func() (string, error)

// Decide returns why r should be reported, or ReasonNone. It must only be called for a
// run that failed. previous is called only for a push to the default branch.
func Decide(r Run, previous PreviousConclusion) (Reason, error) {
	if r.IsPullRequest() {
		if r.PRAuthor == ReleaseBotLogin {
			return ReasonReleaseBotPR, nil
		}
		if isAutomationBranch(r.Branch) {
			return ReasonAutomationBranch, nil
		}
		return ReasonNone, nil
	}
	if r.EventName != "push" || r.Branch == "" {
		return ReasonNone, nil
	}
	if isAutomationBranch(r.Branch) {
		return ReasonAutomationBranch, nil
	}
	if r.Branch != r.DefaultBranch {
		return ReasonNone, nil
	}
	if previous == nil {
		return ReasonNone, errors.New("no lookup for the previous run")
	}
	conclusion, err := previous()
	if err != nil {
		return ReasonNone, fmt.Errorf("looking up the previous run on %s: %w", r.Branch, err)
	}
	if conclusion == "" || conclusion == "success" {
		return ReasonDefaultBranch, nil
	}
	return ReasonNone, nil
}

func isAutomationBranch(branch string) bool {
	for _, p := range AutomationBranchPrefixes {
		if strings.HasPrefix(branch, p) {
			return true
		}
	}
	return false
}

// DedupeKey is the cache key that marks a run's failure as already reported: one key per
// repo + commit + workflow, so re-runs and repeated events for a commit post once.
func DedupeKey(repo, sha, workflow string) string {
	sum := sha256.Sum256([]byte(repo + "\x00" + sha + "\x00" + workflow))
	return "ccf-notify-failure-" + hex.EncodeToString(sum[:])
}

// Message formats the Slack mrkdwn text for a reported run.
func Message(r Run, reason Reason) string {
	repoURL := r.ServerURL + "/" + r.Repo
	var b strings.Builder
	fmt.Fprintf(&b, ":rotating_light: *CI failed* in %s: *%s*\n",
		link(repoURL, r.Repo), escape(r.Workflow))

	switch reason {
	case ReasonReleaseBotPR:
		fmt.Fprintf(&b, "A pull request by %s failed.\n", escape(ReleaseBotLogin))
	case ReasonAutomationBranch:
		fmt.Fprintf(&b, "Automation branch `%s` failed.\n", escape(r.Branch))
	case ReasonDefaultBranch:
		fmt.Fprintf(&b, "`%s` was passing and is now failing.\n", escape(r.Branch))
	}

	if r.PRNumber != 0 {
		label := fmt.Sprintf("#%d", r.PRNumber)
		if r.PRTitle != "" {
			label += " " + r.PRTitle
		}
		url := r.PRURL
		if url == "" {
			url = fmt.Sprintf("%s/pull/%d", repoURL, r.PRNumber)
		}
		fmt.Fprintf(&b, "• Pull request: %s (`%s`)\n", link(url, label), escape(r.Branch))
	} else if r.Branch != "" {
		fmt.Fprintf(&b, "• Branch: `%s`\n", escape(r.Branch))
	}
	short := r.SHA
	if len(short) > 7 {
		short = short[:7]
	}
	fmt.Fprintf(&b, "• Commit: %s\n", link(repoURL+"/commit/"+r.SHA, short))
	runLabel := "run"
	if r.RunNumber != 0 {
		runLabel = fmt.Sprintf("#%d", r.RunNumber)
	}
	if r.RunAttempt != "" && r.RunAttempt != "1" {
		runLabel += " (attempt " + r.RunAttempt + ")"
	}
	fmt.Fprintf(&b, "• Run: %s", link(r.RunURL(), runLabel))
	if r.Actor != "" {
		fmt.Fprintf(&b, " by %s", escape(r.Actor))
	}
	return b.String()
}

// escape escapes the three characters Slack mrkdwn treats as control characters.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func link(url, text string) string {
	// "|" ends the URL part of a link, so it can't appear in the URL; text is escaped.
	return "<" + strings.ReplaceAll(escape(url), "|", "%7C") + "|" + escape(text) + ">"
}
