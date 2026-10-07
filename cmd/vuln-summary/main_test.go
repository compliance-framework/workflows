package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// setup returns the step environment with fake GitHub and Slack servers, and the Slack messages
// they receive. mock-api has two alerts, mock-ui has alerts disabled, the rest have none.
func setup(t *testing.T) (map[string]string, *[]map[string]any) {
	t.Helper()
	posts := &[]map[string]any{}
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" || r.Header.Get("Authorization") != "Bearer xoxb-test" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*posts = append(*posts, body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/compliance-framework/mock-api/dependabot/alerts":
			_, _ = w.Write([]byte(`[{"security_advisory":{"severity":"critical"}},{"security_advisory":{"severity":"high"}}]`))
		case "/repos/compliance-framework/mock-ui/dependabot/alerts":
			http.Error(w, `{"message":"Dependabot alerts are disabled for this repository."}`, http.StatusForbidden)
		default:
			if strings.HasSuffix(r.URL.Path, "/dependabot/alerts") {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(slack.Close)
	t.Cleanup(gh.Close)
	return map[string]string{
		"GITHUB_API_URL": gh.URL, "GH_TOKEN": "gh-token", "GITHUB_SERVER_URL": "https://github.com",
		"GITHUB_REPOSITORY": "compliance-framework/workflows", "GITHUB_RUN_ID": "7",
		"SLACK_API_URL": slack.URL, "SLACK_BOT_TOKEN": "xoxb-test", "SLACK_CHANNEL": "C0VULNS",
	}, posts
}

func runCmd(env map[string]string, args ...string) (string, error) {
	var stdout bytes.Buffer
	err := run(context.Background(), args, func(k string) string { return env[k] }, &stdout)
	return stdout.String(), err
}

func TestList(t *testing.T) {
	out, err := runCmd(nil, "list", "--manifest", "../../repos.mock.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "mock-api,mock-gooci,mock-agent,") || strings.Count(out, ",") != 9 {
		t.Errorf("list = %q", out)
	}
	out, err = runCmd(nil, "list", "--manifest", "../../repos.mock.yaml", "--repos", "mock-ui mock-api")
	if err != nil || out != "mock-api,mock-ui\n" {
		t.Errorf("list subset = %q, %v", out, err)
	}
}

func TestPost(t *testing.T) {
	env, posts := setup(t)
	out, err := runCmd(env, "post", "--manifest", "../../repos.mock.yaml", "--repos", "mock-api,mock-gooci")
	if err != nil {
		t.Fatal(err)
	}
	if len(*posts) != 1 || (*posts)[0]["channel"] != "C0VULNS" {
		t.Fatalf("posts = %v", *posts)
	}
	text, _ := (*posts)[0]["text"].(string)
	for _, want := range []string{
		"*2 open Dependabot alerts* in compliance-framework (2 repo(s) in ../../repos.mock.yaml): 1 critical, 1 high",
		"<https://github.com/compliance-framework/mock-api/security/dependabot|mock-api>: 2 (1 critical, 1 high)",
		"1 other repo has no open alerts.",
		"<https://github.com/compliance-framework/workflows/actions/runs/7|run>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(out, text) || !strings.Contains(out, "posted to C0VULNS") {
		t.Errorf("stdout = %q", out)
	}
	if strings.Contains(out, "xoxb-test") || strings.Contains(out, "gh-token") {
		t.Errorf("stdout leaks a token: %q", out)
	}
}

func TestPostFailsAfterPostingWhenARepoCantBeRead(t *testing.T) {
	env, posts := setup(t)
	_, err := runCmd(env, "post", "--manifest", "../../repos.mock.yaml", "--repos", "mock-api,mock-ui")
	if err == nil || !strings.Contains(err.Error(), "mock-ui") {
		t.Fatalf("err = %v", err)
	}
	if len(*posts) != 1 {
		t.Fatalf("posts = %v", *posts)
	}
	if text, _ := (*posts)[0]["text"].(string); !strings.Contains(text, "Could not read 1 repo") || !strings.Contains(text, "disabled") {
		t.Errorf("text = %s", text)
	}
}

func TestPostWithoutSlackTokenOnlyPrints(t *testing.T) {
	env, posts := setup(t)
	delete(env, "SLACK_BOT_TOKEN")
	delete(env, "SLACK_CHANNEL")
	out, err := runCmd(env, "post", "--manifest", "../../repos.mock.yaml", "--repos", "mock-api")
	if err != nil {
		t.Fatal(err)
	}
	if len(*posts) != 0 || !strings.Contains(out, "nothing posted") || !strings.Contains(out, "mock-api") {
		t.Errorf("posts = %v, stdout = %q", *posts, out)
	}
}

func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		env  map[string]string
		err  string
	}{
		{args: nil, err: "usage"},
		{args: []string{"sync"}, err: "usage"},
		{args: []string{"list", "--manifest", "../../repos.mock.yaml", "extra"}, err: "unexpected arguments"},
		{args: []string{"list", "--manifest", "../../repos.mock.yaml", "--repos", "api"}, err: "not in the manifest: api"},
		{args: []string{"post", "--manifest", "../../repos.mock.yaml"}, err: "GH_TOKEN"},
		{args: []string{"post", "--manifest", "../../repos.mock.yaml", "--repos", "mock-gooci"},
			env: map[string]string{"SLACK_CHANNEL": ""}, err: "SLACK_CHANNEL is empty"},
	} {
		env := map[string]string{}
		if tc.env != nil {
			env, _ = setup(t)
			for k, v := range tc.env {
				env[k] = v
			}
		}
		if _, err := runCmd(env, tc.args...); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.err)
		}
	}
}
