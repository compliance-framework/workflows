package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const renovatePR = `{
  "pull_request": {
    "number": 5,
    "title": "fix(deps): update x",
    "html_url": "https://github.com/compliance-framework/mock-agent/pull/5",
    "user": {"login": "renovate[bot]"},
    "head": {"ref": "renovate/test", "sha": "abcdef0123456789abcdef0123456789abcdef01"}
  },
  "repository": {"default_branch": "main"}
}`

type harness struct {
	env    map[string]string
	output string
	posts  []map[string]any
	ghHits int
}

func newHarness(t *testing.T, eventName, ref, event string) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{output: filepath.Join(dir, "output")}
	eventPath := filepath.Join(dir, "event.json")
	if err := os.WriteFile(eventPath, []byte(event), 0o600); err != nil {
		t.Fatal(err)
	}

	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.posts = append(h.posts, body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(slack.Close)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ghHits++
		switch r.URL.Path {
		case "/repos/compliance-framework/mock-agent/actions/runs/100":
			_, _ = w.Write([]byte(`{"id":100,"workflow_id":9,"run_number":10}`))
		case "/repos/compliance-framework/mock-agent/actions/workflows/9/runs":
			_, _ = w.Write([]byte(`{"workflow_runs":[{"id":99,"run_number":9,"conclusion":"success"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gh.Close)

	h.env = map[string]string{
		"GITHUB_REPOSITORY":  "compliance-framework/mock-agent",
		"GITHUB_WORKFLOW":    "ci",
		"GITHUB_EVENT_NAME":  eventName,
		"GITHUB_EVENT_PATH":  eventPath,
		"GITHUB_SHA":         "1111111111111111111111111111111111111111",
		"GITHUB_REF":         ref,
		"GITHUB_RUN_ID":      "100",
		"GITHUB_RUN_NUMBER":  "10",
		"GITHUB_RUN_ATTEMPT": "1",
		"GITHUB_OUTPUT":      h.output,
		"GITHUB_API_URL":     gh.URL,
		"GH_TOKEN":           "gh-token",
		"SLACK_API_URL":      slack.URL,
		"SLACK_BOT_TOKEN":    "xoxb-test",
		"SLACK_CHANNEL":      "C0CIFAIL",
	}
	return h
}

func (h *harness) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	err := run(context.Background(), args, func(k string) string { return h.env[k] }, &stdout, io.Discard)
	return stdout.String(), err
}

func (h *harness) outputs(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(h.output)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPlanAndPostRenovatePR(t *testing.T) {
	h := newHarness(t, "pull_request", "refs/pull/5/merge", renovatePR)
	if _, err := h.run(t, "plan"); err != nil {
		t.Fatal(err)
	}
	out := h.outputs(t)
	if !strings.Contains(out, "notify=true\n") || !strings.Contains(out, "reason=automation-branch\n") ||
		!strings.Contains(out, "key=ccf-notify-failure-") {
		t.Errorf("outputs = %q", out)
	}
	if h.ghHits != 0 {
		t.Errorf("PR decision called the GitHub API %d times", h.ghHits)
	}

	stdout, err := h.run(t, "post", "--reason", "automation-branch")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "posted to C0CIFAIL") {
		t.Errorf("stdout = %q", stdout)
	}
	if len(h.posts) != 1 || h.posts[0]["channel"] != "C0CIFAIL" ||
		!strings.Contains(h.posts[0]["text"].(string), "Automation branch `renovate/test` failed.") ||
		!strings.Contains(h.posts[0]["text"].(string), "/commit/abcdef0123456789abcdef0123456789abcdef01|abcdef0>") {
		t.Errorf("posts = %v", h.posts)
	}
	if strings.Contains(stdout, "xoxb-test") {
		t.Error("the token was printed")
	}
}

func TestPlanDefaultBranch(t *testing.T) {
	h := newHarness(t, "push", "refs/heads/main", `{"repository":{"default_branch":"main"}}`)
	if _, err := h.run(t, "plan"); err != nil {
		t.Fatal(err)
	}
	if out := h.outputs(t); !strings.Contains(out, "notify=true\nreason=default-branch-broken\n") {
		t.Errorf("outputs = %q", out)
	}
	if h.ghHits != 2 {
		t.Errorf("GitHub API hits = %d, want 2", h.ghHits)
	}
}

func TestPlanNotReported(t *testing.T) {
	h := newHarness(t, "push", "refs/heads/feature", `{"repository":{"default_branch":"main"}}`)
	stdout, err := h.run(t, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if out := h.outputs(t); !strings.HasPrefix(out, "notify=false\nreason=\nkey=ccf-notify-failure-") {
		t.Errorf("outputs = %q", out)
	}
	if !strings.Contains(stdout, "not reporting") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestPostWithoutTokenIsNoop(t *testing.T) {
	h := newHarness(t, "pull_request", "refs/pull/5/merge", renovatePR)
	h.env["SLACK_BOT_TOKEN"] = ""
	stdout, err := h.run(t, "post", "--reason", "automation-branch")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.posts) != 0 || !strings.Contains(stdout, "nothing to post") {
		t.Errorf("posts = %v, stdout = %q", h.posts, stdout)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		mutate  func(map[string]string)
		wantErr string
	}{
		{name: "no command", wantErr: "usage"},
		{name: "unknown command", args: []string{"nope"}, wantErr: "unknown command"},
		{name: "plan extra args", args: []string{"plan", "x"}, wantErr: "unexpected arguments"},
		{name: "plan without GITHUB_OUTPUT", args: []string{"plan"}, mutate: func(e map[string]string) { e["GITHUB_OUTPUT"] = "" }, wantErr: "GITHUB_OUTPUT"},
		{name: "post bad reason", args: []string{"post", "--reason", "nope"}, wantErr: "unknown reason"},
		{name: "post without channel", args: []string{"post", "--reason", "automation-branch"}, mutate: func(e map[string]string) { e["SLACK_CHANNEL"] = "" }, wantErr: "SLACK_CHANNEL"},
		{name: "post Slack unreachable", args: []string{"post", "--reason", "automation-branch", "--slack-api-url", "http://127.0.0.1:1"}, wantErr: "chat.postMessage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, "pull_request", "refs/pull/5/merge", renovatePR)
			if tt.mutate != nil {
				tt.mutate(h.env)
			}
			_, err := h.run(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
