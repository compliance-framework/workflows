// Command manifest validates a repo manifest and prints its release stages.
//
//	go run ./cmd/manifest [--manifest repos.yaml] [--json]
//
// Text output is one stage per line; --json prints the stages as a JSON array of
// arrays, for use with fromJSON in workflows. It exits non-zero if the manifest
// is invalid (unknown fields or kinds, missing dependencies, cycles, ...).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/compliance-framework/workflows/internal/manifest"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "manifest:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("manifest", manifest.DefaultPath, "path to the repo manifest")
	asJSON := fs.Bool("json", false, "print the stages as a JSON array of arrays")
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
