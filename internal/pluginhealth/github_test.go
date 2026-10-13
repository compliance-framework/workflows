package pluginhealth

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// fakeAPI answers GETs from routes (path?query -> JSON) and records the writes.
func fakeAPI(t *testing.T, routes map[string]string, writes *[]string) *GitHub {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("%s %s without the token", r.Method, r.URL)
		}
		if r.Method != http.MethodGet {
			b, _ := io.ReadAll(r.Body)
			*writes = append(*writes, r.Method+" "+r.URL.Path+" "+string(b))
			fmt.Fprint(w, `{"number":5,"node_id":"I_5","html_url":"u5","data":{}}`)
			return
		}
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		body, ok := routes[key]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return &GitHub{BaseURL: srv.URL, Token: "tok", Owner: "o", Repo: "o/workflows"}
}

func TestGitHubSource(t *testing.T) {
	ctx := context.Background()
	g := fakeAPI(t, map[string]string{
		"/repos/o/p1/releases/latest":            `{"tag_name":"v1.2.0","published_at":"2026-09-01T10:00:00Z"}`,
		"/repos/o/p1/contents/go.mod?ref=v1.2.0": `{"content":"bW9kdWxlIHgK"}`,
		"/repos/o/p1/pulls?direction=desc&per_page=100&sort=created&state=all": `[
			{"number":9,"state":"open","head":{"ref":"renovate/x","sha":"s9","repo":{"full_name":"o/p1"}}},
			{"number":8,"state":"open","head":{"ref":"ccf-bump/sync-2026-10-08","sha":"s8","repo":{"full_name":"fork/p1"}}},
			{"number":7,"state":"open","html_url":"u7","head":{"ref":"ccf-bump/sync-2026-10-08","sha":"s7","repo":{"full_name":"o/p1"}}},
			{"number":6,"state":"closed","merged_at":"2026-09-22T00:00:00Z","head":{"ref":"ccf-bump/sync-2026-09-22","sha":"s6","repo":{"full_name":"o/p1"}}}]`,
		"/repos/o/p1/commits/s7/check-runs?check_name=ci+%2F+required&filter=all&page=1&per_page=100": `{"check_runs":[
			{"id":1,"name":"ci / required","status":"completed","conclusion":"success","started_at":"2026-10-08T01:00:00Z"},
			{"id":2,"name":"ci / required","status":"completed","conclusion":"failure","started_at":"2026-10-08T02:00:00Z"}]}`,
		"/repos/o/p2/pulls?direction=desc&per_page=100&sort=created&state=all": `[]`,
	}, nil)

	rel, err := g.LatestRelease(ctx, "p1")
	if err != nil || rel.Tag != "v1.2.0" || rel.Date.Day() != 1 {
		t.Errorf("LatestRelease = %+v, %v", rel, err)
	}
	if rel, err := g.LatestRelease(ctx, "p2"); rel != nil || err != nil {
		t.Errorf("LatestRelease without one = %+v, %v; want nil, nil", rel, err)
	}
	if b, err := g.File(ctx, "p1", "v1.2.0", "go.mod"); string(b) != "module x\n" || err != nil {
		t.Errorf("File = %q, %v", b, err)
	}
	if b, err := g.File(ctx, "p1", "v1.2.0", "main.go"); b != nil || err != nil {
		t.Errorf("File of a missing file = %q, %v; want nil, nil", b, err)
	}
	pr, err := g.LatestBumpPR(ctx, "p1")
	if err != nil || pr.Number != 7 || pr.Required != "failure" || pr.Result() != "failed" {
		t.Errorf("LatestBumpPR = %+v, %v; want #7, failed", pr, err)
	}
	if pr, err := g.LatestBumpPR(ctx, "p2"); pr != nil || err != nil {
		t.Errorf("LatestBumpPR without one = %+v, %v", pr, err)
	}
}

func TestGitHubTracker(t *testing.T) {
	ctx := context.Background()
	var writes []string
	g := fakeAPI(t, map[string]string{
		"/repos/o/workflows/issues?page=1&per_page=100&state=open": `[
			{"number":3,"title":"Plugin health","body":"spoof","user":{"login":"someone"}},
			{"number":2,"title":"Plugin health","body":"mine","user":{"login":"github-actions[bot]"}},
			{"number":1,"title":"Plugin health","pull_request":{},"user":{"login":"github-actions[bot]"}}]`,
		"/repos/o/workflows/issues?direction=desc&labels=train&per_page=30&sort=updated&state=closed": `[
			{"number":40,"closed_at":"2026-09-10T00:00:00Z"},
			{"number":41,"closed_at":"2026-10-10T00:00:00Z"},
			{"number":39,"closed_at":"2026-08-10T00:00:00Z"}]`,
	}, &writes)

	if is, err := g.FindIssue(ctx, "Plugin health", IssueAuthor); err != nil || is == nil || is.Number != 2 {
		t.Errorf("FindIssue = %+v, %v; want #2", is, err)
	}
	if n, err := g.LastTrain(ctx); n != 41 || err != nil {
		t.Errorf("LastTrain = %d, %v", n, err)
	}
	if is, err := g.CreateIssue(ctx, "Plugin health", "b"); err != nil || is.NodeID != "I_5" {
		t.Errorf("CreateIssue = %+v, %v", is, err)
	}
	if err := g.EditIssue(ctx, 2, "b2"); err != nil {
		t.Error(err)
	}
	if err := g.PinIssue(ctx, "I_5"); err != nil {
		t.Error(err)
	}
	want := []string{
		`POST /repos/o/workflows/issues {"body":"b","title":"Plugin health"}`,
		`PATCH /repos/o/workflows/issues/2 {"body":"b2"}`,
		`POST /graphql {"query":"mutation($id: ID!) { pinIssue(input: {issueId: $id}) { issue { number } } }","variables":{"id":"I_5"}}`,
	}
	if strings.Join(writes, "\n") != strings.Join(want, "\n") {
		t.Errorf("writes:\n%s\nwant:\n%s", strings.Join(writes, "\n"), strings.Join(want, "\n"))
	}
}

func TestRegistryAnnotations(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}})
	idx = mutate.Annotations(idx, map[string]string{"org.ccf.plugin.protocol.version": "2"}).(v1.ImageIndex)
	ref, err := name.NewTag(host + "/o/p1:latest")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	ann, found, err := Registry{}.Annotations(ctx, host+"/o/p1:latest")
	if err != nil || !found || ann["org.ccf.plugin.protocol.version"] != "2" {
		t.Errorf("Annotations = %v, %v, %v", ann, found, err)
	}
	if ann, found, err := (Registry{}).Annotations(ctx, host+"/o/p2:latest"); found || err != nil || ann != nil {
		t.Errorf("Annotations of a missing tag = %v, %v, %v; want not found", ann, found, err)
	}
}
