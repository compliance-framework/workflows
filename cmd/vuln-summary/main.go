// Command vuln-summary posts a summary of the open Dependabot alerts, per repo and severity, to
// Slack, for the vuln-summary workflow:
//
//	vuln-summary list [--manifest repos.mock.yaml] [--repos a,b]
//	vuln-summary post [--manifest repos.mock.yaml] [--repos a,b] [--owner org]
//
// list prints the selected repo names, comma-separated, to scope the token. post reads the open
// alerts with the token in GH_TOKEN, prints the summary, and posts it to SLACK_CHANNEL with
// SLACK_BOT_TOKEN; without SLACK_BOT_TOKEN it only prints. Both select every repo in the manifest
// unless --repos names a subset. post fails, after posting, if a repo could not be read. The
// rules are in internal/vulnsummary; docs/vuln-summary.md says why it reads each repo rather
// than the org endpoint.
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

	"github.com/compliance-framework/workflows/internal/manifest"
	"github.com/compliance-framework/workflows/internal/notify"
	"github.com/compliance-framework/workflows/internal/vulnsummary"
)

// defaultManifest is the mocks until the summary is proven on them; the workflow passes the
// manifest explicitly.
const defaultManifest = "repos.mock.yaml"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "::error::vuln-summary: "+err.Error())
		os.Exit(1)
	}
}

const usage = "usage: vuln-summary list|post [flags]"

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	if len(args) == 0 || (args[0] != "list" && args[0] != "post") {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("vuln-summary "+args[0], flag.ContinueOnError)
	path := fs.String("manifest", defaultManifest, "path to the repo manifest")
	repos := fs.String("repos", "", "comma- or space-separated repo names (default: every repo in the manifest)")
	owner := fs.String("owner", "compliance-framework", "the repos' owner")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
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
	gh := &vulnsummary.GitHub{BaseURL: envOr(getenv, "GITHUB_API_URL", "https://api.github.com"), Token: getenv("GH_TOKEN")}
	scope := fmt.Sprintf("%d repo(s) in %s", len(selected), *path)
	s := vulnsummary.FromRepos(ctx, gh, *owner, scope, selected)

	links := vulnsummary.Links{Server: envOr(getenv, "GITHUB_SERVER_URL", "https://github.com")}
	if repo, id := getenv("GITHUB_REPOSITORY"), getenv("GITHUB_RUN_ID"); repo != "" && id != "" {
		links.Run = strings.TrimRight(links.Server, "/") + "/" + repo + "/actions/runs/" + id
	}
	msg := vulnsummary.Message(s, links)
	fmt.Fprintln(stdout, msg)

	if err := post(ctx, getenv, stdout, msg); err != nil {
		return err
	}
	if len(s.Failed) > 0 {
		names := make([]string, len(s.Failed))
		for i, f := range s.Failed {
			names[i] = f.Repo
		}
		return fmt.Errorf("could not read the alerts of %s", strings.Join(names, ", "))
	}
	return nil
}

// post sends msg to SLACK_CHANNEL, or does nothing without SLACK_BOT_TOKEN.
func post(ctx context.Context, getenv func(string) string, stdout io.Writer, msg string) error {
	token := getenv("SLACK_BOT_TOKEN")
	if token == "" {
		fmt.Fprintln(stdout, "SLACK_BOT_TOKEN is not set; nothing posted")
		return nil
	}
	channel := getenv("SLACK_CHANNEL")
	if channel == "" {
		return errors.New("SLACK_CHANNEL is empty: set the SLACK_CHANNEL_VULNS or SLACK_CHANNEL_CI_FAILURES variable")
	}
	slack := &notify.Slack{BaseURL: envOr(getenv, "SLACK_API_URL", "https://slack.com/api"), Token: token}
	if err := slack.PostMessage(ctx, channel, msg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "posted to %s\n", channel)
	return nil
}

// selectRepos returns the repos named in list (comma- or space-separated), or every repo in the
// manifest but the workflows repo (kind workflows) if list is empty, in manifest order. Named
// repos must be in the manifest.
func selectRepos(m *manifest.Manifest, list string) ([]string, error) {
	names := strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
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
		if len(names) == 0 && r.Kind != manifest.KindWorkflows || slices.Contains(names, r.Name) {
			out = append(out, r.Name)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no repos selected")
	}
	return out, nil
}

func envOr(getenv func(string) string, name, def string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return def
}
