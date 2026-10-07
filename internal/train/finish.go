package train

import (
	"context"
	"fmt"
	"strings"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// finish writes the digest draft, opens the chart issues and posts the summary.
func (e *Engine) finish(ctx context.Context, is Issue, st *State) error {
	var notes []Notes
	for _, r := range st.Repos {
		for _, tag := range r.Tags {
			body, url, err := e.Repos.Release(ctx, r.Name, tag)
			if err != nil {
				return err
			}
			notes = append(notes, Notes{Repo: r.Name, Tag: tag, URL: url, Body: body})
		}
	}
	if st.Digest == "" {
		url, err := e.Tracker.Comment(ctx, is.Number, Digest(st.Month, st.Repos, notes))
		if err != nil {
			return err
		}
		st.Digest = url
	}
	charts, err := e.chartIssues(ctx, st, false)
	if err != nil {
		return err
	}
	st.Status = StatusFinished
	msg := fmt.Sprintf(":white_check_mark: *Release train %s finished*: %s. <%s|Digest draft>.", st.Month, st.versions(), st.Digest)
	if len(charts) > 0 {
		msg += " Chart issues: " + strings.Join(charts, ", ") + "."
	}
	e.notify(ctx, st, "finished", msg)
	return nil
}

// chartIssues opens, in each helm repo of the manifest, an issue listing its go-service and
// ui dependencies that went up a minor, so the charts get their new settings. With dryRun it
// only describes them.
func (e *Engine) chartIssues(ctx context.Context, st *State, dryRun bool) ([]string, error) {
	m, err := e.Load(st.Manifest)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, h := range m.Repos {
		if h.Kind != manifest.KindHelm {
			continue
		}
		var lines []string
		for _, d := range h.DependsOn {
			if r := st.Repo(d); r != nil && MinorUp(r.From[RootPackage], r.Version()) {
				lines = append(lines, fmt.Sprintf("- [ ] %s: v%s → v%s", d, r.From[RootPackage], r.Version()))
			}
		}
		if len(lines) == 0 {
			continue
		}
		title := fmt.Sprintf("Release train %s: minor releases to roll into the charts", st.Month)
		if dryRun {
			out = append(out, fmt.Sprintf("%s: %q (%d repos)", h.Name, title, len(lines)))
			continue
		}
		body := fmt.Sprintf("Release train %s released new minor versions of repos these charts deploy (release notes: "+
			"[digest draft](%s)). Check them for new settings, defaults and migrations, and update the charts' values "+
			"and templates:\n\n%s\n", st.Month, st.Digest, strings.Join(lines, "\n"))
		url, err := e.Repos.EnsureIssue(ctx, h.Name, title, body)
		if err != nil {
			return nil, fmt.Errorf("%s issue: %w", h.Name, err)
		}
		out = append(out, url)
	}
	return out, nil
}
