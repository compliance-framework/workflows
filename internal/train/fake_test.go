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

	"golang.org/x/mod/semver"

	"github.com/compliance-framework/workflows/internal/bump"
	"github.com/compliance-framework/workflows/internal/manifest"
	"github.com/compliance-framework/workflows/internal/slackkit"
)

// world is a fake GitHub (the repos and the tracking issues), Slack and ccf-bump. tick plays
// release-please and the release workflows.
type world struct {
	t        *testing.T
	repos    map[string]*fakeRepo
	issues   []*Issue
	comments map[int][]Comment
	admins   []string
	slack    []string           // "thread|text" in the releases channel; the parent's ts is "ts-1"
	updates  []string           // "channel|ts|text" of chat.update
	digests  []slackkit.Message // posted to another channel (the digest channel)
	bumps    []string           // "repo sets dry"
	merges   []string           // "repo#n"
	releases []string           // repos, in the order their release PRs merged
	reruns   []int64
	// lagReruns: a re-run shows up on the next tick, not right after RerunFailed (GitHub lists the
	// failed attempt for a while); lagged holds those re-runs.
	lagReruns bool
	lagged    []int64
	external  []string // issues opened in repos
	id        int64
	// manualBumps: ccf-bump leaves the automerge label off; closedBumps: someone closes the bump PRs.
	manualBumps, closedBumps bool
	mergeErrs                []error // returned by the first merges
	// The gate's runs: probeFail fails the probe of a repo, v1 makes its plugin speak protocol v1,
	// smokeFail fails stack-smoke.
	gateRuns           []*gateRun
	probeFail, v1      map[string]bool
	smokeFail, noRunID bool     // noRunID: dispatches return no run ID
	events             []string // "bump <repo>", "dispatch <workflow>", "done <run id>", in order
}

type gateRun struct {
	workflow string
	inputs   map[string]string
	run      Run
	log      string
}

type fakeRepo struct {
	name      string
	main      string
	manifests map[string]map[string]string // sha -> release-please manifest
	prs       map[int]*PR
	checks    map[string][]Check
	runs      map[string][]Run // event+sha -> runs
	tags      map[string][]string
	notes     map[string]string // tag -> release notes
	rpDone    string            // the main sha release-please last ran on
	// next is the version release-please proposes for unreleased commits (default: a patch).
	next string
	// failChecks fails the checks of the release PR's head; failRelease the release run.
	failChecks, failRelease bool
	failReleasePlease       bool
	bumped                  bool // ccf-bump finds pins to move
	// finals are final releases made outside the train (LatestFinal also reads the tags); lookups
	// counts the LatestFinal calls.
	finals  []string
	lookups int
}

func newWorld(t *testing.T) *world {
	return &world{t: t, repos: map[string]*fakeRepo{}, comments: map[int][]Comment{}, admins: []string{"owner"},
		probeFail: map[string]bool{}, v1: map[string]bool{}}
}

func (w *world) nextID() int64 { w.id++; return w.id }

// repo adds a repo at version with nothing to release yet.
func (w *world) repo(name, version string) *fakeRepo {
	r := &fakeRepo{name: name, main: name + "-0", manifests: map[string]map[string]string{}, prs: map[int]*PR{},
		checks: map[string][]Check{}, runs: map[string][]Run{}, tags: map[string][]string{}, notes: map[string]string{}}
	r.manifests[r.main] = map[string]string{RootPackage: version}
	r.rpDone = r.main
	r.runs["push"+r.main] = []Run{{ID: w.nextID(), Path: ".github/workflows/release-please.yml", Status: "completed", Conclusion: "success", Attempt: 1}}
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
		if pr := r.prs[n]; pr.Open && !pr.Fork && strings.HasPrefix(pr.URL, prefix) {
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

// tick plays release-please on every repo whose main moved, the release workflows of
// tagged releases, and the gate's runs.
func (w *world) tick() {
	for _, id := range w.lagged {
		w.rerun(id)
	}
	w.lagged = nil
	for _, g := range w.gateRuns {
		if g.run.Status != "completed" {
			w.finishGateRun(g)
		}
	}
	for _, r := range w.repos {
		if r.rpDone != r.main {
			r.rpDone = r.main
			c := "success"
			if r.failReleasePlease {
				c = "failure"
			}
			r.runs["push"+r.main] = append(r.runs["push"+r.main], Run{ID: w.nextID(), Path: ".github/workflows/release-please.yml", Status: "completed", Conclusion: c, Attempt: 1})
			if r.next == "" {
				r.next = "0.1.1" // a patch: the repos the tests bump start at 0.1.0
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
			r.checks[head] = append(r.checks[head], Check{ID: w.nextID(), Name: ReleaseCheck, Status: "completed", Conclusion: "success"})
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

func (w *world) Checks(_ context.Context, repo, sha string) ([]Check, error) {
	return w.get(repo).checks[sha], nil
}

func (w *world) Merge(_ context.Context, repo string, n int, sha string) error {
	r := w.get(repo)
	pr := r.prs[n]
	if pr == nil || !pr.Open || pr.HeadSHA != sha {
		return fmt.Errorf("can't merge %s#%d at %s", repo, n, sha)
	}
	if len(w.mergeErrs) > 0 {
		err := w.mergeErrs[0]
		w.mergeErrs = w.mergeErrs[1:]
		return err
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
		r.notes[tag] = "### Features\n\n* " + repo + " feature\n"
	}
	return nil
}

func (w *world) Runs(_ context.Context, repo, event, sha string) ([]Run, error) {
	return w.get(repo).runs[event+sha], nil
}

func (w *world) TagsAt(_ context.Context, repo, sha string) ([]string, error) {
	return slices.Clone(w.get(repo).tags[sha]), nil
}

func (w *world) Tags(_ context.Context, repo string) (map[string]string, error) {
	out := map[string]string{}
	for sha, tags := range w.get(repo).tags {
		for _, tag := range tags {
			out[tag] = sha
		}
	}
	return out, nil
}

func (w *world) Release(_ context.Context, repo, tag string) (string, string, error) {
	return w.get(repo).notes[tag], "https://github.com/o/" + repo + "/releases/tag/" + tag, nil
}

// LatestFinal returns the highest final release among r.finals and r's tags.
func (w *world) LatestFinal(_ context.Context, repo string) (string, error) {
	r := w.get(repo)
	r.lookups++
	best := ""
	for _, tags := range append(slices.Collect(maps.Values(r.tags)), r.finals) {
		for _, tag := range tags {
			if v := semver.Canonical(tag); v == tag && semver.Prerelease(v) == "" && (best == "" || semver.Compare(v, best) > 0) {
				best = v
			}
		}
	}
	return best, nil
}

// RerunFailed re-runs the run, which passes this time (on the next tick, with lagReruns).
func (w *world) RerunFailed(_ context.Context, repo string, id int64) error {
	w.reruns = append(w.reruns, id)
	if w.lagReruns {
		w.lagged = append(w.lagged, id)
		return nil
	}
	w.rerun(id)
	return nil
}

// rerun makes run id's next attempt pass.
func (w *world) rerun(id int64) {
	for _, r := range w.repos {
		for _, runs := range r.runs {
			for i := range runs {
				if runs[i].ID == id {
					runs[i].Status, runs[i].Conclusion = "completed", "success"
					runs[i].Attempt++
				}
			}
		}
	}
}

func (w *world) EnsureIssue(_ context.Context, repo, title, body string) (string, error) {
	w.external = append(w.external, repo+": "+title+"\n"+body)
	return "https://github.com/o/" + repo + "/issues/1", nil
}

// Bump plays ccf-bump: with a repo's bumped set, it opens a green bump PR labelled ccf-bump:automerge.
func (w *world) Bump(_ context.Context, path, repo string, sets map[string]string, dryRun bool) (BumpResult, error) {
	var s []string
	for _, k := range slices.Sorted(maps.Keys(sets)) {
		s = append(s, k+"="+sets[k])
	}
	w.bumps = append(w.bumps, fmt.Sprintf("%s %s dry=%v", repo, strings.Join(s, ","), dryRun))
	w.events = append(w.events, "bump "+repo)
	r := w.get(repo)
	if !r.bumped {
		return BumpResult{Output: "nothing pinned"}, nil
	}
	if dryRun {
		return BumpResult{Changed: true, Output: "dry run: would push"}, nil
	}
	pr := r.openPR("ccf-bump/")
	if pr == nil {
		pr = &PR{Number: int(w.nextID()), URL: "ccf-bump/train-x", Open: !w.closedBumps, HeadSHA: fmt.Sprintf("%s-bump%d", repo, w.id)}
		if !w.manualBumps {
			pr.Labels = []string{bump.AutomergeLabel}
		}
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

func (w *world) Comments(_ context.Context, n int, after int64) ([]Comment, error) {
	var out []Comment
	for _, c := range w.comments[n] {
		if c.ID > after {
			out = append(out, c)
		}
	}
	return out, nil
}

func (w *world) Comment(_ context.Context, n int, body string) (string, error) {
	c := Comment{ID: w.nextID(), Author: "github-actions[bot]", Body: body}
	w.comments[n] = append(w.comments[n], c)
	return fmt.Sprintf("https://github.com/o/workflows/issues/%d#issuecomment-%d", n, c.ID), nil
}

func (w *world) say(n int, author, body string) {
	w.comments[n] = append(w.comments[n], Comment{ID: w.nextID(), Author: author, Body: body})
}

func (w *world) lastComment(n int) string {
	cs := w.comments[n]
	return cs[len(cs)-1].Body
}

func (w *world) IsOrgAdmin(_ context.Context, user string) (bool, error) {
	return slices.Contains(w.admins, user), nil
}

// Slack.

func (w *world) Post(ctx context.Context, channel string, m slackkit.Message) (slackkit.Posted, error) {
	return w.Reply(ctx, channel, "", m)
}

func (w *world) Reply(_ context.Context, channel, thread string, m slackkit.Message) (slackkit.Posted, error) {
	if channel != "C1" {
		w.digests = append(w.digests, m)
		return slackkit.Posted{Channel: channel, TS: "d"}, nil
	}
	w.slack = append(w.slack, thread+"|"+m.Text)
	return slackkit.Posted{Channel: "C1", TS: fmt.Sprintf("ts-%d", len(w.slack))}, nil
}

func (w *world) Update(_ context.Context, channel, ts string, m slackkit.Message) error {
	w.updates = append(w.updates, channel+"|"+ts+"|"+m.Text)
	return nil
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
		Repos: w, Tracker: w, Members: w, Slack: w, Bumper: w, Gate: w,
		Registry:              "ghcr.io/compliance-framework",
		Load:                  func(p string) (*manifest.Manifest, error) { return manifest.Load("../../" + p) },
		ReposURL:              "https://github.com/compliance-framework",
		Channel:               "C1",
		DigestChannel:         "CDIG",
		RequiredCheck:         "ci / required",
		ReleasePleaseWorkflow: "release-please.yml",
		RunURL:                "https://run",
		Now:                   func() time.Time { return now },
	}
}

// Gate.

func (w *world) Dispatch(_ context.Context, workflow string, inputs map[string]string) (Run, error) {
	id := w.nextID()
	g := &gateRun{workflow: workflow, inputs: maps.Clone(inputs), run: Run{ID: id, Path: ".github/workflows/" + workflow, Status: "queued", URL: fmt.Sprintf("https://run/%d", id)}}
	w.gateRuns = append(w.gateRuns, g)
	w.events = append(w.events, "dispatch "+workflow)
	if w.noRunID {
		return Run{}, nil
	}
	return g.run, nil
}

// finishGateRun completes a run: a probe prints its report, and fails for a repo in probeFail.
func (w *world) finishGateRun(g *gateRun) {
	const ts = "2026-12-01T08:00:00.0000000Z "
	failed := w.smokeFail
	g.log = ts + "##[group]Run smoke/run.sh\n" + ts + "##[endgroup]\n"
	if g.workflow == ProbeWorkflow {
		ref := g.inputs["plugins"] + g.inputs["policies"]
		repo := ref[strings.LastIndex(ref, "/")+1 : strings.LastIndex(ref, ":")]
		failed = w.probeFail[repo]
		protocol := 2
		if w.v1[repo] {
			protocol = 1
		}
		report := fmt.Sprintf(`{"plugins":[{"source":%q,"protocol":%d}]}`, ref, protocol)
		if g.inputs["policies"] != "" {
			report = fmt.Sprintf(`{"policies":[{"source":%q}]}`, ref)
		}
		g.log = ts + ProbeReportMarker + report + "\n"
		if failed {
			g.log += ts + "| `" + ref + "` | - | - | **failed**: plugin exited during Init |\n"
		}
	} else if failed {
		g.log += ts + "SMOKE FAILED: agent heartbeat\n"
	}
	w.events = append(w.events, fmt.Sprintf("done %d", g.run.ID))
	g.run.Status, g.run.Conclusion = "completed", "success"
	if failed {
		g.run.Conclusion = "failure"
		g.log += ts + "##[error]Process completed with exit code 1.\n" + ts + "Post job cleanup.\n"
	}
}

func (w *world) gateRun(id int64) *gateRun {
	for _, g := range w.gateRuns {
		if g.run.ID == id {
			return g
		}
	}
	w.t.Fatalf("no gate run %d", id)
	return nil
}

func (w *world) WorkflowRun(_ context.Context, id int64) (Run, error) { return w.gateRun(id).run, nil }

func (w *world) RunLog(_ context.Context, id int64) (string, error) { return w.gateRun(id).log, nil }

// dispatched lists the gate's runs as "workflow name=value ...".
func (w *world) dispatched() []string {
	var out []string
	for _, g := range w.gateRuns {
		var in []string
		for _, k := range slices.Sorted(maps.Keys(g.inputs)) {
			if g.inputs[k] != "" {
				in = append(in, k+"="+g.inputs[k])
			}
		}
		out = append(out, g.workflow+" "+strings.Join(in, " "))
	}
	return out
}
