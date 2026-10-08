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
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/semver"
)

// workflowRef moves the callers' shared-workflow pins, a job's
// `uses: <owner>/workflows/<path>@<ref>`, to the target (rules: Plan.decideWorkflows). A release is
// written `@<sha> # vX.Y.Z`: the comment is added, or its version replaced, keeping any other text.
// Moving a job's pin also deletes the job's `workflows-ref` input with its comment (the reusable
// workflows build their tools at the commit they are called at), and the `with:` it leaves
// empty. Edits are made line by line, so comments and layout stay.
type workflowRef struct{ c Config }

func (workflowRef) Name() string { return "workflow ref" }

// workflowsInput is the input callers passed to notify-failure.yml to choose the ref of its tools.
const workflowsInput = "workflows-ref"

// call is one job's call of a shared workflow.
type call struct {
	ref       Ref
	uses      *yaml.Node // the uses: value
	with, key *yaml.Node // the with: mapping and its key, or nil
}

// calls parses file and returns its jobs' calls of the shared workflows, and its lines.
func (w workflowRef) calls(dir, file string) ([]call, []string, error) {
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", file, err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	jobs := mapValue(&doc, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return nil, lines, nil
	}
	var out []call
	for i := 1; i < len(jobs.Content); i += 2 {
		uses := mapValue(jobs.Content[i], "uses")
		if uses == nil || uses.Kind != yaml.ScalarNode || !strings.HasPrefix(uses.Value, w.c.Owner+"/workflows/") {
			continue
		}
		key, ref, ok := strings.Cut(uses.Value, "@")
		if !ok || ref == "" {
			continue
		}
		r := Ref{Updater: w.Name(), Dep: DepWorkflows, Key: key, Current: ref, File: file}
		if _, tail, ok := splitPinLine(lines[uses.Line-1], uses.Value); ok {
			if m := pinTail.FindStringSubmatch(tail); m != nil {
				r.Version, _ = splitVersion(m[2])
			}
		}
		c := call{ref: r, uses: uses}
		c.key, c.with = mapEntry(jobs.Content[i], "with")
		out = append(out, c)
	}
	return out, lines, nil
}

func (w workflowRef) Scan(dir string) ([]Ref, error) {
	files, err := workflowFiles(dir)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, f := range files {
		cs, _, err := w.calls(dir, f)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			refs = append(refs, c.ref)
		}
	}
	return refs, nil
}

func (w workflowRef) Apply(_ context.Context, dir string, changes []Change) error {
	files, cs := byFile(changes)
	for _, f := range files {
		if err := w.apply(dir, f, cs[f]); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	return nil
}

func (w workflowRef) apply(dir, file string, changes []Change) error {
	calls, lines, err := w.calls(dir, file)
	if err != nil {
		return err
	}
	drop := map[int]bool{} // line numbers to delete
	for _, c := range calls {
		// Two jobs may pin the same SHA with different version comments, and only one move.
		i := slices.IndexFunc(changes, func(ch Change) bool { return ch.Ref == c.ref })
		if i < 0 {
			continue
		}
		ch := changes[i]
		n := c.uses.Line - 1
		head, tail, ok := splitPinLine(lines[n], c.uses.Value)
		if !ok {
			return fmt.Errorf("line %d: can't find %q", c.uses.Line, c.uses.Value)
		}
		lines[n] = head + pinText(ch.To, tail)
		if c.with == nil {
			continue
		}
		k, v := mapEntry(c.with, workflowsInput)
		if k == nil {
			continue
		}
		if c.with.Style&yaml.FlowStyle != 0 || v.Line != k.Line {
			return fmt.Errorf("line %d: remove the %s input by hand (only a one-line entry of a block `with:` is removed)", k.Line, workflowsInput)
		}
		drop[k.Line] = true
		for n := k.Line - 1; k.HeadComment != "" && n > 0 && strings.HasPrefix(strings.TrimSpace(lines[n-1]), "#"); n-- {
			drop[n] = true // the input's own comment
		}
		if len(c.with.Content) == 2 { // the only input: drop `with:` too
			drop[c.key.Line] = true
		}
	}
	var b strings.Builder
	for i, l := range lines {
		if !drop[i+1] {
			b.WriteString(l)
		}
	}
	info, err := os.Stat(filepath.Join(dir, file))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, file), []byte(b.String()), info.Mode().Perm())
}

// pinTail matches what follows a pin's ref on its line: a closing quote, and a comment.
var pinTail = regexp.MustCompile(`^(["']?)(?:[ \t]+#[ \t]*([^\r\n]*?))?[ \t]*(\r?\n)?$`)

// versionWord matches a version at the start of a comment.
var versionWord = regexp.MustCompile(`^(v[0-9][0-9A-Za-z.+-]*)(?:[ \t]+|$)`)

// splitPinLine splits line around the ref of the uses: value: head ends with "@", tail starts
// after the ref.
func splitPinLine(line, value string) (head, tail string, ok bool) {
	i, at := strings.Index(line, value), strings.Index(value, "@")
	if i < 0 || at < 0 {
		return "", "", false
	}
	return line[:i+at+1], line[i+len(value):], true
}

// splitVersion splits a pin's comment into its leading version, if any, and the rest.
func splitVersion(comment string) (version, rest string) {
	if m := versionWord.FindStringSubmatch(comment); m != nil && semver.IsValid(m[1]) {
		return m[1], strings.TrimSpace(comment[len(m[0]):])
	}
	return "", comment
}

// pinText is the new pin, to (a WorkflowsPin or a ref), followed by the rest of the old line,
// tail: the version comment is replaced by to's tag, or dropped for a ref without one.
func pinText(to, tail string) string {
	ref, tag := SplitWorkflowsPin(to)
	m := pinTail.FindStringSubmatch(tail)
	if m == nil { // more YAML follows on the line (a flow mapping): only the ref changes
		return ref + tail
	}
	_, rest := splitVersion(m[2])
	comment := strings.TrimSpace(tag + " " + rest)
	if comment != "" {
		comment = " # " + comment
	}
	return ref + m[1] + comment + m[3]
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
