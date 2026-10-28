package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
)

func TestRunList(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"list", "--manifest", "../../internal/manifest/testdata/mock.yaml"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := "mock-api,mock-gooci,mock-agent,mock-ui,mock-agent-action,mock-plugin-1,mock-plugin-2,mock-plugin-policies-1,mock-plugin-policies-2,mock-helm-charts,workflows\n"
	if got := out.String(); got != want {
		t.Errorf("list = %q, want %q", got, want)
	}

	out.Reset()
	if err := run([]string{"list", "--manifest", "../../repos.yaml"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	// Every batch of repos.yaml is active: every repo, in manifest order (the core repos first).
	m, err := manifest.Load("../../repos.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ActiveRepos()) != len(m.Repos) {
		t.Fatalf("active_batches %v: want every batch of repos.yaml active", m.ActiveBatches)
	}
	var all []string
	for _, r := range m.Repos {
		all = append(all, r.Name)
	}
	if len(all) != 74 || !slices.Equal(all[:7], []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "workflows"}) {
		t.Fatalf("repos.yaml: %d repos %v; want 74, the 7 core repos first", len(all), all)
	}
	want = strings.Join(all, ",") + "\n"
	if got := out.String(); got != want {
		t.Errorf("list repos.yaml = %q, want %q", got, want)
	}
}

func TestRunConfig(t *testing.T) {
	var out bytes.Buffer
	args := []string{"config", "--manifest", "../../internal/manifest/testdata/mock.yaml", "--repos", "mock-ui,mock-api", "--preset", "../../renovate/default.json"}
	if err := run(args, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Repositories []string `json:"repositories"`
		DryRun       string   `json:"dryRun"`
		Onboarding   *bool    `json:"onboarding"`
	}
	if err := json.Unmarshal(out.Bytes(), &cfg); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if !slices.Equal(cfg.Repositories, []string{"compliance-framework/mock-api", "compliance-framework/mock-ui"}) {
		t.Errorf("repositories = %v", cfg.Repositories)
	}
	if cfg.DryRun != "full" {
		t.Errorf("dryRun = %q, want the default full", cfg.DryRun)
	}
	if cfg.Onboarding == nil || *cfg.Onboarding {
		t.Errorf("onboarding = %v, want false", cfg.Onboarding)
	}
}

func TestRunConfigOverrides(t *testing.T) {
	dir := t.TempDir()
	base := []string{"config", "--manifest", "../../internal/manifest/testdata/mock.yaml", "--repos", "mock-ui", "--preset", "../../renovate/default.json"}
	rules := func(t *testing.T, args ...string) []map[string]any {
		t.Helper()
		var out bytes.Buffer
		if err := run(append(slices.Clone(base), args...), &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			PackageRules []map[string]any `json:"packageRules"`
		}
		if err := json.Unmarshal(out.Bytes(), &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg.PackageRules
	}
	preset := rules(t)
	// A missing file (no image-scan artifact) adds nothing.
	if got := rules(t, "--overrides", filepath.Join(dir, "missing.json")); len(got) != len(preset) {
		t.Errorf("missing overrides: %d rules, want the preset's %d", len(got), len(preset))
	}
	file := filepath.Join(dir, "overrides.json")
	write := func(s string) {
		if err := os.WriteFile(file, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`[{"matchRepositories": ["compliance-framework/mock-ui"], "matchManagers": ["dockerfile"], "matchDepNames": ["nginx"], "automerge": true}]`)
	got := rules(t, "--overrides", file)
	if len(got) != len(preset)+1 || got[len(got)-1]["matchDepNames"] == nil {
		t.Errorf("overrides: %d rules, want the preset's %d then the override last", len(got), len(preset))
	}
	write(`[{"matchRepositories": ["compliance-framework/mock-api"], "matchManagers": ["dockerfile"], "matchDepNames": ["nginx"]}]`)
	if err := run(append(slices.Clone(base), "--overrides", file), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "not in this run") {
		t.Errorf("override for a repo outside the run: err = %v", err)
	}
}

// batchesYAML is the fixture with plugin and policy repos in three batches, none active.
const batchesYAML = "../../internal/manifest/testdata/batches.yaml"

func TestRunErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{nil, "usage"},
		{[]string{"sync"}, "usage"},
		{[]string{"list", "extra"}, "unexpected arguments"},
		{[]string{"list", "--manifest", "missing.yaml"}, "read manifest"},
		{[]string{"list", "--manifest", "../../internal/manifest/testdata/mock.yaml", "--repos", "api"}, "not in the manifest: api"},
		{[]string{"config", "--manifest", "../../internal/manifest/testdata/mock.yaml", "--preset", "missing.json"}, "read preset"},
		{[]string{"config", "--manifest", "../../internal/manifest/testdata/mock.yaml", "--preset", "../../renovate/default.json", "--dry-run", "true"}, "dry run"},
		{[]string{"list", "--manifest", "../../internal/manifest/testdata/mock.yaml", "--dry-run", "of"}, "dry run"},
		{[]string{"list", "--manifest", "../../internal/manifest/testdata/mock.yaml", "--dry-run", "off", "--max-repos", "-1"}, "--max-repos -1"},
		// A live run never takes a repo of an inactive batch (batches.yaml has none active; every batch of
		// repos.yaml is).
		{[]string{"list", "--manifest", batchesYAML, "--repos", "plugin-two", "--dry-run", "off"}, "not active"},
		{[]string{"config", "--manifest", batchesYAML, "--repos", "api,plugin-two", "--dry-run", "off", "--preset", "../../renovate/default.json"}, "not active"},
	} {
		var out bytes.Buffer
		if err := run(tc.args, &out, io.Discard); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("run(%v) err = %v, want one containing %q", tc.args, err, tc.err)
		}
	}
}

// TestRunDryRunInactive: a dry run may name repos of an inactive batch, to check the batch before it is switched
// on; the token's list and the config name the same repos.
func TestRunDryRunInactive(t *testing.T) {
	names := "plugin-two,plugin-three-policies" // batches 2 and 3 of batches.yaml, none active
	var list, cfgOut bytes.Buffer
	if err := run([]string{"list", "--manifest", batchesYAML, "--repos", names, "--dry-run", "full"}, &list, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := list.String(); got != names+"\n" {
		t.Errorf("list = %q, want %q", got, names+"\n")
	}
	args := []string{"config", "--manifest", batchesYAML, "--repos", names, "--dry-run", "lookup", "--preset", "../../renovate/default.json"}
	if err := run(args, &cfgOut, io.Discard); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Repositories []string `json:"repositories"`
		DryRun       string   `json:"dryRun"`
	}
	if err := json.Unmarshal(cfgOut.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Repositories, []string{"compliance-framework/plugin-two", "compliance-framework/plugin-three-policies"}) || cfg.DryRun != "lookup" {
		t.Errorf("config: repositories %v, dryRun %q", cfg.Repositories, cfg.DryRun)
	}
}

// TestRunLiveShard: a live run takes one shard of at most --max-repos repos, picked by --slot, and list and config
// agree on it; a dry run takes every selected repo.
func TestRunLiveShard(t *testing.T) {
	base := []string{"--manifest", "../../internal/manifest/testdata/mock.yaml", "--max-repos", "4"}
	// 11 mock repos, at most 4 per run: 3 shards, repo i in shard i mod 3.
	want := map[int64]string{
		0: "mock-api,mock-ui,mock-plugin-2,mock-helm-charts",
		1: "mock-gooci,mock-agent-action,mock-plugin-policies-1,workflows",
		5: "mock-agent,mock-plugin-1,mock-plugin-policies-2",
	}
	for slot, w := range want {
		var list, cfgOut, msg bytes.Buffer
		args := append([]string{"list", "--dry-run", "off", "--slot", fmt.Sprint(slot)}, base...)
		if err := run(args, &list, &msg); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(list.String()); got != w {
			t.Errorf("slot %d: list = %q, want %q", slot, got, w)
		}
		if !strings.Contains(msg.String(), "live run: shard") {
			t.Errorf("slot %d: stderr = %q, want the shard named", slot, msg.String())
		}
		args = append([]string{"config", "--dry-run", "off", "--slot", fmt.Sprint(slot), "--preset", "../../renovate/default.json"}, base...)
		if err := run(args, &cfgOut, io.Discard); err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Repositories []string `json:"repositories"`
		}
		if err := json.Unmarshal(cfgOut.Bytes(), &cfg); err != nil {
			t.Fatal(err)
		}
		if got := strings.ReplaceAll(strings.Join(cfg.Repositories, ","), "compliance-framework/", ""); got != w {
			t.Errorf("slot %d: config repositories = %q, want %q", slot, got, w)
		}
	}

	var list bytes.Buffer
	if err := run(append([]string{"list", "--dry-run", "full", "--slot", "1"}, base...), &list, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Split(strings.TrimSpace(list.String()), ",")); n != 11 {
		t.Errorf("dry run: %d repos, want all 11", n)
	}
}
