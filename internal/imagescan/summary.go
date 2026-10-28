package imagescan

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// MaxSummaryRows caps the findings table of one repo in the step summary (GitHub keeps 1 MiB of
// it); the job log has every finding.
const MaxSummaryRows = 150

// WriteSummary writes the step summary: per repo, a table of the images scanned, a table of their
// HIGH and CRITICAL findings (fixable or not), the rules planned and the notes. It is the only
// report image-scan makes.
func WriteSummary(w io.Writer, results []RepoResult) error {
	var b strings.Builder
	b.WriteString("# Image scan\n\n")
	if len(results) == 0 {
		b.WriteString("No image repos selected.\n")
	}
	for _, r := range results {
		fmt.Fprintf(&b, "## %s\n\n", r.Repo)
		targets := append(slices.Clone(r.Targets), r.Published...)
		if len(targets) > 0 {
			b.WriteString("| Image | Where | Findings | HIGH/CRITICAL | Actionable |\n| --- | --- | --- | --- | --- |\n")
			for _, t := range targets {
				total, high, act := "-", "-", "-"
				if t.Scan != nil {
					total = fmt.Sprint(len(t.Scan.Findings))
					high = fmt.Sprint(countIf(t.Scan.Findings, func(f Finding) bool { return f.Score >= 7 }))
					act = fmt.Sprint(len(actionable(t.Scan.Findings)))
				} else if t.Err != nil {
					total = "scan failed"
				}
				fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", t.Base.Ref, where(t), total, high, act)
			}
			b.WriteString("\n")
		}
		var rows []string
		seen := map[string]bool{}
		for _, t := range targets {
			if t.Scan == nil {
				continue
			}
			for _, f := range t.Scan.Findings {
				if f.Score < 7 {
					continue
				}
				row := fmt.Sprintf("| `%s` | %s | %s | %s | %s | %s | %s | %s |", t.Base.Ref, where(t), f.Package, f.Type, f.Installed, orDash(strings.Join(f.Fixed, ", ")), f.Severity(), strings.Join(f.CVEs(), ", "))
				if !seen[row] {
					seen[row] = true
					rows = append(rows, row)
				}
			}
		}
		if len(rows) > 0 {
			b.WriteString("| Image | Where | Package | Type | Installed | Fixed | Severity | IDs |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
			for i, row := range rows {
				if i == MaxSummaryRows {
					fmt.Fprintf(&b, "\n%d more in the job log.\n", len(rows)-i)
					break
				}
				b.WriteString(row + "\n")
			}
			b.WriteString("\n")
		}
		if len(r.Rules) > 0 {
			fmt.Fprintf(&b, "Renovate rules: %d.\n\n", len(r.Rules))
		}
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "- **error**: %s\n", e)
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteLog writes every finding, one line each, for the job log.
func WriteLog(w io.Writer, results []RepoResult) error {
	for _, r := range results {
		for _, t := range append(slices.Clone(r.Targets), r.Published...) {
			if t.Scan == nil {
				continue
			}
			for _, f := range t.Scan.Findings {
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Repo, t.Base.Ref, where(t), f.Package, f.Type, f.Installed, orDash(strings.Join(f.Fixed, ",")), f.Severity(), strings.Join(append(slices.Clone(f.IDs), f.Aliases...), ",")); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func where(t Target) string {
	if t.Base.Role == "" {
		return "published"
	}
	return fmt.Sprintf("%s, %s stage %s", t.Dockerfile, t.Base.Role, t.Base.Stage)
}

func countIf(fs []Finding, pred func(Finding) bool) int {
	n := 0
	for _, f := range fs {
		if pred(f) {
			n++
		}
	}
	return n
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
