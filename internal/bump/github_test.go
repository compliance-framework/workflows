package bump

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGitHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repos/o/agent/releases":
			if r.URL.Query().Get("page") == "1" {
				fmt.Fprint(w, `[{"tag_name":"v0.10.0-rc1","prerelease":false},{"tag_name":"v0.9.0"},{"tag_name":"v0.11.0","draft":true},
					{"tag_name":"v0.12.0","prerelease":true},{"tag_name":"v0.10.1"},{"tag_name":"mock-app-v1.0.0"}]`)
				return
			}
			fmt.Fprint(w, `[]`)
		case "/repos/o/workflows/commits/v1.1.0": // GitHub peels an annotated tag to its commit
			fmt.Fprint(w, `{"sha":"89abcdef0123456789abcdef0123456789abcdef"}`)
		case "/repos/o/workflows/releases/tags/v1.1.0":
			fmt.Fprint(w, `{"tag_name":"v1.1.0","author":{"login":"ccf-release-bot[bot]"}}`)
		case "/repos/o/workflows/compare/89abcdef0123456789abcdef0123456789abcdef...main":
			if r.URL.Query().Get("per_page") != "1" {
				t.Errorf("compare per_page = %q", r.URL.Query().Get("per_page"))
			}
			fmt.Fprint(w, `{"status":"ahead"}`)
		case "/repos/o/workflows/compare/0123456789abcdef0123456789abcdef01234567...main":
			fmt.Fprint(w, `{"status":"diverged"}`)
		case "/repos/o/none/releases":
			fmt.Fprint(w, `[]`)
		case "/repos/o/agent/contents/go.mod":
			fmt.Fprint(w, `{"content":"cmVxdWlyZSBn\nbyAxLjI2Cg=="}`) // "require go 1.26\n", wrapped as GitHub does
		case "/graphql":
			fmt.Fprint(w, `{"errors":[{"message":"auto-merge is not enabled"}]}`)
		case "/repos/o/agent/pulls":
			if r.URL.Query().Get("head") != "o:ccf-bump/sync-2026-10-08" {
				t.Errorf("head = %q", r.URL.Query().Get("head"))
			}
			fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	g := &GitHub{BaseURL: srv.URL, Token: "tok", Owner: "o"}
	ctx := context.Background()
	if v, err := g.LatestFinal(ctx, "agent"); err != nil || v != "v0.10.1" {
		t.Errorf("LatestFinal = %q, %v; want v0.10.1", v, err)
	}
	if v, err := g.LatestFinal(ctx, "none"); err != nil || v != "" {
		t.Errorf("LatestFinal(none) = %q, %v", v, err)
	}
	if sha, err := g.TagCommit(ctx, "workflows", "v1.1.0"); err != nil || sha != "89abcdef0123456789abcdef0123456789abcdef" {
		t.Errorf("TagCommit = %q, %v", sha, err)
	}
	if _, err := g.TagCommit(ctx, "workflows", "v9.9.9"); err == nil {
		t.Error("TagCommit(missing tag): no error")
	}
	if a, err := g.ReleaseAuthor(ctx, "workflows", "v1.1.0"); err != nil || a != "ccf-release-bot[bot]" {
		t.Errorf("ReleaseAuthor = %q, %v", a, err)
	}
	if _, err := g.ReleaseAuthor(ctx, "workflows", "v9.9.9"); err == nil {
		t.Error("ReleaseAuthor(missing release): no error")
	}
	if on, err := g.OnBranch(ctx, "workflows", "89abcdef0123456789abcdef0123456789abcdef", "main"); err != nil || !on {
		t.Errorf("OnBranch(ahead) = %v, %v", on, err)
	}
	if on, err := g.OnBranch(ctx, "workflows", "0123456789abcdef0123456789abcdef01234567", "main"); err != nil || on {
		t.Errorf("OnBranch(diverged) = %v, %v", on, err)
	}
	if b, err := g.File(ctx, "agent", "v0.10.1", "go.mod"); err != nil || string(b) != "require go 1.26\n" {
		t.Errorf("File = %q, %v", b, err)
	}
	if pr, err := g.OpenPR(ctx, "agent", "ccf-bump/sync-2026-10-08"); err != nil || pr != nil {
		t.Errorf("OpenPR = %v, %v", pr, err)
	}
	if err := g.DisableAutoMerge(ctx, "PR_1"); err == nil || !strings.Contains(err.Error(), "disable auto-merge: auto-merge is not enabled") {
		t.Errorf("DisableAutoMerge error = %v", err)
	}
	if _, err := g.DefaultBranch(ctx, "missing"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("DefaultBranch(missing) error = %v", err)
	}
}

func TestGitHubSupersededPRs(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+string(body))
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/o/ui/pulls":
			fmt.Fprint(w, `[{"number":7,"user":{"login":"b[bot]","type":"Bot"},"head":{"ref":"ccf-bump/sync-x","repo":{"full_name":"o/ui"}}},
				{"number":8,"user":{"login":"u","type":"User"},"head":{"ref":"x","repo":null}}]`)
		case "POST /repos/o/ui/issues/7/comments", "PATCH /repos/o/ui/pulls/7":
			fmt.Fprint(w, `{}`)
		case "DELETE /repos/o/ui/git/refs/heads/ccf-bump/sync-x":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	g := &GitHub{BaseURL: srv.URL, Owner: "o"}
	ctx := context.Background()
	prs, err := g.OpenPRs(ctx, "ui")
	if err != nil || len(prs) != 2 || prs[0].User.Login != "b[bot]" || prs[0].User.Type != "Bot" ||
		prs[0].Head.Ref != "ccf-bump/sync-x" || prs[0].Head.Repo.FullName != "o/ui" || prs[1].Head.Repo != nil {
		t.Fatalf("OpenPRs = %+v, %v", prs, err)
	}
	if err := g.ClosePR(ctx, "ui", 7, "Superseded by #9."); err != nil {
		t.Fatal(err)
	}
	if err := g.DeleteBranch(ctx, "ui", "ccf-bump/sync-x"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /repos/o/ui/pulls ",
		`POST /repos/o/ui/issues/7/comments {"body":"Superseded by #9."}`,
		`PATCH /repos/o/ui/pulls/7 {"state":"closed"}`,
		"DELETE /repos/o/ui/git/refs/heads/ccf-bump/sync-x ",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestGitHubNeedsHuman(t *testing.T) {
	var got []string
	labels := map[string]bool{"ui": false, "api": true} // repo -> needs-human exists
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+string(body))
		repo := strings.Split(r.URL.Path, "/")[3]
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/labels/needs-human"):
			if !labels[repo] {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/"+repo+"/labels":
			labels[repo] = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/3/labels"):
			fmt.Fprint(w, `[]`)
		default:
			http.Error(w, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
		}
	}))
	defer srv.Close()
	g := &GitHub{BaseURL: srv.URL, Owner: "o"}
	ctx := context.Background()
	for _, repo := range []string{"ui", "api"} {
		if err := g.AddLabel(ctx, repo, 3, NeedsHuman); err != nil {
			t.Fatalf("%s: %v", repo, err)
		}
	}
	want := []string{
		"GET /repos/o/ui/labels/needs-human ",
		`POST /repos/o/ui/labels {"color":"d93f0b","description":"A bot PR waiting for a person","name":"needs-human"}`,
		`POST /repos/o/ui/issues/3/labels {"labels":["needs-human"]}`,
		"GET /repos/o/api/labels/needs-human ",
		`POST /repos/o/api/issues/3/labels {"labels":["needs-human"]}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	err := g.AddLabel(ctx, "ui", 4, NeedsHuman) // adding the label is refused
	if se := (*StatusError)(nil); !errors.As(err, &se) || se.Code != http.StatusUnprocessableEntity {
		t.Errorf("AddLabel(#4) error = %v, want a 422 StatusError", err)
	}
}

func TestGitHubMerge(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		switch r.Method + " " + r.URL.Path {
		case "DELETE /repos/o/ui/issues/3/labels/ccf-bump:automerge":
			fmt.Fprint(w, `[]`)
		case "GET /repos/o/ui/pulls/3":
			fmt.Fprint(w, `{"number":3,"title":"t","mergeable":false,"mergeable_state":"dirty","auto_merge":{"merge_method":"squash"},
				"head":{"ref":"ccf-bump/sync-x","sha":"abc"},"labels":[{"name":"ccf-bump:automerge"}]}`)
		case "GET /repos/o/ui/commits/abc/check-runs":
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `{"check_runs":[{"id":2,"name":"ci / required","status":"in_progress","html_url":"u","started_at":"2026-10-08T05:02:00Z"}]}`)
				return
			}
			runs := []string{`{"id":1,"name":"ci / required","status":"completed","conclusion":"failure"}`, `{"id":3,"name":"ci / required-ish"}`}
			for len(runs) < 100 {
				runs = append(runs, `{"id":4,"name":"other"}`)
			}
			fmt.Fprintf(w, `{"check_runs":[%s]}`, strings.Join(runs, ","))
		case "GET /repos/o/ui/commits/none/check-runs":
			fmt.Fprint(w, `{"check_runs":[]}`)
		case "PUT /repos/o/ui/pulls/3/merge":
			fmt.Fprint(w, `{"merged":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	g := &GitHub{BaseURL: srv.URL, Owner: "o"}
	ctx := context.Background()
	if err := g.RemoveLabel(ctx, "ui", 3, AutomergeLabel); err != nil {
		t.Error(err)
	}
	if err := g.RemoveLabel(ctx, "ui", 4, AutomergeLabel); err != nil { // not on the PR: 404
		t.Errorf("RemoveLabel of a missing label: %v", err)
	}
	pr, err := g.PullRequest(ctx, "ui", 3)
	if err != nil || pr.Mergeable == nil || *pr.Mergeable || pr.MergeableState != "dirty" || pr.AutoMerge == nil ||
		pr.Head.SHA != "abc" || pr.Title != "t" || !pr.HasLabel(AutomergeLabel) || pr.HasLabel(NeedsHumanLabel) {
		t.Errorf("PullRequest = %+v, %v", pr, err)
	}
	runs, err := g.CheckRuns(ctx, "ui", "abc", "ci / required")
	if err != nil || len(runs) != 2 || runs[0].ID != 1 || runs[1].ID != 2 || runs[1].Status != "in_progress" ||
		!runs[1].StartedAt.Equal(time.Date(2026, 10, 8, 5, 2, 0, 0, time.UTC)) {
		t.Errorf("CheckRuns = %+v, %v; want runs 1 and 2 of both pages, only ci / required", runs, err)
	}
	if runs, err := g.CheckRuns(ctx, "ui", "none", "ci / required"); err != nil || len(runs) != 0 {
		t.Errorf("CheckRuns(none) = %+v, %v", runs, err)
	}
	if err := g.Merge(ctx, "ui", 3, "abc", "t (#3)"); err != nil {
		t.Error(err)
	}
	want := []string{
		"DELETE /repos/o/ui/issues/3/labels/ccf-bump:automerge ",
		"DELETE /repos/o/ui/issues/4/labels/ccf-bump:automerge ",
		"GET /repos/o/ui/pulls/3 ",
		"GET /repos/o/ui/commits/abc/check-runs?check_name=ci+%2F+required&filter=all&page=1&per_page=100 ",
		"GET /repos/o/ui/commits/abc/check-runs?check_name=ci+%2F+required&filter=all&page=2&per_page=100 ",
		"GET /repos/o/ui/commits/none/check-runs?check_name=ci+%2F+required&filter=all&page=1&per_page=100 ",
		`PUT /repos/o/ui/pulls/3/merge {"commit_title":"t (#3)","merge_method":"squash","sha":"abc"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRequiredCheck(t *testing.T) {
	at := func(id int64, minute int, status, conclusion string) CheckRun {
		return CheckRun{ID: id, Status: status, Conclusion: conclusion, StartedAt: time.Date(2026, 10, 8, 5, minute, 0, 0, time.UTC)}
	}
	for name, tc := range map[string]struct {
		runs            []CheckRun
		running, newest int64 // IDs, 0: nil
	}{
		"none":                         {},
		"one":                          {runs: []CheckRun{at(1, 1, "completed", "success")}, newest: 1},
		"old success, new in progress": {runs: []CheckRun{at(1, 1, "completed", "success"), at(2, 2, "in_progress", "")}, running: 2},
		"old failure, new success":     {runs: []CheckRun{at(2, 2, "completed", "success"), at(1, 1, "completed", "failure")}, newest: 2},
		"old success, new failure":     {runs: []CheckRun{at(1, 1, "completed", "success"), at(2, 2, "completed", "failure")}, newest: 2},
		"by start time, not ID":        {runs: []CheckRun{at(9, 1, "completed", "failure"), at(2, 3, "completed", "success")}, newest: 2},
		"same start: higher ID":        {runs: []CheckRun{at(2, 1, "completed", "success"), at(1, 1, "completed", "failure")}, newest: 2},
	} {
		running, newest := RequiredCheck(tc.runs)
		id := func(c *CheckRun) int64 {
			if c == nil {
				return 0
			}
			return c.ID
		}
		if id(running) != tc.running || id(newest) != tc.newest {
			t.Errorf("%s: running %d, newest %d; want %d, %d", name, id(running), id(newest), tc.running, tc.newest)
		}
	}
}

func TestExpectsCheck(t *testing.T) {
	expected := []byte(`{"message":"Required status check \"ci / required\" is expected."}`)
	// refused is GitHub's 405 for a ruleset violation, as on mock-plugin-2#30 (run 37915939413).
	refused := func(violation string) error {
		return &StatusError{Code: http.StatusMethodNotAllowed, Body: []byte(`{"message":"Repository rule violations found\n\n` + violation + `\n\n"}`)}
	}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"405 check expected":      {&StatusError{Code: http.StatusMethodNotAllowed, Body: expected}, true},
		"405 check queued":        {refused(`Required status check \"ci / required\" is queued.`), true},
		"405 check in progress":   {refused(`Required status check \"ci / required\" is in progress.`), true},
		"405 check pending":       {refused(`Required status check \"ci / required\" is pending.`), true},
		"405 checks expected":     {refused(`2 of 2 required status checks are expected.`), true},
		"405 check failing":       {refused(`Required status check \"ci / required\" is failing.`), false},
		"405 one of two failing":  {refused(`Required status check \"a\" is queued.\n\nRequired status check \"b\" is failing.`), false},
		"405 failed, then queued": {refused(`Required status check \"a\" has failed.\n\nRequired status check \"b\" is queued.`), false},
		"405 review required":     {refused(`At least 1 approving review is required by reviewers with write access.`), false},
		"405 other":               {&StatusError{Code: http.StatusMethodNotAllowed, Body: []byte(`{"message":"Pull Request is not mergeable"}`)}, false},
		"wrapped":                 {fmt.Errorf("merge: %w", &StatusError{Code: http.StatusMethodNotAllowed, Body: expected}), true},
		"409 head moved":          {&StatusError{Code: http.StatusConflict, Body: expected}, false},
		"not a StatusError":       {errors.New("Required status check is expected"), false},
		"nil":                     {nil, false},
	} {
		if got := ExpectsCheck(tc.err); got != tc.want {
			t.Errorf("%s: ExpectsCheck = %v, want %v", name, got, tc.want)
		}
	}
}
