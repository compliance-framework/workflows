package manifest

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// OrgRepo is one of the organisation's repos, as the GitHub API lists it.
type OrgRepo struct {
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

// CheckCoverage compares the manifest with the organisation's repos. Every listed repo must exist
// and not be archived, and every repo that is not archived and matches an include pattern must be
// listed or excluded, so a new plugin or policy repo is a decision rather than an omission.
// Archived repos are never required. include_patterns and exclude feed only this check: the tools
// act on the listed repos.
func (m *Manifest) CheckCoverage(org []OrgRepo) error {
	byName := make(map[string]OrgRepo, len(org))
	for _, r := range org {
		byName[r.Name] = r
	}
	var missing, archived, uncovered []string
	for _, r := range m.Repos {
		switch o, ok := byName[r.Name]; {
		case !ok:
			missing = append(missing, r.Name)
		case o.Archived:
			archived = append(archived, r.Name)
		}
	}
	for _, o := range org {
		if o.Archived || m.Repo(o.Name) != nil || slices.Contains(m.Exclude, o.Name) {
			continue
		}
		for _, p := range m.IncludePatterns {
			if ok, _ := path.Match(p, o.Name); ok { // Validate rejects bad patterns
				uncovered = append(uncovered, o.Name)
				break
			}
		}
	}
	var errs []error
	for _, e := range []struct {
		what  string
		names []string
	}{
		{"listed but not in the organisation", missing},
		{"listed but archived", archived},
		{"match include_patterns but are neither listed nor excluded", uncovered},
	} {
		if len(e.names) > 0 {
			slices.Sort(e.names)
			errs = append(errs, fmt.Errorf("%s: %s", e.what, strings.Join(e.names, ", ")))
		}
	}
	return errors.Join(errs...)
}
