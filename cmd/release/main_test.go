package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "base.json", `{".": "0.9.0"}`)
	head := write(t, dir, "head.json", `{".": "1.0.0"}`)
	gomod := write(t, dir, "go.mod", "module github.com/compliance-framework/mock-api\n\nrequire github.com/compliance-framework/api v1.0.0-rc2\n")
	approved := write(t, dir, "approved.json", `{"pull_request": {"labels": [{"name": "release:major-approved"}]}}`)
	unlabelled := write(t, dir, "unlabelled.json", `{"pull_request": {"labels": [{"name": "autorelease: pending"}]}}`)
	for _, tc := range []struct {
		name  string
		args  []string
		event string
		want  string // in the output, or in the error when fail
		fail  bool
	}{
		{"guard approved", []string{"check", "version-guard", "--base", base, "--head", head}, approved, "approved by", false},
		{"guard unlabelled", []string{"check", "version-guard", "--base", base, "--head", head}, unlabelled, "needs the \"release:major-approved\" label", true},
		{"guard minor", []string{"check", "version-guard", "--base", base, "--head", base}, "", "No major version increase.", false},
		{"guard needs base", []string{"check", "version-guard"}, "", "--base is required", true},
		{"internal deps", []string{"check", "internal-deps", "--gomod", gomod}, "", "github.com/compliance-framework/api@v1.0.0-rc2", true},
		{"module path", []string{"check", "module-path", "--gomod", gomod, "--manifest", head}, "", "matches version 1.0.0", false},
		{"module path, no root package", []string{"check", "module-path", "--gomod", gomod, "--manifest", write(t, dir, "charts.json", `{"charts/a": "1.0.0"}`)}, "", "no version for the root package", true},
		{"unknown", []string{"check", "nope"}, "", "usage:", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			getenv := func(k string) string {
				if k == "GITHUB_EVENT_PATH" {
					return tc.event
				}
				return ""
			}
			err := run(tc.args, getenv, &out)
			got := out.String()
			if err != nil {
				got = err.Error()
			}
			if (err != nil) != tc.fail || !strings.Contains(got, tc.want) {
				t.Fatalf("err = %v, output %q; want fail=%v containing %q", err, out.String(), tc.fail, tc.want)
			}
		})
	}
}
