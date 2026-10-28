package imagescan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/compliance-framework/workflows/internal/bump"
)

func TestGitHubFiles(t *testing.T) {
	var repoReads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/o/ui?":
			repoReads++
			_, _ = w.Write([]byte(`{"default_branch": "trunk"}`))
		case "/repos/o/ui/contents/.github/workflows?ref=trunk":
			_, _ = w.Write([]byte(`[{"name": "ci.yml"}, {"name": "release.yml"}]`))
		case "/repos/o/ui/contents/Dockerfile?ref=trunk":
			_, _ = w.Write([]byte(`{"content": "RlJPTSBuZ2lueAo="}`)) // FROM nginx
		default:
			http.Error(w, `{"message": "Not Found"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	fs := &GitHubFiles{GH: &bump.GitHub{BaseURL: srv.URL, Owner: "o"}}
	ctx := context.Background()

	names, err := fs.Dir(ctx, "ui", ".github/workflows")
	if err != nil || !slices.Equal(names, []string{"ci.yml", "release.yml"}) {
		t.Errorf("Dir = %v, %v", names, err)
	}
	data, ok, err := fs.File(ctx, "ui", "Dockerfile")
	if err != nil || !ok || string(data) != "FROM nginx\n" {
		t.Errorf("File = %q, %v, %v", data, ok, err)
	}
	// A missing file is not an error; any other failure is.
	if data, ok, err := fs.File(ctx, "ui", "Dockerfile-ci"); err != nil || ok || data != nil {
		t.Errorf("missing File = %q, %v, %v; want not found", data, ok, err)
	}
	if _, _, err := fs.File(ctx, "api", "Dockerfile"); err == nil {
		t.Error("File in a repo that can't be read: want an error")
	}
	if repoReads != 1 {
		t.Errorf("default branch read %d times, want once", repoReads)
	}
}
