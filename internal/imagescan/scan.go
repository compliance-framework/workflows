package imagescan

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// ImageKinds are the manifest kinds whose repos have Dockerfiles: go-service and ui publish their
// images to ghcr.io, and an action (agent-action) is built by the runner from its Dockerfile on
// each use, so it has base images to fix but no published image.
var ImageKinds = []manifest.Kind{manifest.KindGoService, manifest.KindUI, manifest.KindAction}

// Publishes reports whether a repo of kind publishes images to ghcr.io.
func Publishes(kind manifest.Kind) bool {
	return kind == manifest.KindGoService || kind == manifest.KindUI
}

// PublishedTags are the tags of a repo's published images that are scanned: the default branch's
// preview and the latest release.
var PublishedTags = []string{"main", "latest"}

// RepoResult is what scanning one repo found and planned.
type RepoResult struct {
	Repo      string
	Images    []Image
	Targets   []Target // the Dockerfiles' base images
	Published []Target // the published images (Base.Role empty)
	Rules     []Rule
	Patches   []FilePatch // the Dockerfiles whose managed block changes (docs/image-scan.md#os-package-patches)
	Notes     []string
	// Errors are what kept the scan from being complete: a Dockerfile that can't be read or
	// parsed, a base image that can't be scanned. A published image that can't be pulled is a
	// note (a repo without a release has no :latest).
	Errors []string
}

// Env is what Run needs.
type Env struct {
	Owner    string
	Files    Files
	Scanner  ImageScanner // cached by Run
	Registry Registry
	EOL      Cycler
	Log      io.Writer
}

// Run scans the Dockerfiles' base images and the published images of each repo and plans their
// rules. An image is scanned once per run, however many repos use it.
func Run(ctx context.Context, env Env, repos []manifest.Repo) []RepoResult {
	scanner := &cachedScanner{next: env.Scanner, log: env.Log, done: map[string]cached{}}
	planner := &Planner{Owner: env.Owner, Scanner: scanner, Registry: env.Registry, EOL: env.EOL}
	var out []RepoResult
	for _, repo := range repos {
		res := RepoResult{Repo: repo.Name}
		errorf := func(format string, args ...any) { res.Errors = append(res.Errors, fmt.Sprintf(format, args...)) }
		res.Images = []Image{Image{}.withDefaults(repo.Name)}
		if Publishes(repo.Kind) {
			imgs, warnings, err := RepoImages(ctx, env.Files, env.Owner, repo.Name)
			res.Notes = append(res.Notes, warnings...)
			if err != nil {
				errorf("%v", err)
				out = append(out, res)
				continue
			}
			res.Images = imgs
		}
		dockerfiles := map[string][]byte{} // path -> source, for the OS package patches
		for _, img := range res.Images {
			if _, done := dockerfiles[img.Dockerfile]; done {
				continue // a second image built from the same Dockerfile
			}
			src, ok, err := env.Files.File(ctx, repo.Name, img.Dockerfile)
			if err != nil {
				errorf("read %s: %v", img.Dockerfile, err)
				continue
			}
			if !ok {
				res.Notes = append(res.Notes, fmt.Sprintf("no %s: image %s skipped", img.Dockerfile, img.Name))
				continue
			}
			df, err := ParseDockerfile(src)
			if err != nil {
				errorf("%s: %v", img.Dockerfile, err)
				continue
			}
			bases, err := df.Bases()
			if err != nil {
				errorf("%s: %v", img.Dockerfile, err)
				continue
			}
			dockerfiles[img.Dockerfile] = src
			for _, b := range bases {
				if slices.ContainsFunc(res.Targets, func(t Target) bool { return t.Dockerfile == img.Dockerfile && t.Base == b }) {
					continue
				}
				s, err := scanner.Scan(ctx, b.Ref.String())
				if err != nil {
					errorf("%s: %s: %v", img.Dockerfile, b.Ref, err)
				}
				res.Notes = appendWarnings(res.Notes, b.Ref.String(), s)
				res.Targets = append(res.Targets, withStage(Target{Dockerfile: img.Dockerfile, Base: b, Scan: s, Err: err}))
			}
		}
		if Publishes(repo.Kind) {
			for _, img := range res.Images {
				for _, tag := range PublishedTags {
					ref := fmt.Sprintf("ghcr.io/%s/%s:%s", env.Owner, img.Name, tag)
					s, err := scanner.Scan(ctx, ref)
					if err != nil {
						res.Notes = append(res.Notes, fmt.Sprintf("published image %s not scanned: %v", ref, err))
					}
					res.Notes = appendWarnings(res.Notes, ref, s)
					res.Published = append(res.Published, Target{Dockerfile: img.Dockerfile, Base: Base{Ref: Ref{Name: "ghcr.io/" + env.Owner + "/" + img.Name, Tag: tag}}, Scan: s, Err: err})
				}
			}
		}
		pl := planner.Plan(ctx, repo.Name, res.Targets)
		res.Rules, res.Notes = pl.Rules, append(res.Notes, pl.Notes...)
		res.Patches = append(res.Patches, osPatches(dockerfiles, res.Targets, pl.Fixed, func(format string, args ...any) {
			res.Notes = append(res.Notes, fmt.Sprintf(format, args...))
		})...)
		out = append(out, res)
	}
	return out
}

// appendWarnings adds the warnings of ref's scan to notes, once per repo (an image can be the base
// of several Dockerfiles).
func appendWarnings(notes []string, ref string, s *Scan) []string {
	if s == nil {
		return notes
	}
	for _, w := range s.Warnings {
		if n := fmt.Sprintf("**warning**: %s: %s", ref, w); !slices.Contains(notes, n) {
			notes = append(notes, n)
		}
	}
	return notes
}

// withStage copies t's scan with each finding's Stage set to the target's stage.
func withStage(t Target) Target {
	if t.Scan == nil {
		return t
	}
	s := *t.Scan
	s.Findings = slices.Clone(s.Findings)
	for i := range s.Findings {
		s.Findings[i].Stage = t.Base.Stage
	}
	t.Scan = &s
	return t
}

// Rules returns every repo's rules, in repo order.
func Rules(results []RepoResult) []Rule {
	var out []Rule
	for _, r := range results {
		out = append(out, r.Rules...)
	}
	return out
}

type cached struct {
	scan *Scan
	err  error
}

type cachedScanner struct {
	next ImageScanner
	log  io.Writer
	done map[string]cached
}

func (c *cachedScanner) Scan(ctx context.Context, ref string) (*Scan, error) {
	if r, ok := c.done[ref]; ok {
		return r.scan, r.err
	}
	if c.log != nil {
		fmt.Fprintf(c.log, "scanning %s\n", ref)
	}
	s, err := c.next.Scan(ctx, ref)
	c.done[ref] = cached{s, err}
	return s, err
}
