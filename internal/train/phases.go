package train

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/compliance-framework/workflows/internal/bump"
	"github.com/compliance-framework/workflows/internal/release"
)

// bump runs ccf-bump with what earlier stages released, then merges its PR.
func (e *Engine) bump(ctx context.Context, st *State, r *RepoState) error {
	sets := st.released(r.Stage)
	if len(sets) == 0 {
		r.next(ReleasePR)
		return nil
	}
	if r.BumpPR == 0 {
		// Always run ccf-bump rather than adopt an open bump PR: one left from an earlier train
		// would pin older versions. A rerun the same day updates the PR it opened.
		res, err := e.Bumper.Bump(ctx, st.Manifest, r.Name, sets, false)
		if err != nil {
			r.hold(Blocked, true, "ccf-bump failed: %v; `/retry %s` runs it again", err, r.Name)
			return nil
		}
		if res.PR == 0 {
			r.next(ReleasePR)
			return nil
		}
		r.BumpPR = res.PR
	}
	pr, err := e.Repos.PR(ctx, r.Name, r.BumpPR)
	if err != nil {
		return err
	}
	switch {
	case pr.Merged:
		r.next(ReleasePR)
	case !pr.Open:
		r.hold(NeedsHuman, false, "bump PR %s was closed without merging; `/retry %s` bumps again, `/skip %s` skips the repo", e.prLink(r.Name, pr.Number), r.Name, r.Name)
	case !slices.Contains(pr.Labels, bump.AutomergeLabel) && !pr.AutoMerge: // AutoMerge: a PR from an older ccf-bump
		r.hold(NeedsHuman, false, "ccf-bump left %s to a person (no %s label: a major update, or a pin that was not a version); review and merge it by hand", e.prLink(r.Name, pr.Number), bump.AutomergeLabel)
	default:
		merged, err := e.mergeGreen(ctx, r, pr, e.requiredChecks(false))
		if merged {
			r.next(ReleasePR)
		}
		return err
	}
	return nil
}

// requiredChecks are the checks that gate a merge: CICheck (or --required-check) on every PR, and
// ReleaseCheck too on a release-please PR.
func (e *Engine) requiredChecks(releasePR bool) []string {
	required := []string{cmp.Or(e.RequiredCheck, CICheck)}
	if releasePR {
		required = append(required, ReleaseCheck)
	}
	return required
}

// mergeGreen merges pr once its required checks pass, and reports whether it did.
func (e *Engine) mergeGreen(ctx context.Context, r *RepoState, pr *PR, required []string) (bool, error) {
	checks, err := e.Repos.Checks(ctx, r.Name, pr.HeadSHA)
	if err != nil {
		return false, err
	}
	switch state, detail := EvaluateChecks(checks, required); state {
	case ChecksPending:
		r.wait("waiting for checks on %s: %s", e.prLink(r.Name, pr.Number), detail)
		return false, nil
	case ChecksFailing:
		r.hold(Blocked, false, "checks failing on %s at %s: %s", e.prLink(r.Name, pr.Number), short(pr.HeadSHA), detail)
		return false, nil
	}
	if err := e.Repos.Merge(ctx, r.Name, pr.Number, pr.HeadSHA); ExpectsCheck(err) {
		// A run of a required check started after the checks were read: the next reconcile retries.
		r.wait("waiting for checks on %s: GitHub still expects a required check", e.prLink(r.Name, pr.Number))
		return false, nil
	} else if err != nil {
		r.hold(Blocked, false, "merging %s failed: %v", e.prLink(r.Name, pr.Number), err)
		return false, nil
	}
	e.logf("%s: merged #%d", r.Name, pr.Number)
	return true, nil
}

// releasePR waits for release-please's run on the default branch's head, then takes its
// release PR, or finds nothing to release. The run is the signal, not the PR's base: for
// commits that release nothing (ci:, chore:) release-please leaves its PR behind the branch.
func (e *Engine) releasePR(ctx context.Context, r *RepoState) error {
	branch, sha, err := e.Repos.DefaultBranch(ctx, r.Name)
	if err != nil {
		return err
	}
	run, err := e.releasePleaseRun(ctx, r.Name, sha)
	switch {
	case err != nil:
		return err
	case run == nil || run.Status != "completed":
		r.wait("waiting for release-please on %s", short(sha))
		return nil
	case run.Conclusion != "success":
		r.hold(Blocked, false, "release-please failed on %s: %s", short(sha), run.URL)
		return nil
	}
	pr, err := e.Repos.OpenPR(ctx, r.Name, ReleaseBranchPrefix+branch)
	switch {
	case err != nil:
		return err
	case pr == nil:
		r.next(Released)
		r.Detail = "nothing to release"
	default:
		r.ReleasePR = pr.Number
		r.next(Merging)
	}
	return nil
}

func (e *Engine) releasePleaseRun(ctx context.Context, repo, sha string) (*Run, error) {
	runs, err := e.Repos.Runs(ctx, repo, "push", sha)
	if err != nil {
		return nil, err
	}
	var latest *Run
	for i, run := range runs {
		if strings.HasSuffix(run.Path, "/"+e.ReleasePleaseWorkflow) && (latest == nil || run.ID > latest.ID) {
			latest = &runs[i]
		}
	}
	return latest, nil
}

// merge merges the release PR once its checks pass and it raises no unapproved major.
func (e *Engine) merge(ctx context.Context, r *RepoState) error {
	pr, err := e.Repos.PR(ctx, r.Name, r.ReleasePR)
	if err != nil {
		return err
	}
	if pr.Fork {
		// OpenPR never adopts a fork's PR, but a state written before it checked could hold one.
		r.ReleasePR = 0
		r.next(ReleasePR)
		r.Detail = fmt.Sprintf("%s is from a fork, not release-please: ignored", e.prLink(r.Name, pr.Number))
		return nil
	}
	if !pr.Merged && !pr.Open {
		r.ReleasePR = 0
		r.next(ReleasePR)
		r.Detail = fmt.Sprintf("release PR %s was closed", e.prLink(r.Name, pr.Number))
		return nil
	}
	if !pr.Merged {
		majors, err := e.majors(ctx, r.Name, pr)
		if err != nil {
			return err
		}
		if len(majors) > 0 && !slices.Contains(pr.Labels, release.MajorApprovedLabel) {
			r.hold(NeedsHuman, false, "%s raises a major version (%s): add the `%s` label to release it, or `/skip %s`",
				e.prLink(r.Name, pr.Number), strings.Join(majors, "; "), release.MajorApprovedLabel, r.Name)
			return nil
		}
		if merged, err := e.mergeGreen(ctx, r, pr, e.requiredChecks(true)); !merged {
			return err
		}
		if pr, err = e.Repos.PR(ctx, r.Name, pr.Number); err != nil || !pr.Merged {
			return err
		}
	}
	base, err := e.manifestAt(ctx, r.Name, pr.BaseSHA)
	if err != nil {
		return err
	}
	head, err := e.manifestAt(ctx, r.Name, pr.MergeSHA)
	if err != nil {
		return err
	}
	r.Versions = map[string]string{}
	for pkg, v := range head {
		if base[pkg] != v {
			r.Versions[pkg] = v
		}
	}
	r.MergeSHA = pr.MergeSHA
	r.next(Publishing)
	return nil
}

func (e *Engine) majors(ctx context.Context, repo string, pr *PR) ([]string, error) {
	base, err := e.manifestAt(ctx, repo, pr.BaseSHA)
	if err != nil {
		return nil, err
	}
	head, err := e.manifestAt(ctx, repo, pr.HeadSHA)
	if err != nil {
		return nil, err
	}
	return release.MajorIncreases(base, head)
}

// manifestAt reads the release-please manifest at ref; none is an empty one.
func (e *Engine) manifestAt(ctx context.Context, repo, ref string) (map[string]string, error) {
	data, err := e.Repos.File(ctx, repo, ref, ReleasePleaseManifestFile)
	if err != nil || data == nil {
		return map[string]string{}, err
	}
	return release.ParseManifest(data)
}

// publish waits for release-please's tags on the merge commit and their release workflows.
func (e *Engine) publish(ctx context.Context, r *RepoState) error {
	tags, err := e.Repos.TagsAt(ctx, r.Name, r.MergeSHA)
	if err != nil {
		return err
	}
	tags = releaseTags(tags, r.Versions)
	slices.Sort(tags)
	r.Tags = tags
	if len(tags) < len(r.Versions) {
		run, err := e.releasePleaseRun(ctx, r.Name, r.MergeSHA)
		switch {
		case err != nil:
			return err
		case run != nil && run.Status == "completed" && run.Conclusion != "success":
			r.hold(Blocked, false, "release-please failed after merging %s: %s", e.prLink(r.Name, r.ReleasePR), run.URL)
		default:
			r.wait("waiting for release-please to tag %s", short(r.MergeSHA))
		}
		return nil
	}
	runs, err := e.Repos.Runs(ctx, r.Name, "release", r.MergeSHA)
	if err != nil {
		return err
	}
	latest := map[string]Run{} // tag and workflow -> latest run
	for _, run := range runs {
		k := run.HeadBranch + " " + run.Path
		if l, ok := latest[k]; slices.Contains(tags, run.HeadBranch) && (!ok || run.ID > l.ID) {
			latest[k] = run
		}
	}
	var pending []string
	for _, tag := range tags {
		found := false
		for _, k := range slices.Sorted(maps.Keys(latest)) {
			run := latest[k]
			if run.HeadBranch != tag {
				continue
			}
			found = true
			if run.Status != "completed" {
				pending = append(pending, tag)
			} else if run.Conclusion != "success" {
				r.FailedRun = run.ID
				r.hold(Blocked, false, "the release workflow failed for %s: %s; re-run it, or `/retry %s`", tag, run.URL, r.Name)
				return nil
			}
		}
		if !found {
			pending = append(pending, tag)
		}
	}
	if len(pending) > 0 {
		r.wait("waiting for the release workflow of %s", strings.Join(pending, ", "))
		return nil
	}
	r.FailedRun = 0
	r.next(Released)
	return nil
}

// releaseTags keeps release-please's release tags among tags: [<component>-]vX.Y.Z[-<pre-release>]
// whose version is one this train released (any, when versions is empty). A release workflow
// moves floating tags (v0, v1, latest) onto the same commit, and no release workflow runs for them.
func releaseTags(tags []string, versions map[string]string) []string {
	proposed := slices.Collect(maps.Values(versions))
	return slices.DeleteFunc(tags, func(tag string) bool {
		_, v, err := release.ComponentTag(tag)
		if err != nil {
			return true
		}
		return len(proposed) > 0 && !slices.Contains(proposed, v)
	})
}
