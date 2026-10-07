package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
	"github.com/compliance-framework/workflows/internal/reposettings"
)

func TestSelectRepos(t *testing.T) {
	m := &manifest.Manifest{Repos: []manifest.Repo{{Name: "a", Release: true}, {Name: "b", Release: false}, {Name: "c", Release: true}}}
	for _, tc := range []struct {
		list string
		want []string
		err  string
	}{
		{list: "", want: []string{"a", "c"}},
		{list: " c, a\n", want: []string{"a", "c"}},
		{list: "c c", want: []string{"c"}},
		{list: "a,b", err: "b"},
		{list: "x", err: "x"},
	} {
		got, err := selectRepos(m, tc.list)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("selectRepos(%q) err = %v, want one naming %q", tc.list, err, tc.err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("selectRepos(%q) = %v, %v; want %v", tc.list, got, err, tc.want)
		}
	}
}

func noClient(func(string) string) reposettings.Client { panic("no client expected") }

func TestRunList(t *testing.T) {
	var out bytes.Buffer
	args := []string{"list", "--manifest", "../../repos.mock.yaml", "--repos", "mock-ui,mock-api"}
	if err := run(context.Background(), args, func(string) string { return "" }, &out, noClient); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "mock-api,mock-ui\n" {
		t.Errorf("list = %q", got)
	}
}

func TestRunSyncErrors(t *testing.T) {
	env := func(token string) func(string) string {
		return func(k string) string {
			if k == "GH_TOKEN" {
				return token
			}
			return ""
		}
	}
	for _, tc := range []struct {
		args  []string
		token string
		err   string
	}{
		{args: []string{"sync", "--manifest", "../../repos.yaml"}, token: "t", err: "--bypass-app-id"},
		{args: []string{"sync", "--manifest", "../../repos.yaml", "--bypass-app-id", "42"}, err: "GH_TOKEN"},
		{args: []string{"sync", "--manifest", "../../repos.yaml", "--bypass-app-id", "42", "--required-check", " "}, token: "t", err: "required check"},
		{args: []string{"apply"}, err: "usage"},
	} {
		err := run(context.Background(), tc.args, env(tc.token), &bytes.Buffer{}, noClient)
		if err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("run(%v) err = %v, want one containing %q", tc.args, err, tc.err)
		}
	}
}

// TestRunSyncFailedStepFailsRun: a refused write fails the run (main exits 1) after the other
// steps ran.
func TestRunSyncFailedStepFailsRun(t *testing.T) {
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const repo = "/repos/compliance-framework/mock-api"
		if r.Method != http.MethodGet {
			writes = append(writes, r.Method+" "+r.URL.Path)
			if r.Method == http.MethodPatch {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			w.WriteHeader(http.StatusCreated)
			return
		}
		switch r.URL.Path {
		case repo:
			_, _ = io.WriteString(w, `{"allow_merge_commit":true}`)
		case repo + "/automated-security-fixes":
			_, _ = io.WriteString(w, `{"enabled":false,"paused":false}`)
		case repo + "/rulesets":
			_, _ = io.WriteString(w, `[]`)
		default: // vulnerability-alerts on; no code security configuration
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	client := func(func(string) string) reposettings.Client {
		return &reposettings.GitHub{BaseURL: srv.URL, Token: "t"}
	}
	args := []string{"sync", "--manifest", "../../repos.mock.yaml", "--repos", "mock-api", "--bypass-app-id", "42", "--check-token-scope=false", "--apply"}
	var out bytes.Buffer
	err := run(context.Background(), args, func(string) string { return "t" }, &out, client)
	if err == nil || !strings.Contains(err.Error(), "repo merge settings") {
		t.Fatalf("err = %v, want the failed merge settings step", err)
	}
	if want := []string{"PATCH /repos/compliance-framework/mock-api", "POST /repos/compliance-framework/mock-api/rulesets", "POST /repos/compliance-framework/mock-api/rulesets"}; !slices.Equal(writes, want) {
		t.Errorf("writes = %v, want %v", writes, want)
	}
	if !strings.Contains(out.String(), "partly applied: 1 of 3 step(s) failed") {
		t.Errorf("output:\n%s", out.String())
	}
}
