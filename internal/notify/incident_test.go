package notify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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

type post struct{ channel, text, threadTS string }

type fakePoster struct {
	posts []post
	err   error
}

func (f *fakePoster) Post(_ context.Context, channel, text, threadTS string) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	f.posts = append(f.posts, post{channel, text, threadTS})
	return "C0RESOLVED", "1700000000.000100", nil
}

func TestHandle(t *testing.T) {
	r := Run{Repo: "o/r", Workflow: "ci", ServerURL: "https://github.com", SHA: "2222222222", RunID: "42", PRNumber: 5}
	open := Incident{Key: "k-", Channel: "C0OLD", TS: "1600000000.000100", Open: true, SHA: "1111111111", FailedJobs: []string{"ci"}}
	closed := open
	closed.Open, closed.FailedJobs = false, nil
	sameCommit := open
	sameCommit.SHA = r.SHA
	failCI := Result{Outcome: OutcomeFailure, FailedJobs: []string{"ci"}}

	tests := []struct {
		name       string
		prev       Incident
		res        Result
		wantAction Action
		wantPost   *post
		wantNext   Incident
	}{
		{"failure, no incident: opens one", Incident{}, failCI, ActionOpen,
			&post{"C0CIFAIL", OpenMessage(r, failCI.FailedJobs), ""},
			Incident{Key: "k-", Channel: "C0RESOLVED", TS: "1700000000.000100", Open: true, SHA: r.SHA, FailedJobs: []string{"ci"}}},
		{"failure after a closed incident: opens a new one", closed, failCI, ActionOpen,
			&post{"C0CIFAIL", OpenMessage(r, failCI.FailedJobs), ""},
			Incident{Key: "k-", Channel: "C0RESOLVED", TS: "1700000000.000100", Open: true, SHA: r.SHA, FailedJobs: []string{"ci"}}},
		{"failure, same commit and jobs: dedupe", sameCommit, failCI, ActionDedupe, nil, sameCommit},
		{"failure, same commit, other jobs: replies", sameCommit, Result{Outcome: OutcomeFailure, FailedJobs: []string{"ci", "release-checks"}}, ActionReply,
			&post{"C0OLD", FailureReply(r, []string{"ci", "release-checks"}), open.TS},
			Incident{Key: "k-", Channel: "C0OLD", TS: open.TS, Open: true, SHA: r.SHA, FailedJobs: []string{"ci", "release-checks"}}},
		{"failure, new commit: replies", open, failCI, ActionReply,
			&post{"C0OLD", FailureReply(r, failCI.FailedJobs), open.TS},
			Incident{Key: "k-", Channel: "C0OLD", TS: open.TS, Open: true, SHA: r.SHA, FailedJobs: []string{"ci"}}},
		{"success, open incident: recovers", open, Result{Outcome: OutcomeSuccess}, ActionRecover,
			&post{"C0OLD", RecoveryReply(r), open.TS},
			Incident{Key: "k-", Channel: "C0OLD", TS: open.TS, Open: false, SHA: r.SHA}},
		{"success, no incident: nothing", Incident{}, Result{Outcome: OutcomeSuccess}, ActionNone, nil, Incident{}},
		{"success, closed incident: nothing", closed, Result{Outcome: OutcomeSuccess}, ActionNone, nil, closed},
		{"cancelled: nothing", open, Result{Outcome: OutcomeNone}, ActionNone, nil, open},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakePoster{}
			next, action, err := Handle(context.Background(), p, "k-", tt.prev, r, tt.res, "C0CIFAIL")
			if err != nil {
				t.Fatal(err)
			}
			if action != tt.wantAction || !reflect.DeepEqual(next, tt.wantNext) {
				t.Errorf("Handle = %+v, %s; want %+v, %s", next, action, tt.wantNext, tt.wantAction)
			}
			switch {
			case tt.wantPost == nil && len(p.posts) != 0:
				t.Errorf("posted %+v, want nothing", p.posts)
			case tt.wantPost != nil && (len(p.posts) != 1 || p.posts[0] != *tt.wantPost):
				t.Errorf("posted %+v, want %+v", p.posts, *tt.wantPost)
			}
		})
	}
}

func TestHandleErrors(t *testing.T) {
	r := Run{Repo: "o/r", SHA: "abc", RunID: "1"}
	fail := Result{Outcome: OutcomeFailure}
	if _, _, err := Handle(context.Background(), &fakePoster{}, "k-", Incident{}, r, fail, ""); err == nil || !strings.Contains(err.Error(), "channel") {
		t.Errorf("no channel: err = %v", err)
	}
	open := Incident{Key: "k-", Channel: "C1", TS: "1.1", Open: true, SHA: "old"}
	for _, tt := range []struct {
		prev Incident
		res  Result
	}{{Incident{}, fail}, {open, fail}, {open, Result{Outcome: OutcomeSuccess}}} {
		next, action, err := Handle(context.Background(), &fakePoster{err: errors.New("boom")}, "k-", tt.prev, r, tt.res, "C1")
		if err == nil || action != ActionNone || !reflect.DeepEqual(next, tt.prev) {
			t.Errorf("Post error from %+v: %+v, %s, %v; want the state unchanged and the error", tt.prev, next, action, err)
		}
	}
}

func TestMessages(t *testing.T) {
	pr := Run{Repo: "o/r", Workflow: "ci <go>", ServerURL: "https://github.com", SHA: "2222222222", Branch: "renovate/x",
		RunID: "42", PRNumber: 12, PRTitle: "fix(deps): a & b"}
	want := "❌ o/r PR #12 failed: ci, release-checks at <https://github.com/o/r/commit/2222222222|2222222> · <https://github.com/o/r/actions/runs/42|run>\n" +
		"Pull request <https://github.com/o/r/pull/12|#12 fix(deps): a &amp; b> · workflow *ci &lt;go&gt;*"
	if got := OpenMessage(pr, []string{"ci", "release-checks"}); got != want {
		t.Errorf("OpenMessage =\n%s\nwant\n%s", got, want)
	}

	push := Run{Repo: "o/r", Workflow: "ci", ServerURL: "https://github.com", SHA: "abc", Branch: "main", RunID: "43"}
	want = "❌ o/r main failed at <https://github.com/o/r/commit/abc|abc> · <https://github.com/o/r/actions/runs/43|run>\n" +
		"workflow *ci*"
	if got := OpenMessage(push, nil); got != want {
		t.Errorf("OpenMessage =\n%s\nwant\n%s", got, want)
	}

	want = "❌ failed: a&lt;b&gt; at <https://github.com/o/r/commit/abc|abc> · <https://github.com/o/r/actions/runs/43|run>"
	if got := FailureReply(push, []string{"a<b>"}); got != want {
		t.Errorf("FailureReply = %s, want %s", got, want)
	}
	want = "✅ passing again at <https://github.com/o/r/commit/abc|abc> · <https://github.com/o/r/actions/runs/43|run>"
	if got := RecoveryReply(push); got != want {
		t.Errorf("RecoveryReply = %s, want %s", got, want)
	}
}
