// Package notify holds the logic of the notify-failure reusable workflow (run through
// cmd/notify): which CI runs it tracks, their incident key, the incident transitions and
// the Slack messages.
//
// A run is tracked when:
//   - (a) it is for a pull request opened by ReleaseBotLogin, or for a branch starting with
//     one of AutomationBranchPrefixes (a pull request, or a push to that branch);
//   - (b) it is for a push to the default branch.
//
// A failed tracked run opens an incident (a top-level Slack message) or, while one is open,
// replies in its thread; a passing run closes it with a reply. See Handle.
package notify

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ReleaseBotLogin is the author of release PRs (release-please, ccf-bump).
const ReleaseBotLogin = "ccf-release-bot[bot]"

// AutomationBranchPrefixes are the branch name prefixes of dependency-bump branches.
var AutomationBranchPrefixes = []string{"renovate/", "ccf-bump/"}

// Reason says why a run is tracked; ReasonNone means it is not.
type Reason string

const (
	ReasonNone             Reason = ""
	ReasonReleaseBotPR     Reason = "release-bot-pr"
	ReasonAutomationBranch Reason = "automation-branch"
	ReasonDefaultBranch    Reason = "default-branch"
)

// Run is the workflow run, from the caller's github context.
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
	RunAttempt    string
	PRNumber      int
	PRTitle       string
	PRAuthor      string
}

// RunFromEnv builds a Run from a workflow step's GITHUB_* variables and the event payload
// at GITHUB_EVENT_PATH.
func RunFromEnv(getenv func(string) string) (Run, error) {
	r := Run{
		Repo:       getenv("GITHUB_REPOSITORY"),
		Workflow:   getenv("GITHUB_WORKFLOW"),
		EventName:  getenv("GITHUB_EVENT_NAME"),
		SHA:        getenv("GITHUB_SHA"),
		ServerURL:  strings.TrimRight(getenv("GITHUB_SERVER_URL"), "/"),
		RunID:      getenv("GITHUB_RUN_ID"),
		RunAttempt: cmp.Or(getenv("GITHUB_RUN_ATTEMPT"), "1"),
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
		// head is the commit that was pushed, so every run for it sees the same commit.
		if pr.Head.SHA != "" {
			r.SHA = pr.Head.SHA
		}
	} else if ref, ok := strings.CutPrefix(getenv("GITHUB_REF"), "refs/heads/"); ok {
		r.Branch = ref
	}
	return r, nil
}

// Decide returns why the run r is tracked, or ReasonNone.
func Decide(r Run) Reason {
	automation := false
	for _, p := range AutomationBranchPrefixes {
		automation = automation || strings.HasPrefix(r.Branch, p)
	}
	switch {
	case r.EventName == "pull_request" || r.EventName == "pull_request_target":
		if r.PRAuthor == ReleaseBotLogin {
			return ReasonReleaseBotPR
		}
		if automation {
			return ReasonAutomationBranch
		}
	case r.EventName == "push" && automation:
		return ReasonAutomationBranch
	case r.EventName == "push" && r.Branch != "" && r.Branch == r.DefaultBranch:
		return ReasonDefaultBranch
	}
	return ReasonNone
}

// IncidentKey is the Actions cache key prefix of r's incident state: one incident per
// repo + workflow (its file, or its name if the file is unknown) + pull request, or + branch
// for a push. Each save appends a unique suffix (StateKey), and the newest entry with this
// prefix is the current state. Changing it forgets every open incident.
func IncidentKey(r Run) string {
	scope := "branch\x00" + r.Branch
	if r.PRNumber != 0 {
		scope = "pr\x00" + strconv.Itoa(r.PRNumber)
	}
	sum := sha256.Sum256([]byte(r.Repo + "\x00" + cmp.Or(r.WorkflowPath, r.Workflow) + "\x00" + scope))
	return "ccf-notify-incident-" + hex.EncodeToString(sum[:16]) + "-"
}

// StateKey is the cache key this run saves its incident state under: IncidentKey plus the
// run ID and attempt, so every save is a new entry.
func StateKey(r Run) string {
	return IncidentKey(r) + r.RunID + "-" + r.RunAttempt
}

// escape escapes the characters Slack mrkdwn treats as control characters.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func link(url, text string) string {
	return "<" + escape(url) + "|" + escape(text) + ">"
}
