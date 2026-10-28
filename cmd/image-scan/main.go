// Command image-scan finds fixable HIGH and CRITICAL vulnerabilities in the CCF container images
// and plans their fixes as Renovate package rules, for renovate.yml's image-scan job:
//
//	image-scan list [--manifest repos.yaml] --repos a,b [--dry-run full]
//	image-scan scan [--manifest repos.yaml] --repos a,b --overrides file [--summary file] [--cache-dir dir] [--owner org]
//
// list prints the repos of --repos (renovate-config list's selection) whose kind has Dockerfiles
// (go-service, ui, action), comma-separated, to scope the job's token; it prints an empty line
// when there are none. scan reads each repo's images from its caller workflows (GH_TOKEN, read
// only), scans the base images of their Dockerfiles and the published :main and :latest images
// with osv-scanner (run with Docker) plus the runtime check, writes the Renovate rules it plans as
// a JSON list to --overrides and a report to --summary. It exits non-zero, after writing both, if
// an image could not be scanned. The rules are in internal/imagescan; see docs/image-scan.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/compliance-framework/workflows/internal/bump"
	"github.com/compliance-framework/workflows/internal/imagescan"
	"github.com/compliance-framework/workflows/internal/manifest"
	"github.com/compliance-framework/workflows/internal/renovate"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, nil); err != nil {
		fmt.Fprintln(os.Stderr, "::error::image-scan: "+err.Error())
		os.Exit(1)
	}
}

const usage = "usage: image-scan list|scan [flags]"

// deps are the scan's outside world; nil uses the real ones (tests pass fakes).
type deps struct {
	files    imagescan.Files
	scanner  imagescan.ImageScanner
	registry imagescan.Registry
	eol      imagescan.Cycler
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer, d *deps) error {
	if len(args) == 0 || (args[0] != "list" && args[0] != "scan") {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("image-scan "+args[0], flag.ContinueOnError)
	path := fs.String("manifest", manifest.DefaultPath, "path to the repo manifest")
	repos := fs.String("repos", "", "comma- or space-separated repo names (renovate-config list's selection)")
	dryRun := fs.String("dry-run", "full", "the Renovate run's dry run mode; only a dry run may name repos of an inactive batch")
	owner := fs.String("owner", "compliance-framework", "the repos' owner")
	overrides := fs.String("overrides", "", "scan: write the planned Renovate packageRules here, as a JSON list")
	summary := fs.String("summary", "", "scan: append the report here (renovate.yml passes $GITHUB_STEP_SUMMARY)")
	cacheDir := fs.String("cache-dir", "", "scan: osv-scanner's offline database cache; empty queries the OSV API")
	scannerImage := fs.String("scanner", imagescan.ScannerImage, "scan: the osv-scanner image")
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
	selected, err := imageRepos(m, *repos, *dryRun)
	if err != nil {
		return err
	}
	if args[0] == "list" {
		names := make([]string, len(selected))
		for i, r := range selected {
			names[i] = r.Name
		}
		_, err := fmt.Fprintln(stdout, strings.Join(names, ","))
		return err
	}

	if *overrides == "" {
		return errors.New("--overrides is required")
	}
	if d == nil {
		if getenv("GH_TOKEN") == "" {
			return errors.New("GH_TOKEN is not set")
		}
		base := getenv("GITHUB_API_URL")
		if base == "" {
			base = "https://api.github.com"
		}
		d = &deps{
			files: &imagescan.GitHubFiles{GH: &bump.GitHub{BaseURL: base, Token: getenv("GH_TOKEN"), Owner: *owner}},
			scanner: &imagescan.Scanner{
				Run:      imagescan.ExecRunner{},
				Image:    *scannerImage,
				OSV:      &imagescan.OSV{BaseURL: imagescan.OSVAPI},
				TempDir:  getenv("RUNNER_TEMP"),
				CacheDir: *cacheDir,
				Pull:     true,
			},
			registry: imagescan.Crane{},
			eol:      &imagescan.EOL{BaseURL: imagescan.EOLAPI},
		}
	}
	results := imagescan.Run(ctx, imagescan.Env{
		Owner: *owner, Files: d.files, Scanner: d.scanner, Registry: d.registry, EOL: d.eol, Log: stdout,
	}, selected)

	rules := imagescan.Rules(results)
	if rules == nil {
		rules = []imagescan.Rule{}
	}
	data, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*overrides, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := imagescan.WriteLog(stdout, results); err != nil {
		return err
	}
	if *summary != "" {
		f, err := os.OpenFile(*summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		werr := imagescan.WriteSummary(f, results)
		if err := f.Close(); werr == nil {
			werr = err
		}
		if werr != nil {
			return werr
		}
	}
	fmt.Fprintf(stdout, "%d Renovate rule(s) written to %s\n", len(rules), *overrides)
	var failed []string
	for _, r := range results {
		for _, e := range r.Errors {
			fmt.Fprintf(stdout, "::warning::image-scan: %s: %s\n", r.Repo, e)
			failed = append(failed, r.Repo)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not scan everything in %s (the rules for the rest are written)", strings.Join(compact(failed), ", "))
	}
	return nil
}

// imageRepos returns the repos of list (validated as renovate-config validates them) whose kind
// has Dockerfiles, in manifest order.
func imageRepos(m *manifest.Manifest, list, dryRun string) ([]manifest.Repo, error) {
	names, err := renovate.SelectRepos(m, list, dryRun)
	if err != nil {
		return nil, err
	}
	var out []manifest.Repo
	for _, r := range m.Repos {
		for _, n := range names {
			if r.Name == n && slices.Contains(imagescan.ImageKinds, r.Kind) {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func compact(s []string) []string {
	var out []string
	for _, v := range s {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}
