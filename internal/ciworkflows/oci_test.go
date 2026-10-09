package ciworkflows_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	// The tags job differs from release-go-image.yml's only in the tag style.
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
		// Once, to the first tag: "Tag the rest" points latest at the same manifest.
		{"release-go-plugin.yml", "publish", "Upload the plugin", "v1.2.3 latest",
			"upload --annotate=org.ccf.plugin.protocol.version=2 dist/ ghcr.io/compliance-framework/mock-plugin:v1.2.3\n"},
		{"release-policies.yml", "publish", "Upload the bundle", "v1.2.3 latest",
			"upload-single $RUNNER_TEMP/bundle.tar.gz ghcr.io/compliance-framework/mock-plugin:v1.2.3\n"},
		{"release-policies.yml", "publish", "Upload the bundle", "v1.2.3-rc1",
			"upload-single $RUNNER_TEMP/bundle.tar.gz ghcr.io/compliance-framework/mock-plugin:v1.2.3-rc1\n"},
	} {
		t.Run(tc.file+" "+tc.tags, func(t *testing.T) {
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

const tagTheRest = "Tag the rest"

// TestUploadOnceTagTheRest: every job that uploads a gooci artifact uploads it once and then
// runs the one "Tag the rest" step, with the one crane install, so all of a build's tags share
// one manifest digest (the agent records it as _plugin_digest and _policy_digest). A gooci
// upload per tag made a new manifest each time.
func TestUploadOnceTagTheRest(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var want, first, wantCrane string
	var jobs []string
	for _, path := range files {
		file := filepath.Base(path)
		if file == "plugin-release.yml" { // legacy, unchanged until removed (README)
			continue
		}
		var wf workflow
		read(t, file, &wf)
		for name, job := range wf.Jobs {
			upload, crane := -1, ""
			for i, s := range job.Steps {
				if s.Name == "Install crane" {
					crane = s.Run
				}
				if !strings.Contains(s.Run, "gooci upload") {
					continue
				}
				upload = i
				if n := strings.Count(s.Run, "gooci upload"); n != 1 || strings.Contains(s.Run, "for ") {
					t.Errorf("%s: job %s step %q: want one gooci upload outside any loop (found %d)", file, name, s.Name, n)
				}
			}
			if upload < 0 {
				continue
			}
			jobs = append(jobs, file+":"+name)
			switch {
			case !strings.Contains(crane, "go install github.com/google/go-containerregistry/cmd/crane@v"):
				t.Errorf("%s: job %s: want an \"Install crane\" step with a pinned go install, got %q", file, name, crane)
			case wantCrane == "":
				wantCrane = crane
			case crane != wantCrane:
				t.Errorf("%s: job %s: \"Install crane\" is %q, want %q (every job the same version)", file, name, crane, wantCrane)
			}
			if upload+1 >= len(job.Steps) || job.Steps[upload+1].Name != tagTheRest {
				t.Errorf("%s: job %s: want %q right after the upload", file, name, tagTheRest)
				continue
			}
			switch got := job.Steps[upload+1].Run; {
			case want == "":
				want, first = got, file+":"+name
			case got != want:
				t.Errorf("%s: job %s: %q differs from %s's", file, name, tagTheRest, first)
			}
		}
	}
	slices.Sort(jobs)
	if wantJobs := []string{"preview.yml:plugin", "preview.yml:policies", "release-go-plugin.yml:publish", "release-policies.yml:publish"}; !slices.Equal(jobs, wantJobs) {
		t.Errorf("gooci uploads in %v, want %v", jobs, wantJobs)
	}
}

// fakeCrane is a registry of tag -> digest lines in $FAKE_REGISTRY (the last line for a tag
// wins). `crane tag <repo>@<digest> <tag>` adds a line unless FAKE_TAG_NOOP is set.
const fakeCrane = `#!/bin/sh
echo "$@" >> "$RUNNER_TEMP/crane"
case "$1" in
digest)
  d=$(grep "^$2 " "$FAKE_REGISTRY" | tail -n 1 | cut -d' ' -f2)
  [ -n "$d" ] || { echo "MANIFEST_UNKNOWN: $2" >&2; exit 1; }
  echo "$d" ;;
tag)
  [ -n "$FAKE_TAG_NOOP" ] || echo "${2%@*}:$3 ${2#*@}" >> "$FAKE_REGISTRY" ;;
*) exit 2 ;;
esac
`

func TestTagTheRest(t *testing.T) {
	needBash4(t)
	src := script(t, "release-go-plugin.yml", "publish", tagTheRest)
	const repo = "ghcr.io/compliance-framework/mock-plugin"
	for _, tc := range []struct {
		name, tags, registry, noop string
		calls, out                 []string
		failed                     bool
	}{
		{name: "final release moves latest to the upload", tags: "v1.2.3 latest",
			registry: repo + ":latest sha256:old\n" + repo + ":v1.2.3 sha256:new\n",
			calls: []string{"digest " + repo + ":v1.2.3", "tag " + repo + "@sha256:new latest",
				"digest " + repo + ":v1.2.3", "digest " + repo + ":latest"},
			out: []string{repo + ": v1.2.3 latest all at sha256:new"}},
		{name: "release candidate has one tag", tags: "v1.2.3-rc1",
			registry: repo + ":v1.2.3-rc1 sha256:rc\n",
			calls:    []string{"digest " + repo + ":v1.2.3-rc1", "digest " + repo + ":v1.2.3-rc1"}},
		{name: "preview on main", tags: "main sha-abc1234",
			registry: repo + ":main sha256:m\n",
			calls: []string{"digest " + repo + ":main", "tag " + repo + "@sha256:m sha-abc1234",
				"digest " + repo + ":main", "digest " + repo + ":sha-abc1234"}},
		{name: "a tag that did not move fails", tags: "v1.2.3 latest", noop: "1",
			registry: repo + ":latest sha256:old\n" + repo + ":v1.2.3 sha256:new\n", failed: true,
			out: []string{"::error::" + repo + ":latest is sha256:old, not sha256:new (the upload to :v1.2.3)"}},
		{name: "a missing upload fails", tags: "v1.2.3 latest", failed: true,
			calls: []string{"digest " + repo + ":v1.2.3"}},
		{name: "no tags fails", tags: "", failed: true, out: []string{"::error::no tags to publish"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := filepath.Join(t.TempDir(), "registry")
			if err := os.WriteFile(registry, []byte(tc.registry), 0o600); err != nil {
				t.Fatal(err)
			}
			r := run(t, t.TempDir(), src, fakeBin(t, "crane", fakeCrane), "GITHUB_REPOSITORY=compliance-framework/Mock-Plugin",
				"TAGS="+tc.tags, "FAKE_REGISTRY="+registry, "FAKE_TAG_NOOP="+tc.noop)
			if r.failed != tc.failed {
				t.Fatalf("failed=%v, want %v; output:\n%s", r.failed, tc.failed, r.out)
			}
			b, _ := os.ReadFile(filepath.Join(r.temp, "crane"))
			if tc.calls != nil {
				if got := strings.Split(strings.TrimSpace(string(b)), "\n"); !slices.Equal(got, tc.calls) {
					t.Errorf("crane calls:\n%s\nwant:\n%s", b, strings.Join(tc.calls, "\n"))
				}
			}
			for _, want := range tc.out {
				if !strings.Contains(r.out, want) {
					t.Errorf("output lacks %q:\n%s", want, r.out)
				}
			}
		})
	}
}
