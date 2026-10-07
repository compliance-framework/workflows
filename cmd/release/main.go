// Command release runs the release rules (internal/release) for the release workflows:
//
//	release check internal-deps [--gomod go.mod]
//	release check module-path [--gomod go.mod] [--manifest .release-please-manifest.json]
//	release check version-guard --base base.json [--head .release-please-manifest.json]
//
// version-guard reads the PR's labels from $GITHUB_EVENT_PATH.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/compliance-framework/workflows/internal/release"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "::error::"+err.Error())
		os.Exit(1)
	}
}

const usage = "usage: release check internal-deps|module-path|version-guard [flags]"

func run(args []string, getenv func(string) string, stdout io.Writer) error {
	if len(args) < 2 || args[0] != "check" {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("release check "+args[1], flag.ContinueOnError)
	switch args[1] {
	case "internal-deps":
		gomod := fs.String("gomod", "go.mod", "go.mod file")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		return checkInternalDeps(*gomod, stdout)
	case "module-path":
		gomod := fs.String("gomod", "go.mod", "go.mod file")
		manifest := fs.String("manifest", ".release-please-manifest.json", "release-please manifest")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		return checkModulePath(*gomod, *manifest, stdout)
	case "version-guard":
		base := fs.String("base", "", "release-please manifest on the PR's base (required)")
		head := fs.String("head", ".release-please-manifest.json", "release-please manifest on the PR's head")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *base == "" {
			return errors.New("--base is required")
		}
		return checkVersionGuard(*base, *head, getenv, stdout)
	}
	return errors.New(usage)
}

func checkInternalDeps(gomod string, stdout io.Writer) error {
	b, err := os.ReadFile(gomod)
	if err != nil {
		return err
	}
	bad, err := release.NonFinalDeps(b)
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		return fmt.Errorf("compliance-framework dependencies must be final releases: %s", strings.Join(bad, ", "))
	}
	fmt.Fprintln(stdout, "Every compliance-framework dependency is a final release.")
	return nil
}

func checkModulePath(gomod, manifest string, stdout io.Writer) error {
	b, err := os.ReadFile(gomod)
	if err != nil {
		return err
	}
	m, err := readManifest(manifest)
	if err != nil {
		return err
	}
	v := m["."]
	if v == "" {
		return fmt.Errorf("%s has no version for the root package \".\"", manifest)
	}
	if err := release.CheckModulePath(b, v); err != nil {
		return fmt.Errorf("module path for version %s: %w", v, err)
	}
	fmt.Fprintf(stdout, "The module path matches version %s.\n", v)
	return nil
}

func checkVersionGuard(base, head string, getenv func(string) string, stdout io.Writer) error {
	from, err := readManifest(base)
	if err != nil {
		return err
	}
	to, err := readManifest(head)
	if err != nil {
		return err
	}
	inc, err := release.MajorIncreases(from, to)
	if err != nil {
		return err
	}
	if len(inc) == 0 {
		fmt.Fprintln(stdout, "No major version increase.")
		return nil
	}
	labels, err := prLabels(getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return err
	}
	if !slices.Contains(labels, release.MajorApprovedLabel) {
		return fmt.Errorf("major version increase (%s) needs the %q label", strings.Join(inc, ", "), release.MajorApprovedLabel)
	}
	fmt.Fprintf(stdout, "Major version increase (%s) approved by the %q label.\n", strings.Join(inc, ", "), release.MajorApprovedLabel)
	return nil
}

func readManifest(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return release.ParseManifest(b)
}

// prLabels reads the pull request's label names from a GitHub event payload.
func prLabels(eventPath string) ([]string, error) {
	var e struct {
		PullRequest struct {
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"pull_request"`
	}
	b, err := os.ReadFile(eventPath)
	if err != nil {
		return nil, fmt.Errorf("event payload: %w", err)
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("event payload: %w", err)
	}
	var out []string
	for _, l := range e.PullRequest.Labels {
		out = append(out, l.Name)
	}
	return out, nil
}
