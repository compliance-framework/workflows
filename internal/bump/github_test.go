package bump

import (
	"context"
	"fmt"
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
