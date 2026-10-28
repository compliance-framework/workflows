package bump

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	goversion "go/version"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/google/go-containerregistry/pkg/crane"
	"golang.org/x/mod/modfile"
)

// goImage moves the `FROM golang:<version>[-variant][@sha256:...]` images of the root
// Dockerfiles so they build the go line of the root go.mod, once go get has moved it (FollowGo,
// which plans them; Decide never does). A Ref's Current is the tag with its digest, if any
// (1.26.1-alpine@sha256:...), and a Change's To is the pin to write in its place. A tag that is
// not a version (golang:alpine, golang:latest) is left alone.
type goImage struct{ c Config }

func (goImage) Name() string { return "golang image" }

// fromGolang matches a FROM of the golang image, after any flags (--platform=...): group 2 is
// the image name as written, group 3 its tag with the digest, if any.
var fromGolang = lineEdit{
	re:    regexp.MustCompile(`(?mi)^([ \t]*FROM[ \t]+(?:--\S+[ \t]+)*)((?:docker\.io/)?(?:library/)?golang):([A-Za-z0-9_][A-Za-z0-9_.-]*(?:@sha256:[0-9a-f]{64})?)(?:[ \t\r]|$)`),
	group: 3,
}

// goTag splits a golang image tag into its version (1, 1.26, 1.26.1, 1.27rc1) and variant suffix
// (-alpine, -bookworm, -alpine3.22).
var goTag = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+){0,2}(?:(?:rc|beta)[0-9]+)?)(-.+)?$`)

// goPinVersion returns the Go version of a golang image pin (tag[@digest]): 1.26 for
// 1.26-alpine@sha256:...; "" when its tag is not a version.
func goPinVersion(pin string) string {
	tag, _, _ := strings.Cut(pin, "@")
	if m := goTag.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	return ""
}

// dockerfiles lists the Dockerfiles in dir's root: Dockerfile, Dockerfile-ci, Dockerfile.dev,
// release.Dockerfile.
func dockerfiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && (strings.HasPrefix(n, "Dockerfile") || strings.HasSuffix(n, ".Dockerfile")) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (g goImage) Scan(dir string) ([]Ref, error) {
	files, err := dockerfiles(dir)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, f := range files {
		ms, err := scanLines(dir, f, fromGolang.re)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			refs = append(refs, Ref{Updater: g.Name(), Dep: DepGo, Key: m[2], Current: m[3], File: f})
		}
	}
	return refs, nil
}

// Apply writes each change's pin (To, planned and checked against the registry by FollowGo).
func (g goImage) Apply(_ context.Context, dir string, changes []Change) error {
	files, cs := byFile(changes)
	for _, f := range files {
		err := fromGolang.rewrite(dir, f, func(m []string) (string, bool) {
			c, ok := find(cs[f], m[2], m[3])
			return c.To, ok
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Registry reads the golang images FollowGo moves, anonymously.
type Registry interface {
	// Digest returns the digest ref (name:tag) points at: a multi-platform index's own digest,
	// the one Renovate pins. It fails when the tag does not exist.
	Digest(ctx context.Context, ref string) (string, error)
	// GoVersion returns the Go an image (name:tag or name@digest) ships: its GOLANG_VERSION.
	GoVersion(ctx context.Context, ref string) (string, error)
}

// Crane is the Registry, with go-containerregistry.
type Crane struct{}

// Digest returns ref's digest.
func (Crane) Digest(ctx context.Context, ref string) (string, error) {
	return crane.Digest(ref, crane.WithContext(ctx))
}

// GoVersion reads GOLANG_VERSION from ref's image config (linux/amd64 of an index; the official
// images set it to the same version on every platform).
func (Crane) GoVersion(ctx context.Context, ref string) (string, error) {
	data, err := crane.Config(ref, crane.WithContext(ctx))
	if err != nil {
		return "", err
	}
	var cfg struct {
		Config struct{ Env []string } `json:"config"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("config of %s: %w", ref, err)
	}
	for _, kv := range cfg.Config.Env {
		if v, ok := strings.CutPrefix(kv, "GOLANG_VERSION="); ok {
			return v, nil
		}
	}
	return "", fmt.Errorf("config of %s sets no GOLANG_VERSION", ref)
}

// RegistryError is a failure to read the registry while planning the golang images (FollowGo):
// nothing was written. ccf-bump fails the repo's bump on it, and only warns in a dry run.
type RegistryError struct{ Err error }

func (e *RegistryError) Error() string { return "registry: " + e.Err.Error() }
func (e *RegistryError) Unwrap() error { return e.Err }

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// goImageTarget returns the version a golang image tag moves to so that it builds a module with
// go line goLine, and whether it must move. A full version (1.26.1, 1.27rc1) moves to goLine. A
// floating tag (1.26, 1) keeps its precision: it moves to goLine's minor (or major) only when it
// is older; golang:1.26 meets go 1.26.3 by name, and FollowGo checks the Go it actually ships. A
// tag that is not a version never moves; nor does one at or above goLine (ccf-bump never
// downgrades).
func goImageTarget(tag, goLine string) (string, bool) {
	m := goTag.FindStringSubmatch(tag)
	if m == nil || !goversion.IsValid("go"+goLine) {
		return "", false
	}
	v := m[1]
	target := goLine
	if floating(v) && !prerelease(goLine) {
		parts := strings.Split(v, ".")
		if lp := strings.Split(goLine, "."); len(lp) > len(parts) {
			target = strings.Join(lp[:len(parts)], ".")
		}
	}
	return target, goversion.Compare("go"+v, "go"+target) < 0
}

// floating reports whether a golang image version names a release line rather than a release
// (1.26, 1): the tag moves to each new patch, and so does its image.
func floating(v string) bool { return len(strings.Split(v, ".")) < 3 && !prerelease(v) }

// prerelease reports whether a Go version is a release candidate or beta (1.27rc1).
func prerelease(v string) bool { return strings.IndexFunc(v, unicode.IsLetter) >= 0 }

// goImagePlanner plans the golang image pins of one checkout against its go line, reading the
// registry once per reference.
type goImagePlanner struct {
	reg      Registry
	goLine   string
	digests  map[string]string
	versions map[string]string
}

func (p *goImagePlanner) digest(ctx context.Context, ref string) (string, error) {
	if d, ok := p.digests[ref]; ok {
		return d, nil
	}
	if p.reg == nil {
		return "", &RegistryError{fmt.Errorf("no registry to read %s", ref)}
	}
	d, err := p.reg.Digest(ctx, ref)
	if err == nil && !digestRE.MatchString(d) {
		err = fmt.Errorf("%q is not a sha256 digest", d)
	}
	if err != nil {
		return "", &RegistryError{fmt.Errorf("digest of %s: %w", ref, err)}
	}
	p.digests[ref] = d
	return d, nil
}

// builds reports whether the image ref ships a Go at least the go line.
func (p *goImagePlanner) builds(ctx context.Context, ref string) (bool, error) {
	v, ok := p.versions[ref]
	if !ok {
		if p.reg == nil {
			return false, &RegistryError{fmt.Errorf("no registry to read %s", ref)}
		}
		var err error
		if v, err = p.reg.GoVersion(ctx, ref); err == nil && !goversion.IsValid("go"+v) {
			err = fmt.Errorf("GOLANG_VERSION %q is not a Go version", v)
		}
		if err != nil {
			return false, &RegistryError{fmt.Errorf("the Go version of %s: %w", ref, err)}
		}
		p.versions[ref] = v
	}
	return goversion.Compare("go"+v, "go"+p.goLine) >= 0, nil
}

// plan returns the pin r moves to, or "" when it already builds the go line or is no version.
//
//   - A full version below the go line moves to it; a floating tag below it, to its line (1.25 to
//     1.26) when that tag's image builds the go line, else to the go line; either keeps its
//     variant.
//   - A floating tag that meets the go line by name is checked against the Go it ships: the
//     image its digest pins (Renovate pins every FROM), or the tag's current image when unpinned.
//     A pinned one that is behind moves to the tag's current digest when that image builds the go
//     line. Otherwise the tag moves to the go line as a full version would.
//
// A new tag must exist: its digest is read (and written when the pin had one), so a variant no
// longer published fails the plan instead of reaching a PR.
func (p *goImagePlanner) plan(ctx context.Context, r Ref) (string, error) {
	tag, pinned, hasDigest := strings.Cut(r.Current, "@")
	m := goTag.FindStringSubmatch(tag)
	if m == nil {
		return "", nil
	}
	variant := m[2]
	target, below := goImageTarget(tag, p.goLine)
	newTag := ""
	switch {
	case below:
		newTag = target + variant
		if floating(target) { // a new release line: its tag's current image must build the go line
			ok, err := p.builds(ctx, r.Key+":"+newTag)
			if err != nil {
				return "", err
			}
			if !ok {
				newTag = p.goLine + variant
			}
		}
	case !floating(m[1]):
		return "", nil // a release at or above the go line
	default:
		ref := r.Key + ":" + tag
		if hasDigest {
			ref = r.Key + "@" + pinned
		}
		ok, err := p.builds(ctx, ref)
		if err != nil || ok {
			return "", err
		}
		if hasDigest {
			d, err := p.digest(ctx, r.Key+":"+tag)
			if err != nil {
				return "", err
			}
			if d != pinned {
				ok, err := p.builds(ctx, r.Key+"@"+d)
				if err != nil {
					return "", err
				}
				if ok {
					return tag + "@" + d, nil
				}
			}
		}
		newTag = p.goLine + variant // not published for the go line yet: name the release
	}
	d, err := p.digest(ctx, r.Key+":"+newTag)
	if err != nil {
		return "", err
	}
	if hasDigest {
		return newTag + "@" + d, nil
	}
	return newTag, nil
}

// toolchainVersion matches a go.mod toolchain name (go1.26.6, go1.27rc1, go1.26.6-custom): group 1
// is its version.
var toolchainVersion = regexp.MustCompile(`^go([0-9]+(?:\.[0-9]+){0,2}(?:(?:rc|beta)[0-9]+)?)(?:[-+].*)?$`)

// buildGo returns the Go a module builds with: its toolchain line's version when that is higher
// than its go line (toolchain go1.26.6 over go 1.26.1), else its go line; "" without a valid go
// line. A Go that runs the module with GOTOOLCHAIN=local must be at least the go line, and builds
// it with that Go; the toolchain line is the one setup-go installs and Renovate moves for
// standard-library fixes (docs/renovate.md), so the images follow it too.
func buildGo(f *modfile.File) string {
	if f.Go == nil || !goversion.IsValid("go"+f.Go.Version) {
		return ""
	}
	line := f.Go.Version
	if f.Toolchain != nil {
		if m := toolchainVersion.FindStringSubmatch(f.Toolchain.Name); m != nil && goversion.Compare("go"+m[1], "go"+line) > 0 {
			line = m[1]
		}
	}
	return line
}

// FollowGo moves the golang builder images of the root Dockerfiles (DepGo) so they build the Go
// the root go.mod asks for (buildGo: the higher of its go and toolchain lines; "the go line" in
// this file), after ApplyAll applied the plan to the checkout in dir: go get raises a consumer's
// go line to the highest of its dependencies'. An image older than that fails the build where it
// keeps the golang image's GOTOOLCHAIN=local, and downloads the toolchain on every build where the
// stage sets GOTOOLCHAIN=auto. It does so only when the plan moves a Go module. Every registry read
// happens before anything is written: a *RegistryError leaves the checkout as it was. It returns
// the plan with the images' changes (applied) and up-to-date pins added, and the changes alone,
// for the output.
func FollowGo(ctx context.Context, dir string, us []Updater, p Plan) (Plan, []Change, error) {
	i := slices.IndexFunc(us, func(u Updater) bool { _, ok := u.(goImage); return ok })
	if i < 0 || !slices.ContainsFunc(p.Changes, Change.GoModule) {
		return p, nil, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil, nil
	}
	if err != nil {
		return p, nil, err
	}
	f, err := modfile.Parse("go.mod", data, nil) // not ParseLax, which skips the toolchain line
	if err != nil {
		return p, nil, err
	}
	line := buildGo(f)
	if line == "" {
		return p, nil, nil
	}
	refs, err := ScanAll(dir, us[i:i+1])
	if err != nil {
		return p, nil, err
	}
	pl := &goImagePlanner{reg: us[i].(goImage).c.Registry, goLine: line, digests: map[string]string{}, versions: map[string]string{}}
	var changes []Change
	var current []Ref
	for _, r := range refs {
		to, err := pl.plan(ctx, r)
		switch {
		case err != nil:
			return p, nil, fmt.Errorf("%s %s:%s: %w", r.File, r.Key, r.Current, err)
		case to != "":
			changes = append(changes, Change{Ref: r, To: to})
		case goPinVersion(r.Current) != "":
			current = append(current, r)
		}
	}
	p.UpToDate = append(slices.Clone(p.UpToDate), current...)
	if len(changes) == 0 {
		return p, nil, nil
	}
	if err := us[i].Apply(ctx, dir, changes); err != nil {
		return p, nil, fmt.Errorf("%s: %w", us[i].Name(), err)
	}
	p.Changes = append(slices.Clone(p.Changes), changes...)
	return p, changes, nil
}
