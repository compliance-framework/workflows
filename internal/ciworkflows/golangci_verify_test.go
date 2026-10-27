package ciworkflows_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeGolangci stands in for `golangci-lint config verify`. With --schema it validates against
// that local file; without, it loads golangci-lint.run's schema, which fails when FAKE_SITE is
// down with the error the real one printed in the outage. A config containing "invalid" doesn't
// validate.
const fakeGolangci = `#!/bin/sh
printf 'golangci-lint %s\n' "$*" >> "$FAKE_LOG"
cfg=.golangci.yml
schema=
for a; do
  case $a in
    --config=*) cfg=${a#--config=} ;;
    --schema=*) schema=${a#--schema=} ;;
  esac
done
if [ -n "$schema" ] && [ ! -s "$schema" ]; then
  echo "validate: compile schema: failing loading \"file://$schema\": no such file"
  exit 3
fi
if [ -z "$schema" ] && [ "$FAKE_SITE" = down ]; then
  echo "The command is terminated due to an error: [$cfg] validate: compile schema: failing loading \"https://golangci-lint.run/jsonschema/golangci.v2.11.jsonschema.json\": context deadline exceeded"
  exit 3
fi
if grep -q invalid "$cfg"; then
  echo 'jsonschema: "linters" does not validate with "/properties/linters/additionalProperties"'
  echo 'The command is terminated due to an error: the configuration contains invalid elements'
  exit 3
fi
`

// fakeCurl downloads a schema to -o, or fails like a timeout when FAKE_GITHUB is down.
const fakeCurl = `#!/bin/sh
printf 'curl %s\n' "$*" >> "$FAKE_LOG"
if [ "$FAKE_GITHUB" = down ]; then
  echo 'curl: (28) Connection timed out' >&2
  exit 28
fi
while [ $# -gt 0 ]; do
  if [ "$1" = -o ]; then out=$2; fi
  shift
done
echo '{}' > "$out"
`

// TestGolangciConfigVerify runs the config verify step with fake golangci-lint, curl and sleep:
// an invalid config fails; an unreachable schema host only warns.
func TestGolangciConfigVerify(t *testing.T) {
	src := script(t, goPlugin, "lint", "Verify the golangci-lint config")
	const schemaURL = "https://schema.example/golangci.json"
	bin := t.TempDir()
	for name, body := range map[string]string{
		"golangci-lint": fakeGolangci,
		"curl":          fakeCurl,
		"sleep":         "#!/bin/sh\nprintf 'sleep %s\\n' \"$*\" >> \"$FAKE_LOG\"\n",
	} {
		writeFile(t, filepath.Join(bin, name), body)
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")

	for _, tc := range []struct {
		name          string
		config        string
		github, site  string
		failed, warns bool
		// The commands the step ran, with <schema> for the downloaded schema file.
		want []string
	}{
		{
			name: "valid config, schema reachable", config: "version: \"2\"\n", github: "up", site: "up",
			want: []string{"curl <curl>", "golangci-lint config verify --schema=<schema>"},
		},
		{
			name: "valid config, GitHub unreachable", config: "version: \"2\"\n", github: "down", site: "up",
			want: []string{"curl <curl>", "golangci-lint config verify"},
		},
		{
			name: "valid config, schema hosts unreachable", config: "version: \"2\"\n", github: "down", site: "down", warns: true,
			want: []string{
				"curl <curl>",
				"golangci-lint config verify", "sleep 15",
				"golangci-lint config verify", "sleep 30",
				"golangci-lint config verify",
			},
		},
		{
			name: "invalid config, schema reachable", config: "invalid: true\n", github: "up", site: "up", failed: true,
			want: []string{"curl <curl>", "golangci-lint config verify --schema=<schema>"},
		},
		{
			name: "invalid config, GitHub unreachable", config: "invalid: true\n", github: "down", site: "up", failed: true,
			want: []string{"curl <curl>", "golangci-lint config verify"},
		},
		{
			// Nothing to validate against: the same warning as for a valid config.
			name: "invalid config, schema hosts unreachable", config: "invalid: true\n", github: "down", site: "down", warns: true,
			want: []string{
				"curl <curl>",
				"golangci-lint config verify", "sleep 15",
				"golangci-lint config verify", "sleep 30",
				"golangci-lint config verify",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, ".golangci.yml"), tc.config)
			log := filepath.Join(t.TempDir(), "log")
			r := run(t, dir, src, path, "CONFIG=", "SCHEMA_URL="+schemaURL, "FAKE_LOG="+log, "FAKE_GITHUB="+tc.github, "FAKE_SITE="+tc.site)
			warned := strings.Contains(r.out, "::warning::")
			errored := strings.Contains(r.out, "::error::")
			if r.failed != tc.failed || warned != tc.warns || errored != tc.failed {
				t.Fatalf("failed=%v warning=%v error=%v, want failed=%v warning=%v:\n%s", r.failed, warned, errored, tc.failed, tc.warns, r.out)
			}
			got := readLog(t, log, r.temp, schemaURL)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ran %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("shared config", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(t.TempDir(), "ccf-golangci.yml")
		writeFile(t, cfg, "version: \"2\"\n")
		writeFile(t, filepath.Join(dir, ".golangci.yml"), "invalid: true\n") // must not be read
		for _, github := range []string{"up", "down"} {
			log := filepath.Join(t.TempDir(), "log")
			r := run(t, dir, src, path, "CONFIG="+cfg, "SCHEMA_URL="+schemaURL, "FAKE_LOG="+log, "FAKE_GITHUB="+github, "FAKE_SITE=up")
			want := "golangci-lint config verify --config=" + cfg
			if github == "up" {
				want += " --schema=<schema>"
			}
			if got := readLog(t, log, r.temp, schemaURL); r.failed || len(got) != 2 || got[1] != want {
				t.Fatalf("GitHub %s: ran %q (failed=%v), want %q:\n%s", github, got, r.failed, want, r.out)
			}
		}
	})
}

// readLog returns the fake commands' log, with the curl arguments checked and replaced by <curl>
// and the downloaded schema's path by <schema>.
func readLog(t *testing.T, log, temp, schemaURL string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	schema := filepath.Join(temp, "golangci.jsonschema.json")
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i, l := range lines {
		if rest, ok := strings.CutPrefix(l, "curl "); ok {
			if !strings.Contains(rest, "--retry ") || !strings.HasSuffix(rest, "-o "+schema+" "+schemaURL) {
				t.Errorf("curl %s: want a retried download of %s to %s", rest, schemaURL, schema)
			}
			lines[i] = "curl <curl>"
		}
		lines[i] = strings.ReplaceAll(lines[i], schema, "<schema>")
	}
	return lines
}

// TestGolangciVerifySetup pins how the Go kind workflows verify the config: the action's own
// verify (which needs golangci-lint.run) is off, and the step's schema is the one of the
// golangci-lint version the action installs.
func TestGolangciVerifySetup(t *testing.T) {
	for _, file := range []string{goPlugin, goService, goLib} {
		var wf struct {
			Jobs map[string]struct {
				Steps []struct {
					ID   string            `yaml:"id"`
					Name string            `yaml:"name"`
					Uses string            `yaml:"uses"`
					If   string            `yaml:"if"`
					With map[string]string `yaml:"with"`
					Env  map[string]string `yaml:"env"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		read(t, file, &wf)
		var version, schemaURL, verifyIf string
		for _, s := range wf.Jobs["lint"].Steps {
			if strings.HasPrefix(s.Uses, "golangci/golangci-lint-action@") {
				if s.ID != "golangci-lint" || s.With["verify"] != "false" {
					t.Errorf("%s: golangci-lint-action id %q verify %q, want id golangci-lint, verify false", file, s.ID, s.With["verify"])
				}
				version = s.With["version"]
			}
			if s.Name == "Verify the golangci-lint config" {
				schemaURL, verifyIf = s.Env["SCHEMA_URL"], s.If
			}
		}
		if want := "https://raw.githubusercontent.com/golangci/golangci-lint/" + version + "/jsonschema/golangci.next.jsonschema.json"; version == "" || schemaURL != want {
			t.Errorf("%s: SCHEMA_URL %q, want %q (the schema of golangci-lint %q)", file, schemaURL, want, version)
		}
		if want := "${{ !cancelled() && steps.golangci-lint.outcome != 'skipped' }}"; verifyIf != want {
			t.Errorf("%s: the verify step's if is %q, want %q", file, verifyIf, want)
		}
	}
}
