package train

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// world is a fake GitHub (the repos and the tracking issues), Slack and ccf-bump. tick plays
// release-please and the release workflows.
type world struct {
	t        *testing.T
	repos    map[string]*fakeRepo
	issues   []*Issue
	slack    []string // "thread|text"; the parent's ts is "ts-1"
	bumps    []string // "repo sets dry"
	merges   []string // "repo#n"
	releases []string // repos, in the order their release PRs merged
	id       int64
}

type fakeRepo struct {
	name      string
	main      string
	manifests map[string]map[string]string // sha -> release-please manifest
	prs       map[int]*PR
	checks    map[string][]Check
	runs      map[string][]Run // event+sha -> runs
	tags      map[string][]string
	rpDone    string // the main sha release-please last ran on
	// next is the version release-please proposes for unreleased commits (default: a patch).
	next string
	// failChecks fails the checks of the release PR's head; failRelease the release run.
	failChecks, failRelease bool
	bumped                  bool // ccf-bump finds pins to move
}

func newWorld(t *testing.T) *world {
	return &world{t: t, repos: map[string]*fakeRepo{}}
}

func (w *world) nextID() int64 { w.id++; return w.id }

// repo adds a repo at version with nothing to release yet.
func (w *world) repo(name, version string) *fakeRepo {
	r := &fakeRepo{name: name, main: name + "-0", manifests: map[string]map[string]string{}, prs: map[int]*PR{},
		checks: map[string][]Check{}, runs: map[string][]Run{}, tags: map[string][]string{}}
	r.manifests[r.main] = map[string]string{RootPackage: version}
	r.rpDone = r.main
	r.runs["push"+r.main] = []Run{{ID: w.nextID(), Path: ".github/workflows/release-please.yml", Status: "completed", Conclusion: "success"}}
	w.repos[name] = r
	return r
}

// pending gives r unreleased commits: release-please opens a PR for version on the next tick.
func (r *fakeRepo) pending(version string) *fakeRepo {
	r.next, r.rpDone = version, ""
	delete(r.runs, "push"+r.main)
	return r
}

func (w *world) get(name string) *fakeRepo { return w.repos[name] }

func (r *fakeRepo) openPR(prefix string) *PR {
	for _, n := range slices.Sorted(maps.Keys(r.prs)) {
		if pr := r.prs[n]; pr.Open && strings.HasPrefix(pr.URL, prefix) {
			return pr
		}
	}
	return nil
}

func (w *world) green(r *fakeRepo, sha string, ok bool) {
	c := "success"
	if !ok {
		c = "failure"
	}
	r.checks[sha] = append(r.checks[sha], Check{ID: w.nextID(), Name: "ci / required", Status: "completed", Conclusion: c})
}

// tick plays release-please on every repo whose main moved, and the release workflows of
// tagged releases.
func (w *world) tick() {
	for _, r := range w.repos {
		if r.rpDone != r.main {
			r.rpDone = r.main
			r.runs["push"+r.main] = append(r.runs["push"+r.main], Run{ID: w.nextID(), Path: ".github/workflows/release-please.yml", Status: "completed", Conclusion: "success"})
			if r.next == "" {
				r.next = nextPatch(r.manifests[r.main][RootPackage])
			}
			head := r.name + "-rp-" + r.main
			r.manifests[head] = map[string]string{RootPackage: r.next}
			pr := r.openPR(ReleaseBranchPrefix)
			if pr == nil {
				pr = &PR{Number: int(w.nextID()), URL: ReleaseBranchPrefix + "main", Open: true}
				r.prs[pr.Number] = pr
			}
			pr.HeadSHA, pr.BaseSHA = head, r.main
			w.green(r, head, !r.failChecks)
		}
		for sha, tags := range r.tags {
			for _, tag := range tags {
				if !slices.ContainsFunc(r.runs["release"+sha], func(run Run) bool { return run.HeadBranch == tag }) {
					c := "success"
					if r.failRelease {
						c = "failure"
					}
					r.runs["release"+sha] = append(r.runs["release"+sha], Run{ID: w.nextID(), Path: ".github/workflows/release.yml", HeadBranch: tag, Status: "completed", Conclusion: c, URL: "run-url"})
				}
			}
		}
	}
}

// Repos.

func (w *world) DefaultBranch(_ context.Context, repo string) (string, string, error) {
	return "main", w.get(repo).main, nil
}

func (w *world) File(_ context.Context, repo, ref, path string) ([]byte, error) {
	m, ok := w.get(repo).manifests[ref]
	if !ok || path != ReleasePleaseManifestFile {
		return nil, nil
	}
	return json.Marshal(m)
}

func (w *world) OpenPR(_ context.Context, repo, prefix string) (*PR, error) {
	if pr := w.get(repo).openPR(prefix); pr != nil {
		c := *pr
		return &c, nil
	}
	return nil, nil
}

func (w *world) PR(_ context.Context, repo string, n int) (*PR, error) {
	pr, ok := w.get(repo).prs[n]
	if !ok {
		return nil, fmt.Errorf("no PR %s#%d", repo, n)
	}
	c := *pr
	return &c, nil
}

func (w *world) BehindBy(_ context.Context, repo, base, head string) (int, error) {
	if strings.HasSuffix(head, "-rp-"+base) {
		return 0, nil
	}
	return 1, nil
}

func (w *world) Checks(_ context.Context, repo, sha string) ([]Check, error) {
	return w.get(repo).checks[sha], nil
}

func (w *world) Merge(_ context.Context, repo string, n int, sha string) error {
	r := w.get(repo)
	pr := r.prs[n]
	if pr == nil || !pr.Open || pr.HeadSHA != sha {
		return fmt.Errorf("can't merge %s#%d at %s", repo, n, sha)
	}
	w.merges = append(w.merges, fmt.Sprintf("%s#%d", repo, n))
	pr.Open, pr.Merged, pr.MergeSHA = false, true, fmt.Sprintf("%s-m%d", repo, n)
	r.manifests[pr.MergeSHA] = r.manifests[pr.HeadSHA]
	if r.manifests[pr.MergeSHA] == nil {
		r.manifests[pr.MergeSHA] = r.manifests[r.main]
	}
	release := strings.HasPrefix(pr.URL, ReleaseBranchPrefix)
	r.main = pr.MergeSHA
	if release {
		// release-please tags the merge commit and has nothing left to release.
		r.rpDone, r.next = r.main, ""
		r.runs["push"+r.main] = append(r.runs["push"+r.main], Run{ID: w.nextID(), Path: ".github/workflows/release-please.yml", Status: "completed", Conclusion: "success"})
		tag := "v" + r.manifests[r.main][RootPackage]
		r.tags[r.main] = []string{tag}
		w.releases = append(w.releases, repo)
	}
	return nil
}

func (w *world) Runs(_ context.Context, repo, event, sha string) ([]Run, error) {
	return w.get(repo).runs[event+sha], nil
}

func (w *world) TagsAt(_ context.Context, repo, sha string) ([]string, error) {
	return slices.Clone(w.get(repo).tags[sha]), nil
}

func (w *world) Release(context.Context, string, string) (string, string, error) { return "", "", nil }

func (w *world) RerunFailed(context.Context, string, int64) error { return nil }

func (w *world) EnsureIssue(context.Context, string, string, string) (string, error) { return "", nil }

// Bump plays ccf-bump: with a repo's bumped set, it opens a green bump PR with auto-merge.
func (w *world) Bump(_ context.Context, path, repo string, sets map[string]string, dryRun bool) (BumpResult, error) {
	var s []string
	for _, k := range slices.Sorted(maps.Keys(sets)) {
		s = append(s, k+"="+sets[k])
	}
	w.bumps = append(w.bumps, fmt.Sprintf("%s %s dry=%v", repo, strings.Join(s, ","), dryRun))
	r := w.get(repo)
	if !r.bumped {
		return BumpResult{Output: "nothing pinned"}, nil
	}
	if dryRun {
		return BumpResult{Changed: true, Output: "dry run: would push"}, nil
	}
	pr := r.openPR("ccf-bump/")
	if pr == nil {
		pr = &PR{Number: int(w.nextID()), URL: "ccf-bump/train-x", Open: true, AutoMerge: true, HeadSHA: fmt.Sprintf("%s-bump%d", repo, w.id)}
		r.prs[pr.Number] = pr
		w.green(r, pr.HeadSHA, true)
	}
	return BumpResult{PR: pr.Number, Changed: true}, nil
}

// Tracker.

func (w *world) Issues(_ context.Context, label string) ([]Issue, error) {
	var out []Issue
	for _, is := range w.issues {
		if slices.Contains(is.Labels, label) {
			out = append(out, *is)
		}
	}
	return out, nil
}

func (w *world) CreateIssue(_ context.Context, title, body string, labels []string) (Issue, error) {
	n := len(w.issues) + 1
	is := &Issue{Number: n, Title: title, Body: body, URL: fmt.Sprintf("https://github.com/o/workflows/issues/%d", n), Open: true, Labels: labels}
	w.issues = append(w.issues, is)
	return *is, nil
}

func (w *world) EditIssue(_ context.Context, n int, body string, labels []string, closed bool) error {
	is := w.issues[n-1]
	is.Body, is.Labels, is.Open = body, labels, !closed
	return nil
}

func (w *world) Comments(context.Context, int, int64) ([]Comment, error) { return nil, nil }

func (w *world) Comment(context.Context, int, string) (string, error) { return "", nil }

// Slack.

func (w *world) Post(_ context.Context, channel, text, thread string) (string, error) {
	w.slack = append(w.slack, thread+"|"+text)
	return fmt.Sprintf("ts-%d", len(w.slack)), nil
}

func (w *world) state(n int) *State {
	st, err := Parse(w.issues[n-1].Body)
	if err != nil {
		w.t.Fatal(err)
	}
	return st
}

// engine returns an Engine on w at now, with the manifest files of the repo root.
func (w *world) engine(now time.Time) *Engine {
	return &Engine{
		Repos: w, Tracker: w, Slack: w, Bumper: w,
		Load:                  func(p string) (*manifest.Manifest, error) { return manifest.Load("../../" + p) },
		Channel:               "C1",
		RequiredCheck:         "ci / required",
		ReleasePleaseWorkflow: "release-please.yml",
		RunURL:                "https://run",
		Now:                   func() time.Time { return now },
	}
}
