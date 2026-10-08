package notify

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/slackkit"
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
		{"closed labelled bot PR", func() Run { r := pr(bot, "renovate/vite-8.x", true); r.PRClosed = true; return r }(), pass, true, pass, ""},
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
	r := Run{Repo: "o/mock-ui", ServerURL: "https://github.com", PRNumber: 13, PRTitle: "chore(deps): update typescript to v7 <major>",
		PRAuthor: "ccf-release-bot[bot]", PRCreatedAt: t0.Add(-26 * time.Hour)}
	f := &slackkit.Fake{}
	res := Result{Outcome: OutcomeFailure, FailedJobs: []string{"go", "required"}}
	rec, err := PostNeedsHuman(context.Background(), f, r, res, "major update", "C0HUMAN", t0)
	if err != nil {
		t.Fatal(err)
	}
	want := slackkit.NeedsHumanCard(slackkit.NeedsHuman{
		Repo: "o/mock-ui", Ref: "o/mock-ui#13", URL: "https://github.com/o/mock-ui/pull/13", Title: r.PRTitle, Why: "Major update",
		CI: ":x: Failing: go, required", OpenedBy: "ccf-release-bot[bot]", OpenedAt: r.PRCreatedAt, Now: t0,
	})
	if len(f.Calls) != 1 || f.Calls[0].Method != "post" || f.Calls[0].Channel != "C0HUMAN" || !reflect.DeepEqual(f.Calls[0].Message, want) {
		t.Errorf("calls = %+v, want the card %+v", f.Calls, want)
	}
	if rec != (Posted{Key: NeedsHumanKey(r), Channel: "C0HUMAN", TS: "1.000001", Reason: "major update", CI: ":x: Failing: go, required"}) {
		t.Errorf("record = %+v", rec)
	}
	path := filepath.Join(t.TempDir(), "rec.json")
	if err := rec.Save(path); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), `"ts":"1.000001","reason":"major update"`) {
		t.Errorf("saved %q, %v", data, err)
	}
	if _, err := PostNeedsHuman(context.Background(), f, r, res, "x", "", t0); err == nil {
		t.Error("no error without a channel")
	}
	for res, want := range map[Outcome]string{OutcomeSuccess: ":white_check_mark: Passing", OutcomeFailure: ":x: Failing", OutcomeNone: ":grey_question: Cancelled or skipped"} {
		if got := ciStatus(Result{Outcome: res}); got != want {
			t.Errorf("ciStatus(%s) = %q, want %q", res, got, want)
		}
	}
}

func TestMarkHandled(t *testing.T) {
	r := Run{Repo: "o/mock-ui", ServerURL: "https://github.com", PRNumber: 13, PRTitle: "chore(deps): x", PRAuthor: "ccf-release-bot[bot]",
		PRCreatedAt: t0.Add(-26 * time.Hour), PRClosed: true, PRMerged: true, PRClosedBy: "octocat"}
	rec := Posted{Key: NeedsHumanKey(r), Channel: "C0HUMAN", TS: "1.5", Reason: "major update", CI: ":white_check_mark: Passing"}
	f := &slackkit.Fake{}
	if err := MarkHandled(context.Background(), f, rec, r, t0); err != nil {
		t.Fatal(err)
	}
	want := slackkit.NeedsHumanCard(slackkit.NeedsHuman{
		Repo: "o/mock-ui", Ref: "o/mock-ui#13", URL: "https://github.com/o/mock-ui/pull/13", Title: "chore(deps): x", Why: "Major update",
		CI: ":white_check_mark: Passing", OpenedBy: "ccf-release-bot[bot]", OpenedAt: r.PRCreatedAt, Handled: "merged", HandledBy: "octocat", HandledAt: t0,
	})
	if len(f.Calls) != 1 || f.Calls[0].Method != "update" || f.Calls[0].Channel != "C0HUMAN" || f.Calls[0].TS != "1.5" || !reflect.DeepEqual(f.Calls[0].Message, want) {
		t.Errorf("calls = %+v, want an update to %+v", f.Calls, want)
	}
	if err := MarkHandled(context.Background(), f, Posted{}, r, t0); err != nil || len(f.Calls) != 1 {
		t.Errorf("no record: err %v, calls %+v", err, f.Calls)
	}

	path := filepath.Join(t.TempDir(), "rec.json")
	if got, err := LoadPosted(path, rec.Key); err != nil || got != (Posted{}) {
		t.Errorf("missing record: %+v, %v", got, err)
	}
	if err := rec.Save(path); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadPosted(path, rec.Key); err != nil || got != rec {
		t.Errorf("LoadPosted = %+v, %v; want %+v", got, err, rec)
	}
	if _, err := LoadPosted(path, "other"); err == nil {
		t.Error("a record for another key loads")
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPosted(path, rec.Key); err == nil {
		t.Error("a broken record loads")
	}
}

func TestRunFromEnvClosed(t *testing.T) {
	for state, want := range map[string]bool{`"closed"`: true, `"open"`: false, `""`: false} {
		r, err := RunFromEnv(getenv(t, "pull_request", "refs/pull/5/merge",
			`{"pull_request":{"number":5,"state":`+state+`,"user":{"login":"renovate[bot]"},"head":{"ref":"renovate/x","sha":"2"}}}`))
		if err != nil || r.PRClosed != want {
			t.Errorf("state %s: PRClosed = %t, %v; want %t", state, r.PRClosed, err, want)
		}
	}
	for event, want := range map[string]Run{
		`"merged":true,"merged_by":{"login":"octocat"}},"sender":{"login":"ccf-release-bot[bot]"}`: {PRMerged: true, PRClosedBy: "octocat"},
		`"merged":false,"merged_by":null},"sender":{"login":"hubot"}`:                              {PRClosedBy: "hubot"},
	} {
		r, err := RunFromEnv(getenv(t, "pull_request", "refs/pull/5/merge",
			`{"pull_request":{"number":5,"state":"closed","user":{"login":"renovate[bot]"},"head":{"ref":"renovate/x","sha":"2"},`+event+`}`))
		if err != nil || r.PRMerged != want.PRMerged || r.PRClosedBy != want.PRClosedBy {
			t.Errorf("%s: merged %t by %q, %v; want %+v", event, r.PRMerged, r.PRClosedBy, err, want)
		}
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
