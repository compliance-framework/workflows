package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestRunList(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"list", "--manifest", "../../repos.mock.yaml"}, &out); err != nil {
		t.Fatal(err)
	}
	want := "mock-api,mock-gooci,mock-agent,mock-ui,mock-agent-action,mock-plugin-1,mock-plugin-2,mock-plugin-policies-1,mock-plugin-policies-2,mock-helm-charts\n"
	if got := out.String(); got != want {
		t.Errorf("list = %q, want %q", got, want)
	}

	out.Reset()
	if err := run([]string{"list", "--manifest", "../../repos.yaml"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "api,gooci,agent,ui,agent-action,helm-charts\n" {
		t.Errorf("list repos.yaml = %q", got)
	}
}

func TestRunConfig(t *testing.T) {
	var out bytes.Buffer
	args := []string{"config", "--manifest", "../../repos.mock.yaml", "--repos", "mock-ui,mock-api", "--preset", "../../renovate/default.json"}
	if err := run(args, &out); err != nil {
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

func TestRunErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{nil, "usage"},
		{[]string{"sync"}, "usage"},
		{[]string{"list", "extra"}, "unexpected arguments"},
		{[]string{"list", "--manifest", "missing.yaml"}, "read manifest"},
		{[]string{"list", "--manifest", "../../repos.mock.yaml", "--repos", "api"}, "not in the manifest: api"},
		{[]string{"config", "--manifest", "../../repos.mock.yaml", "--preset", "missing.json"}, "read preset"},
		{[]string{"config", "--manifest", "../../repos.mock.yaml", "--preset", "../../renovate/default.json", "--dry-run", "true"}, "dry run"},
	} {
		var out bytes.Buffer
		if err := run(tc.args, &out); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("run(%v) err = %v, want one containing %q", tc.args, err, tc.err)
		}
	}
}
