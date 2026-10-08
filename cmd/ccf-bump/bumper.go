package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/compliance-framework/workflows/internal/bump"
	"github.com/compliance-framework/workflows/internal/manifest"
)

type bumper struct {
	e      env
	o      options
	m      *manifest.Manifest
	finals map[string]string // repo -> latest final tag, cached across repos
	// warnings are problems that don't fail the run (auto-merge, superseded PRs), for the summary.
	warnings []string
}

// warn prints a GitHub Actions warning and keeps it for the run summary.
func (b *bumper) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(b.e.stdout, "::warning::ccf-bump: "+msg)
	b.warnings = append(b.warnings, msg)
}

// summary repeats the warnings at the end of the output and in $GITHUB_STEP_SUMMARY when set.
func (b *bumper) summary() {
	if len(b.warnings) == 0 {
		return
	}
	var s strings.Builder
	s.WriteString("### ccf-bump warnings\n\n")
	for _, w := range b.warnings {
		fmt.Fprintf(&s, "- %s\n", w)
	}
	fmt.Fprint(b.e.stdout, "\n"+s.String())
	p := b.e.getenv("GITHUB_STEP_SUMMARY")
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, err = f.WriteString(s.String())
		err = errors.Join(err, f.Close())
	}
	if err != nil {
		fmt.Fprintln(b.e.stdout, "::warning::ccf-bump: step summary: "+err.Error())
	}
}

// config derives the updaters' settings for repo from its kind and dependencies.
func (b *bumper) config(r manifest.Repo) bump.Config {
	c := bump.Config{Owner: b.o.owner, Run: run1}
	service := ""
	for _, d := range r.DependsOn {
		i := slices.IndexFunc(b.m.Repos, func(x manifest.Repo) bool { return x.Name == d })
		if i >= 0 && b.m.Repos[i].Kind == manifest.KindGoService && service == "" {
			service = d
		}
	}
	switch r.Kind {
	case manifest.KindAction:
		if len(r.DependsOn) == 1 {
			c.Source = r.DependsOn[0]
		}
	case manifest.KindUI:
		c.UIDep = service
	case manifest.KindPolicies:
		c.OPASource = service
		if c.OPASource == "" {
			c.OPASource = "agent"
		}
	}
	return c
}

// run1 runs a command in dir (go, or the repo's own sync script), with its output on stderr and
// without the GitHub token in its environment.
func run1(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.Env = dir, os.Stderr, os.Stderr, withoutTokens(os.Environ())
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// withoutTokens drops GH_TOKEN and GITHUB_TOKEN from env.
func withoutTokens(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		return strings.HasPrefix(kv, "GH_TOKEN=") || strings.HasPrefix(kv, "GITHUB_TOKEN=")
	})
}

// git runs git in dir with the token as an HTTP auth header (kept out of URLs and argv).
func (b *bumper) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if tok := b.e.getenv("GH_TOKEN"); tok != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
			"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+auth)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, out)
	}
	return string(out), nil
}

// targets returns dep -> target version for the refs found.
func (b *bumper) targets(ctx context.Context, cfg bump.Config, refs []bump.Ref) (map[string]string, error) {
	t := map[string]string{}
	for k, v := range b.o.set {
		k = strings.TrimPrefix(k, "github.com/"+b.o.owner+"/")
		if k != bump.DepWorkflows {
			v = bump.Canonical(v)
		}
		t[k] = v
	}
	final := func(dep string) (string, error) {
		if v, ok := t[dep]; ok || b.o.mode != "sync" {
			return v, nil
		}
		if v, ok := b.finals[dep]; ok {
			return v, nil
		}
		v, err := b.e.gh.LatestFinal(ctx, dep)
		if err != nil {
			return "", fmt.Errorf("latest release of %s: %w", dep, err)
		}
		b.finals[dep] = v
		return v, nil
	}
	for _, r := range refs {
		if r.Dep == bump.DepWorkflows || r.Dep == bump.DepOPA {
			continue
		}
		v, err := final(r.Dep)
		if err != nil {
			return nil, err
		}
		if v != "" {
			t[r.Dep] = v
		}
	}
	if _, ok := t[bump.DepOPA]; !ok && slices.ContainsFunc(refs, func(r bump.Ref) bool { return r.Dep == bump.DepOPA }) {
		tag, err := final(cfg.OPASource)
		if err != nil || tag == "" {
			return t, err
		}
		data, err := b.e.gh.File(ctx, cfg.OPASource, tag, "go.mod")
		if err != nil {
			return nil, fmt.Errorf("%s go.mod at %s: %w", cfg.OPASource, tag, err)
		}
		f, err := modfile.ParseLax("go.mod", data, nil)
		if err != nil {
			return nil, err
		}
		for _, r := range f.Require {
			if r.Mod.Path == "github.com/open-policy-agent/opa" {
				t[bump.DepOPA] = r.Mod.Version
			}
		}
	}
	return t, nil
}

// bump plans and applies the bump of one repo; it reports whether it opened or updated a PR.
func (b *bumper) bump(ctx context.Context, name string) (bool, error) {
	r := b.m.Repos[slices.IndexFunc(b.m.Repos, func(x manifest.Repo) bool { return x.Name == name })]
	out := b.e.stdout
	cfg := b.config(r)
	us := bump.Updaters(cfg)
	dir, err := os.MkdirTemp("", "ccf-bump-"+name+"-")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	src, depth := b.e.remote(b.o.owner, name), "--depth=1"
	if b.o.clones != "" {
		src, depth = filepath.Join(b.o.clones, name), "--no-local"
	}
	if _, err := b.git(ctx, "", "clone", "--quiet", depth, src, dir); err != nil {
		return false, err
	}
	refs, err := bump.ScanAll(dir, us)
	if err != nil {
		return false, err
	}
	targets, err := b.targets(ctx, cfg, refs)
	if err != nil {
		return false, err
	}
	plan, err := bump.Decide(refs, targets, func(dep, tag string) (time.Time, error) { return b.e.gh.TagTime(ctx, dep, tag) })
	if err != nil {
		return false, err
	}
	fmt.Fprintf(out, "%s/%s (%s):\n%s", b.o.owner, name, r.Kind, report(plan))
	if len(plan.Changes) == 0 {
		return false, nil
	}
	if err := bump.ApplyAll(ctx, dir, us, plan.Changes); err != nil {
		return false, err
	}
	if _, err := b.git(ctx, dir, "add", "-A"); err != nil {
		return false, err
	}
	diff, err := b.git(ctx, dir, "diff", "--cached")
	if err != nil {
		return false, err
	}
	if diff == "" {
		fmt.Fprintln(out, "  no file changes")
		return false, nil
	}
	fmt.Fprint(out, indent(diff))
	if !b.o.pr {
		return false, nil
	}
	title, body := prText(b.o, plan)
	branch := "ccf-bump/" + b.o.mode + "-" + b.e.now().UTC().Format("2006-01-02")
	if b.o.dryRun {
		fmt.Fprintf(out, "  dry run: would push %s and open %q\n%s", branch, title, indent(body))
		// Nothing was opened to tell who ccf-bump runs as, so any bot's ccf-bump PR is listed.
		old, err := b.superseded(ctx, name, branch, func(p bump.PR) bool { return p.User.Type == "Bot" })
		if err != nil {
			b.warn("%s: list open PRs: %v", name, err)
		}
		for _, p := range old {
			fmt.Fprintf(out, "  dry run: would close #%d (%s, by %s) as superseded and delete its branch\n", p.Number, p.Head.Ref, p.User.Login)
		}
		return false, nil
	}
	if _, err := b.git(ctx, dir, "checkout", "--quiet", "-B", branch); err != nil {
		return false, err
	}
	if _, err := b.git(ctx, dir, "commit", "--quiet", "-m", title, "-m", "Changes made by ccf-bump ("+b.o.mode+" mode)."); err != nil {
		return false, err
	}
	if _, err := b.git(ctx, dir, "push", "--quiet", "--force", b.e.remote(b.o.owner, name), "HEAD:refs/heads/"+branch); err != nil {
		return false, err
	}
	return true, b.openPR(ctx, name, branch, title, body, plan)
}

func (b *bumper) openPR(ctx context.Context, repo, branch, title, body string, plan bump.Plan) error {
	pr, err := b.e.gh.OpenPR(ctx, repo, branch)
	if err != nil {
		return err
	}
	if pr != nil {
		err = b.e.gh.UpdatePR(ctx, repo, pr.Number, title, body)
	} else {
		var base string
		if base, err = b.e.gh.DefaultBranch(ctx, repo); err == nil {
			pr, err = b.e.gh.CreatePR(ctx, repo, base, branch, title, body)
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(b.e.stdout, "  PR: %s\n", pr.URL)
	b.closeSuperseded(ctx, repo, branch, pr)
	if slices.ContainsFunc(plan.Changes, bump.Change.Major) {
		fmt.Fprintln(b.e.stdout, "  auto-merge: off (a major update, or a pin that was not a version)")
		return nil
	}
	if err := b.e.gh.EnableAutoMerge(ctx, pr.NodeID); err != nil {
		// e.g. the base branch has no protection rules, or the PR is already mergeable ("clean
		// status"). The PR is open either way; a human merges it.
		b.warn("%s#%d: %v", repo, pr.Number, err)
		return nil
	}
	fmt.Fprintln(b.e.stdout, "  auto-merge: on")
	return nil
}

// superseded returns repo's other open PRs that ccf-bump opened in this mode: head branch
// ccf-bump/<mode>-* in repo itself (not a fork), other than branch, for which mine (who opened
// it) is true.
func (b *bumper) superseded(ctx context.Context, repo, branch string, mine func(bump.PR) bool) ([]bump.PR, error) {
	prs, err := b.e.gh.OpenPRs(ctx, repo)
	if err != nil {
		return nil, err
	}
	prefix := "ccf-bump/" + b.o.mode + "-"
	return slices.DeleteFunc(prs, func(p bump.PR) bool {
		return p.Head.Ref == branch || !strings.HasPrefix(p.Head.Ref, prefix) || p.Head.Repo == nil ||
			!strings.EqualFold(p.Head.Repo.FullName, b.o.owner+"/"+repo) || !mine(p)
	}), nil
}

// closeSuperseded closes repo's earlier ccf-bump PRs opened by the same account as pr, with a
// comment pointing at pr, and deletes their branches. Failures are warnings: pr is open.
func (b *bumper) closeSuperseded(ctx context.Context, repo, branch string, pr *bump.PR) {
	if pr.User.Login == "" {
		b.warn("%s#%d: author unknown; earlier ccf-bump PRs left open", repo, pr.Number)
		return
	}
	old, err := b.superseded(ctx, repo, branch, func(p bump.PR) bool { return p.User.Login == pr.User.Login })
	if err != nil {
		b.warn("%s: list open PRs: %v", repo, err)
		return
	}
	for _, p := range old {
		if err := b.e.gh.ClosePR(ctx, repo, p.Number, fmt.Sprintf("Superseded by #%d.", pr.Number)); err != nil {
			b.warn("%s#%d: close superseded PR: %v", repo, p.Number, err)
			continue
		}
		if err := b.e.gh.DeleteBranch(ctx, repo, p.Head.Ref); err != nil {
			b.warn("%s#%d: closed, but deleting branch %s: %v", repo, p.Number, p.Head.Ref, err)
			continue
		}
		fmt.Fprintf(b.e.stdout, "  closed #%d (%s): superseded by #%d\n", p.Number, p.Head.Ref, pr.Number)
	}
}

// targetsOf lists "dep to version" for each changed dep, sorted.
func targetsOf(plan bump.Plan) []string {
	var out []string
	for _, c := range plan.Changes {
		s := c.Dep + " to " + c.To
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}

// releaseURL links a change's release notes.
func releaseURL(owner string, c bump.Change) string {
	switch c.Dep {
	case bump.DepOPA:
		return "https://github.com/open-policy-agent/opa/releases/tag/" + c.To
	case bump.DepWorkflows:
		return "https://github.com/" + owner + "/workflows/tree/" + c.To
	}
	return "https://github.com/" + owner + "/" + c.Dep + "/releases/tag/" + c.To
}

func prText(o options, plan bump.Plan) (string, string) {
	title := "fix(deps): bump " + strings.Join(targetsOf(plan), ", ")
	var b strings.Builder
	fmt.Fprintf(&b, "Bumps internal dependencies (ccf-bump, %s mode).\n\n| Dependency | From | To | Where |\n| --- | --- | --- | --- |\n", o.mode)
	for _, c := range plan.Changes {
		from := c.Current
		if from == "" {
			from = "(unknown)"
		}
		fmt.Fprintf(&b, "| [%s](%s) | `%s` | `%s` | `%s` %s |\n", c.Dep, releaseURL(o.owner, c), from, c.To, c.File, c.Key)
	}
	if len(plan.Conflicts) > 0 {
		b.WriteString("\nNot changed (conflicts):\n\n")
		for _, c := range plan.Conflicts {
			fmt.Fprintf(&b, "- `%s` %s: %s\n", c.File, c.Key, c.Reason)
		}
	}
	return title, b.String()
}

func report(p bump.Plan) string {
	var b strings.Builder
	for _, c := range p.Changes {
		fmt.Fprintf(&b, "  bump      %-12s %s -> %s (%s %s)\n", c.Dep, orDash(c.Current), c.To, c.File, c.Key)
	}
	for _, c := range p.Conflicts {
		fmt.Fprintf(&b, "  conflict  %-12s %s (%s %s)\n", c.Dep, c.Reason, c.File, c.Key)
	}
	for _, r := range p.UpToDate {
		fmt.Fprintf(&b, "  current   %-12s %s (%s %s)\n", r.Dep, r.Current, r.File, r.Key)
	}
	var none []string // deps without a target, once each
	for _, r := range p.NoTarget {
		if !slices.Contains(none, r.Dep) {
			none = append(none, r.Dep)
		}
	}
	for _, d := range none {
		n := 0
		for _, r := range p.NoTarget {
			if r.Dep == d {
				n++
			}
		}
		fmt.Fprintf(&b, "  no target %-12s %d pin(s) left as they are (no final release, or not asked for)\n", d, n)
	}
	if b.Len() == 0 {
		b.WriteString("  nothing pinned\n")
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func indent(s string) string {
	lines := strings.SplitAfter(strings.TrimRight(s, "\n")+"\n", "\n")
	return "    " + strings.Join(lines[:len(lines)-1], "    ")
}
