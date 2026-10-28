package bump

import (
	"context"
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

	"golang.org/x/mod/modfile"
)

// goImage moves the `FROM golang:<version>[-variant][@sha256:...]` images of the root
// Dockerfiles up to the go line of the root go.mod, once go get has moved it (FollowGo); Decide
// never plans them. A tag that is not a version (golang:alpine, golang:latest) is left alone.
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
			tag, _, _ := strings.Cut(m[3], "@")
			refs = append(refs, Ref{Updater: g.Name(), Dep: DepGo, Key: m[2], Current: tag, File: f})
		}
	}
	return refs, nil
}

// Apply writes each change's version, keeping the tag's variant. An image pinned by digest gets
// the new tag's digest (Config.Digest), the multi-platform index digest Renovate pins: the old
// digest is the old tag's, and dropping it would leave the image unpinned until Renovate pins it
// again.
func (g goImage) Apply(ctx context.Context, dir string, changes []Change) error {
	files, cs := byFile(changes)
	digests := map[string]string{} // image:tag -> digest, across files
	for _, f := range files {
		ms, err := scanLines(dir, f, fromGolang.re)
		if err != nil {
			return err
		}
		repl := map[string]string{} // tag@digest as written -> its replacement
		for _, m := range ms {
			tag, _, pinned := strings.Cut(m[3], "@")
			c, ok := find(cs[f], m[2], tag)
			if !ok {
				continue
			}
			newTag := Bare(c.To)
			if v := goTag.FindStringSubmatch(tag); v != nil {
				newTag += v[2] // the variant: -alpine
			}
			if !pinned {
				repl[m[2]+":"+m[3]] = newTag
				continue
			}
			ref := m[2] + ":" + newTag
			d, ok := digests[ref]
			if !ok {
				if g.c.Digest == nil {
					return fmt.Errorf("%s: %s is pinned by digest and no registry is configured to read %s's", f, m[2]+":"+tag, ref)
				}
				if d, err = g.c.Digest(ctx, ref); err != nil {
					return fmt.Errorf("%s: digest of %s: %w", f, ref, err)
				}
				if !digestRE.MatchString(d) {
					return fmt.Errorf("%s: digest of %s: %q is not a sha256 digest", f, ref, d)
				}
				digests[ref] = d
			}
			repl[m[2]+":"+m[3]] = newTag + "@" + d
		}
		err = fromGolang.rewrite(dir, f, func(m []string) (string, bool) {
			v, ok := repl[m[2]+":"+m[3]]
			return v, ok
		})
		if err != nil {
			return err
		}
	}
	return nil
}

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// goImageTarget returns the version a golang image tag moves to so that it builds a module with
// go line goLine, and whether it must move. A full version (1.26.1, 1.27rc1) moves to goLine. A
// floating tag (1.26, 1) keeps its precision: it moves to goLine's minor (or major) only when it
// is older, so golang:1.26 stays for go 1.26.3. A tag that is not a version never moves; nor
// does one at or above goLine (ccf-bump never downgrades).
func goImageTarget(tag, goLine string) (string, bool) {
	m := goTag.FindStringSubmatch(tag)
	if m == nil || !goversion.IsValid("go"+goLine) {
		return "", false
	}
	v := m[1]
	target := goLine
	if parts := strings.Split(v, "."); len(parts) < 3 && !prerelease(v) && !prerelease(goLine) {
		if lp := strings.Split(goLine, "."); len(lp) > len(parts) {
			target = strings.Join(lp[:len(parts)], ".")
		}
	}
	return target, goversion.Compare("go"+v, "go"+target) < 0
}

// prerelease reports whether a Go version is a release candidate or beta (1.27rc1).
func prerelease(v string) bool { return strings.IndexFunc(v, unicode.IsLetter) >= 0 }

// FollowGo moves the golang builder images of the root Dockerfiles (DepGo) up to the go line of
// the root go.mod, after ApplyAll applied the plan to the checkout in dir: go get raises a
// consumer's go line to the highest of its dependencies', and a builder image older than it fails
// the image build. It does so only when the plan moves a Go module. It returns the plan with the
// images' changes (applied) and up-to-date pins added, and the changes alone, for the output.
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
	f, err := modfile.ParseLax("go.mod", data, nil)
	if err != nil {
		return p, nil, err
	}
	if f.Go == nil {
		return p, nil, nil
	}
	refs, err := ScanAll(dir, us[i:i+1])
	if err != nil {
		return p, nil, err
	}
	var changes []Change
	var current []Ref
	for _, r := range refs {
		switch to, move := goImageTarget(r.Current, f.Go.Version); {
		case move:
			changes = append(changes, Change{Ref: r, To: Canonical(to)})
		case to != "":
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
