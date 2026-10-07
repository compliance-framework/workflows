package train

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestStartPostsTheParentMessage(t *testing.T) {
	w := mockWorld(t)
	w.tick() // release-please has opened stage 1's release PRs
	if err := w.engine(trainDay).Start(ctx, StartOptions{Manifest: "repos.mock.yaml", Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	st := w.state(1)
	if st.ThreadTS != "ts-1" || !strings.HasPrefix(w.slack[0], "|:steam_locomotive: *Release train 2026-12*: 10 repos in 4 stages (mock-api, mock-gooci → mock-agent, mock-ui → ") {
		t.Errorf("thread %q, parent %q", st.ThreadTS, w.slack[0])
	}
	// The first run already merged stage 1's release PRs.
	if st.Repo("mock-api").Phase != Publishing || !w.issues[0].Open || !slices.Contains(w.issues[0].Labels, LabelOpen) {
		t.Errorf("after the start: %s", w.issues[0].Body)
	}
}

func TestStartSchedule(t *testing.T) {
	day := func(s string) time.Time {
		d, _ := time.Parse(time.DateOnly, s)
		return d.Add(8 * time.Hour)
	}
	tests := []struct {
		now      string
		existing string
		want     string // the new issue's title, or ""
	}{
		{"2026-12-02", "", ""},                      // the 1st was the train day
		{"2027-01-01", "", ""},                      // a holiday; the train day is Mon 4 Jan
		{"2027-01-04", "", "Release train 2027-01"}, //
		{"2026-12-01", "Release train 2026-12", ""}, // already ran
		{"2026-12-01", "Release train 2026-11", "Release train 2026-12"},
	}
	for _, tt := range tests {
		w := newWorld(t)
		w.repo("mock-api", "0.1.0")
		if tt.existing != "" {
			w.issues = append(w.issues, &Issue{Number: 1, Title: tt.existing, Labels: []string{LabelTrain, LabelDone}})
		}
		if err := w.engine(day(tt.now)).Start(ctx, StartOptions{Manifest: "repos.mock.yaml", Repos: []string{"mock-api"}, Scheduled: true}); err != nil {
			t.Fatal(err)
		}
		got := ""
		if n := len(w.issues); n > 0 && w.issues[n-1].Title != tt.existing {
			got = w.issues[n-1].Title
		}
		if got != tt.want {
			t.Errorf("%s with %q: started %q, want %q", tt.now, tt.existing, got, tt.want)
		}
	}
	// A manual run starts a second train in the month.
	w := newWorld(t)
	w.repo("mock-api", "0.1.0")
	w.issues = append(w.issues, &Issue{Number: 1, Title: "Release train 2026-12", Labels: []string{LabelTrain, LabelDone}})
	if err := w.engine(day("2026-12-09")).Start(ctx, StartOptions{Manifest: "repos.mock.yaml", Repos: []string{"mock-api"}}); err != nil {
		t.Fatal(err)
	}
	if got := w.issues[len(w.issues)-1].Title; got != "Release train 2026-12 (2)" {
		t.Errorf("manual start: %q", got)
	}
}

func TestEscalation(t *testing.T) {
	w := newWorld(t)
	w.repo("mock-api", "0.1.0")
	w.get("mock-api").failChecks = true
	w.get("mock-api").pending("0.2.0")
	nov := w.engine(time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC))
	if err := nov.Start(ctx, StartOptions{Manifest: "repos.mock.yaml", Repos: []string{"mock-api"}}); err != nil {
		t.Fatal(err)
	}
	dec := w.engine(trainDay)
	for range 2 {
		w.tick()
		if err := dec.Start(ctx, StartOptions{Manifest: "repos.mock.yaml", Scheduled: true}); err != nil {
			t.Fatal(err)
		}
	}
	var escalations []string
	for _, m := range w.slack {
		if strings.Contains(m, ":rotating_light:") {
			escalations = append(escalations, m)
		}
	}
	if len(w.issues) != 1 || len(escalations) != 1 || !strings.HasPrefix(escalations[0], "|") || !strings.Contains(escalations[0], "Release train 2026-11* is still open at the start of 2026-12") {
		t.Errorf("issues %d, escalations %q", len(w.issues), escalations)
	}
}

func TestDryRunPlansTheW1Stages(t *testing.T) {
	w := newWorld(t)
	w.repo("api", "0.21.0").pending("0.22.0")
	w.repo("gooci", "0.0.7").pending("0.0.8")
	for _, n := range []string{"agent", "ui", "agent-action", "helm-charts"} {
		w.repo(n, "0.1.0").bumped = true
	}
	w.tick() // release-please has opened the release PRs
	e := w.engine(trainDay)
	if err := e.Start(ctx, StartOptions{Manifest: "repos.yaml", DryRun: true, Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	is := w.issues[0]
	if is.Open || is.Title != "Release train 2026-12 (dry run)" || !slices.Contains(is.Labels, LabelDryRun) || len(w.merges) != 0 {
		t.Fatalf("issue %q open=%v labels=%v merges=%v", is.Title, is.Open, is.Labels, w.merges)
	}
	plan := w.comments[1][0].Body
	for _, want := range []string{
		"1. api (would release v0.22.0 from #", "gooci (would release v0.0.8 from #",
		"2. agent (ccf-bump would open a PR; the bump would make release-please propose v0.1.1); ui (",
		"3. agent-action (", "4. helm-charts (",
		"Would open the chart issue helm-charts: \"Release train 2026-12: minor releases to roll into the charts\" (1 repos).",
		"<summary>agent: ccf-bump --dry-run</summary>",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan lacks %q:\n%s", want, plan)
		}
	}
	for _, b := range w.bumps {
		if !strings.HasSuffix(b, "dry=true") {
			t.Errorf("bump %q is not a dry run", b)
		}
	}
	if !slices.Contains(w.bumps, "agent api=v0.22.0,gooci=v0.0.8 dry=true") {
		t.Errorf("bumps = %v", w.bumps)
	}
	if len(w.external) != 0 || len(w.slack) != 2 || !strings.Contains(w.slack[1], "Dry-run plan") {
		t.Errorf("external %v, slack %q", w.external, w.slack)
	}
	// A later scheduled day doesn't plan again, and a reconcile finds no train.
	if err := e.Reconcile(ctx); err != nil || len(w.issues) != 1 {
		t.Errorf("reconcile: %v, %d issues", err, len(w.issues))
	}
}
