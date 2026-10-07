package reposettings

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// defaultRepoJSON is GET /repos/o/a for a token that can read the merge settings.
const defaultRepoJSON = `{"allow_squash_merge":true,"allow_merge_commit":true,"allow_rebase_merge":true,"squash_merge_commit_title":"COMMIT_OR_PR_TITLE","squash_merge_commit_message":"COMMIT_MESSAGES","default_branch":"main"}`

// apiServer serves a repo o/a with GitHub's defaults and records every request as "METHOD path".
func apiServer(t *testing.T) (*GitHub, func() []string) {
	t.Helper()
	gh, calls, _ := apiServerWith(t, defaultRepoJSON)
	return gh, calls
}

// apiServerWith is apiServer with repoJSON as GET /repos/o/a; it also returns the PATCH bodies.
func apiServerWith(t *testing.T, repoJSON string) (*GitHub, func() []string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls, patches []string
	get := map[string]string{
		"/installation/repositories":          `{"total_count":1,"repositories":[{"full_name":"o/a"}]}`,
		"/repos/o/a":                          repoJSON,
		"/repos/o/a/automated-security-fixes": `{"enabled":true,"paused":false}`,
		"/repos/o/a/rulesets":                 `[{"id":7,"name":"ccf-review","source_type":"Repository"},{"id":3,"name":"org","source_type":"Organization"}]`,
		"/repos/o/a/rulesets/7":               `{"id":7,"name":"ccf-review","target":"branch","source":"o/a","enforcement":"active","conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},"bypass_actors":[],"rules":[{"type":"pull_request","parameters":{"required_approving_review_count":0}}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("%s %s: missing token", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		needsBody := r.Method == http.MethodPatch || r.Method == http.MethodPost || (r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/rulesets/"))
		if needsBody && len(body) == 0 {
			t.Errorf("%s %s: empty body", r.Method, r.URL.Path)
		}
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPatch {
			patches = append(patches, string(body))
		}
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
	locked := func(s *[]string) func() []string {
		return func() []string {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(*s)
		}
	}
	return &GitHub{BaseURL: srv.URL, Token: "tok"}, locked(&calls), locked(&patches)
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

func TestGitHubMergeSettingsAbsentVsFalse(t *testing.T) {
	// A token with Administration read gets no merge settings; here two are present, one false.
	gh, _, patches := apiServerWith(t, `{"allow_merge_commit":false,"allow_auto_merge":false,"squash_merge_commit_title":null,"default_branch":"main"}`)
	cur, err := gh.MergeSettings(context.Background(), "o/a")
	if err != nil {
		t.Fatal(err)
	}
	if cur.AllowMergeCommit == nil || *cur.AllowMergeCommit || cur.AllowAutoMerge == nil || cur.AllowRebaseMerge != nil || cur.SquashMergeCommitTitle != nil {
		t.Errorf("decoded %+v: want allow_merge_commit and allow_auto_merge false, the rest nil", cur)
	}

	var out bytes.Buffer
	if err := Run(context.Background(), gh, Options{Owner: "o", Repos: []string{"a"}, Desired: desired(t), Apply: true}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"repo.allow_auto_merge: false -> true",
		"repo.allow_rebase_merge: unknown (not readable with Administration read) -> false",
		`repo.squash_merge_commit_title: unknown (not readable with Administration read) -> "PR_TITLE"`,
		", 5 unknown",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "allow_merge_commit") {
		t.Errorf("allow_merge_commit is already false but is reported:\n%s", got)
	}

	// The PATCH carries every managed field, not just the differing or unknown ones.
	p := patches()
	if len(p) != 1 {
		t.Fatalf("PATCH bodies = %v, want one", p)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(p[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if want := toMap(desired(t).Merge); !reflect.DeepEqual(sent, want) {
		t.Errorf("PATCH body = %v, want %v", sent, want)
	}
}
