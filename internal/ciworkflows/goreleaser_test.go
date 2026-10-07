package ciworkflows_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const goreleaserGuard = "Check the goreleaser config"

// goreleaserReleases returns the workflows whose jobs run `goreleaser release` against a real
// release (not --snapshot), and fails the test for any such job that doesn't first run the
// one goreleaser config check, step for step identical across workflows.
func goreleaserReleases(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	var first string
	var users []string
	for _, path := range files {
		file := filepath.Base(path)
		if file == "plugin-release.yml" { // legacy, unchanged until removed (README)
			continue
		}
		var wf struct {
			Jobs map[string]struct {
				Steps []map[string]any `yaml:"steps"`
			} `yaml:"jobs"`
		}
		read(t, file, &wf)
		for name, job := range wf.Jobs {
			var guard map[string]any
			for _, s := range job.Steps {
				if s["name"] == goreleaserGuard {
					guard = s
				}
				uses, _ := s["uses"].(string)
				with, _ := s["with"].(map[string]any)
				args, _ := with["args"].(string)
				if !strings.HasPrefix(uses, "goreleaser/goreleaser-action@") ||
					!strings.HasPrefix(args, "release") || strings.Contains(args, "--snapshot") {
					continue
				}
				users = append(users, file)
				switch {
				case guard == nil:
					t.Errorf("%s: job %s runs goreleaser %s without %q before it", file, name, args, goreleaserGuard)
				case want == nil:
					want, first = guard, file
				case !reflect.DeepEqual(guard, want):
					t.Errorf("%s: %q differs from %s's:\n%v\nwant:\n%v", file, goreleaserGuard, first, guard, want)
				}
			}
		}
	}
	return users
}

// TestGoreleaserReleaseGuard pins the goreleaser config check to every release that
// goreleaser publishes.
func TestGoreleaserReleaseGuard(t *testing.T) {
	users := goreleaserReleases(t)
	for _, want := range []string{"release-go-plugin.yml", "release-go-lib.yml"} {
		if !slices.Contains(users, want) {
			t.Fatalf("goreleaser releases %v: want %s", users, want)
		}
	}
}

func TestGoreleaserReleaseConfig(t *testing.T) {
	need(t, "yq")
	src := script(t, "release-go-plugin.yml", "publish", goreleaserGuard)
	const recommended = "release:\n  prerelease: auto\n  replace_existing_artifacts: true\n"
	for _, tc := range []struct {
		name, file, config, want, notWant string
		failed                            bool
	}{
		{"recommended", ".goreleaser.yaml", "version: 2\n" + recommended, "Using .goreleaser.yaml.", "::warning", false},
		{"config dir", ".config/goreleaser.yml", recommended, "Using .config/goreleaser.yml.", "::warning", false},
		{"no replace", ".goreleaser.yml", "release:\n  prerelease: auto\n", "replace_existing_artifacts is not true", "::error", false},
		{"keep-existing", ".goreleaser.yaml", recommended + "  mode: keep-existing\n", "Using", "::warning", false},
		{"append", ".goreleaser.yaml", recommended + "  mode: append\n", "::warning file=.goreleaser.yaml::release.mode is append", "::error", false},
		{"no prerelease", ".goreleaser.yaml", "version: 2\n", "release.prerelease must be auto (it is '')", "Using", true},
		{"prerelease true", ".goreleaser.yaml", "release:\n  prerelease: true\n", "(it is 'true')", "Using", true},
		{"no config", ".gorelreaser.yml", recommended, "no goreleaser config", "Using", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(tc.file)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			r := run(t, dir, src)
			if r.failed != tc.failed || !strings.Contains(r.out, tc.want) || strings.Contains(r.out, tc.notWant) {
				t.Fatalf("failed=%v, want %v; output:\n%s", r.failed, tc.failed, r.out)
			}
		})
	}
}
