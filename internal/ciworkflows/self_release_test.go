package ciworkflows_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
)

// TestSelfReleaseConfig pins this repo's own release-please setup: the shared defaults, a simple
// package whose first release is 1.0.0 (the manifest is 0.0.0 until then), and no floating tag job.
func TestSelfReleaseConfig(t *testing.T) {
	need(t, "jq")
	root := filepath.Join("..", "..")
	var config struct {
		Packages map[string]map[string]any `json:"packages"`
	}
	var manifest map[string]string
	for file, v := range map[string]any{"release-please-config.json": &config, ".release-please-manifest.json": &manifest} {
		b, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	// The manifest holds only the "." package. Before the first release its version is 0.0.0,
	// "no release" to release-please, so the first release PR proposes initial-version (1.0.0);
	// release-please then writes each released version, which is never below 1.0.0.
	if v, ok := manifest["."]; len(manifest) != 1 || !ok {
		t.Errorf(".release-please-manifest.json = %v, want only the \".\" package", manifest)
	} else if sv := "v" + v; !semver.IsValid(sv) || semver.Canonical(sv) != sv {
		t.Errorf(`.release-please-manifest.json["."] = %q, want a full semver version`, v)
	} else if v != "0.0.0" && semver.Compare(sv, "v1.0.0") < 0 {
		t.Errorf(`.release-please-manifest.json["."] = %q, want 0.0.0 (unreleased) or >= 1.0.0`, v)
	}
	if want := map[string]any{"release-type": "simple", "initial-version": "1.0.0"}; !reflect.DeepEqual(config.Packages["."], want) {
		t.Errorf(`packages["."] = %v, want %v`, config.Packages["."], want)
	}

	// The drift check of release-please.yml finds the shared defaults.
	dir := t.TempDir()
	for src, dst := range map[string]string{"release-please-config.json": "release-please-config.json",
		"release-please/defaults.json": ".ccf-workflows/release-please/defaults.json"} {
		b, err := os.ReadFile(filepath.Join(root, src))
		if err == nil {
			err = os.MkdirAll(filepath.Dir(filepath.Join(dir, dst)), 0o755)
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, dst), b, 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if r := run(t, dir, script(t, "release-please.yml", "release-please", "Compare the config with the shared defaults")); r.failed ||
		!strings.Contains(r.out, "has the shared defaults") {
		t.Errorf("drift check: failed=%v\n%s", r.failed, r.out)
	}

	var wf struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			Uses string `yaml:"uses"`
		} `yaml:"jobs"`
	}
	read(t, "self-release.yml", &wf)
	if len(wf.On) != 1 || wf.On["push"] == nil || len(wf.Jobs) != 1 || wf.Jobs["release-please"].Uses != "./.github/workflows/release-please.yml" {
		t.Errorf("self-release.yml: want only release-please on push, got on %v, jobs %+v", wf.On, wf.Jobs)
	}
}

// TestNotifyToolsRef: notify-failure.yml builds cmd/notify at the commit it is called at unless
// workflows-ref is given, and fails rather than check out the default branch.
func TestNotifyToolsRef(t *testing.T) {
	var wf struct {
		On struct {
			Call struct {
				Inputs map[string]struct {
					Default any `yaml:"default"`
				} `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				With map[string]string `yaml:"with"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	read(t, "notify-failure.yml", &wf)
	if d := wf.On.Call.Inputs["workflows-ref"].Default; d != "" {
		t.Errorf("workflows-ref default = %v, want empty", d)
	}
	const ref = "${{ inputs.workflows-ref || job.workflow_sha }}"
	steps := map[string]map[string]string{}
	for _, s := range wf.Jobs["notify"].Steps {
		steps[s.Name] = map[string]string{"with.ref": s.With["ref"], "env.REF": s.Env["REF"]}
	}
	if got := steps["Check out cmd/notify"]["with.ref"]; got != ref {
		t.Errorf("checkout ref = %q, want %q", got, ref)
	}
	if got := steps["Check the tools ref"]["env.REF"]; got != ref {
		t.Errorf("check REF = %q, want %q", got, ref)
	}
	src := script(t, "notify-failure.yml", "notify", "Check the tools ref")
	if r := run(t, t.TempDir(), src, "REF="); !r.failed || !strings.Contains(r.out, "job.workflow_sha is empty") {
		t.Errorf("empty ref: failed=%v\n%s", r.failed, r.out)
	}
	if r := run(t, t.TempDir(), src, "REF=0123456789abcdef0123456789abcdef01234567"); r.failed {
		t.Errorf("sha ref failed:\n%s", r.out)
	}
}
