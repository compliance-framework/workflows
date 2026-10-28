// Command image-scan finds fixable HIGH and CRITICAL vulnerabilities in the CCF container images
// and plans their fixes as Renovate package rules, for renovate.yml's image-scan job:
//
//	image-scan list [--manifest repos.yaml] --repos a,b [--dry-run full]
//	image-scan check --image ref [--name name] [--summary file] [--cache-dir dir]
//	image-scan scan [--manifest repos.yaml] --repos a,b --overrides file [--summary file] [--cache-dir dir] [--owner org] [--dry-run full] [--author ccf-release-bot[bot]]
//
// list prints the repos of --repos (renovate-config list's selection) whose kind has Dockerfiles
// (go-service, ui, action), comma-separated, to scope the job's token; it prints an empty line
// when there are none. scan reads each repo's images from its caller workflows (GH_TOKEN, read
// only), scans the base images of their Dockerfiles and the published :main and :latest images
// with osv-scanner (run with Docker) plus the runtime check, writes the Renovate rules it plans as
// a JSON list to --overrides and a report to --summary. On a live run (--dry-run off) it then
// keeps each repo's image-scan/os-patches branch and PR in line with the OS package patches it
// plans, committing as GIT_AUTHOR_NAME <GIT_AUTHOR_EMAIL> with GH_TOKEN (Contents and Pull
// requests write); a dry run only logs them. It exits non-zero, after all that, if an image could
// not be scanned or a PR not updated. check, for ci-image.yml, scans a locally built image (--image,
// not pulled) with the same scanner and runtime check, writes every finding to --summary and the
// log, and fails when any is fixable and HIGH or CRITICAL. The rules are in internal/imagescan;
// see docs/image-scan.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

const usage = "usage: image-scan list|scan|check [flags]"

// deps are the scan's outside world; nil uses the real ones (tests pass fakes).
type deps struct {
	files    imagescan.Files
	scanner  imagescan.ImageScanner
	registry imagescan.Registry
	eol      imagescan.Cycler
	patches  imagescan.PatchGitHub
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer, d *deps) error {
	if len(args) == 0 || (args[0] != "list" && args[0] != "scan" && args[0] != "check") {
		return errors.New(usage)
	}
	if args[0] == "check" {
		return check(ctx, args[1:], getenv, stdout, d)
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
	author := fs.String("author", "ccf-release-bot[bot]", "scan: the bot's login (the token's identity); an OS patch PR with anyone else's commit is left alone")
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
	if *cacheDir != "" {
		// Docker mounts it: an absolute path.
		if *cacheDir, err = filepath.Abs(*cacheDir); err != nil {
			return err
		}
	}
	if d == nil {
		if getenv("GH_TOKEN") == "" {
			return errors.New("GH_TOKEN is not set")
		}
		base := getenv("GITHUB_API_URL")
		if base == "" {
			base = "https://api.github.com"
		}
		gh := &bump.GitHub{BaseURL: base, Token: getenv("GH_TOKEN"), Owner: *owner}
		d = &deps{
			files:   &imagescan.GitHubFiles{GH: gh},
			patches: gh,
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
	pub := &imagescan.Publisher{
		GH: d.patches, Author: *author, DryRun: *dryRun != renovate.DryRunOff, Out: stdout,
		Identity: bump.Identity{Name: getenv("GIT_AUTHOR_NAME"), Email: getenv("GIT_AUTHOR_EMAIL")},
	}
	for _, r := range results {
		if len(r.Errors) > 0 {
			// Its plan may miss a Dockerfile: closing or rewriting the PR on that would be wrong.
			fmt.Fprintf(stdout, "%s: OS package patches skipped, the scan is incomplete\n", r.Repo)
			continue
		}
		if err := pub.Publish(ctx, r.Repo, r.Patches); err != nil {
			fmt.Fprintf(stdout, "::warning::image-scan: %s: OS package patch PR: %v\n", r.Repo, err)
			failed = append(failed, r.Repo)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not scan or patch everything in %s (the rules for the rest are written)", strings.Join(compact(failed), ", "))
	}
	return nil
}

// check scans a locally built image and fails on any fixable HIGH or CRITICAL finding.
func check(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer, d *deps) error {
	fs := flag.NewFlagSet("image-scan check", flag.ContinueOnError)
	image := fs.String("image", "", "the local image to check (required; not pulled)")
	name := fs.String("name", "", "the image's name in the report (default: --image)")
	summary := fs.String("summary", "", "append the report here (ci-image.yml passes $GITHUB_STEP_SUMMARY)")
	cacheDir := fs.String("cache-dir", "", "osv-scanner's offline database cache; empty queries the OSV API")
	scannerImage := fs.String("scanner", imagescan.ScannerImage, "the osv-scanner image")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *image == "" {
		return errors.New("--image is required")
	}
	if *name == "" {
		*name = *image
	}
	var scanner imagescan.ImageScanner
	if d != nil {
		scanner = d.scanner
	} else {
		dir := *cacheDir
		if dir != "" {
			var err error
			if dir, err = filepath.Abs(dir); err != nil {
				return err
			}
		}
		scanner = &imagescan.Scanner{
			Run: imagescan.ExecRunner{}, Image: *scannerImage, OSV: &imagescan.OSV{BaseURL: imagescan.OSVAPI},
			TempDir: getenv("RUNNER_TEMP"), CacheDir: dir,
		}
	}
	s, err := scanner.Scan(ctx, *image)
	if err != nil {
		return err
	}
	for _, f := range s.Findings {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\n", f.Package, f.Type, f.Installed, orDash(strings.Join(f.Fixed, ",")), f.Severity(), strings.Join(append(slices.Clone(f.IDs), f.Aliases...), ","))
	}
	if *summary != "" {
		f, err := os.OpenFile(*summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		werr := imagescan.WriteCheckSummary(f, *name, s)
		if err := f.Close(); werr == nil {
			werr = err
		}
		if werr != nil {
			return werr
		}
	}
	n := 0
	for _, f := range s.Findings {
		if f.Actionable() {
			n++
			fmt.Fprintf(stdout, "::error::%s: %s %s %s is %s (%s), fixed in %s\n", *name, f.Type, f.Package, f.Installed, f.Severity(), strings.Join(f.CVEs(), ", "), strings.Join(f.Fixed, ", "))
		}
	}
	if n > 0 {
		return fmt.Errorf("%s: %d fixable HIGH or CRITICAL finding(s)", *name, n)
	}
	fmt.Fprintf(stdout, "%s: no fixable HIGH or CRITICAL finding (%d finding(s) in all)\n", *name, len(s.Findings))
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
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
