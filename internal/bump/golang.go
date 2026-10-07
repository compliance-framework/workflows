package bump

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
)

// goMod bumps the direct requirements on the org's modules in the root go.mod, with
// `go get <mod>@<ver>...` then `go mod tidy`. Indirect requirements follow through tidy.
type goMod struct{ c Config }

func (goMod) Name() string { return "go.mod" }

// repoOf returns the repo a github.com/<owner>/<repo>[/...] path is in, or "".
func repoOf(owner, path string) string {
	rest, ok := strings.CutPrefix(path, "github.com/"+owner+"/")
	if !ok {
		return ""
	}
	repo, _, _ := strings.Cut(rest, "/")
	return repo
}

func (g goMod) Scan(dir string) ([]Ref, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f, err := modfile.ParseLax("go.mod", data, nil)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, r := range f.Require {
		repo := repoOf(g.c.Owner, r.Mod.Path)
		if r.Indirect || repo == "" || (f.Module != nil && r.Mod.Path == f.Module.Mod.Path) {
			continue
		}
		refs = append(refs, Ref{Updater: g.Name(), Dep: repo, Key: r.Mod.Path, Current: r.Mod.Version, File: "go.mod"})
	}
	return refs, nil
}

func (g goMod) Apply(ctx context.Context, dir string, changes []Change) error {
	args := []string{"get"}
	for _, c := range changes {
		args = append(args, c.Key+"@"+c.To)
	}
	if err := g.c.Run(ctx, dir, "go", args...); err != nil {
		return err
	}
	return g.c.Run(ctx, dir, "go", "mod", "tidy")
}

// goInstall bumps `go install github.com/<owner>/<repo>[/cmd/x]@<ver>` pins in workflow files.
type goInstall struct{ c Config }

func (goInstall) Name() string { return "go install" }

func (g goInstall) edit() lineEdit {
	return lineEdit{re: regexp.MustCompile(`go install (github\.com/` + regexp.QuoteMeta(g.c.Owner) + `/[A-Za-z0-9_.-]+[^@\s]*)@([^\s"'` + "`" + `]+)`), group: 2}
}

func (g goInstall) Scan(dir string) ([]Ref, error) {
	files, err := workflowFiles(dir)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, f := range files {
		ms, err := scanLines(dir, f, g.edit().re)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			refs = append(refs, Ref{Updater: g.Name(), Dep: repoOf(g.c.Owner, m[1]), Key: m[1], Current: m[2], File: f})
		}
	}
	return refs, nil
}

func (g goInstall) Apply(_ context.Context, dir string, changes []Change) error {
	files, cs := byFile(changes)
	for _, f := range files {
		err := g.edit().rewrite(dir, f, func(m []string) (string, bool) {
			c, ok := find(cs[f], m[1], m[2])
			return c.To, ok
		})
		if err != nil {
			return err
		}
	}
	return nil
}
