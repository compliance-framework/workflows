package bump

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/manifest"
)

var update = flag.Bool("update", false, "rewrite the testdata/*.golden files")

// copyDir copies testdata/<fixture> into a temp dir.
func copyDir(t *testing.T, fixture string) string {
	t.Helper()
	src, dst := filepath.Join("testdata", fixture), t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, _ := d.Info()
		return os.WriteFile(filepath.Join(dst, rel), data, info.Mode())
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// recorder runs bash for real (the ui sync script) and records every command.
type recorder struct{ cmds []string }

func (r *recorder) run(ctx context.Context, dir, name string, args ...string) error {
	r.cmds = append(r.cmds, strings.Join(append([]string{name}, args...), " "))
	if name != "bash" {
		return nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

func describe(p Plan) (changes, conflicts, upToDate []string) {
	for _, c := range p.Changes {
		changes = append(changes, fmt.Sprintf("%s %s %s: %s -> %s", c.Updater, c.File, c.Key, c.Current, c.To))
	}
	for _, c := range p.Conflicts {
		conflicts = append(conflicts, fmt.Sprintf("%s %s: %s", c.File, c.Key, c.Reason))
	}
	for _, r := range p.UpToDate {
		upToDate = append(upToDate, fmt.Sprintf("%s %s: %s", r.File, r.Key, r.Current))
	}
	return
}

// Shared-workflow pins in the fixtures.
const (
	wf    = "compliance-framework/workflows/.github/workflows/"
	sha10 = "0123456789abcdef0123456789abcdef01234567"
	sha11 = "89abcdef0123456789abcdef0123456789abcdef"
	sha20 = "fedcba9876543210fedcba9876543210fedcba98"
)

func TestUpdaters(t *testing.T) {
	pseudoTime := time.Date(2026, 10, 7, 17, 3, 2, 0, time.UTC)
	tagTimes := map[string]time.Time{"mock-api v0.1.0": pseudoTime.Add(time.Hour), "mock-api v0.0.1": pseudoTime.Add(-time.Hour)}
	tagTime := func(dep, tag string) (time.Time, error) { return tagTimes[dep+" "+tag], nil }
	tests := []struct {
		fixture       string
		cfg           Config
		targets       map[string]string
		changes       []string
		conflicts     []string
		upToDate      []string
		cmds          []string
		golden        bool // compare changed files with testdata/<fixture>.golden
		reapplyStable bool // a second scan of the result plans nothing
	}{
		{
			fixture: "service",
			targets: map[string]string{"mock-api": "v0.1.0", "mock-gooci": "v0.1.0", "agent": "v0.9.0", "api": "v0.22.0"},
			changes: []string{"go.mod go.mod github.com/compliance-framework/mock-api: v0.0.0-20261007170302-d0c48b16df4b -> v0.1.0"},
			conflicts: []string{"go.mod github.com/compliance-framework/mock-gooci: " +
				"pinned v0.2.0 is newer than v0.1.0; ccf-bump never downgrades"},
			upToDate: []string{"go.mod github.com/compliance-framework/agent: v0.9.0"},
			cmds:     []string{"go get github.com/compliance-framework/mock-api@v0.1.0", "go mod tidy"},
		},
		{
			fixture: "service",
			targets: map[string]string{"mock-api": "v0.0.1"},
			conflicts: []string{"go.mod github.com/compliance-framework/mock-api: pinned pseudo-version " +
				"v0.0.0-20261007170302-d0c48b16df4b (commit 2026-10-07T17:03:02Z) is newer than v0.0.1 " +
				"(commit 2026-10-07T16:03:02Z); ccf-bump never downgrades"},
		},
		{
			fixture: "plugin",
			targets: map[string]string{"mock-gooci": "v0.3.0", "gooci": "v0.0.7", DepWorkflows: "v1"},
			changes: []string{
				"go install .github/workflows/release.yml github.com/compliance-framework/mock-gooci/cmd/mock-gooci: v0.1.0 -> v0.3.0",
				"workflow ref .github/workflows/ci.yaml compliance-framework/workflows/.github/workflows/ci-go-plugin.yml: 0123456789abcdef0123456789abcdef01234567 -> v1",
				"workflow ref .github/workflows/ci.yaml compliance-framework/workflows/.github/workflows/release-checks.yml: v1.2.0 -> v1",
				"workflow ref .github/workflows/release.yml compliance-framework/workflows/.github/workflows/release-go-plugin.yml: ra/some-branch -> v1",
			},
			upToDate:      []string{".github/workflows/release.yml github.com/compliance-framework/gooci: v0.0.7"},
			golden:        true,
			reapplyStable: true,
		},
		{
			// A release: SHA pins with a version comment; main, tags, older SHAs and a SHA without
			// comment move, and the moved jobs lose their workflows-ref input.
			fixture: "workflows",
			targets: map[string]string{DepWorkflows: WorkflowsPin(sha11, "v1.1.0")},
			changes: []string{
				"workflow ref .github/workflows/ci.yml " + wf + "ci-go-service.yml: main -> " + sha11 + " # v1.1.0",
				"workflow ref .github/workflows/ci.yml " + wf + "release-checks.yml: " + sha10 + " -> " + sha11 + " # v1.1.0",
				"workflow ref .github/workflows/ci.yml " + wf + "notify-failure.yml: main -> " + sha11 + " # v1.1.0",
				"workflow ref .github/workflows/release.yml " + wf + "release-go-image.yml: v1.0.0 -> " + sha11 + " # v1.1.0",
				"workflow ref .github/workflows/release.yml " + wf + "cut-prerelease.yml: " + sha11 + " -> " + sha11 + " # v1.1.0",
				"workflow ref .github/workflows/release.yml " + wf + "notify-failure.yml: v1 -> " + sha11 + " # v1.1.0",
			},
			conflicts: []string{
				".github/workflows/ci.yml " + wf + "release-checks.yml: pinned " + sha10 + " # v1.2.0 is newer than v1.1.0; ccf-bump never downgrades",
				".github/workflows/release.yml " + wf + "release-checks.yml: pinned to ra/try-something, not main, a commit SHA or a release: a human decides",
			},
			upToDate:      []string{".github/workflows/release.yml " + wf + "preview.yml: " + sha11},
			golden:        true,
			reapplyStable: true,
		},
		{
			// A new major is a human's call: only main and a SHA of unknown version move.
			fixture: "workflows",
			targets: map[string]string{DepWorkflows: WorkflowsPin(sha20, "v2.0.0")},
			changes: []string{
				"workflow ref .github/workflows/ci.yml " + wf + "ci-go-service.yml: main -> " + sha20 + " # v2.0.0",
				"workflow ref .github/workflows/ci.yml " + wf + "notify-failure.yml: main -> " + sha20 + " # v2.0.0",
				"workflow ref .github/workflows/release.yml " + wf + "cut-prerelease.yml: " + sha11 + " -> " + sha20 + " # v2.0.0",
			},
			conflicts: []string{
				".github/workflows/ci.yml " + wf + "release-checks.yml: pinned " + sha10 + " # v1.0.0 is major v1, the latest release is v2.0.0: moving to another major is a human's call",
				".github/workflows/ci.yml " + wf + "release-checks.yml: pinned " + sha10 + " # v1.2.0 is major v1, the latest release is v2.0.0: moving to another major is a human's call",
				".github/workflows/release.yml " + wf + "release-go-image.yml: pinned v1.0.0 is major v1, the latest release is v2.0.0: moving to another major is a human's call",
				".github/workflows/release.yml " + wf + "preview.yml: pinned " + sha11 + " # v1.1.0 is major v1, the latest release is v2.0.0: moving to another major is a human's call",
				".github/workflows/release.yml " + wf + "release-checks.yml: pinned to ra/try-something, not main, a commit SHA or a release: a human decides",
				".github/workflows/release.yml " + wf + "notify-failure.yml: pinned v1 is major v1, the latest release is v2.0.0: moving to another major is a human's call",
			},
		},
		{
			// Never downgrade a pin to a later release.
			fixture: "workflows",
			targets: map[string]string{DepWorkflows: WorkflowsPin(sha10, "v1.0.0")},
			changes: []string{
				"workflow ref .github/workflows/ci.yml " + wf + "ci-go-service.yml: main -> " + sha10 + " # v1.0.0",
				"workflow ref .github/workflows/ci.yml " + wf + "notify-failure.yml: main -> " + sha10 + " # v1.0.0",
				"workflow ref .github/workflows/release.yml " + wf + "release-go-image.yml: v1.0.0 -> " + sha10 + " # v1.0.0",
				"workflow ref .github/workflows/release.yml " + wf + "cut-prerelease.yml: " + sha11 + " -> " + sha10 + " # v1.0.0",
				"workflow ref .github/workflows/release.yml " + wf + "notify-failure.yml: v1 -> " + sha10 + " # v1.0.0",
				"workflow ref .github/workflows/ci.yml " + wf + "release-checks.yml: " + sha10 + " -> " + sha10 + " # v1.0.0", // a wrong comment
			},
			conflicts: []string{
				".github/workflows/release.yml " + wf + "preview.yml: pinned " + sha11 + " # v1.1.0 is newer than v1.0.0; ccf-bump never downgrades",
				".github/workflows/release.yml " + wf + "release-checks.yml: pinned to ra/try-something, not main, a commit SHA or a release: a human decides",
			},
			upToDate: []string{".github/workflows/ci.yml " + wf + "release-checks.yml: " + sha10},
		},
		{
			fixture:       "action",
			cfg:           Config{Source: "mock-agent"},
			targets:       map[string]string{"mock-agent": "v0.1.0"},
			changes:       []string{"Dockerfile Dockerfile ghcr.io/compliance-framework/mock-agent: alpine:3.20 -> v0.1.0"},
			golden:        true,
			reapplyStable: true,
		},
		{
			fixture: "policies",
			cfg:     Config{OPASource: "mock-agent"},
			targets: map[string]string{DepOPA: "v1.15.0", DepWorkflows: "v1"},
			changes: []string{
				"opa-version .github/workflows/preview.yml opa-version: 1.10.0 -> v1.15.0",
				"opa-version .github/workflows/release.yml opa-version: 1.14.1 -> v1.15.0",
			},
			conflicts: []string{".github/workflows/preview.yml opa-version: pinned 1.16.0 is newer than v1.15.0; ccf-bump never downgrades"},
			upToDate: []string{
				".github/workflows/preview.yml compliance-framework/workflows/.github/workflows/preview.yml: v1",
				".github/workflows/preview.yml compliance-framework/workflows/.github/workflows/preview.yml: v1",
				".github/workflows/preview.yml compliance-framework/workflows/.github/workflows/preview.yml: v1",
				".github/workflows/release.yml compliance-framework/workflows/.github/workflows/release-policies.yml: v1",
			},
			golden:        true,
			reapplyStable: true,
		},
		{
			fixture:       "ui",
			cfg:           Config{UIDep: "mock-api"},
			targets:       map[string]string{"mock-api": "v0.1.0"},
			changes:       []string{"ui sync scripts/sync-mock-api-version.sh scripts/sync-mock-api-version.sh: v0.0.0 -> v0.1.0"},
			cmds:          []string{"bash scripts/sync-mock-api-version.sh v0.1.0"},
			golden:        true,
			reapplyStable: true,
		},
		{
			fixture: "helm",
			targets: map[string]string{"mock-api": "v0.1.0", "mock-ui": "v0.1.0", "mock-agent": "v0.2.0", "api": "v0.18.0", "ui": "v2.10.1"},
			changes: []string{
				"helm charts/ccf-app/values.yaml api.image.tag: 0.17.1 -> v0.18.0",
				"helm charts/ccf-app/Chart.yaml appVersion: 0.17.1 -> v0.18.0",
				"helm charts/mock-agent/Chart.yaml appVersion: 0.0.0 -> v0.2.0",
				"helm charts/mock-app/values.yaml ui.image.tag: 0.0.0 -> v0.1.0",
				"helm charts/mock-app/Chart.yaml appVersion: 0.0.0 -> v0.1.0",
			},
			upToDate:      []string{"charts/ccf-app/values.yaml ui.image.tag: 2.10.1"},
			golden:        true,
			reapplyStable: true,
		},
	}
	for i, tc := range tests {
		t.Run(fmt.Sprintf("%d-%s", i, tc.fixture), func(t *testing.T) {
			dir := copyDir(t, tc.fixture)
			rec := &recorder{}
			cfg := tc.cfg
			cfg.Owner, cfg.Run = "compliance-framework", rec.run
			us := Updaters(cfg)
			refs, err := ScanAll(dir, us)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Decide(refs, tc.targets, tagTime)
			if err != nil {
				t.Fatal(err)
			}
			changes, conflicts, upToDate := describe(plan)
			check(t, "changes", changes, tc.changes)
			check(t, "conflicts", conflicts, tc.conflicts)
			check(t, "up to date", upToDate, tc.upToDate)
			if err := ApplyAll(context.Background(), dir, us, plan.Changes); err != nil {
				t.Fatal(err)
			}
			check(t, "commands", rec.cmds, tc.cmds)
			if tc.golden {
				compareGolden(t, dir, tc.fixture)
			}
			if tc.reapplyStable {
				refs, err := ScanAll(dir, us)
				if err != nil {
					t.Fatal(err)
				}
				again, err := Decide(refs, tc.targets, tagTime)
				if err != nil {
					t.Fatal(err)
				}
				if len(again.Changes) > 0 {
					t.Errorf("second run plans changes: %v", again.Changes)
				}
			}
		})
	}
}

func check(t *testing.T, what string, got, want []string) {
	t.Helper()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// compareGolden checks each file of dir against testdata/<fixture>.golden/<file> when there is one,
// and otherwise against the unchanged fixture. With -update it writes the changed files there.
func compareGolden(t *testing.T, dir, fixture string) {
	t.Helper()
	golden := filepath.Join("testdata", fixture+".golden")
	if *update {
		if err := os.RemoveAll(golden); err != nil {
			t.Fatal(err)
		}
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		got, _ := os.ReadFile(p)
		orig, _ := os.ReadFile(filepath.Join("testdata", fixture, rel))
		if *update {
			if string(got) == string(orig) {
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(filepath.Join(golden, rel)), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(golden, rel), got, 0o644)
		}
		want, err := os.ReadFile(filepath.Join(golden, rel))
		if os.IsNotExist(err) {
			want, err = orig, nil
		}
		if err != nil {
			return err
		}
		if string(got) != string(want) {
			t.Errorf("%s: unexpected content:\n%s", rel, got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMajor(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		major    bool
	}{
		{"v0.1.0", "v0.2.0", false},
		{"0.17.1", "v0.18.0", false},
		{"v0.0.0-20261007170302-d0c48b16df4b", "v0.1.0", false},
		{"v1.9.0", "v2.0.0", true},
		{"alpine:3.20", "v0.1.0", true},
		{"", "v0.1.0", true},
		{"ra/some-branch", "v1", true},
	} {
		if got := (Change{Ref: Ref{Current: tc.from}, To: tc.to}).Major(); got != tc.major {
			t.Errorf("Major(%s -> %s) = %v, want %v", tc.from, tc.to, got, tc.major)
		}
	}
	for _, tc := range []struct {
		from, version, to string
		major             bool
	}{
		{"main", "", WorkflowsPin(sha10, "v1.0.0"), true},
		{sha10, "", WorkflowsPin(sha11, "v1.1.0"), true}, // version unknown
		{sha10, "v1.0.0", WorkflowsPin(sha11, "v1.1.0"), false},
		{sha11, "", WorkflowsPin(sha11, "v1.1.0"), false}, // only the comment
		{"v1", "", WorkflowsPin(sha11, "v1.1.0"), false},
		{"v1.0.0", "", WorkflowsPin(sha20, "v2.0.0"), true},
		{sha10, "v1.0.0", "main", true}, // a ref given by hand
	} {
		c := Change{Ref: Ref{Dep: DepWorkflows, Current: tc.from, Version: tc.version}, To: tc.to}
		if got := c.Major(); got != tc.major {
			t.Errorf("Major(workflows %s -> %s) = %v, want %v", c.Pinned(), tc.to, got, tc.major)
		}
	}
}

func TestNeedsHuman(t *testing.T) {
	app := func(file, key, from, to string) Change {
		return Change{Ref: Ref{Updater: helm{}.Name(), Dep: "api", Key: key, Current: from, File: file}, To: to}
	}
	tag := func(from, to string) Change { return app("charts/ccf/values.yaml", "api.image.tag", from, to) }
	appVersion := func(from, to string) Change { return app("charts/ccf/Chart.yaml", "appVersion", from, to) }
	wf := func(from, version, to string) Change {
		return Change{Ref: Ref{Updater: workflowRef{}.Name(), Dep: DepWorkflows, Current: from, Version: version}, To: to}
	}
	gomod := Change{Ref: Ref{Updater: goMod{}.Name(), Dep: "api", Current: "v0.21.0"}, To: "v0.22.0"}
	const helmRepo, serviceRepo = manifest.KindHelm, manifest.KindGoService
	for name, tc := range map[string]struct {
		c    Change
		kind manifest.Kind
		want string
	}{
		"helm 0.x minor tag":            {tag("0.21.0", "v0.22.0"), helmRepo, HelmAppReason},
		"helm 0.x minor appVersion":     {appVersion("0.21.0", "v0.22.0"), helmRepo, HelmAppReason},
		"helm 0.x patch tag":            {tag("0.21.0", "v0.21.1"), helmRepo, ""},
		"helm 0.x patch appVersion":     {appVersion("0.21.0", "v0.21.1"), helmRepo, ""},
		"helm 1.x+ minor":               {tag("2.12.1", "v2.13.0"), helmRepo, HelmAppReason},
		"helm 1.x+ patch":               {appVersion("2.12.1", "v2.12.2"), helmRepo, ""},
		"helm major":                    {tag("2.12.1", "v3.0.0"), helmRepo, HelmAppReason},
		"helm 0.x to 1.0":               {appVersion("0.21.0", "v1.0.0"), helmRepo, HelmAppReason},
		"helm tag not a version":        {tag("latest", "v0.22.0"), helmRepo, HelmAppReason},
		"helm workflows pin same major": {wf(sha10, "v1.0.0", WorkflowsPin(sha11, "v1.1.0")), helmRepo, ""},
		"helm workflows pin major":      {wf("v1.0.0", "", WorkflowsPin(sha20, "v2.0.0")), helmRepo, MajorReason},
		"go-service minor":              {gomod, serviceRepo, ""},
		"go-service app minor":          {tag("0.21.0", "v0.22.0"), serviceRepo, ""}, // helm refs outside a helm repo
		"go-service major":              {Change{Ref: Ref{Updater: goMod{}.Name(), Current: "v1.9.0"}, To: "v2.0.0"}, serviceRepo, MajorReason},
	} {
		if got := tc.c.HoldReason(tc.kind); got != tc.want {
			t.Errorf("%s: HoldReason(%s) = %q, want %q", name, tc.kind, got, tc.want)
		}
		if got := tc.c.NeedsHuman(tc.kind); got != (tc.want != "") {
			t.Errorf("%s: NeedsHuman(%s) = %v", name, tc.kind, got)
		}
	}
	p := Plan{Changes: []Change{tag("0.21.0", "v0.21.1"), gomod, appVersion("0.21.0", "v0.22.0"), tag("0.21.0", "v0.22.0"),
		wf("v1.0.0", "", WorkflowsPin(sha20, "v2.0.0"))}}
	if got, want := p.HoldReasons(helmRepo), []string{HelmAppReason, MajorReason}; !slices.Equal(got, want) {
		t.Errorf("HoldReasons(helm) = %q, want %q", got, want)
	}
	if got := (Plan{Changes: []Change{tag("0.21.0", "v0.21.1"), gomod}}).HoldReasons(helmRepo); got != nil {
		t.Errorf("HoldReasons(helm) of patches = %q, want none", got)
	}
}

func TestDecideNoTarget(t *testing.T) {
	p, err := Decide([]Ref{{Dep: "mock-api", Current: "v0.1.0"}, {Dep: DepWorkflows, Current: "main"}}, map[string]string{"mock-api": ""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.NoTarget) != 2 || len(p.Changes) != 0 {
		t.Errorf("want 2 refs without a target, got %+v", p)
	}
}
