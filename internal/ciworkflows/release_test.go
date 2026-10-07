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
