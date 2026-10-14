package main

import (
	"context"

	"github.com/compliance-framework/workflows/internal/automerge"
	"github.com/compliance-framework/workflows/internal/bump"
)

// mergeAll merges the green ccf-bump:automerge PRs of repos as the token's identity, so the
// ccf-review bypass applies (GitHub's native auto-merge never applies it; docs/ccf-bump.md): those
// from a ccf-bump/* branch of the repo itself, opened by --author and not labelled needs-human.
// Renovate, release-please and people's PRs never qualify. Listing or reading a PR failing fails
// the run; a PR it leaves (failing, conflicting, refused) is a warning.
func (b *bumper) mergeAll(ctx context.Context, repos []string) error {
	m := &automerge.Merger{
		GH: b.e.gh, Tool: "ccf-bump", Owner: b.o.owner,
		Label: bump.AutomergeLabel, BranchPrefix: "ccf-bump/", Author: b.o.author, Required: b.o.required,
		Wait: b.o.wait, DryRun: b.o.dryRun,
		Out: b.e.stdout, Warn: b.warn, Now: b.e.now, Sleep: b.e.sleep,
	}
	return m.MergeAll(ctx, repos)
}
