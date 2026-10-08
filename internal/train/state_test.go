package train

import (
	"reflect"
	"strings"
	"testing"
)

func testState() *State {
	return &State{
		Month: "2026-12", Manifest: "repos.mock.yaml", Status: StatusOpen, Channel: "C1", ThreadTS: "1.2",
		Repos: []*RepoState{
			{Name: "mock-api", Stage: 1, Phase: Released, From: map[string]string{".": "0.1.0"}, Versions: map[string]string{".": "0.2.0"}, ReleasePR: 4},
			{Name: "mock-agent", Stage: 2, Phase: Merging, Hold: Blocked, Detail: "checks failing on #9: ci / go | x", BumpPR: 8, ReleasePR: 9},
			{Name: "mock-helm-charts", Stage: 3, Phase: Waiting, From: map[string]string{"charts/mock-app": "0.2.0", "charts/mock-agent": "0.2.0"}},
		},
		Notified: []string{"mock-agent|merging|blocked|x"},
	}
}

func TestRenderParseRoundTrip(t *testing.T) {
	s := testState()
	s.Repos[1].Detail += " --> <!-- injected"
	body, err := Render(s, "https://github.com/o", "Comment /skip <repo>.")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Release train 2026-12 over `repos.mock.yaml`: **open**.",
		"| 1 | mock-api | released | v0.1.0 | v0.2.0 | release [mock-api#4](https://github.com/o/mock-api/pull/4) |  |",
		"| 2 | mock-agent | blocked |  |  | bump [mock-agent#8](https://github.com/o/mock-agent/pull/8), release [mock-agent#9](https://github.com/o/mock-agent/pull/9) | " +
			`checks failing on #9: ci / go \| x --> <!-- injected |`,
		"| 3 | mock-helm-charts | waiting | mock-agent v0.2.0, mock-app v0.2.0 |",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	got, err := Parse("edited above\n" + body + "\nedited below")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, s)
	}
}

func TestParseErrors(t *testing.T) {
	for _, body := range []string{"no state", "<!-- train-state\n{}", "<!-- train-state\n{nope\n-->"} {
		if _, err := Parse(body); err == nil {
			t.Errorf("Parse(%q): want an error", body)
		}
	}
}

func TestStages(t *testing.T) {
	s := testState()
	if s.Stages() != 3 || !s.StageOpen(2) || s.StageOpen(3) || s.AllDone() {
		t.Errorf("Stages=%d StageOpen(2)=%v StageOpen(3)=%v AllDone=%v", s.Stages(), s.StageOpen(2), s.StageOpen(3), s.AllDone())
	}
	s.Repos[1].Phase, s.Repos[2].Phase = Skipped, Released
	if !s.StageOpen(3) || !s.AllDone() || s.Repo("mock-agent") != s.Repos[1] || s.Repo("nope") != nil {
		t.Error("after skip and release: want stage 3 open and all done")
	}
}

func TestNotified(t *testing.T) {
	s := testState()
	if s.MarkNotified("mock-agent|merging|blocked|x") || !s.MarkNotified("mock-api|x") || !s.MarkNotified("mock-agent-action|y") {
		t.Fatal("MarkNotified: want false for a known key, true for new ones")
	}
	s.ForgetNotified("mock-agent")
	if want := []string{"mock-api|x", "mock-agent-action|y"}; !reflect.DeepEqual(s.Notified, want) {
		t.Errorf("Notified = %v, want %v", s.Notified, want)
	}
}

func TestTitle(t *testing.T) {
	tests := []struct {
		dryRun   bool
		existing []string
		want     string
	}{
		{false, nil, "Release train 2026-12"},
		{false, []string{"Release train 2026-12", "Release train 2026-12 (2)"}, "Release train 2026-12 (3)"},
		{true, []string{"Release train 2026-12"}, "Release train 2026-12 (dry run)"},
		{true, []string{"Release train 2026-12 (dry run)"}, "Release train 2026-12 (dry run 2)"},
	}
	for _, tt := range tests {
		if got := Title("2026-12", tt.dryRun, tt.existing); got != tt.want {
			t.Errorf("Title(%v, %v) = %q, want %q", tt.dryRun, tt.existing, got, tt.want)
		}
	}
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		body    string
		want    Command
		ok, err bool
	}{
		{"/skip mock-agent", Command{"skip", "mock-agent"}, true, false},
		{"  /retry mock-ui  \nbecause the PR is fixed", Command{"retry", "mock-ui"}, true, false},
		{"/abort", Command{Name: "abort"}, true, false},
		{"/skip", Command{}, true, true},
		{"/abort now please", Command{}, true, true},
		{"/unknown x", Command{}, false, false},
		{"looks good", Command{}, false, false},
		{"", Command{}, false, false},
	}
	for _, tt := range tests {
		c, ok, err := ParseCommand(tt.body)
		if c != tt.want || ok != tt.ok || (err != nil) != tt.err {
			t.Errorf("ParseCommand(%q) = %+v, %v, %v", tt.body, c, ok, err)
		}
	}
}
