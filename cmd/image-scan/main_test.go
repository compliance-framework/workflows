package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/bump"
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
	pm := ""
	switch {
	case strings.HasPrefix(ref, "debian"):
		pm = imagescan.APT
	case strings.HasPrefix(ref, "nginx"):
		pm = imagescan.APK
	}
	return &imagescan.Scan{Ref: ref, Findings: fs, PackageManager: pm}, nil
}

// fakePatchGH is a repo with no image-scan/os-patches branch or PR, whose default branch's head
// commit "head" holds files; it records the writes.
type fakePatchGH struct {
	files fakeFiles
	calls []string
}

func (f *fakePatchGH) DefaultBranch(context.Context, string) (string, error) { return "main", nil }
func (f *fakePatchGH) BranchHead(_ context.Context, _, branch string) (string, error) {
	if branch == "main" {
		return "head", nil
	}
	return "", nil
}
func (f *fakePatchGH) BranchAuthors(context.Context, string, string, string) ([]string, error) {
	return nil, errors.New("no branch")
}
func (f *fakePatchGH) File(_ context.Context, repo, ref, path string) ([]byte, error) {
	c, ok := f.files[repo+"/"+path]
	if ref != "head" || !ok {
		return nil, &bump.StatusError{Code: http.StatusNotFound}
	}
	return []byte(c), nil
}
func (f *fakePatchGH) CommitTree(context.Context, string, string) (string, []string, error) {
	return "tree0", nil, nil
}
func (f *fakePatchGH) CreateTree(_ context.Context, repo, base string, files map[string][]byte) (string, error) {
	for p, c := range files {
		f.calls = append(f.calls, fmt.Sprintf("tree %s %s %s:\n%s", repo, base, p, c))
	}
	return "tree1", nil
}
func (f *fakePatchGH) CreateCommit(_ context.Context, repo, message, tree, parent string, author bump.Identity) (string, error) {
	f.calls = append(f.calls, fmt.Sprintf("commit %s %s on %s by %s <%s>: %s", repo, tree, parent, author.Name, author.Email, strings.SplitN(message, "\n", 2)[0]))
	return "c1", nil
}
func (f *fakePatchGH) SetBranch(_ context.Context, repo, branch, sha string, create bool) error {
	f.calls = append(f.calls, fmt.Sprintf("branch %s %s %s create=%v", repo, branch, sha, create))
	return nil
}
func (f *fakePatchGH) OpenPR(context.Context, string, string) (*bump.PR, error) { return nil, nil }
func (f *fakePatchGH) PRCommitAuthors(context.Context, string, int) ([]string, error) {
	return nil, errors.New("no PR")
}
func (f *fakePatchGH) CreatePR(_ context.Context, repo, base, head, title, _ string) (*bump.PR, error) {
	f.calls = append(f.calls, fmt.Sprintf("pr %s %s<-%s %q", repo, base, head, title))
	return &bump.PR{Number: 7}, nil
}
func (f *fakePatchGH) UpdatePR(context.Context, string, int, string, string) error {
	return errors.New("unexpected update")
}
func (f *fakePatchGH) AddLabel(_ context.Context, repo string, n int, l bump.Label) error {
	f.calls = append(f.calls, fmt.Sprintf("label %s#%d %s", repo, n, l.Name))
	return nil
}
func (f *fakePatchGH) ClosePR(context.Context, string, int, string) error {
	return errors.New("unexpected close")
}
func (f *fakePatchGH) DeleteBranch(context.Context, string, string) error {
	return errors.New("unexpected delete")
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
	libc := imagescan.Finding{Package: "libc6", Source: "glibc", Type: imagescan.TypeOS, Installed: "2.36-9+deb12u13", Fixed: []string{"2.36-9+deb12u14"}, IDs: []string{"DEBIAN-CVE-2025-15281"}, Aliases: []string{"CVE-2025-15281"}, Score: 7.5}
	gh := &fakePatchGH{}
	d := &deps{
		patches: gh,
		files: fakeFiles{
			"mock-ui/.github/workflows/release.yml": "jobs:\n  r:\n    uses: compliance-framework/workflows/.github/workflows/release-ui.yml@abc\n",
			"mock-ui/Dockerfile":                    "FROM node:22 AS build\nFROM " + old + " AS production\nFROM production\n",
			"mock-agent-action/Dockerfile":          "FROM debian:bookworm-slim\n",
		},
		scanner: fakeScanner{
			old: {brotli},
			"nginx:stable-alpine@sha256:" + strings.Repeat("2", 64): nil,
			"node:22":              nil,
			"debian:bookworm-slim": {libc},
			"ghcr.io/compliance-framework/mock-ui:main": {brotli},
			// mock-ui:latest is missing: a note, not an error.
		},
		registry: fakeRegistry{},
		eol:      fakeEOL{},
	}
	dir := t.TempDir()
	overrides, summary := filepath.Join(dir, "overrides.json"), filepath.Join(dir, "summary.md")
	var out bytes.Buffer
	err := run(context.Background(), []string{"scan", "--manifest", mock, "--repos", "mock-ui,mock-agent-action,mock-plugin-1", "--overrides", overrides, "--summary", summary}, env(nil), &out, d)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	// A dry run plans the OS package patch of the agent action's Debian image and writes nothing;
	// the ui's nginx is fixed by rule 1 instead.
	if !strings.Contains(out.String(), `mock-agent-action: dry run: would push image-scan/os-patches and open or update "fix(deps): patch OS packages in debian:bookworm-slim (CVE-2025-15281)"`) ||
		!strings.Contains(out.String(), "+ RUN apt-get update && apt-get install -y --no-install-recommends --only-upgrade libc6 && rm -rf /var/lib/apt/lists/*") ||
		strings.Contains(out.String(), "mock-ui: dry run: would push") || len(gh.calls) > 0 {
		t.Errorf("dry run: writes %q; log:\n%s", gh.calls, out.String())
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
	err = run(context.Background(), []string{"scan", "--manifest", mock, "--repos", "mock-ui,mock-agent-action", "--overrides", overrides}, env(nil), &bytes.Buffer{}, d)
	if err == nil || !strings.Contains(err.Error(), "mock-agent-action") {
		t.Errorf("err = %v, want one naming mock-agent-action", err)
	}
	if data, err := os.ReadFile(overrides); err != nil || !strings.Contains(string(data), "mock-ui") {
		t.Errorf("overrides not written on failure: %v\n%s", err, data)
	}
}

// TestScanLive: a live run commits the patch as GIT_AUTHOR_NAME <GIT_AUTHOR_EMAIL> on a new
// image-scan/os-patches branch and opens its PR, labelled renovate:automerge.
func TestScanLive(t *testing.T) {
	libc := imagescan.Finding{Package: "libc6", Type: imagescan.TypeOS, Installed: "2.36-9+deb12u13", Fixed: []string{"2.36-9+deb12u14"}, IDs: []string{"DEBIAN-CVE-2025-15281"}, Aliases: []string{"CVE-2025-15281"}, Score: 7.5}
	files := fakeFiles{"mock-agent-action/Dockerfile": "FROM debian:bookworm-slim\nUSER app\n"}
	gh := &fakePatchGH{files: files}
	d := &deps{
		patches:  gh,
		files:    files,
		scanner:  fakeScanner{"debian:bookworm-slim": {libc}},
		registry: fakeRegistry{},
		eol:      fakeEOL{},
	}
	var out bytes.Buffer
	getenv := env(map[string]string{"GIT_AUTHOR_NAME": "ccf-release-bot[bot]", "GIT_AUTHOR_EMAIL": "1+ccf-release-bot[bot]@users.noreply.github.com"})
	args := []string{"scan", "--manifest", mock, "--repos", "mock-agent-action", "--dry-run", "off", "--overrides", filepath.Join(t.TempDir(), "o.json")}
	if err := run(context.Background(), args, getenv, &out, d); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	want := []string{
		"tree mock-agent-action tree0 Dockerfile:\nFROM debian:bookworm-slim\n" +
			"# image-scan:os-patches begin\n" +
			"# Managed by compliance-framework/workflows image-scan (docs/image-scan.md#os-package-patches); removed once the base image ships the fixes.\n" +
			"# hadolint ignore=DL3008\n" +
			"RUN apt-get update && apt-get install -y --no-install-recommends --only-upgrade libc6 && rm -rf /var/lib/apt/lists/*\n" +
			"# image-scan:os-patches end\nUSER app\n",
		"commit mock-agent-action tree1 on head by ccf-release-bot[bot] <1+ccf-release-bot[bot]@users.noreply.github.com>: fix(deps): patch OS packages in debian:bookworm-slim (CVE-2025-15281)",
		"branch mock-agent-action image-scan/os-patches c1 create=true",
		`pr mock-agent-action main<-image-scan/os-patches "fix(deps): patch OS packages in debian:bookworm-slim (CVE-2025-15281)"`,
		"label mock-agent-action#7 renovate:automerge",
	}
	if !slices.Equal(gh.calls, want) {
		t.Errorf("writes:\n%s\nwant:\n%s", strings.Join(gh.calls, "\n"), strings.Join(want, "\n"))
	}
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

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
