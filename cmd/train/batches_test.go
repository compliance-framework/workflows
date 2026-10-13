package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// TestTrainReposByBatch: a train acts on today's repos (the core repos with release: true) until a batch is
// active; then that batch's released repos join.
func TestTrainReposByBatch(t *testing.T) {
	for _, tc := range []struct {
		active []int
		list   string
		want   []string
		err    string
	}{
		{nil, "", []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts"}, ""},
		{[]int{1}, "", []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "plugin-one", "plugin-one-policies"}, ""},
		{nil, "plugin-one", nil, "batch 1, which is not active in batches.yaml"},
		{[]int{1}, "plugin-two", nil, "batch 2, which is not active"},
		{[]int{1}, "plugin-template", nil, "not in batches.yaml with release: true"},
	} {
		m, err := manifest.Load("../../internal/manifest/testdata/batches.yaml")
		if err != nil {
			t.Fatal(err)
		}
		m.ActiveBatches = tc.active
		got, err := trainRepos(m, "batches.yaml", strings.Fields(tc.list))
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

// TestTrainReposByBatchReposYAML: every batch of repos.yaml is active (1, 2 and 3), so the selection is the released
// core repos plus every batch's released repos (27, 27 and 10), in manifest order. The inactive-batch paths are
// covered on testdata/batches.yaml above.
func TestTrainReposByBatchReposYAML(t *testing.T) {
	m, err := manifest.Load("../../repos.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ActiveRepos()) != len(m.Repos) {
		t.Fatalf("active_batches %v: want every batch of repos.yaml active", m.ActiveBatches)
	}
	want := []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts"}
	for _, r := range m.Repos {
		if r.Batch != 0 && r.Release {
			want = append(want, r.Name)
		}
	}
	if got, err := trainRepos(m, "repos.yaml", nil); err != nil || len(got) != 70 || !slices.Equal(got, want) {
		t.Errorf("repos.yaml: got %v, %v; want %v", got, err, want)
	}
}
