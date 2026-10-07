// Package notify holds the logic of the notify-failure reusable workflow (run through
// cmd/notify): whether a failed CI run is posted to Slack, its dedupe key, and the message.
//
// A failed run is posted when:
//   - (a) it is for a pull request opened by ReleaseBotLogin, or for a branch starting with
//     one of AutomationBranchPrefixes (a pull request, or a push to that branch);
//   - (b) it is for a push to the default branch, and the previous completed run of the
//     same workflow on that branch passed (or there is none).
package notify

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ReleaseBotLogin is the author of release PRs (release-please, ccf-bump).
const ReleaseBotLogin = "ccf-release-bot[bot]"

// AutomationBranchPrefixes are the branch name prefixes of dependency-bump branches.
var AutomationBranchPrefixes = []string{"renovate/", "ccf-bump/"}

// Reason says why a run is posted; ReasonNone means it is not.
type Reason string

const (
	ReasonNone             Reason = ""
	ReasonReleaseBotPR     Reason = "release-bot-pr"
	ReasonAutomationBranch Reason = "automation-branch"
	ReasonDefaultBranch    Reason = "default-branch-broken"
)

// ParseReason checks that s is a known, non-empty Reason.
func ParseReason(s string) (Reason, error) {
	switch r := Reason(s); r {
	case ReasonReleaseBotPR, ReasonAutomationBranch, ReasonDefaultBranch:
		return r, nil
	}
	return ReasonNone, fmt.Errorf("unknown reason %q", s)
}

// Run is the failed workflow run, from the caller's github context.
type Run struct {
	Repo          string // owner/name
	Workflow      string // the caller workflow's name
	WorkflowPath  string // its file, e.g. .github/workflows/ci.yml: unlike the name, unique in a repo
	EventName     string
	SHA           string // the commit under test: the PR head, or the pushed commit
	Branch        string // the PR head branch, or the pushed branch
	DefaultBranch string
	ServerURL     string
	RunID         string
	PRNumber      int
	PRTitle       string
	PRAuthor      string
}

// RunFromEnv builds a Run from a workflow step's GITHUB_* variables and the event payload
// at GITHUB_EVENT_PATH.
func RunFromEnv(getenv func(string) string) (Run, error) {
	r := Run{
		Repo:      getenv("GITHUB_REPOSITORY"),
		Workflow:  getenv("GITHUB_WORKFLOW"),
		EventName: getenv("GITHUB_EVENT_NAME"),
		SHA:       getenv("GITHUB_SHA"),
		ServerURL: strings.TrimRight(getenv("GITHUB_SERVER_URL"), "/"),
		RunID:     getenv("GITHUB_RUN_ID"),
	}
	for _, name := range []string{"GITHUB_REPOSITORY", "GITHUB_WORKFLOW", "GITHUB_EVENT_NAME", "GITHUB_SHA", "GITHUB_RUN_ID"} {
		if getenv(name) == "" {
			return Run{}, fmt.Errorf("%s is not set", name)
		}
	}
	if r.ServerURL == "" {
		r.ServerURL = "https://github.com"
	}
	// GITHUB_WORKFLOW_REF is "owner/repo/.github/workflows/ci.yml@refs/heads/main".
	path, _, _ := strings.Cut(getenv("GITHUB_WORKFLOW_REF"), "@")
	r.WorkflowPath = strings.TrimPrefix(path, r.Repo+"/")

	var ev struct {
		PullRequest *struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			User   struct {
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
	if path := getenv("GITHUB_EVENT_PATH"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Run{}, fmt.Errorf("reading the event payload: %w", err)
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return Run{}, fmt.Errorf("parsing the event payload: %w", err)
		}
	}
	r.DefaultBranch = ev.Repository.DefaultBranch
	if r.DefaultBranch == "" {
		r.DefaultBranch = "main"
	}
	if pr := ev.PullRequest; pr != nil {
		r.PRNumber, r.PRTitle, r.PRAuthor, r.Branch = pr.Number, pr.Title, pr.User.Login, pr.Head.Ref
		// GITHUB_SHA is the test merge commit, which changes whenever the base moves; the
		// head is the commit that was pushed, so every run for it shares a dedupe key.
		if pr.Head.SHA != "" {
			r.SHA = pr.Head.SHA
		}
	} else if ref, ok := strings.CutPrefix(getenv("GITHUB_REF"), "refs/heads/"); ok {
		r.Branch = ref
	}
	return r, nil
}

// Decide returns why the failed run r is posted, or ReasonNone. previous returns the
// conclusion of the previous run on the default branch ("" if none); it is called only
// for a push to that branch.
func Decide(r Run, previous func() (string, error)) (Reason, error) {
	automation := false
	for _, p := range AutomationBranchPrefixes {
		automation = automation || strings.HasPrefix(r.Branch, p)
	}
	switch {
	case r.EventName == "pull_request" || r.EventName == "pull_request_target":
		if r.PRAuthor == ReleaseBotLogin {
			return ReasonReleaseBotPR, nil
		}
		if automation {
			return ReasonAutomationBranch, nil
		}
	case r.EventName == "push" && automation:
		return ReasonAutomationBranch, nil
	case r.EventName == "push" && r.Branch == r.DefaultBranch:
		conclusion, err := previous()
		if err != nil {
			return ReasonNone, fmt.Errorf("looking up the previous run on %s: %w", r.Branch, err)
		}
		if conclusion == "" || conclusion == "success" {
			return ReasonDefaultBranch, nil
		}
	}
	return ReasonNone, nil
}

// DedupeKey is the cache key marking r's failure as posted: one per repo + commit +
// workflow (its file, or its name if the file is unknown), so re-runs and repeated events
// for a commit post once. Changing it re-posts failures already posted under the old key.
func DedupeKey(r Run) string {
	sum := sha256.Sum256([]byte(r.Repo + "\x00" + r.SHA + "\x00" + cmp.Or(r.WorkflowPath, r.Workflow)))
	return "ccf-notify-failure-" + hex.EncodeToString(sum[:])
}

// Message formats the Slack mrkdwn text for a posted run.
func Message(r Run, reason Reason) string {
	repoURL := r.ServerURL + "/" + r.Repo
	var b strings.Builder
	fmt.Fprintf(&b, ":rotating_light: *CI failed* in %s: *%s*\n", link(repoURL, r.Repo), escape(r.Workflow))
	switch reason {
	case ReasonReleaseBotPR:
		b.WriteString("A pull request by " + ReleaseBotLogin + " failed.\n")
	case ReasonAutomationBranch:
		b.WriteString("Automation branch `" + escape(r.Branch) + "` failed.\n")
	case ReasonDefaultBranch:
		b.WriteString("`" + escape(r.Branch) + "` was passing and is now failing.\n")
	}
	if r.PRNumber != 0 {
		label := strings.TrimSpace(fmt.Sprintf("#%d %s", r.PRNumber, r.PRTitle))
		fmt.Fprintf(&b, "Pull request %s · ", link(fmt.Sprintf("%s/pull/%d", repoURL, r.PRNumber), label))
	}
	short := r.SHA[:min(7, len(r.SHA))]
	fmt.Fprintf(&b, "commit %s · %s", link(repoURL+"/commit/"+r.SHA, short),
		link(repoURL+"/actions/runs/"+r.RunID, "run"))
	return b.String()
}

// escape escapes the characters Slack mrkdwn treats as control characters.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func link(url, text string) string {
	return "<" + escape(url) + "|" + escape(text) + ">"
}
