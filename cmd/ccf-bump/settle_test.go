package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/bump"
)

// conformanceScript is an offline stand-in for ui's scripts/sync-agentconfig-conformance.sh, with
// its url line as written (the ${ref} of compliance-framework/ui#355); it writes %q to the fixture
// instead of downloading it.
const conformanceScript = `#!/usr/bin/env bash
set -euo pipefail
ref="${1:-main}"
url="https://raw.githubusercontent.com/compliance-framework/api/${ref}/pkg/agentconfig/testdata/conformance.json"
fixture="src/utils/agent-config/__tests__/fixtures/agentconfig-conformance.json"
cd "$(dirname "$0")/.."
printf '%%s\n' %q > "$fixture"
echo "Copied api@${ref} ($url) to $fixture."
`

const (
	conformanceFixture = "src/utils/agent-config/__tests__/fixtures/agentconfig-conformance.json"
	mockScript         = "scripts/sync-mock-api-version.sh"
	mockVersionFile    = "src/api-version.json"
	// workflowsCI pins a shared workflow to an older release of the same major: an applied move
	// that is auto-merged.
	workflowsCI = "name: ci\non: pull_request\njobs:\n  ci:\n" +
		"    uses: compliance-framework/workflows/.github/workflows/ci-ui.yml@0123456789abcdef0123456789abcdef01234567 # v1.0.0\n"
)

// addRepo makes a local clone and a bare "GitHub" remote for repo with files, as setup does.
func addRepo(t *testing.T, root, repo string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "clones", repo)
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "init", "--quiet", "-b", "main")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "seed")
	git(t, root, "clone", "--quiet", "--bare", dir, filepath.Join(root, "remotes", repo+".git"))
}

// TestSettledPR: a bump PR's title, table and label follow only the changes its diff applies.
func TestSettledPR(t *testing.T) {
	const sha = "89abcdef0123456789abcdef0123456789abcdef"
	const repo = "mock-ui-2"
	workflowsOnly := `create mock-ui-2 main<-ccf-bump/sync-2026-10-08 "ci(deps): bump workflows to v1.1.0"`
	both := `create mock-ui-2 main<-ccf-bump/sync-2026-10-08 "fix(deps): bump mock-api to v0.1.0, workflows to v1.1.0"`
	automerge, held := "label mock-ui-2#1 ccf-bump:automerge", "label mock-ui-2#1 needs-human"
	noop := "#!/usr/bin/env bash\nexit 0\n"
	for name, tc := range map[string]struct {
		files   map[string]string
		calls   []string
		out     []string // in the output
		api     bool     // the PR table lists mock-api
		warning string
	}{
		// compliance-framework/ui#355: the conformance script ran, the fixture was already api@v0.1.0's.
		"conformance fixture already current": {
			files: map[string]string{"scripts/sync-agentconfig-conformance.sh": fmt.Sprintf(conformanceScript, `{"cases":[]}`),
				conformanceFixture: "{\"cases\":[]}\n", ".github/workflows/ci.yml": workflowsCI},
			calls: []string{workflowsOnly, automerge},
			out:   []string{"unchanged mock-api     scripts/sync-agentconfig-conformance.sh left " + conformanceFixture + " as it was (already v0.1.0)"},
		},
		"conformance fixture changed": {
			files: map[string]string{"scripts/sync-agentconfig-conformance.sh": fmt.Sprintf(conformanceScript, `{"cases":[1]}`),
				conformanceFixture: "{\"cases\":[]}\n", ".github/workflows/ci.yml": workflowsCI},
			calls: []string{both, held},
			out:   []string{"auto-merge: off (" + bump.MajorReason + ")"},
			api:   true,
		},
		"a pin that is not a version, moved": {
			files: map[string]string{mockScript: "#!/usr/bin/env bash\nprintf '{\"mockApi\": \"%s\"}\\n' \"$1\" > src/api-version.json\n",
				mockVersionFile: `{"mockApi": "main"}` + "\n", ".github/workflows/ci.yml": workflowsCI},
			calls: []string{both, held},
			out:   []string{"auto-merge: off (" + bump.MajorReason + ")"},
			api:   true,
		},
		"a planned change that is not applied": {
			files: map[string]string{mockScript: noop, mockVersionFile: `{"mockApi": "main"}` + "\n", ".github/workflows/ci.yml": workflowsCI},
			calls: []string{workflowsOnly, automerge},
			warning: "::warning::ccf-bump: mock-ui-2: planned mock-api main -> v0.1.0 (src/api-version.json " + mockScript +
				") was not applied; it is left out of the PR",
		},
		"nothing applied": {
			files:   map[string]string{mockScript: noop, mockVersionFile: `{"mockApi": "main"}` + "\n"},
			out:     []string{"no file changes"},
			warning: "::warning::ccf-bump: mock-ui-2: planned mock-api main -> v0.1.0",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, e, gh, out := setup(t)
			gh.finals["workflows"], gh.commits = "v1.1.0", map[string]string{"workflows@v1.1.0": sha}
			m := filepath.Join(root, "ui.yaml")
			if err := os.WriteFile(m, []byte(testManifest+"  - {name: mock-ui-2, kind: ui, depends_on: [mock-api], release: true}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			addRepo(t, root, repo, tc.files)
			args := []string{"sync", "--repos", repo, "--manifest", m, "--pr"}
			if err := run(context.Background(), args, e); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !slices.Equal(gh.calls, tc.calls) {
				t.Errorf("calls %q, want %q\n%s", gh.calls, tc.calls, out)
			}
			for _, s := range append(tc.out, tc.warning) {
				if !strings.Contains(out.String(), s) {
					t.Errorf("output lacks %q:\n%s", s, out)
				}
			}
			if tc.warning == "" && strings.Contains(out.String(), "::warning::") {
				t.Errorf("unexpected warning:\n%s", out)
			}
			if tc.calls == nil {
				return
			}

			// The PR body's table lists only what the diff changes.
			out.Reset()
			if err := run(context.Background(), append(args, "--dry-run"), e); err != nil {
				t.Fatalf("dry run: %v\n%s", err, out)
			}
			if got := strings.Contains(out.String(), "| [mock-api]("); got != tc.api {
				t.Errorf("PR table lists mock-api: %v, want %v:\n%s", got, tc.api, out)
			}
			if !strings.Contains(out.String(), "| [workflows](") {
				t.Errorf("PR table lacks the workflows pin:\n%s", out)
			}
		})
	}
}
