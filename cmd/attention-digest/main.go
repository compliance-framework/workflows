// Command attention-digest posts the open PRs of the manifest's repos that wait for a person to
// Slack, once a week, for the attention-digest workflow:
//
//	attention-digest list [--manifest repos.mock.yaml] [--repos a,b]
//	attention-digest post [--manifest repos.mock.yaml] [--repos a,b] [--owner org] [--dry-run] [flags]
//
// list prints the selected repo names, comma-separated, to scope the token: every repo in the
// manifest (the workflows repo included) unless --repos names a subset. post reads the open PRs
// with the read-only token in GH_TOKEN, prints the digest and posts it to SLACK_CHANNEL with
// SLACK_BOT_TOKEN, unless --dry-run, either is unset, or no PR needs a human. It fails, after
// posting, if a repo or PR could not be read. The rules are in internal/attention;
// docs/attention.md has the details.
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

	"github.com/compliance-framework/workflows/internal/attention"
	"github.com/compliance-framework/workflows/internal/manifest"
	"github.com/compliance-framework/workflows/internal/notify"
)

// defaultManifest is the mocks until the digest is proven on them; the workflow passes the
// manifest explicitly.
const defaultManifest = "repos.mock.yaml"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, "::error::attention-digest: "+err.Error())
		os.Exit(1)
	}
}

const usage = "usage: attention-digest list|post [flags]"

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer, now func() time.Time) error {
	if len(args) == 0 || (args[0] != "list" && args[0] != "post") {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("attention-digest "+args[0], flag.ContinueOnError)
	path := fs.String("manifest", defaultManifest, "path to the repo manifest")
	repos := fs.String("repos", "", "comma-separated repo names (default: every repo in the manifest)")
	owner := fs.String("owner", "compliance-framework", "the repos' owner")
	bot := fs.String("bot", notify.ReleaseBotLogin, "login of the release bot, which opens Renovate, ccf-bump and release PRs")
	required := fs.String("required-checks", "ci / required", "comma-separated checks every PR must pass")
	releaseCheck := fs.String("release-check", "release-checks / release-checks", "the release-checks check of release-please PRs")
	staleDays := fs.Int("stale-days", 7, "a bot PR (but a release-please PR) open longer than this many days needs a human")
	dryRun := fs.Bool("dry-run", false, "print the digest without posting it")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *staleDays < 1 {
		return fmt.Errorf("--stale-days %d: want at least 1", *staleDays)
	}
	m, err := manifest.Load(*path)
	if err != nil {
		return err
	}
	selected, err := selectRepos(m, *repos)
	if err != nil {
		return err
	}
	if args[0] == "list" {
		_, err := fmt.Fprintln(stdout, strings.Join(selected, ","))
		return err
	}

	if getenv("GH_TOKEN") == "" {
		return errors.New("GH_TOKEN is not set")
	}
	gh := &attention.GitHub{BaseURL: envOr(getenv, "GITHUB_API_URL", "https://api.github.com"), Token: getenv("GH_TOKEN")}
	cfg := attention.Config{Owner: *owner, BotLogin: *bot, RequiredChecks: splitList(*required), ReleaseCheck: *releaseCheck,
		StaleAfter: time.Duration(*staleDays) * 24 * time.Hour, Now: now()}
	d := attention.Collect(ctx, gh, cfg, selected)
	for _, f := range d.Failed {
		fmt.Fprintf(stdout, "::warning::attention-digest: %s: %v\n", f.Where, f.Err)
	}

	runURL := ""
	if repo, id := getenv("GITHUB_REPOSITORY"), getenv("GITHUB_RUN_ID"); repo != "" && id != "" {
		runURL = strings.TrimRight(envOr(getenv, "GITHUB_SERVER_URL", "https://github.com"), "/") + "/" + repo + "/actions/runs/" + id
	}
	if len(d.Items) == 0 {
		fmt.Fprintf(stdout, "no PR needs a human in %d repo(s) of %s; nothing posted\n", len(selected), *path)
	} else {
		msg := attention.Message(d, cfg.Now, runURL)
		fmt.Fprintln(stdout, msg)
		if err := post(ctx, getenv, stdout, msg, *dryRun); err != nil {
			return err
		}
	}
	if len(d.Failed) > 0 {
		where := make([]string, len(d.Failed))
		for i, f := range d.Failed {
			where[i] = f.Where
		}
		return fmt.Errorf("could not read %s", strings.Join(where, ", "))
	}
	return nil
}

// post sends msg to SLACK_CHANNEL; a dry run, or no SLACK_BOT_TOKEN or channel, only says so.
func post(ctx context.Context, getenv func(string) string, stdout io.Writer, msg string, dryRun bool) error {
	token, channel := getenv("SLACK_BOT_TOKEN"), getenv("SLACK_CHANNEL")
	switch {
	case dryRun:
		fmt.Fprintln(stdout, "dry run: nothing posted")
		return nil
	case token == "" || channel == "":
		fmt.Fprintln(stdout, "SLACK_BOT_TOKEN or SLACK_CHANNEL (the SLACK_CHANNEL_NEEDS_HUMAN variable) is not set; nothing posted")
		return nil
	}
	slack := &notify.Slack{BaseURL: envOr(getenv, "SLACK_API_URL", "https://slack.com/api"), Token: token}
	if err := slack.PostMessage(ctx, channel, msg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "posted to %s\n", channel)
	return nil
}

// selectRepos returns the repos named in list, or every repo in the manifest if list is empty,
// in manifest order. Named repos must be in the manifest.
func selectRepos(m *manifest.Manifest, list string) ([]string, error) {
	names := splitList(list)
	var unknown []string
	for _, n := range names {
		if !slices.ContainsFunc(m.Repos, func(r manifest.Repo) bool { return r.Name == n }) {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("not in the manifest: %s", strings.Join(unknown, ", "))
	}
	var out []string
	for _, r := range m.Repos {
		if len(names) == 0 || slices.Contains(names, r.Name) {
			out = append(out, r.Name)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no repos selected")
	}
	return out, nil
}

// splitList splits a comma-separated list, trimming spaces and dropping empty items.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(getenv func(string) string, name, def string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return def
}
