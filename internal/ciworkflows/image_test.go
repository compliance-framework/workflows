package ciworkflows_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const ciImage = "ci-image.yml"

// callerJob is a job that calls a reusable workflow.
type callerJob struct {
	If          string            `yaml:"if"`
	Uses        string            `yaml:"uses"`
	With        map[string]string `yaml:"with"`
	Needs       []string          `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
}

type callerWorkflow struct {
	On struct {
		Call struct {
			Inputs map[string]struct {
				Description string `yaml:"description"`
				Type        string `yaml:"type"`
				Default     string `yaml:"default"`
			} `yaml:"inputs"`
		} `yaml:"workflow_call"`
	} `yaml:"on"`
	Jobs map[string]callerJob `yaml:"jobs"`
}

// TestImageJob: ci-go-service.yml, ci-ui.yml and ci-action.yml call ci-image.yml as job `image`,
// with contents: read only, skipped on a closed PR, and never in `required`'s needs, so the
// check doesn't gate merges (Renovate, ccf-bump and the train gate on ci / required). The Go
// service and ui workflows take an `images` input with publish-image.yml's shape and default.
func TestImageJob(t *testing.T) {
	var publish callerWorkflow
	read(t, "publish-image.yml", &publish)
	want := publish.On.Call.Inputs["images"]
	for _, tc := range []struct{ file, images string }{
		{goService, "${{ inputs.images }}"},
		{ui, "${{ inputs.images }}"},
		{action, `${{ format('[{{"dockerfile":{0},"context":{1}}}]', toJSON(inputs.dockerfile), toJSON(inputs.context)) }}`},
	} {
		var wf callerWorkflow
		read(t, tc.file, &wf)
		job, ok := wf.Jobs["image"]
		if !ok {
			t.Errorf("%s: no image job", tc.file)
			continue
		}
		if job.Uses != "./.github/workflows/"+ciImage || job.With["images"] != tc.images || len(job.With) != 1 {
			t.Errorf("%s: image job uses %q with %v; want ./.github/workflows/%s with images %s", tc.file, job.Uses, job.With, ciImage, tc.images)
		}
		if len(job.Permissions) != 1 || job.Permissions["contents"] != "read" {
			t.Errorf("%s: image job permissions %v, want contents: read only", tc.file, job.Permissions)
		}
		if job.If != "${{ "+notClosed+" }}" || len(job.Needs) != 0 {
			t.Errorf("%s: image job if %q needs %v", tc.file, job.If, job.Needs)
		}
		if slices.Contains(wf.Jobs["required"].Needs, "image") {
			t.Errorf("%s: required needs image: the image check must not gate merges", tc.file)
		}
		if tc.file == action {
			continue
		}
		got := wf.On.Call.Inputs["images"]
		if got.Type != "string" || got.Default != want.Default || want.Default != "[{}]" {
			t.Errorf("%s: images input %+v, want a string defaulting to %q like publish-image.yml", tc.file, got, want.Default)
		}
	}
}

// TestCIImageWorkflow pins ci-image.yml: publish-image.yml's images input, read-only, amd64 only,
// built and loaded but never pushed (fork PRs), the GitHub Actions layer cache, the tools from this
// repo at job.workflow_sha, and the check of the built image.
func TestCIImageWorkflow(t *testing.T) {
	var wf callerWorkflow
	read(t, ciImage, &wf)
	var publish callerWorkflow
	read(t, "publish-image.yml", &publish)
	if wf.On.Call.Inputs["images"] != publish.On.Call.Inputs["images"] || len(wf.On.Call.Inputs) != 1 {
		t.Errorf("inputs %+v, want only publish-image.yml's images", wf.On.Call.Inputs)
	}
	var full struct {
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			If          string            `yaml:"if"`
			RunsOn      string            `yaml:"runs-on"`
			Permissions map[string]string `yaml:"permissions"`
			Env         map[string]string `yaml:"env"`
			Steps       []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				If   string            `yaml:"if"`
				With map[string]string `yaml:"with"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	read(t, ciImage, &full)
	if len(full.Permissions) != 0 || len(full.Jobs) != 1 {
		t.Fatalf("top-level permissions %v, %d jobs; want {} and one job", full.Permissions, len(full.Jobs))
	}
	job := full.Jobs["image"]
	if job.If != "${{ "+notClosed+" }}" || job.RunsOn != "ubuntu-latest" || len(job.Permissions) != 1 || job.Permissions["contents"] != "read" {
		t.Errorf("job: if %q, runs-on %q, permissions %v", job.If, job.RunsOn, job.Permissions)
	}
	if job.Env["DOCKERFILE"] != "caller/${{ matrix.image.dockerfile || 'Dockerfile' }}" || job.Env["CONTEXT"] != "caller/${{ matrix.image.context || '.' }}" {
		t.Errorf("env %v: want publish-image.yml's Dockerfile and context defaults under caller/", job.Env)
	}
	pin := regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)
	src, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", ciImage))
	if err != nil {
		t.Fatal(err)
	}
	var built, checked, tools, caller bool
	for _, s := range job.Steps {
		if s.Uses != "" {
			if !pin.MatchString(s.Uses) || !regexp.MustCompile(regexp.QuoteMeta(s.Uses)+` # v\d+\.\d+\.\d+\n`).Match(src) {
				t.Errorf("step %q: %s is not pinned by full SHA with a # vX.Y.Z comment", s.Name, s.Uses)
			}
		}
		switch {
		case strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["repository"] == "${{ job.workflow_repository }}":
			tools = s.With["ref"] == "${{ job.workflow_sha }}" && s.With["persist-credentials"] == "false"
		case strings.HasPrefix(s.Uses, "actions/checkout@"):
			caller = s.With["path"] == "caller" && s.With["persist-credentials"] == "false"
		case strings.HasPrefix(s.Uses, "docker/build-push-action@"):
			w := s.With
			built = w["push"] == "false" && w["load"] == "true" && w["platforms"] == "linux/amd64" &&
				w["build-args"] == "VERSION=${{ steps.version.outputs.version }}" && w["tags"] == "ci-image:local" &&
				strings.HasPrefix(w["cache-from"], "type=gha,") && strings.HasPrefix(w["cache-to"], "type=gha,mode=max,") &&
				w["context"] == "${{ env.CONTEXT }}" && w["file"] == "${{ env.DOCKERFILE }}"
		case s.Name == "Choose the version":
			if s.Run != "go run ./cmd/release build-version" {
				t.Errorf("version step runs %q", s.Run)
			}
		case s.Name == "Check the image":
			checked = s.Run == `go run ./cmd/image-scan check --image ci-image:local --name "$NAME" --summary "$GITHUB_STEP_SUMMARY"`
		}
		if s.Name != "Check the tools ref" && s.Name != "Find the Dockerfile" && !strings.HasPrefix(s.Uses, "actions/checkout@") && s.If != "steps.dockerfile.outputs.found == 'true'" {
			t.Errorf("step %q %s runs without a Dockerfile (if %q)", s.Name, s.Uses, s.If)
		}
	}
	if !built || !checked || !tools || !caller {
		t.Errorf("build (load, no push, amd64, gha cache) %v, check %v, tools at job.workflow_sha %v, caller under caller/ %v", built, checked, tools, caller)
	}
}

// TestCIImageNoDockerfile: without the Dockerfile the job notes it and skips the build and check.
func TestCIImageNoDockerfile(t *testing.T) {
	src := script(t, ciImage, "image", "Find the Dockerfile")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "caller"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "caller", "Dockerfile"), []byte("FROM alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ dockerfile, found, out string }{
		{"caller/Dockerfile", "true", ""},
		{"caller/Dockerfile-ci", "false", "::notice::Dockerfile-ci not found: no image to check for agent-ci"},
	} {
		r := run(t, dir, src, "DOCKERFILE="+tc.dockerfile, "NAME=agent-ci")
		if r.failed || r.outputs["found"] != tc.found || !strings.Contains(r.out, tc.out) {
			t.Errorf("%s: found=%q failed=%v; output:\n%s", tc.dockerfile, r.outputs["found"], r.failed, r.out)
		}
	}
}
