// Command ccf-bump moves the pinned versions of internal dependencies in the manifest's repos
// (internal/bump) and opens one PR per repo:
//
//	ccf-bump --repo NAME [--set dep=version]... [--mode train|sync] [--pr] [--dry-run] [flags]
//	ccf-bump sync --all|--repos a,b [--batch 10] [--pr] [--dry-run] [flags]
//	ccf-bump merge --all|--repos a,b [--wait 20m] [--dry-run]   merge its green ccf-bump:automerge PRs
//	ccf-bump list [--repos a,b]       the selected repos, comma-separated (to scope the token)
//
// sync mode targets each dependency's latest final release; train mode only the --set versions.
// Without --pr it prints the plan and the diff; --pr --dry-run also prints the PR it would open.
// docs/ccf-bump.md has the details.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/compliance-framework/workflows/internal/bump"
	"github.com/compliance-framework/workflows/internal/manifest"
)

// GitHub is what ccf-bump needs from the API; *bump.GitHub implements it.
type GitHub interface {
	LatestFinal(ctx context.Context, repo string) (string, error)
	TagTime(ctx context.Context, repo, tag string) (time.Time, error)
	TagCommit(ctx context.Context, repo, tag string) (string, error)
	File(ctx context.Context, repo, ref, path string) ([]byte, error)
	DefaultBranch(ctx context.Context, repo string) (string, error)
	OpenPR(ctx context.Context, repo, branch string) (*bump.PR, error)
	CreatePR(ctx context.Context, repo, base, head, title, body string) (*bump.PR, error)
	UpdatePR(ctx context.Context, repo string, number int, title, body string) error
	DisableAutoMerge(ctx context.Context, id string) error
	OpenPRs(ctx context.Context, repo string) ([]bump.PR, error)
	ClosePR(ctx context.Context, repo string, number int, comment string) error
	DeleteBranch(ctx context.Context, repo, branch string) error
	AddLabel(ctx context.Context, repo string, number int, l bump.Label) error
	RemoveLabel(ctx context.Context, repo string, number int, name string) error
	PullRequest(ctx context.Context, repo string, number int) (*bump.PR, error)
	CheckRuns(ctx context.Context, repo, sha, name string) ([]bump.CheckRun, error)
	Merge(ctx context.Context, repo string, number int, sha, title string) error
}

// env is everything run depends on, so tests can fake it.
type env struct {
	gh     GitHub
	getenv func(string) string
	stdout io.Writer
	now    func() time.Time
	sleep  func(time.Duration)
	remote func(owner, repo string) string // clone/push URL
}

func main() {
	getenv := os.Getenv
	base := getenv("GITHUB_API_URL")
	if base == "" {
		base = "https://api.github.com"
	}
	e := env{
		gh:     &bump.GitHub{BaseURL: base, Token: getenv("GH_TOKEN"), Owner: "compliance-framework"},
		getenv: getenv, stdout: os.Stdout, now: time.Now, sleep: time.Sleep,
		remote: func(owner, repo string) string { return "https://github.com/" + owner + "/" + repo + ".git" },
	}
	if err := run(context.Background(), os.Args[1:], e); err != nil {
		fmt.Fprintln(os.Stderr, "::error::ccf-bump: "+err.Error())
		os.Exit(1)
	}
}

type setFlag map[string]string

func (s setFlag) String() string { return fmt.Sprint(map[string]string(s)) }
func (s setFlag) Set(v string) error {
	k, ver, ok := strings.Cut(v, "=")
	if !ok || k == "" || ver == "" {
		return fmt.Errorf("want dep=version, got %q", v)
	}
	s[k] = ver
	return nil
}

type options struct {
	owner, mode, clones, workflowsRef string
	set                               setFlag
	pr, dryRun                        bool
	batch                             int
	wait                              time.Duration
	author, required                  string
}

func run(ctx context.Context, args []string, e env) error {
	cmd := ""
	if len(args) > 0 && (args[0] == "sync" || args[0] == "list" || args[0] == "merge") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("ccf-bump "+cmd, flag.ContinueOnError)
	o := options{set: setFlag{}}
	path := fs.String("manifest", manifest.DefaultPath, "path to the repo manifest")
	repo := fs.String("repo", "", "the repo to bump")
	repos := fs.String("repos", "", "sync, merge, list: comma-separated repos (default with --all: every repo with release: true)")
	all := fs.Bool("all", false, "sync, merge: every repo with release: true")
	fs.StringVar(&o.owner, "owner", "compliance-framework", "the repos' owner")
	fs.StringVar(&o.mode, "mode", "sync", "sync (latest final releases) or train (only --set versions)")
	fs.Var(o.set, "set", "dep=version target (repeatable); dep is a repo, a github.com/<owner>/ module path, opa or workflows")
	fs.StringVar(&o.workflowsRef, "workflows-ref", "", "move compliance-framework/workflows pins to this release (vX.Y.Z, pinned by its commit SHA) or ref (same as --set workflows=REF)")
	fs.StringVar(&o.clones, "clones", "", "directory of local clones (<dir>/<repo>) to read instead of cloning from GitHub")
	fs.BoolVar(&o.pr, "pr", false, "push a branch and open or update a PR per repo")
	fs.BoolVar(&o.dryRun, "dry-run", false, "with --pr: print the PR instead of pushing; merge: print what it would merge")
	fs.IntVar(&o.batch, "batch", 0, "sync: open at most this many PRs per hour (0: no limit)")
	fs.DurationVar(&o.wait, "wait", 0, "merge: poll PRs whose required check is pending for up to this long")
	fs.StringVar(&o.author, "author", "ccf-release-bot[bot]", "merge: the account whose PRs it merges (the token's identity)")
	fs.StringVar(&o.required, "required-check", "ci / required", "merge: the check that must pass on a PR's head")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if o.mode != "sync" && o.mode != "train" {
		return fmt.Errorf("--mode %q: want sync or train", o.mode)
	}
	if o.workflowsRef != "" {
		o.set[bump.DepWorkflows] = o.workflowsRef
	}
	for k, v := range o.set {
		if k != bump.DepWorkflows && !semver.IsValid(bump.Canonical(v)) {
			return fmt.Errorf("--set %s=%s: want a version (v1.2.3)", k, v)
		}
	}
	m, err := manifest.Load(*path)
	if err != nil {
		return err
	}
	var selected []string
	switch {
	case cmd == "" && (*all || *repos != "" || o.batch != 0):
		return errors.New("--all, --repos and --batch go with sync: ccf-bump sync --all|--repos")
	case cmd == "" && *repo == "":
		return errors.New("--repo is required (or use: ccf-bump sync --all)")
	case cmd == "":
		selected, err = selectRepos(m, *repo)
	case *all == (*repos != "") && (cmd == "sync" || cmd == "merge"):
		return errors.New(cmd + " takes one of --all or --repos")
	default:
		selected, err = selectRepos(m, *repos)
	}
	if err != nil {
		return err
	}
	if cmd == "list" {
		_, err := fmt.Fprintln(e.stdout, strings.Join(selected, ","))
		return err
	}
	if cmd == "merge" {
		b := &bumper{e: e, o: o, m: m}
		err := b.mergeAll(ctx, selected)
		b.summary()
		return err
	}
	if cmd == "sync" && o.mode != "sync" {
		return errors.New("sync runs in sync mode; use --repo with --mode train")
	}
	b := &bumper{e: e, o: o, m: m, finals: map[string]string{}, commits: map[string]string{}}
	inWindow, windowStart := 0, e.now() // PRs opened in the current hour (--batch)
	var failed []error
	for _, r := range selected {
		if o.batch > 0 && inWindow >= o.batch {
			if wait := time.Hour - e.now().Sub(windowStart); wait > 0 {
				fmt.Fprintf(e.stdout, "opened %d PRs this hour (--batch); waiting %s\n", inWindow, wait.Round(time.Second))
				e.sleep(wait)
			}
			inWindow, windowStart = 0, e.now()
		}
		pr, err := b.bump(ctx, r)
		if err != nil {
			// One repo's failure doesn't stop the others; the run still fails.
			err = fmt.Errorf("%s: %w", r, err)
			fmt.Fprintln(e.stdout, "::error::ccf-bump: "+err.Error())
			failed = append(failed, err)
		}
		if pr {
			inWindow++
		}
	}
	b.summary()
	return errors.Join(failed...)
}

// selectRepos returns the named repos (default every repo with release: true) in stage order.
func selectRepos(m *manifest.Manifest, list string) ([]string, error) {
	var out []string
	if strings.TrimSpace(list) == "" {
		for _, r := range m.Repos {
			if r.Release {
				out = append(out, r.Name)
			}
		}
		return stageOrder(m, out)
	}
	for _, n := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' }) {
		i := slices.IndexFunc(m.Repos, func(r manifest.Repo) bool { return r.Name == n })
		if i < 0 || !m.Repos[i].Release {
			return nil, fmt.Errorf("repo %q is not in the manifest with release: true", n)
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return stageOrder(m, out)
}

// stageOrder sorts names by release stage, so dependencies are bumped first.
func stageOrder(m *manifest.Manifest, names []string) ([]string, error) {
	stages, err := m.Stages()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range stages {
		for _, r := range s {
			if slices.Contains(names, r) {
				out = append(out, r)
			}
		}
	}
	return out, nil
}
