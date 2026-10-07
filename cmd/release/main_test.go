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
			err := run(tc.args, getenv, strings.NewReader(""), &out)
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

func TestNextRC(t *testing.T) {
	var out strings.Builder
	err := run([]string{"next-rc", "--version", "1.2.0"}, func(string) string { return "" }, strings.NewReader("v1.2.0-rc1\nv1.2.0-rc2\n"), &out)
	if err != nil || out.String() != "v1.2.0-rc3\n" {
		t.Fatalf("got %q, %v", out.String(), err)
	}
}

func TestPreviewTags(t *testing.T) {
	dir := t.TempDir()
	repo := `"repository": {"full_name": "compliance-framework/mock-api", "default_branch": "main"}`
	pr := func(head string) string {
		return write(t, dir, strings.ReplaceAll(head, "/", "_")+".json", `{`+repo+`, "pull_request": {"number": 12, "labels": [{"name": "preview"}], "head": {"repo": {"full_name": "`+head+`"}}}}`)
	}
	for _, tc := range []struct {
		name, eventName, ref, event, want string
	}{
		{"push to main", "push", "refs/heads/main", write(t, dir, "push.json", `{`+repo+`}`), "tags=main sha-0123456"},
		{"labelled PR", "pull_request", "refs/pull/12/merge", pr("compliance-framework/mock-api"), "tags=pr-12"},
		{"fork PR", "pull_request", "refs/pull/12/merge", pr("someone/mock-api"), "tags="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ghOut := filepath.Join(t.TempDir(), "out")
			env := map[string]string{"GITHUB_EVENT_PATH": tc.event, "GITHUB_EVENT_NAME": tc.eventName, "GITHUB_REF": tc.ref, "GITHUB_SHA": "0123456789abcdef", "GITHUB_OUTPUT": ghOut}
			var out strings.Builder
			if err := run([]string{"preview-tags"}, func(k string) string { return env[k] }, strings.NewReader(""), &out); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(ghOut)
			if err != nil || strings.TrimSpace(string(b)) != tc.want {
				t.Fatalf("GITHUB_OUTPUT = %q, %v; want %q (output %q)", b, err, tc.want, out.String())
			}
		})
	}
}

func TestReleaseTags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
		fail bool
	}{
		{[]string{"--tag", "v1.2.3"}, "tags=1.2.3 1.2 1 latest\nfinal=true\nmajor=v1", false},
		{[]string{"--tag", "v1.2.3-rc1", "--style", "artifact"}, "tags=v1.2.3-rc1\nfinal=false\nmajor=v1", false},
		{[]string{"--tag", "chart-v0.2.0", "--prefix", "chart-v", "--style", "artifact"}, "tags=v0.2.0 latest\nfinal=true\nmajor=v0", false},
		{[]string{"--tag", ""}, "", true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			ghOut := filepath.Join(t.TempDir(), "out")
			var out strings.Builder
			err := run(append([]string{"release-tags"}, tc.args...), func(k string) string { return map[string]string{"GITHUB_OUTPUT": ghOut}[k] }, strings.NewReader(""), &out)
			if (err != nil) != tc.fail {
				t.Fatalf("err = %v, want fail=%v", err, tc.fail)
			}
			if b, _ := os.ReadFile(ghOut); strings.TrimSpace(string(b)) != tc.want {
				t.Fatalf("GITHUB_OUTPUT = %q, want %q (output %q)", b, tc.want, out.String())
			}
		})
	}
}

func TestChart(t *testing.T) {
	dir := t.TempDir()
	for chart, name := range map[string]string{"ccf-agent": "ccf-agent", "ccf-app": `"ccf"`} {
		if err := os.MkdirAll(filepath.Join(dir, chart), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, chart), "Chart.yaml", "apiVersion: v2\nname: "+name+"\nversion: 0.1.0\n")
	}
	for _, tc := range []struct{ tag, want, err string }{
		{"ccf-agent-v0.3.0", "path=" + filepath.Join(dir, "ccf-agent") + "\nname=ccf-agent\nversion=0.3.0", ""},
		{"ccf-v0.9.0-rc1", "path=" + filepath.Join(dir, "ccf-app") + "\nname=ccf\nversion=0.9.0-rc1", ""},
		{"v0.3.0", "", "has 2 charts"},
		{"nope-v1.0.0", "", "found 0"},
		{"ccf-agent", "", "is not"},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			ghOut := filepath.Join(t.TempDir(), "out")
			var out strings.Builder
			err := run([]string{"chart", "--tag", tc.tag, "--charts-dir", dir}, func(k string) string { return map[string]string{"GITHUB_OUTPUT": ghOut}[k] }, strings.NewReader(""), &out)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if b, _ := os.ReadFile(ghOut); err != nil || strings.TrimSpace(string(b)) != tc.want {
				t.Fatalf("GITHUB_OUTPUT = %q, %v; want %q", b, err, tc.want)
			}
		})
	}
}
