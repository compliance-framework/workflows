package pluginprobe

import (
	"fmt"
	"io"
	"strings"
)

// WriteMarkdown writes the report as markdown tables, for a job summary or a PR comment.
func (r Report) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	b.WriteString("## Plugin probe\n\n")
	fmt.Fprintf(&b, "Agent library %s, OPA %s.\n\n", orDash(r.AgentVersion), orDash(r.OPAVersion))
	if len(r.Plugins) > 0 {
		b.WriteString("| Plugin | Protocol | Agent library | Result | Notes |\n| --- | --- | --- | --- | --- |\n")
		for _, p := range r.Plugins {
			protocol := "-"
			if p.Protocol > 0 {
				protocol = fmt.Sprintf("v%d", p.Protocol)
			}
			notes := append([]string{}, p.Warnings...)
			if p.InitError != "" {
				notes = append(notes, "Init with an empty request: "+p.InitError)
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", cell(p.Source), protocol, cell(orDash(p.LibVersion)),
				result(p.Error), cell(strings.Join(notes, "; ")))
		}
		b.WriteString("\n")
	}
	if len(r.Policies) > 0 {
		b.WriteString("| Policy bundle | Modules | Result |\n| --- | --- | --- |\n")
		for _, p := range r.Policies {
			fmt.Fprintf(&b, "| `%s` | %d | %s |\n", cell(p.Source), p.Modules, result(p.Error))
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func result(err string) string {
	if err == "" {
		return "ok"
	}
	return "**failed**: " + cell(err)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// cell keeps s on one table row: no pipes or newlines.
func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "\r", " ", "\n", " ", "`", "'").Replace(s)
}
