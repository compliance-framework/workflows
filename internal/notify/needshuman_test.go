package notify

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRoute(t *testing.T) {
	const bot = ReleaseBotLogin
	pr := func(author, branch string, labelled bool) Run {
		return Run{Repo: "o/mock-ui", EventName: "pull_request", PRNumber: 13, PRAuthor: author, Branch: branch, PRNeedsHuman: labelled}
	}
	fail := func(jobs ...string) Result { return Result{Outcome: OutcomeFailure, FailedJobs: jobs} }
	pass := Result{Outcome: OutcomeSuccess}
	tests := []struct {
		name         string
		run          Run
		res          Result
		channel      bool
		wantIncident Result
		wantReason   string
	}{
		{"renovate major", pr("renovate[bot]", "renovate/typescript-7.x", true), pass, true, pass, "major update"},
		{"renovate major failing CI keeps its incident", pr(bot, "renovate/vite-8.x", true), fail("ci"), true, fail("ci"), "major update"},
		{"api OPA", pr(bot, "renovate/opa", true), pass, true, pass, "OPA update in the api (never auto-merged)"},
		{"ccf-bump auto-merge off", pr(bot, "ccf-bump/sync-2026-10-08", true), pass, true, pass, "auto-merge off (a major update or an unversioned pin)"},
		{"another bot PR labelled", pr(bot, "feature", true), pass, true, pass, "labelled needs-human"},
		{"bot PR without the label", pr(bot, "renovate/all-non-major", false), fail("ci"), true, fail("ci"), ""},
		{"release PR blocked: no incident", pr(bot, "release-please--branches--main", false), fail("release-checks"), true,
			Result{Outcome: OutcomeNone}, ReasonReleaseBlocked},
		{"release PR blocked and CI failed: the incident stays", pr(bot, "release-please--branches--main", false), fail("ci", "release-checks"), true,
			fail("ci", "release-checks"), ReasonReleaseBlocked},
		{"release PR, CI failed only", pr(bot, "release-please--branches--main", false), fail("ci"), true, fail("ci"), ""},
		{"no channel: incidents as before", pr(bot, "release-please--branches--main", true), fail("release-checks"), false, fail("release-checks"), ""},
		{"human PR labelled", pr("octocat", "fix", true), fail("release-checks"), true, fail("release-checks"), ""},
		{"human release-please branch", pr("octocat", "release-please--x", false), fail("release-checks"), true, fail("release-checks"), ""},
		{"push", Run{Repo: "o/r", EventName: "push", Branch: "renovate/x"}, fail("ci"), true, fail("ci"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			incident, reason := Route(tt.run, tt.res, tt.channel)
			if incident.Outcome != tt.wantIncident.Outcome || !slices.Equal(incident.FailedJobs, tt.wantIncident.FailedJobs) || reason != tt.wantReason {
				t.Errorf("Route = %+v, %q; want %+v, %q", incident, reason, tt.wantIncident, tt.wantReason)
			}
		})
	}
}

func TestNeedsHumanKey(t *testing.T) {
	a := Run{Repo: "o/r", PRNumber: 1, Workflow: "ci", RunID: "1"}
	b := Run{Repo: "o/r", PRNumber: 1, Workflow: "other", WorkflowPath: ".github/workflows/x.yml", RunID: "2"}
	if NeedsHumanKey(a) != NeedsHumanKey(b) {
		t.Error("the key depends on more than the repo and PR")
	}
	for _, c := range []Run{{Repo: "o/r", PRNumber: 2}, {Repo: "o/s", PRNumber: 1}} {
		if NeedsHumanKey(c) == NeedsHumanKey(a) {
			t.Errorf("%+v shares a's key", c)
		}
	}
	if k := NeedsHumanKey(a); !strings.HasPrefix(k, "ccf-notify-needs-human-") || len(k) != len("ccf-notify-needs-human-")+32 {
		t.Errorf("key = %q", k)
	}
}

func TestPostNeedsHuman(t *testing.T) {
	r := Run{Repo: "o/mock-ui", ServerURL: "https://github.com", PRNumber: 13, PRTitle: "chore(deps): update typescript to v7 <major>"}
	p := &fakePoster{}
	rec, err := PostNeedsHuman(context.Background(), p, r, "major update", "C0HUMAN")
	if err != nil {
		t.Fatal(err)
	}
	want := ":raising_hand: <https://github.com/o/mock-ui/pull/13|mock-ui#13> chore(deps): update typescript to v7 &lt;major&gt; — needs a human: major update"
	if len(p.posts) != 1 || p.posts[0].text != want || p.posts[0].channel != "C0HUMAN" || p.posts[0].threadTS != "" {
		t.Errorf("posts = %+v, want %q", p.posts, want)
	}
	if rec != (Posted{Key: NeedsHumanKey(r), Channel: "C0RESOLVED", TS: "1700000000.000100"}) {
		t.Errorf("record = %+v", rec)
	}
	path := filepath.Join(t.TempDir(), "rec.json")
	if err := rec.Save(path); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), `"channel":"C0RESOLVED"`) {
		t.Errorf("saved %q, %v", data, err)
	}
	if _, err := PostNeedsHuman(context.Background(), p, r, "x", ""); err == nil {
		t.Error("no error without a channel")
	}
}

func TestRunFromEnvLabels(t *testing.T) {
	for labels, want := range map[string]bool{`[{"name":"dependencies"},{"name":"needs-human"}]`: true, `[{"name":"needs-humans"}]`: false, `[]`: false} {
		r, err := RunFromEnv(getenv(t, "pull_request", "refs/pull/5/merge",
			`{"pull_request":{"number":5,"user":{"login":"renovate[bot]"},"head":{"ref":"renovate/x","sha":"2"},"labels":`+labels+`}}`))
		if err != nil || r.PRNeedsHuman != want {
			t.Errorf("labels %s: PRNeedsHuman = %t, %v; want %t", labels, r.PRNeedsHuman, err, want)
		}
	}
}
