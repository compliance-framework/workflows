package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// TestSelectReposByBatch: the digest reads today's repos (every core repo) until a batch is active; then
// only that batch's repos join.
func TestSelectReposByBatch(t *testing.T) {
	for _, tc := range []struct {
		active []int
		list   string
		want   []string
		err    string
	}{
		{nil, "", []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "workflows"}, ""},
		{[]int{1}, "", []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "workflows", "plugin-one", "plugin-one-policies", "plugin-template"}, ""},
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

// TestSelectReposByBatchReposYAML: every batch of repos.yaml is active (1, 2 and 3), so the selection is
// the core repos plus every batch's repos (30, 27 and 10), in manifest order. The inactive-batch paths
// are covered on testdata/batches.yaml above.
func TestSelectReposByBatchReposYAML(t *testing.T) {
	m, err := manifest.Load("../../repos.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ActiveRepos()) != len(m.Repos) {
		t.Fatalf("active_batches %v: want every batch of repos.yaml active", m.ActiveBatches)
	}
	want := []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "workflows", "factory"}
	for _, r := range m.Repos {
		if r.Batch != 0 {
			want = append(want, r.Name)
		}
	}
	if got, err := selectRepos(m, ""); err != nil || len(got) != 75 || !slices.Equal(got, want) {
		t.Errorf("repos.yaml: got %v, %v; want %v", got, err, want)
	}
}
