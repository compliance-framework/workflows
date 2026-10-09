// Package ciworkflows_test runs the shell steps of the reusable CI workflows locally, the
// way the runner does (bash -e, GITHUB_OUTPUT, RUNNER_TEMP), against fixtures.
package ciworkflows_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	goPlugin  = "ci-go-plugin.yml"
	policies  = "ci-policies.yml"
	goService = "ci-go-service.yml"
	goLib     = "ci-go-lib.yml"
	ui        = "ci-ui.yml"
	helm      = "ci-helm.yml"
	action    = "ci-action.yml"
)

// kinds are the kind CI workflows; each ends in the same `required` job.
var kinds = []string{goPlugin, policies, goService, goLib, ui, helm, action}

type workflow struct {
	Jobs map[string]struct {
		Needs []string `yaml:"needs"`
		If    string   `yaml:"if"`
		Steps []struct {
			Name string `yaml:"name"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func read(t *testing.T, file string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", file))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// script returns the run script of the named step of a job.
func script(t *testing.T, file, job, step string) string {
	t.Helper()
	var wf workflow
	read(t, file, &wf)
	for _, s := range wf.Jobs[job].Steps {
		if s.Name == step && s.Run != "" {
			return s.Run
		}
	}
	t.Fatalf("%s: no step %q with a run script in job %q", file, step, job)
	return ""
}

type result struct {
	out     string
	outputs map[string]string
	failed  bool
	temp    string
}

// run executes a step script in dir with the given step env.
func run(t *testing.T, dir, src string, env ...string) result {
	t.Helper()
	temp := t.TempDir()
	ghOut := filepath.Join(temp, "github-output")
	if err := os.WriteFile(ghOut, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-e", "-c", src)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append(env, "RUNNER_TEMP="+temp, "GITHUB_OUTPUT="+ghOut)...)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatal(err)
	}
	b, rerr := os.ReadFile(ghOut)
	if rerr != nil {
		t.Fatal(rerr)
	}
	outputs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			outputs[k] = v
		}
	}
	return result{out: string(out), outputs: outputs, failed: err != nil, temp: temp}
}

func need(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRequired(t *testing.T) {
	need(t, "jq")
	src := script(t, goPlugin, "required", "Check the results")
	for _, file := range kinds[1:] {
		if other := script(t, file, "required", "Check the results"); other != src {
			t.Fatalf("the required check differs between %s and %s", goPlugin, file)
		}
	}
	for _, tc := range []struct {
		name, needs, want string
		failed            bool
	}{
		{"all succeeded", `{"common":{"result":"success","outputs":{}},"go":{"result":"success","outputs":{}}}`, "All required jobs succeeded.", false},
		{"failure", `{"common":{"result":"success"},"go":{"result":"failure"}}`, "go: failure", true},
		{"cancelled", `{"common":{"result":"cancelled"},"go":{"result":"success"}}`, "common: cancelled", true},
		{"skipped", `{"common":{"result":"success"},"go":{"result":"skipped"}}`, "go: skipped", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, t.TempDir(), src, "NEEDS="+tc.needs)
			if r.failed != tc.failed || !strings.Contains(r.out, tc.want) {
				t.Fatalf("failed=%v, want %v; output:\n%s", r.failed, tc.failed, r.out)
			}
		})
	}
}

func TestRequiredNeedsEveryJob(t *testing.T) {
	for _, file := range kinds {
		var wf workflow
		read(t, file, &wf)
		var others []string
		for name := range wf.Jobs {
			if name != "required" {
				others = append(others, name)
			}
		}
		req := wf.Jobs["required"]
		slices.Sort(others)
		if got := slices.Sorted(slices.Values(req.Needs)); !slices.Equal(got, others) || req.If != requiredIf {
			t.Errorf("%s: required needs %v if %q, want %v if %q", file, got, req.If, others, requiredIf)
		}
	}
}

// notClosed is the guard every CI job carries: nothing runs for a closed PR, which a labeled
// event (release-please relabelling its merged release PR) would otherwise re-run CI on.
const (
	notClosed = "!(github.event_name == 'pull_request' && github.event.pull_request.state == 'closed')"
	// requiredIf runs `required` whatever the jobs it needs did, except on a closed PR, where
	// it is skipped with them instead of failing on their skips.
	requiredIf = "${{ always() && " + notClosed + " }}"
	// prOnly is the guard of the jobs that only run for a pull request.
	prOnly = "github.event_name == 'pull_request' && github.event.pull_request.state != 'closed'"
)

// TestClosedPRGuard: every job of the CI workflows callers run on pull_request events skips on
// a closed PR, and `required` is skipped (not failed) with them.
func TestClosedPRGuard(t *testing.T) {
	for _, file := range append(slices.Clone(kinds), "ci-common.yml", "release-checks.yml") {
		var wf workflow
		read(t, file, &wf)
		if len(wf.Jobs) == 0 {
			t.Fatalf("%s: no jobs", file)
		}
		for name, job := range wf.Jobs {
			ok := job.If == "${{ "+notClosed+" }}" || job.If == prOnly || strings.HasPrefix(job.If, prOnly+" && ")
			if name == "required" {
				ok = job.If == requiredIf
			}
			if !ok {
				t.Errorf("%s: job %s has if %q, want it to skip on a closed PR", file, name, job.If)
			}
		}
	}
}

// TestSharedCopies pins the jobs and steps the kind workflows repeat to one copy.
func TestSharedCopies(t *testing.T) {
	jobs := func(file string) map[string]any {
		var wf struct {
			Jobs map[string]any `yaml:"jobs"`
		}
		read(t, file, &wf)
		return wf.Jobs
	}
	for _, file := range []string{goService, goLib} {
		if !reflect.DeepEqual(jobs(file)["lint"], jobs(goPlugin)["lint"]) {
			t.Errorf("the lint job differs between %s and %s", goPlugin, file)
		}
	}
	// ci-go-lib.yml's goreleaser job is ci-go-plugin.yml's without the build step.
	steps := func(file string) []any { return jobs(file)["goreleaser"].(map[string]any)["steps"].([]any) }
	if plugin := steps(goPlugin); !reflect.DeepEqual(steps(goLib), plugin[:len(plugin)-1]) {
		t.Errorf("the goreleaser setup and check differ between %s and %s", goPlugin, goLib)
	}
	for _, c := range []struct{ file, job, step, otherFile, otherJob string }{
		{goPlugin, "go", "gofmt", goService, "go"},
		{goPlugin, "go", "gofmt", goLib, "go"},
		{goPlugin, "go", "go mod tidy", goService, "go"},
		{goService, "go", "Prepare", goService, "make"},
		{goService, "make", "make", helm, "helm"},
	} {
		if script(t, c.file, c.job, c.step) != script(t, c.otherFile, c.otherJob, c.step) {
			t.Errorf("step %q differs between %s (%s) and %s (%s)", c.step, c.file, c.job, c.otherFile, c.otherJob)
		}
	}
}

// TestLintTimeout: each Go kind workflow takes a lint-timeout-minutes input (number, default
// 15, the job's former fixed limit) and its golangci-lint job runs for that long. The lint job
// sets no --timeout of its own (golangci-lint v2 has none by default), so the job's limit is
// the only one.
func TestLintTimeout(t *testing.T) {
	const timeout = "${{ inputs.lint-timeout-minutes }}"
	for _, file := range []string{goPlugin, goService, goLib} {
		var wf struct {
			On struct {
				Call struct {
					Inputs map[string]struct {
						Type    string `yaml:"type"`
						Default any    `yaml:"default"`
					} `yaml:"inputs"`
				} `yaml:"workflow_call"`
			} `yaml:"on"`
			Jobs map[string]struct {
				TimeoutMinutes string `yaml:"timeout-minutes"`
				Steps          []struct {
					With map[string]string `yaml:"with"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		read(t, file, &wf)
		in, ok := wf.On.Call.Inputs["lint-timeout-minutes"]
		if !ok || in.Type != "number" || in.Default != 15 {
			t.Errorf("%s: lint-timeout-minutes input = %+v (present %v), want type number default 15", file, in, ok)
		}
		lint := wf.Jobs["lint"]
		if lint.TimeoutMinutes != timeout {
			t.Errorf("%s: lint job timeout-minutes = %q, want %q", file, lint.TimeoutMinutes, timeout)
		}
		args := script(t, file, "lint", "Choose the lint arguments")
		for _, s := range lint.Steps {
			args += "\n" + s.With["args"]
		}
		if strings.Contains(args, "--timeout") {
			t.Errorf("%s: the golangci-lint arguments set a --timeout; it must then follow lint-timeout-minutes too", file)
		}
	}
}

func TestMakeTargets(t *testing.T) {
	src := script(t, goService, "make", "make")
	bin := t.TempDir()
	fake := "#!/bin/sh\necho \"ran $1\"\n[ \"$1\" != bad ]\n"
	if err := os.WriteFile(filepath.Join(bin, "make"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
	for _, tc := range []struct {
		targets string
		failed  bool
		want    []string
	}{
		{"", false, []string{"No make targets."}},
		{"one two", false, []string{"ran one", "ran two"}},
		{"bad two", true, []string{"ran bad", "ran two", "make targets failed: bad"}},
	} {
		t.Run(tc.targets, func(t *testing.T) {
			r := run(t, t.TempDir(), src, path, "TARGETS="+tc.targets)
			for _, w := range tc.want {
				if !strings.Contains(r.out, w) {
					t.Fatalf("output lacks %q:\n%s", w, r.out)
				}
			}
			if r.failed != tc.failed {
				t.Fatalf("failed=%v, want %v:\n%s", r.failed, tc.failed, r.out)
			}
		})
	}
}

func TestNodeVersion(t *testing.T) {
	src := script(t, ui, "node", "Choose the Node version")
	dir := t.TempDir()
	if r := run(t, dir, src, "NODE_VERSION=20"); r.failed || r.outputs["version"] != "20" || r.outputs["file"] != "" {
		t.Fatalf("without .nvmrc: got %v, failed=%v:\n%s", r.outputs, r.failed, r.out)
	}
	if err := os.WriteFile(filepath.Join(dir, ".nvmrc"), []byte("22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := run(t, dir, src, "NODE_VERSION=20"); r.failed || r.outputs["file"] != ".nvmrc" || r.outputs["version"] != "" {
		t.Fatalf("with .nvmrc: got %v, failed=%v:\n%s", r.outputs, r.failed, r.out)
	}
}

func TestHadolintFiles(t *testing.T) {
	need(t, "git")
	src := script(t, action, "hadolint", "hadolint")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "hadolint"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	if r := run(t, dir, src, path); !r.failed || !strings.Contains(r.out, "no Dockerfile") {
		t.Fatalf("want a failure without Dockerfiles:\n%s", r.out)
	}
	for _, f := range []string{"Dockerfile", "Dockerfile-ci", "build/Dockerfile.dev", "Dockerfile.d/notes", ".dockerignore"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", ".")
	r := run(t, dir, src, path)
	if got := strings.Fields(r.out); r.failed || !slices.Equal(got, []string{"Dockerfile", "Dockerfile-ci", "build/Dockerfile.dev"}) {
		t.Fatalf("linted %v (failed=%v)", got, r.failed)
	}
}

func TestKubeconform(t *testing.T) {
	src := script(t, helm, "helm", "kubeconform")
	bin := t.TempDir()
	fakes := map[string]string{
		// go install drops a fake kubeconform that rejects manifests containing "invalid".
		"go":   "#!/bin/sh\nprintf '#!/bin/sh\\n! grep -q invalid\\n' > \"$GOBIN/kubeconform\"\nchmod +x \"$GOBIN/kubeconform\"\n",
		"helm": "#!/bin/sh\n[ \"$3\" != charts/broken ] || exit 1\ncat \"$3/manifest\"\n",
	}
	for name, body := range fakes {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	for chart, manifest := range map[string]string{"good": "kind: Pod", "bad": "invalid", "broken": ""} {
		if err := os.MkdirAll(filepath.Join(dir, "charts", chart), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"Chart.yaml": "", "manifest": manifest} {
			if err := os.WriteFile(filepath.Join(dir, "charts", chart, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
	r := run(t, dir, src, path, "CHARTS_DIR=charts")
	// broken fails only through pipefail: kubeconform accepts its empty input.
	if !r.failed || !strings.Contains(r.out, "not valid Kubernetes objects: charts/bad charts/broken\n") {
		t.Fatalf("want bad and broken reported (failed=%v):\n%s", r.failed, r.out)
	}
	if err := os.RemoveAll(filepath.Join(dir, "charts", "bad")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "charts", "broken")); err != nil {
		t.Fatal(err)
	}
	if r := run(t, dir, src, path, "CHARTS_DIR=charts"); r.failed {
		t.Fatalf("valid chart failed:\n%s", r.out)
	}
}

// TestCtLint pins that the repo's ct.yaml reaches ct only through --config:
// chart-testing-action points ct's own config search at the action's install dir.
func TestCtLint(t *testing.T) {
	src := script(t, helm, "ct", "ct lint")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ct"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
	lint := []string{"lint", "--all", "--chart-dirs", "charts", "--check-version-increment=false"}
	dir := t.TempDir()
	if r := run(t, dir, src, path, "CHARTS_DIR=charts"); r.failed || !slices.Equal(strings.Fields(r.out), lint) {
		t.Fatalf("without ct.yaml: ran ct %v (failed=%v)", strings.Fields(r.out), r.failed)
	}
	if err := os.WriteFile(filepath.Join(dir, "ct.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	want := append([]string{"lint", "--config", "ct.yaml"}, lint[1:]...)
	if r := run(t, dir, src, path, "CHARTS_DIR=charts"); r.failed || !slices.Equal(strings.Fields(r.out), want) {
		t.Fatalf("with ct.yaml: ran ct %v (failed=%v), want %v", strings.Fields(r.out), r.failed, want)
	}
}

func TestFindConfig(t *testing.T) {
	for _, tc := range []struct{ file, job, step, repoConfig string }{
		{goPlugin, "lint", "Find the repo's golangci-lint config", ".golangci.yaml"},
		{policies, "regal", "Find the repo's Regal config", ".regal/config.yaml"},
	} {
		src := script(t, tc.file, tc.job, tc.step)
		t.Run(tc.file+"/repo config", func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.repoConfig)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if r := run(t, dir, src, "WORKFLOW_SHA="); r.failed || r.outputs["shared"] != "false" {
				t.Fatalf("got %v, failed=%v:\n%s", r.outputs, r.failed, r.out)
			}
		})
		t.Run(tc.file+"/shared config", func(t *testing.T) {
			if r := run(t, t.TempDir(), src, "WORKFLOW_SHA=abc123"); r.failed || r.outputs["shared"] != "true" {
				t.Fatalf("got %v, failed=%v:\n%s", r.outputs, r.failed, r.out)
			}
		})
		t.Run(tc.file+"/no workflow sha", func(t *testing.T) {
			if r := run(t, t.TempDir(), src, "WORKFLOW_SHA="); !r.failed || r.outputs["shared"] != "" {
				t.Fatalf("want a failure without output, got %v, failed=%v:\n%s", r.outputs, r.failed, r.out)
			}
		})
	}
}

func TestLintArgs(t *testing.T) {
	need(t, "git")
	src := script(t, goPlugin, "lint", "Choose the lint arguments")
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "one")
	before := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "two")
	zeros := strings.Repeat("0", 40)

	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"pull request", []string{"NEW_ONLY=true", "BASE_REF=main", "BEFORE="}, "--new-from-merge-base=origin/main"},
		{"push", []string{"NEW_ONLY=true", "BASE_REF=", "BEFORE=" + before}, "--new-from-rev=" + before},
		{"new branch push", []string{"NEW_ONLY=true", "BASE_REF=", "BEFORE=" + zeros}, ""},
		{"no event range", []string{"NEW_ONLY=true", "BASE_REF=", "BEFORE="}, ""},
		{"lint everything", []string{"NEW_ONLY=false", "BASE_REF=main", "BEFORE=" + before}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, dir, src, append(tc.env, "SHARED=false")...)
			if got := strings.Join(strings.Fields(r.outputs["args"]), " "); r.failed || got != tc.want {
				t.Fatalf("args %q, want %q (failed=%v):\n%s", got, tc.want, r.failed, r.out)
			}
		})
	}

	t.Run("shared config", func(t *testing.T) {
		clone := filepath.Join(dir, ".ccf-workflows")
		if err := os.MkdirAll(clone, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(clone, ".golangci.yml"), []byte("version: \"2\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := run(t, dir, src, "SHARED=true", "NEW_ONLY=true", "BASE_REF=main", "BEFORE=")
		cfg := filepath.Join(r.temp, "ccf-golangci.yml")
		want := "--config=" + cfg + " --new-from-merge-base=origin/main"
		if got := strings.Join(strings.Fields(r.outputs["args"]), " "); r.failed || got != want {
			t.Fatalf("args %q, want %q (failed=%v):\n%s", got, want, r.failed, r.out)
		}
		if _, err := os.Stat(cfg); err != nil {
			t.Fatalf("shared config not moved: %v", err)
		}
		if _, err := os.Stat(clone); !os.IsNotExist(err) {
			t.Fatalf("clone not removed: %v", err)
		}
	})
}

func TestGoreleaserCheck(t *testing.T) {
	src := script(t, goPlugin, "goreleaser", "goreleaser check")
	bin := t.TempDir()
	fake := "#!/bin/sh\n[ \"$1\" = check ] || exit 99\nexit \"$FAKE_RC\"\n"
	if err := os.WriteFile(filepath.Join(bin, "goreleaser"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
	for _, tc := range []struct {
		rc      string
		failed  bool
		warning bool
	}{
		{"0", false, false},
		{"2", false, true}, // valid, but uses deprecated properties
		{"1", true, false},
	} {
		t.Run("exit "+tc.rc, func(t *testing.T) {
			r := run(t, t.TempDir(), src, path, "FAKE_RC="+tc.rc)
			warned := strings.Contains(r.out, "::warning::")
			if r.failed != tc.failed || warned != tc.warning {
				t.Fatalf("failed=%v warning=%v, want %v/%v:\n%s", r.failed, warned, tc.failed, tc.warning, r.out)
			}
		})
	}
}

// TestSelfRequired: this repo's own ci.yml reports `ci / required` (what ccf-required requires)
// after every other job: anything but success fails, except release-checks skipped (every PR but
// a release-please one, and every push).
func TestSelfRequired(t *testing.T) {
	var wf struct {
		Jobs map[string]struct {
			Name  string   `yaml:"name"`
			Needs []string `yaml:"needs"`
			If    string   `yaml:"if"`
		} `yaml:"jobs"`
	}
	read(t, "ci.yml", &wf)
	req := wf.Jobs["required"]
	var others []string
	for name := range wf.Jobs {
		if name != "required" {
			others = append(others, name)
		}
	}
	slices.Sort(others)
	if got := slices.Sorted(slices.Values(req.Needs)); req.Name != "ci / required" || !slices.Equal(got, others) || req.If != "${{ always() }}" {
		t.Errorf("ci.yml required = %+v, want name ci / required, needs %v, if always()", req, others)
	}
	need(t, "jq")
	src := script(t, "ci.yml", "required", "Check the results")
	for _, tc := range []struct {
		name, needs, want string
		failed            bool
	}{
		{"release PR", `{"go":{"result":"success"},"actionlint":{"result":"success"},"release-checks":{"result":"success"}}`, "All required jobs succeeded.", false},
		{"other PR", `{"go":{"result":"success"},"actionlint":{"result":"success"},"release-checks":{"result":"skipped"}}`, "All required jobs succeeded.", false},
		{"release-checks failed", `{"go":{"result":"success"},"actionlint":{"result":"success"},"release-checks":{"result":"failure"}}`, "release-checks: failure", true},
		{"go skipped", `{"go":{"result":"skipped"},"actionlint":{"result":"success"},"release-checks":{"result":"skipped"}}`, "go: skipped", true},
		{"cancelled", `{"go":{"result":"success"},"actionlint":{"result":"cancelled"},"release-checks":{"result":"skipped"}}`, "actionlint: cancelled", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, t.TempDir(), src, "NEEDS="+tc.needs)
			if r.failed != tc.failed || !strings.Contains(r.out, tc.want) {
				t.Fatalf("failed=%v, want %v; output:\n%s", r.failed, tc.failed, r.out)
			}
		})
	}
}
