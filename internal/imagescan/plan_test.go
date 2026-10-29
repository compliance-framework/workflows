package imagescan

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/renovate"
)

const (
	oldDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

type fakeRegistry struct {
	digests map[string]string
	tags    map[string][]string
}

func (r fakeRegistry) Digest(_ context.Context, ref string) (string, error) {
	if d, ok := r.digests[ref]; ok {
		return d, nil
	}
	return "", errors.New("MANIFEST_UNKNOWN: " + ref)
}

func (r fakeRegistry) Tags(_ context.Context, name string) ([]string, error) {
	return r.tags[name], nil
}

// eolServer serves endoflife.date's API from testdata/endoflife/<product>.json, recorded; down
// makes every request fail.
func eolServer(t *testing.T, down bool) *EOL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		product, ok := strings.CutPrefix(strings.TrimSuffix(r.URL.Path, "/"), "/products/")
		data, err := os.ReadFile(filepath.Join("testdata", "endoflife", product+".json"))
		if down || !ok || err != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return &EOL{BaseURL: srv.URL}
}

// planImages are the recorded images plus the candidates the plan scans. node:22's current
// release is 22.23.4 (endoflife.date), which OSV has no advisory for; its packages are left out.
func planImages() map[string]fakeImage {
	images := map[string]fakeImage{
		"nginx:stable-alpine@" + oldDigest: spikeImages["nginx:stable-alpine"],
		"nginx:stable-alpine":              spikeImages["nginx:stable-alpine"],
		"node:20":                          spikeImages["node:20"],
		"node:22":                          {env: []string{"NODE_VERSION=22.23.4"}},
		"node:22-alpine":                   {env: []string{"NODE_VERSION=22.23.4"}},
		"node:20-alpine":                   {report: "", env: []string{"NODE_VERSION=20.20.2"}},
		// The rebuild of the tag: brotli-libs and curl fixed, so no findings.
		"nginx:stable-alpine@" + newDigest:         {env: []string{"NGINX_VERSION=1.30.1"}},
		"ghcr.io/compliance-framework/agent:0.9.3": spikeImages["nginx:stable-alpine"],
		"golang:1.26.1": spikeImages["nginx:stable-alpine"],
		"golang:1.26.2": {},
		// Third-party images with a Go binary built with Go 1.26.1 (agent's recorded report):
		// 1.0.0 also has nginx's Alpine packages, which 1.0.1 fixes but not the Go binary; 2.0.1
		// is rebuilt with a newer Go; 3.0.1 is not.
		"gotool:1.0.0": {report: "nginx_stable-alpine.json,agent.json"},
		"gotool:1.0.1": {report: "agent.json"},
		"gotool:2.0.0": {report: "agent.json"},
		"gotool:2.0.1": {},
		"gotool:3.0.0": {report: "agent.json"},
		"gotool:3.0.1": {report: "agent.json"},
	}
	return images
}

func TestPlan(t *testing.T) {
	reg := fakeRegistry{
		digests: map[string]string{"nginx:stable-alpine": newDigest},
		tags:    map[string][]string{"golang": {"1.26.0", "1.26.1", "1.26.2", "1.27.0"}, "gotool": {"1.0.0", "1.0.1", "2.0.0", "2.0.1", "3.0.0", "3.0.1"}},
	}
	type tgt struct {
		ref, stage string
		role       Role
	}
	for _, tc := range []struct {
		name      string
		targets   []tgt
		registry  fakeRegistry
		eolDown   bool
		rules     string // golden file under testdata/rules, "" for none
		notes     []string
		noteCount int
	}{
		{
			name:    "rule 1: the final image's tag was rebuilt with the fixes",
			targets: []tgt{{"node:22", "build", RoleBuild}, {"nginx:stable-alpine@" + oldDigest, "production", RoleFinal}},
			rules:   "rule1.golden.json",
			notes:   []string{"nginx:stable-alpine@sha256:2222222222222222222222222222222222222222222222222222222222222222 fixes CVE-2026-111497, CVE-2026-92392 (rule 1)"},
		},
		{
			name:     "rule 3: the tag was not rebuilt",
			targets:  []tgt{{"nginx:stable-alpine@" + oldDigest, "production", RoleFinal}},
			registry: fakeRegistry{digests: map[string]string{"nginx:stable-alpine": oldDigest}},
			notes:    []string{"the tag has not been rebuilt since the pinned digest; the distribution's fix waits for an upstream rebuild"},
		},
		{
			name:    "rule 3: an unpinned tag is already the newest image",
			targets: []tgt{{"nginx:stable-alpine", "production", RoleFinal}},
			notes:   []string{"the tag is not pinned by digest"},
		},
		{
			name:    "rule 1: the newest patch of an X.Y.Z tag",
			targets: []tgt{{"golang:1.26.1", "0", RoleFinal}},
			rules:   "rule1-patch.golden.json",
			notes:   []string{"golang:1.26.2 fixes"},
		},
		{
			name:    "rule 2: node 20 is end-of-life; node:22 fixes the runtime",
			targets: []tgt{{"node:20", "build", RoleBuild}},
			rules:   "rule2.golden.json",
			notes:   []string{"nodejs 20 is end-of-life; node:22 fixes CVE-2026-111497, CVE-2026-48617, CVE-2026-48937, CVE-2026-56846, CVE-2026-56848, CVE-2026-58043 (rule 2)"},
		},
		{
			name:    "rule 2 once for an image in two stages",
			targets: []tgt{{"node:20", "build", RoleBuild}, {"node:20", "final", RoleFinal}},
			rules:   "rule2.golden.json",
			notes:   []string{"(rule 2)"},
		},
		{
			name:    "rule 2 keeps the tag's suffix; an image without node findings isn't moved",
			targets: []tgt{{"node:20-alpine", "0", RoleFinal}},
			rules:   "rule2-alpine.golden.json",
			notes:   []string{"node:22-alpine fixes"},
		},
		{
			name:      "endoflife.date down: fail soft, then rule 3 for the runtime",
			targets:   []tgt{{"node:20", "0", RoleFinal}},
			eolDown:   true,
			notes:     []string{"endoflife.date nodejs unavailable, EOL rule skipped", "the tag is not pinned by digest"},
			noteCount: 2,
		},
		{
			name:    "rule 1: a Go binary rebuilt with a newer Go in the next patch",
			targets: []tgt{{"gotool:2.0.0", "0", RoleFinal}},
			rules:   "rule1-gobinary.golden.json",
			notes:   []string{"gotool:2.0.1 fixes CVE-2026-39821, CVE-2026-97032 (rule 1)"},
		},
		{
			name:    "rule 1: the OS fixes go ahead, the Go binary not yet rebuilt waits",
			targets: []tgt{{"gotool:1.0.0", "0", RoleFinal}},
			rules:   "rule1-mixed.golden.json",
			notes: []string{
				"gotool:1.0.1 fixes CVE-2026-111497, CVE-2026-92392 (rule 1)",
				"the candidate gotool:1.0.1 still has the Go standard library finding(s) CVE-2026-39821, CVE-2026-97032, in a Go binary of the image, which wait for an upstream rebuild with a newer Go",
			},
			noteCount: 2,
		},
		{
			name:      "no rule for a Go binary the next patch doesn't rebuild",
			targets:   []tgt{{"gotool:3.0.0", "0", RoleFinal}},
			notes:     []string{"the candidate gotool:3.0.1 still has the Go standard library finding(s) CVE-2026-39821, CVE-2026-97032"},
			noteCount: 1,
		},
		{
			name:    "internal images and build stages are only noted",
			targets: []tgt{{"ghcr.io/compliance-framework/agent:0.9.3", "source", RoleBuild}, {"golang:1.26.1", "builder", RoleBuild}},
			notes:   []string{"internal image, which ccf-bump moves", "in a build stage, which the image doesn't ship"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.registry.digests == nil && tc.registry.tags == nil {
				tc.registry = reg
			}
			s := newScanner(t, &fakeDocker{images: planImages()})
			var targets []Target
			for _, x := range tc.targets {
				ref, err := ParseRef(x.ref)
				if err != nil {
					t.Fatal(err)
				}
				scan, err := s.Scan(context.Background(), x.ref)
				if err != nil {
					t.Fatal(err)
				}
				targets = append(targets, Target{Dockerfile: "Dockerfile", Base: Base{Ref: ref, Stage: x.stage, Role: x.role}, Scan: scan})
			}
			p := &Planner{Owner: "compliance-framework", Scanner: s, Registry: tc.registry, EOL: eolServer(t, tc.eolDown)}
			pl := p.Plan(context.Background(), "ui", targets)
			rules, notes := pl.Rules, pl.Notes
			if (len(pl.Fixed) > 0) != strings.HasPrefix(tc.rules, "rule1") {
				t.Errorf("Fixed = %v, want the image rule 1 moves and nothing else", pl.Fixed)
			}
			if tc.rules == "" {
				if len(rules) != 0 {
					t.Errorf("rules = %v, want none", rules)
				}
			} else {
				golden(t, filepath.Join("testdata", "rules", tc.rules), rules)
			}
			for _, want := range tc.notes {
				if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, want) }) {
					t.Errorf("no note containing %q in:\n%s", want, strings.Join(notes, "\n"))
				}
			}
			if tc.noteCount > 0 && len(notes) != tc.noteCount {
				t.Errorf("%d notes, want %d:\n%s", len(notes), tc.noteCount, strings.Join(notes, "\n"))
			}
		})
	}
}

// Every rule the plan writes passes renovate-config's check of the overrides file, rule 1 and 2.
func TestRuleGoldensPassParseOverrides(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "rules", "*.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		rules, err := renovate.ParseOverrides(data, "compliance-framework", []string{"ui"})
		if err != nil || len(rules) == 0 {
			t.Errorf("%s: %d rules, %v", f, len(rules), err)
		}
		for _, r := range rules {
			switch {
			case r.AllowedVersions != "":
				kinds = append(kinds, "rule 2")
			case len(r.MatchUpdateTypes) > 0:
				kinds = append(kinds, "rule 1 "+r.MatchUpdateTypes[0])
			}
		}
	}
	for _, want := range []string{"rule 1 digest", "rule 1 patch", "rule 2"} {
		if !slices.Contains(kinds, want) {
			t.Errorf("no %s rule among the goldens (%v)", want, kinds)
		}
	}
}

func TestCycleRegex(t *testing.T) {
	re := cycleRegex("3.20")
	if re != `/^3\.20(\.|-|$)/` {
		t.Errorf("cycleRegex(3.20) = %s", re)
	}
}
