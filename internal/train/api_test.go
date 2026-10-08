package train

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestEvaluateChecks(t *testing.T) {
	req := Check{ID: 1, Name: "ci / required", Status: "completed", Conclusion: "success"}
	tests := []struct {
		name          string
		checks        []Check
		state, detail string
	}{
		{"green, skipped counts as passing", []Check{req, {ID: 2, Name: "notify", Status: "completed", Conclusion: "skipped"}}, ChecksGreen, ""},
		{"no checks yet", nil, ChecksPending, "ci / required"},
		{"the required check is missing", []Check{{ID: 2, Name: "ci / go", Status: "completed", Conclusion: "success"}}, ChecksPending, "ci / required"},
		{"running", []Check{req, {ID: 2, Name: "ci / go", Status: "in_progress"}}, ChecksPending, "ci / go"},
		{"failing wins over pending", []Check{req, {ID: 2, Name: "ci / go", Status: "completed", Conclusion: "failure"}, {ID: 3, Name: "b", Status: "queued"},
			{ID: 4, Name: "a", Status: "completed", Conclusion: "timed_out"}}, ChecksFailing, "a, ci / go"},
		{"a re-run that passed replaces the failure", []Check{{ID: 5, Name: "ci / go", Status: "completed", Conclusion: "failure"}, req,
			{ID: 9, Name: "ci / go", Status: "completed", Conclusion: "success"}}, ChecksGreen, ""},
		{"the latest run failed", []Check{{ID: 9, Name: "ci / go", Status: "completed", Conclusion: "cancelled"}, req,
			{ID: 5, Name: "ci / go", Status: "completed", Conclusion: "success"}}, ChecksFailing, "ci / go"},
		// Several runs of one check on a commit (a re-run, or a run for labeled).
		{"old success, new run in progress", []Check{req, at(2, 2, "ci / required", "in_progress", "")}, ChecksPending, "ci / required"},
		{"old failure, new run in progress", []Check{req, at(2, 1, "ci / go", "completed", "failure"), at(3, 2, "ci / go", "queued", "")}, ChecksPending, "ci / go"},
		{"old failure, new success", []Check{at(2, 2, "ci / required", "completed", "success"), at(1, 1, "ci / required", "completed", "failure")}, ChecksGreen, ""},
		{"old success, new failure", []Check{req, at(2, 2, "ci / required", "completed", "failure")}, ChecksFailing, "ci / required"},
		{"newest by start time, not ID", []Check{at(9, 1, "ci / required", "completed", "failure"), at(2, 3, "ci / required", "completed", "success")}, ChecksGreen, ""},
	}
	for _, tt := range tests {
		state, detail := EvaluateChecks(tt.checks, "ci / required")
		if state != tt.state || detail != tt.detail {
			t.Errorf("%s: got %s %q, want %s %q", tt.name, state, detail, tt.state, tt.detail)
		}
	}
	if state, _ := EvaluateChecks(nil, ""); state != ChecksGreen {
		t.Errorf("no required check and no checks: %s", state)
	}
}

// at is a check run with ID id, started minute minutes past 05:00.
func at(id int64, minute int, name, status, conclusion string) Check {
	return Check{ID: id, Name: name, Status: status, Conclusion: conclusion, StartedAt: time.Date(2026, 10, 8, 5, minute, 0, 0, time.UTC)}
}

func TestExpectsCheck(t *testing.T) {
	expected := `{"message":"Required status check \"ci / required\" is expected."}`
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"405 check expected": {&StatusError{Status: http.StatusMethodNotAllowed, Body: expected}, true},
		"wrapped":            {fmt.Errorf("merge: %w", &StatusError{Status: http.StatusMethodNotAllowed, Body: expected}), true},
		"405 other":          {&StatusError{Status: http.StatusMethodNotAllowed, Body: `{"message":"Pull Request is not mergeable"}`}, false},
		"409 head moved":     {&StatusError{Status: http.StatusConflict, Body: expected}, false},
		"not a StatusError":  {errors.New("Required status check is expected"), false},
		"nil":                {nil, false},
	} {
		if got := ExpectsCheck(tc.err); got != tc.want {
			t.Errorf("%s: ExpectsCheck = %v, want %v", name, got, tc.want)
		}
	}
}

// TestMergeGreenRuns: mergeGreen judges ci / required by its newest run and waits, never holds,
// while GitHub still expects the check.
func TestMergeGreenRuns(t *testing.T) {
	expected := &StatusError{Status: http.StatusMethodNotAllowed, Body: `{"message":"Required status check \"ci / required\" is expected."}`}
	for name, tc := range map[string]struct {
		checks    []Check
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
		"GitHub still expects the check": {
			checks: []Check{at(1, 1, "ci / required", "completed", "success")}, mergeErrs: []error{expected},
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
			merged, err := e.mergeGreen(context.Background(), r, pr)
			if err != nil || merged != tc.merged || r.Hold != tc.hold || !strings.Contains(r.Detail, tc.detail) || r.Phase != Merging {
				t.Fatalf("merged %v, %v; %s %s %q", merged, err, r.Phase, r.Hold, r.Detail)
			}
			if tc.merged || tc.hold != NoHold {
				return
			}
			// Still waiting: the next reconcile merges once the running run passed.
			for i, c := range repo.checks["head7"] {
				if c.Status != "completed" {
					repo.checks["head7"][i].Status, repo.checks["head7"][i].Conclusion = "completed", "success"
				}
			}
			if merged, err := e.mergeGreen(context.Background(), r, pr); err != nil || !merged || r.Hold != NoHold {
				t.Errorf("next reconcile: merged %v, %v; %s %q", merged, err, r.Hold, r.Detail)
			}
		})
	}
}
