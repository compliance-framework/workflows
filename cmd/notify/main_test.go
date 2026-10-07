package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setup returns the step environment for a run, with fake GitHub and Slack servers, and
// the Slack messages they receive.
func setup(t *testing.T, eventName, ref, event string) (map[string]string, *[]map[string]any) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "event.json"), []byte(event), 0o600); err != nil {
		t.Fatal(err)
	}
	posts := &[]map[string]any{}
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*posts = append(*posts, body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/actions/runs/100":
			_, _ = w.Write([]byte(`{"id":100,"workflow_id":9,"run_number":10}`))
		case "/repos/o/r/actions/workflows/9/runs":
			_, _ = w.Write([]byte(`{"workflow_runs":[{"run_number":9,"conclusion":"success"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(slack.Close)
	t.Cleanup(gh.Close)
	return map[string]string{
		"GITHUB_REPOSITORY": "o/r", "GITHUB_WORKFLOW": "ci", "GITHUB_EVENT_NAME": eventName,
		"GITHUB_EVENT_PATH": filepath.Join(dir, "event.json"), "GITHUB_SHA": "1111111111", "GITHUB_REF": ref,
		"GITHUB_RUN_ID": "100", "GITHUB_OUTPUT": filepath.Join(dir, "output"), "GITHUB_API_URL": gh.URL,
		"GH_TOKEN": "gh-token", "SLACK_API_URL": slack.URL, "SLACK_BOT_TOKEN": "xoxb-test", "SLACK_CHANNEL": "C0CIFAIL",
	}, posts
}

func runCmd(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	err := run(context.Background(), args, func(k string) string { return env[k] }, &stdout)
	return stdout.String(), err
}

func outputs(t *testing.T, env map[string]string) string {
	t.Helper()
	data, err := os.ReadFile(env["GITHUB_OUTPUT"])
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const renovatePR = `{"pull_request":{"number":5,"user":{"login":"renovate[bot]"},"head":{"ref":"renovate/test","sha":"abcdef0123"}}}`

func TestRenovatePR(t *testing.T) {
	env, posts := setup(t, "pull_request", "refs/pull/5/merge", renovatePR)
	if _, err := runCmd(t, env, "plan"); err != nil {
		t.Fatal(err)
	}
	if out := outputs(t, env); !strings.HasPrefix(out, "notify=true\nreason=automation-branch\nkey=ccf-notify-failure-") {
		t.Fatalf("outputs = %q", out)
	}
	stdout, err := runCmd(t, env, "post", "--reason", "automation-branch")
	if err != nil {
		t.Fatal(err)
	}
	if len(*posts) != 1 || (*posts)[0]["channel"] != "C0CIFAIL" ||
		!strings.Contains((*posts)[0]["text"].(string), "Automation branch `renovate/test` failed.") ||
		!strings.Contains((*posts)[0]["text"].(string), "/commit/abcdef0123|abcdef0>") {
		t.Errorf("posts = %v", *posts)
	}
	if strings.Contains(stdout, "xoxb-test") {
		t.Error("the token was printed")
	}
}

func TestPlanDefaultBranch(t *testing.T) {
	env, _ := setup(t, "push", "refs/heads/main", `{"repository":{"default_branch":"main"}}`)
	if _, err := runCmd(t, env, "plan"); err != nil {
		t.Fatal(err)
	}
	if out := outputs(t, env); !strings.HasPrefix(out, "notify=true\nreason=default-branch-broken\n") {
		t.Errorf("outputs = %q", out)
	}
}

func TestPostWithoutTokenIsNoop(t *testing.T) {
	env, posts := setup(t, "pull_request", "refs/pull/5/merge", renovatePR)
	env["SLACK_BOT_TOKEN"] = ""
	if stdout, err := runCmd(t, env, "post", "--reason", "nonsense"); err != nil || len(*posts) != 0 || !strings.Contains(stdout, "nothing to post") {
		t.Errorf("stdout = %q, err = %v, posts = %v", stdout, err, *posts)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		unset   string
		wantErr string
	}{
		{"no command", nil, "", "usage"},
		{"plan without GITHUB_OUTPUT", []string{"plan"}, "GITHUB_OUTPUT", "GITHUB_OUTPUT"},
		{"post bad reason", []string{"post", "--reason", "nope"}, "", "unknown reason"},
		{"post without channel", []string{"post", "--reason", "automation-branch"}, "SLACK_CHANNEL", "SLACK_CHANNEL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _ := setup(t, "pull_request", "refs/pull/5/merge", renovatePR)
			delete(env, tt.unset)
			if _, err := runCmd(t, env, tt.args...); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
