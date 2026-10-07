// Package release holds the rules behind the release workflows, starting with the
// release-please PR checks (release-checks.yml). Versions without a "v" are X.Y.Z, as in
// .release-please-manifest.json.
package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// OrgPrefix is the module path prefix of every compliance-framework module, mocks included.
const OrgPrefix = "github.com/compliance-framework/"

// MajorApprovedLabel is the PR label that allows a major version increase.
const MajorApprovedLabel = "release:major-approved"

// NonFinalDeps lists the compliance-framework requirements in a go.mod that aren't a final
// semver (pre-releases and pseudo-versions), and every replace of a compliance-framework module.
func NonFinalDeps(gomod []byte) ([]string, error) {
	f, err := modfile.Parse("go.mod", gomod, nil)
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, r := range f.Require {
		v := r.Mod.Version
		if strings.HasPrefix(r.Mod.Path, OrgPrefix) && (!semver.IsValid(v) || semver.Prerelease(v) != "") {
			bad = append(bad, r.Mod.Path+"@"+v)
		}
	}
	for _, r := range f.Replace {
		if strings.HasPrefix(r.Old.Path, OrgPrefix) {
			bad = append(bad, fmt.Sprintf("replace %s => %s %s", r.Old.Path, r.New.Path, r.New.Version))
		}
	}
	return bad, nil
}

// CheckModulePath checks that the go.mod module path's major suffix matches version: none
// for v0 and v1, /vN for N >= 2.
func CheckModulePath(gomod []byte, version string) error {
	f, err := modfile.Parse("go.mod", gomod, nil)
	if err != nil {
		return err
	}
	if f.Module == nil {
		return errors.New("go.mod has no module directive")
	}
	if !semver.IsValid("v" + version) {
		return fmt.Errorf("invalid version %q", version)
	}
	_, pathMajor, ok := module.SplitPathVersion(f.Module.Mod.Path)
	if !ok {
		return fmt.Errorf("invalid module path %q", f.Module.Mod.Path)
	}
	return module.CheckPathMajor("v"+version, pathMajor)
}

// ParseManifest reads a release-please manifest: package path -> version.
func ParseManifest(b []byte) (map[string]string, error) {
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("release-please manifest: %w", err)
	}
	return m, nil
}

// MajorIncreases lists the packages whose major version is higher in head than in base
// (0.x to 1.0 included). A package missing from base counts as 0.0.0.
func MajorIncreases(base, head map[string]string) ([]string, error) {
	var out []string
	for _, pkg := range slices.Sorted(maps.Keys(head)) {
		from, to := "v"+base[pkg], "v"+head[pkg]
		if base[pkg] == "" {
			from = "v0.0.0"
		}
		if !semver.IsValid(from) || !semver.IsValid(to) {
			return nil, fmt.Errorf("package %q: invalid version %q -> %q", pkg, base[pkg], head[pkg])
		}
		if semver.Compare(semver.Major(to), semver.Major(from)) > 0 {
			out = append(out, fmt.Sprintf("%s: %s -> %s", pkg, from, to))
		}
	}
	return out, nil
}
