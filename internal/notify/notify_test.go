package notify

import (
	"errors"
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
		DefaultBranch: "trunk", ServerURL: "https://github.com", RunID: "42", PRNumber: 5, PRTitle: "t", PRAuthor: "renovate[bot]"}); pr != want {
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
	prev := func(c string) func() (string, error) { return func() (string, error) { return c, nil } }
	unused := func() (string, error) { return "", errors.New("lookup must not be called") }
	tests := []struct {
		name     string
		run      Run
		previous func() (string, error)
		want     Reason
		wantErr  bool
	}{
		{"PR by the release bot", Run{EventName: "pull_request", PRAuthor: ReleaseBotLogin, Branch: "release-please--branches--main"}, unused, ReasonReleaseBotPR, false},
		{"PR from renovate/", Run{EventName: "pull_request", PRAuthor: "renovate[bot]", Branch: "renovate/test"}, unused, ReasonAutomationBranch, false},
		{"pull_request_target from ccf-bump/", Run{EventName: "pull_request_target", Branch: "ccf-bump/api"}, unused, ReasonAutomationBranch, false},
		{"PR by a human", Run{EventName: "pull_request", PRAuthor: "octocat", Branch: "fix/renovate/x"}, unused, ReasonNone, false},
		{"push to renovate/", Run{EventName: "push", Branch: "renovate/test", DefaultBranch: "main"}, unused, ReasonAutomationBranch, false},
		{"main after a pass", Run{EventName: "push", Branch: "main", DefaultBranch: "main"}, prev("success"), ReasonDefaultBranch, false},
		{"main with no previous run", Run{EventName: "push", Branch: "main", DefaultBranch: "main"}, prev(""), ReasonDefaultBranch, false},
		{"main already failing", Run{EventName: "push", Branch: "main", DefaultBranch: "main"}, prev("failure"), ReasonNone, false},
		{"main after a timeout", Run{EventName: "push", Branch: "main", DefaultBranch: "main"}, prev("timed_out"), ReasonNone, false},
		{"push to another branch", Run{EventName: "push", Branch: "feature", DefaultBranch: "main"}, unused, ReasonNone, false},
		{"tag push", Run{EventName: "push", DefaultBranch: "main"}, unused, ReasonNone, false},
		{"schedule on main", Run{EventName: "schedule", Branch: "main", DefaultBranch: "main"}, unused, ReasonNone, false},
		{"lookup error", Run{EventName: "push", Branch: "main", DefaultBranch: "main"}, func() (string, error) { return "", errors.New("boom") }, ReasonNone, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decide(tt.run, tt.previous)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("Decide = %q, %v; want %q, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestDedupeKey(t *testing.T) {
	r := Run{Repo: "compliance-framework/mock-agent", SHA: "2222222222222222222222222222222222222222", Workflow: "ci", WorkflowPath: ".github/workflows/ci.yml"}
	// Pinned: a new key would re-post failures already posted under the old one.
	if got, want := DedupeKey(r), "ccf-notify-failure-28135599d46d14bf7d99c375c67d4634d3d018965f52784732bd87f33e09f39c"; got != want {
		t.Errorf("DedupeKey = %s, want %s", got, want)
	}
	seen := map[string]bool{}
	for _, k := range []Run{
		{Repo: "o/r", SHA: "sha", Workflow: "ci", WorkflowPath: "a.yml"},
		{Repo: "o/r", SHA: "sha", Workflow: "ci", WorkflowPath: "b.yml"}, // same name, another file
		{Repo: "o/r2", SHA: "sha", WorkflowPath: "a.yml"},
		{Repo: "o/r", SHA: "sha2", WorkflowPath: "a.yml"},
		{Repo: "o/rsha", WorkflowPath: "a.yml"}, // the separator keeps fields apart
		{Repo: "o/r", SHA: "sha", Workflow: "ci"},
	} {
		if key := DedupeKey(k); seen[key] {
			t.Errorf("collision for %+v", k)
		} else {
			seen[key] = true
		}
	}
}

func TestMessage(t *testing.T) {
	pr := Run{Repo: "o/r", Workflow: "ci <go>", ServerURL: "https://github.com", SHA: "2222222222", Branch: "renovate/x",
		RunID: "42", PRNumber: 12, PRTitle: "fix(deps): a & b"}
	want := ":rotating_light: *CI failed* in <https://github.com/o/r|o/r>: *ci &lt;go&gt;*\n" +
		"Automation branch `renovate/x` failed.\n" +
		"Pull request <https://github.com/o/r/pull/12|#12 fix(deps): a &amp; b> · " +
		"commit <https://github.com/o/r/commit/2222222222|2222222> · <https://github.com/o/r/actions/runs/42|run>"
	if got := Message(pr, ReasonAutomationBranch); got != want {
		t.Errorf("Message =\n%s\nwant\n%s", got, want)
	}

	push := Run{Repo: "o/r", Workflow: "ci", ServerURL: "https://github.com", SHA: "abc", Branch: "main", RunID: "43"}
	want = ":rotating_light: *CI failed* in <https://github.com/o/r|o/r>: *ci*\n" +
		"`main` was passing and is now failing.\n" +
		"commit <https://github.com/o/r/commit/abc|abc> · <https://github.com/o/r/actions/runs/43|run>"
	if got := Message(push, ReasonDefaultBranch); got != want {
		t.Errorf("Message =\n%s\nwant\n%s", got, want)
	}

	pr.PRTitle = ""
	want = ":rotating_light: *CI failed* in <https://github.com/o/r|o/r>: *ci &lt;go&gt;*\n" +
		"A pull request by ccf-release-bot[bot] failed.\n" +
		"Pull request <https://github.com/o/r/pull/12|#12> · " +
		"commit <https://github.com/o/r/commit/2222222222|2222222> · <https://github.com/o/r/actions/runs/42|run>"
	if got := Message(pr, ReasonReleaseBotPR); got != want {
		t.Errorf("Message =\n%s\nwant\n%s", got, want)
	}
}
