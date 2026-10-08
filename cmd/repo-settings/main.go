// Command repo-settings brings the merge settings, security settings, Actions settings and rulesets
// of the manifest's repos to the desired state in internal/reposettings, for the repo-settings
// workflow:
//
//	repo-settings list [--manifest repos.yaml] [--repos a,b]
//	repo-settings sync [--manifest repos.yaml] [--repos a,b] --bypass-app-id ID [--apply] [flags]
//
// list prints the selected repo names, comma-separated, to scope the token. sync prints each repo's
// diff of current vs desired settings, and writes the changes only with --apply. Both select every
// repo in the manifest (the workflows repo too, whatever its release flag) unless --repos names a
// subset. sync reads the token from GH_TOKEN.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/compliance-framework/workflows/internal/manifest"
	"github.com/compliance-framework/workflows/internal/reposettings"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, newGitHub); err != nil {
		fmt.Fprintln(os.Stderr, "::error::repo-settings: "+err.Error())
		os.Exit(1)
	}
}

const usage = "usage: repo-settings list|sync [flags]"

func newGitHub(getenv func(string) string) reposettings.Client {
	base := getenv("GITHUB_API_URL")
	if base == "" {
		base = "https://api.github.com"
	}
	return &reposettings.GitHub{BaseURL: base, Token: getenv("GH_TOKEN")}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer, client func(func(string) string) reposettings.Client) error {
	if len(args) == 0 || (args[0] != "list" && args[0] != "sync") {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("repo-settings "+args[0], flag.ContinueOnError)
	path := fs.String("manifest", manifest.DefaultPath, "path to the repo manifest")
	repos := fs.String("repos", "", "comma- or space-separated repo names (default: every repo in the manifest)")
	owner := fs.String("owner", "compliance-framework", "the repos' owner")
	apply := fs.Bool("apply", false, "write the changes (default: dry run)")
	check := fs.String("required-check", reposettings.DefaultRequiredCheck, "status check context ccf-required requires")
	checkApp := fs.Int64("required-check-app-id", reposettings.GitHubActionsAppID, "app that must report the required check (0: any)")
	bypass := fs.String("bypass-app-id", "", "ccf-release-bot app ID, the ccf-review bypass actor (sync)")
	scope := fs.Bool("check-token-scope", true, "fail if the token reaches repos outside the selection (needs an installation token)")
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

	bypassID, err := strconv.ParseInt(strings.TrimSpace(*bypass), 10, 64)
	if err != nil {
		return fmt.Errorf("--bypass-app-id %q: want the ccf-release-bot app's numeric ID", *bypass)
	}
	desired, err := reposettings.DesiredState(reposettings.Config{RequiredCheck: *check, RequiredCheckAppID: *checkApp, BypassAppID: bypassID})
	if err != nil {
		return err
	}
	if getenv("GH_TOKEN") == "" {
		return errors.New("GH_TOKEN is not set")
	}
	return reposettings.Run(ctx, client(getenv), reposettings.Options{
		Owner: *owner, Repos: selected, Desired: desired, Apply: *apply, CheckTokenScope: *scope,
	}, stdout)
}

// selectRepos returns the repos named in list (comma- or space-separated), or every repo in the
// manifest if list is empty, in manifest order. Every repo gets the same settings, including the
// workflows repo (release: false): it holds the code every other repo's CI and releases run.
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
		if len(names) == 0 || slices.Contains(names, r.Name) {
			out = append(out, r.Name)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no repos selected")
	}
	return out, nil
}
