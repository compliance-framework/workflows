package ciworkflows_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeGolangci stands in for `golangci-lint config verify` as of v2.15.0: it validates against
// the schema built into it and, like the real one, rejects the --schema flag that version
// removed. A config containing "invalid" doesn't validate.
const fakeGolangci = `#!/bin/sh
printf 'golangci-lint %s\n' "$*" >> "$FAKE_LOG"
cfg=.golangci.yml
for a; do
  case $a in
    --config=*) cfg=${a#--config=} ;;
    --schema=*) echo 'Error: unknown flag: --schema'; exit 1 ;;
  esac
done
if grep -q invalid "$cfg"; then
  echo 'jsonschema: "linters" does not validate with "/properties/linters/additionalProperties"'
  echo 'The command is terminated due to an error: the configuration contains invalid elements'
  exit 3
fi
`

// TestGolangciConfigVerify runs the config verify step with a fake golangci-lint (and a curl that
// only logs, so a download would show up): a valid config passes, an invalid one fails with an
// ::error, and a shared config is passed with --config.
func TestGolangciConfigVerify(t *testing.T) {
	src := script(t, goPlugin, "lint", "Verify the golangci-lint config")
	bin := t.TempDir()
	for name, body := range map[string]string{
		"golangci-lint": fakeGolangci,
		"curl":          "#!/bin/sh\nprintf 'curl %s\\n' \"$*\" >> \"$FAKE_LOG\"\n",
	} {
		writeFile(t, filepath.Join(bin, name), body)
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")

	for _, tc := range []struct {
		name   string
		config string
		failed bool
	}{
		{name: "valid config", config: "version: \"2\"\n"},
		{name: "invalid config", config: "invalid: true\n", failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, ".golangci.yml"), tc.config)
			log := filepath.Join(t.TempDir(), "log")
			r := run(t, dir, src, path, "CONFIG=", "FAKE_LOG="+log)
			errored := strings.Contains(r.out, "::error::")
			if r.failed != tc.failed || errored != tc.failed {
				t.Fatalf("failed=%v error=%v, want both %v:\n%s", r.failed, errored, tc.failed, r.out)
			}
			if got, want := readLog(t, log), []string{"golangci-lint config verify"}; !slices.Equal(got, want) {
				t.Errorf("ran %q, want %q", got, want)
			}
		})
	}

	t.Run("shared config", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(t.TempDir(), "ccf-golangci.yml")
		writeFile(t, cfg, "version: \"2\"\n")
		writeFile(t, filepath.Join(dir, ".golangci.yml"), "invalid: true\n") // must not be read
		log := filepath.Join(t.TempDir(), "log")
		r := run(t, dir, src, path, "CONFIG="+cfg, "FAKE_LOG="+log)
		want := []string{"golangci-lint config verify --config=" + cfg}
		if got := readLog(t, log); r.failed || !slices.Equal(got, want) {
			t.Fatalf("ran %q (failed=%v), want %q:\n%s", got, r.failed, want, r.out)
		}
	})
}

// readLog returns the fake commands' log, one command per line.
func readLog(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// TestGolangciVerifySetup pins how the Go kind workflows verify the config: the action's own
// verify, which runs before the lint, is off, and the step after it runs whenever the action ran
// and passes no --schema (golangci-lint v2.15.0 removed the flag).
func TestGolangciVerifySetup(t *testing.T) {
	for _, file := range []string{goPlugin, goService, goLib} {
		var wf struct {
			Jobs map[string]struct {
				Steps []struct {
					ID   string            `yaml:"id"`
					Name string            `yaml:"name"`
					Uses string            `yaml:"uses"`
					If   string            `yaml:"if"`
					Run  string            `yaml:"run"`
					With map[string]string `yaml:"with"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		read(t, file, &wf)
		var action, verify bool
		for _, s := range wf.Jobs["lint"].Steps {
			if strings.HasPrefix(s.Uses, "golangci/golangci-lint-action@") {
				action = true
				if s.ID != "golangci-lint" || s.With["verify"] != "false" {
					t.Errorf("%s: golangci-lint-action id %q verify %q, want id golangci-lint, verify false", file, s.ID, s.With["verify"])
				}
			}
			if s.Name == "Verify the golangci-lint config" {
				verify = true
				if want := "${{ !cancelled() && steps.golangci-lint.outcome != 'skipped' }}"; s.If != want {
					t.Errorf("%s: the verify step's if is %q, want %q", file, s.If, want)
				}
				if strings.Contains(s.Run, "--schema") {
					t.Errorf("%s: the verify step passes --schema, which golangci-lint v2.15.0 removed", file)
				}
			}
		}
		if !action || !verify {
			t.Errorf("%s: lint job has golangci-lint-action %v, verify step %v; want both", file, action, verify)
		}
	}
}
