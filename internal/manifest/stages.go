package manifest

import (
	"fmt"
	"slices"
	"strings"
)

// Stages groups the repos into topological stages: every repo's dependencies
// are in earlier stages, and each repo sits in the earliest stage it can.
//
// Helm repos are deployment bundles that pin the versions of everything else,
// so they release last: a repo of kind helm implicitly depends on every
// non-helm repo, on top of its depends_on. With repos.yaml that puts
// helm-charts in a stage of its own after agent-action.
//
// The repos within a stage are sorted by name. If the dependencies contain a
// cycle it returns an error naming the repos that cannot be ordered (those in
// the cycle and those depending on it).
//
// Dependencies on repos that are not in the manifest are ignored here; Validate
// reports them.
func (m *Manifest) Stages() ([][]string, error) {
	pending := make(map[string]int, len(m.Repos)) // unmet dependency count
	dependents := make(map[string][]string, len(m.Repos))
	for _, r := range m.Repos {
		pending[r.Name] = 0
	}
	var nonHelm []string
	for _, r := range m.Repos {
		if r.Kind != KindHelm {
			nonHelm = append(nonHelm, r.Name)
		}
	}
	for _, r := range m.Repos {
		deps := r.DependsOn
		if r.Kind == KindHelm {
			deps = slices.Concat(deps, nonHelm)
		}
		seen := make(map[string]bool, len(deps))
		for _, d := range deps {
			if _, ok := pending[d]; !ok || seen[d] {
				continue
			}
			seen[d] = true
			pending[r.Name]++
			dependents[d] = append(dependents[d], r.Name)
		}
	}

	var current []string
	for name, n := range pending {
		if n == 0 {
			current = append(current, name)
		}
	}

	var stages [][]string
	for len(current) > 0 {
		slices.Sort(current)
		stages = append(stages, current)
		var next []string
		for _, name := range current {
			delete(pending, name)
			for _, dep := range dependents[name] {
				pending[dep]--
				if pending[dep] == 0 {
					next = append(next, dep)
				}
			}
		}
		current = next
	}

	if len(pending) > 0 {
		left := make([]string, 0, len(pending))
		for name := range pending {
			left = append(left, name)
		}
		slices.Sort(left)
		return nil, fmt.Errorf("dependency cycle: cannot order repos %s", strings.Join(left, ", "))
	}
	return stages, nil
}
