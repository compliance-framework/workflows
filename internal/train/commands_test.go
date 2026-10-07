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
	if err := e.Start(ctx, StartOptions{Manifest: "repos.mock.yaml", Repos: []string{"mock-api", "mock-gooci", "mock-agent"}}); err != nil {
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
	if err := e.Start(ctx, StartOptions{Manifest: "repos.mock.yaml", Repos: []string{"mock-api"}}); err != nil {
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
