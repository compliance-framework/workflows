package pluginhealth

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func report(candidates ...string) Report {
	rep := Report{Owner: "o", AgentRepo: "agent", Agent: "v0.9.0"}
	for _, n := range []string{"p1", "p2", "p3"} {
		st := Healthy
		if slices.Contains(candidates, n) {
			st = Candidate
		}
		rep.Rows = append(rep.Rows, Row{Repo: n, Lib: "v0.9.0", Protocol: 2, Status: st,
			Release: &Release{Tag: "v1.0.0", Date: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, Notes: []string{"a|b"}})
	}
	return rep
}

func TestMarkdown(t *testing.T) {
	rep := report("p2")
	rep.Rows[0].Bump = &BumpPR{Number: 7, URL: "u7", State: "closed", Merged: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
	rep.Rows[1].Behind, rep.Rows[1].Protocol = OlderMajor, 0
	rep.Rows[2].Behind, rep.Rows[2].Protocol, rep.Rows[2].Lib = -1, -1, ""
	md := Markdown(rep, State{Candidates: rep.Candidates(), Train: 3}, Header{Manifest: "repos.yaml", Batches: []int{1}, Run: "https://run", Docs: "https://docs"})
	for _, want := range []string{
		`<!-- plugin-health {"candidates":["p2"],"train":3} -->`,
		"[plugin-health](https://run)", "from `repos.yaml` (active batches [1])", "Latest agent final release: **v0.9.0**",
		"| [p1](https://github.com/o/p1) | v0.9.0 | 0 | v2 | merged 2026-10-08 ([#7](u7)) | v1.0.0 (2026-09-01) | healthy | a\\|b |",
		"| [p2](https://github.com/o/p2) | v0.9.0 | older major | unset | none |",
		"| [p3](https://github.com/o/p3) | – | – | – |",
		"Deprecation candidates: p2.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if !Changed(nil, []string{"a"}) || Changed([]string{"a", "b"}, []string{"b", "a"}) {
		t.Error("Changed")
	}
	if got := SlackMessage([]string{"a"}, nil, "u"); got != ":warning: Plugin deprecation candidates changed. Now: none. No longer: a. <u|Plugin health>" {
		t.Errorf("SlackMessage = %q", got)
	}
}
