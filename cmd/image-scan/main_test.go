package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/imagescan"
	"github.com/compliance-framework/workflows/internal/renovate"
)

const mock = "../../internal/manifest/testdata/mock.yaml"

func TestList(t *testing.T) {
	for _, tc := range []struct{ repos, want string }{
		{"", "mock-api,mock-agent,mock-ui,mock-agent-action\n"},
		{"mock-plugin-1,mock-ui,workflows", "mock-ui\n"},
		{"mock-plugin-1", "\n"},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), []string{"list", "--manifest", mock, "--repos", tc.repos}, nil, &out, nil); err != nil {
			t.Fatal(err)
		}
		if out.String() != tc.want {
			t.Errorf("list --repos %q = %q, want %q", tc.repos, out.String(), tc.want)
		}
	}
	if err := run(context.Background(), []string{"list", "--manifest", mock, "--repos", "api"}, nil, &bytes.Buffer{}, nil); err == nil {
		t.Error("list of a repo not in the manifest: want an error")
	}
}

type fakeFiles map[string]string // "repo/path" -> content

func (f fakeFiles) Dir(_ context.Context, repo, dir string) ([]string, error) {
	var out []string
	for k := range f {
		if name, ok := strings.CutPrefix(k, repo+"/"+dir+"/"); ok {
			out = append(out, name)
		}
	}
	return out, nil
}

func (f fakeFiles) File(_ context.Context, repo, path string) ([]byte, bool, error) {
	s, ok := f[repo+"/"+path]
	return []byte(s), ok, nil
}

// fakeScanner returns a scan per image; an image it doesn't know fails.
type fakeScanner map[string][]imagescan.Finding

func (f fakeScanner) Scan(_ context.Context, ref string) (*imagescan.Scan, error) {
	fs, ok := f[ref]
	if !ok {
		return nil, errors.New("pull " + ref + ": manifest unknown")
	}
	return &imagescan.Scan{Ref: ref, Findings: fs}, nil
}

type fakeRegistry struct{}

func (fakeRegistry) Digest(context.Context, string) (string, error) {
	return "sha256:" + strings.Repeat("2", 64), nil
}
func (fakeRegistry) Tags(context.Context, string) ([]string, error) { return nil, nil }

type fakeEOL struct{}

func (fakeEOL) Cycles(context.Context, string) ([]imagescan.Cycle, error) {
	return nil, errors.New("unavailable")
}

func TestScan(t *testing.T) {
	old := "nginx:stable-alpine@sha256:" + strings.Repeat("1", 64)
	brotli := imagescan.Finding{Package: "brotli-libs", Source: "brotli", Type: imagescan.TypeOS, Installed: "1.2.0-r1", Fixed: []string{"1.2.1-r0"}, IDs: []string{"ALPINE-CVE-2026-111497"}, Aliases: []string{"CVE-2026-111497"}, Score: 9.2}
	d := &deps{
		files: fakeFiles{
			"mock-ui/.github/workflows/release.yml": "jobs:\n  r:\n    uses: compliance-framework/workflows/.github/workflows/release-ui.yml@abc\n",
			"mock-ui/Dockerfile":                    "FROM node:22 AS build\nFROM " + old + " AS production\nFROM production\n",
			"mock-agent-action/Dockerfile":          "FROM debian:bookworm-slim\n",
		},
		scanner: fakeScanner{
			old: {brotli},
			"nginx:stable-alpine@sha256:" + strings.Repeat("2", 64): nil,
			"node:22":              nil,
			"debian:bookworm-slim": nil,
			"ghcr.io/compliance-framework/mock-ui:main": {brotli},
			// mock-ui:latest is missing: a note, not an error.
		},
		registry: fakeRegistry{},
		eol:      fakeEOL{},
	}
	dir := t.TempDir()
	overrides, summary := filepath.Join(dir, "overrides.json"), filepath.Join(dir, "summary.md")
	var out bytes.Buffer
	err := run(context.Background(), []string{"scan", "--manifest", mock, "--repos", "mock-ui,mock-agent-action,mock-plugin-1", "--overrides", overrides, "--summary", summary}, nil, &out, d)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	data, err := os.ReadFile(overrides)
	if err != nil {
		t.Fatal(err)
	}
	// What image-scan writes, renovate-config accepts.
	rules, err := renovate.ParseOverrides(data, "compliance-framework", []string{"mock-ui", "mock-agent-action", "mock-plugin-1"})
	if err != nil || len(rules) != 1 || !strings.Contains(string(data), `"compliance-framework/mock-ui"`) || !strings.Contains(string(data), "CVE-2026-111497") {
		t.Errorf("overrides (%v, %d rules):\n%s", err, len(rules), data)
	}
	md, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## mock-ui", "## mock-agent-action",
		"| `" + old + "` | Dockerfile, final stage production | 1 | 1 | 1 |",
		"| `ghcr.io/compliance-framework/mock-ui:main` | published | brotli-libs | os | 1.2.0-r1 | 1.2.1-r0 | CRITICAL | CVE-2026-111497 |",
		"published image ghcr.io/compliance-framework/mock-ui:latest not scanned",
		"(rule 1)",
	} {
		if !strings.Contains(string(md), want) {
			t.Errorf("summary has no %q:\n%s", want, md)
		}
	}
	if strings.Contains(string(md), "mock-plugin-1") {
		t.Errorf("summary covers a plugin repo:\n%s", md)
	}

	// A base image that can't be scanned fails the run, after the outputs are written.
	delete(d.scanner.(fakeScanner), "debian:bookworm-slim")
	if err := os.Remove(overrides); err != nil {
		t.Fatal(err)
	}
	err = run(context.Background(), []string{"scan", "--manifest", mock, "--repos", "mock-ui,mock-agent-action", "--overrides", overrides}, nil, &bytes.Buffer{}, d)
	if err == nil || !strings.Contains(err.Error(), "mock-agent-action") {
		t.Errorf("err = %v, want one naming mock-agent-action", err)
	}
	if data, err := os.ReadFile(overrides); err != nil || !strings.Contains(string(data), "mock-ui") {
		t.Errorf("overrides not written on failure: %v\n%s", err, data)
	}
}

func TestRunErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{nil, "usage"},
		{[]string{"check"}, "usage"},
		{[]string{"list", "extra"}, "unexpected arguments"},
		{[]string{"scan", "--manifest", mock}, "--overrides is required"},
		{[]string{"scan", "--manifest", mock, "--overrides", "x"}, "GH_TOKEN is not set"},
	} {
		err := run(context.Background(), tc.args, func(string) string { return "" }, &bytes.Buffer{}, nil)
		if err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%v: err = %v, want one containing %q", tc.args, err, tc.err)
		}
	}
}
