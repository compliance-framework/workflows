package attention

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)

type fake struct {
	prs    map[string][]PR
	failed map[string]bool   // "repo@sha:check" -> failed
	files  map[string]string // "repo@ref" -> release-please manifest
	errs   map[string]error  // repo, or "repo@sha:check"
}

func (f *fake) OpenPRs(_ context.Context, owner, repo string) ([]PR, error) {
	if owner != "o" {
		return nil, errors.New("wrong owner " + owner)
	}
	return slices.Clone(f.prs[repo]), f.errs[repo]
}
func (f *fake) FailedCheck(_ context.Context, _, repo, sha, name string) (bool, error) {
	k := repo + "@" + sha + ":" + name
	return f.failed[k], f.errs[k]
}
func (f *fake) File(_ context.Context, _, repo, ref, path string) ([]byte, error) {
	if path != ReleaseManifest {
		return nil, errors.New("unexpected file " + path)
	}
	if s, ok := f.files[repo+"@"+ref]; ok {
		return []byte(s), nil
	}
	return nil, nil
}

func pr(n int, author, head string, age time.Duration, labels ...string) PR {
	return PR{Number: n, Title: "pr " + head, URL: "https://github.com/o/r/pull/" + head, Author: author, HeadRef: head,
		HeadSHA: head + "-sha", BaseSHA: "base", Labels: labels, Created: now.Add(-age)}
}

var cfg = Config{Owner: "o", BotLogin: "ccf-release-bot[bot]", RequiredChecks: []string{"ci / required"},
	ReleaseCheck: "release-checks / release-checks", StaleAfter: 7 * 24 * time.Hour, Now: now}

func TestCollect(t *testing.T) {
	const bot, day = "ccf-release-bot[bot]", 24 * time.Hour
	f := &fake{
		prs: map[string][]PR{
			"mock-ui": {
				pr(14, bot, "renovate/vite-8.x", 2*day, "needs-human"),
				pr(13, bot, "renovate/typescript-7.x", 9*day, "dependencies", "needs-human"),
				pr(15, bot, "renovate/all-non-major", day),               // green and fresh: nothing
				pr(16, "octocat", "fix", 30*day),                         // a human's old PR: nothing
				pr(17, "octocat", "feature", day, "needs-human"),         // a human's labelled PR
				pr(18, "renovate[bot]", "renovate/go", 3*day),            // required check failed
				pr(19, bot, "ccf-bump/sync-2026-10-08", 8*day),           // stale bot PR
				pr(20, bot, "release-please--branches--main", 3*day),     // a major without approval
				pr(21, bot, "release-please--branches--main--x", 3*day),  // release check failed, no major
				pr(22, bot, "release-please--branches--main--ok", 3*day), // approved: passes
				pr(23, "someone[bot]", "dependabot/npm/x", 10*day),       // not one of our bots
			},
			"mock-api":  nil,
			"workflows": {pr(3, bot, "release-please--branches--main", time.Hour)},
		},
		failed: map[string]bool{
			"mock-ui@renovate/go-sha:ci / required":                                         true,
			"mock-ui@release-please--branches--main-sha:release-checks / release-checks":    true,
			"mock-ui@release-please--branches--main--x-sha:release-checks / release-checks": true,
		},
		files: map[string]string{
			"mock-ui@base": `{".": "1.4.0"}`,
			"mock-ui@release-please--branches--main-sha":    `{".": "2.0.0"}`,
			"mock-ui@release-please--branches--main--x-sha": `{".": "1.5.0"}`,
		},
		errs: map[string]error{
			"mock-gone": errors.New("404"),
			"workflows@release-please--branches--main-sha:ci / required": errors.New("403"),
		},
	}
	d := Collect(context.Background(), f, cfg, []string{"mock-ui", "mock-api", "mock-gone", "workflows"})
	var got []string
	for _, it := range d.Items {
		got = append(got, it.PR.Repo+"#"+strconv.Itoa(it.PR.Number)+": "+strings.Join(it.Reasons, "; "))
	}
	want := []string{
		"mock-ui#13: labelled needs-human; open over 7d",
		"mock-ui#14: labelled needs-human",
		"mock-ui#17: labelled needs-human",
		"mock-ui#18: ci / required failed",
		"mock-ui#19: open over 7d",
		"mock-ui#20: needs release:major-approved (.: v1.4.0 -> v2.0.0)",
		"mock-ui#21: release-checks / release-checks failed",
	}
	if !slices.Equal(got, want) {
		t.Errorf("items:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(d.Failed) != 2 || d.Failed[0].Where != "mock-gone" || d.Failed[1].Where != "workflows#3" {
		t.Errorf("failed = %+v", d.Failed)
	}
}

func TestReleaseReasonApproved(t *testing.T) {
	f := &fake{
		prs:    map[string][]PR{"r": {pr(1, "x", "release-please--branches--main", time.Hour, "release:major-approved")}},
		failed: map[string]bool{"r@release-please--branches--main-sha:release-checks / release-checks": true},
		files:  map[string]string{"r@base": `{".": "1.0.0"}`, "r@release-please--branches--main-sha": `{".": "2.0.0"}`},
	}
	d := Collect(context.Background(), f, cfg, []string{"r"})
	if len(d.Items) != 1 || !slices.Equal(d.Items[0].Reasons, []string{"release-checks / release-checks failed"}) {
		t.Errorf("items = %+v", d.Items)
	}
}

func TestMessage(t *testing.T) {
	d := Digest{
		Items: []Item{
			{PR: PR{Repo: "mock-ui", Number: 13, Title: "chore(deps): update typescript to v7 <major>", URL: "https://github.com/o/mock-ui/pull/13", Created: now.Add(-9*24*time.Hour - time.Hour)},
				Reasons: []string{"labelled needs-human", "open over 7d"}},
			{PR: PR{Repo: "mock-ui", Number: 14, Title: "t", URL: "https://github.com/o/mock-ui/pull/14", Created: now.Add(-5 * time.Hour)}, Reasons: []string{"x"}},
			{PR: PR{Repo: "workflows", Number: 3, Title: "chore(main): release 2.0.0", URL: "https://github.com/o/workflows/pull/3", Created: now.Add(-time.Minute)},
				Reasons: []string{"needs release:major-approved (.: v1.4.0 -> v2.0.0)"}},
		},
		Failed: []Failure{{Where: "mock-gone", Err: errors.New("404")}},
	}
	want := `:raising_hand: 3 PRs need a human
*mock-ui*
• <https://github.com/o/mock-ui/pull/13|mock-ui#13> chore(deps): update typescript to v7 &lt;major&gt; · labelled needs-human, open over 7d · 9d
• <https://github.com/o/mock-ui/pull/14|mock-ui#14> t · x · 5h
*workflows*
• <https://github.com/o/workflows/pull/3|workflows#3> chore(main): release 2.0.0 · needs release:major-approved (.: v1.4.0 -&gt; v2.0.0) · <1h
Could not read: mock-gone
<https://github.com/o/workflows/actions/runs/7|run>`
	if got := Message(d, now, "https://github.com/o/workflows/actions/runs/7"); got != want {
		t.Errorf("message:\n%s\nwant:\n%s", got, want)
	}
	if got := Message(Digest{Items: d.Items[:1]}, now, ""); !strings.HasPrefix(got, ":raising_hand: 1 PR needs a human\n") || strings.Contains(got, "run>") {
		t.Errorf("one item:\n%s", got)
	}
}

// Release-please PRs wait for the monthly train by design: their age never lists them, only the
// release check, a failing required check or the needs-human label.
func TestReleasePRsAreNeverStale(t *testing.T) {
	const bot, day = "ccf-release-bot[bot]", 24 * time.Hour
	f := &fake{
		prs: map[string][]PR{"r": {
			pr(1, bot, "release-please--branches--main", 20*day),
			pr(2, bot, "release-please--branches--main--ci", 20*day),
			pr(3, bot, "release-please--branches--main--label", 20*day, "needs-human"),
			pr(4, bot, "renovate/old", 20*day),
		}},
		failed: map[string]bool{"r@release-please--branches--main--ci-sha:ci / required": true},
	}
	d := Collect(context.Background(), f, cfg, []string{"r"})
	var got []string
	for _, it := range d.Items {
		got = append(got, strconv.Itoa(it.PR.Number)+": "+strings.Join(it.Reasons, "; "))
	}
	want := []string{"2: ci / required failed", "3: labelled needs-human", "4: open over 7d"}
	if !slices.Equal(got, want) || len(d.Failed) != 0 {
		t.Errorf("items %q, want %q; failed %+v", got, want, d.Failed)
	}
}
