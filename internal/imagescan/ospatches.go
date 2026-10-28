package imagescan

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/compliance-framework/workflows/internal/bump"
	"github.com/compliance-framework/workflows/internal/renovate"
)

// PatchBranch is the branch image-scan owns in each repo for its OS package patches. Not under
// renovate/: Renovate deletes the renovate/* branches it doesn't know. renovate-config merge
// merges image-scan/* PRs as it does renovate/* ones.
const PatchBranch = "image-scan/os-patches"

// FilePatch is a change to one Dockerfile's managed block (PatchDockerfile).
type FilePatch struct {
	Dockerfile string
	Image      string    // the final image's base, as written
	Findings   []Finding // the actionable OS findings the block fixes; none when it is removed
	Packages   []string  // the packages it upgrades; none when it is removed
	Old, New   []byte
}

// Removes reports whether the patch only removes the block.
func (p FilePatch) Removes() bool { return len(p.Packages) == 0 }

// osPatches returns the managed-block changes of a repo's Dockerfiles (path -> source on the default
// branch). A Dockerfile gets a block for the actionable OS findings of its final image's base,
// unless rule 1 already moves that image to a newer one that fixes them (fixed), and loses its block
// once the base image has none. A base image that couldn't be scanned leaves its Dockerfile as is.
func osPatches(dockerfiles map[string][]byte, targets []Target, fixed map[string]bool, note func(string, ...any)) []FilePatch {
	var out []FilePatch
	for _, path := range slices.Sorted(maps.Keys(dockerfiles)) {
		src := dockerfiles[path]
		i := slices.IndexFunc(targets, func(t Target) bool { return t.Dockerfile == path && t.Base.Role == RoleFinal })
		if i < 0 {
			// A final stage from scratch: nothing to upgrade, but an old block must still go.
			if bytes.Contains(src, []byte(BlockBegin)) {
				note("%s: the final stage has no base image; its %s block is kept for a person to remove", path, BlockBegin)
			}
			continue
		}
		t := targets[i]
		if t.Scan == nil || fixed[t.Base.Ref.String()] {
			continue
		}
		var act []Finding
		var pkgs []string
		for _, f := range actionable(t.Scan.Findings) {
			if f.Type == TypeOS {
				act = append(act, f)
				if !slices.Contains(pkgs, f.Package) {
					pkgs = append(pkgs, f.Package)
				}
			}
		}
		slices.Sort(pkgs)
		if len(pkgs) > 0 && t.Scan.PackageManager == "" {
			note("%s: %s has %d actionable OS package finding(s) but no apk or apt-get to upgrade them with (distroless?); not patched", path, t.Base.Ref, len(act))
			pkgs, act = nil, nil
		}
		patched, err := PatchDockerfile(src, t.Scan.PackageManager, pkgs)
		if err != nil {
			note("%s: OS package patch: %v", path, err)
			continue
		}
		if bytes.Equal(patched, src) {
			continue
		}
		p := FilePatch{Dockerfile: path, Image: t.Base.Ref.WithTag(t.Base.Ref.Tag).String(), Findings: act, Packages: pkgs, Old: src, New: patched}
		if p.Removes() {
			note("%s: %s ships the fixes now; the OS package block goes", path, t.Base.Ref)
		} else {
			note("%s: OS package patch for %s: %s %s", path, t.Base.Ref, t.Scan.PackageManager, strings.Join(pkgs, " "))
		}
		out = append(out, p)
	}
	return out
}

// PatchGitHub is what Publisher needs from the API; *bump.GitHub implements it.
type PatchGitHub interface {
	DefaultBranch(ctx context.Context, repo string) (string, error)
	BranchHead(ctx context.Context, repo, branch string) (string, error)
	CommitTree(ctx context.Context, repo, sha string) (string, []string, error)
	CreateTree(ctx context.Context, repo, base string, files map[string][]byte) (string, error)
	CreateCommit(ctx context.Context, repo, message, tree, parent string, author bump.Identity) (string, error)
	SetBranch(ctx context.Context, repo, branch, sha string, create bool) error
	OpenPR(ctx context.Context, repo, branch string) (*bump.PR, error)
	PRCommitAuthors(ctx context.Context, repo string, number int) ([]string, error)
	CreatePR(ctx context.Context, repo, base, head, title, body string) (*bump.PR, error)
	UpdatePR(ctx context.Context, repo string, number int, title, body string) error
	AddLabel(ctx context.Context, repo string, number int, l bump.Label) error
	ClosePR(ctx context.Context, repo string, number int, comment string) error
	DeleteBranch(ctx context.Context, repo, branch string) error
}

// automergeLabel is the preset's label, which renovate.yml's merge pass merges by.
var automergeLabel = bump.Label{Name: renovate.AutomergeLabel, Color: "0e8a16", Description: "renovate.yml's merge pass merges this once CI is green"}

// Publisher keeps a repo's PatchBranch and its PR in line with the patches.
type Publisher struct {
	GH       PatchGitHub
	Author   string        // the bot's login, e.g. ccf-release-bot[bot]: the PR is left alone if anyone else committed
	Identity bump.Identity // the commits' author and committer; zero leaves them to GitHub (the token's app)
	DryRun   bool          // log what it would do; write nothing
	Out      io.Writer
}

// Publish rebuilds PatchBranch from the default branch's head with patches applied and opens or
// updates its PR, labelled renovate:automerge; with no patches it closes the PR and deletes the
// branch. A PR with a commit by anyone but Author is left alone (Renovate's modified-branch rule).
func (p *Publisher) Publish(ctx context.Context, repo string, patches []FilePatch) error {
	pr, err := p.GH.OpenPR(ctx, repo, PatchBranch)
	if err != nil {
		return fmt.Errorf("find the %s PR: %w", PatchBranch, err)
	}
	if pr != nil {
		authors, err := p.GH.PRCommitAuthors(ctx, repo, pr.Number)
		if err != nil {
			return fmt.Errorf("#%d: list commits: %w", pr.Number, err)
		}
		if !strings.EqualFold(pr.User.Login, p.Author) || !allBy(authors, p.Author) {
			fmt.Fprintf(p.Out, "%s#%d: not every commit is by %s; left alone\n", repo, pr.Number, p.Author)
			return nil
		}
	}
	if len(patches) == 0 {
		if pr == nil {
			return nil
		}
		if p.DryRun {
			fmt.Fprintf(p.Out, "%s#%d: dry run: would close it (nothing to patch) and delete %s\n", repo, pr.Number, PatchBranch)
			return nil
		}
		if err := p.GH.ClosePR(ctx, repo, pr.Number, "image-scan: the base images ship these fixes now (or a newer base image does), so there is nothing to patch."); err != nil {
			return fmt.Errorf("#%d: close: %w", pr.Number, err)
		}
		fmt.Fprintf(p.Out, "%s#%d: closed, nothing to patch\n", repo, pr.Number)
		return p.GH.DeleteBranch(ctx, repo, PatchBranch)
	}

	title, body := patchPRText(patches)
	if p.DryRun {
		fmt.Fprintf(p.Out, "%s: dry run: would push %s and open or update %q, labelled %s:\n", repo, PatchBranch, title, automergeLabel.Name)
		for _, fp := range patches {
			fmt.Fprintf(p.Out, "--- %s\n%s", fp.Dockerfile, diffLines(fp.Old, fp.New))
		}
		return nil
	}
	base, err := p.GH.DefaultBranch(ctx, repo)
	if err != nil {
		return err
	}
	head, err := p.GH.BranchHead(ctx, repo, base)
	if err != nil {
		return fmt.Errorf("head of %s: %w", base, err)
	}
	if head == "" {
		return fmt.Errorf("no branch %s", base)
	}
	baseTree, _, err := p.GH.CommitTree(ctx, repo, head)
	if err != nil {
		return err
	}
	files := map[string][]byte{}
	for _, fp := range patches {
		files[fp.Dockerfile] = fp.New
	}
	tree, err := p.GH.CreateTree(ctx, repo, baseTree, files)
	if err != nil {
		return fmt.Errorf("tree: %w", err)
	}
	cur, err := p.GH.BranchHead(ctx, repo, PatchBranch)
	if err != nil {
		return err
	}
	// The branch already is this change on the default branch's head: no push, no new CI run.
	same := false
	if cur != "" {
		curTree, parents, err := p.GH.CommitTree(ctx, repo, cur)
		if err != nil {
			return err
		}
		same = curTree == tree && slices.Equal(parents, []string{head})
	}
	if !same {
		commit, err := p.GH.CreateCommit(ctx, repo, title+"\n\n"+commitBody(patches), tree, head, p.Identity)
		if err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		if err := p.GH.SetBranch(ctx, repo, PatchBranch, commit, cur == ""); err != nil {
			return fmt.Errorf("push %s: %w", PatchBranch, err)
		}
		fmt.Fprintf(p.Out, "%s: pushed %s at %s\n", repo, PatchBranch, short(commit))
	}
	if pr == nil {
		if pr, err = p.GH.CreatePR(ctx, repo, base, PatchBranch, title, body); err != nil {
			return fmt.Errorf("open the PR: %w", err)
		}
		fmt.Fprintf(p.Out, "%s#%d: opened %q\n", repo, pr.Number, title)
	} else if pr.Title != title || !same {
		if err := p.GH.UpdatePR(ctx, repo, pr.Number, title, body); err != nil {
			return fmt.Errorf("#%d: update: %w", pr.Number, err)
		}
		fmt.Fprintf(p.Out, "%s#%d: updated %q\n", repo, pr.Number, title)
	}
	if !pr.HasLabel(automergeLabel.Name) {
		if err := p.GH.AddLabel(ctx, repo, pr.Number, automergeLabel); err != nil {
			return fmt.Errorf("#%d: label: %w", pr.Number, err)
		}
	}
	return nil
}

// patchPRText is the PR's title and body. The title is fix(deps) when a block upgrades packages,
// so the fix releases, and chore(deps) when the PR only removes blocks.
func patchPRText(patches []FilePatch) (string, string) {
	var images, removed, ids []string
	var b strings.Builder
	b.WriteString("image-scan (compliance-framework/workflows, `renovate.yml`) found fixable HIGH or CRITICAL vulnerabilities in OS packages that the distribution has fixed but the base image doesn't ship yet. This PR upgrades them in a managed block right after the final stage's `FROM`; a later PR removes the block once the base image ships the fixes ([docs](https://github.com/compliance-framework/workflows/blob/main/docs/image-scan.md#os-package-patches)).\n\n")
	b.WriteString("| Dockerfile | Base image | Package | Installed | Fixed | Severity | CVEs |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, p := range patches {
		if p.Removes() {
			removed = append(removed, fmt.Sprintf("- `%s`: `%s` ships the fixes now, so its block goes.", p.Dockerfile, p.Image))
			continue
		}
		if !slices.Contains(images, p.Image) {
			images = append(images, p.Image)
		}
		for _, f := range p.Findings {
			fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s | %s | %s |\n", p.Dockerfile, p.Image, f.Package, f.Installed, strings.Join(f.Fixed, ", "), f.Severity(), strings.Join(f.CVEs(), ", "))
			ids = append(ids, f.CVEs()...)
		}
	}
	if len(images) == 0 {
		b.Reset()
		for _, p := range patches {
			images = append(images, p.Image)
		}
		b.WriteString("image-scan (compliance-framework/workflows, `renovate.yml`): the base images ship the fixes of the OS package patches now, so the managed blocks go.\n\n")
	} else {
		b.WriteString("\n")
	}
	if len(removed) > 0 {
		b.WriteString(strings.Join(removed, "\n") + "\n\n")
	}
	b.WriteString("The branch `" + PatchBranch + "` is rebuilt from the default branch on every run; a commit by anyone else stops that, and the PR is then left to you. `renovate.yml`'s merge pass merges it once `ci / required` passes (`renovate:automerge`); add `needs-human` to hold it.\n")
	slices.Sort(images)
	images = slices.Compact(images)
	if len(ids) == 0 {
		return fmt.Sprintf("chore(deps): remove the OS package patches of %s", strings.Join(images, ", ")), b.String()
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	shown := ids
	if len(ids) > 4 {
		shown = append(slices.Clone(ids[:4]), fmt.Sprintf("and %d more", len(ids)-4))
	}
	return fmt.Sprintf("fix(deps): patch OS packages in %s (%s)", strings.Join(images, ", "), strings.Join(shown, ", ")), b.String()
}

// commitBody lists what the commit changes, one line per Dockerfile.
func commitBody(patches []FilePatch) string {
	var lines []string
	for _, p := range patches {
		if p.Removes() {
			lines = append(lines, fmt.Sprintf("%s: remove the OS package block (%s ships the fixes)", p.Dockerfile, p.Image))
		} else {
			lines = append(lines, fmt.Sprintf("%s: upgrade %s (%s)", p.Dockerfile, strings.Join(p.Packages, " "), strings.Join(cves(p.Findings), ", ")))
		}
	}
	return strings.Join(lines, "\n") + "\n\nChanges made by image-scan (compliance-framework/workflows)."
}

// diffLines is a minimal diff for the dry-run log: the lines only in before (-) or only in after (+).
func diffLines(before, after []byte) string {
	ol, nl := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	var b strings.Builder
	for _, l := range ol {
		if !slices.Contains(nl, l) {
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range nl {
		if !slices.Contains(ol, l) {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}

// allBy reports whether there is at least one author and every one is login.
func allBy(authors []string, login string) bool {
	for _, a := range authors {
		if !strings.EqualFold(a, login) {
			return false
		}
	}
	return len(authors) > 0
}

func short(sha string) string { return sha[:min(7, len(sha))] }
