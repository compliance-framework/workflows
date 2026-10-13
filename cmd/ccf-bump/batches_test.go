package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// TestSelectReposByBatch: ccf-bump (list, sync, merge) acts on today's repos (the core repos with release:
// true) until a batch is active; then that batch's released repos join in stage 3, before helm-charts. A live run
// rejects a named repo of an inactive batch; a dry run may name one (to check a batch before switching it on),
// still in stage order.
func TestSelectReposByBatch(t *testing.T) {
	for _, tc := range []struct {
		active []int
		list   string
		dryRun bool
		want   []string
		err    string
	}{
		{nil, "", false, []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts"}, ""},
		{[]int{1}, "", false, []string{"api", "gooci", "agent", "ui", "agent-action", "plugin-one", "plugin-one-policies", "helm-charts"}, ""},
		{nil, "plugin-one", false, nil, "not active"},
		{[]int{1}, "plugin-two", false, nil, "not active"},
		{[]int{1}, "plugin-template", false, nil, "with release: true"},
		// Dry runs.
		{nil, "", true, []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts"}, ""},
		{nil, "plugin-one-policies,plugin-one", true, []string{"plugin-one", "plugin-one-policies"}, ""},
		{nil, "helm-charts,plugin-three-policies,plugin-two,agent,plugin-one", true, []string{"agent", "plugin-one", "plugin-three-policies", "plugin-two", "helm-charts"}, ""},
		{[]int{1}, "plugin-two,plugin-one", true, []string{"plugin-one", "plugin-two"}, ""},
		{nil, "plugin-template", true, nil, "with release: true"},
		{nil, "plugin-nine", true, nil, "with release: true"},
	} {
		m, err := manifest.Load("../../internal/manifest/testdata/batches.yaml")
		if err != nil {
			t.Fatal(err)
		}
		m.ActiveBatches = tc.active
		got, err := selectRepos(m, tc.list, tc.dryRun)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("active_batches %v, %q, dry run %v: err = %v, want one containing %q", tc.active, tc.list, tc.dryRun, err, tc.err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("active_batches %v, %q, dry run %v: got %v, %v; want %v", tc.active, tc.list, tc.dryRun, got, err, tc.want)
		}
		if !slices.Equal(m.ActiveBatches, tc.active) {
			t.Errorf("active_batches %v changed to %v", tc.active, m.ActiveBatches)
		}
	}
}

// TestRunDryRunInactive: list, sync and merge take a repo of an inactive batch only with --dry-run.
func TestRunDryRunInactive(t *testing.T) {
	m := "../../internal/manifest/testdata/batches.yaml"
	var out bytes.Buffer
	e := env{getenv: func(string) string { return "" }, stdout: &out}
	if err := run(context.Background(), []string{"list", "--manifest", m, "--repos", "plugin-two,plugin-one", "--dry-run"}, e); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "plugin-one,plugin-two\n" {
		t.Errorf("list --dry-run = %q", got)
	}
	for _, args := range [][]string{
		{"list", "--repos", "plugin-one"},
		{"list", "--repos", "plugin-one", "--dry-run=false"},
		{"sync", "--repos", "api,plugin-one", "--pr"},
		{"merge", "--repos", "plugin-one"},
		{"--repo", "plugin-one", "--pr"},
	} {
		if err := run(context.Background(), append(args, "--manifest", m), e); err == nil || !strings.Contains(err.Error(), "not active") {
			t.Errorf("%v: err = %v, want not active", args, err)
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
	if got, err := selectRepos(m, "", false); err != nil || !slices.Equal(got, []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts"}) {
		t.Errorf("repos.yaml: got %v, %v; want today's selection", got, err)
	}
}
