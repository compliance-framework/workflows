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
)

// kinds are the kind CI workflows; each ends in the same `required` job.
var kinds = []string{goPlugin, policies, goService, goLib}

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
		if got := slices.Sorted(slices.Values(req.Needs)); !slices.Equal(got, others) || req.If != "always()" {
			t.Errorf("%s: required needs %v if %q, want %v if always()", file, got, req.If, others)
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
	} {
		if script(t, c.file, c.job, c.step) != script(t, c.otherFile, c.otherJob, c.step) {
			t.Errorf("step %q differs between %s (%s) and %s (%s)", c.step, c.file, c.job, c.otherFile, c.otherJob)
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
