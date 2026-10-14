package bump

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
)

func TestTemplated(t *testing.T) {
	for v, want := range map[string]bool{
		"${ref}": true, "$VERSION": true, "${{ inputs.version }}": true, "$(cat VERSION)": true,
		"{{ .Ref }}": true, "%s": true, "<version>": true, "v${MAJOR}.0.0": true,
		"v0.21.0": false, "0.21.0": false, "main": false, "latest": false, "alpine:3.20": false,
		"ra/some-branch": false, sha10: false, "v0.0.0-20261007170302-d0c48b16df4b": false, "": false,
	} {
		if got := Templated(v); got != want {
			t.Errorf("Templated(%q) = %v, want %v", v, got, want)
		}
	}
}

// TestScanSkipsTemplated: a ref filled in when the file is used is not a pin; real pins that are
// not versions (a branch, latest, an image tag) still are.
func TestScanSkipsTemplated(t *testing.T) {
	dir := t.TempDir()
	const gooci = "github.com/compliance-framework/mock-gooci/cmd/mock-gooci"
	files := map[string]string{
		".github/workflows/release.yml": `name: release
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: go install ` + gooci + `@${{ inputs.gooci }}
      - run: go install ` + gooci + `@$GOOCI_VERSION
      - run: go install ` + gooci + `@${GOOCI_VERSION}
      - run: printf 'go install ` + gooci + `@%s' "$v"
      - run: echo "go install ` + gooci + `@<version>"
      - run: go install ` + gooci + `@latest
  templated:
    uses: compliance-framework/workflows/.github/workflows/ci.yml@{{ .WorkflowsRef }}
  branch:
    uses: compliance-framework/workflows/.github/workflows/ci.yml@main
`,
		"Dockerfile":            "FROM ghcr.io/compliance-framework/mock-agent:${AGENT_VERSION} AS source\n",
		"charts/app/Chart.yaml": "apiVersion: v2\nname: app\nversion: 0.1.0\nappVersion: 0.17.1\n",
		"charts/app/values.yaml": "api:\n  image:\n    repository: ghcr.io/compliance-framework/api\n    tag: \"{{ .Values.global.tag }}\"\n" +
			"ui:\n  image:\n    repository: ghcr.io/compliance-framework/ui\n    tag: 2.10.1\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := ScanAll(dir, Updaters(Config{Owner: "compliance-framework", Source: "mock-agent"}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range refs {
		got = append(got, fmt.Sprintf("%s %s@%s", r.Updater, r.Key, r.Current))
	}
	check(t, "refs", got, []string{
		"go install " + gooci + "@latest",
		"workflow ref compliance-framework/workflows/.github/workflows/ci.yml@main",
		"helm ui.image.tag@2.10.1",
	})
}

func TestSettle(t *testing.T) {
	gomod := Ref{Updater: goMod{}.Name(), Dep: "api", Key: "github.com/compliance-framework/api", Current: "v0.20.0", File: "go.mod"}
	pin := Ref{Updater: workflowRef{}.Name(), Dep: DepWorkflows, Key: wf + "ci.yml", Current: sha10, Version: "v1.0.0", File: ".github/workflows/ci.yml"}
	conformance := Ref{Updater: uiScript{}.Name(), Dep: "api", Key: "scripts/sync-agentconfig-conformance.sh", File: "src/fixture.json"}
	mockUI := Ref{Updater: uiScript{}.Name(), Dep: "api", Key: "scripts/sync-mock-api-version.sh", File: "src/api-version.json"}
	image := Ref{Updater: dockerfile{}.Name(), Dep: "agent", Key: "ghcr.io/compliance-framework/agent", Current: "alpine:3.20", File: "Dockerfile"}
	gone := Ref{Updater: helm{}.Name(), Dep: "api", Key: "appVersion", Current: "0.20.0", File: "charts/ccf/Chart.yaml"}
	release := WorkflowsPin(sha11, "v1.1.0")
	p := Plan{
		Changes: []Change{
			{Ref: gomod, To: "v0.21.0"},
			{Ref: pin, To: release}, {Ref: pin, To: release}, // two jobs pin it, a same-major move; one moved
			{Ref: conformance, To: "v0.21.0"}, // the script left its file as it was
			{Ref: mockUI, To: "v0.21.0"},      // version unknown, file written
			{Ref: image, To: "v0.21.0"},       // not a version: would be held; the edit did nothing
			{Ref: gone, To: "v0.21.0"},        // not found again, but its file is unchanged
		},
		UpToDate: []Ref{{Dep: "ui", Current: "v2.10.1"}},
	}
	movedPin := pin
	movedPin.Current, movedPin.Version = sha11, "v1.1.0"
	movedMod := gomod
	movedMod.Current = "v0.21.0"
	after := []Ref{movedMod, pin, movedPin, conformance, {Updater: mockUI.Updater, Dep: "api", Key: mockUI.Key, Current: "v0.21.0", File: mockUI.File}, image}
	changed := map[string]bool{"go.mod": true, "go.sum": true, ".github/workflows/ci.yml": true, "src/api-version.json": true}

	if got := p.HoldReasons(manifest.KindUI); !slices.Equal(got, []string{MajorReason}) {
		t.Fatalf("planned HoldReasons = %q", got)
	}
	settled, unchanged, dropped := p.Settle(after, func(f string) bool { return changed[f] })
	describeAll := func(cs []Change) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.File+" "+c.Key+" "+c.Current)
		}
		return out
	}
	check(t, "applied", describeAll(settled.Changes), []string{
		"go.mod github.com/compliance-framework/api v0.20.0",
		".github/workflows/ci.yml " + wf + "ci.yml " + sha10,
		"src/api-version.json scripts/sync-mock-api-version.sh ",
	})
	check(t, "unchanged", describeAll(unchanged), []string{"src/fixture.json scripts/sync-agentconfig-conformance.sh "})
	check(t, "dropped", describeAll(dropped), []string{
		".github/workflows/ci.yml " + wf + "ci.yml " + sha10,
		"Dockerfile ghcr.io/compliance-framework/agent alpine:3.20",
		"charts/ccf/Chart.yaml appVersion 0.20.0",
	})
	if len(settled.UpToDate) != 2 || settled.UpToDate[1] != conformance {
		t.Errorf("UpToDate = %+v, want the conformance pin added", settled.UpToDate)
	}
	if len(p.UpToDate) != 1 {
		t.Errorf("Settle changed the plan it was called on: %+v", p.UpToDate)
	}
	// mockUI's unknown version still holds the PR; the dropped image would have too.
	if got := settled.HoldReasons(manifest.KindUI); !slices.Equal(got, []string{MajorReason}) {
		t.Errorf("settled HoldReasons = %q", got)
	}
	settled.Changes = slices.DeleteFunc(settled.Changes, func(c Change) bool { return c.Ref == mockUI })
	if got := settled.HoldReasons(manifest.KindUI); got != nil {
		t.Errorf("HoldReasons without unapplied changes = %q, want none", got)
	}
}
