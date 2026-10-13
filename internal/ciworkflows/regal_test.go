package ciworkflows_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type regalConfig struct {
	Rules map[string]map[string]map[string]any `yaml:"rules"`
}

func readRegalConfig(t *testing.T, path string) regalConfig {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c regalConfig
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSharedRegalConfig pins the rule the shared config turns off for policy behaviour reasons.
func TestSharedRegalConfig(t *testing.T) {
	c := readRegalConfig(t, filepath.Join("..", "..", ".regal", "config.yaml"))
	if got := c.Rules["idiomatic"]["prefer-string-interpolation"]["level"]; got != "ignore" {
		t.Errorf("idiomatic/prefer-string-interpolation level %v, want ignore", got)
	}
}

// TestRegalDataPaths runs the regal lint step with a fake regal: the shared config Regal gets
// lists the top-level keys of the bundle's data files as unresolved-reference except-paths.
func TestRegalDataPaths(t *testing.T) {
	need(t, "yq", "jq")
	src := script(t, policies, "regal", "regal lint")
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "regal"), "#!/bin/sh\nprintf '%s\\n' \"$@\"\n")
	if err := os.Chmod(filepath.Join(bin, "regal"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
	shared, err := os.ReadFile(filepath.Join("..", "..", ".regal", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		dir   string
		files map[string]string
		want  []string
	}{
		{
			name: "data files",
			dir:  "policies",
			files: map[string]string{
				// Non-identifier keys are cut at their first [ by Regal, so they're left out.
				"policies/data.json":           `{"wget_version": "1.21", "env_names": ["prod"], "my-key": 1, "nested": {"a": 1}}`,
				"policies/teams/data.yaml":     "members: [a]\n",
				"policies/teams/sub/data.yml":  "limits:\n  max: 1\n",
				"policies/bad-dir/data.json":   `{"skipped": true}`,
				"policies/list/data.json":      `["not", "a", "map"]`,
				"policies/teams/policy.rego":   "package compliance_framework.teams\n",
				"policies/other/notdata.json":  `{"ignored": true}`,
				"policies/teams/data.json.bak": `{"ignored": true}`,
			},
			want: []string{
				"data.env_names", "data.env_names.**",
				"data.nested", "data.nested.**",
				"data.teams.members", "data.teams.members.**",
				"data.teams.sub.limits", "data.teams.sub.limits.**",
				"data.wget_version", "data.wget_version.**",
			},
		},
		{
			name:  "trailing slash in directory",
			dir:   "policies/",
			files: map[string]string{"policies/data.json": `{"a": 1}`, "policies/x/data.json": `{"b": 1}`},
			want:  []string{"data.a", "data.a.**", "data.x.b", "data.x.b.**"},
		},
		{
			name:  "no data files",
			dir:   "policies",
			files: map[string]string{"policies/p.rego": "package p\n"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tc.files {
				writeFile(t, filepath.Join(dir, name), content)
			}
			writeFile(t, filepath.Join(dir, ".ccf-workflows", ".regal", "config.yaml"), string(shared))
			r := run(t, dir, src, path, "SHARED=true", "POLICY_DIR="+tc.dir)
			cfg := filepath.Join(r.temp, "ccf-regal.yaml")
			if wantArgs := []string{"lint", "--config-file", cfg, "--format", "github", tc.dir}; r.failed || !slices.Equal(lastLines(r.out, len(wantArgs)), wantArgs) {
				t.Fatalf("failed=%v; want regal %v; output:\n%s", r.failed, wantArgs, r.out)
			}
			if _, err := os.Stat(filepath.Join(dir, ".ccf-workflows")); !os.IsNotExist(err) {
				t.Fatalf("clone not removed: %v", err)
			}
			c := readRegalConfig(t, cfg)
			var got []string
			if ref, ok := c.Rules["imports"]["unresolved-reference"]; ok {
				paths, ok := ref["except-paths"].([]any)
				if !ok {
					t.Fatalf("unresolved-reference except-paths %#v, want a list", ref["except-paths"])
				}
				for _, p := range paths {
					s, ok := p.(string)
					if !ok {
						t.Fatalf("except-paths entry %#v, want a string", p)
					}
					got = append(got, s)
				}
				if len(ref) != 1 {
					t.Errorf("unresolved-reference config %v, want only except-paths (its level stays the default)", ref)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("except-paths %v, want %v", got, tc.want)
			}
			if got := c.Rules["idiomatic"]["prefer-string-interpolation"]["level"]; got != "ignore" {
				t.Errorf("the generated config lost the shared rules: prefer-string-interpolation level %v", got)
			}
		})
	}

	t.Run("repo config", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "policies", "data.json"), `{"a": 1}`)
		r := run(t, dir, src, path, "SHARED=false", "POLICY_DIR=policies")
		if want := []string{"lint", "--format", "github", "policies"}; r.failed || !slices.Equal(strings.Fields(r.out), want) {
			t.Fatalf("failed=%v; want regal %v; output:\n%s", r.failed, want, r.out)
		}
	})

	t.Run("invalid data file", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "policies", "data.json"), `{"a": `)
		writeFile(t, filepath.Join(dir, ".ccf-workflows", ".regal", "config.yaml"), string(shared))
		if r := run(t, dir, src, path, "SHARED=true", "POLICY_DIR=policies"); !r.failed || strings.Contains(r.out, "--config-file") {
			t.Fatalf("want a failure before regal runs, got failed=%v:\n%s", r.failed, r.out)
		}
	})
}

// lastLines returns the last n non-empty lines of out.
func lastLines(out string, n int) []string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < n {
		return lines
	}
	return lines[len(lines)-n:]
}
