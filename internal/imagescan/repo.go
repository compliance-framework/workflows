package imagescan

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Image is one entry of the `images` input of publish-image.yml and the workflows that call it.
type Image struct {
	Name       string `json:"name"`       // defaults to the repo name
	Dockerfile string `json:"dockerfile"` // defaults to Dockerfile, relative to the repo root
	Context    string `json:"context"`    // defaults to .
}

// withDefaults fills in publish-image.yml's defaults.
func (i Image) withDefaults(repo string) Image {
	if i.Name == "" {
		i.Name = repo
	}
	if i.Dockerfile == "" {
		i.Dockerfile = "Dockerfile"
	}
	if i.Context == "" {
		i.Context = "."
	}
	return i
}

// imageWorkflows are the shared workflows whose `images` input lists a repo's images.
var imageWorkflows = regexp.MustCompile(`/workflows/\.github/workflows/(release-go-image|release-ui|preview|publish-image)\.ya?ml@`)

// CallerImages returns the images a repo's workflows (files, by name) pass to the shared image
// workflows of owner, merged by name, in the order first seen. A call without an `images` input
// means the default, one image named after the repo; so does a repo with no such call. A preview
// call of another kind (go-plugin, policies) publishes no image and is skipped.
func CallerImages(owner, repo string, files map[string][]byte, names []string) ([]Image, []string, error) {
	var out []Image
	var warnings []string
	add := func(img Image, file string) {
		img = img.withDefaults(repo)
		for _, o := range out {
			if o.Name == img.Name {
				if o != img {
					warnings = append(warnings, fmt.Sprintf("%s: image %s is %s in one workflow and %s in %s; using the first", repo, img.Name, o.Dockerfile, img.Dockerfile, file))
				}
				return
			}
		}
		out = append(out, img)
	}
	for _, file := range names {
		var wf struct {
			Jobs map[string]struct {
				Uses string         `yaml:"uses"`
				With map[string]any `yaml:"with"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(files[file], &wf); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %s: %v", repo, file, err))
			continue
		}
		// Sorted, so the first job seen is the same every run.
		for _, job := range slices.Sorted(maps.Keys(wf.Jobs)) {
			j := wf.Jobs[job]
			if !strings.HasPrefix(j.Uses, owner+"/") || !imageWorkflows.MatchString(j.Uses) {
				continue
			}
			if kind, ok := j.With["kind"].(string); ok && strings.Contains(j.Uses, "/preview.y") && kind != "image" {
				continue
			}
			raw, ok := j.With["images"].(string)
			if !ok || strings.TrimSpace(raw) == "" {
				add(Image{}, file)
				continue
			}
			var imgs []Image
			if err := json.Unmarshal([]byte(raw), &imgs); err != nil {
				return nil, warnings, fmt.Errorf("%s: %s: job %s: images: %w", repo, file, job, err)
			}
			for _, img := range imgs {
				add(img, file)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, Image{}.withDefaults(repo))
	}
	return out, warnings, nil
}

// Files reads a repo's files on its default branch.
type Files interface {
	// Dir lists the names of the files in dir; a missing dir is an error.
	Dir(ctx context.Context, repo, dir string) ([]string, error)
	// File returns the content of path; ok is false if there is no such file.
	File(ctx context.Context, repo, path string) (data []byte, ok bool, err error)
}

// RepoImages reads repo's caller workflows and returns its images (CallerImages).
func RepoImages(ctx context.Context, fs Files, owner, repo string) ([]Image, []string, error) {
	const dir = ".github/workflows"
	names, err := fs.Dir(ctx, repo, dir)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: list %s: %w", repo, dir, err)
	}
	files := map[string][]byte{}
	var wfs []string
	for _, n := range names {
		if ext := path.Ext(n); ext != ".yml" && ext != ".yaml" {
			continue
		}
		data, ok, err := fs.File(ctx, repo, dir+"/"+n)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: read %s/%s: %w", repo, dir, n, err)
		}
		if ok {
			files[n] = data
			wfs = append(wfs, n)
		}
	}
	return CallerImages(owner, repo, files, wfs)
}
