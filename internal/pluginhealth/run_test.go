package pluginhealth

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/manifest"
)

type fakeTracker struct {
	issue   *Issue
	train   int
	created int
	edits   int
	pinErr  error
	pinned  []string
}

func (f *fakeTracker) FindIssue(_ context.Context, title, author string) (*Issue, error) {
	if author != IssueAuthor {
		return nil, errors.New("unexpected author " + author)
	}
	return f.issue, nil
}

func (f *fakeTracker) CreateIssue(_ context.Context, title, body string) (*Issue, error) {
	f.created++
	f.issue = &Issue{Number: 9, NodeID: "I_9", URL: "https://github.com/o/workflows/issues/9", Body: body}
	return f.issue, nil
}

func (f *fakeTracker) EditIssue(_ context.Context, number int, body string) error {
	f.edits++
	f.issue.Body = body
	return nil
}

func (f *fakeTracker) PinIssue(_ context.Context, id string) error {
	f.pinned = append(f.pinned, id)
	return f.pinErr
}

func (f *fakeTracker) LastTrain(context.Context) (int, error) { return f.train, nil }

type fakeSlack struct{ posts []string }

func (f *fakeSlack) PostMessage(_ context.Context, channel, text string) error {
	f.posts = append(f.posts, channel+": "+text)
	return nil
}

func TestPublish(t *testing.T) {
	ctx := context.Background()
	tr := &fakeTracker{train: 41, pinErr: errors.New("forbidden")}
	slack := &fakeSlack{}
	var log bytes.Buffer
	h := Header{Manifest: "repos.yaml", Batches: []int{1}, Now: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)}

	// First run: opens and pins the issue; a candidate is new, so it posts.
	if _, err := Publish(ctx, tr, slack, "C1", "Plugin health", report("p2"), h, &log); err != nil {
		t.Fatal(err)
	}
	if tr.created != 1 || !slices.Equal(tr.pinned, []string{"I_9"}) || !strings.Contains(log.String(), "::warning::could not pin") {
		t.Fatalf("created %d, pinned %v, log %s", tr.created, tr.pinned, log.String())
	}
	if s, ok := ParseState(tr.issue.Body); !ok || s.Train != 41 || !slices.Equal(s.Candidates, []string{"p2"}) {
		t.Fatalf("state %+v %v", s, ok)
	}
	if len(slack.posts) != 1 || !strings.Contains(slack.posts[0], "C1: :warning: Plugin deprecation candidates changed. Now: p2. Added: p2.") {
		t.Fatalf("posts %v", slack.posts)
	}

	// Same candidates: edits, no post.
	if _, err := Publish(ctx, tr, slack, "C1", "Plugin health", report("p2"), h, &log); err != nil {
		t.Fatal(err)
	}
	if tr.created != 1 || tr.edits != 1 || len(slack.posts) != 1 {
		t.Fatalf("created %d, edits %d, posts %v", tr.created, tr.edits, slack.posts)
	}

	// Changed candidates: posts what changed.
	if _, err := Publish(ctx, tr, slack, "C1", "Plugin health", report("p3"), h, &log); err != nil {
		t.Fatal(err)
	}
	if len(slack.posts) != 2 || !strings.Contains(slack.posts[1], "Now: p3. Added: p3. No longer: p2.") {
		t.Fatalf("posts %v", slack.posts)
	}

	// A candidate whose reads failed stays one: nothing posted.
	flaky := report()
	flaky.Rows[2].Status, flaky.Rows[2].Errors = Unknown, []string{"latest release: 502"}
	if _, err := Publish(ctx, tr, slack, "C1", "Plugin health", flaky, h, &log); err != nil {
		t.Fatal(err)
	}
	if s, _ := ParseState(tr.issue.Body); len(slack.posts) != 2 || !slices.Equal(s.Candidates, []string{"p3"}) {
		t.Fatalf("posts %v, state %+v", slack.posts, s)
	}

	// No Slack client: nothing posted, no error.
	if _, err := Publish(ctx, tr, nil, "C1", "Plugin health", report(), h, &log); err != nil {
		t.Fatal(err)
	}
	if len(slack.posts) != 2 || !strings.Contains(log.String(), "nothing posted") {
		t.Fatalf("posts %v", slack.posts)
	}
}

func TestDue(t *testing.T) {
	ctx := context.Background()
	body := Markdown(report(), State{Train: 41}, Header{})
	for _, tc := range []struct {
		name            string
		event, workflow string
		train           int
		issue           *Issue
		want            bool
	}{
		{"dispatch", "workflow_dispatch", "", 0, nil, true},
		{"after ccf-bump-sync", "workflow_run", "ccf-bump-sync", 0, nil, true},
		{"train, none finished", "workflow_run", "train", 0, nil, false},
		{"train, no report yet", "workflow_run", "train", 41, nil, true},
		{"train, already reported", "workflow_run", "train", 41, &Issue{Body: body}, false},
		{"train, a newer train closed", "workflow_run", "train", 42, &Issue{Body: body}, true},
		{"train, report without state", "workflow_run", "train", 41, &Issue{Body: "edited by hand"}, true},
	} {
		got, reason, err := Due(ctx, &fakeTracker{train: tc.train, issue: tc.issue}, "Plugin health", tc.event, tc.workflow)
		if err != nil || got != tc.want || reason == "" {
			t.Errorf("%s: Due = %v (%q), %v; want %v", tc.name, got, reason, err, tc.want)
		}
	}
}

func TestPlugins(t *testing.T) {
	const head = "active_batches: [1]\nrepos:\n  - {name: agent, kind: go-service, release: true}\n  - {name: agent2, kind: go-service, release: true}\n"
	for _, tc := range []struct {
		name, repos, agent, err string
		plugins                 []string
	}{
		{"kept plugins only", "  - {name: p1, kind: go-plugin, depends_on: [agent], release: true, batch: 1}\n" +
			"  - {name: p2, kind: go-plugin, depends_on: [agent], release: true, batch: 2}\n" +
			"  - {name: tpl, kind: go-plugin, depends_on: [agent], release: false, batch: 1}\n" +
			"  - {name: p1-policies, kind: policies, depends_on: [agent], release: true, batch: 1}\n", "agent", "", []string{"p1"}},
		{"two agents", "  - {name: p1, kind: go-plugin, depends_on: [agent], release: true, batch: 1}\n" +
			"  - {name: p3, kind: go-plugin, depends_on: [agent2], release: true, batch: 1}\n", "", "two agents", nil},
		{"no agent", "  - {name: p1, kind: go-plugin, release: true, batch: 1}\n", "", "no go-service", nil},
		{"no plugins", "  - {name: p1-policies, kind: policies, depends_on: [agent], release: true, batch: 1}\n", "", "no active go-plugin", nil},
	} {
		m, err := manifest.Parse([]byte(head + tc.repos))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		plugins, agent, err := Plugins(m)
		if !slices.Equal(plugins, tc.plugins) || agent != tc.agent || (err == nil) != (tc.err == "") || err != nil && !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%s: Plugins = %v, %q, %v; want %v, %q, error %q", tc.name, plugins, agent, err, tc.plugins, tc.agent, tc.err)
		}
	}
}

func TestTitle(t *testing.T) {
	if got := Title("repos.yaml"); got != "Plugin health" {
		t.Errorf("Title = %q", got)
	}
	if got := Title("repos.mock.yaml"); got != "Plugin health (repos.mock.yaml)" {
		t.Errorf("Title = %q", got)
	}
}
