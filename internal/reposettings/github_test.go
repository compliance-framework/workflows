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
	gh, calls, _ := apiServerWith(t, defaultRepoJSON, nil)
	return gh, calls
}

// reply is a canned response: a status code and a body.
type reply struct {
	code int
	body string
}

// apiServerWith is apiServer with repoJSON as GET /repos/o/a and the replies in override, keyed by
// "METHOD path"; it also returns the PATCH bodies.
func apiServerWith(t *testing.T, repoJSON string, override map[string]reply) (*GitHub, func() []string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls, patches []string
	get := map[string]string{
		"/installation/repositories":                                  `{"total_count":1,"repositories":[{"full_name":"o/a"}]}`,
		"/repos/o/a":                                                  repoJSON,
		"/repos/o/a/automated-security-fixes":                         `{"enabled":true,"paused":false}`,
		"/repos/o/a/rulesets":                                         `[{"id":7,"name":"ccf-review","source_type":"Repository"},{"id":3,"name":"org","source_type":"Organization"}]`,
		"/repos/o/a/rulesets/7":                                       `{"id":7,"name":"ccf-review","target":"branch","source":"o/a","enforcement":"active","conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},"bypass_actors":[],"rules":[{"type":"pull_request","parameters":{"required_approving_review_count":0}}]}`,
		"/repos/o/a/actions/permissions/workflow":                     `{"default_workflow_permissions":"write","can_approve_pull_request_reviews":true}`,
		"/repos/o/a/actions/permissions/fork-pr-contributor-approval": `{"approval_policy":"first_time_contributors"}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("%s %s: missing token", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		needsBody := r.Method == http.MethodPatch || r.Method == http.MethodPost ||
			(r.Method == http.MethodPut && (strings.Contains(r.URL.Path, "/rulesets/") || strings.Contains(r.URL.Path, "/actions/permissions/")))
		if needsBody && len(body) == 0 {
			t.Errorf("%s %s: empty body", r.Method, r.URL.Path)
		}
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPatch || (r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/actions/permissions/")) {
			patches = append(patches, r.URL.Path+" "+string(body))
		}
		mu.Unlock()
		if o, ok := override[r.Method+" "+r.URL.Path]; ok {
			w.WriteHeader(o.code)
			_, _ = io.WriteString(w, o.body)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		resp, ok := get[r.URL.Path]
		if !ok {
			http.NotFound(w, r) // vulnerability alerts are off; no code security configuration
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
		"GET /installation/repositories", "GET /repos/o/a", "GET /repos/o/a/code-security-configuration", "GET /repos/o/a/vulnerability-alerts",
		"GET /repos/o/a/automated-security-fixes", "GET /repos/o/a/actions/permissions/workflow",
		"GET /repos/o/a/actions/permissions/fork-pr-contributor-approval", "GET /repos/o/a/rulesets", "GET /repos/o/a/rulesets/7",
	}
	if got := calls(); !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestGitHubApplyWrites(t *testing.T) {
	gh, calls, bodies := apiServerWith(t, defaultRepoJSON, nil)
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
		"PATCH /repos/o/a", "POST /repos/o/a/rulesets", "PUT /repos/o/a/rulesets/7", "POST /repos/o/a/rulesets",
		"PUT /repos/o/a/actions/permissions/workflow", "PUT /repos/o/a/actions/permissions/fork-pr-contributor-approval",
		"PUT /repos/o/a/vulnerability-alerts", "DELETE /repos/o/a/automated-security-fixes",
	}
	if !slices.Equal(writes, want) {
		t.Errorf("writes = %v, want %v", writes, want)
	}
	// The workflow permissions keep their default (write); only PR approval changes.
	for _, want := range []string{
		`/repos/o/a/actions/permissions/workflow {"default_workflow_permissions":"write","can_approve_pull_request_reviews":false}`,
		`/repos/o/a/actions/permissions/fork-pr-contributor-approval {"approval_policy":"all_external_contributors"}`,
	} {
		if !slices.Contains(bodies(), want) {
			t.Errorf("bodies %q lack %q", bodies(), want)
		}
	}
}

func TestGitHubForkPRApprovalPrivateRepo(t *testing.T) {
	gh, _, _ := apiServerWith(t, defaultRepoJSON, map[string]reply{
		"GET /repos/o/a/actions/permissions/fork-pr-contributor-approval": {404, `{"message":"Not Found"}`}})
	if policy, err := gh.ForkPRApproval(context.Background(), "o/a"); err != nil || policy != "" {
		t.Errorf("ForkPRApproval = %q, %v; want none", policy, err)
	}
}

func TestGitHubMergeSettingsAbsentVsFalse(t *testing.T) {
	// A token with Administration read gets no merge settings; here two are present, one false.
	gh, _, patches := apiServerWith(t, `{"allow_merge_commit":false,"allow_auto_merge":false,"squash_merge_commit_title":null,"default_branch":"main"}`, nil)
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
	var p []string
	for _, b := range patches() {
		if body, ok := strings.CutPrefix(b, "/repos/o/a "); ok {
			p = append(p, body)
		}
	}
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

func TestGitHubSecurityUpdates(t *testing.T) {
	const path = "GET /repos/o/a/automated-security-fixes"
	off, on := false, true
	for _, c := range []struct {
		name  string
		reply reply
		want  *bool
	}{
		{"disabled", reply{200, `{"enabled":false,"paused":false}`}, &off},
		{"enabled", reply{200, `{"enabled":true,"paused":false}`}, &on},
		{"field absent", reply{200, `{"paused":false}`}, nil},
		{"empty", reply{204, ``}, nil},
		{"forbidden", reply{403, `{"message":"Resource not accessible by integration"}`}, nil},
		{"dependabot off", reply{404, `{"message":"Not Found"}`}, &off},
	} {
		t.Run(c.name, func(t *testing.T) {
			gh, _, _ := apiServerWith(t, defaultRepoJSON, map[string]reply{path: c.reply})
			got, err := gh.SecurityUpdates(context.Background(), "o/a")
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
				t.Errorf("SecurityUpdates = %v, want %v", ptrString(got), ptrString(c.want))
			}
		})
	}
	gh, _, _ := apiServerWith(t, defaultRepoJSON, map[string]reply{path: {500, "boom"}})
	if _, err := gh.SecurityUpdates(context.Background(), "o/a"); err == nil {
		t.Error("a 500 is not an error")
	}
}

func ptrString(b *bool) string {
	if b == nil {
		return "unknown"
	}
	return stateName[*b]
}

// baselineJSON is GET /repos/{o}/{r}/code-security-configuration on the CCF repos (abridged).
const baselineJSON = `{"status":"enforced","configuration":{"id":227600,"target_type":"organization","name":"Baseline Security Profile","dependabot_alerts":"enabled","dependabot_security_updates":"disabled","enforcement":"enforced"}}`

func TestGitHubSecurityConfiguration(t *testing.T) {
	const path = "GET /repos/o/a/code-security-configuration"
	for _, c := range []struct {
		name  string
		reply reply
		want  *SecurityConfiguration
	}{
		{"enforced", reply{200, baselineJSON}, baseline()},
		{"unenforced", reply{200, strings.NewReplacer(`"status":"enforced"`, `"status":"attached"`, `"enforcement":"enforced"`, `"enforcement":"unenforced"`).Replace(baselineJSON)},
			&SecurityConfiguration{Name: "Baseline Security Profile", DependabotAlerts: "enabled", DependabotSecurityUpdates: "disabled"}},
		{"detached", reply{200, strings.Replace(baselineJSON, `"status":"enforced"`, `"status":"removed"`, 1)}, nil},
		{"none", reply{204, ``}, nil},
		{"not found", reply{404, `{"message":"Not Found"}`}, nil},
		{"forbidden", reply{403, `{"message":"Resource not accessible by integration"}`}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			gh, _, _ := apiServerWith(t, defaultRepoJSON, map[string]reply{path: c.reply})
			got, err := gh.SecurityConfiguration(context.Background(), "o/a")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("SecurityConfiguration = %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestGitHubEnforcedConfigNotWritten replays the first real apply: the repo reads as enabled while
// the enforced org configuration says disabled. Nothing is written to the security settings.
func TestGitHubEnforcedConfigNotWritten(t *testing.T) {
	gh, calls, _ := apiServerWith(t, defaultRepoJSON, map[string]reply{"GET /repos/o/a/code-security-configuration": {200, baselineJSON}})
	var out bytes.Buffer
	if err := Run(context.Background(), gh, Options{Owner: "o", Repos: []string{"a"}, Desired: desired(t), Apply: true}, &out); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls() {
		if strings.Contains(c, "automated-security-fixes") || strings.Contains(c, "vulnerability-alerts") {
			t.Errorf("called %s for an org-managed setting", c)
		}
	}
	if !strings.Contains(out.String(), `security.dependabot_security_updates: managed by org configuration "Baseline Security Profile" (disabled)`) {
		t.Errorf("output:\n%s", out.String())
	}
}

// TestGitHubRefusedStepKeepsApplying: a 422 on the security write doesn't stop the rulesets, and
// the run fails at the end.
func TestGitHubRefusedStepKeepsApplying(t *testing.T) {
	refusal := reply{422, `{"message":"An enforced security configuration prevented modifying dependabot security updates enablement."}`}
	gh, calls, _ := apiServerWith(t, defaultRepoJSON, map[string]reply{
		"GET /repos/o/a/code-security-configuration": {403, `{"message":"Resource not accessible by integration"}`},
		"DELETE /repos/o/a/automated-security-fixes": refusal,
	})
	var out bytes.Buffer
	err := Run(context.Background(), gh, Options{Owner: "o", Repos: []string{"a"}, Desired: desired(t), Apply: true}, &out)
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("err = %v, want the 422", err)
	}
	got := calls()
	for _, want := range []string{"POST /repos/o/a/rulesets", "PUT /repos/o/a/rulesets/7", "PUT /repos/o/a/vulnerability-alerts"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s: %v", want, got)
		}
	}
	if !strings.Contains(out.String(), "o/a: partly applied: 1 of 8 step(s) failed") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestGitHubRulesetBypassAbsentVsEmpty(t *testing.T) {
	// A token with Administration read gets no bypass_actors; one with write gets [] for none.
	hidden := `{"id":7,"name":"ccf-review","target":"branch","enforcement":"active","conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},"rules":[]}`
	for _, tc := range []struct {
		name     string
		override map[string]reply
		want     *[]BypassActor
	}{
		{"absent", map[string]reply{"GET /repos/o/a/rulesets/7": {http.StatusOK, hidden}}, nil},
		{"empty", nil, &[]BypassActor{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh, _, _ := apiServerWith(t, defaultRepoJSON, tc.override)
			got, err := gh.Rulesets(context.Background(), "o/a")
			if err != nil {
				t.Fatal(err)
			}
			if rs := got[7]; rs.Name != ReviewRuleset || !reflect.DeepEqual(rs.BypassActors, tc.want) || rs.Ruleset.BypassActors != nil {
				t.Errorf("ruleset 7 = %+v, want bypass actors %v", rs, tc.want)
			}
		})
	}
}
