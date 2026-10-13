package manifest

import (
	"fmt"
	"slices"
	"strings"
)

// Batches gate the plugin and policy repos. A repo with a batch is acted on by the tools
// (Renovate, ccf-bump, the train, the attention digest, the vulnerability summary and
// repo-settings' default selection) only once its batch is in active_batches; a repo without one
// is a core repo and always active. Adding a batch's repos to the manifest therefore changes
// nothing until the batch is switched on.

// batchKinds are the kinds that may carry a batch: every other repo is core.
var batchKinds = []Kind{KindGoPlugin, KindPolicies}

// Repo returns the repo named name, or nil when the manifest has none.
func (m *Manifest) Repo(name string) *Repo {
	for i := range m.Repos {
		if m.Repos[i].Name == name {
			return &m.Repos[i]
		}
	}
	return nil
}

// Active reports whether the tools act on r: it is a core repo (no batch) or its batch is active.
func (m *Manifest) Active(r Repo) bool {
	return r.Batch == 0 || slices.Contains(m.ActiveBatches, r.Batch)
}

// ActiveRepos returns the core repos and the repos of the active batches, in manifest order.
// Every tool that enumerates the manifest's repos starts from it.
func (m *Manifest) ActiveRepos() []Repo {
	var out []Repo
	for _, r := range m.Repos {
		if m.Active(r) {
			out = append(out, r)
		}
	}
	return out
}

// Select returns the repos named in names, in manifest order and without duplicates, or
// ActiveRepos when names is empty. Every name must be in the manifest; unless inactive is true,
// it must also be active. Only repo-settings passes inactive: a batch's settings are applied
// before the batch is switched on.
func (m *Manifest) Select(names []string, inactive bool) ([]Repo, error) {
	var unknown, off []string
	for _, n := range names {
		switch r := m.Repo(n); {
		case r == nil:
			unknown = append(unknown, n)
		case !inactive && !m.Active(*r):
			off = append(off, fmt.Sprintf("%s (batch %d)", n, r.Batch))
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("not in the manifest: %s", strings.Join(unknown, ", "))
	}
	if len(off) > 0 {
		return nil, fmt.Errorf("in a batch that is not active (active_batches: %v): %s", m.ActiveBatches, strings.Join(off, ", "))
	}
	if len(names) == 0 {
		return m.ActiveRepos(), nil
	}
	var out []Repo
	for _, r := range m.Repos {
		if slices.Contains(names, r.Name) {
			out = append(out, r)
		}
	}
	return out, nil
}

// validateBatches checks that only plugin and policy repos have a batch, that every active batch
// is listed once and has repos, that no repo depends on a repo of a later batch (a core repo on
// none), and that no repo is both listed and excluded.
func (m *Manifest) validateBatches() error {
	used := map[int]bool{}
	for _, r := range m.Repos {
		if r.Batch == 0 {
			continue
		}
		if !slices.Contains(batchKinds, r.Kind) {
			return fmt.Errorf("repo %q: kind %q is a core repo, always active: only kinds %q and %q take a batch", r.Name, r.Kind, KindGoPlugin, KindPolicies)
		}
		used[r.Batch] = true
	}
	for i, b := range m.ActiveBatches {
		if !used[b] {
			return fmt.Errorf("active_batches: no repo is in batch %d", b)
		}
		if slices.Contains(m.ActiveBatches[:i], b) {
			return fmt.Errorf("active_batches: batch %d is listed more than once", b)
		}
	}
	for _, r := range m.Repos {
		for _, d := range r.DependsOn {
			if dep := m.Repo(d); dep != nil && dep.Batch != 0 && (r.Batch == 0 || dep.Batch > r.Batch) {
				return fmt.Errorf("repo %q depends on %q, which is in a later batch (%d)", r.Name, d, dep.Batch)
			}
		}
	}
	for _, x := range m.Exclude {
		if m.Repo(x) != nil {
			return fmt.Errorf("repo %q is both listed and excluded", x)
		}
	}
	return nil
}
