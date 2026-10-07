package train

import "testing"

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
