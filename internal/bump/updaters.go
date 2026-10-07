package bump

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Updater finds one kind of pin in a checkout and rewrites it.
type Updater interface {
	Name() string
	Scan(dir string) ([]Ref, error)
	// Apply rewrites the given changes, all found by this updater's Scan.
	Apply(ctx context.Context, dir string, changes []Change) error
}

// Runner runs a command in dir (go get, go mod tidy, the ui sync script).
type Runner func(ctx context.Context, dir, name string, args ...string) error

// Config says which updaters apply to a repo and how; the caller derives it from the manifest.
type Config struct {
	Owner     string // GitHub org, e.g. "compliance-framework"
	Source    string // action repos: the repo whose image the Dockerfile's source stage uses
	UIDep     string // ui repos: the repo whose tag the sync script takes
	OPASource string // policy repos: the repo whose go.mod sets the OPA version ("opa" pins)
	Run       Runner
}

// Updaters returns every updater for c; one with nothing to do in a checkout finds no refs.
func Updaters(c Config) []Updater {
	us := []Updater{goMod{c}, goInstall{c}, workflowRef{c}}
	if c.Source != "" {
		us = append(us, dockerfile{c})
	}
	if c.UIDep != "" {
		us = append(us, uiScript{c})
	}
	if c.OPASource != "" {
		us = append(us, opaVersion{})
	}
	return us
}

// ScanAll runs every updater's Scan.
func ScanAll(dir string, us []Updater) ([]Ref, error) {
	var refs []Ref
	for _, u := range us {
		r, err := u.Scan(dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", u.Name(), err)
		}
		refs = append(refs, r...)
	}
	return refs, nil
}

// ApplyAll hands each updater its changes.
func ApplyAll(ctx context.Context, dir string, us []Updater, changes []Change) error {
	for _, u := range us {
		var mine []Change
		for _, c := range changes {
			if c.Updater == u.Name() {
				mine = append(mine, c)
			}
		}
		if len(mine) == 0 {
			continue
		}
		if err := u.Apply(ctx, dir, mine); err != nil {
			return fmt.Errorf("%s: %w", u.Name(), err)
		}
	}
	return nil
}

// workflowFiles lists .github/workflows/*.yml and *.yaml, relative to dir.
func workflowFiles(dir string) ([]string, error) {
	var out []string
	for _, pat := range []string{"*.yml", "*.yaml"} {
		m, err := filepath.Glob(filepath.Join(dir, ".github", "workflows", pat))
		if err != nil {
			return nil, err
		}
		for _, f := range m {
			rel, _ := filepath.Rel(dir, f)
			out = append(out, filepath.ToSlash(rel))
		}
	}
	sort.Strings(out)
	return out, nil
}

// lineEdit is a regexp whose submatch `group` holds a version, applied line by line.
type lineEdit struct {
	re    *regexp.Regexp
	group int
}

// scanLines returns, for each match in file, the full submatches.
func scanLines(dir, file string, re *regexp.Regexp) ([][]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, file))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return re.FindAllStringSubmatch(string(data), -1), nil
}

// rewrite replaces submatch e.group in every match for which to returns a new value.
func (e lineEdit) rewrite(dir, file string, to func(m []string) (string, bool)) error {
	path := filepath.Join(dir, file)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	src := string(data)
	var b strings.Builder
	last := 0
	for _, idx := range e.re.FindAllStringSubmatchIndex(src, -1) {
		m := make([]string, len(idx)/2)
		for i := range m {
			if idx[2*i] >= 0 {
				m[i] = src[idx[2*i]:idx[2*i+1]]
			}
		}
		v, ok := to(m)
		if !ok {
			continue
		}
		s, end := idx[2*e.group], idx[2*e.group+1]
		b.WriteString(src[last:s])
		b.WriteString(v)
		last = end
	}
	b.WriteString(src[last:])
	return os.WriteFile(path, []byte(b.String()), info.Mode().Perm())
}

// byFile groups changes by file, in a stable order.
func byFile(changes []Change) ([]string, map[string][]Change) {
	m := map[string][]Change{}
	for _, c := range changes {
		m[c.File] = append(m[c.File], c)
	}
	files := make([]string, 0, len(m))
	for f := range m {
		files = append(files, f)
	}
	slices.Sort(files)
	return files, m
}

// find returns the change in cs for the pin of key at current; a pin of the same key at another
// version (up to date, or a conflict) is not one of them.
func find(cs []Change, key, current string) (Change, bool) {
	for _, c := range cs {
		if c.Key == key && c.Current == current {
			return c, true
		}
	}
	return Change{}, false
}
