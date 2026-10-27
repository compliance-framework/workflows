package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/bump"
)

const bot = "ccf-release-bot[bot]"

// mergeSetup is a fake GitHub with open PRs on mock-ui and a manifest, without clones.
func mergeSetup(t *testing.T, prs ...*bump.PR) (args []string, e env, gh *fakeGH, out *bytes.Buffer, slept *[]time.Duration) {
	m := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(m, []byte(testManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	gh = &fakeGH{open: map[string]*bump.PR{}, checks: map[string][][]bump.CheckRun{}, conflicts: map[int]bool{}, computing: map[int]int{}}
	for _, p := range prs {
		gh.open[fmt.Sprintf("mock-ui %d", p.Number)] = p
	}
	now, slept := time.Date(2026, 10, 8, 5, 41, 0, 0, time.UTC), &[]time.Duration{}
	out = &bytes.Buffer{}
	e = env{gh: gh, getenv: func(string) string { return "" }, stdout: out, now: func() time.Time { return now },
		sleep: func(d time.Duration) { *slept = append(*slept, d); now = now.Add(d) }}
	return []string{"merge", "--repos", "mock-ui", "--manifest", m}, e, gh, out, slept
}

// botPR is a ccf-bump PR labelled ccf-bump:automerge, with head SHA "sha<n>".
func botPR(n int, branch, login string, labels ...string) *bump.PR {
	p := newPR(n, "compliance-framework", "mock-ui", branch, login, "Bot")
	p.Title, p.Head.SHA = fmt.Sprintf("ci(deps): bump workflows to v1.%d.0", n), fmt.Sprintf("sha%d", n)
	for _, l := range append([]string{bump.AutomergeLabel}, labels...) {
		p.Labels = append(p.Labels, bump.PRLabel{Name: l})
	}
	return p
}

// check is a run of ci / required; nthRun(n, ...) is the nth, started n minutes after the first.
func check(status, conclusion string) bump.CheckRun { return nthRun(1, status, conclusion) }

func nthRun(n int, status, conclusion string) bump.CheckRun {
	return bump.CheckRun{ID: int64(n), Name: "ci / required", Status: status, Conclusion: conclusion,
		StartedAt: time.Date(2026, 10, 8, 5, n, 0, 0, time.UTC), URL: fmt.Sprintf("https://example/run%d", n)}
}

func merged(n int) string {
	return fmt.Sprintf(`merge mock-ui#%d sha%d "ci(deps): bump workflows to v1.%d.0 (#%d)"`, n, n, n, n)
}

func TestMerge(t *testing.T) {
	green := [][]bump.CheckRun{{check("completed", "success")}}
	expected := &bump.StatusError{Method: "PUT", Path: "/repos/compliance-framework/mock-ui/pulls/1/merge", Code: 405,
		Body: []byte(`{"message":"Required status check \"ci / required\" is expected."}`)}
	// queued is mock-plugin-2#30's refusal (run 37915939413): the labeled run was queued.
	queued := &bump.StatusError{Method: "PUT", Path: "/repos/compliance-framework/mock-ui/pulls/1/merge", Code: 405,
		Body: []byte(`{"message":"Repository rule violations found\n\nRequired status check \"ci / required\" is queued.\n\n"}`)}
	for name, tc := range map[string]struct {
		fake  func(*fakeGH)
		flags []string
		calls []string
		out   []string
		slept int
	}{
		"green and mergeable": {
			fake:  func(f *fakeGH) { f.checks["sha1"] = green },
			calls: []string{merged(1)},
			out:   []string{`mock-ui#1: merged "ci(deps): bump workflows to v1.1.0 (#1)"`},
		},
		"failing": {
			fake: func(f *fakeGH) { f.checks["sha1"] = [][]bump.CheckRun{{check("completed", "failure")}} },
			out:  []string{"::warning::ccf-bump: mock-ui#1: ci / required concluded failure at sha1; left open (https://example/run1)"},
		},
		"conflicting": {
			fake: func(f *fakeGH) { f.checks["sha1"], f.conflicts[1] = green, true },
			out:  []string{"::warning::ccf-bump: mock-ui#1: conflicts with its base; left open"},
		},
		"pending, no wait": {
			fake: func(f *fakeGH) { f.checks["sha1"] = [][]bump.CheckRun{{check("in_progress", "")}} },
			out:  []string{"mock-ui#1: still pending (ci / required in_progress)"},
		},
		"not started, no wait": {
			out: []string{"mock-ui#1: still pending (ci / required not started)"},
		},
		"pending, then green within --wait": {
			fake: func(f *fakeGH) {
				f.checks["sha1"] = [][]bump.CheckRun{{}, {check("queued", "")}, {check("completed", "success")}}
				f.computing[1] = 1
			},
			flags: []string{"--wait", "20m"},
			calls: []string{merged(1)},
			out:   []string{"1 PR(s) pending; checking again in 30s", `mock-ui#1: merged`},
			slept: 3, // not started, queued, mergeability computing
		},
		"pending past --wait": {
			fake:  func(f *fakeGH) { f.checks["sha1"] = [][]bump.CheckRun{{check("in_progress", "")}} },
			flags: []string{"--wait", "45s"},
			out:   []string{"checking again in 15s", "mock-ui#1: still pending (ci / required in_progress)"},
			slept: 2,
		},
		"old success, new run in progress, then green within --wait": {
			fake: func(f *fakeGH) {
				f.checks["sha1"] = [][]bump.CheckRun{
					{nthRun(1, "completed", "success"), nthRun(2, "in_progress", "")},
					{nthRun(1, "completed", "success"), nthRun(2, "completed", "success")},
				}
			},
			flags: []string{"--wait", "20m"},
			calls: []string{merged(1)},
			out:   []string{"1 PR(s) pending; checking again in 30s", "mock-ui#1: merged"},
			slept: 1,
		},
		"old success, new run in progress, no wait": {
			fake: func(f *fakeGH) {
				f.checks["sha1"] = [][]bump.CheckRun{{nthRun(1, "completed", "success"), nthRun(2, "queued", "")}}
			},
			out: []string{"mock-ui#1: still pending (ci / required queued)"},
		},
		"old failure, new success": {
			fake: func(f *fakeGH) {
				f.checks["sha1"] = [][]bump.CheckRun{{nthRun(2, "completed", "success"), nthRun(1, "completed", "failure")}}
			},
			calls: []string{merged(1)},
		},
		"old success, new failure": {
			fake: func(f *fakeGH) {
				f.checks["sha1"] = [][]bump.CheckRun{{nthRun(1, "completed", "success"), nthRun(2, "completed", "failure")}}
			},
			out: []string{"::warning::ccf-bump: mock-ui#1: ci / required concluded failure at sha1; left open (https://example/run2)"},
		},
		"newest by start time, not ID": {
			fake: func(f *fakeGH) {
				late, early := nthRun(3, "completed", "success"), nthRun(1, "completed", "failure")
				early.ID = 9
				f.checks["sha1"] = [][]bump.CheckRun{{late, early}}
			},
			calls: []string{merged(1)},
		},
		"GitHub still expects the check, then merges within --wait": {
			fake:  func(f *fakeGH) { f.checks["sha1"], f.mergeErrs = green, []error{expected} },
			flags: []string{"--wait", "20m"},
			calls: []string{merged(1), merged(1)},
			out:   []string{"1 PR(s) pending", "mock-ui#1: merged"},
			slept: 1,
		},
		"GitHub still expects the check, no wait": {
			fake:  func(f *fakeGH) { f.checks["sha1"], f.mergeErr = green, expected },
			calls: []string{merged(1)},
			out:   []string{"mock-ui#1: still pending (GitHub still expects ci / required)"},
		},
		"GitHub reports the check queued, no wait": {
			fake:  func(f *fakeGH) { f.checks["sha1"], f.mergeErr = green, queued },
			calls: []string{merged(1)},
			out:   []string{"mock-ui#1: still pending (GitHub still expects ci / required)"},
		},
		"merge refused": {
			fake:  func(f *fakeGH) { f.checks["sha1"], f.mergeErr = green, fmt.Errorf("405 Method Not Allowed") },
			calls: []string{merged(1)},
			out:   []string{"::warning::ccf-bump: mock-ui#1: merge refused: 405 Method Not Allowed"},
		},
		"dry run": {
			fake:  func(f *fakeGH) { f.checks["sha1"] = green },
			flags: []string{"--dry-run", "--wait", "20m"},
			out:   []string{`mock-ui#1: dry run: would merge "ci(deps): bump workflows to v1.1.0 (#1)"`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			args, e, gh, out, slept := mergeSetup(t, botPR(1, "ccf-bump/sync-2026-10-08", bot))
			if tc.fake != nil {
				tc.fake(gh)
			}
			if err := run(context.Background(), append(args, tc.flags...), e); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !slices.Equal(gh.calls, tc.calls) {
				t.Errorf("calls %q, want %q", gh.calls, tc.calls)
			}
			for _, s := range tc.out {
				if !strings.Contains(out.String(), s) {
					t.Errorf("output lacks %q:\n%s", s, out)
				}
			}
			if len(*slept) != tc.slept {
				t.Errorf("slept %v, want %d polls", *slept, tc.slept)
			}
		})
	}
}

func TestMergeOnlyItsOwnPRs(t *testing.T) {
	fork := botPR(5, "ccf-bump/sync-2026-10-08", bot)
	fork.Head.Repo.FullName = "someone/mock-ui"
	unlabelled := botPR(8, "ccf-bump/sync-2026-10-08", bot)
	unlabelled.Labels = nil
	args, e, gh, out, _ := mergeSetup(t,
		botPR(1, "ccf-bump/train-2026-10-08", bot),
		botPR(2, "ccf-bump/sync-2026-10-08", "someone"), // a person's
		botPR(3, "renovate/go", "renovate[bot]"),        // Renovate
		botPR(4, "release-please--branches--main", bot), // release-please
		fork, // a fork
		botPR(6, "ccf-bump/sync-2026-10-08", "other-app[bot]"),          // another account
		botPR(7, "ccf-bump/sync-2026-10-08", bot, bump.NeedsHumanLabel), // held for a person
		unlabelled,
	)
	for n := 1; n <= 8; n++ {
		gh.checks[fmt.Sprintf("sha%d", n)] = [][]bump.CheckRun{{check("completed", "success")}}
	}
	if err := run(context.Background(), args, e); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := []string{merged(1)}; !slices.Equal(gh.calls, want) {
		t.Errorf("calls %q, want %q\n%s", gh.calls, want, out)
	}
	for _, n := range []int{2, 3, 4, 5, 6} {
		if s := fmt.Sprintf("mock-ui#%d: labelled ccf-bump:automerge, but not a ccf-bump/* branch", n); !strings.Contains(out.String(), s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if !strings.Contains(out.String(), "mock-ui#7: labelled needs-human; left for a person") {
		t.Errorf("output:\n%s", out)
	}
}

func TestMergeChangedMeanwhile(t *testing.T) {
	args, e, gh, out, _ := mergeSetup(t, botPR(1, "ccf-bump/sync-2026-10-08", bot), botPR(2, "ccf-bump/sync-2026-10-08", bot),
		botPR(3, "ccf-bump/train-2026-10-08", bot))
	for _, sha := range []string{"sha1", "sha2", "new2", "sha3"} {
		gh.checks[sha] = [][]bump.CheckRun{{check("completed", "success")}}
	}
	// After the listing, #1 gets needs-human, #2 a new head (a sync rerun pushed) and the train
	// merges #3 (GitHub then reports mergeable null).
	gh.changed = map[int]func(*bump.PR){
		1: func(p *bump.PR) { p.Labels = append(p.Labels, bump.PRLabel{Name: bump.NeedsHumanLabel}) },
		2: func(p *bump.PR) { p.Head.SHA = "new2" },
		3: func(p *bump.PR) { p.State, p.Mergeable = "closed", nil },
	}
	if err := run(context.Background(), append(args, "--wait", "5m"), e); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := []string{`merge mock-ui#2 new2 "ci(deps): bump workflows to v1.2.0 (#2)"`}; !slices.Equal(gh.calls, want) {
		t.Errorf("calls %q, want %q", gh.calls, want)
	}
	for _, s := range []string{"mock-ui#1: labels changed", "mock-ui#3: no longer open"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out.String(), "still pending") {
		t.Errorf("a PR is left pending:\n%s", out)
	}
}

func TestMergeErrors(t *testing.T) {
	args, e, gh, out, _ := mergeSetup(t)
	gh.listErr = fmt.Errorf("502 Bad Gateway")
	err := run(context.Background(), args, e)
	if err == nil || !strings.Contains(err.Error(), "mock-ui: list open PRs: 502 Bad Gateway") {
		t.Errorf("error = %v\n%s", err, out)
	}
	for flags, want := range map[string]string{
		"merge":                  "merge takes one of --all or --repos",
		"merge --all --wait -1s": "--wait must not be negative",
	} {
		if err := run(context.Background(), append(strings.Fields(flags), "--manifest", args[4]), e); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", flags, err, want)
		}
	}
}
