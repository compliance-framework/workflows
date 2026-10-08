package ciworkflows_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestAppKeysOnlyInEnvironments: every job that reads an app private key names the environment
// that holds it (repo-settings limits each to the default branch, and release tags for release),
// so a workflow on another branch never gets the key from an org or repo secret by mistake.
func TestAppKeysOnlyInEnvironments(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"RELEASE_BOT_PRIVATE_KEY": "release", "REPO_ADMIN_PRIVATE_KEY": "repo-admin"}
	found := 0
	for _, file := range files {
		var wf struct {
			Jobs map[string]yaml.Node `yaml:"jobs"`
		}
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(b, &wf); err != nil {
			t.Fatal(err)
		}
		for name, node := range wf.Jobs {
			var job struct {
				Environment string `yaml:"environment"`
				Uses        string `yaml:"uses"`
				Steps       []struct {
					Uses string            `yaml:"uses"`
					With map[string]string `yaml:"with"`
				} `yaml:"steps"`
			}
			if err := node.Decode(&job); err != nil {
				t.Fatalf("%s: job %s: %v", file, name, err)
			}
			for _, s := range job.Steps {
				if !strings.HasPrefix(s.Uses, "actions/create-github-app-token@") {
					continue
				}
				for secret, env := range want {
					if strings.Contains(s.With["private-key"], "secrets."+secret) {
						found++
						if job.Environment != env {
							t.Errorf("%s: job %s reads %s outside the %s environment (environment %q)", filepath.Base(file), name, secret, env, job.Environment)
						}
					}
				}
			}
		}
	}
	if found < 10 {
		t.Errorf("found %d token mints, want every workflow that mints one checked", found)
	}
}
