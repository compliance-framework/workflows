// Command renovate-config prepares a renovate.yml run from a manifest:
//
//	renovate-config list   [--manifest repos.yaml] [--repos a,b]
//	renovate-config config [--manifest repos.yaml] [--repos a,b] [--preset renovate/default.json] [--dry-run full] [--owner compliance-framework]
//
// list prints the selected repo names, comma-separated, to scope the token. config prints the
// Renovate global config (internal/renovate) as JSON. Both select every repo in the manifest
// unless --repos names a subset.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/compliance-framework/workflows/internal/manifest"
	"github.com/compliance-framework/workflows/internal/renovate"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "::error::renovate-config: "+err.Error())
		os.Exit(1)
	}
}

const usage = "usage: renovate-config list|config [flags]"

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 || (args[0] != "list" && args[0] != "config") {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("renovate-config "+args[0], flag.ContinueOnError)
	path := fs.String("manifest", manifest.DefaultPath, "path to the repo manifest")
	repos := fs.String("repos", "", "comma- or space-separated repo names (default: every repo in the manifest)")
	owner := fs.String("owner", "compliance-framework", "the repos' owner")
	preset := fs.String("preset", "renovate/default.json", "the shared Renovate preset (config)")
	dryRun := fs.String("dry-run", "full", "full, lookup, extract, or off to run live (config)")
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
	selected, err := renovate.SelectRepos(m, *repos)
	if err != nil {
		return err
	}
	if args[0] == "list" {
		_, err := fmt.Fprintln(stdout, strings.Join(selected, ","))
		return err
	}
	data, err := os.ReadFile(*preset)
	if err != nil {
		return fmt.Errorf("read preset: %w", err)
	}
	cfg, err := renovate.GlobalConfig(data, renovate.Options{Owner: *owner, Repos: selected, DryRun: *dryRun})
	if err != nil {
		return err
	}
	_, err = stdout.Write(cfg)
	return err
}
