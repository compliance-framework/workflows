// Package renovate builds the self-hosted Renovate global configuration that renovate.yml runs
// with: the shared preset (renovate/default.json) plus the repository list from a manifest.
package renovate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// DryRunOff runs Renovate live: it pushes branches and opens and merges PRs.
const DryRunOff = "off"

// dryRunModes are the values Renovate's dryRun option takes, plus DryRunOff.
var dryRunModes = []string{"full", "lookup", "extract", DryRunOff}

// Options select the repos and the mode of one run.
type Options struct {
	// Owner is the repos' owner, e.g. "compliance-framework".
	Owner string
	// Repos are the repo names, without the owner.
	Repos []string
	// DryRun is "full", "lookup", "extract" or DryRunOff.
	DryRun string
}

// runnerKeys are the settings GlobalConfig owns. The preset must not set them: they decide which
// repos the token reaches and whether anything is written.
var runnerKeys = []string{"platform", "autodiscover", "repositories", "onboarding", "requireConfig", "dryRun", "statusCheckWhen"}

// SelectRepos returns the repos named in list (comma- or space-separated), or every repo in the
// manifest if list is empty, in manifest order. Named repos must be in the manifest.
func SelectRepos(m *manifest.Manifest, list string) ([]string, error) {
	names := strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
	var unknown []string
	for _, n := range names {
		if !slices.ContainsFunc(m.Repos, func(r manifest.Repo) bool { return r.Name == n }) {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("not in the manifest: %s", strings.Join(unknown, ", "))
	}
	var out []string
	for _, r := range m.Repos {
		if len(names) == 0 || slices.Contains(names, r.Name) {
			out = append(out, r.Name)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no repos selected")
	}
	return out, nil
}

// GlobalConfig returns the Renovate global config: every setting of preset (a JSON object), plus
// the settings the runner owns. Renovate applies the global config to every listed repo as its
// base; a repo's own renovate.json only overrides it.
func GlobalConfig(preset []byte, opts Options) ([]byte, error) {
	if opts.Owner == "" {
		return nil, errors.New("no owner")
	}
	if len(opts.Repos) == 0 {
		return nil, errors.New("no repos")
	}
	if !slices.Contains(dryRunModes, opts.DryRun) {
		return nil, fmt.Errorf("dry run %q: want one of %s", opts.DryRun, strings.Join(dryRunModes, ", "))
	}
	dec := json.NewDecoder(bytes.NewReader(preset))
	dec.UseNumber() // keep the preset's numbers as written
	var cfg map[string]any
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("preset: %w", err)
	}
	if cfg == nil {
		return nil, errors.New("preset: want a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("preset: trailing data after the JSON object")
	}
	for _, k := range runnerKeys {
		if _, ok := cfg[k]; ok {
			return nil, fmt.Errorf("preset sets %q, which the runner owns", k)
		}
	}

	repos := make([]string, len(opts.Repos))
	for i, r := range opts.Repos {
		repos[i] = opts.Owner + "/" + r
	}
	cfg["platform"] = "github"
	// Only the listed repos, never every repo the app is installed on (all org repos).
	cfg["autodiscover"] = false
	cfg["repositories"] = repos
	// No onboarding PRs: the preset applies to a repo with no renovate.json of its own.
	cfg["onboarding"] = false
	cfg["requireConfig"] = "optional"
	// ccf-release-bot has no Commit statuses permission, so Renovate must not set its
	// renovate/stability-days status. internalChecksFilter "strict" already keeps a branch from
	// being created before the update is old enough, so the status would add nothing.
	cfg["statusCheckWhen"] = map[string]any{"minimumReleaseAge": "never"}
	if opts.DryRun != DryRunOff {
		cfg["dryRun"] = opts.DryRun
	}

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
