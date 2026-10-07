package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/pluginprobe"
)

func TestSplitList(t *testing.T) {
	got := splitList(" a,b\n c\t,,d ")
	if want := []string{"a", "b", "c", "d"}; !slices.Equal(got, want) {
		t.Errorf("splitList = %q, want %q", got, want)
	}
	if got := splitList(" \n"); len(got) != 0 {
		t.Errorf("splitList(blank) = %q", got)
	}
}

func TestPluginEnv(t *testing.T) {
	env := map[string]string{"PATH": "/bin", "HOME": "/home/x", "GH_TOKEN": "secret"}
	got := pluginEnv(func(k string) string { return env[k] })
	if want := []string{"PATH=/bin", "HOME=/home/x"}; !slices.Equal(got, want) {
		t.Errorf("pluginEnv = %q, want %q", got, want)
	}
}

func TestVersionsFromInfo(t *testing.T) {
	agent, opa := versionsFromInfo(&debug.BuildInfo{Deps: []*debug.Module{
		{Path: pluginprobe.AgentModule, Version: "v0.9.0"},
		{Path: pluginprobe.OPAModule, Version: "v1.14.1"},
	}})
	if agent != "v0.9.0" || opa != "v1.14.1" {
		t.Errorf("versionsFromInfo = %q, %q", agent, opa)
	}
}

func TestRunErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{nil, "nothing to probe"},
		{[]string{"--plugins", " , "}, "nothing to probe"},
		{[]string{"--plugins", "x", "extra"}, "unexpected arguments"},
		{[]string{"--plugins", "x", "--json", "-"}, "both write to stdout"},
		{[]string{"--bogus"}, "flag provided but not defined"},
		{[]string{"--plugins", "x", "--log-level", "loud"}, "--log-level"},
	} {
		var out, errOut bytes.Buffer
		if err := run(context.Background(), tc.args, &out, &errOut); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("run(%q) = %v, want an error containing %q", tc.args, err, tc.err)
		}
	}
}

func TestRunReports(t *testing.T) {
	good, bad := t.TempDir(), t.TempDir()
	rego := "package ccf.mock\n\nviolation contains msg if {\n\tnot input.enabled\n\tmsg := \"disabled\"\n}\n"
	for dir, content := range map[string]string{good: rego, bad: "package x\n\nallow if undefined_fn(1)\n"} {
		if err := os.MkdirAll(filepath.Join(dir, "policies"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "policies", "a.rego"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	jsonPath := filepath.Join(t.TempDir(), "report.json")

	var out, errOut bytes.Buffer
	if err := run(context.Background(), []string{"--policies", good, "--json", jsonPath}, &out, &errOut); err != nil {
		t.Fatalf("run = %v\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "| `"+good+"` | 1 | ok |") {
		t.Errorf("markdown:\n%s", out.String())
	}
	var report pluginprobe.Report
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &report); err != nil || len(report.Policies) != 1 || report.Policies[0].Modules != 1 {
		t.Errorf("JSON report = %s (%v)", data, err)
	}

	// A failure still writes both reports, then fails the run.
	out.Reset()
	err = run(context.Background(), []string{"--policies", good + "," + bad, "--plugins", "no/such/plugin", "--json", "-", "--markdown", ""}, &out, &errOut)
	if !errors.Is(err, errFailed) {
		t.Fatalf("run = %v, want errFailed", err)
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || len(report.Plugins) != 1 || report.Plugins[0].Error == "" ||
		len(report.Policies) != 2 || report.Policies[1].Error == "" {
		t.Errorf("JSON report = %s (%v)", out.String(), err)
	}
}
