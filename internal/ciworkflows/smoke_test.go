package ciworkflows_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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

// fakeStack is a fake curl for a healthy stack: it answers each URL smoke/run.sh calls, and
// lists the agent's instance without a heartbeat on the first poll and with one afterwards.
const fakeStack = `#!/bin/sh
echo "curl $*" >> "$CALLS"
out=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    http://*) url="$1" ;;
  esac
  shift
done
code=200
case "$url" in
  */api/health/ready) body='{"status":"ok"}' ;;
  */api/auth/login) body='{"data":{"auth_token":"t0ken"}}' ;;
  */api/admin/agents) body='{"data":{"id":"agent-1"}}'; code=201 ;;
  */api/admin/agents/agent-1/keys) body='{"data":{"client-id":"client-1","client-secret":"s3cret"}}'; code=201 ;;
  */api/admin/agents/agent-1/instances)
    n=$(( $(cat "$CALLS.polls" 2>/dev/null || echo 0) + 1 ))
    echo "$n" > "$CALLS.polls"
    if [ "$n" -lt 2 ]; then
      body='{"data":[{"instance-id":"inst-1","heartbeat-config-revision":null}]}'
    else
      body='{"data":[{"instance-id":"inst-1","agent-version":"0.9.0","mode":"report","status":"not-applicable","heartbeat-config-revision":0}]}'
    fi ;;
  http://127.0.0.1:18000/) body='<!doctype html><div id="app"></div>' ;;
  *) body='not found'; code=404 ;;
esac
if [ -n "$out" ]; then printf '%s' "$body" > "$out"; printf '%s' "$code"; else printf '%s' "$body"; fi
`

// runSmoke runs smoke/run.sh with a fake docker and a fake curl that log their arguments.
// healthy picks fakeStack; otherwise every HTTP status is 503 (a stack that never gets ready).
func runSmoke(t *testing.T, healthy bool, env ...string) (out, calls string, failed bool) {
	t.Helper()
	need(t, "jq", "openssl")
	script, err := filepath.Abs(filepath.Join("..", "..", "smoke", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	callsFile := filepath.Join(t.TempDir(), "calls")
	curl := "#!/bin/sh\necho \"curl $*\" >> \"$CALLS\"\nprintf 503\n"
	if healthy {
		curl = fakeStack
	}
	bin := t.TempDir()
	for name, src := range map[string]string{
		// The psql query for the API's tables finds all four; starting the agent records the
		// key it gets.
		"docker": `#!/bin/sh
echo "docker $*" >> "$CALLS"
case "$*" in
  *psql*) echo 4 ;;
  *"up -d --no-deps agent"*) echo "agent key: $SMOKE_AGENT_CLIENT_ID $SMOKE_AGENT_CLIENT_SECRET" >> "$CALLS" ;;
esac
`,
		"curl": curl,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(src), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), append([]string{
		"PATH=" + bin + ":" + os.Getenv("PATH"), "CALLS=" + callsFile, "GITHUB_ACTIONS=",
		"SMOKE_API_TAG=0.21.0", "SMOKE_UI_TAG=2.12.1", "SMOKE_AGENT_TAG=0.9.0", "SMOKE_TIMEOUT=0",
		"SMOKE_API_PORT=", "SMOKE_UI_PORT=", "SMOKE_PROJECT=", "SMOKE_KEEP=",
	}, env...)...)
	b, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatal(err)
	}
	c, _ := os.ReadFile(callsFile)
	return string(b), string(c), err != nil
}

// composeCalls returns the docker compose subcommands in calls, without the project and file.
func composeCalls(calls string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(calls), "\n") {
		if rest, ok := strings.CutPrefix(line, "docker compose -p ccf-smoke -f "); ok {
			_, sub, _ := strings.Cut(rest, " ")
			out = append(out, sub)
		}
	}
	return out
}

func TestSmokeRunSettings(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want string
	}{
		{"SMOKE_UI_TAG=", "SMOKE_UI_TAG is not set"},
		{"SMOKE_AGENT_TAG=0.9.0;id", "SMOKE_AGENT_TAG '0.9.0;id' is not a valid image tag"},
		{"SMOKE_API_TAG=-rc", "SMOKE_API_TAG '-rc' is not a valid image tag"},
		{"SMOKE_TIMEOUT=3m", "SMOKE_TIMEOUT '3m' is not a whole number"},
		{"SMOKE_API_PORT=80a", "SMOKE_API_PORT '80a' is not a whole number"},
		{"SMOKE_UI_PORT=-1", "SMOKE_UI_PORT '-1' is not a whole number"},
	} {
		out, calls, failed := runSmoke(t, true, tc.env)
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
	out, calls, failed := runSmoke(t, false)
	if !failed || !strings.Contains(out, "SMOKE FAILED: the API did not become ready") {
		t.Fatalf("failed=%v; output:\n%s", failed, out)
	}
	want := []string{"pull --quiet", "up -d postgres api ui", "ps -a", "logs --no-color --timestamps", "down -v --remove-orphans"}
	if got := composeCalls(calls); !reflect.DeepEqual(got, want) {
		t.Errorf("compose calls %q, want %q", got, want)
	}

	// SMOKE_KEEP=1 still dumps the logs but leaves the stack up.
	_, calls, _ = runSmoke(t, false, "SMOKE_KEEP=1")
	if got := composeCalls(calls); !slices.Contains(got, "logs --no-color --timestamps") || slices.Contains(got, "down -v --remove-orphans") {
		t.Errorf("SMOKE_KEEP=1: compose calls %q", got)
	}
}

// TestSmokeRunPasses runs the whole script against a healthy fake stack: it waits for the
// instance's heartbeat, starts the agent with the key it created, never prints the key's
// secret (and masks it on Actions), and tears down without dumping logs.
func TestSmokeRunPasses(t *testing.T) {
	out, calls, failed := runSmoke(t, true, "SMOKE_TIMEOUT=30")
	if failed || !strings.Contains(out, "agent: instance inst-1 (version 0.9.0, mode report, status not-applicable) registered and heartbeating") ||
		!strings.Contains(out, "Stack smoke test passed") {
		t.Fatalf("failed=%v; output:\n%s", failed, out)
	}
	if strings.Contains(out, "s3cret") {
		t.Errorf("the key's secret is in the output:\n%s", out)
	}
	if !strings.Contains(calls, "agent key: client-1 s3cret") {
		t.Errorf("the agent did not get the key; calls:\n%s", calls)
	}
	if n := strings.Count(calls, "/api/admin/agents/agent-1/instances"); n < 2 {
		t.Errorf("polled the instances %d time(s); the first poll has no heartbeat, so want at least 2", n)
	}
	want := []string{"pull --quiet", "up -d postgres api ui",
		"exec -T postgres psql -U postgres -d ccf -tAc select count(*) from information_schema.tables where table_schema = 'public' and table_name in ('ccf_users', 'ccf_agents', 'ccf_agent_service_account_keys', 'ccf_agent_instances')"}
	got := composeCalls(calls)
	if len(got) != 6 || !reflect.DeepEqual(got[:3], want) || !strings.HasPrefix(got[3], "exec -T api /api users add -e smoke-admin@example.test ") ||
		got[4] != "up -d --no-deps agent" || got[5] != "down -v --remove-orphans" {
		t.Errorf("compose calls %q", got)
	}

	out, _, failed = runSmoke(t, true, "SMOKE_TIMEOUT=30", "GITHUB_ACTIONS=true")
	if failed || !strings.Contains(out, "::add-mask::s3cret") || !strings.Contains(out, "::add-mask::t0ken") {
		t.Errorf("on Actions the key's secret and the token must be masked; failed=%v, output:\n%s", failed, out)
	}
}
