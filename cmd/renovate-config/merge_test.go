package main

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/bump"
	"github.com/compliance-framework/workflows/internal/renovate"
)

const bot = "ccf-release-bot[bot]"

// fakeGH serves open PRs per repo, ci / required runs per head SHA (the last entry repeats) and
// commit authors per PR, and records merges.
type fakeGH struct {
	owner   string
	open    map[string][]bump.PR
	checks  map[string][][]bump.CheckRun
	authors map[int][]string
	merges  []string
}

func (f *fakeGH) OpenPRs(_ context.Context, repo string) ([]bump.PR, error) {
	return f.open[repo], nil
}

func (f *fakeGH) PullRequest(_ context.Context, repo string, number int) (*bump.PR, error) {
	for _, p := range f.open[repo] {
		if p.Number == number {
			mergeable := true
			p.State, p.Mergeable = "open", &mergeable
			return &p, nil
		}
	}
	return nil, fmt.Errorf("%s#%d: 404", repo, number)
}

func (f *fakeGH) CheckRuns(_ context.Context, _, sha, _ string) ([]bump.CheckRun, error) {
	runs := f.checks[sha]
	if len(runs) == 0 {
		return nil, nil
	}
	if len(runs) > 1 {
		f.checks[sha] = runs[1:]
	}
	return runs[0], nil
}

func (f *fakeGH) PRCommitAuthors(_ context.Context, _ string, number int) ([]string, error) {
	return f.authors[number], nil
}

func (f *fakeGH) Merge(_ context.Context, repo string, number int, sha, title string) error {
	f.merges = append(f.merges, fmt.Sprintf("%s#%d %s %q", repo, number, sha, title))
	return nil
}

// renovatePR is a PR from branch of mock-plugin-1 opened by login, with head "sha<n>", labelled
// renovate:automerge plus labels.
func renovatePR(n int, branch, login string, labels ...string) bump.PR {
	var p bump.PR
	p.Number, p.Title, p.State = n, fmt.Sprintf("fix(deps): update security fixes %d", n), "open"
	p.User.Login = login
	p.Head.Ref, p.Head.SHA = branch, fmt.Sprintf("sha%d", n)
	p.Head.Repo = &struct {
		FullName string `json:"full_name"`
	}{"compliance-framework/mock-plugin-1"}
	for _, l := range append([]string{renovate.AutomergeLabel}, labels...) {
		p.Labels = append(p.Labels, bump.PRLabel{Name: l})
	}
	return p
}

func green() [][]bump.CheckRun {
	return [][]bump.CheckRun{{{ID: 1, Name: "ci / required", Status: "completed", Conclusion: "success"}}}
}

func runMerge(t *testing.T, gh *fakeGH, args ...string) (string, int, error) {
	t.Helper()
	var out bytes.Buffer
	now, slept := time.Date(2026, 10, 14, 5, 17, 0, 0, time.UTC), 0
	e := mergeEnv{
		github: func(owner string) mergeGitHub { gh.owner = owner; return gh },
		stdout: &out, now: func() time.Time { return now },
		sleep: func(d time.Duration) { slept++; now = now.Add(d) },
	}
	err := merge(context.Background(), append([]string{"--manifest", "../../internal/manifest/testdata/mock.yaml"}, args...), e)
	return out.String(), slept, err
}

func TestMergeCandidates(t *testing.T) {
	fork := renovatePR(5, "renovate/security-gomod", bot)
	fork.Head.Repo.FullName = "someone/mock-plugin-1"
	unlabelled := renovatePR(9, "renovate/all-non-major", bot)
	unlabelled.Labels = nil
	gh := &fakeGH{
		open: map[string][]bump.PR{"mock-plugin-1": {
			renovatePR(1, "renovate/security-gomod", bot),
			renovatePR(2, "renovate/all-non-major", bot),
			renovatePR(3, "renovate/all-non-major", "someone"), // a person's
			renovatePR(4, "ccf-bump/sync-2026-10-08", bot),     // ccf-bump's
			fork, // a fork
			renovatePR(6, "renovate/major-security-gomod", bot, "needs-human"), // held for a person
			renovatePR(7, "renovate/security-gomod-x", bot),                    // a person pushed to it
			renovatePR(8, "renovate/security-gomod-y", bot),                    // an unlinked author pushed to it
			unlabelled,
			renovatePR(10, "renovate/security-gomod-z", bot), // no commits listed
			renovatePR(11, "image-scan/os-patches", bot),     // image-scan's OS package patches
			renovatePR(12, "image-scan/os-patches", bot),     // a person pushed to it
			renovatePR(13, "imagescan/os-patches", bot),      // not image-scan/
		}},
		checks:  map[string][][]bump.CheckRun{},
		authors: map[int][]string{1: {bot}, 2: {bot, bot}, 7: {bot, "someone"}, 8: {""}, 11: {bot}, 12: {bot, "someone"}, 13: {bot}},
	}
	for n := 1; n <= 13; n++ {
		gh.checks[fmt.Sprintf("sha%d", n)] = green()
	}
	out, _, err := runMerge(t, gh, "--repos", "mock-plugin-1", "--dry-run", "off")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := []string{
		`mock-plugin-1#1 sha1 "fix(deps): update security fixes 1 (#1)"`,
		`mock-plugin-1#2 sha2 "fix(deps): update security fixes 2 (#2)"`,
		`mock-plugin-1#11 sha11 "fix(deps): update security fixes 11 (#11)"`,
	}
	if !slices.Equal(gh.merges, want) {
		t.Errorf("merges %q, want %q\n%s", gh.merges, want, out)
	}
	if gh.owner != "compliance-framework" {
		t.Errorf("client owner %q, want compliance-framework", gh.owner)
	}
	for _, s := range []string{
		"mock-plugin-1#3: labelled renovate:automerge, but not a renovate/* or image-scan/* branch of the repo opened by ccf-release-bot[bot]; left alone",
		"mock-plugin-1#4: labelled renovate:automerge, but not a renovate/* or image-scan/* branch",
		"mock-plugin-1#5: labelled renovate:automerge, but not a renovate/* or image-scan/* branch",
		"mock-plugin-1#12: not every commit is authored by ccf-release-bot[bot]; left for a person",
		"mock-plugin-1#13: labelled renovate:automerge, but not a renovate/* or image-scan/* branch",
		"mock-plugin-1#6: labelled needs-human; left for a person",
		"mock-plugin-1#7: not every commit is authored by ccf-release-bot[bot]; left for a person",
		"mock-plugin-1#8: not every commit is authored by ccf-release-bot[bot]; left for a person",
		"mock-plugin-1#10: not every commit is authored by ccf-release-bot[bot]; left for a person",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "#9") {
		t.Errorf("an unlabelled PR is mentioned:\n%s", out)
	}
}

func TestMergeWaitAndDryRun(t *testing.T) {
	setup := func() *fakeGH {
		return &fakeGH{
			open: map[string][]bump.PR{"mock-plugin-1": {renovatePR(1, "renovate/security-gomod", bot)}},
			checks: map[string][][]bump.CheckRun{"sha1": {
				{{ID: 1, Name: "ci / required", Status: "in_progress"}},
				{{ID: 1, Name: "ci / required", Status: "completed", Conclusion: "success"}},
			}},
			authors: map[int][]string{1: {bot}},
		}
	}
	// Renovate just pushed the branch: its check runs, and the pass waits for it.
	gh := setup()
	out, slept, err := runMerge(t, gh, "--repos", "mock-plugin-1", "--dry-run", "off", "--wait", "20m")
	if err != nil || len(gh.merges) != 1 || slept != 1 || !strings.Contains(out, "1 PR(s) pending; checking again in 30s") {
		t.Errorf("--wait 20m: merges %q, slept %d, err %v\n%s", gh.merges, slept, err, out)
	}
	// Any dry run (the default full) only prints, and doesn't wait.
	for _, args := range [][]string{{"--wait", "20m"}, {"--dry-run", "lookup"}} {
		gh = setup()
		gh.checks["sha1"] = green()
		out, slept, err = runMerge(t, gh, append([]string{"--repos", "mock-plugin-1"}, args...)...)
		if err != nil || len(gh.merges) != 0 || slept != 0 || !strings.Contains(out, `mock-plugin-1#1: dry run: would merge "fix(deps): update security fixes 1 (#1)"`) {
			t.Errorf("%v: merges %q, slept %d, err %v\n%s", args, gh.merges, slept, err, out)
		}
	}
}

func TestMergeErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{nil, "merge takes --repos"},
		{[]string{"--repos", " "}, "merge takes --repos"},
		{[]string{"--repos", "mock-ui", "extra"}, "unexpected arguments"},
		{[]string{"--repos", "api"}, "not in the manifest: api"},
		{[]string{"--repos", "mock-ui", "--dry-run", "true"}, "dry run"},
		{[]string{"--repos", "mock-ui", "--wait", "-1s"}, "--wait must not be negative"},
	} {
		if _, _, err := runMerge(t, &fakeGH{}, tc.args...); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("merge %v: err %v, want one containing %q", tc.args, err, tc.err)
		}
	}
	// A live pass never takes a repo of an inactive batch (batches.yaml has none active).
	var out bytes.Buffer
	e := mergeEnv{github: func(string) mergeGitHub { return &fakeGH{} }, stdout: &out, now: time.Now, sleep: func(time.Duration) {}}
	if err := merge(context.Background(), []string{"--manifest", batchesYAML, "--repos", "plugin-two", "--dry-run", "off"}, e); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Errorf("live merge of an inactive repo: err %v", err)
	}
}
