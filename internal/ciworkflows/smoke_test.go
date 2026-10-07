package ciworkflows_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestStackSmokeWorkflow pins stack-smoke.yml's interface: the same inputs and defaults for
// workflow_call and workflow_dispatch, read-only permissions, and each input reaching
// smoke/run.sh as the variable smoke/compose.yaml reads.
func TestStackSmokeWorkflow(t *testing.T) {
	var wf struct {
		On map[string]struct {
			Inputs map[string]struct {
				Type    string `yaml:"type"`
				Default string `yaml:"default"`
			} `yaml:"inputs"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	read(t, "stack-smoke.yml", &wf)
	call, dispatch := wf.On["workflow_call"].Inputs, wf.On["workflow_dispatch"].Inputs
	if !reflect.DeepEqual(call, dispatch) {
		t.Errorf("workflow_call inputs %v differ from workflow_dispatch inputs %v", call, dispatch)
	}
	wantDefaults := map[string]string{"api-tag": "0.21.0", "ui-tag": "2.12.1", "agent-tag": "0.9.0"}
	for name, def := range wantDefaults {
		if in, ok := call[name]; !ok || in.Default != def || in.Type != "string" {
			t.Errorf("input %s = %+v, want a string defaulting to %q", name, in, def)
		}
	}
	if len(call) != len(wantDefaults) {
		t.Errorf("inputs %v, want exactly %v", call, wantDefaults)
	}
	if !reflect.DeepEqual(wf.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("permissions %v, want contents: read only", wf.Permissions)
	}

	compose, err := os.ReadFile(filepath.Join("..", "..", "smoke", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for name, job := range wf.Jobs {
		if job.Permissions != nil {
			t.Errorf("job %s sets its own permissions %v", name, job.Permissions)
		}
		for _, s := range job.Steps {
			if s.Run != "smoke/run.sh" {
				continue
			}
			found = true
			for input, env := range map[string]string{"api-tag": "SMOKE_API_TAG", "ui-tag": "SMOKE_UI_TAG", "agent-tag": "SMOKE_AGENT_TAG"} {
				if want := "${{ inputs." + input + " }}"; s.Env[env] != want {
					t.Errorf("step %q: %s = %q, want %q", s.Name, env, s.Env[env], want)
				}
				if !strings.Contains(string(compose), "${"+env+":?") {
					t.Errorf("smoke/compose.yaml does not require %s", env)
				}
			}
		}
	}
	if !found {
		t.Error("no step runs smoke/run.sh")
	}
}

// TestSmokeCompose checks that the smoke stack only uses the published product images, binds
// its ports to localhost and reads no env files (no secrets beyond what run.sh generates).
func TestSmokeCompose(t *testing.T) {
	var c struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "smoke", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	images := map[string]string{
		"postgres": "postgres:17.5",
		"api":      "ghcr.io/compliance-framework/api:${SMOKE_API_TAG:?",
		"ui":       "ghcr.io/compliance-framework/ui:${SMOKE_UI_TAG:?",
		"agent":    "ghcr.io/compliance-framework/agent:${SMOKE_AGENT_TAG:?",
	}
	if len(c.Services) != len(images) {
		t.Errorf("services %v, want %v", c.Services, images)
	}
	for name, svc := range c.Services {
		if img, _ := svc["image"].(string); images[name] == "" || !strings.HasPrefix(img, images[name]) {
			t.Errorf("service %s: image %q, want %s...", name, img, images[name])
		}
		if _, ok := svc["build"]; ok {
			t.Errorf("service %s builds an image; the smoke test runs published images only", name)
		}
		if _, ok := svc["env_file"]; ok {
			t.Errorf("service %s reads an env_file", name)
		}
		ports, _ := svc["ports"].([]any)
		for _, p := range ports {
			if s, _ := p.(string); !strings.HasPrefix(s, "127.0.0.1:") {
				t.Errorf("service %s: port %q is not bound to 127.0.0.1", name, s)
			}
		}
	}
}

// runSmoke runs smoke/run.sh with fake docker and curl that log their arguments to calls.
func runSmoke(t *testing.T, env ...string) (out, calls string, failed bool) {
	t.Helper()
	need(t, "jq", "openssl")
	script, err := filepath.Abs(filepath.Join("..", "..", "smoke", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	callsFile := filepath.Join(t.TempDir(), "calls")
	bin := t.TempDir()
	for name, src := range map[string]string{
		"docker": "#!/bin/sh\necho \"docker $*\" >> \"$CALLS\"\n",
		// A stack that never gets ready: every HTTP status is 503.
		"curl": "#!/bin/sh\necho \"curl $*\" >> \"$CALLS\"\nprintf 503\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(src), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), append([]string{
		"PATH=" + bin + ":" + os.Getenv("PATH"), "CALLS=" + callsFile, "GITHUB_ACTIONS=",
		"SMOKE_API_TAG=0.21.0", "SMOKE_UI_TAG=2.12.1", "SMOKE_AGENT_TAG=0.9.0", "SMOKE_TIMEOUT=0",
	}, env...)...)
	b, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatal(err)
	}
	c, _ := os.ReadFile(callsFile)
	return string(b), string(c), err != nil
}

func TestSmokeRunSettings(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want string
	}{
		{"SMOKE_UI_TAG=", "SMOKE_UI_TAG is not set"},
		{"SMOKE_AGENT_TAG=0.9.0;id", "SMOKE_AGENT_TAG '0.9.0;id' is not a valid image tag"},
		{"SMOKE_API_TAG=-rc", "SMOKE_API_TAG '-rc' is not a valid image tag"},
	} {
		out, calls, failed := runSmoke(t, tc.env)
		if !failed || !strings.Contains(out, tc.want) {
			t.Errorf("%s: failed=%v, want %q; output:\n%s", tc.env, failed, tc.want, out)
		}
		if calls != "" {
			t.Errorf("%s: ran commands before checking the settings:\n%s", tc.env, calls)
		}
	}
}

// TestSmokeRunFailure checks that a failed check names what failed, dumps the container logs
// and tears the stack down.
func TestSmokeRunFailure(t *testing.T) {
	out, calls, failed := runSmoke(t)
	if !failed || !strings.Contains(out, "SMOKE FAILED: the API did not become ready") {
		t.Fatalf("failed=%v; output:\n%s", failed, out)
	}
	var compose []string
	for _, line := range strings.Split(strings.TrimSpace(calls), "\n") {
		if rest, ok := strings.CutPrefix(line, "docker compose -p ccf-smoke -f "); ok {
			compose = append(compose, rest[strings.Index(rest, " ")+1:])
		}
	}
	want := []string{"pull --quiet", "up -d postgres api ui", "ps -a", "logs --no-color --timestamps", "down -v --remove-orphans"}
	if !reflect.DeepEqual(compose, want) {
		t.Errorf("compose calls %q, want %q", compose, want)
	}

	// SMOKE_KEEP=1 still dumps the logs but leaves the stack up.
	_, calls, _ = runSmoke(t, "SMOKE_KEEP=1")
	if !strings.Contains(calls, " logs ") || strings.Contains(calls, " down ") {
		t.Errorf("SMOKE_KEEP=1: calls:\n%s", calls)
	}
}
