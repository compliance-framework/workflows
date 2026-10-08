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
// calls it receives (each body with its "method" added). Each call returns a new ts.
func setup(t *testing.T, eventName, ref, event string) (map[string]string, *[]map[string]any) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "event.json"), []byte(event), 0o600); err != nil {
		t.Fatal(err)
	}
	posts := &[]map[string]any{}
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.URL.Path != "/chat.postMessage" && r.URL.Path != "/chat.update") || r.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["method"] = strings.TrimPrefix(r.URL.Path, "/")
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

	step("aaaaaaaaaa", failed, "true") // failure, no incident: a new card
	if len(*posts) != 1 || (*posts)[0]["thread_ts"] != nil || (*posts)[0]["channel"] != "C0CIFAIL" || text(0) != "CI failing: o/r#5 ci" ||
		(*posts)[0]["attachments"] == nil {
		t.Fatalf("posts = %v", *posts)
	}

	step("aaaaaaaaaa", failed, "false") // a re-run of the same commit: dedupe
	if len(*posts) != 1 {
		t.Fatalf("a re-run posted again: %v", (*posts)[1:])
	}

	// A new commit fails: a reply in the thread, and the card edited in place.
	step("bbbbbbbbbb", failed, "true")
	if len(*posts) != 3 || (*posts)[1]["thread_ts"] != "1700000000.000001" ||
		!strings.HasPrefix(text(1), "❌ failed: ci at <https://github.com/o/r/commit/bbbbbbbbbb|bbbbbbb>") ||
		(*posts)[2]["method"] != "chat.update" || (*posts)[2]["ts"] != "1700000000.000001" || text(2) != "CI failing again: o/r#5 ci" {
		t.Fatalf("posts = %v", (*posts)[1:])
	}

	step("cccccccccc", passed, "true") // a pass: the recovery in the thread, the card resolved
	if len(*posts) != 5 || (*posts)[3]["thread_ts"] != "1700000000.000001" ||
		!strings.HasPrefix(text(3), "✅ passing again at <https://github.com/o/r/commit/cccccccccc|ccccccc>") ||
		(*posts)[4]["method"] != "chat.update" || text(4) != "CI resolved: o/r#5 ci" {
		t.Fatalf("posts = %v", (*posts)[3:])
	}

	step("dddddddddd", passed, "false") // another pass: nothing
	step("eeeeeeeeee", failed, "true")  // failing again: a new card
	if len(*posts) != 6 || (*posts)[5]["thread_ts"] != nil || (*posts)[5]["method"] != "chat.postMessage" || text(5) != "CI failing: o/r#5 ci" {
		t.Fatalf("posts = %v", (*posts)[5:])
	}
}

// TestCardEditFailureStillSaves: when chat.update fails after the reply, the state is saved
// anyway, so the reply isn't posted again.
func TestCardEditFailureStillSaves(t *testing.T) {
	env, _ := setup(t, "pull_request", "refs/pull/5/merge", renovatePR("bbbbbbbbbb"))
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.update" {
			_, _ = w.Write([]byte(`{"ok":false,"error":"message_not_found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C0CIFAIL","ts":"1700000000.000009"}`))
	}))
	defer failing.Close()
	env["SLACK_API_URL"], env["NEEDS"] = failing.URL, failed
	open := `{"key":"` + incidentKey(t, env) + `","channel":"C0CIFAIL","ts":"1.1","open":true,"sha":"aaaaaaaaaa","failed_jobs":["ci"]}`
	if err := os.WriteFile(env["STATE_FILE"], []byte(open), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, err := runCmd(t, env, "post")
	if err != nil || !strings.Contains(stdout, "::warning::updating the incident card: chat.update: message_not_found") {
		t.Fatalf("stdout %q, err %v", stdout, err)
	}
	if out := outputs(t, env); out != "save=true\n" {
		t.Errorf("outputs = %q", out)
	}
}

// incidentKey is the run's incident key, from plan's restore-key output.
func incidentKey(t *testing.T, env map[string]string) string {
	t.Helper()
	if _, err := runCmd(t, env, "plan"); err != nil {
		t.Fatal(err)
	}
	_, after, _ := strings.Cut(outputs(t, env), "restore-key=")
	key, _, _ := strings.Cut(after, "\n")
	return key
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

// TestClosedPRIsIgnored: release-please relabels its PR after the merge, and the labeled event
// re-runs CI on the closed PR. Whatever that run's result, notify posts nothing.
func TestClosedPRIsIgnored(t *testing.T) {
	closed := strings.Replace(releasePR(`[{"name":"needs-human"}]`), `"number":7,`, `"number":7,"state":"closed",`, 1)
	for _, needs := range []string{failed, releaseChecksFailed, passed} {
		env, posts := setup(t, "pull_request", "refs/pull/7/merge", closed)
		env["NEEDS"], env["NEEDS_HUMAN_CHANNEL"] = needs, "C0HUMAN"
		env["NEEDS_HUMAN_STATE_FILE"] = filepath.Join(t.TempDir(), "needs-human.json")
		if _, err := runCmd(t, env, "plan"); err != nil {
			t.Fatal(err)
		}
		if out := outputs(t, env); !strings.HasPrefix(out, "notify=false\nreason=\n") || !strings.Contains(out, "needs-human=false\n") {
			t.Errorf("needs %s: plan outputs = %q", needs, out)
		}
		for _, cmd := range []string{"post", "needs-human"} {
			if _, err := runCmd(t, env, cmd); err != nil {
				t.Fatal(err)
			}
			if out := outputs(t, env); out != "save=false\n" {
				t.Errorf("needs %s: %s outputs = %q", needs, cmd, out)
			}
		}
		if len(*posts) != 0 {
			t.Errorf("needs %s: posts = %v", needs, *posts)
		}
	}
}

func TestPostIgnoresBrokenState(t *testing.T) {
	env, posts := setup(t, "pull_request", "refs/pull/5/merge", renovatePR("abc"))
	if err := os.WriteFile(env["STATE_FILE"], []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, err := runCmd(t, env, "post") // no NEEDS: a legacy if: failure() caller
	if err != nil || len(*posts) != 1 || !strings.Contains(stdout, "ignoring the restored incident state") ||
		(*posts)[0]["text"] != "CI failing: o/r#5 ci" {
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
	want := "Needs a human: o/r#7 chore(main): release 2.0.0"
	if len(*posts) != 1 || (*posts)[0]["channel"] != "C0HUMAN" || (*posts)[0]["text"] != want ||
		!strings.Contains(fmt.Sprint((*posts)[0]["attachments"]), "Release PR blocked by release-checks") {
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
