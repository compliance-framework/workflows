package ciworkflows_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestReleaseCopies pins the jobs the release workflows and preview.yml repeat to one copy.
func TestReleaseCopies(t *testing.T) {
	jobs := func(file string) map[string]any {
		var wf struct {
			Jobs map[string]any `yaml:"jobs"`
		}
		read(t, file, &wf)
		return wf.Jobs
	}
	// The tags job differs from release-go-image.yml's only in the tag style, and the two
	// artifact workflows end in the same finished job.
	asYAML := func(v any) string {
		b, err := yaml.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	imageTags := asYAML(jobs("release-go-image.yml")["tags"])
	for _, file := range []string{"release-go-plugin.yml", "release-policies.yml"} {
		if got := asYAML(jobs(file)["tags"]); strings.Replace(got, "--style artifact", "--style image", 1) != imageTags || got == imageTags {
			t.Errorf("%s: the tags job is not release-go-image.yml's with --style artifact:\n%s", file, got)
		}
	}
	if !reflect.DeepEqual(jobs("release-policies.yml")["finished"], jobs("release-go-plugin.yml")["finished"]) {
		t.Error("the finished job differs between release-go-plugin.yml and release-policies.yml")
	}
	// A preview publishes the way a release does, from a snapshot instead of a release.
	if !reflect.DeepEqual(jobs("preview.yml")["policies"].(map[string]any)["steps"], jobs("release-policies.yml")["publish"].(map[string]any)["steps"]) {
		t.Error("the policy bundle steps differ between preview.yml and release-policies.yml")
	}
	if script(t, "preview.yml", "plugin", "Upload the plugin") != script(t, "release-go-plugin.yml", "publish", "Upload the plugin") {
		t.Error("the plugin upload differs between preview.yml and release-go-plugin.yml")
	}
}

func TestPreviewKind(t *testing.T) {
	src := script(t, "preview.yml", "tags", "Check the kind")
	for kind, failed := range map[string]bool{"image": false, "go-plugin": false, "policies": false, "": true, "helm": true} {
		if r := run(t, t.TempDir(), src, "KIND="+kind); r.failed != failed {
			t.Errorf("kind %q: failed=%v, want %v; output:\n%s", kind, r.failed, failed, r.out)
		}
	}
}

func TestGoociUpload(t *testing.T) {
	needBash4(t)
	path := fakeBin(t, "gooci", "#!/bin/sh\necho \"$@\" >> \"$RUNNER_TEMP/gooci\"\n")
	env := []string{path, "GITHUB_REPOSITORY=compliance-framework/Mock-Plugin", "PROTOCOL_VERSION=2"}
	for _, tc := range []struct{ file, job, step, tags, want string }{
		{"release-go-plugin.yml", "publish", "Upload the plugin", "v1.2.3 latest",
			"upload --annotate=org.ccf.plugin.protocol.version=2 dist/ ghcr.io/compliance-framework/mock-plugin:v1.2.3\n" +
				"upload --annotate=org.ccf.plugin.protocol.version=2 dist/ ghcr.io/compliance-framework/mock-plugin:latest\n"},
		{"release-policies.yml", "publish", "Upload the bundle", "v1.2.3-rc1",
			"upload-single $RUNNER_TEMP/bundle.tar.gz ghcr.io/compliance-framework/mock-plugin:v1.2.3-rc1\n"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			src := script(t, tc.file, tc.job, tc.step)
			r := run(t, t.TempDir(), src, append(env, "TAGS="+tc.tags)...)
			b, _ := os.ReadFile(filepath.Join(r.temp, "gooci"))
			if want := strings.ReplaceAll(tc.want, "$RUNNER_TEMP", r.temp); r.failed || string(b) != want {
				t.Fatalf("failed=%v; gooci calls:\n%s\nwant:\n%s\noutput:\n%s", r.failed, b, want, r.out)
			}
			if r := run(t, t.TempDir(), src, append(env, "TAGS=")...); !r.failed {
				t.Fatalf("no tags: want a failure; output:\n%s", r.out)
			}
		})
	}
}
