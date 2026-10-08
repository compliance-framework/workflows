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
	commits map[string]string // "repo@tag" -> commit SHA
	// authors: "repo@tag" -> who published the release (default ccf-release-bot[bot]); offMain:
	// commits that are not on the default branch.
	authors  map[string]string
	offMain  map[string]bool
	open     map[string]*bump.PR
	calls    []string
	nextPR   int
	autoErr  error // DisableAutoMerge
	closeErr error
	labelErr error
	anon     bool // CreatePR returns a PR without its author
	// checks: head SHA -> the required check's runs on each read (the last list repeats).
	checks    map[string][][]bump.CheckRun
	conflicts map[int]bool // PR number -> mergeable false
	computing map[int]int  // PR number -> reads that return mergeable null
	mergeErr  error
	mergeErrs []error                // returned by the first merges, before mergeErr
	listErr   error                  // OpenPRs
	changed   map[int]func(*bump.PR) // PR number -> a change seen by PullRequest only (after the listing)
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
func (f *fakeGH) TagCommit(_ context.Context, repo, tag string) (string, error) {
	c, ok := f.commits[repo+"@"+tag]
	if !ok {
		return "", fmt.Errorf("no tag %s %s", repo, tag)
	}
	return c, nil
}
func (f *fakeGH) ReleaseAuthor(_ context.Context, repo, tag string) (string, error) {
	if a, ok := f.authors[repo+"@"+tag]; ok {
		return a, nil
	}
	return "ccf-release-bot[bot]", nil
}
func (f *fakeGH) OnBranch(_ context.Context, _, sha, branch string) (bool, error) {
	return branch == "main" && !f.offMain[sha], nil
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

// newPR is an open PR from branch in owner/repo.
func newPR(n int, owner, repo, branch, login, typ string) *bump.PR {
	pr := &bump.PR{Number: n, NodeID: fmt.Sprintf("PR_%d", n), URL: fmt.Sprintf("https://example/%s/%d", repo, n), State: "open"}
	pr.User.Login, pr.User.Type, pr.Head.Ref = login, typ, branch
	pr.Head.Repo = &struct {
		FullName string `json:"full_name"`
	}{owner + "/" + repo}
	return pr
}

func (f *fakeGH) CreatePR(_ context.Context, repo, base, head, title, _ string) (*bump.PR, error) {
	f.nextPR++
	pr := newPR(f.nextPR, "compliance-framework", repo, head, "ccf-release-bot[bot]", "Bot")
	if f.anon {
		pr.User.Login = ""
	}
	f.open[repo+" "+head] = pr
	f.calls = append(f.calls, fmt.Sprintf("create %s %s<-%s %q", repo, base, head, title))
	return pr, nil
}
func (f *fakeGH) UpdatePR(_ context.Context, repo string, n int, title, _ string) error {
	f.calls = append(f.calls, fmt.Sprintf("update %s#%d %q", repo, n, title))
	return nil
}
func (f *fakeGH) DisableAutoMerge(_ context.Context, id string) error {
	f.calls = append(f.calls, "disable-automerge "+id)
	return f.autoErr
}
func (f *fakeGH) OpenPRs(_ context.Context, repo string) ([]bump.PR, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []bump.PR
	for k, pr := range f.open {
		if strings.HasPrefix(k, repo+" ") {
			out = append(out, *pr)
		}
	}
	slices.SortFunc(out, func(a, b bump.PR) int { return a.Number - b.Number })
	return out, nil
}
func (f *fakeGH) ClosePR(_ context.Context, repo string, n int, comment string) error {
	f.calls = append(f.calls, fmt.Sprintf("close %s#%d %q", repo, n, comment))
	if f.closeErr != nil {
		return f.closeErr
	}
	for k, pr := range f.open {
		if strings.HasPrefix(k, repo+" ") && pr.Number == n {
			delete(f.open, k)
		}
	}
	return nil
}
func (f *fakeGH) DeleteBranch(_ context.Context, repo, branch string) error {
	f.calls = append(f.calls, "delete "+repo+" "+branch)
	return nil
}
func (f *fakeGH) AddLabel(_ context.Context, repo string, n int, l bump.Label) error {
	f.calls = append(f.calls, fmt.Sprintf("label %s#%d %s", repo, n, l.Name))
	return f.labelErr
}
func (f *fakeGH) RemoveLabel(_ context.Context, repo string, n int, name string) error {
	f.calls = append(f.calls, fmt.Sprintf("unlabel %s#%d %s", repo, n, name))
	return nil
}
func (f *fakeGH) find(repo string, n int) *bump.PR {
	for k, pr := range f.open {
		if strings.HasPrefix(k, repo+" ") && pr.Number == n {
			return pr
		}
	}
	return nil
}
func (f *fakeGH) PullRequest(_ context.Context, repo string, n int) (*bump.PR, error) {
	pr := f.find(repo, n)
	if pr == nil {
		return nil, fmt.Errorf("no PR %s#%d", repo, n)
	}
	cp, ok := *pr, !f.conflicts[n]
	if f.computing[n] > 0 {
		f.computing[n]--
	} else {
		cp.Mergeable = &ok
	}
	if c := f.changed[n]; c != nil {
		c(&cp)
	}
	return &cp, nil
}
func (f *fakeGH) CheckRuns(_ context.Context, _, sha, name string) ([]bump.CheckRun, error) {
	if name != "ci / required" {
		return nil, fmt.Errorf("check %q", name)
	}
	reads := f.checks[sha]
	if len(reads) == 0 {
		return nil, nil
	}
	if len(reads) > 1 {
		f.checks[sha] = reads[1:]
	}
	return slices.Clone(reads[0]), nil
}
func (f *fakeGH) Merge(_ context.Context, repo string, n int, sha, title string) error {
	f.calls = append(f.calls, fmt.Sprintf("merge %s#%d %s %q", repo, n, sha, title))
	if len(f.mergeErrs) > 0 {
		err := f.mergeErrs[0]
		f.mergeErrs = f.mergeErrs[1:]
		return err
	}
	return f.mergeErr
}

const testManifest = `repos:
  - {name: mock-api, kind: go-service, release: true}
  - {name: mock-agent, kind: go-service, depends_on: [mock-api], release: true}
  - {name: mock-ui, kind: ui, depends_on: [mock-api], release: true}
  - {name: mock-agent-action, kind: action, depends_on: [mock-agent], release: true}
  - {name: mock-plugin-1, kind: go-plugin, depends_on: [mock-agent], release: true}
  - {name: mock-plugin-policies-1, kind: policies, depends_on: [mock-agent], release: true}
  - {name: old, kind: go-lib, release: false}
  - {name: workflows, kind: workflows, release: false}
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
		"label mock-ui#1 ccf-bump:automerge",
		`create mock-agent-action main<-ccf-bump/sync-2026-10-08 "fix(deps): bump mock-agent to v0.2.0"`,
		"label mock-agent-action#2 needs-human",
		`create mock-plugin-1 main<-ccf-bump/sync-2026-10-08 "fix(deps): bump mock-gooci to v0.3.0"`,
		"label mock-plugin-1#3 ccf-bump:automerge",
		`create mock-plugin-policies-1 main<-ccf-bump/sync-2026-10-08 "fix(deps): bump opa to v1.15.0"`,
		"label mock-plugin-policies-1#4 ccf-bump:automerge",
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
	if want := []string{`update mock-ui#1 "fix(deps): bump mock-api to v0.1.0"`, "label mock-ui#1 ccf-bump:automerge"}; !slices.Equal(gh.calls, want) {
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

func TestWorkflowsPins(t *testing.T) {
	const sha = "89abcdef0123456789abcdef0123456789abcdef"
	root, e, gh, out := setup(t)
	m := filepath.Join(root, "repos.yaml")
	gh.finals["workflows"], gh.commits = "v1.1.0", map[string]string{"workflows@v1.1.0": sha}
	if err := run(context.Background(), []string{"sync", "--repos", "mock-plugin-1", "--manifest", m, "--pr"}, e); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, s := range []string{
		"bump      workflows    0123456789abcdef0123456789abcdef01234567 -> " + sha + " # v1.1.0",
		"conflict  workflows    pinned v1.2.0 is newer than v1.1.0; ccf-bump never downgrades",
		"conflict  workflows    pinned to ra/some-branch, not main, a commit SHA or a release: a human decides",
		"auto-merge: off",
	} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if want := []string{`create mock-plugin-1 main<-ccf-bump/sync-2026-10-08 "fix(deps): bump mock-gooci to v0.3.0, workflows to v1.1.0"`,
		"label mock-plugin-1#1 needs-human"}; !slices.Equal(gh.calls, want) {
		t.Errorf("calls %q, want %q", gh.calls, want)
	}
	got := git(t, root, "--git-dir", filepath.Join("remotes", "mock-plugin-1.git"), "show", "ccf-bump/sync-2026-10-08:.github/workflows/ci.yaml")
	if !strings.Contains(got, "ci-go-plugin.yml@"+sha+" # v1.1.0 pinned\n") {
		t.Errorf("ci.yaml:\n%s", got)
	}

	// Train mode with a given release (with or without the v) resolves its commit too.
	out.Reset()
	args := []string{"--repo", "mock-plugin-1", "--mode", "train", "--set", "workflows=1.1.0", "--manifest", m, "--clones", filepath.Join(root, "clones")}
	if err := run(context.Background(), args, e); err != nil || !strings.Contains(out.String(), "-> "+sha+" # v1.1.0") {
		t.Errorf("train --set workflows=1.1.0: %v\n%s", err, out)
	}

	// A release someone published by hand, or cut from a commit that isn't on main, doesn't move
	// the pins: sync warns and leaves them, a given release fails.
	for _, untrusted := range []struct {
		name string
		set  func()
		why  string
	}{
		{"published by hand", func() { gh.authors = map[string]string{"workflows@v1.1.0": "mallory"} },
			"release v1.1.0 of workflows was published by mallory, not ccf-release-bot[bot]"},
		{"off main", func() { gh.authors, gh.offMain = nil, map[string]bool{sha: true} },
			"release v1.1.0 of workflows is at " + sha + ", which is not on main"},
	} {
		untrusted.set()
		out.Reset()
		gh.finals["workflows"] = "v1.1.0"
		args := []string{"sync", "--repos", "mock-plugin-1", "--manifest", m, "--clones", filepath.Join(root, "clones")}
		if err := run(context.Background(), args, e); err != nil {
			t.Fatalf("%s: sync: %v\n%s", untrusted.name, err, out)
		}
		if !strings.Contains(out.String(), "::warning::ccf-bump: workflows pins left as they are: "+untrusted.why) ||
			!strings.Contains(out.String(), "no target workflows    3 pin(s) left as they are") {
			t.Errorf("%s: sync output:\n%s", untrusted.name, out)
		}
		out.Reset()
		args = []string{"--repo", "mock-plugin-1", "--mode", "train", "--set", "workflows=v1.1.0", "--manifest", m, "--clones", filepath.Join(root, "clones")}
		if err := run(context.Background(), args, e); err == nil || !strings.Contains(err.Error(), untrusted.why) {
			t.Errorf("%s: train --set workflows=v1.1.0: %v\n%s", untrusted.name, err, out)
		}
	}
	gh.authors, gh.offMain = nil, nil

	// Train mode moves the pins only when asked; sync mode without a workflows release leaves them.
	for _, args := range [][]string{
		{"--repo", "mock-plugin-1", "--mode", "train", "--set", "mock-gooci=0.3.0"},
		{"sync", "--repos", "mock-plugin-1"},
	} {
		out.Reset()
		gh.finals["workflows"] = ""
		if err := run(context.Background(), append(args, "--manifest", m, "--clones", filepath.Join(root, "clones")), e); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "no target workflows    3 pin(s) left as they are") {
			t.Errorf("%q: output:\n%s", args, out)
		}
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

func TestLabelFailureWarns(t *testing.T) {
	root, e, gh, out := setup(t)
	gh.labelErr = fmt.Errorf("403 Forbidden")
	summary := filepath.Join(root, "summary.md")
	e.getenv = func(k string) string { return map[string]string{"GITHUB_STEP_SUMMARY": summary}[k] }
	if err := run(context.Background(), []string{"sync", "--all", "--manifest", filepath.Join(root, "repos.yaml"), "--pr"}, e); err != nil {
		t.Fatalf("run failed on a label error: %v\n%s", err, out)
	}
	if n := strings.Count(strings.Join(gh.calls, "\n"), "create "); n != 4 {
		t.Errorf("created %d PRs, want 4: %q", n, gh.calls)
	}
	want := "mock-ui#1: label ccf-bump:automerge: 403 Forbidden"
	if !strings.Contains(out.String(), "::warning::ccf-bump: "+want) {
		t.Errorf("no ::warning:: line:\n%s", out)
	}
	got, err := os.ReadFile(summary)
	if err != nil || !strings.Contains(string(got), "### ccf-bump warnings") || !strings.Contains(string(got), "- "+want) {
		t.Errorf("step summary %q, %v", got, err)
	}
}

// stalePRs seeds open PRs (keyed "base-repo head") that ccf-bump on mock-ui must leave alone, but
// for #50, its own earlier sync PR.
func stalePRs(gh *fakeGH) {
	const o, bot = "compliance-framework", "ccf-release-bot[bot]"
	gh.open = map[string]*bump.PR{
		"mock-ui ccf-bump/sync-2026-09-22":           newPR(50, o, "mock-ui", "ccf-bump/sync-2026-09-22", bot, "Bot"),
		"mock-ui ccf-bump/sync-2026-09-08":           newPR(51, o, "mock-ui", "ccf-bump/sync-2026-09-08", "someone", "User"),
		"mock-ui renovate/go":                        newPR(52, o, "mock-ui", "renovate/go", "renovate[bot]", "Bot"),
		"mock-ui fork:ccf-bump/sync-2026-09-01":      newPR(53, "fork", "mock-ui", "ccf-bump/sync-2026-09-01", bot, "Bot"),
		"mock-ui ccf-bump/train-2026-09-30":          newPR(54, o, "mock-ui", "ccf-bump/train-2026-09-30", bot, "Bot"),
		"mock-agent-action ccf-bump/sync-2026-09-22": newPR(55, o, "mock-agent-action", "ccf-bump/sync-2026-09-22", bot, "Bot"),
	}
	gh.nextPR = 99
}

func TestSupersededPRsClosed(t *testing.T) {
	root, e, gh, out := setup(t)
	stalePRs(gh)
	if err := run(context.Background(), []string{"sync", "--repos", "mock-ui", "--manifest", filepath.Join(root, "repos.yaml"), "--pr"}, e); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := []string{
		`create mock-ui main<-ccf-bump/sync-2026-10-08 "fix(deps): bump mock-api to v0.1.0"`,
		`close mock-ui#50 "Superseded by #100."`,
		"delete mock-ui ccf-bump/sync-2026-09-22",
		"label mock-ui#100 ccf-bump:automerge",
	}
	if !slices.Equal(gh.calls, want) {
		t.Errorf("calls:\n%s\nwant:\n%s\noutput:\n%s", strings.Join(gh.calls, "\n"), strings.Join(want, "\n"), out)
	}
	if !strings.Contains(out.String(), "closed #50 (ccf-bump/sync-2026-09-22): superseded by #100") {
		t.Errorf("output:\n%s", out)
	}
}

func TestSupersededPRsWarnings(t *testing.T) {
	for name, tc := range map[string]struct {
		fake  func(*fakeGH)
		warn  string
		calls []string
	}{
		"close fails": {
			fake: func(f *fakeGH) { f.closeErr = fmt.Errorf("403 Forbidden") },
			warn: "mock-ui#50: close superseded PR: 403 Forbidden",
			calls: []string{`create mock-ui main<-ccf-bump/sync-2026-10-08 "fix(deps): bump mock-api to v0.1.0"`,
				`close mock-ui#50 "Superseded by #100."`, "label mock-ui#100 ccf-bump:automerge"},
		},
		"author unknown": {
			fake:  func(f *fakeGH) { f.anon = true },
			warn:  "mock-ui#100: author unknown; earlier ccf-bump PRs left open",
			calls: []string{`create mock-ui main<-ccf-bump/sync-2026-10-08 "fix(deps): bump mock-api to v0.1.0"`, "label mock-ui#100 ccf-bump:automerge"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, e, gh, out := setup(t)
			stalePRs(gh)
			tc.fake(gh)
			if err := run(context.Background(), []string{"sync", "--repos", "mock-ui", "--manifest", filepath.Join(root, "repos.yaml"), "--pr"}, e); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !slices.Equal(gh.calls, tc.calls) {
				t.Errorf("calls %q, want %q", gh.calls, tc.calls)
			}
			if !strings.Contains(out.String(), "::warning::ccf-bump: "+tc.warn) {
				t.Errorf("no warning %q:\n%s", tc.warn, out)
			}
		})
	}
}

func TestSupersededPRsDryRun(t *testing.T) {
	root, e, gh, out := setup(t)
	stalePRs(gh)
	if err := run(context.Background(), []string{"sync", "--repos", "mock-ui", "--manifest", filepath.Join(root, "repos.yaml"), "--pr", "--dry-run"}, e); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(gh.calls) != 0 {
		t.Errorf("dry run wrote: %q", gh.calls)
	}
	if !strings.Contains(out.String(), "dry run: would close #50 (ccf-bump/sync-2026-09-22, by ccf-release-bot[bot]) as superseded") {
		t.Errorf("output:\n%s", out)
	}
	if n := strings.Count(out.String(), "would close"); n != 1 {
		t.Errorf("would close %d PRs, want only #50:\n%s", n, out)
	}
}

func TestWithoutTokens(t *testing.T) {
	got := withoutTokens([]string{"PATH=/bin", "GH_TOKEN=x", "GITHUB_TOKEN=y", "GH_TOKENS=z"})
	if !slices.Equal(got, []string{"PATH=/bin", "GH_TOKENS=z"}) {
		t.Errorf("withoutTokens = %q", got)
	}
}

func TestLabels(t *testing.T) {
	automerge := []bump.PRLabel{{Name: bump.AutomergeLabel}}
	for name, tc := range map[string]struct {
		repo  string
		open  *bump.PR // the PR an earlier run opened on today's branch
		fake  func(*fakeGH)
		calls []string
		out   string
	}{
		"eligible": {
			repo: "mock-ui", calls: []string{"label mock-ui#1 ccf-bump:automerge"}, out: "  label: ccf-bump:automerge\n",
		},
		"label fails": {
			repo: "mock-ui", fake: func(f *fakeGH) { f.labelErr = fmt.Errorf("403 Forbidden") },
			calls: []string{"label mock-ui#1 ccf-bump:automerge", "label mock-ui#1 needs-human"},
			out:   "::warning::ccf-bump: mock-ui#1: label needs-human: 403 Forbidden",
		},
		"major": {
			repo: "mock-agent-action", calls: []string{"label mock-agent-action#1 needs-human"}, out: "auto-merge: off",
		},
		"major, earlier run eligible": {
			repo: "mock-agent-action", open: &bump.PR{Number: 7, Labels: automerge},
			calls: []string{"unlabel mock-agent-action#7 ccf-bump:automerge", "label mock-agent-action#7 needs-human"},
		},
		"GitHub auto-merge from an older run": {
			repo: "mock-ui", open: &bump.PR{Number: 7, NodeID: "PR_7", AutoMerge: map[string]any{}},
			calls: []string{"disable-automerge PR_7", "label mock-ui#7 ccf-bump:automerge"}, out: "GitHub auto-merge: disabled",
		},
		"disabling GitHub auto-merge fails": {
			repo: "mock-ui", open: &bump.PR{Number: 7, NodeID: "PR_7", AutoMerge: map[string]any{}},
			fake:  func(f *fakeGH) { f.autoErr = fmt.Errorf("disable auto-merge: nope") },
			calls: []string{"disable-automerge PR_7", "label mock-ui#7 ccf-bump:automerge"},
			out:   "::warning::ccf-bump: mock-ui#7: disable auto-merge: nope",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, e, gh, out := setup(t)
			if tc.open != nil {
				gh.open[tc.repo+" ccf-bump/sync-2026-10-08"] = tc.open
			}
			if tc.fake != nil {
				tc.fake(gh)
			}
			args := []string{"sync", "--repos", tc.repo, "--manifest", filepath.Join(root, "repos.yaml"), "--pr"}
			if err := run(context.Background(), args, e); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if got := gh.calls[1:]; !slices.Equal(got, tc.calls) { // [0] is the create or update
				t.Errorf("calls %q, want %q", got, tc.calls)
			}
			if !strings.Contains(out.String(), tc.out) {
				t.Errorf("output lacks %q:\n%s", tc.out, out)
			}
		})
	}
}

func TestNeedsHumanDryRun(t *testing.T) {
	root, e, gh, out := setup(t)
	args := []string{"--repo", "mock-agent-action", "--manifest", filepath.Join(root, "repos.yaml"), "--pr", "--dry-run"}
	if err := run(context.Background(), args, e); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(gh.calls) != 0 {
		t.Errorf("dry run called the API: %q", gh.calls)
	}
	if s := "dry run: would add label needs-human"; !strings.Contains(out.String(), s) {
		t.Errorf("output lacks %q:\n%s", s, out)
	}
}

func TestPRTitleType(t *testing.T) {
	const sha = "89abcdef0123456789abcdef0123456789abcdef"
	wf := func(file string) bump.Change {
		return bump.Change{Ref: bump.Ref{Dep: bump.DepWorkflows, File: file, Current: "main"}, To: bump.WorkflowsPin(sha, "v1.1.0")}
	}
	gomod := bump.Change{Ref: bump.Ref{Dep: "mock-api", File: "go.mod", Current: "v0.1.0"}, To: "v0.2.0"}
	for name, tc := range map[string]struct {
		changes []bump.Change
		want    string
	}{
		"only workflow pins":    {[]bump.Change{wf(".github/workflows/ci.yml"), wf(".github/workflows/release.yml")}, "ci(deps): bump workflows to v1.1.0"},
		"a runtime dep too":     {[]bump.Change{wf(".github/workflows/ci.yml"), gomod}, "fix(deps): bump mock-api to v0.2.0, workflows to v1.1.0"},
		"only a runtime dep":    {[]bump.Change{gomod}, "fix(deps): bump mock-api to v0.2.0"},
		"OPA counts as runtime": {[]bump.Change{{Ref: bump.Ref{Dep: bump.DepOPA, File: ".github/workflows/release.yml"}, To: "v1.15.0"}, wf("x")}, "fix(deps): bump opa to v1.15.0, workflows to v1.1.0"},
	} {
		if title, _ := prText(options{owner: "compliance-framework", mode: "sync"}, bump.Plan{Changes: tc.changes}); title != tc.want {
			t.Errorf("%s: title %q, want %q", name, title, tc.want)
		}
	}
}
