package notify

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envFunc returns a getenv over m, writing event (if non-empty) to GITHUB_EVENT_PATH.
func envFunc(t *testing.T, m map[string]string, event string) func(string) string {
	t.Helper()
	if event != "" {
		p := filepath.Join(t.TempDir(), "event.json")
		if err := os.WriteFile(p, []byte(event), 0o600); err != nil {
			t.Fatal(err)
		}
		m["GITHUB_EVENT_PATH"] = p
	}
	return func(k string) string { return m[k] }
}

func baseEnv(event, ref string) map[string]string {
	return map[string]string{
		"GITHUB_REPOSITORY":  "compliance-framework/mock-agent",
		"GITHUB_WORKFLOW":    "ci",
		"GITHUB_EVENT_NAME":  event,
		"GITHUB_SHA":         "1111111111111111111111111111111111111111",
		"GITHUB_REF":         ref,
		"GITHUB_SERVER_URL":  "https://github.com",
		"GITHUB_RUN_ID":      "42",
		"GITHUB_RUN_NUMBER":  "7",
		"GITHUB_RUN_ATTEMPT": "1",
		"GITHUB_ACTOR":       "renovate[bot]",
	}
}

const prEvent = `{
  "pull_request": {
    "number": 12,
    "title": "fix(deps): update module x to v1.2.3",
    "html_url": "https://github.com/compliance-framework/mock-agent/pull/12",
    "user": {"login": "renovate[bot]"},
    "head": {"ref": "renovate/x", "sha": "2222222222222222222222222222222222222222"}
  },
  "repository": {"default_branch": "main"}
}`

func TestRunFromEnv(t *testing.T) {
	t.Run("pull request uses the head commit and branch", func(t *testing.T) {
		r, err := RunFromEnv(envFunc(t, baseEnv("pull_request", "refs/pull/12/merge"), prEvent))
		if err != nil {
			t.Fatal(err)
		}
		if r.SHA != "2222222222222222222222222222222222222222" || r.Branch != "renovate/x" ||
			r.PRNumber != 12 || r.PRAuthor != "renovate[bot]" || r.DefaultBranch != "main" || r.RunNumber != 7 {
			t.Errorf("unexpected run: %+v", r)
		}
	})
	t.Run("push uses GITHUB_SHA and the pushed branch", func(t *testing.T) {
		r, err := RunFromEnv(envFunc(t, baseEnv("push", "refs/heads/main"), `{"repository":{"default_branch":"trunk"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if r.SHA != "1111111111111111111111111111111111111111" || r.Branch != "main" || r.DefaultBranch != "trunk" || r.PRNumber != 0 {
			t.Errorf("unexpected run: %+v", r)
		}
	})
	t.Run("defaults without an event payload", func(t *testing.T) {
		env := baseEnv("push", "refs/tags/v1.0.0")
		delete(env, "GITHUB_SERVER_URL")
		r, err := RunFromEnv(envFunc(t, env, ""))
		if err != nil {
			t.Fatal(err)
		}
		if r.Branch != "" || r.DefaultBranch != "main" || r.ServerURL != "https://github.com" {
			t.Errorf("unexpected run: %+v", r)
		}
	})
	t.Run("missing required variable", func(t *testing.T) {
		env := baseEnv("push", "refs/heads/main")
		delete(env, "GITHUB_RUN_ID")
		if _, err := RunFromEnv(envFunc(t, env, "")); err == nil || !strings.Contains(err.Error(), "GITHUB_RUN_ID") {
			t.Errorf("err = %v, want GITHUB_RUN_ID not set", err)
		}
	})
	t.Run("bad event payload", func(t *testing.T) {
		if _, err := RunFromEnv(envFunc(t, baseEnv("push", "refs/heads/main"), "{")); err == nil {
			t.Error("want a parse error")
		}
	})
}

func TestDecide(t *testing.T) {
	prev := func(c string) PreviousConclusion {
		return func() (string, error) { return c, nil }
	}
	noLookup := func() (string, error) {
		return "", errors.New("lookup must not be called")
	}
	tests := []struct {
		name     string
		run      Run
		previous PreviousConclusion
		want     Reason
		wantErr  bool
	}{
		{
			name:     "PR by the release bot",
			run:      Run{EventName: "pull_request", PRAuthor: ReleaseBotLogin, Branch: "release-please--branches--main"},
			previous: noLookup,
			want:     ReasonReleaseBotPR,
		},
		{
			name:     "PR from a renovate branch",
			run:      Run{EventName: "pull_request", PRAuthor: "renovate[bot]", Branch: "renovate/test"},
			previous: noLookup,
			want:     ReasonAutomationBranch,
		},
		{
			name:     "pull_request_target from a ccf-bump branch",
			run:      Run{EventName: "pull_request_target", PRAuthor: "someone", Branch: "ccf-bump/api-v1.2.3"},
			previous: noLookup,
			want:     ReasonAutomationBranch,
		},
		{
			name:     "PR by a human from another branch",
			run:      Run{EventName: "pull_request", PRAuthor: "octocat", Branch: "feature/renovate/x"},
			previous: noLookup,
			want:     ReasonNone,
		},
		{
			name:     "push to a renovate branch",
			run:      Run{EventName: "push", Branch: "renovate/test", DefaultBranch: "main"},
			previous: noLookup,
			want:     ReasonAutomationBranch,
		},
		{
			name:     "push to main after a passing run",
			run:      Run{EventName: "push", Branch: "main", DefaultBranch: "main"},
			previous: prev("success"),
			want:     ReasonDefaultBranch,
		},
		{
			name:     "push to main with no previous run",
			run:      Run{EventName: "push", Branch: "main", DefaultBranch: "main"},
			previous: prev(""),
			want:     ReasonDefaultBranch,
		},
		{
			name:     "push to main that was already failing",
			run:      Run{EventName: "push", Branch: "main", DefaultBranch: "main"},
			previous: prev("failure"),
			want:     ReasonNone,
		},
		{
			name:     "push to main after a timeout",
			run:      Run{EventName: "push", Branch: "main", DefaultBranch: "main"},
			previous: prev("timed_out"),
			want:     ReasonNone,
		},
		{
			name:     "push to another branch",
			run:      Run{EventName: "push", Branch: "feature", DefaultBranch: "main"},
			previous: noLookup,
			want:     ReasonNone,
		},
		{
			name:     "tag push",
			run:      Run{EventName: "push", Branch: "", DefaultBranch: "main"},
			previous: noLookup,
			want:     ReasonNone,
		},
		{
			name:     "scheduled run on main",
			run:      Run{EventName: "schedule", Branch: "main", DefaultBranch: "main"},
			previous: noLookup,
			want:     ReasonNone,
		},
		{
			name:     "lookup error",
			run:      Run{EventName: "push", Branch: "main", DefaultBranch: "main"},
			previous: func() (string, error) { return "", errors.New("boom") },
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decide(tt.run, tt.previous)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Decide = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDedupeKey(t *testing.T) {
	const (
		repo = "compliance-framework/mock-agent"
		sha  = "2222222222222222222222222222222222222222"
	)
	key := DedupeKey(repo, sha, "ci")
	// Pinned: changing the key would re-post failures already reported under the old one.
	if want := "ccf-notify-failure-9870162b5c5a718031a46b4dd649a2c359caf551c817147ed9984dbac194606c"; key != want {
		t.Errorf("DedupeKey = %s, want %s", key, want)
	}
	if key != DedupeKey(repo, sha, "ci") {
		t.Error("key is not stable")
	}
	for _, other := range []string{
		DedupeKey("compliance-framework/mock-api", sha, "ci"),
		DedupeKey(repo, "3333333333333333333333333333333333333333", "ci"),
		DedupeKey(repo, sha, "release"),
		// The separator keeps field boundaries: these must not collide.
		DedupeKey(repo+sha, "", "ci"),
	} {
		if other == key {
			t.Errorf("key collision: %s", other)
		}
	}
}

func TestMessage(t *testing.T) {
	pr := Run{
		Repo: "compliance-framework/mock-agent", Workflow: "ci <go>", ServerURL: "https://github.com",
		SHA: "2222222222222222222222222222222222222222", Branch: "renovate/x", RunID: "42", RunNumber: 7,
		RunAttempt: "2", Actor: "renovate[bot]", PRNumber: 12, PRTitle: "fix(deps): a & b",
		PRURL: "https://github.com/compliance-framework/mock-agent/pull/12",
	}
	want := ":rotating_light: *CI failed* in <https://github.com/compliance-framework/mock-agent|compliance-framework/mock-agent>: *ci &lt;go&gt;*\n" +
		"Automation branch `renovate/x` failed.\n" +
		"• Pull request: <https://github.com/compliance-framework/mock-agent/pull/12|#12 fix(deps): a &amp; b> (`renovate/x`)\n" +
		"• Commit: <https://github.com/compliance-framework/mock-agent/commit/2222222222222222222222222222222222222222|2222222>\n" +
		"• Run: <https://github.com/compliance-framework/mock-agent/actions/runs/42/attempts/2|#7 (attempt 2)> by renovate[bot]"
	if got := Message(pr, ReasonAutomationBranch); got != want {
		t.Errorf("Message =\n%s\nwant\n%s", got, want)
	}

	push := Run{
		Repo: "compliance-framework/mock-agent", Workflow: "ci", ServerURL: "https://github.com",
		SHA: "1111111111111111111111111111111111111111", Branch: "main", RunID: "43", RunAttempt: "1",
	}
	want = ":rotating_light: *CI failed* in <https://github.com/compliance-framework/mock-agent|compliance-framework/mock-agent>: *ci*\n" +
		"`main` was passing and is now failing.\n" +
		"• Branch: `main`\n" +
		"• Commit: <https://github.com/compliance-framework/mock-agent/commit/1111111111111111111111111111111111111111|1111111>\n" +
		"• Run: <https://github.com/compliance-framework/mock-agent/actions/runs/43|run>"
	if got := Message(push, ReasonDefaultBranch); got != want {
		t.Errorf("Message =\n%s\nwant\n%s", got, want)
	}

	bot := pr
	bot.PRURL = ""
	if got := Message(bot, ReasonReleaseBotPR); !strings.Contains(got, "A pull request by ccf-release-bot[bot] failed.\n") ||
		!strings.Contains(got, "<https://github.com/compliance-framework/mock-agent/pull/12|") {
		t.Errorf("release bot message = %s", got)
	}
}

func TestParseReason(t *testing.T) {
	for _, r := range []Reason{ReasonReleaseBotPR, ReasonAutomationBranch, ReasonDefaultBranch} {
		if got, err := ParseReason(string(r)); err != nil || got != r {
			t.Errorf("ParseReason(%q) = %q, %v", r, got, err)
		}
	}
	for _, s := range []string{"", "nope"} {
		if _, err := ParseReason(s); err == nil {
			t.Errorf("ParseReason(%q): want error", s)
		}
	}
}
