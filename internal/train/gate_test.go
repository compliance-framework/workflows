package train

import (
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// The mock train probes its plugin and policy repos before helm; mock-plugin-2 speaks v1.
func TestGatePassesThenHelmReleases(t *testing.T) {
	w := mockWorld(t)
	w.v1["mock-plugin-2"] = true
	e := w.engine(trainDay)
	open(t, w, e, StartOptions{Manifest: "repos.mock.yaml"})
	drive(t, w, e, 30)
	st := w.state(1)
	if w.issues[0].Open || st.Status != StatusFinished || st.Repo("mock-helm-charts").Phase != Released {
		t.Fatalf("open=%v\n%s", w.issues[0].Open, w.issues[0].Body)
	}
	// The mocks have no agent repo: the probe uses the go.mod pin; no stack-smoke.
	want := []string{
		"plugin-probe.yml plugins=ghcr.io/compliance-framework/mock-plugin-1:v0.1.1",
		"plugin-probe.yml plugins=ghcr.io/compliance-framework/mock-plugin-2:v0.1.1",
		"plugin-probe.yml policies=ghcr.io/compliance-framework/mock-plugin-policies-1:v0.1.1",
		"plugin-probe.yml policies=ghcr.io/compliance-framework/mock-plugin-policies-2:v0.1.1",
	}
	if got := w.dispatched(); !slices.Equal(got, want) {
		t.Errorf("dispatched:\n%s", strings.Join(got, "\n"))
	}
	if g := st.GateCheck("mock-plugin-2"); g.Result != GatePassed || !g.Candidate {
		t.Errorf("mock-plugin-2 = %+v", g)
	}
	if st.GateCheck("mock-plugin-1").Candidate {
		t.Error("mock-plugin-1 is v2")
	}
	text := strings.Join(w.slack, "\n")
	if !strings.Contains(text, "*mock-plugin-2* v0.1.1 speaks plugin protocol v1: a deprecation candidate (not blocking)") ||
		strings.Contains(text, "The gate failed") || !strings.Contains(text, "The gate passed (4 checks)") {
		t.Errorf("slack:\n%s", text)
	}
	// helm starts (its bump) only after every probe run finished.
	helm, done := slices.Index(w.events, "bump mock-helm-charts"), 0
	for i, ev := range w.events {
		if strings.HasPrefix(ev, "done ") && i < helm {
			done++
		}
	}
	if helm < 0 || done != 4 {
		t.Errorf("%d gate runs finished before helm's bump: %v", done, w.events)
	}
	if !strings.Contains(w.issues[0].Body, "| mock-plugin-2 | 0.1.1 | [passed](https://run/") {
		t.Errorf("issue:\n%s", w.issues[0].Body)
	}
}

// mock-plugin-1's release is broken: the gate blocks it and helm waits, until a fix release.
func TestGateFailureBlocksHelmUntilAFix(t *testing.T) {
	w := mockWorld(t)
	w.probeFail["mock-plugin-1"] = true
	e := w.engine(trainDay)
	open(t, w, e, StartOptions{Manifest: "repos.mock.yaml"})
	drive(t, w, e, 30)
	st := w.state(1)
	p1, helm := st.Repo("mock-plugin-1"), st.Repo("mock-helm-charts")
	if !w.issues[0].Open || p1.Phase != Released || p1.Hold != Blocked || !strings.Contains(p1.Detail, "the gate failed: plugin-probe.yml failure: https://run/") {
		t.Fatalf("mock-plugin-1 = %+v", p1)
	}
	if helm.Phase != Waiting || slices.ContainsFunc(w.bumps, func(b string) bool { return strings.HasPrefix(b, "mock-helm-charts") }) {
		t.Fatalf("helm started: %+v, bumps %v", helm, w.bumps)
	}
	var fails []string
	for _, m := range w.slack {
		if strings.Contains(m, "The gate failed") {
			fails = append(fails, m)
		}
	}
	if len(fails) != 1 || !strings.HasPrefix(fails[0], "ts-0|") || !strings.Contains(fails[0], "*mock-plugin-1 v0.1.1*") ||
		!strings.Contains(fails[0], "**failed**: plugin exited during Init") || strings.Contains(fails[0], "Post job cleanup") {
		t.Errorf("failure messages: %q", fails)
	}
	if !strings.Contains(w.issues[0].Body, "| 3 | mock-plugin-1 | blocked |") {
		t.Errorf("table:\n%s", w.issues[0].Body)
	}
	if u := w.updates[len(w.updates)-1]; !strings.HasSuffix(u, "Stage 4 of 4 · 1 held") {
		t.Errorf("board %q", u)
	}

	// A fix: mock-plugin-1's release PR for v0.1.2 merges. Its image isn't pushed until its release
	// workflow succeeds, so the gate waits for the tag, then for the run, without dispatching.
	runs := len(w.gateRuns)
	delete(w.probeFail, "mock-plugin-1")
	release := fixRelease(w, "mock-plugin-1", "0.1.2", "in_progress", "")
	tags := w.get("mock-plugin-1").tags
	delete(tags, "mock-plugin-1-fix")
	waitingFor := func(detail string) {
		t.Helper()
		drive(t, w, e, 3)
		g := w.state(1).GateCheck("mock-plugin-1")
		if len(w.gateRuns) != runs || g.Result != GateFailed || g.Version != "0.1.1" || g.Detail != detail || !w.issues[0].Open {
			t.Fatalf("%d runs -> %d, mock-plugin-1 = %+v", runs, len(w.gateRuns), g)
		}
	}
	waitingFor("v0.1.2 found; waiting for its release tag")
	tags["mock-plugin-1-fix"] = []string{"v0.1.2"}
	waitingFor("v0.1.2 found; waiting for its release workflow")
	// The release workflow succeeds: the fix is probed once, and passes.
	release.Status, release.Conclusion = "completed", "success"
	drive(t, w, e, 30)
	st = w.state(1)
	if len(w.gateRuns) != runs+1 {
		t.Errorf("%d runs -> %d: %q", runs, len(w.gateRuns), w.dispatched())
	}
	if w.issues[0].Open || st.Repo("mock-helm-charts").Phase != Released || st.Repo("mock-plugin-1").Hold != NoHold {
		t.Fatalf("after the fix: open=%v\n%s", w.issues[0].Open, w.issues[0].Body)
	}
	if d := w.dispatched(); d[len(d)-1] != "plugin-probe.yml plugins=ghcr.io/compliance-framework/mock-plugin-1:v0.1.2" {
		t.Errorf("dispatched %q", d)
	}
}

// fixRelease merges a release PR for version on repo's default branch and tags it, with a release
// workflow run of the tag in status (conclusion when completed); tick doesn't replace the run.
func fixRelease(w *world, repo, version, status, conclusion string) *Run {
	r := w.get(repo)
	r.main = repo + "-fix"
	r.manifests[r.main] = map[string]string{RootPackage: version}
	r.tags[r.main] = []string{"v" + version}
	r.runs["release"+r.main] = []Run{{ID: w.nextID(), Path: ".github/workflows/release.yml", HeadBranch: "v" + version,
		Status: status, Conclusion: conclusion, URL: "https://release-run"}}
	return &r.runs["release"+r.main][0]
}

// A fix release whose release workflow failed is never probed: the check stays failed and says
// why, until the workflow is re-run and succeeds.
func TestGateWaitsForTheFixReleaseWorkflow(t *testing.T) {
	w := mockWorld(t)
	w.probeFail["mock-plugin-1"] = true
	e := w.engine(trainDay)
	open(t, w, e, StartOptions{Manifest: "repos.mock.yaml"})
	drive(t, w, e, 30)
	runs := len(w.gateRuns)
	delete(w.probeFail, "mock-plugin-1")
	release := fixRelease(w, "mock-plugin-1", "0.1.2", "completed", "failure")
	drive(t, w, e, 3)
	st := w.state(1)
	g, p1 := st.GateCheck("mock-plugin-1"), st.Repo("mock-plugin-1")
	want := "the release workflow of v0.1.2 failed: https://release-run; re-run it and the gate probes v0.1.2"
	if len(w.gateRuns) != runs || g.Result != GateFailed || g.Version != "0.1.1" || g.Detail != want ||
		p1.Hold != Blocked || !strings.Contains(p1.Detail, want) || !w.issues[0].Open {
		t.Fatalf("%d runs -> %d, check %+v, repo %+v", runs, len(w.gateRuns), g, p1)
	}
	// A pending check (after /retry) fails on the same release rather than probing.
	w.say(1, "owner", "/retry mock-plugin-1")
	drive(t, w, e, 3)
	if g := w.state(1).GateCheck("mock-plugin-1"); len(w.gateRuns) != runs || g.Result != GateFailed || g.Detail != want {
		t.Fatalf("after /retry: %d runs -> %d, check %+v", runs, len(w.gateRuns), g)
	}
	release.Conclusion = "success" // re-run
	drive(t, w, e, 30)
	st = w.state(1)
	if g := st.GateCheck("mock-plugin-1"); len(w.gateRuns) != runs+1 || g.Result != GatePassed || g.Version != "0.1.2" || w.issues[0].Open {
		t.Fatalf("after the re-run: %d runs -> %d, check %+v\n%s", runs, len(w.gateRuns), g, w.issues[0].Body)
	}
}

func TestGateRetryAndSkip(t *testing.T) {
	for _, cmd := range []string{"/retry mock-plugin-1", "/skip mock-plugin-1"} {
		w := mockWorld(t)
		w.probeFail["mock-plugin-1"] = true
		e := w.engine(trainDay)
		open(t, w, e, StartOptions{Manifest: "repos.mock.yaml"})
		drive(t, w, e, 30)
		if st := w.state(1); st.GateCheck("mock-plugin-1").Result != GateFailed || st.Repo("mock-helm-charts").Phase != Waiting {
			t.Fatalf("%s: before:\n%s", cmd, w.issues[0].Body)
		}
		runs := len(w.gateRuns)
		if strings.HasPrefix(cmd, "/retry") {
			delete(w.probeFail, "mock-plugin-1") // a flake, or a fixed registry
		}
		w.say(1, "owner", cmd)
		drive(t, w, e, 30)
		st := w.state(1)
		if w.issues[0].Open || st.Repo("mock-helm-charts").Phase != Released {
			t.Fatalf("%s: after:\n%s", cmd, w.issues[0].Body)
		}
		g := st.GateCheck("mock-plugin-1")
		switch cmd {
		case "/retry mock-plugin-1":
			if g.Result != GatePassed || len(w.gateRuns) != runs+1 {
				t.Errorf("retry: %+v, %d runs -> %d", g, runs, len(w.gateRuns))
			}
		default:
			if g.Result != GateSkipped || g.Detail != "skipped by owner" || len(w.gateRuns) != runs {
				t.Errorf("skip: %+v, %d runs -> %d", g, runs, len(w.gateRuns))
			}
		}
	}
}

// A dispatch GitHub answers without a run ID fails the check instead of dispatching again.
func TestGateDispatchWithoutRunID(t *testing.T) {
	w := mockWorld(t)
	w.noRunID = true
	e := w.engine(trainDay)
	open(t, w, e, StartOptions{Manifest: "repos.mock.yaml"})
	drive(t, w, e, 30)
	g := w.state(1).GateCheck("mock-plugin-1")
	if len(w.gateRuns) != 4 || g.Result != GateFailed || !strings.Contains(g.Detail, "GitHub returned no run ID") || !w.issues[0].Open {
		t.Errorf("%d runs, mock-plugin-1 = %+v", len(w.gateRuns), g)
	}
}

// productWorld is a train over a small manifest with the product's api, ui and agent, a plugin
// and helm-charts.
func productWorld(t *testing.T) (*world, *Engine) {
	m, err := manifest.Parse([]byte(`
repos:
  - {name: api, kind: go-service, release: true}
  - {name: agent, kind: go-service, depends_on: [api], release: true}
  - {name: ui, kind: ui, depends_on: [api], release: true}
  - {name: plugin-file-attestation, kind: go-plugin, depends_on: [agent], release: true}
  - {name: plugin-file-attestation-policies, kind: policies, depends_on: [agent], release: true}
  - {name: helm-charts, kind: helm, depends_on: [api, ui, agent], release: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	w := newWorld(t)
	w.repo("api", "0.21.0").pending("0.22.0")
	for _, n := range []string{"agent", "ui", "plugin-file-attestation", "plugin-file-attestation-policies", "helm-charts"} {
		w.repo(n, "0.1.0").bumped = true
	}
	e := w.engine(trainDay)
	e.Load = func(string) (*manifest.Manifest, error) { return m, nil }
	return w, e
}

// The probe follows the agent this train released, and stack-smoke runs with the train's api,
// ui, agent and plugin-file-attestation; a smoke failure holds helm until /skip stack-smoke.
func TestGateSmokeWithTheTrainVersions(t *testing.T) {
	w, e := productWorld(t)
	w.smokeFail = true
	open(t, w, e, StartOptions{Manifest: "repos.yaml"})
	drive(t, w, e, 30)
	want := []string{
		"plugin-probe.yml agent-ref=v0.1.1 plugins=ghcr.io/compliance-framework/plugin-file-attestation:v0.1.1",
		"plugin-probe.yml agent-ref=v0.1.1 policies=ghcr.io/compliance-framework/plugin-file-attestation-policies:v0.1.1",
		"stack-smoke.yml agent-tag=0.1.1 api-tag=0.22.0 plugin-tag=v0.1.1 policy-tag=v0.1.1 ui-tag=0.1.1",
	}
	if got := w.dispatched(); !slices.Equal(got, want) {
		t.Errorf("dispatched:\n%s", strings.Join(got, "\n"))
	}
	st := w.state(1)
	if !w.issues[0].Open || st.GateCheck(SmokeCheck).Result != GateFailed || st.Repo("helm-charts").Phase != Waiting {
		t.Fatalf("open=%v\n%s", w.issues[0].Open, w.issues[0].Body)
	}
	if text := strings.Join(w.slack, "\n"); !strings.Contains(text, "The gate failed for *stack-smoke*") || !strings.Contains(text, "SMOKE FAILED: agent heartbeat") {
		t.Errorf("slack:\n%s", text)
	}
	w.say(1, "owner", "/skip stack-smoke")
	drive(t, w, e, 30)
	if st := w.state(1); w.issues[0].Open || st.Repo("helm-charts").Phase != Released || st.GateCheck(SmokeCheck).Result != GateSkipped {
		t.Fatalf("after /skip:\n%s", w.issues[0].Body)
	}
}

// A dry run plans the gate with the planned versions and runs nothing.
func TestDryRunPlansTheGate(t *testing.T) {
	w, e := productWorld(t)
	w.tick()
	if err := e.Start(ctx, StartOptions{Manifest: "repos.yaml", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	plan := w.comments[1][0].Body
	if !strings.Contains(plan, "Gate before stage 4 (not run in a dry run): plugin-probe ghcr.io/compliance-framework/plugin-file-attestation:v0.1.1 (agent v0.1.1); plugin-probe ghcr.io/compliance-framework/plugin-file-attestation-policies:v0.1.1 (agent v0.1.1); stack-smoke with api-tag 0.22.0, ui-tag 0.1.1, agent-tag 0.1.1, plugin-tag v0.1.1, policy-tag v0.1.1.") {
		t.Errorf("plan:\n%s", plan)
	}
	if len(w.gateRuns) != 0 {
		t.Errorf("dispatched %v", w.dispatched())
	}
}

// A train without the agent probes with the go.mod pin and runs no stack-smoke: the release-bot
// token reaches only the train's repos, so the gate reads nothing else.
func TestGateInATrainWithoutTheStack(t *testing.T) {
	w, e := productWorld(t)
	open(t, w, e, StartOptions{Manifest: "repos.yaml", Repos: []string{"plugin-file-attestation", "helm-charts"}})
	drive(t, w, e, 30)
	want := []string{"plugin-probe.yml plugins=ghcr.io/compliance-framework/plugin-file-attestation:v0.1.0"}
	if got := w.dispatched(); !slices.Equal(got, want) || w.issues[0].Open {
		t.Errorf("open=%v, dispatched:\n%s", w.issues[0].Open, strings.Join(got, "\n"))
	}
}

func TestLogTailAndV1(t *testing.T) {
	log := "2026-12-01T08:00:00.1Z ##[group]Run x\n2026-12-01T08:00:00.1Z ##[endgroup]\n2026-12-01T08:00:00.1Z boom\r\n" +
		"2026-12-01T08:00:00.1Z ##[error]Process completed with exit code 1.\n2026-12-01T08:00:00.1Z Post job cleanup.\n"
	if got := logTail(log); got != "##[group]Run x\nboom" {
		t.Errorf("logTail = %q", got)
	}
	if v1Plugin(log) || !v1Plugin("2026-12-01T08:00:00Z "+ProbeReportMarker+`{"plugins":[{"protocol":2},{"protocol":1}]}`) ||
		v1Plugin("  echo \""+ProbeReportMarker+`$(jq -c . r.json)"`) {
		t.Error("v1Plugin")
	}
}
