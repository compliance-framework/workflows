package train

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestEvaluateChecks(t *testing.T) {
	req := Check{ID: 1, Name: CICheck, Status: "completed", Conclusion: "success"}
	rel := Check{ID: 20, Name: ReleaseCheck, Status: "completed", Conclusion: "success"}
	ci, release := []string{CICheck}, []string{CICheck, ReleaseCheck}
	tests := []struct {
		name          string
		checks        []Check
		required      []string
		state, detail string
	}{
		{"green, skipped counts as passing", []Check{req, {ID: 2, Name: "notify", Status: "completed", Conclusion: "skipped"}}, ci, ChecksGreen, ""},
		{"no checks yet", nil, ci, ChecksPending, CICheck},
		{"the required check is missing", []Check{{ID: 2, Name: "ci / go", Status: "completed", Conclusion: "success"}}, ci, ChecksPending, CICheck},
		{"a required check never reported", []Check{req}, release, ChecksPending, ReleaseCheck},
		{"required checks pass", []Check{req, rel}, release, ChecksGreen, ""},
		{"running", []Check{at(2, 2, CICheck, "in_progress", "")}, ci, ChecksPending, CICheck},
		{"failing wins over pending", []Check{at(1, 1, CICheck, "completed", "failure"), {ID: 3, Name: ReleaseCheck, Status: "queued"}}, release, ChecksFailing, CICheck},
		{"skipped and neutral required checks pass", []Check{at(1, 1, CICheck, "completed", "skipped"), at(2, 1, ReleaseCheck, "completed", "neutral")}, release, ChecksGreen, ""},
		// Checks that are not required never wait or hold, whatever they report.
		{"a cancelled preview, required green", []Check{req, rel, {ID: 4, Name: "preview / plugin", Status: "completed", Conclusion: "cancelled"},
			{ID: 5, Name: "preview / policies", Status: "completed", Conclusion: "cancelled"}}, release, ChecksGreen, ""},
		{"a failing check that is not required", []Check{req, {ID: 2, Name: "ci / go", Status: "completed", Conclusion: "failure"},
			{ID: 3, Name: "CodeRabbit", Status: "completed", Conclusion: "timed_out"}, {ID: 4, Name: "osv-scanner", Status: "in_progress"}}, ci, ChecksGreen, ""},
		// A cancelled run was superseded or stopped: the check waits for a newer run, never fails.
		{"the newest required run was cancelled", []Check{at(1, 1, CICheck, "completed", "success"), at(2, 2, CICheck, "completed", "cancelled")}, ci, ChecksPending, CICheck},
		{"a cancelled run, then a newer success", []Check{at(2, 1, CICheck, "completed", "cancelled"), at(1, 2, CICheck, "completed", "success")}, ci, ChecksGreen, ""},
		{"a cancelled run, then a newer failure", []Check{at(1, 1, CICheck, "completed", "cancelled"), at(2, 2, CICheck, "completed", "failure")}, ci, ChecksFailing, CICheck},
		// Several runs of one check on a commit (a re-run, or a run for labeled).
		{"old success, new run in progress", []Check{req, at(2, 2, CICheck, "in_progress", "")}, ci, ChecksPending, CICheck},
		{"old failure, new run in progress", []Check{at(1, 1, CICheck, "completed", "failure"), at(3, 2, CICheck, "queued", "")}, ci, ChecksPending, CICheck},
		{"newer run done, older still running", []Check{at(1, 1, CICheck, "in_progress", ""), at(2, 2, CICheck, "completed", "success")}, ci, ChecksPending, CICheck},
		{"old failure, new success", []Check{at(2, 2, CICheck, "completed", "success"), at(1, 1, CICheck, "completed", "failure")}, ci, ChecksGreen, ""},
		{"old success, new failure", []Check{req, at(2, 2, CICheck, "completed", "failure")}, ci, ChecksFailing, CICheck},
		{"newest by start time, not ID", []Check{at(9, 1, CICheck, "completed", "failure"), at(2, 3, CICheck, "completed", "success")}, ci, ChecksGreen, ""},
		{"a failing required check holds", []Check{req, at(2, 1, ReleaseCheck, "completed", "failure")}, release, ChecksFailing, ReleaseCheck},
	}
	for _, tt := range tests {
		state, detail := EvaluateChecks(tt.checks, tt.required)
		if state != tt.state || detail != tt.detail {
			t.Errorf("%s: got %s %q, want %s %q", tt.name, state, detail, tt.state, tt.detail)
		}
	}
	if state, _ := EvaluateChecks([]Check{{ID: 1, Name: "x", Status: "completed", Conclusion: "failure"}}, nil); state != ChecksGreen {
		t.Errorf("no required checks: %s", state)
	}
}

func TestRequiredChecks(t *testing.T) {
	for _, tc := range []struct {
		configured string
		releasePR  bool
		want       []string
	}{
		{"", false, []string{CICheck}}, // unset never means "no required check"
		{"", true, []string{CICheck, ReleaseCheck}},
		{"ci / all", false, []string{"ci / all"}},
		{"ci / all", true, []string{"ci / all", ReleaseCheck}},
	} {
		e := &Engine{RequiredCheck: tc.configured}
		if got := e.requiredChecks(tc.releasePR); !slices.Equal(got, tc.want) {
			t.Errorf("requiredChecks(%v) with %q = %q, want %q", tc.releasePR, tc.configured, got, tc.want)
		}
	}
}

// at is a check run with ID id, started minute minutes past 05:00.
func at(id int64, minute int, name, status, conclusion string) Check {
	return Check{ID: id, Name: name, Status: status, Conclusion: conclusion, StartedAt: time.Date(2026, 10, 8, 5, minute, 0, 0, time.UTC)}
}

func TestExpectsCheck(t *testing.T) {
	expected := `{"message":"Required status check \"ci / required\" is expected."}`
	// refused is GitHub's 405 for a ruleset violation, as on mock-plugin-2#30 (run 37915939413).
	refused := func(violation string) error {
		return &StatusError{Status: http.StatusMethodNotAllowed, Body: `{"message":"Repository rule violations found\n\n` + violation + `\n\n"}`}
	}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"405 check expected":     {&StatusError{Status: http.StatusMethodNotAllowed, Body: expected}, true},
		"405 check queued":       {refused(`Required status check \"ci / required\" is queued.`), true},
		"405 check in progress":  {refused(`Required status check \"ci / required\" is in progress.`), true},
		"405 check pending":      {refused(`Required status check \"ci / required\" is pending.`), true},
		"405 checks expected":    {refused(`2 of 2 required status checks are expected.`), true},
		"405 check failing":      {refused(`Required status check \"ci / required\" is failing.`), false},
		"405 one of two failing": {refused(`Required status check \"a\" is queued.\n\nRequired status check \"b\" is failing.`), false},
		"405 review required":    {refused(`At least 1 approving review is required by reviewers with write access.`), false},
		"405 other":              {&StatusError{Status: http.StatusMethodNotAllowed, Body: `{"message":"Pull Request is not mergeable"}`}, false},
		"wrapped":                {fmt.Errorf("merge: %w", &StatusError{Status: http.StatusMethodNotAllowed, Body: expected}), true},
		"409 head moved":         {&StatusError{Status: http.StatusConflict, Body: expected}, false},
		"not a StatusError":      {errors.New("Required status check is expected"), false},
		"nil":                    {nil, false},
	} {
		if got := ExpectsCheck(tc.err); got != tc.want {
			t.Errorf("%s: ExpectsCheck = %v, want %v", name, got, tc.want)
		}
	}
}

// TestMergeGreenRuns: mergeGreen judges only the required checks, each by its newest run, and
// waits, never holds, on a cancelled run, a missing check, or while GitHub still expects one.
func TestMergeGreenRuns(t *testing.T) {
	expected := &StatusError{Status: http.StatusMethodNotAllowed, Body: `{"message":"Required status check \"ci / required\" is expected."}`}
	for name, tc := range map[string]struct {
		checks    []Check
		release   bool // a release-please PR: release-checks is required too
		mergeErrs []error
		merged    bool
		hold      Hold
		detail    string
	}{
		"old success, new run in progress": {
			checks: []Check{at(1, 1, "ci / required", "completed", "success"), at(2, 2, "ci / required", "in_progress", "")},
			detail: "waiting for checks on [mock-api#7](https://github.com/compliance-framework/mock-api/pull/7): ci / required",
		},
		"old failure, new success": {
			checks: []Check{at(1, 1, "ci / required", "completed", "failure"), at(2, 2, "ci / required", "completed", "success")},
			merged: true,
		},
		"old success, new failure": {
			checks: []Check{at(1, 1, "ci / required", "completed", "success"), at(2, 2, "ci / required", "completed", "failure")},
			hold:   Blocked, detail: "checks failing on [mock-api#7](https://github.com/compliance-framework/mock-api/pull/7) at head7",
		},
		"a cancelled preview, required checks green": {
			checks: []Check{at(1, 1, CICheck, "completed", "success"), at(2, 1, ReleaseCheck, "completed", "success"),
				at(3, 2, "preview / plugin", "completed", "cancelled"), at(4, 2, "preview / policies", "completed", "cancelled")},
			release: true, merged: true,
		},
		"a failing check that is not required": {
			checks: []Check{at(1, 1, CICheck, "completed", "success"), at(2, 2, "osv-scanner", "completed", "failure")},
			merged: true,
		},
		"the newest required run was cancelled": {
			checks: []Check{at(1, 1, CICheck, "completed", "success"), at(2, 2, CICheck, "completed", "cancelled")},
			detail: "waiting for checks on [mock-api#7](https://github.com/compliance-framework/mock-api/pull/7): ci / required",
		},
		"release-checks never reported on a release PR": {
			checks: []Check{at(1, 1, CICheck, "completed", "success")}, release: true,
			detail: "waiting for checks on [mock-api#7](https://github.com/compliance-framework/mock-api/pull/7): release-checks / release-checks",
		},
		"a failing required check": {
			checks: []Check{at(1, 1, CICheck, "completed", "success"), at(2, 1, ReleaseCheck, "completed", "failure")}, release: true,
			hold: Blocked, detail: "at head7: release-checks / release-checks",
		},
		"GitHub still expects the check": {
			checks: []Check{at(1, 1, "ci / required", "completed", "success")}, mergeErrs: []error{expected},
			detail: "GitHub still expects a required check",
		},
		"GitHub reports the check queued": {
			checks: []Check{at(1, 1, "ci / required", "completed", "success")},
			mergeErrs: []error{&StatusError{Status: http.StatusMethodNotAllowed,
				Body: `{"message":"Repository rule violations found\n\nRequired status check \"ci / required\" is queued.\n\n"}`}},
			detail: "GitHub still expects a required check",
		},
		"another merge refusal": {
			checks: []Check{at(1, 1, "ci / required", "completed", "success")}, mergeErrs: []error{&StatusError{Status: http.StatusMethodNotAllowed, Body: "not mergeable"}},
			hold: Blocked, detail: "merging [mock-api#7](https://github.com/compliance-framework/mock-api/pull/7) failed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			repo := w.repo("mock-api", "0.1.0")
			pr := &PR{Number: 7, URL: ReleaseBranchPrefix + "main", HeadSHA: "head7", Open: true}
			repo.prs[7], repo.checks["head7"], w.mergeErrs = pr, tc.checks, tc.mergeErrs
			e := w.engine(trainDay)
			r := &RepoState{Name: "mock-api", Phase: Merging}
			required := e.requiredChecks(tc.release)
			merged, err := e.mergeGreen(context.Background(), r, pr, required)
			if err != nil || merged != tc.merged || r.Hold != tc.hold || !strings.Contains(r.Detail, tc.detail) || r.Phase != Merging {
				t.Fatalf("merged %v, %v; %s %s %q", merged, err, r.Phase, r.Hold, r.Detail)
			}
			if tc.merged || tc.hold != NoHold {
				return
			}
			// Still waiting: the next reconcile merges once the running runs are done and every
			// required check has a newer passing run.
			for i, c := range repo.checks["head7"] {
				if c.Status != "completed" {
					repo.checks["head7"][i].Status, repo.checks["head7"][i].Conclusion = "completed", "success"
				}
			}
			for _, name := range required {
				repo.checks["head7"] = append(repo.checks["head7"], at(int64(100+len(repo.checks["head7"])), 30, name, "completed", "success"))
			}
			if merged, err := e.mergeGreen(context.Background(), r, pr, required); err != nil || !merged || r.Hold != NoHold {
				t.Errorf("next reconcile: merged %v, %v; %s %q", merged, err, r.Hold, r.Detail)
			}
		})
	}
}
