package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestRunList(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"list", "--manifest", "../../repos.mock.yaml"}, &out, io.Discard); err != nil {
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
	// Batches 1 and 2 are active: the core repos, then batch 1's 30 repos and batch 2's 27.
	want = "api,gooci,agent,ui,agent-action,helm-charts,workflows," +
		"plugin-file-attestation,plugin-file-attestation-policies,plugin-file-attestation-ctrf-policies,plugin-file-attestation-sarif-policies," +
		"plugin-github-settings,plugin-github-settings-policies,plugin-github-repositories,plugin-github-repositories-policies," +
		"plugin-github-repositories-custom-policies,plugin-github-repositories-dependency-policies,plugin-github-repositories-pr-policies," +
		"plugin-dependabot,plugin-dependabot-policies,plugin-dependabot-granular-policies,plugin-apt-versions,plugin-apt-versions-policies," +
		"plugin-local-ssh,plugin-local-ssh-policies,plugin-jira,plugin-jira-policies,plugin-jira-custom-policies," +
		"plugin-gitlab-repositories,plugin-gitlab-repositories-policies,plugin-k8s,plugin-k8s-opres-policies," +
		"plugin-cloud-custodian,plugin-cloud-custodian-policies,plugin-template,plugin-template-policies,policy-template," +
		"plugin-aws-rds-aurora-psql,plugin-aws-rds-aurora-psql-policies,plugin-http-collector,plugin-http-collector-policies," +
		"plugin-intruder,plugin-intruder-policies,plugin-aws-acm,plugin-aws-acm-certificate-policies," +
		"plugin-aws-ecr,plugin-aws-ecr-image-policies,plugin-aws-ecr-registry-policies,plugin-aws-ecr-repository-policies," +
		"plugin-aws-ec2,plugin-aws-ec2-policies,plugin-aws-eks,plugin-aws-eks-policies,plugin-aws-eks-addon-policies,plugin-aws-eks-nodegroup-policies," +
		"plugin-aws-elb,plugin-aws-elb-listener-policies,plugin-aws-elb-loadbalancer-policies,plugin-aws-elb-targetgroup-policies," +
		"plugin-aws-elb-targethealth-policies,plugin-aws-s3,plugin-aws-s3-policies,plugin-aws-secretsmanager,plugin-aws-secretsmanager-policies\n"
	if got := out.String(); got != want {
		t.Errorf("list repos.yaml = %q, want %q", got, want)
	}
}

func TestRunConfig(t *testing.T) {
	var out bytes.Buffer
	args := []string{"config", "--manifest", "../../repos.mock.yaml", "--repos", "mock-ui,mock-api", "--preset", "../../renovate/default.json"}
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
		{[]string{"list", "--manifest", "../../repos.mock.yaml", "--dry-run", "of"}, "dry run"},
		{[]string{"list", "--manifest", "../../repos.mock.yaml", "--dry-run", "off", "--max-repos", "-1"}, "--max-repos -1"},
		// A live run never takes a repo of an inactive batch (repos.yaml has batch 3 inactive).
		{[]string{"list", "--manifest", "../../repos.yaml", "--repos", "plugin-aws-vpc", "--dry-run", "off"}, "not active"},
		{[]string{"config", "--manifest", "../../repos.yaml", "--repos", "api,plugin-aws-vpc", "--dry-run", "off", "--preset", "../../renovate/default.json"}, "not active"},
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
	names := "plugin-aws-vpc,plugin-aws-vpc-policies" // batch 3
	var list, cfgOut bytes.Buffer
	if err := run([]string{"list", "--manifest", "../../repos.yaml", "--repos", names, "--dry-run", "full"}, &list, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := list.String(); got != names+"\n" {
		t.Errorf("list = %q, want %q", got, names+"\n")
	}
	args := []string{"config", "--manifest", "../../repos.yaml", "--repos", names, "--dry-run", "lookup", "--preset", "../../renovate/default.json"}
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
	if !slices.Equal(cfg.Repositories, []string{"compliance-framework/plugin-aws-vpc", "compliance-framework/plugin-aws-vpc-policies"}) || cfg.DryRun != "lookup" {
		t.Errorf("config: repositories %v, dryRun %q", cfg.Repositories, cfg.DryRun)
	}
}

// TestRunLiveShard: a live run takes one shard of at most --max-repos repos, picked by --slot, and list and config
// agree on it; a dry run takes every selected repo.
func TestRunLiveShard(t *testing.T) {
	base := []string{"--manifest", "../../repos.mock.yaml", "--max-repos", "4"}
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
