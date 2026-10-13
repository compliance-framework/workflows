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
