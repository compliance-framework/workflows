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
	"strings"
	"testing"

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
