package bump

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// workflowRef moves `uses: <owner>/workflows/<path>@<ref>` to the target ref. It has a target only
// when one is given (--set workflows=<ref>); any ref other than the target is replaced.
type workflowRef struct{ c Config }

func (workflowRef) Name() string { return "workflow ref" }

func (w workflowRef) edit() lineEdit {
	return lineEdit{re: regexp.MustCompile(`uses:\s*(` + regexp.QuoteMeta(w.c.Owner) + `/workflows/[^@\s]+)@([^\s#"']+)`), group: 2}
}

func (w workflowRef) Scan(dir string) ([]Ref, error) {
	files, err := workflowFiles(dir)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, f := range files {
		ms, err := scanLines(dir, f, w.edit().re)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			refs = append(refs, Ref{Updater: w.Name(), Dep: DepWorkflows, Key: m[1], Current: m[2], File: f})
		}
	}
	return refs, nil
}

func (w workflowRef) Apply(_ context.Context, dir string, changes []Change) error {
	files, cs := byFile(changes)
	for _, f := range files {
		err := w.edit().rewrite(dir, f, func(m []string) (string, bool) {
			c, ok := find(cs[f], m[1], m[2])
			return c.To, ok
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// opaVersion sets policy repos' `opa-version:` workflow inputs to the OPA version in the agent's
// go.mod (the caller resolves the DepOPA target from Config.OPASource).
type opaVersion struct{}

func (opaVersion) Name() string { return "opa-version" }

// The value must look like a version, so an expression (${{ inputs.opa }}) is left alone.
var opaEdit = lineEdit{re: regexp.MustCompile(`(?m)^([ \t]*opa-version:[ \t]*["']?)(v?[0-9][0-9A-Za-z.+-]*)`), group: 2}

func (o opaVersion) Scan(dir string) ([]Ref, error) {
	files, err := workflowFiles(dir)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, f := range files {
		ms, err := scanLines(dir, f, opaEdit.re)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			refs = append(refs, Ref{Updater: o.Name(), Dep: DepOPA, Key: "opa-version", Current: m[2], File: f})
		}
	}
	return refs, nil
}

func (opaVersion) Apply(_ context.Context, dir string, changes []Change) error {
	files, cs := byFile(changes)
	for _, f := range files {
		err := opaEdit.rewrite(dir, f, func(m []string) (string, bool) {
			c, ok := find(cs[f], "opa-version", m[2])
			return Bare(c.To), ok
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// dockerfile rewrites the image of the Dockerfile's `FROM <image> AS source` stage, whatever it
// is, to ghcr.io/<owner>/<source>:<version> (release images are tagged without the "v").
type dockerfile struct{ c Config }

func (dockerfile) Name() string { return "Dockerfile" }

var fromSource = lineEdit{re: regexp.MustCompile(`(?mi)^FROM\s+(\S+)\s+AS\s+source\s*$`), group: 1}

func (d dockerfile) image() string { return "ghcr.io/" + d.c.Owner + "/" + d.c.Source }

func (d dockerfile) Scan(dir string) ([]Ref, error) {
	ms, err := scanLines(dir, "Dockerfile", fromSource.re)
	if err != nil || len(ms) == 0 {
		return nil, err
	}
	if len(ms) > 1 {
		return nil, errors.New("Dockerfile has more than one `FROM ... AS source` stage")
	}
	cur := ms[0][1]
	if tag, ok := strings.CutPrefix(cur, d.image()+":"); ok {
		cur = tag
	}
	return []Ref{{Updater: d.Name(), Dep: d.c.Source, Key: d.image(), Current: cur, File: "Dockerfile"}}, nil
}

func (d dockerfile) Apply(_ context.Context, dir string, changes []Change) error {
	to := d.image() + ":" + Bare(changes[0].To)
	return fromSource.rewrite(dir, "Dockerfile", func([]string) (string, bool) { return to, true })
}

// uiScript runs the ui repo's sync script with the API tag. The scripts are known by path;
// versionFile/versionKey, when set, say where the script records the tag (the current version).
type uiScript struct{ c Config }

var uiScripts = []struct{ script, versionFile, versionKey string }{
	{script: "scripts/sync-agentconfig-conformance.sh"},
	{script: "scripts/sync-mock-api-version.sh", versionFile: "src/api-version.json", versionKey: "mockApi"},
}

func (uiScript) Name() string { return "ui sync" }

func (u uiScript) Scan(dir string) ([]Ref, error) {
	var refs []Ref
	for _, s := range uiScripts {
		if _, err := os.Stat(filepath.Join(dir, s.script)); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		cur := ""
		if s.versionFile != "" {
			data, err := os.ReadFile(filepath.Join(dir, s.versionFile))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			var m map[string]any
			if len(data) > 0 {
				if err := json.Unmarshal(data, &m); err != nil {
					return nil, fmt.Errorf("%s: %w", s.versionFile, err)
				}
			}
			cur, _ = m[s.versionKey].(string)
		}
		refs = append(refs, Ref{Updater: u.Name(), Dep: u.c.UIDep, Key: s.script, Current: cur, File: s.script})
	}
	return refs, nil
}

func (u uiScript) Apply(ctx context.Context, dir string, changes []Change) error {
	for _, c := range changes {
		if err := u.c.Run(ctx, dir, "bash", c.Key, c.To); err != nil {
			return err
		}
	}
	return nil
}
