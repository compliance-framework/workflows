package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setup returns the step environment for a run, with a fake Slack server, and the
// messages it receives. Each message gets a new ts.
func setup(t *testing.T, eventName, ref, event string) (map[string]string, *[]map[string]any) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "event.json"), []byte(event), 0o600); err != nil {
		t.Fatal(err)
	}
	posts := &[]map[string]any{}
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" || r.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*posts = append(*posts, body)
		_, _ = fmt.Fprintf(w, `{"ok":true,"channel":"C0CIFAIL","ts":"1700000000.%06d"}`, len(*posts))
	}))
	t.Cleanup(slack.Close)
	return map[string]string{
		"GITHUB_REPOSITORY": "o/r", "GITHUB_WORKFLOW": "ci", "GITHUB_EVENT_NAME": eventName,
		"GITHUB_EVENT_PATH": filepath.Join(dir, "event.json"), "GITHUB_SHA": "1111111111", "GITHUB_REF": ref,
		"GITHUB_RUN_ID": "100", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_OUTPUT": filepath.Join(dir, "output"),
		"GITHUB_WORKFLOW_REF": "o/r/.github/workflows/ci.yml@" + ref, "STATE_FILE": filepath.Join(dir, "state.json"),
		"SLACK_API_URL": slack.URL, "SLACK_BOT_TOKEN": "xoxb-test", "SLACK_CHANNEL": "C0CIFAIL",
	}, posts
}

func runCmd(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	err := run(context.Background(), args, func(k string) string { return env[k] }, &stdout)
	return stdout.String(), err
}

// outputs returns and clears what the last command wrote to GITHUB_OUTPUT.
func outputs(t *testing.T, env map[string]string) string {
	t.Helper()
	data, err := os.ReadFile(env["GITHUB_OUTPUT"])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(env["GITHUB_OUTPUT"]); err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func renovatePR(sha string) string {
	return `{"pull_request":{"number":5,"title":"chore(deps): x","user":{"login":"renovate[bot]"},"head":{"ref":"renovate/test","sha":"` + sha + `"}}}`
}

const (
	failed = `{"ci":{"result":"failure"},"release-checks":{"result":"success"}}`
	passed = `{"ci":{"result":"success"},"release-checks":{"result":"skipped"}}`
)

func TestPlan(t *testing.T) {
	tests := []struct {
		name, event, ref, eventJSON, needs, want string
	}{
		{"renovate PR failed", "pull_request", "refs/pull/5/merge", renovatePR("abc"), failed,
			"notify=true\nreason=automation-branch\nkey=ccf-notify-incident-"},
		{"renovate PR passed", "pull_request", "refs/pull/5/merge", renovatePR("abc"), passed,
			"notify=true\nreason=automation-branch\n"},
		{"renovate PR cancelled", "pull_request", "refs/pull/5/merge", renovatePR("abc"), `{"ci":{"result":"cancelled"}}`,
			"notify=false\nreason=automation-branch\n"},
		{"main failed", "push", "refs/heads/main", `{"repository":{"default_branch":"main"}}`, failed,
			"notify=true\nreason=default-branch\n"},
		{"human PR", "pull_request", "refs/pull/6/merge", `{"pull_request":{"number":6,"user":{"login":"octocat"},"head":{"ref":"fix","sha":"abc"}}}`, failed,
			"notify=false\nreason=\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _ := setup(t, tt.event, tt.ref, tt.eventJSON)
			env["NEEDS"] = tt.needs
			if _, err := runCmd(t, env, "plan"); err != nil {
				t.Fatal(err)
			}
			out := outputs(t, env)
			if !strings.HasPrefix(out, tt.want) || !strings.Contains(out, "-100-1\nrestore-key=ccf-notify-incident-") {
				t.Errorf("outputs = %q, want the prefix %q", out, tt.want)
			}
		})
	}
}

// TestIncidentLifecycle runs a PR's notify job through every transition, carrying the state
// file between runs the way the cache does.
func TestIncidentLifecycle(t *testing.T) {
	env, posts := setup(t, "pull_request", "refs/pull/5/merge", renovatePR("aaaaaaaaaa"))
	step := func(sha, needs, wantSave string) {
		t.Helper()
		if err := os.WriteFile(env["GITHUB_EVENT_PATH"], []byte(renovatePR(sha)), 0o600); err != nil {
			t.Fatal(err)
		}
		env["NEEDS"] = needs
		stdout, err := runCmd(t, env, "post")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stdout, "xoxb-test") {
			t.Error("the token was printed")
		}
		if out := outputs(t, env); out != "save="+wantSave+"\n" {
			t.Errorf("%s %s: outputs = %q, want save=%s", sha, needs, out, wantSave)
		}
	}
	text := func(i int) string { return (*posts)[i]["text"].(string) }

	step("aaaaaaaaaa", passed, "false") // success, no incident: nothing
	if len(*posts) != 0 {
		t.Fatalf("posts = %v", *posts)
	}

	step("aaaaaaaaaa", failed, "true") // failure, no incident: a new thread
	if len(*posts) != 1 || (*posts)[0]["thread_ts"] != nil || (*posts)[0]["channel"] != "C0CIFAIL" ||
		!strings.HasPrefix(text(0), "❌ o/r PR #5 failed: ci at <https://github.com/o/r/commit/aaaaaaaaaa|aaaaaaa>") {
		t.Fatalf("posts = %v", *posts)
	}

	step("aaaaaaaaaa", failed, "false") // a re-run of the same commit: dedupe
	if len(*posts) != 1 {
		t.Fatalf("a re-run posted again: %v", (*posts)[1:])
	}

	step("bbbbbbbbbb", failed, "true") // a new commit fails: a reply in the thread
	if len(*posts) != 2 || (*posts)[1]["thread_ts"] != "1700000000.000001" ||
		!strings.HasPrefix(text(1), "❌ failed: ci at <https://github.com/o/r/commit/bbbbbbbbbb|bbbbbbb>") {
		t.Fatalf("posts = %v", (*posts)[1:])
	}

	step("cccccccccc", passed, "true") // a pass: the recovery in the thread
	if len(*posts) != 3 || (*posts)[2]["thread_ts"] != "1700000000.000001" ||
		!strings.HasPrefix(text(2), "✅ passing again at <https://github.com/o/r/commit/cccccccccc|ccccccc>") {
		t.Fatalf("posts = %v", (*posts)[2:])
	}

	step("dddddddddd", passed, "false") // another pass: nothing
	step("eeeeeeeeee", failed, "true")  // failing again: a new thread
	if len(*posts) != 4 || (*posts)[3]["thread_ts"] != nil || !strings.HasPrefix(text(3), "❌ o/r PR #5 failed: ci at ") {
		t.Fatalf("posts = %v", (*posts)[3:])
	}
}

func TestPostWithoutTokenIsNoop(t *testing.T) {
	env, posts := setup(t, "pull_request", "refs/pull/5/merge", renovatePR("abc"))
	env["SLACK_BOT_TOKEN"] = ""
	env["NEEDS"] = "not json"
	if stdout, err := runCmd(t, env, "post"); err != nil || len(*posts) != 0 || !strings.Contains(stdout, "nothing to post") {
		t.Errorf("stdout = %q, err = %v, posts = %v", stdout, err, *posts)
	}
}

func TestPostUntrackedRunIsNoop(t *testing.T) {
	env, posts := setup(t, "pull_request", "refs/pull/6/merge", `{"pull_request":{"number":6,"user":{"login":"octocat"},"head":{"ref":"fix","sha":"abc"}}}`)
	if stdout, err := runCmd(t, env, "post"); err != nil || len(*posts) != 0 || !strings.Contains(stdout, "not tracked") {
		t.Errorf("stdout = %q, err = %v, posts = %v", stdout, err, *posts)
	}
	if out := outputs(t, env); out != "save=false\n" {
		t.Errorf("outputs = %q", out)
	}
}

func TestPostIgnoresBrokenState(t *testing.T) {
	env, posts := setup(t, "pull_request", "refs/pull/5/merge", renovatePR("abc"))
	if err := os.WriteFile(env["STATE_FILE"], []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, err := runCmd(t, env, "post") // no NEEDS: a legacy if: failure() caller
	if err != nil || len(*posts) != 1 || !strings.Contains(stdout, "ignoring the restored incident state") ||
		!strings.HasPrefix((*posts)[0]["text"].(string), "❌ o/r PR #5 failed at ") {
		t.Errorf("stdout = %q, err = %v, posts = %v", stdout, err, *posts)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		unset   string
		needs   string
		wantErr string
	}{
		{"no command", nil, "", "", "usage"},
		{"plan without GITHUB_OUTPUT", []string{"plan"}, "GITHUB_OUTPUT", "", "GITHUB_OUTPUT"},
		{"plan with bad needs", []string{"plan"}, "", "{", "needs"},
		{"post without STATE_FILE", []string{"post"}, "STATE_FILE", "", "STATE_FILE"},
		{"post without channel", []string{"post"}, "SLACK_CHANNEL", "", "channel"},
		{"post with no needs jobs", []string{"post"}, "", "{}", "no jobs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _ := setup(t, "pull_request", "refs/pull/5/merge", renovatePR("abc"))
			delete(env, tt.unset)
			env["NEEDS"] = tt.needs
			if _, err := runCmd(t, env, tt.args...); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func releasePR(labels string) string {
	return `{"pull_request":{"number":7,"title":"chore(main): release 2.0.0","user":{"login":"ccf-release-bot[bot]"},` +
		`"head":{"ref":"release-please--branches--main","sha":"abc"},"labels":` + labels + `}}`
}

const releaseChecksFailed = `{"ci":{"result":"success"},"release-checks":{"result":"failure"}}`

func TestPlanNeedsHuman(t *testing.T) {
	majorPR := `{"pull_request":{"number":13,"title":"chore(deps): update typescript to v7","user":{"login":"ccf-release-bot[bot]"},` +
		`"head":{"ref":"renovate/typescript-7.x","sha":"abc"},"labels":[{"name":"needs-human"}]}}`
	tests := []struct {
		name, eventJSON, needs, channel string
		want                            []string
	}{
		{"labelled bot PR", majorPR, passed, "C0HUMAN", []string{"notify=true\n", "needs-human=true\nneeds-human-key=ccf-notify-needs-human-"}},
		{"release PR blocked: no incident", releasePR("[]"), releaseChecksFailed, "C0HUMAN", []string{"notify=false\n", "needs-human=true\n"}},
		{"no channel: an incident as before", releasePR("[]"), releaseChecksFailed, "", []string{"notify=true\n", "needs-human=false\n"}},
		{"human PR", `{"pull_request":{"number":6,"user":{"login":"octocat"},"head":{"ref":"fix","sha":"abc"},"labels":[{"name":"needs-human"}]}}`,
			failed, "C0HUMAN", []string{"notify=false\n", "needs-human=false\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _ := setup(t, "pull_request", "refs/pull/7/merge", tt.eventJSON)
			env["NEEDS"], env["NEEDS_HUMAN_CHANNEL"] = tt.needs, tt.channel
			if _, err := runCmd(t, env, "plan"); err != nil {
				t.Fatal(err)
			}
			out := outputs(t, env)
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("outputs = %q, want %q in it", out, w)
				}
			}
		})
	}
}

func TestNeedsHumanPost(t *testing.T) {
	env, posts := setup(t, "pull_request", "refs/pull/7/merge", releasePR("[]"))
	env["NEEDS"], env["NEEDS_HUMAN_CHANNEL"] = releaseChecksFailed, "C0HUMAN"
	env["NEEDS_HUMAN_STATE_FILE"] = filepath.Join(t.TempDir(), "needs-human.json")

	// The incident rules leave a release-checks-only failure to the needs-human rule.
	if _, err := runCmd(t, env, "post"); err != nil {
		t.Fatal(err)
	}
	if out := outputs(t, env); out != "save=false\n" || len(*posts) != 0 {
		t.Fatalf("post: outputs %q, posts %v", out, *posts)
	}

	stdout, err := runCmd(t, env, "needs-human")
	if err != nil {
		t.Fatal(err)
	}
	if out := outputs(t, env); out != "save=true\n" {
		t.Errorf("outputs = %q", out)
	}
	want := ":raising_hand: <https://github.com/o/r/pull/7|r#7> chore(main): release 2.0.0 — needs a human: release PR blocked by release-checks"
	if len(*posts) != 1 || (*posts)[0]["channel"] != "C0HUMAN" || !strings.HasPrefix((*posts)[0]["text"].(string), want) {
		t.Fatalf("posts = %v, want %q", *posts, want)
	}
	if strings.Contains(stdout, "xoxb-test") {
		t.Error("the token was printed")
	}
	data, err := os.ReadFile(env["NEEDS_HUMAN_STATE_FILE"])
	if err != nil || !strings.Contains(string(data), `"key":"ccf-notify-needs-human-`) {
		t.Errorf("record %q, %v", data, err)
	}
}

func TestNeedsHumanNoop(t *testing.T) {
	for name, tc := range map[string]struct{ channel, token, event string }{
		"no channel":      {"", "xoxb-test", releasePR("[]")},
		"no token":        {"C0HUMAN", "", releasePR("[]")},
		"nothing to post": {"C0HUMAN", "xoxb-test", renovatePR("abc")},
	} {
		t.Run(name, func(t *testing.T) {
			env, posts := setup(t, "pull_request", "refs/pull/7/merge", tc.event)
			env["NEEDS"], env["NEEDS_HUMAN_CHANNEL"], env["SLACK_BOT_TOKEN"] = releaseChecksFailed, tc.channel, tc.token
			env["NEEDS_HUMAN_STATE_FILE"] = filepath.Join(t.TempDir(), "needs-human.json")
			if stdout, err := runCmd(t, env, "needs-human"); err != nil || !strings.Contains(stdout, "nothing to post") {
				t.Errorf("stdout %q, err %v", stdout, err)
			}
			if out := outputs(t, env); out != "save=false\n" || len(*posts) != 0 {
				t.Errorf("outputs %q, posts %v", out, *posts)
			}
		})
	}
	env, _ := setup(t, "pull_request", "refs/pull/7/merge", releasePR("[]"))
	env["NEEDS"], env["NEEDS_HUMAN_CHANNEL"] = releaseChecksFailed, "C0HUMAN"
	if _, err := runCmd(t, env, "needs-human"); err == nil || !strings.Contains(err.Error(), "NEEDS_HUMAN_STATE_FILE") {
		t.Errorf("err = %v, want NEEDS_HUMAN_STATE_FILE", err)
	}
}
