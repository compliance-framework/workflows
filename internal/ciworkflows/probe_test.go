package ciworkflows_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProbe stands in for the built plugin-probe: it records its arguments, writes a report with
// a newline in a string to the --json path and exits with $FAKE_STATUS.
const fakeProbe = `cat > "$RUNNER_TEMP/plugin-probe" <<'FAKE'
#!/bin/sh
echo "$@" > "$RUNNER_TEMP/args"
while [ $# -gt 0 ]; do
  if [ "$1" = --json ]; then printf '{\n  "plugins": [{"source": "a", "error": "line1\\nline2"}]\n}\n' > "$2"; fi
  shift
done
exit "$FAKE_STATUS"
FAKE
chmod +x "$RUNNER_TEMP/plugin-probe"
`

func TestProbeStep(t *testing.T) {
	need(t, "jq")
	src := fakeProbe + script(t, "plugin-probe.yml", "probe", "Probe")
	for _, status := range []string{"0", "1"} {
		summary := filepath.Join(t.TempDir(), "summary")
		r := run(t, t.TempDir(), src, "FAKE_STATUS="+status, "GITHUB_STEP_SUMMARY="+summary,
			"PLUGINS=ghcr.io/x/a:v1,ghcr.io/x/b:v1", "POLICIES=")
		if r.failed != (status == "1") {
			t.Fatalf("status %s: failed=%v; output:\n%s", status, r.failed, r.out)
		}
		var report struct {
			Plugins []struct{ Error string } `json:"plugins"`
		}
		if err := json.Unmarshal([]byte(r.outputs["report"]), &report); err != nil || len(report.Plugins) != 1 || report.Plugins[0].Error != "line1\nline2" {
			t.Errorf("report output = %q (%v)", r.outputs["report"], err)
		}
		args, err := os.ReadFile(filepath.Join(r.temp, "args"))
		if err != nil {
			t.Fatal(err)
		}
		if want := "--plugins ghcr.io/x/a:v1,ghcr.io/x/b:v1 --policies  --json " + r.temp + "/report.json --markdown " + summary; strings.TrimSpace(string(args)) != want {
			t.Errorf("args = %q, want %q", args, want)
		}
	}
}

// TestAgentRefStep runs the step against a copy of this module: it moves the agent library to
// another release and keeps OPA at the version that release requires.
func TestAgentRefStep(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads modules")
	}
	need(t, "go", "jq")
	tools := t.TempDir()
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(tools, rel), 0o755)
		}
		if !strings.HasSuffix(path, ".go") && d.Name() != "go.mod" && d.Name() != "go.sum" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(tools, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}

	r := run(t, tools, script(t, "plugin-probe.yml", "probe", "Use the agent ref"), "TOOLS_DIR="+tools, "AGENT_REF=v0.8.1")
	if r.failed || !strings.Contains(r.out, "Probing with agent v0.8.1 and OPA v1.14.1") {
		t.Fatalf("step failed=%v; output:\n%s", r.failed, r.out)
	}
	cmd := exec.Command("go", "build", "-o", os.DevNull, "./cmd/plugin-probe")
	cmd.Dir = tools
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("building against agent v0.8.1: %v\n%s", err, out)
	}
}
