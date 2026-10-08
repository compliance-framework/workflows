package train

import (
	"reflect"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/slackkit"
)

func TestBoard(t *testing.T) {
	st := &State{Month: "2026-12", Status: StatusOpen, Repos: []*RepoState{
		{Name: "mock-api", Stage: 1, Phase: Released, Versions: map[string]string{".": "0.2.0"}},
		{Name: "mock-gooci", Stage: 1, Phase: Skipped},
		{Name: "mock-agent", Stage: 2, Phase: Merging, Hold: Blocked, Detail: "checks failing"},
		{Name: "mock-ui", Stage: 2, Phase: Publishing},
		{Name: "mock-helm-charts", Stage: 3, Phase: Waiting},
	}}
	now := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	repo := func(name string, p slackkit.Pill, status, v string) slackkit.TrainRepo {
		return slackkit.TrainRepo{Name: name, URL: "https://github.com/o/" + name, Pill: p, Status: status, Version: v}
	}
	want := slackkit.TrainBoard(slackkit.Train{
		Title: "Release train 2026-12", Stage: 2, IssueURL: "https://issue", UpdatedAt: now,
		Stages: [][]slackkit.TrainRepo{
			{repo("mock-api", slackkit.PillDone, "released", "v0.2.0"), repo("mock-gooci", slackkit.PillSkipped, "skipped", "")},
			{repo("mock-agent", slackkit.PillHeld, "blocked", ""), repo("mock-ui", slackkit.PillRunning, "publishing", "")},
			{repo("mock-helm-charts", slackkit.PillPending, "waiting", "")},
		},
	})
	got := Board(st, "https://github.com/o/", "https://issue", now)
	if !reflect.DeepEqual(got, want) || got.Color != slackkit.ColorRed {
		t.Errorf("board = %+v\nwant %+v", got, want)
	}
	// The hash ignores the time, and changes with the content.
	h := boardHash(st, "https://github.com/o", "https://issue")
	st.Repos[2].Hold = NoHold
	if h == "" || h == boardHash(st, "https://github.com/o", "https://issue") {
		t.Errorf("hash %q didn't change with the content", h)
	}
	st.Status = StatusFinished
	if got := Board(st, "", "", now); got.Color != slackkit.ColorGreen || got.Text != "Release train 2026-12: Finished · 3 stages" {
		t.Errorf("finished board = %q, %s", got.Text, got.Color)
	}
}
