package renovate

import (
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// TestSelectReposByBatch: the Renovate repo list is today's (every core repo, workflows too) until a batch is active;
// then only that batch's repos join. A live run rejects a named repo of an inactive batch; a dry run (any mode but
// off) may name one, so a batch can be checked before it is switched on.
func TestSelectReposByBatch(t *testing.T) {
	for _, tc := range []struct {
		active []int
		list   string
		dryRun string
		want   []string
		err    string
	}{
		{nil, "", DryRunOff, []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "workflows"}, ""},
		{[]int{1}, "", DryRunOff, []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "workflows", "plugin-one", "plugin-one-policies", "plugin-template"}, ""},
		{[]int{1, 2}, "plugin-two", DryRunOff, []string{"plugin-two"}, ""},
		{nil, "plugin-one", DryRunOff, nil, "not active"},
		{[]int{1}, "plugin-two", DryRunOff, nil, "not active"},
		// Dry runs: inactive repos only when named; the default selection stays the active repos.
		{nil, "", "full", []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "workflows"}, ""},
		{nil, "plugin-template,plugin-one,plugin-one-policies", "full", []string{"plugin-one", "plugin-one-policies", "plugin-template"}, ""},
		{[]int{1}, "plugin-two, api", "lookup", []string{"api", "plugin-two"}, ""},
		{nil, "plugin-three-policies", "extract", []string{"plugin-three-policies"}, ""},
		{nil, "plugin-one,plugin-nine", "full", nil, "not in the manifest: plugin-nine"},
		{nil, "plugin-one", "yes", nil, "dry run"},
		{nil, "plugin-one", "", nil, "dry run"},
	} {
		m, err := manifest.Load("../../internal/manifest/testdata/batches.yaml")
		if err != nil {
			t.Fatal(err)
		}
		m.ActiveBatches = tc.active
		got, err := SelectRepos(m, tc.list, tc.dryRun)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("active_batches %v, %q, dry run %q: err = %v, want one containing %q", tc.active, tc.list, tc.dryRun, err, tc.err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("active_batches %v, %q, dry run %q: got %v, %v; want %v", tc.active, tc.list, tc.dryRun, got, err, tc.want)
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
	if got, err := SelectRepos(m, "", DryRunOff); err != nil || !slices.Equal(got, []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "workflows"}) {
		t.Errorf("repos.yaml: got %v, %v; want today's selection", got, err)
	}
}
