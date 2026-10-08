package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/slackkit"
)

func TestParseNeeds(t *testing.T) {
	tests := []struct {
		name, needs string
		want        Result
		wantErr     string
	}{
		{"legacy caller without needs", "", Result{Outcome: OutcomeFailure}, ""},
		{"all passed", `{"ci":{"result":"success","outputs":{}},"release-checks":{"result":"success"}}`, Result{Outcome: OutcomeSuccess}, ""},
		{"passed with a skip", `{"ci":{"result":"success"},"release-checks":{"result":"skipped"}}`, Result{Outcome: OutcomeSuccess}, ""},
		{"failed jobs, sorted", `{"z":{"result":"failure"},"ci":{"result":"failure"},"ok":{"result":"success"},"c":{"result":"cancelled"}}`, Result{Outcome: OutcomeFailure, FailedJobs: []string{"ci", "z"}}, ""},
		{"cancelled", `{"ci":{"result":"cancelled"},"release-checks":{"result":"success"}}`, Result{Outcome: OutcomeNone}, ""},
		{"all skipped", `{"ci":{"result":"skipped"}}`, Result{Outcome: OutcomeNone}, ""},
		{"no jobs", `{}`, Result{}, "no jobs"},
		{"not JSON", `{`, Result{}, "parsing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseNeeds(tt.needs)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseNeeds = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestLoadAndSaveIncident(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if inc, err := LoadIncident(path, "k-"); err != nil || !reflect.DeepEqual(inc, Incident{}) {
		t.Errorf("missing file: %+v, %v", inc, err)
	}
	want := Incident{Key: "k-", Channel: "C1", TS: "1.2", Open: true, SHA: "abc", FailedJobs: []string{"ci"}}
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadIncident(path, "k-"); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("LoadIncident = %+v, %v; want %+v", got, err, want)
	}
	if got, err := LoadIncident(path, "other-"); err == nil || got.Open {
		t.Errorf("another key: %+v, %v; want no incident and an error", got, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadIncident(path, "k-"); err == nil || got.Open {
		t.Errorf("bad JSON: %+v, %v; want no incident and an error", got, err)
	}
}

var t0 = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)

// call is a Fake call without the message, which the card tests check.
type call struct{ method, channel, ts, text string }

func calls(f *slackkit.Fake) []call {
	var got []call
	for _, c := range f.Calls {
		got = append(got, call{c.Method, c.Channel, c.TS, c.Message.Text})
	}
	return got
}

func TestHandle(t *testing.T) {
	r := Run{Repo: "o/r", Workflow: "ci", ServerURL: "https://github.com", SHA: "2222222222", RunID: "42", PRNumber: 5, PRTitle: "fix: x"}
	open := Incident{Key: "k-", Channel: "C0OLD", TS: "1600000000.000100", Open: true, SHA: "1111111111", FailedJobs: []string{"ci"},
		OpenedSHA: "1111111111", OpenedAt: t0.Add(-time.Hour)}
	closed := open
	closed.Open, closed.ResolvedAt = false, t0.Add(-time.Minute)
	sameCommit := open
	sameCommit.SHA = r.SHA
	failCI := Result{Outcome: OutcomeFailure, FailedJobs: []string{"ci"}}
	opened := Incident{Key: "k-", Channel: "C0CIFAIL", TS: "1.000001", Open: true, SHA: r.SHA, FailedJobs: []string{"ci"}, OpenedSHA: r.SHA, OpenedAt: t0}
	failingAgain := func(prev Incident, jobs ...string) Incident {
		prev.SHA, prev.FailedJobs, prev.Again = r.SHA, jobs, true
		return prev
	}
	resolved := open
	resolved.Open, resolved.SHA, resolved.ResolvedAt = false, r.SHA, t0

	tests := []struct {
		name       string
		prev       Incident
		res        Result
		wantAction Action
		wantCalls  []call
		wantNext   Incident
		wantStatus slackkit.IncidentStatus // of the card posted or updated
	}{
		{"failure, no incident: opens one", Incident{}, failCI, ActionOpen,
			[]call{{"post", "C0CIFAIL", "", "CI failing: o/r#5 ci"}}, opened, slackkit.Failing},
		{"failure after a closed incident: opens a new one", closed, failCI, ActionOpen,
			[]call{{"post", "C0CIFAIL", "", "CI failing: o/r#5 ci"}}, opened, slackkit.Failing},
		{"failure, same commit and jobs: dedupe", sameCommit, failCI, ActionDedupe, nil, sameCommit, ""},
		{"failure, same commit, other jobs: replies and edits", sameCommit, Result{Outcome: OutcomeFailure, FailedJobs: []string{"ci", "release-checks"}}, ActionReply,
			[]call{{"reply", "C0OLD", open.TS, FailureReply(r, []string{"ci", "release-checks"})}, {"update", "C0OLD", open.TS, "CI failing again: o/r#5 ci"}},
			failingAgain(sameCommit, "ci", "release-checks"), slackkit.FailingAgain},
		{"failure, new commit: replies and edits", open, failCI, ActionReply,
			[]call{{"reply", "C0OLD", open.TS, FailureReply(r, failCI.FailedJobs)}, {"update", "C0OLD", open.TS, "CI failing again: o/r#5 ci"}},
			failingAgain(open, "ci"), slackkit.FailingAgain},
		{"success, open incident: recovers and edits", open, Result{Outcome: OutcomeSuccess}, ActionRecover,
			[]call{{"reply", "C0OLD", open.TS, RecoveryReply(r)}, {"update", "C0OLD", open.TS, "CI resolved: o/r#5 ci"}}, resolved, slackkit.Resolved},
		{"success, no incident: nothing", Incident{}, Result{Outcome: OutcomeSuccess}, ActionNone, nil, Incident{}, ""},
		{"success, closed incident: nothing", closed, Result{Outcome: OutcomeSuccess}, ActionNone, nil, closed, ""},
		{"cancelled: nothing", open, Result{Outcome: OutcomeNone}, ActionNone, nil, open, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &slackkit.Fake{}
			next, action, err := Handle(context.Background(), f, "k-", tt.prev, r, tt.res, "C0CIFAIL", t0)
			if err != nil {
				t.Fatal(err)
			}
			if action != tt.wantAction || !reflect.DeepEqual(next, tt.wantNext) {
				t.Errorf("Handle = %+v, %s; want %+v, %s", next, action, tt.wantNext, tt.wantAction)
			}
			if got := calls(f); !reflect.DeepEqual(got, tt.wantCalls) {
				t.Errorf("calls = %+v, want %+v", got, tt.wantCalls)
			}
			if tt.wantStatus != "" {
				card := f.Calls[len(f.Calls)-1].Message
				if want := IncidentCard(r, tt.wantNext, t0); !reflect.DeepEqual(card, want) || !strings.Contains(card.Text, strings.ToLower(string(tt.wantStatus))) {
					t.Errorf("card = %+v, want %+v (%s)", card, want, tt.wantStatus)
				}
			}
		})
	}
}

func TestHandleErrors(t *testing.T) {
	r := Run{Repo: "o/r", SHA: "abc", RunID: "1"}
	fail := Result{Outcome: OutcomeFailure}
	if _, _, err := Handle(context.Background(), &slackkit.Fake{}, "k-", Incident{}, r, fail, "", t0); err == nil || !strings.Contains(err.Error(), "channel") {
		t.Errorf("no channel: err = %v", err)
	}
	open := Incident{Key: "k-", Channel: "C1", TS: "1.1", Open: true, SHA: "old"}
	for _, tt := range []struct {
		prev Incident
		res  Result
	}{{Incident{}, fail}, {open, fail}, {open, Result{Outcome: OutcomeSuccess}}} {
		next, action, err := Handle(context.Background(), &slackkit.Fake{Err: errors.New("boom")}, "k-", tt.prev, r, tt.res, "C1", t0)
		if err == nil || action != ActionNone || !reflect.DeepEqual(next, tt.prev) {
			t.Errorf("Post error from %+v: %+v, %s, %v; want the state unchanged and the error", tt.prev, next, action, err)
		}
	}
	// The reply is posted but the card isn't edited: the next state comes with a *CardError.
	next, action, err := Handle(context.Background(), &updateFails{}, "k-", open, r, Result{Outcome: OutcomeSuccess}, "C1", t0)
	var cardErr *CardError
	if !errors.As(err, &cardErr) || action != ActionRecover || next.Open || !strings.Contains(err.Error(), "message_not_found") {
		t.Errorf("update error: %+v, %s, %v; want the recovered state and a *CardError", next, action, err)
	}
}

// updateFails is a Slack whose chat.update fails.
type updateFails struct{ slackkit.Fake }

func (u *updateFails) Update(context.Context, string, string, slackkit.Message) error {
	return errors.New("message_not_found")
}

func TestIncidentCard(t *testing.T) {
	pr := Run{Repo: "o/r", Workflow: "ci", ServerURL: "https://github.com", Branch: "renovate/x", RunID: "42", PRNumber: 12, PRTitle: "fix(deps): a & b"}
	inc := Incident{Open: true, SHA: "3333333333", OpenedSHA: "2222222222", OpenedAt: t0, FailedJobs: []string{"ci"}}
	got := slackkit.IncidentCard(slackkit.Incident{
		Repo: "o/r", Ref: "o/r#12", RefURL: "https://github.com/o/r/pull/12", Title: "fix(deps): a & b", Workflow: "ci",
		Status: slackkit.Failing, SHA: "2222222222", SHAURL: "https://github.com/o/r/commit/2222222222", FailedJobs: []string{"ci"},
		OpenedAt: t0, PRURL: "https://github.com/o/r/pull/12", RunURL: "https://github.com/o/r/actions/runs/42", UpdatedAt: t0,
	})
	if card := IncidentCard(pr, inc, t0); !reflect.DeepEqual(card, got) {
		t.Errorf("PR card = %+v\nwant %+v", card, got)
	}
	// A push, and a state saved before cards (no opened_sha): the last commit stands in.
	push := Run{Repo: "o/r", Workflow: "ci", ServerURL: "https://github.com", Branch: "main", RunID: "43"}
	card := IncidentCard(push, Incident{Open: true, SHA: "abc"}, t0)
	if card.Text != "CI failing: o/r@main ci" || !strings.Contains(card.Blocks[0].Text.Text, "<https://github.com/o/r/tree/main|o/r@main>") ||
		!strings.Contains(card.Blocks[1].Fields[2].Text, "commit/abc|abc>") {
		t.Errorf("push card = %+v", card)
	}
	if strings.Contains(fmt.Sprint(card.Blocks), "View PR") {
		t.Errorf("a push card has a View PR button: %+v", card.Blocks)
	}
}

func TestReplies(t *testing.T) {
	push := Run{Repo: "o/r", Workflow: "ci", ServerURL: "https://github.com", SHA: "abc", Branch: "main", RunID: "43"}
	want := "❌ failed: a&lt;b&gt; at <https://github.com/o/r/commit/abc|abc> · <https://github.com/o/r/actions/runs/43|run>"
	if got := FailureReply(push, []string{"a<b>"}); got != want {
		t.Errorf("FailureReply = %s, want %s", got, want)
	}
	want = "✅ passing again at <https://github.com/o/r/commit/abc|abc> · <https://github.com/o/r/actions/runs/43|run>"
	if got := RecoveryReply(push); got != want {
		t.Errorf("RecoveryReply = %s, want %s", got, want)
	}
}
