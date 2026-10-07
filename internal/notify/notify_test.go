package notify

import (
	"os"
	"path/filepath"
	"testing"
)

func getenv(t *testing.T, eventName, ref, event string) func(string) string {
	t.Helper()
	m := map[string]string{
		"GITHUB_REPOSITORY": "o/r", "GITHUB_WORKFLOW": "ci", "GITHUB_EVENT_NAME": eventName,
		"GITHUB_SHA": "1111111111", "GITHUB_REF": ref, "GITHUB_RUN_ID": "42",
		"GITHUB_WORKFLOW_REF": "o/r/.github/workflows/ci.yml@" + ref,
	}
	if event != "" {
		m["GITHUB_EVENT_PATH"] = filepath.Join(t.TempDir(), "event.json")
		if err := os.WriteFile(m["GITHUB_EVENT_PATH"], []byte(event), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return func(k string) string { return m[k] }
}

func TestRunFromEnv(t *testing.T) {
	pr, err := RunFromEnv(getenv(t, "pull_request", "refs/pull/5/merge",
		`{"pull_request":{"number":5,"title":"t","user":{"login":"renovate[bot]"},"head":{"ref":"renovate/x","sha":"2222222222"}},"repository":{"default_branch":"trunk"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Run{Repo: "o/r", Workflow: "ci", WorkflowPath: ".github/workflows/ci.yml", EventName: "pull_request", SHA: "2222222222", Branch: "renovate/x",
		DefaultBranch: "trunk", ServerURL: "https://github.com", RunID: "42", RunAttempt: "1", PRNumber: 5, PRTitle: "t", PRAuthor: "renovate[bot]"}); pr != want {
		t.Errorf("PR run = %+v, want %+v", pr, want)
	}

	push, err := RunFromEnv(getenv(t, "push", "refs/heads/main", ""))
	if err != nil {
		t.Fatal(err)
	}
	if push.SHA != "1111111111" || push.Branch != "main" || push.DefaultBranch != "main" || push.PRNumber != 0 {
		t.Errorf("push run = %+v", push)
	}

	if _, err := RunFromEnv(func(string) string { return "" }); err == nil {
		t.Error("want an error without GITHUB_* variables")
	}
	if _, err := RunFromEnv(getenv(t, "push", "refs/heads/main", "{")); err == nil {
		t.Error("want an error for a bad event payload")
	}
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name string
		run  Run
		want Reason
	}{
		{"PR by the release bot", Run{EventName: "pull_request", PRAuthor: ReleaseBotLogin, Branch: "release-please--branches--main"}, ReasonReleaseBotPR},
		{"PR from renovate/", Run{EventName: "pull_request", PRAuthor: "renovate[bot]", Branch: "renovate/test"}, ReasonAutomationBranch},
		{"pull_request_target from ccf-bump/", Run{EventName: "pull_request_target", Branch: "ccf-bump/api"}, ReasonAutomationBranch},
		{"PR by a human", Run{EventName: "pull_request", PRAuthor: "octocat", Branch: "fix/renovate/x"}, ReasonNone},
		{"PR by a human into main", Run{EventName: "pull_request", PRAuthor: "octocat", Branch: "main", DefaultBranch: "main"}, ReasonNone},
		{"push to renovate/", Run{EventName: "push", Branch: "renovate/test", DefaultBranch: "main"}, ReasonAutomationBranch},
		{"push to main", Run{EventName: "push", Branch: "main", DefaultBranch: "main"}, ReasonDefaultBranch},
		{"push to another branch", Run{EventName: "push", Branch: "feature", DefaultBranch: "main"}, ReasonNone},
		{"tag push", Run{EventName: "push", DefaultBranch: "main"}, ReasonNone},
		{"schedule on main", Run{EventName: "schedule", Branch: "main", DefaultBranch: "main"}, ReasonNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.run); got != tt.want {
				t.Errorf("Decide = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIncidentKey(t *testing.T) {
	pr := Run{Repo: "compliance-framework/mock-agent", WorkflowPath: ".github/workflows/ci.yml", PRNumber: 12, Branch: "renovate/x", SHA: "a", RunID: "7", RunAttempt: "2"}
	// Pinned: a new key forgets every open incident.
	if got, want := IncidentKey(pr), "ccf-notify-incident-17f485cd39eb5d65d1013e7a0796ae97-"; got != want {
		t.Errorf("IncidentKey = %s, want %s", got, want)
	}
	if got, want := StateKey(pr), IncidentKey(pr)+"7-2"; got != want {
		t.Errorf("StateKey = %s, want %s", got, want)
	}
	// The commit, run and PR head branch don't change a PR's incident.
	later := pr
	later.SHA, later.RunID, later.Branch = "b", "8", "renovate/y"
	if IncidentKey(later) != IncidentKey(pr) {
		t.Error("a later run of the same PR has another incident")
	}

	seen := map[string]bool{}
	for _, r := range []Run{
		{Repo: "o/r", WorkflowPath: "a.yml", PRNumber: 1},
		{Repo: "o/r", WorkflowPath: "a.yml", PRNumber: 2},
		{Repo: "o/r", WorkflowPath: "b.yml", PRNumber: 1},    // another workflow file
		{Repo: "o/r2", WorkflowPath: "a.yml", PRNumber: 1},   // another repo
		{Repo: "o/r", WorkflowPath: "a.yml", Branch: "main"}, // a push
		{Repo: "o/r", WorkflowPath: "a.yml", Branch: "1"},    // a branch named like a PR number
		{Repo: "o/r", WorkflowPath: "a.yml", Branch: "renovate/x"},
		{Repo: "o/r", Workflow: "ci", Branch: "main"},           // no workflow file: by name
		{Repo: "o/r", WorkflowPath: "a.yml\x00pr", Branch: "1"}, // the separator keeps fields apart
	} {
		if key := IncidentKey(r); seen[key] {
			t.Errorf("collision for %+v", r)
		} else {
			seen[key] = true
		}
	}
}
