package attention

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHub(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Method != http.MethodGet {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		got = append(got, r.URL.Path+"?"+r.URL.RawQuery)
		switch r.URL.Path {
		case "/repos/o/r/pulls":
			if r.URL.Query().Get("page") == "1" {
				fmt.Fprint(w, "[")
				for i := 1; i <= 100; i++ {
					sep := ","
					if i == 100 {
						sep = ""
					}
					fmt.Fprintf(w, `{"number":%d,"title":"t","html_url":"u","created_at":"2026-10-01T00:00:00Z","user":{"login":"b"},`+
						`"head":{"ref":"renovate/x","sha":"h"},"base":{"sha":"s"},"labels":[{"name":"needs-human"}]}%s`, i, sep)
				}
				fmt.Fprint(w, "]")
				return
			}
			fmt.Fprint(w, `[{"number":101,"title":"last","user":{"login":"b"},"head":{"ref":"x","sha":"h2"},"base":{"sha":"s"}}]`)
		case "/repos/o/r/commits/h/check-runs":
			switch r.URL.Query().Get("check_name") {
			case "ci / required":
				fmt.Fprint(w, `{"check_runs":[{"status":"completed","conclusion":"failure"}]}`)
			case "pending":
				fmt.Fprint(w, `{"check_runs":[{"status":"in_progress","conclusion":null}]}`)
			default:
				fmt.Fprint(w, `{"check_runs":[{"status":"completed","conclusion":"success"}]}`)
			}
		case "/repos/o/r/contents/.release-please-manifest.json":
			if r.URL.Query().Get("ref") == "s" {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("Accept") != "application/vnd.github.raw" {
				t.Errorf("Accept = %q", r.Header.Get("Accept"))
			}
			fmt.Fprint(w, `{".":"2.0.0"}`)
		default:
			http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
		}
	}))
	defer srv.Close()
	g := &GitHub{BaseURL: srv.URL + "/", Token: "tok"}
	ctx := context.Background()
	prs, err := g.OpenPRs(ctx, "o", "r")
	if err != nil || len(prs) != 101 || prs[0].Repo != "r" || prs[0].HeadRef != "renovate/x" || prs[0].BaseSHA != "s" ||
		prs[0].Labels[0] != "needs-human" || prs[0].Created.Day() != 1 || prs[100].Title != "last" || prs[100].Labels != nil {
		t.Fatalf("OpenPRs = %d PRs, %v", len(prs), err)
	}
	for name, want := range map[string]bool{"ci / required": true, "pending": false, "other": false} {
		if failed, err := g.FailedCheck(ctx, "o", "r", "h", name); err != nil || failed != want {
			t.Errorf("FailedCheck(%s) = %t, %v; want %t", name, failed, err, want)
		}
	}
	if b, err := g.File(ctx, "o", "r", "h", ReleaseManifest); err != nil || string(b) != `{".":"2.0.0"}` {
		t.Errorf("File = %q, %v", b, err)
	}
	if b, err := g.File(ctx, "o", "r", "s", ReleaseManifest); err != nil || b != nil {
		t.Errorf("File(missing) = %q, %v", b, err)
	}
	if _, err := g.OpenPRs(ctx, "o", "denied"); err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "tok") {
		t.Errorf("OpenPRs(denied) error = %v", err)
	}
	if !strings.Contains(strings.Join(got, "\n"), "check_name=ci+%2F+required&filter=latest") {
		t.Errorf("requests:\n%s", strings.Join(got, "\n"))
	}
}
