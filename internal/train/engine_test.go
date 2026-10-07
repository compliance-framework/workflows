package train

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	ctx      = context.Background()
	trainDay = time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC) // a Tuesday
)

// mockWorld has the repos.mock.yaml repos: mock-api has a minor release pending, mock-gooci a
// patch, and every later repo pins something the train bumps.
func mockWorld(t *testing.T) *world {
	w := newWorld(t)
	w.repo("mock-api", "0.1.0").pending("0.2.0")
	w.repo("mock-gooci", "0.1.0").pending("0.1.1")
	for _, n := range []string{"mock-agent", "mock-ui", "mock-agent-action", "mock-plugin-1", "mock-plugin-2",
		"mock-plugin-policies-1", "mock-plugin-policies-2", "mock-helm-charts"} {
		w.repo(n, "0.1.0").bumped = true
	}
	return w
}

// drive runs reconcile until the train closes, playing GitHub between runs.
func drive(t *testing.T, w *world, e *Engine, runs int) {
	t.Helper()
	for i := 0; i < runs && w.issues[0].Open; i++ {
		w.tick()
		if err := e.Reconcile(ctx); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
}

// open starts a train the way Start does, without its schedule checks and Slack parent.
func open(t *testing.T, w *world, e *Engine, o StartOptions) {
	t.Helper()
	m, err := e.Load(o.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.newState(ctx, m, e.Now().UTC().Format("2006-01"), o)
	if err != nil {
		t.Fatal(err)
	}
	st.ThreadTS = "ts-0"
	body, err := Render(st, issueHelp)
	if err != nil {
		t.Fatal(err)
	}
	is, _ := w.CreateIssue(ctx, Title(st.Month, false, nil), body, []string{LabelTrain, LabelOpen})
	if err := e.step(ctx, is, st); err != nil {
		t.Fatal(err)
	}
}

func stageOf(st *State, repo string) int { return st.Repo(repo).Stage }

func TestTrainReleasesStageByStage(t *testing.T) {
	w := mockWorld(t)
	e := w.engine(trainDay)
	open(t, w, e, StartOptions{Manifest: "repos.mock.yaml"})
	drive(t, w, e, 30)
	is := w.issues[0]
	st := w.state(1)
	if is.Open || st.Status != StatusFinished || !slices.Contains(is.Labels, LabelDone) || is.Title != "Release train 2026-12" {
		t.Fatalf("issue %q open=%v labels=%v status=%s:\n%s", is.Title, is.Open, is.Labels, st.Status, is.Body)
	}
	// Release PRs merge stage by stage, helm last.
	last := 0
	for _, repo := range w.releases {
		if s := stageOf(st, repo); s < last {
			t.Errorf("%s (stage %d) released after stage %d: %v", repo, s, last, w.releases)
		} else {
			last = s
		}
	}
	if last != 4 || stageOf(st, "mock-helm-charts") != 4 || stageOf(st, "mock-agent") != 2 {
		t.Errorf("stages: last %d, helm %d, agent %d", last, stageOf(st, "mock-helm-charts"), stageOf(st, "mock-agent"))
	}
	if !slices.Contains(w.bumps, "mock-agent mock-api=v0.2.0,mock-gooci=v0.1.1 dry=false") {
		t.Errorf("bumps = %v", w.bumps)
	}
	if v := st.Repo("mock-plugin-1").Version(); v != "0.1.1" {
		t.Errorf("mock-plugin-1 released %q", v)
	}
	// Slack: only thread replies, ending with the finish message.
	for _, m := range w.slack {
		if !strings.HasPrefix(m, "ts-0|") {
			t.Errorf("not in the thread: %q", m)
		}
	}
	if f := w.slack[len(w.slack)-1]; !strings.Contains(f, "finished*: mock-api v0.2.0, mock-gooci v0.1.1, mock-agent v0.1.1") {
		t.Errorf("finish = %q", f)
	}
	// Repeating a run on a finished train changes nothing.
	n := len(w.merges)
	if err := e.Reconcile(ctx); err != nil || len(w.merges) != n {
		t.Errorf("rerun: %v, merges %d -> %d", err, n, len(w.merges))
	}
}

func TestFailureInStage2BlocksStage3(t *testing.T) {
	w := mockWorld(t)
	w.get("mock-agent").failChecks = true
	e := w.engine(trainDay)
	open(t, w, e, StartOptions{Manifest: "repos.mock.yaml"})
	drive(t, w, e, 10)
	st := w.state(1)
	agent := st.Repo("mock-agent")
	if agent.Hold != Blocked || agent.Phase != Merging || !strings.Contains(agent.Detail, "checks failing on #") {
		t.Fatalf("mock-agent = %+v", agent)
	}
	if ui := st.Repo("mock-ui"); ui.Phase != Released {
		t.Errorf("mock-ui (stage 2 too) = %s", ui.Phase)
	}
	for _, r := range st.Repos {
		if r.Stage >= 3 && r.Phase != Waiting {
			t.Errorf("%s (stage %d) = %s, want waiting", r.Name, r.Stage, r.Phase)
		}
	}
	for _, b := range w.bumps {
		if strings.HasPrefix(b, "mock-plugin-1 ") || strings.HasPrefix(b, "mock-helm-charts ") {
			t.Errorf("stage 3 or 4 bumped while stage 2 is blocked: %s", b)
		}
	}
	blocked := 0
	for _, m := range w.slack {
		if strings.Contains(m, "*mock-agent* is blocked") {
			blocked++
		}
	}
	if blocked != 1 {
		t.Errorf("want one blocked message over the runs, got %d: %q", blocked, w.slack)
	}
	if !strings.Contains(w.issues[0].Body, "| 2 | mock-agent | blocked |") {
		t.Errorf("table:\n%s", w.issues[0].Body)
	}

	// Someone fixes the PR; release-please regenerates it and the checks pass.
	r := w.get("mock-agent")
	r.failChecks, r.rpDone = false, ""
	drive(t, w, e, 30)
	st = w.state(1)
	if w.issues[0].Open || st.Status != StatusFinished || st.Repo("mock-agent").Version() == "" || st.Repo("mock-helm-charts").Phase != Released {
		t.Fatalf("after the fix: open=%v\n%s", w.issues[0].Open, w.issues[0].Body)
	}
}

func TestMajorNeedsApproval(t *testing.T) {
	w := newWorld(t)
	w.repo("mock-api", "0.9.0").pending("1.0.0")
	w.repo("mock-gooci", "0.1.0")
	e := w.engine(trainDay)
	open(t, w, e, StartOptions{Manifest: "repos.mock.yaml", Repos: []string{"mock-gooci", "mock-api"}})
	drive(t, w, e, 3)
	api := w.state(1).Repo("mock-api")
	if api.Hold != NeedsHuman || !strings.Contains(api.Detail, "raises a major version (.: v0.9.0 -> v1.0.0)") {
		t.Fatalf("mock-api = %+v", api)
	}
	if g := w.state(1).Repo("mock-gooci"); g.Phase != Released || g.Detail != "nothing to release" {
		t.Errorf("mock-gooci = %+v", g)
	}
	pr := w.get("mock-api").openPR(ReleaseBranchPrefix)
	pr.Labels = append(pr.Labels, "release:major-approved")
	drive(t, w, e, 5)
	if st := w.state(1); st.Status != StatusFinished || st.Repo("mock-api").Version() != "1.0.0" {
		t.Errorf("after approval: %s", w.issues[0].Body)
	}
}
