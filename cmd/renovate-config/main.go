// Command renovate-config prepares a renovate.yml run from a manifest:
//
//	renovate-config list   [--manifest repos.yaml] [--repos a,b] [--dry-run full] [--max-repos 15] [--slot N]
//	renovate-config config [--manifest repos.yaml] [--repos a,b] [--dry-run full] [--max-repos 15] [--slot N] [--preset renovate/default.json] [--owner compliance-framework]
//
// list prints the selected repo names, comma-separated, to scope the token. config prints the
// Renovate global config (internal/renovate) as JSON. Both select every active repo in the
// manifest unless --repos names a subset; a dry run may name repos of an inactive batch. A live run
// (--dry-run off) takes only one shard of at most --max-repos of them, picked by --slot, so pass
// both commands the same --slot.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/manifest"
	"github.com/compliance-framework/workflows/internal/renovate"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "::error::renovate-config: "+err.Error())
		os.Exit(1)
	}
}

const usage = "usage: renovate-config list|config [flags]"

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "list" && args[0] != "config") {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("renovate-config "+args[0], flag.ContinueOnError)
	path := fs.String("manifest", manifest.DefaultPath, "path to the repo manifest")
	repos := fs.String("repos", "", "comma- or space-separated repo names (default: every active repo in the manifest)")
	owner := fs.String("owner", "compliance-framework", "the repos' owner")
	preset := fs.String("preset", "renovate/default.json", "the shared Renovate preset (config)")
	dryRun := fs.String("dry-run", "full", "full, lookup, extract, or off to run live; only a dry run may name repos of an inactive batch")
	maxRepos := fs.Int("max-repos", renovate.MaxLiveRepos, "a live run takes one shard of at most this many repos (0: every selected repo)")
	slot := fs.Int64("slot", time.Now().Unix()/3600, "picks the live run's shard (slot mod shards); default: hours since the Unix epoch")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *maxRepos < 0 {
		return fmt.Errorf("--max-repos %d: want 0 (no limit) or more", *maxRepos)
	}
	m, err := manifest.Load(*path)
	if err != nil {
		return err
	}
	selected, err := renovate.SelectRepos(m, *repos, *dryRun)
	if err != nil {
		return err
	}
	if *dryRun == renovate.DryRunOff {
		var index, count int
		all := len(selected)
		selected, index, count = renovate.Shard(selected, *maxRepos, *slot)
		if count > 1 {
			fmt.Fprintf(stderr, "live run: shard %d of %d (%d of %d repos, at most %d per run): %s\n",
				index+1, count, len(selected), all, *maxRepos, strings.Join(selected, ","))
		}
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
