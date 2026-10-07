package reposettings

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// apiServer serves a repo o/a with GitHub's defaults and records every request as "METHOD path".
func apiServer(t *testing.T) (*GitHub, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	get := map[string]string{
		"/installation/repositories":          `{"total_count":1,"repositories":[{"full_name":"o/a"}]}`,
		"/repos/o/a":                          `{"allow_squash_merge":true,"allow_merge_commit":true,"allow_rebase_merge":true,"squash_merge_commit_title":"COMMIT_OR_PR_TITLE","squash_merge_commit_message":"COMMIT_MESSAGES","default_branch":"main"}`,
		"/repos/o/a/automated-security-fixes": `{"enabled":true,"paused":false}`,
		"/repos/o/a/rulesets":                 `[{"id":7,"name":"ccf-review","source_type":"Repository"},{"id":3,"name":"org","source_type":"Organization"}]`,
		"/repos/o/a/rulesets/7":               `{"id":7,"name":"ccf-review","target":"branch","source":"o/a","enforcement":"active","conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},"bypass_actors":[],"rules":[{"type":"pull_request","parameters":{"required_approving_review_count":0}}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("%s %s: missing token", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet && r.Method != http.MethodDelete && r.Method != http.MethodPut && len(body) == 0 {
			t.Errorf("%s %s: empty body", r.Method, r.URL.Path)
		}
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		resp, ok := get[r.URL.Path]
		if !ok {
			http.NotFound(w, r) // vulnerability alerts are off
			return
		}
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return &GitHub{BaseURL: srv.URL, Token: "tok"}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

func TestGitHubDryRunOnlyReads(t *testing.T) {
	gh, calls := apiServer(t)
	var out bytes.Buffer
	if err := Run(context.Background(), gh, Options{Owner: "o", Repos: []string{"a"}, Desired: desired(t), CheckTokenScope: true}, &out); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /installation/repositories", "GET /repos/o/a", "GET /repos/o/a/vulnerability-alerts",
		"GET /repos/o/a/automated-security-fixes", "GET /repos/o/a/rulesets", "GET /repos/o/a/rulesets/7",
	}
	if got := calls(); !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestGitHubApplyWrites(t *testing.T) {
	gh, calls := apiServer(t)
	if err := Run(context.Background(), gh, Options{Owner: "o", Repos: []string{"a"}, Desired: desired(t), Apply: true}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var writes []string
	for _, c := range calls() {
		if c[:4] != "GET " {
			writes = append(writes, c)
		}
	}
	want := []string{
		"PATCH /repos/o/a", "PUT /repos/o/a/vulnerability-alerts", "DELETE /repos/o/a/automated-security-fixes",
		"POST /repos/o/a/rulesets", "PUT /repos/o/a/rulesets/7",
	}
	if !slices.Equal(writes, want) {
		t.Errorf("writes = %v, want %v", writes, want)
	}
}
