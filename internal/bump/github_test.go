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
		case "/repos/o/none/releases":
			fmt.Fprint(w, `[]`)
		case "/repos/o/agent/contents/go.mod":
			fmt.Fprint(w, `{"content":"cmVxdWlyZSBn\nbyAxLjI2Cg=="}`) // "require go 1.26\n", wrapped as GitHub does
		case "/graphql":
			fmt.Fprint(w, `{"errors":[{"message":"auto-merge is not allowed"}]}`)
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
	if b, err := g.File(ctx, "agent", "v0.10.1", "go.mod"); err != nil || string(b) != "require go 1.26\n" {
		t.Errorf("File = %q, %v", b, err)
	}
	if pr, err := g.OpenPR(ctx, "agent", "ccf-bump/sync-2026-10-08"); err != nil || pr != nil {
		t.Errorf("OpenPR = %v, %v", pr, err)
	}
	if err := g.EnableAutoMerge(ctx, "PR_1"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("EnableAutoMerge error = %v", err)
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
		case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/issues/3/labels") || strings.HasSuffix(r.URL.Path, "/requested_reviewers")):
			fmt.Fprint(w, `[]`)
		default:
			http.Error(w, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
		}
	}))
	defer srv.Close()
	g := &GitHub{BaseURL: srv.URL, Owner: "o"}
	ctx := context.Background()
	for _, repo := range []string{"ui", "api"} {
		if err := g.LabelNeedsHuman(ctx, repo, 3); err != nil {
			t.Fatalf("%s: %v", repo, err)
		}
	}
	if err := g.RequestTeamReview(ctx, "ui", 3, "admins"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /repos/o/ui/labels/needs-human ",
		`POST /repos/o/ui/labels {"color":"d93f0b","description":"A bot PR waiting for a person","name":"needs-human"}`,
		`POST /repos/o/ui/issues/3/labels {"labels":["needs-human"]}`,
		"GET /repos/o/api/labels/needs-human ",
		`POST /repos/o/api/issues/3/labels {"labels":["needs-human"]}`,
		`POST /repos/o/ui/pulls/3/requested_reviewers {"team_reviewers":["admins"]}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	err := g.LabelNeedsHuman(ctx, "ui", 4) // adding the label is refused
	if se := (*StatusError)(nil); !errors.As(err, &se) || se.Code != http.StatusUnprocessableEntity {
		t.Errorf("LabelNeedsHuman(#4) error = %v, want a 422 StatusError", err)
	}
}
