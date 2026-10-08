// Command train runs the monthly release train (internal/train, docs/train.md):
//
//	train select --for start|reconcile [--manifest M] [--repos a,b]
//	    print the manifest and repos the run acts on (open=, manifest=, repos= lines for
//	    $GITHUB_OUTPUT), to scope the release-bot token: the open train's, else the selection
//	train start [--manifest M] [--repos a,b] [--dry-run] [--scheduled] [--watch D]
//	train reconcile [--watch D]
//
// --watch keeps reconciling every --interval, up to D, while the open train has a repo that is
// waiting (not held for a human), so one run sees release-please and CI finish instead of
// waiting for the next event or hourly run.
//
// Environment: GH_TOKEN (the release bot, scoped to the train's repos; ccf-bump uses it too),
// TRACKER_TOKEN (issues in the workflows repo), MEMBERS_TOKEN (optional: org members read, for
// comment commands), SLACK_BOT_TOKEN and SLACK_CHANNEL (optional), GITHUB_API_URL.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/manifest"
	"github.com/compliance-framework/workflows/internal/notify"
	"github.com/compliance-framework/workflows/internal/train"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, "::error::train: "+err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer, now func() time.Time) error {
	if len(args) == 0 {
		return errors.New("usage: train select|start|reconcile [flags]")
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet("train "+cmd, flag.ContinueOnError)
	path := fs.String("manifest", manifest.DefaultPath, "select, start: the manifest (a .yaml file in this repo's root)")
	repos := fs.String("repos", "", "select, start: comma-separated repos (default: every repo with release: true)")
	mode := fs.String("for", "", "select: start or reconcile")
	dryRun := fs.Bool("dry-run", false, "start: plan and post, merge nothing")
	scheduled := fs.Bool("scheduled", false, "start: only on the month's first working weekday, once")
	owner := fs.String("owner", "compliance-framework", "the repos' owner")
	trackerRepo := fs.String("tracker-repo", "workflows", "the repo of the tracking issues")
	bumpPath := fs.String("ccf-bump", "ccf-bump", "the ccf-bump binary")
	required := fs.String("required-check", "ci / required", "a check every merged PR must pass")
	rpWorkflow := fs.String("release-please-workflow", "release-please.yml", "the repos' release-please workflow file")
	watch := fs.Duration("watch", 0, "start, reconcile: keep reconciling while the open train is waiting on GitHub, for up to this long")
	interval := fs.Duration("interval", time.Minute, "start, reconcile: the pause between --watch runs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	api := envOr(getenv, "GITHUB_API_URL", "https://api.github.com")
	tracker := &train.GitHub{BaseURL: api, Token: getenv("TRACKER_TOKEN"), Owner: *owner, Repo: *trackerRepo}
	selected := split(*repos)
	switch cmd {
	case "select":
		if *mode != "start" && *mode != "reconcile" {
			return errors.New("select needs --for start or --for reconcile")
		}
		return selectRepos(ctx, tracker, *mode, *path, selected, stdout)
	case "start", "reconcile":
	default:
		return fmt.Errorf("unknown command %q: want select, start or reconcile", cmd)
	}
	e := &train.Engine{
		Repos:                 &train.GitHub{BaseURL: api, Token: getenv("GH_TOKEN"), Owner: *owner},
		Tracker:               tracker,
		Bumper:                execBumper{path: *bumpPath, log: stdout},
		Load:                  load,
		Owner:                 *owner,
		Channel:               getenv("SLACK_CHANNEL"),
		RequiredCheck:         *required,
		ReleasePleaseWorkflow: *rpWorkflow,
		RunURL:                fmt.Sprintf("%s/%s/actions/runs/%s", envOr(getenv, "GITHUB_SERVER_URL", "https://github.com"), getenv("GITHUB_REPOSITORY"), getenv("GITHUB_RUN_ID")),
		Now:                   now,
		Log:                   stdout,
	}
	if tok := getenv("MEMBERS_TOKEN"); tok != "" {
		e.Members = &train.GitHub{BaseURL: api, Token: tok, Owner: *owner}
	}
	if tok := getenv("SLACK_BOT_TOKEN"); tok != "" {
		e.Slack = slack{&notify.Slack{BaseURL: envOr(getenv, "SLACK_API_URL", "https://slack.com/api"), Token: tok}}
	}
	var err error
	if cmd == "reconcile" {
		err = e.Reconcile(ctx)
	} else {
		err = e.Start(ctx, train.StartOptions{Manifest: *path, Repos: selected, DryRun: *dryRun, Scheduled: *scheduled})
	}
	// Every run reads GitHub afresh, so a failed one is retried; the last result counts.
	for deadline := now().Add(*watch); now().Add(*interval).Before(deadline); {
		issues, ierr := tracker.Issues(ctx, train.LabelOpen)
		if ierr != nil || !slices.ContainsFunc(issues, waiting) {
			return errors.Join(err, ierr)
		}
		sleep(*interval)
		err = e.Reconcile(ctx)
	}
	return err
}

// waiting reports whether is is an open train with a repo in an open stage that waits on
// GitHub (release-please, checks, a release workflow) rather than on a human.
func waiting(is train.Issue) bool {
	st, err := train.Parse(is.Body)
	if !is.Open || err != nil || st.Status != train.StatusOpen {
		return false
	}
	return slices.ContainsFunc(st.Repos, func(r *train.RepoState) bool {
		return st.StageOpen(r.Stage) && !r.Phase.Done() && r.Hold == train.NoHold
	})
}

// sleep is time.Sleep; tests replace it.
var sleep = time.Sleep

// manifestName is a manifest in this repo's root; a train's state names its manifest, so it
// can't point anywhere else.
var manifestName = regexp.MustCompile(`^[A-Za-z0-9._-]+\.ya?ml$`)

func load(path string) (*manifest.Manifest, error) {
	if !manifestName.MatchString(path) {
		return nil, fmt.Errorf("manifest %q: want a .yaml file in the repo root", path)
	}
	return manifest.Load(path)
}

// selectRepos prints the repos the run may act on: the open train's, checked against its
// manifest, else (for start) the selection. No open train for reconcile prints no repos.
func selectRepos(ctx context.Context, tracker *train.GitHub, mode, path string, selected []string, stdout io.Writer) error {
	issues, err := tracker.Issues(ctx, train.LabelOpen)
	if err != nil {
		return err
	}
	open := false
	for _, is := range issues {
		if !is.Open {
			continue
		}
		st, err := train.Parse(is.Body)
		if err != nil {
			return fmt.Errorf("#%d: %w", is.Number, err)
		}
		if open {
			return fmt.Errorf("more than one open train (#%d too); close all but one", is.Number)
		}
		open, path, selected = true, st.Manifest, nil
		for _, r := range st.Repos {
			selected = append(selected, r.Name)
		}
	}
	if !open && mode == "reconcile" {
		_, err := fmt.Fprintf(stdout, "open=false\nmanifest=\nrepos=\n")
		return err
	}
	m, err := load(path)
	if err != nil {
		return err
	}
	var names []string
	for _, r := range m.Repos {
		if r.Release && (len(selected) == 0 || slices.Contains(selected, r.Name)) {
			names = append(names, r.Name)
		}
	}
	for _, s := range selected {
		if !slices.Contains(names, s) {
			return fmt.Errorf("repo %q is not in %s with release: true", s, path)
		}
	}
	if len(names) == 0 {
		return errors.New("no repos selected")
	}
	_, err = fmt.Fprintf(stdout, "open=%t\nmanifest=%s\nrepos=%s\n", open, path, strings.Join(names, ","))
	return err
}

func split(list string) []string {
	return strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' })
}

func envOr(getenv func(string) string, name, def string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return def
}

// slack adapts notify.Slack to train.Slack.
type slack struct{ s *notify.Slack }

func (s slack) Post(ctx context.Context, channel, text, threadTS string) (string, error) {
	_, ts, err := s.s.Post(ctx, channel, text, threadTS)
	return ts, err
}

// execBumper runs the ccf-bump binary (cmd/ccf-bump) in train mode.
type execBumper struct {
	path string
	log  io.Writer
}

var prLine = regexp.MustCompile(`(?m)^\s*PR: \S+/pull/(\d+)\s*$`)

func (b execBumper) Bump(ctx context.Context, manifestPath, repo string, sets map[string]string, dryRun bool) (train.BumpResult, error) {
	args := []string{"--manifest", manifestPath, "--repo", repo, "--mode", "train", "--pr"}
	for _, k := range slices.Sorted(maps.Keys(sets)) {
		args = append(args, "--set", k+"="+sets[k])
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	fmt.Fprintf(b.log, "::group::ccf-bump %s\n", strings.Join(args, " "))
	defer fmt.Fprintln(b.log, "::endgroup::")
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, b.path, args...)
	w := io.MultiWriter(&out, b.log) // one writer, so exec never writes from two goroutines at once
	cmd.Stdout, cmd.Stderr = w, w
	err := cmd.Run()
	res := train.BumpResult{Output: out.String()}
	if m := prLine.FindStringSubmatch(res.Output); m != nil {
		res.PR, _ = strconv.Atoi(m[1])
	}
	res.Changed = res.PR > 0 || strings.Contains(res.Output, "dry run: would push")
	if err != nil {
		return res, fmt.Errorf("%w: %s", err, lastError(res.Output))
	}
	return res, nil
}

// lastError returns the last ::error:: line of out, or its last line.
func lastError(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if _, msg, ok := strings.Cut(lines[i], "::error::"); ok {
			return msg
		}
	}
	return lines[len(lines)-1]
}
