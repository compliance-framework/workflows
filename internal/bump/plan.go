// Package bump finds the pinned versions of internal dependencies in a repo checkout (Go modules,
// go install pins, the action's source image, policy repos' OPA version, the ui sync script, helm
// appVersions and image tags, shared-workflow refs), decides which to move to a target version
// (never downgrading), and rewrites them. cmd/ccf-bump drives it.
package bump

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// Dependency names that are not repos: OPA (policy repos copy it from the agent's go.mod) and the
// shared workflows (refs move only when a target ref is given).
const (
	DepOPA       = "opa"
	DepWorkflows = "workflows"
)

// Ref is one pinned version of a dependency, as found in a repo.
type Ref struct {
	Updater string // the updater that found it, and rewrites it
	Dep     string // the repo it pins (e.g. "mock-api"), DepOPA or DepWorkflows
	Key     string // what is pinned: a module path, an image, a YAML path, a workflow path
	Current string // as written: "v0.1.0", "0.1.0", "alpine:3.20"; "" when unknown
	File    string // path relative to the repo root
}

// Change moves a Ref to To, a canonical version ("v1.2.3") or, for DepWorkflows, a ref.
type Change struct {
	Ref
	To string
}

// Conflict is a Ref that is not moved, and why.
type Conflict struct {
	Ref
	Target string
	Reason string
}

// Plan is what a bump does to one repo.
type Plan struct {
	Changes   []Change
	Conflicts []Conflict
	UpToDate  []Ref
	NoTarget  []Ref // no target version: no final release, or not asked for
}

// TagTime returns the commit time of dep's tag; it is needed only to compare a pseudo-version.
type TagTime func(dep, tag string) (time.Time, error)

// Canonical returns v with a leading "v" when it looks like a version without one ("0.1.0").
func Canonical(v string) string {
	v = strings.TrimSpace(v)
	if v != "" && v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return v
}

// Bare returns a canonical version without its leading "v", as image tags and appVersions use.
func Bare(v string) string {
	return strings.TrimPrefix(Canonical(v), "v")
}

// Decide plans refs against targets (dep -> version). It never downgrades: a pin newer than the
// target, including a pseudo-version of a later commit, is a conflict. A pin that is not a version
// (an image like alpine:3.20, a branch, a SHA, "latest") or is unknown is replaced, and so is a
// shared-workflow ref that differs from the target ref.
func Decide(refs []Ref, targets map[string]string, tagTime TagTime) (Plan, error) {
	var p Plan
	for _, r := range refs {
		target, ok := targets[r.Dep]
		if !ok || target == "" {
			p.NoTarget = append(p.NoTarget, r)
			continue
		}
		cur, tgt := Canonical(r.Current), Canonical(target)
		switch {
		case r.Current != "" && (cur == tgt || r.Current == target):
			p.UpToDate = append(p.UpToDate, r)
			continue
		case r.Dep == DepWorkflows: // a ref is used as given, whatever it was
		case !semver.IsValid(cur) || !semver.IsValid(tgt):
		case semver.Compare(cur, tgt) == 0:
			p.UpToDate = append(p.UpToDate, r)
			continue
		case semver.Compare(cur, tgt) > 0:
			p.Conflicts = append(p.Conflicts, Conflict{Ref: r, Target: tgt,
				Reason: fmt.Sprintf("pinned %s is newer than %s; ccf-bump never downgrades", r.Current, tgt)})
			continue
		case module.IsPseudoVersion(cur) && tagTime != nil:
			pt, err := module.PseudoVersionTime(cur)
			if err != nil {
				return p, fmt.Errorf("%s %s: %w", r.Key, cur, err)
			}
			tt, err := tagTime(r.Dep, tgt)
			if err != nil {
				return p, fmt.Errorf("commit time of %s %s: %w", r.Dep, tgt, err)
			}
			if pt.After(tt) {
				p.Conflicts = append(p.Conflicts, Conflict{Ref: r, Target: tgt, Reason: fmt.Sprintf(
					"pinned pseudo-version %s (commit %s) is newer than %s (commit %s); ccf-bump never downgrades",
					r.Current, pt.UTC().Format(time.RFC3339), tgt, tt.UTC().Format(time.RFC3339))})
				continue
			}
		}
		if r.Dep == DepWorkflows {
			tgt = target
		}
		p.Changes = append(p.Changes, Change{Ref: r, To: tgt})
	}
	return p, nil
}

// Major reports whether the change may break callers: a new major version, or a move from a pin
// that is not a version. Only changes that are not major are auto-merged.
func (c Change) Major() bool {
	from := Canonical(c.Current)
	if !semver.IsValid(from) || !semver.IsValid(c.To) {
		return true
	}
	return semver.Major(from) != semver.Major(c.To)
}
