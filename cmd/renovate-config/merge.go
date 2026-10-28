package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/automerge"
	"github.com/compliance-framework/workflows/internal/manifest"
	"github.com/compliance-framework/workflows/internal/renovate"
)

// mergeGitHub is what merge needs from the API; *bump.GitHub implements it.
type mergeGitHub interface {
	automerge.GitHub
	automerge.CommitAuthors
}

// mergeEnv is everything merge depends on, so tests can fake it.
type mergeEnv struct {
	github func(owner string) mergeGitHub // the API client for the repos of owner
	stdout io.Writer
	now    func() time.Time
	sleep  func(time.Duration)
}

// merge merges the green Renovate PRs of --repos (renovate.yml passes its run's shard, which its
// token is scoped to) as the token's identity: Renovate itself merges at most one PR per repo per
// run (docs/renovate.md#the-merge-pass). A PR qualifies when it is labelled renovate:automerge,
// from a renovate/* branch, or an image-scan/* one (image-scan's OS package patches,
// docs/image-scan.md#os-package-patches), of the repo itself, opened by --author, every commit authored by
// --author (nobody pushed to it), and not labelled needs-human; it merges once the newest run of
// --required-check on its head succeeded. --dry-run off merges; any other value prints what it
// would merge.
func merge(ctx context.Context, args []string, e mergeEnv) error {
	fs := flag.NewFlagSet("renovate-config merge", flag.ContinueOnError)
	path := fs.String("manifest", manifest.DefaultPath, "path to the repo manifest")
	repos := fs.String("repos", "", "comma- or space-separated repos to merge in (required: the run's shard, the token's scope)")
	owner := fs.String("owner", "compliance-framework", "the repos' owner")
	dryRun := fs.String("dry-run", "full", "off merges; full, lookup or extract print what it would merge")
	wait := fs.Duration("wait", 0, "poll PRs whose required check is pending for up to this long")
	author := fs.String("author", "ccf-release-bot[bot]", "the bot's login (the token's identity): only its PRs and commits merge")
	required := fs.String("required-check", "ci / required", "the check that must pass on a PR's head")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(*repos) == "" {
		return errors.New("merge takes --repos (the run's shard, which the token is scoped to)")
	}
	m, err := manifest.Load(*path)
	if err != nil {
		return err
	}
	selected, err := renovate.SelectRepos(m, *repos, *dryRun)
	if err != nil {
		return err
	}
	gh := e.github(*owner)
	mg := &automerge.Merger{
		GH: gh, Commits: gh, Tool: "renovate-config", Owner: *owner,
		Label: renovate.AutomergeLabel, BranchPrefixes: []string{"renovate/", "image-scan/"}, Author: *author, Required: *required,
		Wait: *wait, DryRun: *dryRun != renovate.DryRunOff,
		Out: e.stdout, Now: e.now, Sleep: e.sleep,
		Warn: func(format string, args ...any) {
			fmt.Fprintln(e.stdout, "::warning::renovate-config: "+fmt.Sprintf(format, args...))
		},
	}
	return mg.MergeAll(ctx, selected)
}
