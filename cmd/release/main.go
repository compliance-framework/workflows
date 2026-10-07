// Command release runs the release rules (internal/release) for the release workflows:
//
//	release check internal-deps [--gomod go.mod]
//	release check module-path [--gomod go.mod] [--manifest .release-please-manifest.json]
//	release check version-guard --base base.json [--head .release-please-manifest.json]
//	release next-rc --version X.Y.Z [--prefix v]    (existing tags on stdin, one per line)
//	release preview-tags [--on-main=true|false]     (appends tags=... to $GITHUB_OUTPUT)
//	release release-tags --tag T [--prefix v] [--style image|artifact]
//	                                               (appends tags=... and final=... to $GITHUB_OUTPUT)
//
// version-guard and preview-tags read the PR from $GITHUB_EVENT_PATH.
package main

import (
	"bufio"
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
	if err := run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "::error::"+err.Error())
		os.Exit(1)
	}
}

const usage = "usage: release check internal-deps|module-path|version-guard [flags] | next-rc [flags] | preview-tags [flags] | release-tags [flags]"

func run(args []string, getenv func(string) string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "next-rc":
		fs := flag.NewFlagSet("release next-rc", flag.ContinueOnError)
		version := fs.String("version", "", "version (X.Y.Z) to cut a release candidate of")
		prefix := fs.String("prefix", "v", "tag prefix")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return nextRC(*prefix, *version, stdin, stdout)
	case "preview-tags":
		fs := flag.NewFlagSet("release preview-tags", flag.ContinueOnError)
		onMain := fs.Bool("on-main", true, "publish previews on pushes to the default branch")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return previewTags(*onMain, getenv, stdout)
	case "release-tags":
		fs := flag.NewFlagSet("release release-tags", flag.ContinueOnError)
		tag := fs.String("tag", "", "the release's git tag (required)")
		prefix := fs.String("prefix", "v", "tag prefix")
		style := fs.String("style", release.ImageTags, "registry tag style: image or artifact")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return releaseTags(*tag, *prefix, *style, getenv, stdout)
	}
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
	e, err := readEvent(getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return err
	}
	if !slices.Contains(e.PullRequest.labels(), release.MajorApprovedLabel) {
		return fmt.Errorf("major version increase (%s) needs the %q label", strings.Join(inc, ", "), release.MajorApprovedLabel)
	}
	fmt.Fprintf(stdout, "Major version increase (%s) approved by the %q label.\n", strings.Join(inc, ", "), release.MajorApprovedLabel)
	return nil
}

func nextRC(prefix, version string, stdin io.Reader, stdout io.Writer) error {
	var tags []string
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		tags = append(tags, strings.TrimSpace(sc.Text()))
	}
	if err := sc.Err(); err != nil {
		return err
	}
	tag, err := release.NextRC(prefix, version, tags)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, tag)
	return nil
}

func previewTags(onMain bool, getenv func(string) string, stdout io.Writer) error {
	e, err := readEvent(getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return err
	}
	pr := e.PullRequest
	tags, reason := release.PreviewTags(release.Event{
		Name: getenv("GITHUB_EVENT_NAME"), Ref: getenv("GITHUB_REF"), SHA: getenv("GITHUB_SHA"),
		DefaultBranch: e.Repository.DefaultBranch, PRNumber: pr.Number, Labels: pr.labels(),
		Fork: pr.Head.Repo.FullName != "" && pr.Head.Repo.FullName != e.Repository.FullName,
	}, onMain)
	if len(tags) == 0 {
		fmt.Fprintln(stdout, "No preview: "+reason+".")
	} else {
		fmt.Fprintln(stdout, "Preview tags: "+strings.Join(tags, " "))
	}
	return writeOutputs(getenv, "tags="+strings.Join(tags, " "))
}

func releaseTags(tag, prefix, style string, getenv func(string) string, stdout io.Writer) error {
	tags, final, err := release.ReleaseTags(tag, prefix, style)
	if err != nil {
		return err
	}
	kind := "pre-release"
	if final {
		kind = "final release"
	}
	fmt.Fprintf(stdout, "Release tags for %s (%s): %s\n", tag, kind, strings.Join(tags, " "))
	return writeOutputs(getenv, "tags="+strings.Join(tags, " "), fmt.Sprintf("final=%t", final))
}

// writeOutputs appends name=value lines to $GITHUB_OUTPUT.
func writeOutputs(getenv func(string) string, lines ...string) error {
	out := getenv("GITHUB_OUTPUT")
	if out == "" {
		return errors.New("GITHUB_OUTPUT is not set")
	}
	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, strings.Join(lines, "\n")); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func readManifest(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m, err := release.ParseManifest(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// event is the part of a GitHub event payload the commands read.
type event struct {
	Repository struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	PullRequest pullRequest `json:"pull_request"`
}

type pullRequest struct {
	Number int `json:"number"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Head struct {
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
}

func (pr pullRequest) labels() []string {
	var out []string
	for _, l := range pr.Labels {
		out = append(out, l.Name)
	}
	return out
}

func readEvent(path string) (event, error) {
	var e event
	b, err := os.ReadFile(path)
	if err != nil {
		return e, fmt.Errorf("event payload: %w", err)
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return e, fmt.Errorf("event payload: %w", err)
	}
	return e, nil
}
