package ciworkflows_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasePleaseDefaultsDrift(t *testing.T) {
	need(t, "jq")
	src := script(t, "release-please.yml", "release-please", "Compare the config with the shared defaults")
	defaults, err := os.ReadFile(filepath.Join("..", "..", "release-please", "defaults.json"))
	if err != nil {
		t.Fatal(err)
	}
	copied := strings.Replace(string(defaults), "{", `{"packages": {".": {"release-type": "go"}},`, 1)
	drifted := strings.Replace(copied, `"bump-minor-pre-major": true`, `"bump-minor-pre-major": false`, 1)
	for _, tc := range []struct{ name, config, want, notWant string }{
		{"copied", copied, "has the shared defaults", "::warning"},
		{"drifted", drifted, `::warning file=release-please-config.json::"bump-minor-pre-major" differs`, "changelog-sections"},
		{"missing", "", "::warning::no release-please-config.json", "differs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, ".ccf-workflows", "release-please"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".ccf-workflows", "release-please", "defaults.json"), defaults, 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.config != "" {
				if err := os.WriteFile(filepath.Join(dir, "release-please-config.json"), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			r := run(t, dir, src)
			if r.failed || !strings.Contains(r.out, tc.want) || strings.Contains(r.out, tc.notWant) {
				t.Fatalf("failed=%v; output:\n%s", r.failed, r.out)
			}
		})
	}
}

func TestReleaseChecksBaseManifest(t *testing.T) {
	src := script(t, "release-checks.yml", "release-checks", "Read the base manifest")
	bin := t.TempDir()
	fake := "#!/bin/sh\ncase \"$FAKE\" in\nok) echo '{\".\": \"1.2.0\"}' ;;\n404) echo 'gh: Not Found (HTTP 404)' >&2; exit 1 ;;\n*) echo 'gh: Server Error (HTTP 500)' >&2; exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		fake, want string
		failed     bool
	}{
		{"ok", `{".": "1.2.0"}`, false},
		{"404", "{}", false},
		{"500", "", true},
	} {
		t.Run(tc.fake, func(t *testing.T) {
			r := run(t, t.TempDir(), src, "PATH="+bin+":"+os.Getenv("PATH"), "FAKE="+tc.fake, "GITHUB_REPOSITORY=o/r", "BASE_SHA=abc")
			if r.failed != tc.failed {
				t.Fatalf("failed=%v, want %v; output:\n%s", r.failed, tc.failed, r.out)
			}
			if !tc.failed {
				b, err := os.ReadFile(filepath.Join(r.temp, "base.json"))
				if err != nil || strings.TrimSpace(string(b)) != tc.want {
					t.Fatalf("base.json = %q, %v; want %q", b, err, tc.want)
				}
			}
		})
	}
}

// TestTokenScopeGuard pins the guard that stops an org-wide release token to one copy.
func TestTokenScopeGuard(t *testing.T) {
	const step = "Check the token scope"
	src := script(t, "release-please.yml", "release-please", step)
	if other := script(t, "cut-prerelease.yml", "cut", step); other != src {
		t.Fatalf("step %q differs between release-please.yml and cut-prerelease.yml", step)
	}
	for _, tc := range []struct {
		name, repoName, repository string
		failed                     bool
	}{
		{"this repo", "mock-api", "compliance-framework/mock-api", false},
		{"empty name", "", "compliance-framework/mock-api", true},
		{"other owner", "mock-api", "someone/mock-api", true},
		{"other repo", "api", "compliance-framework/mock-api", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, t.TempDir(), src, "REPO_NAME="+tc.repoName, "GITHUB_REPOSITORY="+tc.repository)
			if r.failed != tc.failed {
				t.Fatalf("failed=%v, want %v; output:\n%s", r.failed, tc.failed, r.out)
			}
		})
	}
}

func TestCutPrerelease(t *testing.T) {
	need(t, "go", "jq")
	src := script(t, "cut-prerelease.yml", "cut", "Tag and create the prerelease")
	bin := t.TempDir()
	// Prints what the real gh would after --jq, and records `gh release create`.
	fake := `#!/bin/sh
case "$1 $2" in
"pr list") echo "$FAKE_PRS" ;;
"release create") echo "$@" > "$RUNNER_TEMP/release-create" ;;
*) case "$*" in
  *contents/.release-please-manifest.json*) echo '{".": "1.3.0", "charts/a": "0.2.0"}' ;;
  *matching-refs*) printf '%s\n' $FAKE_TAGS ;;
  *commits/main*) echo 0123abc ;;
  esac ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, prs, tags, pkg, want string
		failed                     bool
	}{
		{"first rc", "release-please--branches--main", "v1.2.0", ".", "release create v1.3.0-rc1 --repo o/r --target 0123abc --prerelease --latest=false", false},
		{"third rc", "release-please--branches--main", "v1.3.0-rc1 v1.3.0-rc2", ".", "release create v1.3.0-rc3 ", false},
		{"no release PR", "", "", ".", "found 0", true},
		{"two release PRs", "release-please--branches--main release-please--branches--main--components--a", "", ".", "found 2", true},
		{"unknown package", "release-please--branches--main", "", "charts/b", "no version for package 'charts/b'", true},
		{"already released", "release-please--branches--main", "v1.3.0-rc1 v1.3.0", ".", "v1.3.0 is already released", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, filepath.Join("..", ".."), src, "PATH="+bin+":"+os.Getenv("PATH"), "FAKE_PRS="+tc.prs, "FAKE_TAGS="+tc.tags,
				"GITHUB_REPOSITORY=o/r", "DEFAULT_BRANCH=main", "PKG="+tc.pkg, "PREFIX=v", "GITHUB_STEP_SUMMARY=/dev/null")
			got := r.out
			if b, err := os.ReadFile(filepath.Join(r.temp, "release-create")); err == nil {
				got = string(b)
			} else if !tc.failed {
				t.Fatalf("no release created; output:\n%s", r.out)
			}
			if r.failed != tc.failed || !strings.Contains(got, tc.want) {
				t.Fatalf("failed=%v, want %v; got:\n%s", r.failed, tc.failed, got)
			}
		})
	}
}
