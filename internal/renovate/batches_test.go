package renovate

import (
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// TestSelectReposByBatch: the Renovate repo list is today's (every core repo, workflows too) until a batch is active;
// then only that batch's repos join.
func TestSelectReposByBatch(t *testing.T) {
	for _, tc := range []struct {
		active []int
		list   string
		want   []string
		err    string
	}{
		{nil, "", []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "workflows"}, ""},
		{[]int{1}, "", []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "workflows", "plugin-one", "plugin-one-policies", "plugin-template"}, ""},
		{[]int{1, 2}, "plugin-two", []string{"plugin-two"}, ""},
		{nil, "plugin-one", nil, "not active"},
		{[]int{1}, "plugin-two", nil, "not active"},
	} {
		m, err := manifest.Load("../../internal/manifest/testdata/batches.yaml")
		if err != nil {
			t.Fatal(err)
		}
		m.ActiveBatches = tc.active
		got, err := SelectRepos(m, tc.list)
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

// TestSelectReposByBatchReposYAML: repos.yaml lists every plugin and policy repo with no batch active, and the
// selection is still today's.
func TestSelectReposByBatchReposYAML(t *testing.T) {
	m, err := manifest.Load("../../repos.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Repos) == len(m.ActiveRepos()) {
		t.Fatal("repos.yaml has no inactive repos to leave out")
	}
	if got, err := SelectRepos(m, ""); err != nil || !slices.Equal(got, []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "workflows"}) {
		t.Errorf("repos.yaml: got %v, %v; want today's selection", got, err)
	}
}
