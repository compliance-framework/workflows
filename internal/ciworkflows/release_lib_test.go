package ciworkflows_test

import (
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestReleaseFinishedJobs pins the release-finished.yml tail of every release-<kind> workflow
// to one copy: the same job but for `needs`, which lists every other job, and the images to
// prune.
func TestReleaseFinishedJobs(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "release-*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Not kind release workflows, or (release-ui.yml) one that calls another.
	noTail := []string{"release-checks.yml", "release-finished.yml", "release-please.yml", "release-ui.yml"}
	var want map[string]any
	var tails []string
	for _, path := range files {
		file := filepath.Base(path)
		var wf struct {
			Jobs map[string]map[string]any `yaml:"jobs"`
		}
		read(t, file, &wf)
		finished, ok := wf.Jobs["finished"]
		if !ok {
			if !slices.Contains(noTail, file) {
				t.Errorf("%s: no finished job", file)
			}
			continue
		}
		tails = append(tails, file)
		var needs []string
		for _, n := range finished["needs"].([]any) {
			needs = append(needs, n.(string))
		}
		others := slices.DeleteFunc(slices.Sorted(maps.Keys(wf.Jobs)), func(j string) bool { return j == "finished" })
		if slices.Sort(needs); !slices.Equal(needs, others) {
			t.Errorf("%s: finished needs %v, want every other job %v", file, needs, others)
		}
		got := maps.Clone(finished)
		delete(got, "needs")
		with := maps.Clone(got["with"].(map[string]any))
		delete(with, "images") // release-go-image.yml prunes its images
		got["with"] = with
		if want == nil {
			want = got
		} else if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the finished job differs from %s's:\n%v\nwant:\n%v", file, tails[0], got, want)
		}
	}
	if !slices.Contains(tails, "release-go-lib.yml") {
		t.Fatalf("release-go-lib.yml not checked; checked %v", tails)
	}
}

// TestGoreleaserPins pins every workflow to one goreleaser-action and goreleaser version.
func TestGoreleaserPins(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	pins := map[string][]string{}
	for _, path := range files {
		file := filepath.Base(path)
		if file == "plugin-release.yml" { // legacy, unchanged until removed (README)
			continue
		}
		var wf struct {
			Jobs map[string]struct {
				Steps []struct {
					Uses string            `yaml:"uses"`
					With map[string]string `yaml:"with"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		read(t, file, &wf)
		for _, job := range wf.Jobs {
			for _, s := range job.Steps {
				if strings.HasPrefix(s.Uses, "goreleaser/goreleaser-action@") {
					pin := s.Uses + " " + s.With["version"]
					pins[pin] = append(pins[pin], file)
				}
			}
		}
	}
	if len(pins) != 1 {
		t.Fatalf("want one goreleaser-action and goreleaser version, got %v", pins)
	}
	for _, users := range pins {
		if !slices.Contains(users, "release-go-lib.yml") || !slices.Contains(users, "ci-go-lib.yml") {
			t.Fatalf("goreleaser users %v: want release-go-lib.yml and ci-go-lib.yml", users)
		}
	}
}

func TestReleaseTagGuard(t *testing.T) {
	src := script(t, "release-go-lib.yml", "publish", "Check the release tag")
	if r := run(t, t.TempDir(), src, "TAG=v0.1.0"); r.failed {
		t.Fatalf("a release tag must pass; output:\n%s", r.out)
	}
	if r := run(t, t.TempDir(), src, "TAG="); !r.failed || !strings.Contains(r.out, "release: published") {
		t.Fatalf("no tag must fail; output:\n%s", r.out)
	}
}
