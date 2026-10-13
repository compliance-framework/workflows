package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// TestSelectReposByBatch: the summary reads today's repos (every core repo but workflows) until a batch is
// active; then only that batch's repos join.
func TestSelectReposByBatch(t *testing.T) {
	for _, tc := range []struct {
		active []int
		list   string
		want   []string
		err    string
	}{
		{nil, "", []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts"}, ""},
		{[]int{1}, "", []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "plugin-one", "plugin-one-policies", "plugin-template"}, ""},
		{nil, "plugin-one", nil, "not active"},
		{[]int{1}, "plugin-two", nil, "not active"},
	} {
		m, err := manifest.Load("../../internal/manifest/testdata/batches.yaml")
		if err != nil {
			t.Fatal(err)
		}
		m.ActiveBatches = tc.active
		got, err := selectRepos(m, tc.list)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("active_batches %v, %q: err = %v, want one containing %q", tc.active, tc.list, err, tc.err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("active_batches %v, %q: got %v, %v; want %v", tc.active, tc.list, got, err, tc.want)
		}
	}
}

// TestSelectReposByBatchReposYAML: repos.yaml has batches 1 and 2 active, so the selection is the core repos but
// workflows, plus batch 1's 30 repos and batch 2's 27, in manifest order; batch 3 stays out.
func TestSelectReposByBatchReposYAML(t *testing.T) {
	m, err := manifest.Load("../../repos.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.ActiveBatches, []int{1, 2}) || len(m.Repos) == len(m.ActiveRepos()) {
		t.Fatalf("active_batches %v: want [1 2], with batch 3 inactive", m.ActiveBatches)
	}
	want := []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts"}
	for _, r := range m.Repos {
		if r.Batch == 1 || r.Batch == 2 {
			want = append(want, r.Name)
		}
	}
	if got, err := selectRepos(m, ""); err != nil || len(got) != 63 || !slices.Equal(got, want) {
		t.Errorf("repos.yaml: got %v, %v; want %v", got, err, want)
	}
}
