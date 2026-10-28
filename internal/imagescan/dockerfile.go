// Package imagescan finds fixable HIGH and CRITICAL vulnerabilities in the CCF container images and
// plans their fixes as Renovate package rules for renovate.yml (docs/image-scan.md): it parses the
// repos' Dockerfiles, scans their base images and published images with osv-scanner plus a
// runtime check against OSV, and turns what a newer base image fixes into overrides that
// `renovate-config config --overrides` appends to the preset's packageRules.
package imagescan

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Role says which stage of a Dockerfile a base image is in.
type Role string

const (
	// RoleFinal is the base image of the final stage (the last FROM, which publish-image.yml
	// builds, since it passes no --target), followed through `FROM <stage>`.
	RoleFinal Role = "final"
	// RoleBuild is the base image of any other stage.
	RoleBuild Role = "build"
)

// Stage is one FROM instruction of a Dockerfile.
type Stage struct {
	Index    int    // 0 for the first FROM
	Name     string // the AS alias as written; "" without one
	From     string // the image or stage it builds on, with the global ARGs substituted
	Platform string // the --platform flag, substituted; "" without one
	Line     int    // the 1-based line of the FROM keyword
	End      int    // the 1-based last line of the FROM instruction (after its continuations)
	Parent   int    // the index of the stage From names (FROM <alias>), or -1 for an image
}

// Scratch reports whether the stage starts from the empty image.
func (s Stage) Scratch() bool { return s.Parent < 0 && strings.EqualFold(s.From, "scratch") }

// Dockerfile is the parsed stage structure of a Dockerfile.
type Dockerfile struct {
	Stages []Stage
}

// Base is a base image of a Dockerfile: a FROM that names an image, not a stage or scratch.
type Base struct {
	Ref   Ref
	Stage string // the stage's alias, or its index when it has none
	Role  Role
	Line  int
}

// ParseDockerfile reads the FROM instructions of src: their stages and aliases, --platform, the
// ARG defaults declared before the first FROM (the only ARGs a FROM can use), and line
// continuations. Other instructions are skipped.
func ParseDockerfile(src []byte) (*Dockerfile, error) {
	args := map[string]string{}
	d := &Dockerfile{}
	for _, in := range instructions(string(src)) {
		keyword, rest, _ := strings.Cut(in.text, " ")
		rest = strings.TrimSpace(rest)
		switch strings.ToUpper(keyword) {
		case "ARG":
			if len(d.Stages) > 0 {
				continue // a stage's ARG is only visible inside that stage
			}
			for _, f := range strings.Fields(rest) {
				name, value, _ := strings.Cut(f, "=")
				args[name] = unquote(expand(value, args))
			}
		case "FROM":
			s, err := parseFrom(rest, args, d.Stages)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", in.line, err)
			}
			s.Index, s.Line, s.End = len(d.Stages), in.line, in.end
			d.Stages = append(d.Stages, s)
		}
	}
	if len(d.Stages) == 0 {
		return nil, errors.New("no FROM instruction")
	}
	return d, nil
}

func parseFrom(rest string, args map[string]string, earlier []Stage) (Stage, error) {
	s := Stage{Parent: -1}
	fields := strings.Fields(rest)
	for len(fields) > 0 && strings.HasPrefix(fields[0], "--") {
		if v, ok := strings.CutPrefix(fields[0], "--platform="); ok {
			s.Platform = expand(v, args)
		}
		fields = fields[1:]
	}
	switch {
	case len(fields) == 1:
	case len(fields) == 3 && strings.EqualFold(fields[1], "AS"):
		s.Name = fields[2]
	default:
		return s, fmt.Errorf("FROM %s: want FROM [--platform=...] <image> [AS <name>]", rest)
	}
	s.From = expand(fields[0], args)
	if s.From == "" {
		return s, fmt.Errorf("FROM %s: the image is empty after ARG substitution", fields[0])
	}
	for i := len(earlier) - 1; i >= 0; i-- {
		if earlier[i].Name != "" && strings.EqualFold(earlier[i].Name, s.From) {
			s.Parent = i
			break
		}
	}
	return s, nil
}

// Final returns the index of the final stage: the last one, as publish-image.yml builds without
// --target.
func (d *Dockerfile) Final() int { return len(d.Stages) - 1 }

// FinalBase returns the index of the stage that starts from the final image's base: the final
// stage, followed back through FROM <stage>.
func (d *Dockerfile) FinalBase() int {
	final := d.Final()
	for d.Stages[final].Parent >= 0 {
		final = d.Stages[final].Parent
	}
	return final
}

// Bases returns the image each stage starts from, skipping scratch and FROM <stage>, in order. The
// stage the final stage builds on, through any FROM <stage> chain, is RoleFinal.
func (d *Dockerfile) Bases() ([]Base, error) {
	final := d.FinalBase()
	var out []Base
	for _, s := range d.Stages {
		if s.Parent >= 0 || s.Scratch() {
			continue
		}
		ref, err := ParseRef(s.From)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", s.Line, err)
		}
		b := Base{Ref: ref, Stage: s.Name, Role: RoleBuild, Line: s.Line}
		if b.Stage == "" {
			b.Stage = strconv.Itoa(s.Index)
		}
		if s.Index == final {
			b.Role = RoleFinal
		}
		out = append(out, b)
	}
	return out, nil
}

type instruction struct {
	line, end int // its first and last line
	text      string
}

// instructions splits a Dockerfile into instructions: comment lines dropped (also inside a
// continuation, as Docker does), lines ending in a backslash joined to the next (blank lines
// inside a continuation skipped), and here-document bodies (RUN <<EOF ... EOF) skipped, so a body
// line that starts with FROM is not read as one.
func instructions(src string) []instruction {
	var out []instruction
	var cur []string
	start := 0
	heredoc := ""
	for i, line := range strings.Split(src, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if heredoc != "" {
			if trimmed == heredoc {
				heredoc = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		if len(cur) == 0 {
			start = i + 1
		}
		if body, ok := strings.CutSuffix(trimmed, `\`); ok {
			cur = append(cur, strings.TrimSpace(body))
			continue
		}
		cur = append(cur, trimmed)
		text := strings.Join(strings.Fields(strings.Join(cur, " ")), " ")
		out = append(out, instruction{line: start, end: i + 1, text: text})
		cur = nil
		if m := heredocStart.FindStringSubmatch(text); m != nil {
			heredoc = m[1]
		}
	}
	if len(cur) > 0 {
		out = append(out, instruction{line: start, end: strings.Count(src, "\n") + 1, text: strings.Join(strings.Fields(strings.Join(cur, " ")), " ")})
	}
	return out
}

// heredocStart finds the first here-document of an instruction (<<EOF, <<-EOF, <<"EOF").
var heredocStart = regexp.MustCompile(`<<-?["']?([A-Za-z_][A-Za-z0-9_]*)["']?`)

// expand substitutes $NAME, ${NAME}, ${NAME:-default}, ${NAME-default}, ${NAME:+alt} and
// ${NAME+alt} from args; an undeclared ARG is empty, as in Docker.
func expand(s string, args map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		if s[i+1] == '{' {
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				b.WriteString(s[i:])
				break
			}
			b.WriteString(substitute(s[i+2:i+end], args))
			i += end
			continue
		}
		j := i + 1
		for j < len(s) && (s[j] == '_' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9') {
			j++
		}
		if j == i+1 {
			b.WriteByte(s[i])
			continue
		}
		b.WriteString(args[s[i+1:j]])
		i = j - 1
	}
	return b.String()
}

func substitute(expr string, args map[string]string) string {
	for _, op := range []string{":-", ":+", "-", "+"} {
		name, word, ok := strings.Cut(expr, op)
		if !ok {
			continue
		}
		value, set := args[name]
		switch op {
		case ":-":
			if value == "" {
				return word
			}
		case "-":
			if !set {
				return word
			}
		case ":+":
			if value != "" {
				return word
			}
			return ""
		case "+":
			if set {
				return word
			}
			return ""
		}
		return value
	}
	return args[expr]
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
