package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/train"
)

// tracker serves the open-train issue listing with the given issues JSON.
func tracker(t *testing.T, issues string) func(string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/compliance-framework/workflows/issues" || r.URL.Query().Get("labels") != train.LabelOpen {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, issues)
	}))
	t.Cleanup(srv.Close)
	return func(k string) string {
		return map[string]string{"GITHUB_API_URL": srv.URL, "TRACKER_TOKEN": "t"}[k]
	}
}

func openTrain(t *testing.T, repos ...string) string {
	st := &train.State{Month: "2026-12", Manifest: "repos.mock.yaml", Status: train.StatusOpen}
	for _, r := range repos {
		st.Repos = append(st.Repos, &train.RepoState{Name: r, Stage: 1, Phase: train.Waiting})
	}
	body, err := train.Render(st, "")
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`[{"number":7,"state":"open","body":%q}]`, body)
}

func TestSelect(t *testing.T) {
	t.Chdir("../..")
	tests := []struct {
		name, issues string
		args         []string
		want, err    string
	}{
		{"start, no open train", `[]`, []string{"--for", "start", "--manifest", "repos.mock.yaml", "--repos", "mock-ui,mock-api"},
			"open=false\nmanifest=repos.mock.yaml\nrepos=mock-api,mock-ui\n", ""},
		{"start, every release repo", `[]`, []string{"--for", "start"}, "open=false\nmanifest=repos.yaml\nrepos=api,gooci,agent,ui,agent-action,helm-charts\n", ""},
		{"reconcile, no open train", `[]`, []string{"--for", "reconcile"}, "open=false\nmanifest=\nrepos=\n", ""},
		{"the open train's repos win", openTrain(t, "mock-gooci", "mock-api"), []string{"--for", "start", "--repos", "mock-ui"},
			"open=true\nmanifest=repos.mock.yaml\nrepos=mock-api,mock-gooci\n", ""},
		{"a state naming a repo outside its manifest", openTrain(t, "mock-api", "api"), []string{"--for", "reconcile"}, "", `repo "api" is not in repos.mock.yaml`},
		{"unknown repo", `[]`, []string{"--for", "start", "--repos", "nope"}, "", `repo "nope" is not in repos.yaml`},
		{"manifest outside the root", `[]`, []string{"--for", "start", "--manifest", "../repos.yaml"}, "", "want a .yaml file in the repo root"},
		{"no mode", `[]`, nil, "", "--for start or --for reconcile"},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		err := run(context.Background(), append([]string{"select"}, tt.args...), tracker(t, tt.issues), &out, time.Now)
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("%s: err = %v, want %q", tt.name, err, tt.err)
			}
			continue
		}
		if err != nil || out.String() != tt.want {
			t.Errorf("%s: %v\n got %q\nwant %q", tt.name, err, out.String(), tt.want)
		}
	}
}

func TestExecBumper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	script := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var log bytes.Buffer
	b := execBumper{path: script("ok", `echo "args: $*"; echo "  PR: https://github.com/o/r/pull/12"; echo "  auto-merge: on"`), log: &log}
	res, err := b.Bump(context.Background(), "repos.mock.yaml", "mock-agent", map[string]string{"mock-gooci": "v0.1.1", "mock-api": "v0.2.0"}, false)
	if err != nil || res.PR != 12 || !res.Changed {
		t.Fatalf("Bump = %+v, %v", res, err)
	}
	if want := "args: --manifest repos.mock.yaml --repo mock-agent --mode train --pr --set mock-api=v0.2.0 --set mock-gooci=v0.1.1\n"; !strings.HasPrefix(res.Output, want) || !strings.Contains(log.String(), want) {
		t.Errorf("output %q, log %q", res.Output, log.String())
	}

	b.path = script("dry", `echo "$*" | grep -q -- '--dry-run$' && echo "  dry run: would push ccf-bump/train-2026-12-01"`)
	if res, err := b.Bump(context.Background(), "repos.mock.yaml", "mock-agent", map[string]string{"mock-api": "v0.2.0"}, true); err != nil || res.PR != 0 || !res.Changed {
		t.Errorf("dry run = %+v, %v", res, err)
	}
	b.path = script("none", `echo "  nothing pinned"`)
	if res, err := b.Bump(context.Background(), "repos.mock.yaml", "mock-api", map[string]string{"x": "v1.0.0"}, false); err != nil || res.PR != 0 || res.Changed {
		t.Errorf("no change = %+v, %v", res, err)
	}
	b.path = script("fail", `echo "::error::ccf-bump: mock-agent: git push: denied"; echo "more" >&2; exit 1`)
	if _, err := b.Bump(context.Background(), "repos.mock.yaml", "mock-agent", nil, false); err == nil || !strings.Contains(err.Error(), "ccf-bump: mock-agent: git push: denied") {
		t.Errorf("failure: %v", err)
	}
}

func TestRunRejectsUnknownCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"go"}, {"start", "extra"}} {
		if err := run(context.Background(), args, func(string) string { return "" }, io.Discard, time.Now); err == nil {
			t.Errorf("run(%q): want an error", args)
		}
	}
}
