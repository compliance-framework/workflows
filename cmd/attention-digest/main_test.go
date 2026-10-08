package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)

// setup returns the step environment with fake GitHub and Slack servers, and the Slack messages
// they receive. mock-ui has a labelled Renovate major; prs overrides a repo's open PRs (JSON).
func setup(t *testing.T, prs map[string]string) (map[string]string, *[]map[string]any) {
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
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C0HUMAN","ts":"1.1"}`))
	}))
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("a write: %s %s", r.Method, r.URL.Path)
		}
		parts := strings.Split(r.URL.Path, "/") // /repos/compliance-framework/<repo>/...
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			if body, ok := prs[parts[3]]; ok {
				fmt.Fprint(w, body)
				return
			}
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			fmt.Fprint(w, `{"check_runs":[{"status":"completed","conclusion":"success"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(slack.Close)
	t.Cleanup(gh.Close)
	return map[string]string{
		"GITHUB_API_URL": gh.URL, "GH_TOKEN": "gh-token", "GITHUB_SERVER_URL": "https://github.com",
		"GITHUB_REPOSITORY": "compliance-framework/workflows", "GITHUB_RUN_ID": "7",
		"SLACK_API_URL": slack.URL, "SLACK_BOT_TOKEN": "xoxb-test", "SLACK_CHANNEL": "C0HUMAN",
	}, posts
}

const majorPR = `[{"number":13,"title":"chore(deps): update typescript to v7","html_url":"https://github.com/compliance-framework/mock-ui/pull/13",
	"created_at":"2026-10-03T08:00:00Z","user":{"login":"ccf-release-bot[bot]"},"head":{"ref":"renovate/typescript-7.x","sha":"h"},
	"base":{"sha":"b"},"labels":[{"name":"needs-human"}]}]`

func runCmd(env map[string]string, args ...string) (string, error) {
	var stdout bytes.Buffer
	err := run(context.Background(), args, func(k string) string { return env[k] }, &stdout, func() time.Time { return now })
	return stdout.String(), err
}

func TestList(t *testing.T) {
	out, err := runCmd(nil, "list", "--manifest", "../../repos.mock.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "mock-api,") || !strings.HasSuffix(out, ",workflows\n") || strings.Count(out, ",") != 10 {
		t.Errorf("list = %q, want every mock repo and workflows", out)
	}
	if out, err := runCmd(nil, "list", "--manifest", "../../repos.mock.yaml", "--repos", "mock-ui, workflows"); err != nil || out != "mock-ui,workflows\n" {
		t.Errorf("list --repos = %q, %v", out, err)
	}
}

func TestPost(t *testing.T) {
	env, posts := setup(t, map[string]string{"mock-ui": majorPR})
	out, err := runCmd(env, "post", "--manifest", "../../repos.mock.yaml")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := "1 PR needs a human: compliance-framework/mock-ui#13"
	line := "• <https://github.com/compliance-framework/mock-ui/pull/13|compliance-framework/mock-ui#13> " +
		"chore(deps): update typescript to v7 · labelled needs-human, open over 7d · 9d"
	if len(*posts) != 1 || (*posts)[0]["channel"] != "C0HUMAN" || (*posts)[0]["text"] != want ||
		!strings.Contains(fmt.Sprint((*posts)[0]["attachments"]), line) || !strings.Contains(fmt.Sprint((*posts)[0]["blocks"]), "1 PR needs a human") {
		t.Fatalf("posts = %v\nwant %q with %q", *posts, want, line)
	}
	if !strings.Contains(out, line) {
		t.Errorf("the log lacks the digest:\n%s", out)
	}
	if strings.Contains(out, "gh-token") || strings.Contains(out, "xoxb-test") {
		t.Error("a token was printed")
	}
}

func TestPostNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		prs  map[string]string
		args []string
		env  map[string]string
		want string
	}{
		"no PR needs a human": {nil, nil, nil, "no PR needs a human in 11 repo(s) of ../../repos.mock.yaml; nothing posted"},
		"dry run":             {map[string]string{"mock-ui": majorPR}, []string{"--dry-run"}, nil, "dry run: nothing posted"},
		"no channel":          {map[string]string{"mock-ui": majorPR}, nil, map[string]string{"SLACK_CHANNEL": ""}, "is not set; nothing posted"},
		"no Slack token":      {map[string]string{"mock-ui": majorPR}, nil, map[string]string{"SLACK_BOT_TOKEN": ""}, "is not set; nothing posted"},
	} {
		t.Run(name, func(t *testing.T) {
			env, posts := setup(t, tc.prs)
			for k, v := range tc.env {
				env[k] = v
			}
			out, err := runCmd(env, append([]string{"post", "--manifest", "../../repos.mock.yaml"}, tc.args...)...)
			if err != nil || len(*posts) != 0 || !strings.Contains(out, tc.want) {
				t.Errorf("err %v, posts %v, output:\n%s", err, *posts, out)
			}
		})
	}
}

func TestPostFailsAfterPosting(t *testing.T) {
	env, posts := setup(t, map[string]string{"mock-ui": majorPR, "mock-api": `not json`})
	out, err := runCmd(env, "post", "--manifest", "../../repos.mock.yaml")
	if err == nil || !strings.Contains(err.Error(), "could not read mock-api") {
		t.Errorf("err = %v", err)
	}
	if len(*posts) != 1 || !strings.Contains(fmt.Sprint((*posts)[0]["attachments"]), "Could not read: mock-api") {
		t.Errorf("posts = %v", *posts)
	}
	if !strings.Contains(out, "::warning::attention-digest: mock-api: ") {
		t.Errorf("output:\n%s", out)
	}
}

func TestErrors(t *testing.T) {
	env, _ := setup(t, nil)
	for args, want := range map[string]string{
		"":                            "usage",
		"nope":                        "usage",
		"post --repos mock-nope":      "not in the manifest: mock-nope",
		"post --stale-days 0":         "--stale-days 0",
		"post extra":                  "unexpected arguments",
		"list --manifest nowhere.yml": "nowhere.yml",
	} {
		a := strings.Fields(args)
		if len(a) > 0 && !strings.Contains(args, "--manifest") {
			a = append(a, "--manifest", "../../repos.mock.yaml")
		}
		if _, err := runCmd(env, a...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", args, err, want)
		}
	}
	env["GH_TOKEN"] = ""
	if _, err := runCmd(env, "post", "--manifest", "../../repos.mock.yaml"); err == nil || !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Errorf("no GH_TOKEN: err %v", err)
	}
}
