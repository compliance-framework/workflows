// Command manifest validates a repo manifest and prints its release stages.
//
//	go run ./cmd/manifest [--manifest repos.yaml] [--json] [--org compliance-framework]
//
// Text output is one stage per line; --json prints the stages as a JSON array of
// arrays, for use with fromJSON in workflows. It exits non-zero if the manifest
// is invalid (unknown fields or kinds, missing dependencies, cycles, ...).
//
// --org also checks the manifest against the organisation's repos
// (manifest.CheckCoverage), read from the GitHub API (GITHUB_API_URL, default
// https://api.github.com) with GH_TOKEN if set. It only reads public metadata.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/manifest"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "manifest:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("manifest", manifest.DefaultPath, "path to the repo manifest")
	asJSON := fs.Bool("json", false, "print the stages as a JSON array of arrays")
	org := fs.String("org", "", "also check the manifest covers this GitHub organisation's repos (include_patterns, exclude)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	m, err := manifest.Load(*path)
	if err != nil {
		return err
	}
	stages, err := m.Stages()
	if err != nil {
		return err
	}
	if *org != "" {
		repos, err := orgRepos(context.Background(), getenv, *org)
		if err != nil {
			return err
		}
		if err := m.CheckCoverage(repos); err != nil {
			return fmt.Errorf("%s against %s:\n%w", *path, *org, err)
		}
		if _, err := fmt.Fprintf(stderr, "%s covers the %d repos of %s\n", *path, len(repos), *org); err != nil {
			return err
		}
	}

	if *asJSON {
		return json.NewEncoder(stdout).Encode(stages)
	}
	for i, stage := range stages {
		if _, err := fmt.Fprintf(stdout, "stage %d: %s\n", i+1, strings.Join(stage, " ")); err != nil {
			return err
		}
	}
	return nil
}

// perPage is the GitHub API's largest page size.
const perPage = 100

// orgRepos lists every repo of org that the token (if any) can see, archived ones included.
func orgRepos(ctx context.Context, getenv func(string) string, org string) ([]manifest.OrgRepo, error) {
	base := strings.TrimSuffix(getenv("GITHUB_API_URL"), "/")
	if base == "" {
		base = "https://api.github.com"
	}
	client := &http.Client{Timeout: 30 * time.Second}
	var all []manifest.OrgRepo
	for page := 1; ; page++ {
		u := fmt.Sprintf("%s/orgs/%s/repos?type=all&per_page=%d&page=%d", base, url.PathEscape(org), perPage, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		if t := getenv("GH_TOKEN"); t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list %s repos: %w", org, err)
		}
		var repos []manifest.OrgRepo
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("list %s repos: %s", org, resp.Status)
		} else {
			err = json.NewDecoder(resp.Body).Decode(&repos)
		}
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		all = append(all, repos...)
		if len(repos) < perPage {
			return all, nil
		}
	}
}
