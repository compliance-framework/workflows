// Package manifest loads and validates the repo manifest (repos.yaml or
// repos.mock.yaml) that the shared workflows and tools operate on.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"go.yaml.in/yaml/v3"
)

// DefaultPath is the default value of every tool's --manifest flag.
const DefaultPath = "repos.yaml"

// dateLayout is the ISO date format used for holidays.
const dateLayout = "2006-01-02"

// Kind says how a repo is built and released, and so which CI and release
// workflows apply to it.
type Kind string

// The repo kinds a manifest may use.
const (
	KindGoService Kind = "go-service"
	KindGoPlugin  Kind = "go-plugin"
	KindGoLib     Kind = "go-lib"
	KindPolicies  Kind = "policies"
	KindUI        Kind = "ui"
	KindHelm      Kind = "helm"
	KindAction    Kind = "action"
)

var validKinds = map[Kind]bool{
	KindGoService: true,
	KindGoPlugin:  true,
	KindGoLib:     true,
	KindPolicies:  true,
	KindUI:        true,
	KindHelm:      true,
	KindAction:    true,
}

// Repo is one repository entry in the manifest.
type Repo struct {
	// Name is the repo name within the organisation, e.g. "agent".
	Name string `yaml:"name"`
	// Kind selects the CI and release workflows for the repo.
	Kind Kind `yaml:"kind"`
	// DependsOn lists the names of repos this repo consumes; they release first.
	DependsOn []string `yaml:"depends_on,omitempty"`
	// Release says whether the release tooling acts on this repo.
	Release bool `yaml:"release"`
	// Charts lists the chart names in a helm repo.
	Charts []string `yaml:"charts,omitempty"`
}

// Manifest is the parsed and validated manifest file.
type Manifest struct {
	// Holidays are ISO dates (YYYY-MM-DD) that are not working days.
	Holidays []string `yaml:"holidays,omitempty"`
	// IncludePatterns are repo name globs (path.Match syntax) to include.
	IncludePatterns []string `yaml:"include_patterns,omitempty"`
	// Exclude lists repo names that IncludePatterns must not pick up.
	Exclude []string `yaml:"exclude,omitempty"`
	// Repos are the repos, in file order.
	Repos []Repo `yaml:"repos"`
}

// rawRepo mirrors Repo but keeps release as a pointer, so a missing value is
// an error rather than a silent false.
type rawRepo struct {
	Name      string   `yaml:"name"`
	Kind      Kind     `yaml:"kind"`
	DependsOn []string `yaml:"depends_on"`
	Release   *bool    `yaml:"release"`
	Charts    []string `yaml:"charts"`
}

type rawManifest struct {
	Holidays        []string  `yaml:"holidays"`
	IncludePatterns []string  `yaml:"include_patterns"`
	Exclude         []string  `yaml:"exclude"`
	Repos           []rawRepo `yaml:"repos"`
}

// Load reads and validates the manifest at path.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	m, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// Parse decodes and validates a manifest. Unknown fields are rejected.
func Parse(data []byte) (*Manifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var raw rawManifest
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("manifest is empty")
		}
		return nil, fmt.Errorf("decode manifest: %w", err)
	}

	m := &Manifest{
		Holidays:        raw.Holidays,
		IncludePatterns: raw.IncludePatterns,
		Exclude:         raw.Exclude,
		Repos:           make([]Repo, 0, len(raw.Repos)),
	}
	for i, r := range raw.Repos {
		if r.Release == nil {
			return nil, fmt.Errorf("repos[%d] (%q): release must be set to true or false", i, r.Name)
		}
		m.Repos = append(m.Repos, Repo{
			Name:      r.Name,
			Kind:      r.Kind,
			DependsOn: r.DependsOn,
			Release:   *r.Release,
			Charts:    r.Charts,
		})
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Validate checks the manifest's invariants: unique names, known kinds,
// dependencies that exist and form no cycle (and none on a helm repo from a
// non-helm repo, since helm repos release last), ISO holidays and valid globs.
func (m *Manifest) Validate() error {
	if len(m.Repos) == 0 {
		return errors.New("manifest has no repos")
	}
	names := make(map[string]bool, len(m.Repos))
	for i, r := range m.Repos {
		if r.Name == "" {
			return fmt.Errorf("repos[%d]: name is required", i)
		}
		if names[r.Name] {
			return fmt.Errorf("repo %q is listed more than once", r.Name)
		}
		names[r.Name] = true
		if !validKinds[r.Kind] {
			return fmt.Errorf("repo %q: unknown kind %q", r.Name, r.Kind)
		}
		if len(r.Charts) > 0 && r.Kind != KindHelm {
			return fmt.Errorf("repo %q: charts are only allowed on kind %q", r.Name, KindHelm)
		}
	}
	kinds := make(map[string]Kind, len(m.Repos))
	for _, r := range m.Repos {
		kinds[r.Name] = r.Kind
	}
	for _, r := range m.Repos {
		seen := make(map[string]bool, len(r.DependsOn))
		for _, d := range r.DependsOn {
			if d == r.Name {
				return fmt.Errorf("repo %q depends on itself", r.Name)
			}
			if !names[d] {
				return fmt.Errorf("repo %q depends on unknown repo %q", r.Name, d)
			}
			if seen[d] {
				return fmt.Errorf("repo %q lists dependency %q more than once", r.Name, d)
			}
			if r.Kind != KindHelm && kinds[d] == KindHelm {
				return fmt.Errorf("repo %q depends on helm repo %q, but helm repos release last", r.Name, d)
			}
			seen[d] = true
		}
	}
	for _, h := range m.Holidays {
		if _, err := time.Parse(dateLayout, h); err != nil {
			return fmt.Errorf("holiday %q is not an ISO date (YYYY-MM-DD)", h)
		}
	}
	for _, p := range m.IncludePatterns {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("include pattern %q: %w", p, err)
		}
	}
	if _, err := m.Stages(); err != nil {
		return err
	}
	return nil
}
