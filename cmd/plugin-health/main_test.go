package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/pluginhealth"
)

// source answers every plugin the same: release v1.0.0 on agent v0.8.0, annotated v2, unless
// the repo is in v1 (protocol 1, no Init) or broken (its release can't be read).
type source struct{ v1, broken string }

func (s source) LatestFinal(context.Context, string) (string, error) { return "v0.9.0", nil }

func (s source) LatestRelease(_ context.Context, repo string) (*pluginhealth.Release, error) {
	if repo == s.broken {
		return nil, errors.New("502 Bad Gateway")
	}
	return &pluginhealth.Release{Tag: "v1.0.0", Date: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, nil
}

func (s source) File(_ context.Context, repo, ref, path string) ([]byte, error) {
	if path == "go.mod" {
		return []byte("module x\nrequire github.com/compliance-framework/agent v0.8.0\n"), nil
	}
	return []byte("runner.RunnerGRPCPlugin{}"), nil
}

func (s source) LatestBumpPR(context.Context, string) (*pluginhealth.BumpPR, error) { return nil, nil }

func (s source) Annotations(_ context.Context, ref string) (map[string]string, bool, error) {
	if strings.Contains(ref, "/"+s.v1+":") {
		return map[string]string{"org.ccf.plugin.protocol.version": "1"}, true, nil
	}
	return map[string]string{"org.ccf.plugin.protocol.version": "2"}, true, nil
}

type tracker struct {
	issue *pluginhealth.Issue
	train int
}

func (t *tracker) FindIssue(context.Context, string, string) (*pluginhealth.Issue, error) {
	return t.issue, nil
}

func (t *tracker) CreateIssue(_ context.Context, title, body string) (*pluginhealth.Issue, error) {
	t.issue = &pluginhealth.Issue{Number: 1, URL: "https://github.com/compliance-framework/workflows/issues/1", Body: body}
	return t.issue, nil
}

func (t *tracker) EditIssue(_ context.Context, _ int, body string) error {
	t.issue.Body = body
	return nil
}

func (t *tracker) PinIssue(context.Context, string) error { return nil }
func (t *tracker) LastTrain(context.Context) (int, error) { return t.train, nil }

type slack struct{ posts []string }

func (s *slack) PostMessage(_ context.Context, channel, text string) error {
	s.posts = append(s.posts, channel+" "+text)
	return nil
}

func testDeps(env map[string]string, src source, tr *tracker, sl *slack) deps {
	return deps{
		getenv:  func(k string) string { return env[k] },
		now:     func() time.Time { return time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC) },
		source:  func(string) pluginhealth.Source { return src },
		tracker: func(string, string) pluginhealth.Tracker { return tr },
		slack: func() pluginhealth.Poster {
			if sl == nil {
				return nil
			}
			return sl
		},
	}
}

// batch1 are the kept plugins of batch 1 (plugin-template has release: false).
var batch1 = []string{"plugin-file-attestation", "plugin-github-settings", "plugin-github-repositories",
	"plugin-dependabot", "plugin-apt-versions", "plugin-local-ssh", "plugin-jira", "plugin-gitlab-repositories",
	"plugin-k8s", "plugin-cloud-custodian"}

func TestPlan(t *testing.T) {
	env := map[string]string{"GITHUB_REPOSITORY": "compliance-framework/workflows", "ISSUE_TOKEN": "t"}
	for _, tc := range []struct {
		name, event, workflow string
		tr                    *tracker
		due                   bool
	}{
		{"dispatch", "workflow_dispatch", "", &tracker{}, true},
		{"after ccf-bump-sync", "workflow_run", "ccf-bump-sync", &tracker{}, true},
		{"after a train run, train #4 finished", "workflow_run", "train", &tracker{train: 4}, true},
		{"after a train run, already reported", "workflow_run", "train",
			&tracker{train: 4, issue: &pluginhealth.Issue{Body: "<!-- plugin-health {\"candidates\":[],\"train\":4} -->"}}, false},
	} {
		var out, log bytes.Buffer
		args := []string{"plan", "--manifest", "../../repos.yaml", "--event", tc.event, "--workflow", tc.workflow}
		if err := run(context.Background(), args, testDeps(env, source{}, tc.tr, nil), &out, &log); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		want := "repos=agent," + strings.Join(batch1, ",") + "\ndue=" + map[bool]string{true: "true", false: "false"}[tc.due] + "\n"
		if out.String() != want {
			t.Errorf("%s: output\n%s\nwant\n%s", tc.name, out.String(), want)
		}
	}
}

// TestReportBatch1 is what a manual run on repos.yaml produces: a row per batch-1 plugin.
func TestReportBatch1(t *testing.T) {
	env := map[string]string{"GH_TOKEN": "t", "ISSUE_TOKEN": "t", "GITHUB_REPOSITORY": "compliance-framework/workflows",
		"GITHUB_RUN_ID": "77", "SLACK_CHANNEL": "C-RELEASES"}
	tr, sl := &tracker{train: 4}, &slack{}

	var out, log bytes.Buffer
	if err := run(context.Background(), []string{"report", "--manifest", "../../repos.yaml", "--dry-run"}, testDeps(env, source{v1: "plugin-jira"}, tr, sl), &out, &log); err != nil {
		t.Fatal(err)
	}
	if tr.issue != nil || len(sl.posts) > 0 {
		t.Fatal("a dry run wrote the issue or posted")
	}
	md := out.String()
	if n := strings.Count(md, "\n| [plugin-"); n != len(batch1) {
		t.Errorf("%d rows, want %d:\n%s", n, len(batch1), md)
	}
	for _, p := range batch1 {
		want := "| [" + p + "](https://github.com/compliance-framework/" + p + ") | v0.8.0 | 1 | v2 | none | v1.0.0 (2026-09-01) | healthy |"
		if p == "plugin-jira" {
			want = "| [plugin-jira](https://github.com/compliance-framework/plugin-jira) | v0.8.0 | 1 | v1 | none | v1.0.0 (2026-09-01) | deprecation candidate |"
		}
		if !strings.Contains(md, want) {
			t.Errorf("no row %q", want)
		}
	}
	if !strings.Contains(md, "https://github.com/compliance-framework/workflows/actions/runs/77") {
		t.Error("no link to the run")
	}

	// A live run opens the issue and posts the new candidate; the next one, unchanged, doesn't.
	for range 2 {
		if err := run(context.Background(), []string{"report", "--manifest", "../../repos.yaml"}, testDeps(env, source{v1: "plugin-jira"}, tr, sl), &out, &log); err != nil {
			t.Fatal(err)
		}
	}
	if tr.issue == nil || !strings.Contains(tr.issue.Body, `{"candidates":["plugin-jira"],"train":4}`) {
		t.Fatalf("issue %+v", tr.issue)
	}
	if len(sl.posts) != 1 || !strings.HasPrefix(sl.posts[0], "C-RELEASES :warning: Plugin deprecation candidates changed. Now: plugin-jira.") {
		t.Errorf("posts %v", sl.posts)
	}

	// A failed read still writes the issue, then fails the run.
	err := run(context.Background(), []string{"report", "--manifest", "../../repos.yaml"}, testDeps(env, source{broken: "plugin-k8s"}, tr, sl), &out, &log)
	if err == nil || !strings.Contains(err.Error(), "plugin-k8s: latest release: 502 Bad Gateway") {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(tr.issue.Body, `{"candidates":[],"train":4}`) || len(sl.posts) != 2 {
		t.Errorf("issue not rewritten after the failed read, or no post: %v", sl.posts)
	}
}

func TestReportNeedsTokens(t *testing.T) {
	var out, log bytes.Buffer
	err := run(context.Background(), []string{"report", "--manifest", "../../repos.yaml"}, testDeps(map[string]string{}, source{}, &tracker{}, nil), &out, &log)
	if err == nil || !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Errorf("err = %v", err)
	}
	err = run(context.Background(), []string{"report", "--manifest", "../../repos.yaml"}, testDeps(map[string]string{"GH_TOKEN": "t"}, source{}, &tracker{}, nil), &out, &log)
	if err == nil || !strings.Contains(err.Error(), "ISSUE_TOKEN") {
		t.Errorf("err = %v", err)
	}
	if err := run(context.Background(), []string{"nope"}, testDeps(nil, source{}, nil, nil), &out, &log); err == nil {
		t.Error("unknown subcommand: want an error")
	}
}
