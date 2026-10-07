package main

import (
	"bytes"
	"context"
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
