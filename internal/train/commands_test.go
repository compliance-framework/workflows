package train

import (
	"slices"
	"strings"
	"testing"
)

func TestCommands(t *testing.T) {
	w := newWorld(t)
	w.repo("mock-api", "0.1.0").pending("0.2.0")
	w.repo("mock-gooci", "0.1.0").pending("0.1.1").failRelease = true
	w.repo("mock-agent", "0.1.0").bumped = true
	e := w.engine(trainDay)
	if err := e.Start(ctx, StartOptions{Manifest: "mock.yaml", Repos: []string{"mock-api", "mock-gooci", "mock-agent"}}); err != nil {
		t.Fatal(err)
	}
	drive(t, w, e, 4)
	gooci := w.state(1).Repo("mock-gooci")
	if gooci.Hold != Blocked || gooci.FailedRun == 0 || !strings.Contains(gooci.Detail, "release workflow failed for v0.1.1") {
		t.Fatalf("mock-gooci = %+v", gooci)
	}

	tests := []struct{ author, body, reply string }{
		{"stranger", "/retry mock-gooci", "only organization owners"},
		{"owner", "/skip mock-nope", "mock-nope is not in this train"},
		{"owner", "/skip", "usage: /skip <repo>"},
		{"owner", "/retry mock-api", "mock-api is already released"},
	}
	for _, tt := range tests {
		w.say(1, tt.author, tt.body)
		if err := e.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if got := w.lastComment(1); !strings.Contains(got, tt.reply) || !strings.HasPrefix(got, "@"+tt.author) {
			t.Errorf("%s %q: reply %q, want %q", tt.author, tt.body, got, tt.reply)
		}
	}
	if len(w.reruns) != 0 {
		t.Fatalf("reruns = %v", w.reruns)
	}
	w.say(1, "owner", "/retry mock-gooci")
	w.say(1, "owner", "/skip mock-agent")
	drive(t, w, e, 3)
	st := w.state(1)
	if len(w.reruns) != 1 || st.Repo("mock-gooci").Phase != Released || st.Repo("mock-agent").Phase != Skipped || st.Status != StatusFinished {
		t.Fatalf("reruns %v:\n%s", w.reruns, w.issues[0].Body)
	}
	if slices.ContainsFunc(w.bumps, func(b string) bool { return strings.HasPrefix(b, "mock-agent") }) {
		t.Errorf("a skipped repo was bumped: %v", w.bumps)
	}
}

func TestAbort(t *testing.T) {
	w := newWorld(t)
	w.repo("mock-api", "0.1.0").pending("0.2.0")
	w.get("mock-api").failChecks = true
	e := w.engine(trainDay)
	if err := e.Start(ctx, StartOptions{Manifest: "mock.yaml", Repos: []string{"mock-api"}}); err != nil {
		t.Fatal(err)
	}
	w.say(1, "owner", "/abort")
	drive(t, w, e, 2)
	is := w.issues[0]
	if is.Open || !slices.Contains(is.Labels, LabelAborted) || w.state(1).Status != StatusAborted || len(w.merges) != 0 {
		t.Errorf("open=%v labels=%v merges=%v", is.Open, is.Labels, w.merges)
	}
	if !strings.Contains(w.slack[len(w.slack)-1], "aborted by owner") {
		t.Errorf("slack = %q", w.slack)
	}
}

func TestRetryReleasePlease(t *testing.T) {
	w := newWorld(t)
	w.repo("mock-api", "0.1.0").pending("0.2.0").failReleasePlease = true
	w.lagReruns = true
	e := w.engine(trainDay)
	if err := e.Start(ctx, StartOptions{Manifest: "mock.yaml", Repos: []string{"mock-api"}}); err != nil {
		t.Fatal(err)
	}
	drive(t, w, e, 3)
	api := w.state(1).Repo("mock-api")
	if api.Phase != ReleasePR || api.Hold != Blocked || api.FailedRun == 0 || api.FailedAttempt != 1 ||
		!strings.Contains(api.Detail, "`/retry mock-api`") {
		t.Fatalf("mock-api = %+v", api)
	}
	failed := api.FailedRun

	// GitHub still lists the failed attempt in the run that applies /retry: the repo waits for the
	// re-run instead of blocking on the old attempt again.
	w.say(1, "owner", "/retry mock-api")
	if err := e.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	api = w.state(1).Repo("mock-api")
	if !slices.Equal(w.reruns, []int64{failed}) || api.Phase != ReleasePR || api.Hold != NoHold || !api.Rerun ||
		!strings.HasPrefix(api.Detail, "waiting for the re-run of release-please") {
		t.Fatalf("reruns %v, mock-api = %+v", w.reruns, api)
	}
	if got := w.lastComment(1); !strings.HasSuffix(got, ": done.") {
		t.Errorf("reply %q", got)
	}

	drive(t, w, e, 4)
	st := w.state(1)
	api = st.Repo("mock-api")
	if !slices.Equal(w.reruns, []int64{failed}) || api.Phase != Released || api.Version() != "0.2.0" ||
		api.FailedRun != 0 || api.Rerun || st.Status != StatusFinished {
		t.Fatalf("reruns %v, mock-api = %+v", w.reruns, api)
	}
}

func TestRetryReleasePleaseWithoutARecordedRun(t *testing.T) {
	// A state written before release-pr recorded the failed run: /retry only clears the hold, and
	// the next run records the run, so a second /retry re-runs it.
	w := newWorld(t)
	w.repo("mock-api", "0.1.0").pending("0.2.0").failReleasePlease = true
	e := w.engine(trainDay)
	if err := e.Start(ctx, StartOptions{Manifest: "mock.yaml", Repos: []string{"mock-api"}}); err != nil {
		t.Fatal(err)
	}
	drive(t, w, e, 3)
	st := w.state(1)
	st.Repo("mock-api").FailedRun, st.Repo("mock-api").FailedAttempt = 0, 0
	body, err := Render(st, e.ReposURL, issueHelp)
	if err != nil {
		t.Fatal(err)
	}
	w.issues[0].Body = body
	w.say(1, "owner", "/retry mock-api")
	drive(t, w, e, 1)
	api := w.state(1).Repo("mock-api")
	if len(w.reruns) != 0 || api.Hold != Blocked || api.FailedRun == 0 {
		t.Fatalf("reruns %v, mock-api = %+v", w.reruns, api)
	}
	w.say(1, "owner", "/retry mock-api")
	drive(t, w, e, 4)
	if api = w.state(1).Repo("mock-api"); len(w.reruns) != 1 || api.Phase != Released {
		t.Fatalf("reruns %v, mock-api = %+v", w.reruns, api)
	}
}
