package train

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// plan is the dry run: it walks every stage as if the earlier ones had released what their
// release PRs propose, runs ccf-bump --pr --dry-run with those versions, writes the plan as a
// comment and in the thread, and closes the issue. It merges and opens nothing.
func (e *Engine) plan(ctx context.Context, is Issue, st *State) error {
	var b, details strings.Builder
	var errs []error
	for stage := 1; stage <= st.Stages(); stage++ {
		var line []string
		for _, r := range st.Repos {
			if r.Stage != stage {
				continue
			}
			if err := e.planRepo(ctx, st, r, &details); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", r.Name, err))
				r.hold(Blocked, false, "planning failed: %v", err)
			}
			line = append(line, fmt.Sprintf("%s (%s)", r.Name, r.Detail))
		}
		fmt.Fprintf(&b, "%d. %s\n", stage, strings.Join(line, "; "))
	}
	charts, err := e.chartIssues(ctx, st, true)
	errs = append(errs, err)
	for _, c := range charts {
		fmt.Fprintf(&b, "\nWould open the chart issue %s.", c)
	}
	comment := "## Dry-run plan\n\nStages, in order:\n\n" + b.String() + "\n"
	if details.Len() > 0 {
		comment += "\n" + details.String()
	}
	if _, err := e.Tracker.Comment(ctx, is.Number, comment); err != nil {
		errs = append(errs, err)
	}
	e.notify(ctx, st, "plan", fmt.Sprintf(":clipboard: Dry-run plan for *%s* (nothing merged):\n%s", is.Title, SlackText(b.String())))
	st.Status = StatusFinished
	return errors.Join(errs...)
}

// planRepo plans one repo: its bump, the release PR and the version it would release.
func (e *Engine) planRepo(ctx context.Context, st *State, r *RepoState, details *strings.Builder) error {
	var notes []string
	bumped := false
	if sets := st.released(r.Stage); len(sets) > 0 {
		res, err := e.Bumper.Bump(ctx, st.Manifest, r.Name, sets, true)
		bumped = res.Changed || err != nil
		switch {
		case err != nil:
			// Planned versions aren't tagged yet, so pins that need the tag (a pseudo-version,
			// the OPA version) fail; the real run bumps after the release.
			notes = append(notes, fmt.Sprintf("ccf-bump couldn't plan (planned versions aren't tagged yet?): %v", err))
		case bumped:
			notes = append(notes, "ccf-bump would open a PR")
			fmt.Fprintf(details, "<details><summary>%s: ccf-bump --dry-run</summary>\n\n```text\n%s\n```\n</details>\n\n",
				r.Name, strings.ReplaceAll(clip(strings.TrimSpace(res.Output), maxBumpOutput), "```", "'''"))
		default:
			notes = append(notes, "nothing to bump")
		}
	}
	branch, _, err := e.Repos.DefaultBranch(ctx, r.Name)
	if err != nil {
		return err
	}
	pr, err := e.Repos.OpenPR(ctx, r.Name, ReleaseBranchPrefix+branch)
	if err != nil {
		return err
	}
	switch {
	case pr != nil:
		r.ReleasePR = pr.Number
		base, err := e.manifestAt(ctx, r.Name, pr.BaseSHA)
		if err != nil {
			return err
		}
		head, err := e.manifestAt(ctx, r.Name, pr.HeadSHA)
		if err != nil {
			return err
		}
		r.Versions = map[string]string{}
		for pkg, v := range head {
			if base[pkg] != v {
				r.Versions[pkg] = v
			}
		}
		notes = append(notes, fmt.Sprintf("would release %s from %s", Versions(r.Versions), e.prLink(r.Name, pr.Number)))
		majors, err := e.majors(ctx, r.Name, pr)
		if err != nil {
			return err
		}
		if len(majors) > 0 {
			notes = append(notes, "needs a human: a major version")
		}
		checks, err := e.Repos.Checks(ctx, r.Name, pr.HeadSHA)
		if err != nil {
			return err
		}
		if state, detail := EvaluateChecks(checks, e.requiredChecks(true)); state != ChecksGreen {
			notes = append(notes, fmt.Sprintf("checks %s: %s", state, detail))
		}
	case bumped && r.From[RootPackage] != "":
		r.Versions = map[string]string{RootPackage: nextPatch(r.From[RootPackage])}
		notes = append(notes, "the bump would make release-please propose "+Versions(r.Versions))
	case bumped:
		notes = append(notes, "the bump would make release-please propose a release")
	default:
		notes = append(notes, "nothing to release")
	}
	r.Detail = strings.Join(notes, "; ")
	return nil
}

// maxBumpOutput bounds each repo's ccf-bump output in the plan comment: it ends with the diff,
// go.sum included, and a comment holds at most 65536 characters.
const maxBumpOutput = 4000

// clip cuts s to n bytes, on a rune boundary, and says so.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "\n... (cut; the full output is in the run log)"
}

// nextPatch returns X.Y.(Z+1), or v unchanged if it is not X.Y.Z.
func nextPatch(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return v
	}
	z, err := strconv.Atoi(parts[2])
	if err != nil {
		return v
	}
	return fmt.Sprintf("%s.%s.%d", parts[0], parts[1], z+1)
}
