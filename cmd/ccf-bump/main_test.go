package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/bump"
)

type fakeGH struct {
	failFor string // LatestFinal fails for this repo
	finals  map[string]string
	files   map[string]string // "repo@ref:path" -> content
	open    map[string]*bump.PR
	calls   []string
	nextPR  int
	autoErr error
}

func (f *fakeGH) LatestFinal(_ context.Context, repo string) (string, error) {
	if repo == f.failFor {
		return "", fmt.Errorf("boom")
	}
	return f.finals[repo], nil
}
func (f *fakeGH) TagTime(context.Context, string, string) (time.Time, error) {
	return time.Time{}, nil
}
func (f *fakeGH) File(_ context.Context, repo, ref, path string) ([]byte, error) {
	c, ok := f.files[repo+"@"+ref+":"+path]
	if !ok {
		return nil, fmt.Errorf("no %s@%s:%s", repo, ref, path)
	}
	return []byte(c), nil
}
func (f *fakeGH) DefaultBranch(context.Context, string) (string, error) { return "main", nil }
func (f *fakeGH) OpenPR(_ context.Context, repo, branch string) (*bump.PR, error) {
	return f.open[repo+" "+branch], nil
}
func (f *fakeGH) CreatePR(_ context.Context, repo, base, head, title, _ string) (*bump.PR, error) {
	f.nextPR++
	pr := &bump.PR{Number: f.nextPR, NodeID: fmt.Sprintf("PR_%d", f.nextPR), URL: fmt.Sprintf("https://example/%s/%d", repo, f.nextPR)}
	f.open[repo+" "+head] = pr
	f.calls = append(f.calls, fmt.Sprintf("create %s %s<-%s %q", repo, base, head, title))
	return pr, nil
}
func (f *fakeGH) UpdatePR(_ context.Context, repo string, n int, title, _ string) error {
	f.calls = append(f.calls, fmt.Sprintf("update %s#%d %q", repo, n, title))
	return nil
}
func (f *fakeGH) EnableAutoMerge(_ context.Context, id string) error {
	f.calls = append(f.calls, "automerge "+id)
	return f.autoErr
}

const testManifest = `repos:
  - {name: mock-api, kind: go-service, release: true}
  - {name: mock-agent, kind: go-service, depends_on: [mock-api], release: true}
  - {name: mock-ui, kind: ui, depends_on: [mock-api], release: true}
  - {name: mock-agent-action, kind: action, depends_on: [mock-agent], release: true}
  - {name: mock-plugin-1, kind: go-plugin, depends_on: [mock-agent], release: true}
  - {name: mock-plugin-policies-1, kind: policies, depends_on: [mock-agent], release: true}
  - {name: old, kind: go-lib, release: false}
`

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// setup makes a local clone and a bare "GitHub" remote per mock repo, from internal/bump fixtures.
func setup(t *testing.T) (root string, e env, gh *fakeGH, out *bytes.Buffer) {
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "ccf-release-bot[bot]")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "bot@example.com")
	}
	root = t.TempDir()
	fixtures := map[string]string{"mock-api": "", "mock-agent": "", "mock-ui": "ui", "mock-agent-action": "action",
		"mock-plugin-1": "plugin", "mock-plugin-policies-1": "policies"}
	for repo, fx := range fixtures {
		dir := filepath.Join(root, "clones", repo)
		if fx != "" {
			if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "internal", "bump", "testdata", fx))); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(repo+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "init", "--quiet", "-b", "main")
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "--quiet", "-m", "seed")
		git(t, root, "clone", "--quiet", "--bare", dir, filepath.Join(root, "remotes", repo+".git"))
	}
	if err := os.WriteFile(filepath.Join(root, "repos.yaml"), []byte(testManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	gh = &fakeGH{
		finals: map[string]string{"mock-api": "v0.1.0", "mock-agent": "v0.2.0", "mock-gooci": "v0.3.0", "gooci": "v0.0.7"},
		files:  map[string]string{"mock-agent@v0.2.0:go.mod": "module m\n\nrequire github.com/open-policy-agent/opa v1.15.0\n"},
		open:   map[string]*bump.PR{},
	}
	out = &bytes.Buffer{}
	e = env{gh: gh, getenv: func(string) string { return "" }, stdout: out,
		now: func() time.Time { return time.Date(2026, 10, 8, 4, 0, 0, 0, time.UTC) }, sleep: func(time.Duration) {},
		remote: func(_, repo string) string { return "file://" + filepath.Join(root, "remotes", repo+".git") }}
	return root, e, gh, out
}

func TestSyncAllPR(t *testing.T) {
	root, e, gh, out := setup(t)
	var slept []time.Duration
	e.sleep = func(d time.Duration) { slept = append(slept, d) }
	args := []string{"sync", "--all", "--manifest", filepath.Join(root, "repos.yaml"), "--pr", "--batch", "2"}
	if err := run(context.Background(), args, e); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := []string{
		`create mock-ui main<-ccf-bump/sync-2026-10-08 "fix(deps): bump mock-api to v0.1.0"`,
		"automerge PR_1",
		`create mock-agent-action main<-ccf-bump/sync-2026-10-08 "fix(deps): bump mock-agent to v0.2.0"`,
		`create mock-plugin-1 main<-ccf-bump/sync-2026-10-08 "fix(deps): bump mock-gooci to v0.3.0"`,
		"automerge PR_3",
		`create mock-plugin-policies-1 main<-ccf-bump/sync-2026-10-08 "fix(deps): bump opa to v1.15.0"`,
		"automerge PR_4",
	}
	if !slices.Equal(gh.calls, want) {
		t.Errorf("calls:\n%s\nwant:\n%s\noutput:\n%s", strings.Join(gh.calls, "\n"), strings.Join(want, "\n"), out)
	}
	if len(slept) != 1 || slept[0] != time.Hour {
		t.Errorf("slept %v, want one hour after the first batch of 2", slept)
	}
	for repo, line := range map[string]string{
		"mock-agent-action:Dockerfile":                         "FROM ghcr.io/compliance-framework/mock-agent:0.2.0 AS source",
		"mock-ui:src/api-version.json":                         `"mockApi": "v0.1.0"`,
		"mock-plugin-1:.github/workflows/release.yml":          "mock-gooci/cmd/mock-gooci@v0.3.0",
		"mock-plugin-policies-1:.github/workflows/release.yml": `opa-version: "1.15.0"`,
		"mock-plugin-policies-1:.github/workflows/preview.yml": "opa-version: ${{ vars.OPA_VERSION }}",
		"mock-plugin-1:.github/workflows/ci.yaml":              "@0123456789abcdef0123456789abcdef01234567",
	} {
		name, file, _ := strings.Cut(repo, ":")
		got := git(t, root, "--git-dir", filepath.Join("remotes", name+".git"), "show", "ccf-bump/sync-2026-10-08:"+file)
		if !strings.Contains(got, line) {
			t.Errorf("%s: want %q in:\n%s", repo, line, got)
		}
	}
	log := git(t, root, "--git-dir", filepath.Join("remotes", "mock-ui.git"), "log", "-1", "--format=%an %s", "ccf-bump/sync-2026-10-08")
	if log != "ccf-release-bot[bot] fix(deps): bump mock-api to v0.1.0\n" {
		t.Errorf("commit: %q", log)
	}

	// A second run the same day updates the open PRs instead of opening new ones.
	gh.calls = nil
	if err := run(context.Background(), []string{"sync", "--repos", "mock-ui", "--manifest", filepath.Join(root, "repos.yaml"), "--pr"}, e); err != nil {
		t.Fatal(err)
	}
	if want := []string{`update mock-ui#1 "fix(deps): bump mock-api to v0.1.0"`, "automerge PR_1"}; !slices.Equal(gh.calls, want) {
		t.Errorf("rerun calls %q, want %q", gh.calls, want)
	}
}

func TestTrainDryRun(t *testing.T) {
	root, e, gh, out := setup(t)
	args := []string{"--repo", "mock-agent-action", "--manifest", filepath.Join(root, "repos.yaml"), "--mode", "train",
		"--set", "mock-agent=0.3.0", "--workflows-ref", "v1", "--pr", "--dry-run", "--clones", filepath.Join(root, "clones")}
	if err := run(context.Background(), args, e); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, s := range []string{
		"bump      mock-agent   alpine:3.20 -> v0.3.0",
		"+FROM ghcr.io/compliance-framework/mock-agent:0.3.0 AS source",
		`dry run: would push ccf-bump/train-2026-10-08 and open "fix(deps): bump mock-agent to v0.3.0"`,
		"| [mock-agent](https://github.com/compliance-framework/mock-agent/releases/tag/v0.3.0) | `alpine:3.20` | `v0.3.0` |",
	} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if len(gh.calls) != 0 {
		t.Errorf("dry run called the API: %q", gh.calls)
	}
	if b := git(t, root, "--git-dir", filepath.Join("remotes", "mock-agent-action.git"), "branch", "--list", "ccf-bump/*"); b != "" {
		t.Errorf("dry run pushed %s", b)
	}
}

func TestListAndErrors(t *testing.T) {
	root, e, _, out := setup(t)
	m := filepath.Join(root, "repos.yaml")
	if err := run(context.Background(), []string{"list", "--manifest", m}, e); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "mock-api,mock-agent,mock-ui,mock-agent-action,mock-plugin-1,mock-plugin-policies-1\n" {
		t.Errorf("list = %q", got)
	}
	for args, want := range map[string]string{
		"sync":                               "one of --all or --repos",
		"sync --all --repos mock-ui":         "one of --all or --repos",
		"--mode nope --repo mock-ui":         "want sync or train",
		"":                                   "--repo is required",
		"--repo old":                         `"old" is not in the manifest with release: true`,
		"--set nope --repo mock-ui":          "want dep=version",
		"--set mock-api=main --repo mock-ui": "want a version",
		"--batch 5 --repo mock-ui":           "go with sync",
		"sync --all --mode train":            "sync runs in sync mode",
	} {
		err := run(context.Background(), append(strings.Fields(args), "--manifest", m), e)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", args, err, want)
		}
	}
}

func TestOneRepoFailing(t *testing.T) {
	root, e, gh, out := setup(t)
	gh.failFor = "mock-gooci" // only mock-plugin-1 pins it
	err := run(context.Background(), []string{"sync", "--all", "--manifest", filepath.Join(root, "repos.yaml"), "--pr"}, e)
	if err == nil || !strings.Contains(err.Error(), "mock-plugin-1: latest release of mock-gooci: boom") {
		t.Errorf("error = %v", err)
	}
	if !strings.Contains(out.String(), "::error::ccf-bump: mock-plugin-1:") {
		t.Errorf("no ::error:: line:\n%s", out)
	}
	var created int
	for _, c := range gh.calls {
		if strings.HasPrefix(c, "create ") {
			created++
		}
	}
	if created != 3 {
		t.Errorf("created %d PRs, want 3 (every repo but mock-plugin-1): %q", created, gh.calls)
	}
}

func TestWithoutTokens(t *testing.T) {
	got := withoutTokens([]string{"PATH=/bin", "GH_TOKEN=x", "GITHUB_TOKEN=y", "GH_TOKENS=z"})
	if !slices.Equal(got, []string{"PATH=/bin", "GH_TOKENS=z"}) {
		t.Errorf("withoutTokens = %q", got)
	}
}
