package slackkit

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden renders m's chat.postMessage payload one JSON value per line (the arguments, then each
// top-level block, then each attachment and its blocks) and compares it with testdata/<name>.golden.
func golden(t *testing.T, name string, m Message) {
	t.Helper()
	p := m.payload(false)
	var b bytes.Buffer
	line := func(prefix string, v any) {
		raw, err := marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(prefix)
		b.Write(raw)
		b.WriteByte('\n')
	}
	blocks, _ := p["blocks"].([]Block)
	attachments, _ := p["attachments"].([]any)
	delete(p, "blocks")
	delete(p, "attachments")
	line("", p)
	for _, bl := range blocks {
		line("blocks ", bl)
	}
	for _, a := range attachments {
		a := a.(map[string]any)
		inner := a["blocks"].([]Block)
		line("attachment ", map[string]any{"color": a["color"], "fallback": a["fallback"]})
		for _, bl := range inner {
			line("  blocks ", bl)
		}
	}
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/slackkit -update)", err)
	}
	if !bytes.Equal(b.Bytes(), want) {
		t.Errorf("%s changed:\n got:\n%s\nwant:\n%s", path, b.Bytes(), want)
	}
}

var (
	t0 = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	gh = "https://github.com/compliance-framework/"
)

func TestIncidentCard(t *testing.T) {
	in := Incident{
		Repo: "compliance-framework/mock-api", Ref: "compliance-framework/mock-api#12", RefURL: gh + "mock-api/pull/12",
		Title: "fix(deps): update module <x> to v2", Workflow: "ci", Status: Failing,
		SHA: "2222222abcdef", SHAURL: gh + "mock-api/commit/2222222abcdef", FailedJobs: []string{"go / test", "required"},
		OpenedAt: t0, PRURL: gh + "mock-api/pull/12", RunURL: gh + "mock-api/actions/runs/7", UpdatedAt: t0,
	}
	golden(t, "incident-failing", IncidentCard(in))
	in.Status, in.UpdatedAt = FailingAgain, t0.Add(20*time.Minute)
	golden(t, "incident-failing-again", IncidentCard(in))
	in.Status, in.ResolvedAt, in.UpdatedAt = Resolved, t0.Add(72*time.Minute), t0.Add(72*time.Minute)
	golden(t, "incident-resolved", IncidentCard(in))
	in.Status, in.StatusNote = Closed, "PR merged"
	golden(t, "incident-closed", IncidentCard(in))
	push := Incident{Repo: "compliance-framework/mock-api", Ref: "compliance-framework/mock-api@main", RefURL: gh + "mock-api/tree/main",
		Workflow: "ci", Status: Failing, SHA: "abc1234", FailedJobs: []string{"go"}, RunURL: gh + "mock-api/actions/runs/8"}
	golden(t, "incident-push", IncidentCard(push))
}

func TestNeedsHumanCard(t *testing.T) {
	n := NeedsHuman{
		Repo: "compliance-framework/mock-agent", Ref: "compliance-framework/mock-agent#34", URL: gh + "mock-agent/pull/34",
		Title: "fix(deps): update module github.com/compliance-framework/mock-api to v2", Why: "Renovate major update",
		CI: ":white_check_mark: Passing", OpenedBy: "ccf-release-bot[bot]", OpenedAt: t0, Now: t0.Add(51 * time.Hour),
	}
	golden(t, "needs-human-open", NeedsHumanCard(n))
	n.Handled, n.HandledBy, n.HandledAt = "merged", "octocat", t0.Add(52*time.Hour)
	golden(t, "needs-human-merged", NeedsHumanCard(n))
	n.Handled = "closed"
	if got := NeedsHumanCard(n); got.Color != ColorGrey || got.Text != "Handled (closed): "+n.Ref+" "+n.Title {
		t.Errorf("closed: color %s, text %q", got.Color, got.Text)
	}
}

func board() Train {
	repo := func(name string, p Pill, status, v string) TrainRepo {
		return TrainRepo{Name: name, URL: gh + name, Pill: p, Status: status, Version: v}
	}
	return Train{
		Title: "Release train 2026-10", Stage: 2, IssueURL: gh + "workflows/issues/70", UpdatedAt: t0,
		Stages: [][]TrainRepo{
			{repo("mock-api", PillDone, "released", "v1.4.0"), repo("mock-gooci", PillSkipped, "skipped", "")},
			{repo("mock-agent", PillRunning, "publishing", "v0.9.0"), repo("mock-ui", PillRunning, "merging", "")},
			{repo("mock-agent-action", PillPending, "waiting", "")},
		},
	}
}

func TestTrainBoard(t *testing.T) {
	tr := board()
	golden(t, "train-running", TrainBoard(tr))
	tr.Stages[1][1] = TrainRepo{Name: "mock-ui", Pill: PillHeld, Status: "blocked: release checks failed"}
	tr.DryRun = true
	golden(t, "train-held", TrainBoard(tr))
	tr = board()
	tr.Stage, tr.Finished = 3, true
	if got := TrainBoard(tr); got.Color != ColorGreen || got.Text != "Release train 2026-10: Finished · 3 stages" {
		t.Errorf("finished: color %s, text %q", got.Color, got.Text)
	}
	tr.Aborted = true
	if got := TrainBoard(tr); got.Color != ColorGrey {
		t.Errorf("aborted: color %s", got.Color)
	}
}

func TestDigestCard(t *testing.T) {
	d := Digest{
		Title: "Release digest 2026-10", Draft: true, IssueURL: gh + "workflows/issues/70",
		Repos: []DigestRepo{
			{Name: "mock-api", From: "v1.3.2", To: "v1.4.0", ChangelogURL: gh + "mock-api/releases/tag/v1.4.0"},
			{Name: "mock-ui", To: "v0.1.0", ChangelogURL: gh + "mock-ui/releases/tag/v0.1.0"},
		},
		Highlights: []string{"*mock-api*: evidence streaming", "*mock-ui*: first release"},
	}
	golden(t, "digest-draft", DigestCard(d))
	d.Draft = false
	if got := DigestCard(d); got.Color != ColorGreen || got.Header != ":newspaper: Release digest 2026-10" || got.Blocks[0].Type == "context" {
		t.Errorf("published digest: %+v", got)
	}
}

func TestNote(t *testing.T) {
	golden(t, "note", Note(":x: Failed again at `3333333`", "<https://example.com/run|run 9>"))
}
