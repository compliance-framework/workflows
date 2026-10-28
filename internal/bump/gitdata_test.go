package bump

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestGitData(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, fmt.Sprintf("%s %s %s", r.Method, r.URL.Path, body))
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/o/ui/git/ref/heads/main":
			fmt.Fprint(w, `{"object":{"sha":"h1"}}`)
		case "GET /repos/o/ui/git/ref/heads/image-scan/os-patches":
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case "GET /repos/o/ui/git/commits/h1":
			fmt.Fprint(w, `{"tree":{"sha":"t0"},"parents":[{"sha":"p0"}]}`)
		case "POST /repos/o/ui/git/trees":
			fmt.Fprint(w, `{"sha":"t1"}`)
		case "POST /repos/o/ui/git/commits":
			fmt.Fprint(w, `{"sha":"c1"}`)
		case "POST /repos/o/ui/git/refs", "PATCH /repos/o/ui/git/refs/heads/image-scan/os-patches":
			fmt.Fprint(w, `{}`)
		default:
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	}))
	defer srv.Close()
	g := &GitHub{BaseURL: srv.URL, Owner: "o"}
	ctx := context.Background()

	if sha, err := g.BranchHead(ctx, "ui", "main"); err != nil || sha != "h1" {
		t.Errorf("BranchHead(main) = %q, %v", sha, err)
	}
	if sha, err := g.BranchHead(ctx, "ui", "image-scan/os-patches"); err != nil || sha != "" {
		t.Errorf("BranchHead of a missing branch = %q, %v; want empty, no error", sha, err)
	}
	if tree, parents, err := g.CommitTree(ctx, "ui", "h1"); err != nil || tree != "t0" || !slices.Equal(parents, []string{"p0"}) {
		t.Errorf("CommitTree = %q, %v, %v", tree, parents, err)
	}
	if sha, err := g.CreateTree(ctx, "ui", "t0", map[string][]byte{"b/Dockerfile": []byte("FROM b\n"), "Dockerfile": []byte("FROM a\n")}); err != nil || sha != "t1" {
		t.Errorf("CreateTree = %q, %v", sha, err)
	}
	bot := Identity{Name: "ccf-release-bot[bot]", Email: "1+ccf-release-bot[bot]@users.noreply.github.com"}
	if sha, err := g.CreateCommit(ctx, "ui", "msg", "t1", "h1", bot); err != nil || sha != "c1" {
		t.Errorf("CreateCommit = %q, %v", sha, err)
	}
	if _, err := g.CreateCommit(ctx, "ui", "msg", "t1", "h1", Identity{}); err != nil {
		t.Error(err)
	}
	if err := g.SetBranch(ctx, "ui", "image-scan/os-patches", "c1", true); err != nil {
		t.Error(err)
	}
	if err := g.SetBranch(ctx, "ui", "image-scan/os-patches", "c1", false); err != nil {
		t.Error(err)
	}

	decode := func(call string) map[string]any {
		var m map[string]any
		_, _, body := cut3(call)
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatalf("%s: %v", call, err)
		}
		return m
	}
	tree := decode(calls[3])
	if tree["base_tree"] != "t0" || fmt.Sprint(tree["tree"]) != "[map[content:FROM a\n mode:100644 path:Dockerfile type:blob] map[content:FROM b\n mode:100644 path:b/Dockerfile type:blob]]" {
		t.Errorf("tree request %v", tree)
	}
	commit := decode(calls[4])
	if fmt.Sprint(commit["parents"]) != "[h1]" || fmt.Sprint(commit["author"]) != "map[email:1+ccf-release-bot[bot]@users.noreply.github.com name:ccf-release-bot[bot]]" || fmt.Sprint(commit["committer"]) != fmt.Sprint(commit["author"]) {
		t.Errorf("commit request %v", commit)
	}
	if c := decode(calls[5]); c["author"] != nil || c["committer"] != nil {
		t.Errorf("commit with no identity sets one: %v", c)
	}
	if c := decode(calls[6]); c["ref"] != "refs/heads/image-scan/os-patches" || c["sha"] != "c1" {
		t.Errorf("create ref request %v", c)
	}
	if c := decode(calls[7]); c["sha"] != "c1" || c["force"] != true {
		t.Errorf("update ref request %v, want forced", c)
	}
}

func TestBranchAuthors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/ui/compare/main...p1":
			fmt.Fprint(w, `{"total_commits":2,"commits":[{"author":{"login":"ccf-release-bot[bot]"}},{"author":null}]}`)
		case "/repos/o/ui/compare/main...p2":
			fmt.Fprint(w, `{"total_commits":0,"commits":[]}`)
		case "/repos/o/ui/compare/main...p3":
			fmt.Fprint(w, `{"total_commits":101,"commits":[{"author":{"login":"a"}}]}`)
		default:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	g := &GitHub{BaseURL: srv.URL, Owner: "o"}
	ctx := context.Background()
	if a, err := g.BranchAuthors(ctx, "ui", "main", "p1"); err != nil || !slices.Equal(a, []string{"ccf-release-bot[bot]", ""}) {
		t.Errorf("BranchAuthors = %q, %v; want the bot, then an unlinked author", a, err)
	}
	if a, err := g.BranchAuthors(ctx, "ui", "main", "p2"); err != nil || len(a) != 0 {
		t.Errorf("BranchAuthors of a merged branch = %q, %v; want none", a, err)
	}
	if _, err := g.BranchAuthors(ctx, "ui", "main", "p3"); err == nil {
		t.Error("more than a page of commits: want an error")
	}
	if _, err := g.BranchAuthors(ctx, "ui", "main", "nope"); err == nil {
		t.Error("compare failed: want an error")
	}
}

// cut3 splits "METHOD path body".
func cut3(s string) (string, string, string) {
	var m, p string
	n, _ := fmt.Sscanf(s, "%s %s", &m, &p)
	if n != 2 {
		return "", "", ""
	}
	return m, p, s[len(m)+len(p)+2:]
}
