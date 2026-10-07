package release

import (
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

// ComponentTag splits a release-please tag into its component and version: "<component>-vX.Y.Z"
// (a multi-package repo, e.g. charts) or "vX.Y.Z" (component ""), each with an optional
// pre-release part. The component is what precedes the first "-v" followed by a valid version,
// so a component may itself contain "-v" (my-vault-v1.0.0 is my-vault, 1.0.0).
func ComponentTag(tag string) (component, version string, err error) {
	if v, ok := strings.CutPrefix(tag, "v"); ok && valid(v) {
		return "", v, nil
	}
	for i := strings.Index(tag, "-v"); i > 0; {
		if v := tag[i+2:]; valid(v) {
			return tag[:i], v, nil
		}
		next := strings.Index(tag[i+1:], "-v")
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return "", "", fmt.Errorf("tag %q is not [<component>-]vX.Y.Z[-<pre-release>]", tag)
}

// valid reports whether version is X.Y.Z with an optional pre-release part.
func valid(version string) bool {
	v := "v" + version
	return semver.IsValid(v) && semver.Canonical(v) == v
}

// Chart is a chart in a helm repo: its directory name and its Chart.yaml name.
type Chart struct {
	Dir, Name string
}

// PickChart returns the chart a release of component is for: the one whose directory or
// Chart.yaml name is component (the directory wins), or, for a tag without a component, the
// repo's only chart.
func PickChart(charts []Chart, component string) (Chart, error) {
	if component == "" {
		if len(charts) != 1 {
			return Chart{}, fmt.Errorf("the tag has no chart component and the repo has %d charts", len(charts))
		}
		return charts[0], nil
	}
	var byName []Chart
	for _, c := range charts {
		if c.Dir == component {
			return c, nil
		}
		if c.Name == component {
			byName = append(byName, c)
		}
	}
	if len(byName) != 1 {
		return Chart{}, fmt.Errorf("want one chart with directory or name %q, found %d", component, len(byName))
	}
	return byName[0], nil
}
