package ciworkflows_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/imagescan"
	"go.yaml.in/yaml/v3"
)

// badDQEscapes returns the \' escapes in src's double-quoted scalars, as "line:column". Go's yaml
// accepts \' there, but it isn't a YAML 1.2 escape (a single quote needs none in double quotes),
// and it is the only one: Go's yaml rejects every other escape YAML 1.2 doesn't have, so src
// failing to parse is an error too.
func badDQEscapes(src []byte) ([]string, error) {
	// Every document, as Renovate parses them all.
	var docs []*yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		docs = append(docs, &doc)
	}
	lines := strings.SplitAfter(string(src), "\n")
	var bad []string
	var walk func(n *yaml.Node) error
	walk = func(n *yaml.Node) error {
		if n.Kind == yaml.ScalarNode && n.Style&yaml.DoubleQuotedStyle != 0 {
			// The source from the scalar's line on; rest[start] is its opening quote (yaml's
			// columns count runes). lineStart is the index of the current line's first rune.
			line, lineStart, start := n.Line, 0, n.Column-1
			rest := []rune(strings.Join(lines[line-1:], ""))
			if start >= len(rest) || rest[start] != '"' {
				return fmt.Errorf("%d:%d: no opening quote for %q", n.Line, n.Column, n.Value)
			}
			for i := start + 1; i < len(rest) && rest[i] != '"'; i++ {
				if rest[i] == '\\' && i+1 < len(rest) {
					i++ // the escaped rune, so an escaped quote doesn't end the scalar
					if rest[i] == '\'' {
						bad = append(bad, fmt.Sprintf("%d:%d", line, i-lineStart))
					}
				}
				if rest[i] == '\n' { // also an escaped one: a line continuation
					line, lineStart = line+1, i+1
				}
			}
		}
		for _, c := range n.Content {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	for _, doc := range docs {
		if err := walk(doc); err != nil {
			return nil, err
		}
	}
	return bad, nil
}

func TestBadDQEscapes(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want []string
	}{
		{"a: \"caller\\'s\"\n", []string{"1:11"}},
		{"\"k\\'\": \"\\\"\\\\'\"\n", []string{"1:3"}}, // a key; \\ then a plain '
		{"a: \"tab\\t \\\" \\\\ \\x41 \\u00e9 \\\n  more\"\n", nil},
		{"a:\n  - b: \"one \\\n    two \\'\"\n  - é: \"\\'\"\n", []string{"3:9", "4:9"}},
		{"a: 1\n---\nb: \"\\'\"\n", []string{"3:5"}}, // a later document
		// Not double-quoted: a shell script's \' is the script's, not YAML's.
		{"run: |\n  echo it\\'s \"x\\'\"\nb: 'it''s'\nc: it\\'s\n", nil},
	} {
		got, err := badDQEscapes([]byte(tc.src))
		if err != nil {
			t.Fatalf("%q: %v", tc.src, err)
		}
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%q: got %q, want %q", tc.src, got, tc.want)
		}
	}
}

// TestGitHubYAMLEscapes keeps every YAML file under .github valid YAML 1.2 in its double-quoted
// escapes. GitHub and Go's yaml accept a non-standard escape such as \' (as in "the caller\'s
// secret"), but Renovate's YAML parser rejects the file, and its github-actions manager then
// silently drops everything it reads from the parsed workflow: the `with:` versions (goreleaser,
// golangci-lint, chart-testing, node), containers, services and runners. Only the `uses:` lines,
// which it finds with a regex, still get updates, so the pins drift apart (PR #146).
func TestGitHubYAMLEscapes(t *testing.T) {
	root := filepath.Join("..", "..", ".github")
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || (filepath.Ext(path) != ".yml" && filepath.Ext(path) != ".yaml") {
			return err
		}
		n++
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		bad, err := badDQEscapes(src)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for _, b := range bad {
			t.Errorf(`%s:%s: \' is not a YAML 1.2 escape, so Renovate can't parse the file; write ' instead`, path, b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no YAML files under .github")
	}
}

// TestSchemaURLRenovate checks this repo's renovate.json custom manager for the golangci-lint
// schema URL (SCHEMA_URL) of the Go kind workflows: it reads every file with such a URL, and finds
// the URL's version as the dependency the github-actions manager reads from the
// golangci-lint-action step's with.version (same depName, packageName and datasource), so Renovate
// bumps both in one PR and TestGolangciVerifySetup keeps passing.
func TestSchemaURLRenovate(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "renovate.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		CustomManagers []struct {
			CustomType          string   `json:"customType"`
			ManagerFilePatterns []string `json:"managerFilePatterns"`
			MatchStrings        []string `json:"matchStrings"`
			DatasourceTemplate  string   `json:"datasourceTemplate"`
			DepNameTemplate     string   `json:"depNameTemplate"`
			PackageNameTemplate string   `json:"packageNameTemplate"`
		} `json:"customManagers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	var files []*regexp.Regexp
	var match []*regexp.Regexp
	for _, m := range cfg.CustomManagers {
		if m.DepNameTemplate != "golangci/golangci-lint" {
			continue
		}
		if m.CustomType != "regex" || m.DatasourceTemplate != "github-releases" || m.PackageNameTemplate != "golangci/golangci-lint" {
			t.Errorf("golangci-lint custom manager: customType %q, datasource %q, packageName %q; want regex, github-releases, golangci/golangci-lint (as the github-actions manager's golangci-lint-action dep)",
				m.CustomType, m.DatasourceTemplate, m.PackageNameTemplate)
		}
		for _, p := range m.ManagerFilePatterns {
			// A Renovate file pattern is a regex when written /like this/, else a glob.
			re, ok := strings.CutPrefix(p, "/")
			if re, ok = strings.CutSuffix(re, "/"); !ok {
				t.Fatalf("managerFilePatterns %q: want a /regex/", p)
			}
			files = append(files, regexp.MustCompile(re))
		}
		for _, s := range m.MatchStrings {
			re := regexp.MustCompile(s)
			if re.SubexpIndex("currentValue") < 0 {
				t.Fatalf("matchStrings %q: no currentValue group", s)
			}
			match = append(match, re)
		}
	}
	if len(files) == 0 || len(match) == 0 {
		t.Fatal("renovate.json: no golangci/golangci-lint custom manager with managerFilePatterns and matchStrings")
	}

	paths, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	withURL := map[string]bool{}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "raw.githubusercontent.com/golangci/golangci-lint/") {
			withURL[filepath.Base(path)] = true
		}
	}
	for _, file := range []string{goPlugin, goService, goLib} {
		if !withURL[file] {
			t.Errorf("%s has no golangci-lint schema URL", file)
		}
	}
	for file := range withURL {
		rel := ".github/workflows/" + file
		matched := false
		for _, re := range files {
			matched = matched || re.MatchString(rel)
		}
		if !matched {
			t.Errorf("%s has a golangci-lint schema URL, but the custom manager's managerFilePatterns don't match it", rel)
			continue
		}
		src, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", file))
		if err != nil {
			t.Fatal(err)
		}
		var found []string
		for _, re := range match {
			for _, m := range re.FindAllStringSubmatch(string(src), -1) {
				found = append(found, m[re.SubexpIndex("currentValue")])
			}
		}
		version := golangciVersion(t, file)
		if len(found) != 1 || found[0] != version {
			t.Errorf("%s: the custom manager finds %q, want the golangci-lint-action version [%s]", file, found, version)
		}
	}
}

// golangciVersion returns the with.version of file's golangci-lint-action step.
func golangciVersion(t *testing.T, file string) string {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	read(t, file, &wf)
	var versions []string
	for _, job := range wf.Jobs {
		for _, s := range job.Steps {
			if strings.HasPrefix(s.Uses, "golangci/golangci-lint-action@") {
				versions = append(versions, s.With["version"])
			}
		}
	}
	if len(versions) != 1 || versions[0] == "" {
		t.Fatalf("%s: golangci-lint-action versions %q, want one", file, versions)
	}
	return versions[0]
}

// TestScannerImageRenovate checks this repo's renovate.json custom manager for the osv-scanner
// image image-scan runs (imagescan.ScannerImage, a Go constant no manager reads): it reads
// scanner.go and finds the constant's image, tag and digest, so Renovate updates the pin.
func TestScannerImageRenovate(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "renovate.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		CustomManagers []struct {
			CustomType          string   `json:"customType"`
			ManagerFilePatterns []string `json:"managerFilePatterns"`
			MatchStrings        []string `json:"matchStrings"`
			DatasourceTemplate  string   `json:"datasourceTemplate"`
		} `json:"customManagers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	const file = "internal/imagescan/scanner.go"
	src, err := os.ReadFile(filepath.Join("..", "..", file))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, m := range cfg.CustomManagers {
		if m.DatasourceTemplate != "docker" {
			continue
		}
		matched := false
		for _, p := range m.ManagerFilePatterns {
			re, prefixed := strings.CutPrefix(p, "/")
			re, suffixed := strings.CutSuffix(re, "/")
			if !prefixed || !suffixed {
				t.Fatalf("managerFilePatterns %q: want a /regex/", p)
			}
			matched = matched || regexp.MustCompile(re).MatchString(file)
		}
		if !matched || m.CustomType != "regex" {
			continue
		}
		for _, s := range m.MatchStrings {
			re := regexp.MustCompile(s)
			for _, g := range []string{"depName", "currentValue", "currentDigest"} {
				if re.SubexpIndex(g) < 0 {
					t.Fatalf("matchStrings %q: no %s group", s, g)
				}
			}
			for _, sm := range re.FindAllStringSubmatch(string(src), -1) {
				found = append(found, sm[re.SubexpIndex("depName")]+":"+sm[re.SubexpIndex("currentValue")]+"@"+sm[re.SubexpIndex("currentDigest")])
			}
		}
	}
	if len(found) != 1 || found[0] != imagescan.ScannerImage {
		t.Errorf("renovate.json's docker custom managers find %q in %s, want [%s]", found, file, imagescan.ScannerImage)
	}
}

// renovateWorkflow is the part of renovate.yml the tests below pin.
type renovateWorkflow struct {
	Env  map[string]string `yaml:"env"`
	Jobs map[string]struct {
		Needs       []string          `yaml:"needs"`
		If          string            `yaml:"if"`
		Environment string            `yaml:"environment"`
		Outputs     map[string]string `yaml:"outputs"`
		Timeout     int               `yaml:"timeout-minutes"`
		Steps       []struct {
			ID              string            `yaml:"id"`
			Name            string            `yaml:"name"`
			Uses            string            `yaml:"uses"`
			If              string            `yaml:"if"`
			ContinueOnError bool              `yaml:"continue-on-error"`
			With            map[string]string `yaml:"with"`
			Env             map[string]string `yaml:"env"`
			Run             string            `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// TestRenovateJobs pins renovate.yml's split into select, image-scan and renovate: image-scan runs
// on the shard's image repos with a read-only token scoped to them, and Renovate runs whenever
// select succeeded, with the overrides when there are any.
func TestRenovateJobs(t *testing.T) {
	var wf renovateWorkflow
	read(t, "renovate.yml", &wf)
	if len(wf.Jobs) != 3 {
		t.Fatalf("jobs: %d, want select, image-scan and renovate", len(wf.Jobs))
	}
	sel, scan, ren := wf.Jobs["select"], wf.Jobs["image-scan"], wf.Jobs["renovate"]
	for _, out := range []string{"repos", "slot", "image-repos"} {
		if sel.Outputs[out] != "${{ steps.select.outputs."+out+" }}" {
			t.Errorf("select output %s = %q", out, sel.Outputs[out])
		}
	}
	if sel.Environment != "" {
		t.Errorf("select has environment %q: it reads no secret", sel.Environment)
	}
	if !strings.Contains(wf.Env["DRY_RUN"], "vars.RENOVATE_LIVE == 'true'") {
		t.Errorf("DRY_RUN = %q, want the workflow-level fallback for every job", wf.Env["DRY_RUN"])
	}

	if !slices.Equal(scan.Needs, []string{"select"}) || scan.If != "${{ needs.select.outputs.image-repos != '' }}" || scan.Environment != "release" {
		t.Errorf("image-scan: needs %v, if %q, environment %q", scan.Needs, scan.If, scan.Environment)
	}
	if scan.Timeout == 0 || scan.Timeout > 55 {
		t.Errorf("image-scan timeout %d, want within the token's hour", scan.Timeout)
	}
	var minted, scanned, uploaded bool
	for _, s := range scan.Steps {
		switch {
		case strings.HasPrefix(s.Uses, "actions/create-github-app-token@"):
			minted = true
			if s.With["repositories"] != "${{ needs.select.outputs.image-repos }}" {
				t.Errorf("image-scan token repositories = %q, want the image repos", s.With["repositories"])
			}
			var perms []string
			for k, v := range s.With {
				if strings.HasPrefix(k, "permission-") {
					perms = append(perms, k+": "+v)
				}
			}
			if !slices.Equal(perms, []string{"permission-contents: read"}) {
				t.Errorf("image-scan token permissions %v, want contents: read only", perms)
			}
		case s.Name == "Scan the images":
			scanned = strings.Contains(s.Run, "go run ./cmd/image-scan scan") &&
				strings.Contains(s.Run, `--overrides "$RUNNER_TEMP/image-scan/overrides.json"`) &&
				strings.Contains(s.Run, `--summary "$GITHUB_STEP_SUMMARY"`) &&
				strings.Contains(s.Run, "--cache-dir ~/.cache/osv-scanner") &&
				s.Env["GH_TOKEN"] == "${{ steps.token.outputs.token }}"
		case strings.HasPrefix(s.Uses, "actions/upload-artifact@"):
			uploaded = s.If == "${{ !cancelled() }}" && s.With["name"] == "image-scan-overrides" && s.With["if-no-files-found"] == "ignore"
		}
	}
	if !minted || !scanned || !uploaded {
		t.Errorf("image-scan: token %v, scan step %v, overrides upload after a failure too %v", minted, scanned, uploaded)
	}

	if !slices.Equal(ren.Needs, []string{"select", "image-scan"}) || ren.If != "${{ !cancelled() && needs.select.result == 'success' }}" {
		t.Errorf("renovate: needs %v, if %q; want it to run whenever select succeeded", ren.Needs, ren.If)
	}
	var downloaded, configured bool
	for _, s := range ren.Steps {
		if strings.HasPrefix(s.Uses, "actions/download-artifact@") {
			downloaded = s.ContinueOnError && s.With["name"] == "image-scan-overrides" && s.If == "${{ needs.image-scan.result != 'skipped' }}"
		}
		if s.Name == "Build the Renovate config" {
			configured = strings.Contains(s.Run, `--overrides "$RUNNER_TEMP/image-scan/overrides.json"`)
		}
		if strings.HasPrefix(s.Uses, "actions/create-github-app-token@") && s.With["repositories"] != "${{ needs.select.outputs.repos }}" {
			t.Errorf("renovate token %s: repositories %q, want the shard", s.ID, s.With["repositories"])
		}
	}
	if !downloaded || !configured {
		t.Errorf("renovate: overrides downloaded (soft) %v, passed to the config %v", downloaded, configured)
	}
}

// TestRenovateSelect runs the select step with a fake go: it outputs the shard and the shard's
// image repos (empty skips image-scan), and fails on an empty shard, which would scope a token to
// every repo.
func TestRenovateSelect(t *testing.T) {
	src := script(t, "renovate.yml", "select", "Select repos")
	path := fakeBin(t, "go", `#!/bin/sh
case "$2" in
./cmd/renovate-config) echo "$FAKE_REPOS" ;;
./cmd/image-scan) [ "$3" = list ] && echo "$FAKE_IMAGES" ;;
*) exit 1 ;;
esac
`)
	for _, tc := range []struct {
		name, repos, images string
		failed              bool
	}{
		{"image repos in the shard", "api,ui,plugin-a", "api,ui", false},
		{"none", "plugin-a", "", false},
		{"empty shard", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, t.TempDir(), src, path, "FAKE_REPOS="+tc.repos, "FAKE_IMAGES="+tc.images, "RUN_NUMBER=7", "MANIFEST=repos.yaml", "REPOS=", "DRY_RUN=full")
			if r.failed != tc.failed {
				t.Fatalf("failed = %v, want %v; output:\n%s", r.failed, tc.failed, r.out)
			}
			if !tc.failed && (r.outputs["repos"] != tc.repos || r.outputs["image-repos"] != tc.images || r.outputs["slot"] != "7") {
				t.Errorf("outputs = %v", r.outputs)
			}
		})
	}
}
