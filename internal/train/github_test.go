package train

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeAPI serves canned responses by "METHOD path?query" and records request bodies.
func fakeAPI(t *testing.T, routes map[string]string) (*GitHub, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		bodies = append(bodies, key+" "+string(b))
		resp, ok := routes[key]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return &GitHub{BaseURL: srv.URL, Token: "tok", Owner: "o", Repo: "workflows"}, &bodies
}

func TestGitHubRepos(t *testing.T) {
	g, bodies := fakeAPI(t, map[string]string{
		"GET /repos/o/r":               `{"default_branch":"main"}`,
		"GET /repos/o/r/branches/main": `{"commit":{"sha":"abc"}}`,
		"GET /repos/o/r/contents/.release-please-manifest.json?ref=abc": `{"content":"eyIuIjoi\nMC4xLjAifQ=="}`,
		// #1 and #2 are fork PRs named like release-please's (#2's fork was deleted): never adopted.
		"GET /repos/o/r/pulls?state=open&sort=created&direction=asc&per_page=100&page=1": `[{"number":1,"state":"open","head":{"ref":"release-please--branches--main","sha":"f","repo":{"full_name":"mallory/r"}}},
			{"number":2,"state":"open","head":{"ref":"release-please--branches--main","sha":"g","repo":null}},
			{"number":3,"head":{"ref":"ccf-bump/train-x","repo":{"full_name":"o/r"}}},
			{"number":4,"state":"open","html_url":"u4","auto_merge":{"merge_method":"squash"},"head":{"ref":"release-please--branches--main","sha":"h","repo":{"full_name":"O/r"}},"base":{"sha":"b"},"labels":[{"name":"l"}]}]`,
		"GET /repos/o/r/pulls/4": `{"number":4,"state":"closed","merged_at":"2026-12-01T00:00:00Z","merge_commit_sha":"m","head":{"sha":"h","repo":{"full_name":"o/r"}},"base":{"sha":"b"}}`,
		"GET /repos/o/r/pulls/1": `{"number":1,"state":"open","head":{"ref":"release-please--branches--main","sha":"f","repo":{"full_name":"mallory/r"}}}`,
		"GET /repos/o/r/commits/h/check-runs?filter=all&per_page=100&page=1": `{"check_runs":[{"id":1,"name":"ci / required","status":"completed","conclusion":"success","started_at":"2026-10-08T05:01:00Z"}]}`,
		"GET /repos/o/r/commits/h/status?per_page=100":                       `{"statuses":[{"id":7,"context":"osv","state":"error"},{"id":8,"context":"cla","state":"pending"}]}`,
		"GET /repos/o/r/tags?per_page=100&page=1":                            `[{"name":"v0.2.0","commit":{"sha":"m"}},{"name":"v0.1.0","commit":{"sha":"x"}}]`,
		"PUT /repos/o/r/pulls/4/merge":                                       `{"merged":true}`,
	})
	branch, sha, err := g.DefaultBranch(ctx, "r")
	if err != nil || branch != "main" || sha != "abc" {
		t.Errorf("DefaultBranch = %q %q %v", branch, sha, err)
	}
	if data, err := g.File(ctx, "r", "abc", ReleasePleaseManifestFile); err != nil || string(data) != `{".":"0.1.0"}` {
		t.Errorf("File = %q %v", data, err)
	}
	if data, err := g.File(ctx, "r", "nope", ReleasePleaseManifestFile); err != nil || data != nil {
		t.Errorf("missing File = %q %v", data, err)
	}
	pr, err := g.OpenPR(ctx, "r", ReleaseBranchPrefix)
	if want := (&PR{Number: 4, URL: "u4", HeadSHA: "h", BaseSHA: "b", Open: true, AutoMerge: true, Labels: []string{"l"}}); err != nil || !reflect.DeepEqual(pr, want) {
		t.Errorf("OpenPR = %+v %v", pr, err)
	}
	if pr, err := g.PR(ctx, "r", 4); err != nil || !pr.Merged || pr.MergeSHA != "m" || pr.Open || pr.Fork {
		t.Errorf("PR = %+v %v", pr, err)
	}
	if pr, err := g.PR(ctx, "r", 1); err != nil || !pr.Fork {
		t.Errorf("fork PR = %+v %v", pr, err)
	}
	checks, err := g.Checks(ctx, "r", "h")
	// Statuses map onto checks: error fails, pending runs. Checks that are not required are ignored.
	if state, _ := EvaluateChecks(checks, []string{CICheck}); err != nil || state != ChecksGreen {
		t.Errorf("Checks = %+v %v -> %s", checks, err, state)
	}
	if state, detail := EvaluateChecks(checks, []string{CICheck, "osv", "cla"}); err != nil || state != ChecksFailing || detail != "osv" ||
		!checks[0].StartedAt.Equal(time.Date(2026, 10, 8, 5, 1, 0, 0, time.UTC)) {
		t.Errorf("Checks = %+v %v -> %s %s", checks, err, state, detail)
	}
	if tags, err := g.TagsAt(ctx, "r", "m"); err != nil || !reflect.DeepEqual(tags, []string{"v0.2.0"}) {
		t.Errorf("TagsAt = %v %v", tags, err)
	}
	if tags, err := g.Tags(ctx, "r"); err != nil || !reflect.DeepEqual(tags, map[string]string{"v0.2.0": "m", "v0.1.0": "x"}) {
		t.Errorf("Tags = %v %v", tags, err)
	}
	if err := g.Merge(ctx, "r", 4, "h"); err != nil || !strings.HasSuffix((*bodies)[len(*bodies)-1], `{"merge_method":"squash","sha":"h"}`) {
		t.Errorf("Merge: %v %v", err, *bodies)
	}
	if _, err := g.PR(ctx, "r", 5); err == nil || !strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "tok") {
		t.Errorf("PR 5: %v", err)
	}
}

func TestGitHubTracker(t *testing.T) {
	g, bodies := fakeAPI(t, map[string]string{
		"GET /repos/o/workflows/issues?state=all&labels=train%3Aopen&per_page=100&page=1": `[{"number":1,"title":"Release train 2026-12","state":"open","labels":[{"name":"train:open"}]},{"number":2,"pull_request":{}}]`,
		"PATCH /repos/o/workflows/issues/1":                                               `{}`,
		"GET /repos/o/workflows/issues/1/comments?per_page=100&page=1":                    `[{"id":5,"body":"old","user":{"login":"a"}},{"id":9,"body":"/abort","user":{"login":"b"}}]`,
		"GET /orgs/o/memberships/boss":                                                    `{"state":"active","role":"admin"}`,
		"GET /orgs/o/memberships/dev":                                                     `{"state":"active","role":"member"}`,
		"GET /repos/o/helm/issues?state=open&per_page=100&page=1":                         `[{"number":3,"title":"t","html_url":"u3"}]`,
	})
	issues, err := g.Issues(ctx, LabelOpen)
	if err != nil || len(issues) != 1 || !issues[0].Open || issues[0].Labels[0] != LabelOpen {
		t.Errorf("Issues = %+v %v", issues, err)
	}
	if err := g.EditIssue(ctx, 1, "b", []string{LabelTrain, LabelDone}, true); err != nil {
		t.Fatal(err)
	}
	var patch map[string]any
	_ = json.Unmarshal([]byte(strings.SplitN((*bodies)[len(*bodies)-1], " ", 3)[2]), &patch)
	if patch["state"] != "closed" || patch["body"] != "b" {
		t.Errorf("PATCH = %v", patch)
	}
	if cs, err := g.Comments(ctx, 1, 5); err != nil || len(cs) != 1 || cs[0] != (Comment{ID: 9, Author: "b", Body: "/abort"}) {
		t.Errorf("Comments = %+v %v", cs, err)
	}
	for user, want := range map[string]bool{"boss": true, "dev": false, "stranger": false} {
		if got, err := g.IsOrgAdmin(ctx, user); err != nil || got != want {
			t.Errorf("IsOrgAdmin(%s) = %v %v", user, got, err)
		}
	}
	if u, err := g.EnsureIssue(ctx, "helm", "t", "body"); err != nil || u != "u3" {
		t.Errorf("EnsureIssue (exists) = %q %v", u, err)
	}
}

func TestGitHubGate(t *testing.T) {
	g, bodies := fakeAPI(t, map[string]string{
		"GET /repos/o/workflows":                                                `{"default_branch":"main"}`,
		"GET /repos/o/workflows/branches/main":                                  `{"commit":{"sha":"abc"}}`,
		"POST /repos/o/workflows/actions/workflows/plugin-probe.yml/dispatches": `{"workflow_run_id":42,"run_url":"api","html_url":"https://run/42"}`,
		"GET /repos/o/workflows/actions/runs/42":                                `{"id":42,"path":".github/workflows/plugin-probe.yml","status":"completed","conclusion":"failure","html_url":"https://run/42","run_attempt":2}`,
		"GET /repos/o/workflows/actions/runs/42/jobs?per_page=100":              `{"jobs":[{"id":5},{"id":6}]}`,
		"GET /repos/o/workflows/actions/jobs/5/logs":                            "line 1\n",
		"GET /repos/o/workflows/actions/jobs/6/logs":                            "line 2\n",
	})
	run, err := g.Dispatch(ctx, "plugin-probe.yml", map[string]string{"plugins": "ghcr.io/o/p:v1.0.0"})
	if err != nil || run.ID != 42 || run.URL != "https://run/42" {
		t.Errorf("Dispatch = %+v %v", run, err)
	}
	// return_run_details: without it GitHub answers 204 with no run ID.
	if want := `POST /repos/o/workflows/actions/workflows/plugin-probe.yml/dispatches {"inputs":{"plugins":"ghcr.io/o/p:v1.0.0"},"ref":"main","return_run_details":true}`; !slices.Contains(*bodies, want) {
		t.Errorf("bodies = %q", *bodies)
	}
	if run, err := g.WorkflowRun(ctx, 42); err != nil || run.Status != "completed" || run.Conclusion != "failure" || run.Attempt != 2 {
		t.Errorf("WorkflowRun = %+v %v", run, err)
	}
	if log, err := g.RunLog(ctx, 42); err != nil || log != "line 1\nline 2\n" {
		t.Errorf("RunLog = %q %v", log, err)
	}
}

// A dispatch GitHub answers 204 (no body) or with a body it can't read is a run without an ID,
// not an error: the gate fails the check rather than dispatching it again. The call keeps the
// client's API version.
func TestGitHubDispatchNoRunID(t *testing.T) {
	for name, resp := range map[string]string{"204": "", "unreadable": "<html>", "no ID": `{"html_url":"u"}`} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /repos/o/workflows":
					_, _ = io.WriteString(w, `{"default_branch":"main"}`)
				case "GET /repos/o/workflows/branches/main":
					_, _ = io.WriteString(w, `{"commit":{"sha":"abc"}}`)
				case "POST /repos/o/workflows/actions/workflows/stack-smoke.yml/dispatches":
					if v := r.Header.Get("X-GitHub-Api-Version"); v != "2022-11-28" {
						t.Errorf("X-GitHub-Api-Version = %q", v)
					}
					var in map[string]any
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in["return_run_details"] != true || in["ref"] != "main" {
						t.Errorf("dispatch body = %v %v", in, err)
					}
					if resp == "" {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					_, _ = io.WriteString(w, resp)
				default:
					http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)
			g := &GitHub{BaseURL: srv.URL, Owner: "o", Repo: "workflows"}
			run, err := g.Dispatch(ctx, "stack-smoke.yml", map[string]string{"api": "v1"})
			if err != nil || run.ID != 0 || run.Path != ".github/workflows/stack-smoke.yml" || run.Status != "queued" {
				t.Errorf("Dispatch = %+v %v", run, err)
			}
		})
	}
}
