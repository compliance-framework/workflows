package bump

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// helm bumps, in each charts/<name>/ chart, the values.yaml image tags of the org's images
// (`repository: ghcr.io/<owner>/<repo>` next to a non-empty `tag`) and the Chart.yaml appVersion.
// appVersion tracks the first org image, in values order, whose tag is empty (it defaults to
// appVersion) or equals appVersion. Edits are made in place, so comments and layout are kept.
// Chart versions are release-please's.
type helm struct{ c Config }

func (helm) Name() string { return "helm" }

// image is one org image in values.yaml: its tag node and YAML path.
type image struct {
	dep, path string
	tag       *yaml.Node
}

func parseYAML(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &doc, nil
}

// images walks n in document order and returns the org images under it.
func (h helm) images(n *yaml.Node, path string) []image {
	var out []image
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			out = append(out, h.images(c, path)...)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			out = append(out, h.images(c, path+"["+strconv.Itoa(i)+"]")...)
		}
	case yaml.MappingNode:
		repo, tag := mapValue(n, "repository"), mapValue(n, "tag")
		if repo != nil && tag != nil && repo.Kind == yaml.ScalarNode && tag.Kind == yaml.ScalarNode {
			if rest, ok := strings.CutPrefix(repo.Value, "ghcr.io/"+h.c.Owner+"/"); ok && rest != "" && !strings.Contains(rest, "/") {
				out = append(out, image{dep: rest, path: join(path, "tag"), tag: tag})
			}
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, h.images(n.Content[i+1], join(path, n.Content[i].Value))...)
		}
	}
	return out
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// chart returns a chart's refs and, by "<file>\x00<key>", the nodes they point at.
func (h helm) chart(dir, chartDir string) ([]Ref, map[string]*yaml.Node, error) {
	nodes := map[string]*yaml.Node{}
	var refs []Ref
	valuesFile := filepath.ToSlash(filepath.Join(chartDir, "values.yaml"))
	var imgs []image
	if _, err := os.Stat(filepath.Join(dir, valuesFile)); err == nil {
		values, err := parseYAML(filepath.Join(dir, valuesFile))
		if err != nil {
			return nil, nil, err
		}
		imgs = h.images(values, "")
	}
	for _, img := range imgs {
		if img.tag.Value == "" {
			continue // follows appVersion
		}
		refs = append(refs, Ref{Updater: h.Name(), Dep: img.dep, Key: img.path, Current: img.tag.Value, File: valuesFile})
		nodes[valuesFile+"\x00"+img.path] = img.tag
	}
	chartFile := filepath.ToSlash(filepath.Join(chartDir, "Chart.yaml"))
	chart, err := parseYAML(filepath.Join(dir, chartFile))
	if err != nil {
		return nil, nil, err
	}
	app := mapValue(chart, "appVersion")
	if app == nil || app.Kind != yaml.ScalarNode {
		return refs, nodes, nil
	}
	for _, img := range imgs {
		if img.tag.Value == "" || Bare(img.tag.Value) == Bare(app.Value) {
			refs = append(refs, Ref{Updater: h.Name(), Dep: img.dep, Key: "appVersion", Current: app.Value, File: chartFile})
			nodes[chartFile+"\x00appVersion"] = app
			break
		}
	}
	return refs, nodes, nil
}

func (h helm) charts(dir string) ([]string, error) {
	m, err := filepath.Glob(filepath.Join(dir, "charts", "*", "Chart.yaml"))
	if err != nil {
		return nil, err
	}
	out := make([]string, len(m))
	for i, f := range m {
		out[i], _ = filepath.Rel(dir, filepath.Dir(f))
	}
	return out, nil
}

func (h helm) Scan(dir string) ([]Ref, error) {
	charts, err := h.charts(dir)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, c := range charts {
		r, _, err := h.chart(dir, c)
		if err != nil {
			return nil, err
		}
		refs = append(refs, r...)
	}
	return refs, nil
}

func (h helm) Apply(_ context.Context, dir string, changes []Change) error {
	charts, err := h.charts(dir)
	if err != nil {
		return err
	}
	nodes := map[string]*yaml.Node{}
	for _, c := range charts {
		_, n, err := h.chart(dir, c)
		if err != nil {
			return err
		}
		for k, v := range n {
			nodes[k] = v
		}
	}
	files, cs := byFile(changes)
	for _, f := range files {
		path := filepath.Join(dir, f)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.SplitAfter(string(data), "\n")
		for _, c := range cs[f] {
			n := nodes[f+"\x00"+c.Key]
			if n == nil {
				return fmt.Errorf("%s: %s not found", f, c.Key)
			}
			if lines[n.Line-1], err = setScalar(lines[n.Line-1], n, Bare(c.To)); err != nil {
				return fmt.Errorf("%s:%d: %w", f, n.Line, err)
			}
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// setScalar replaces scalar n, which starts on line at n.Column, keeping its quoting style.
func setScalar(line string, n *yaml.Node, v string) (string, error) {
	start := n.Column - 1
	if start < 0 || start >= len(line) {
		return "", errors.New("scalar position out of range")
	}
	var end int
	switch n.Style {
	case yaml.DoubleQuotedStyle, yaml.SingleQuotedStyle:
		q := line[start]
		i := strings.IndexByte(line[start+1:], q)
		if i < 0 {
			return "", errors.New("unterminated quoted scalar")
		}
		end = start + 1 + i + 1
		v = string(q) + v + string(q)
	case 0:
		end = start + len(n.Value)
		if !strings.HasPrefix(line[start:], n.Value) {
			return "", fmt.Errorf("expected %q", n.Value)
		}
	default:
		return "", errors.New("only plain or quoted scalars can be rewritten")
	}
	return line[:start] + v + line[end:], nil
}
