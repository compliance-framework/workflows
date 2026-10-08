package train

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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
		"GET /repos/o/r/pulls?state=open&sort=created&direction=asc&per_page=100&page=1": `[{"number":3,"head":{"ref":"ccf-bump/train-x"}},
			{"number":4,"state":"open","html_url":"u4","auto_merge":{"merge_method":"squash"},"head":{"ref":"release-please--branches--main","sha":"h"},"base":{"sha":"b"},"labels":[{"name":"l"}]}]`,
		"GET /repos/o/r/pulls/4": `{"number":4,"state":"closed","merged_at":"2026-12-01T00:00:00Z","merge_commit_sha":"m","head":{"sha":"h"},"base":{"sha":"b"}}`,
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
	if pr, err := g.PR(ctx, "r", 4); err != nil || !pr.Merged || pr.MergeSHA != "m" || pr.Open {
		t.Errorf("PR = %+v %v", pr, err)
	}
	checks, err := g.Checks(ctx, "r", "h")
	if state, detail := EvaluateChecks(checks, "ci / required"); err != nil || state != ChecksFailing || detail != "osv" ||
		!checks[0].StartedAt.Equal(time.Date(2026, 10, 8, 5, 1, 0, 0, time.UTC)) {
		t.Errorf("Checks = %+v %v -> %s %s", checks, err, state, detail)
	}
	if tags, err := g.TagsAt(ctx, "r", "m"); err != nil || !reflect.DeepEqual(tags, []string{"v0.2.0"}) {
		t.Errorf("TagsAt = %v %v", tags, err)
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
