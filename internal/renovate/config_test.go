package renovate

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
)

func TestSelectRepos(t *testing.T) {
	m := &manifest.Manifest{Repos: []manifest.Repo{{Name: "a", Release: true}, {Name: "b", Release: false}, {Name: "c", Release: true}, {Name: "w", Kind: manifest.KindWorkflows}}}
	for _, tc := range []struct {
		list string
		want []string
		err  string
	}{
		{list: "", want: []string{"a", "b", "c", "w"}}, // the workflows repo too
		{list: " c, a\n", want: []string{"a", "c"}},
		{list: "c c", want: []string{"c"}},
		{list: "b", want: []string{"b"}},
		{list: "w", want: []string{"w"}},
		{list: "a,x", err: "x"},
	} {
		got, err := SelectRepos(m, tc.list, DryRunOff)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("SelectRepos(%q) err = %v, want one naming %q", tc.list, err, tc.err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("SelectRepos(%q) = %v, %v; want %v", tc.list, got, err, tc.want)
		}
	}
	if _, err := SelectRepos(&manifest.Manifest{}, "", DryRunOff); err == nil {
		t.Error("SelectRepos on an empty manifest: want an error")
	}
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, b)
	}
	return cfg
}

func TestGlobalConfig(t *testing.T) {
	preset := []byte(`{"extends": ["config:recommended"], "prHourlyLimit": 4, "vulnerabilityAlerts": {"minimumReleaseAge": null}, "description": "a > b"}`)
	out, err := GlobalConfig(preset, Options{Owner: "compliance-framework", Repos: []string{"mock-api", "mock-ui"}, DryRun: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"a > b"`) {
		t.Errorf("strings are HTML-escaped:\n%s", out)
	}
	if !strings.Contains(string(out), `"prHourlyLimit": 4,`) {
		t.Errorf("number not kept as written:\n%s", out)
	}
	cfg := decode(t, out)
	for k, want := range map[string]any{
		"platform":        "github",
		"autodiscover":    false,
		"onboarding":      false,
		"requireConfig":   "optional",
		"dryRun":          "full",
		"repositories":    []any{"compliance-framework/mock-api", "compliance-framework/mock-ui"},
		"statusCheckWhen": map[string]any{"minimumReleaseAge": "never"},
		"extends":         []any{"config:recommended"},
		"prHourlyLimit":   float64(4),
	} {
		got, _ := json.Marshal(cfg[k])
		w, _ := json.Marshal(want)
		if string(got) != string(w) {
			t.Errorf("%s = %s, want %s", k, got, w)
		}
	}
	va, ok := cfg["vulnerabilityAlerts"].(map[string]any)
	if v, set := va["minimumReleaseAge"]; !ok || !set || v != nil {
		t.Errorf("vulnerabilityAlerts.minimumReleaseAge = %v (set %v), want null", v, set)
	}

	out, err = GlobalConfig(preset, Options{Owner: "o", Repos: []string{"r"}, DryRun: DryRunOff})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decode(t, out)["dryRun"]; ok {
		t.Errorf("dry run off still sets dryRun:\n%s", out)
	}
}

func TestGlobalConfigErrors(t *testing.T) {
	ok := Options{Owner: "o", Repos: []string{"r"}, DryRun: "full"}
	for _, tc := range []struct {
		name   string
		preset string
		opts   Options
		err    string
	}{
		{"bad dry run", `{}`, Options{Owner: "o", Repos: []string{"r"}, DryRun: "yes"}, "dry run"},
		{"empty dry run", `{}`, Options{Owner: "o", Repos: []string{"r"}}, "dry run"},
		{"no repos", `{}`, Options{Owner: "o", DryRun: "full"}, "no repos"},
		{"no owner", `{}`, Options{Repos: []string{"r"}, DryRun: "full"}, "no owner"},
		{"not JSON", `{`, ok, "preset"},
		{"not an object", `[]`, ok, "preset"},
		{"null", `null`, ok, "JSON object"},
		{"trailing data", `{} {}`, ok, "trailing"},
		{"sets repositories", `{"repositories": ["o/other"]}`, ok, `"repositories"`},
		{"sets autodiscover", `{"autodiscover": true}`, ok, `"autodiscover"`},
		{"sets dryRun", `{"dryRun": null}`, ok, `"dryRun"`},
	} {
		if _, err := GlobalConfig([]byte(tc.preset), tc.opts); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.err)
		}
	}
}

// TestSharedPreset checks renovate/default.json keeps the rules docs/renovate.md describes, so an
// edit that drops one fails here rather than in a live run.
func TestSharedPreset(t *testing.T) {
	data, err := os.ReadFile("../../renovate/default.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GlobalConfig(data, Options{Owner: "o", Repos: []string{"r"}, DryRun: "full"}); err != nil {
		t.Fatalf("the shared preset does not build a global config: %v", err)
	}
	var p struct {
		Schedule               []string `json:"schedule"`
		MinimumReleaseAge      string   `json:"minimumReleaseAge"`
		InternalChecksFilter   string   `json:"internalChecksFilter"`
		OSVVulnerabilityAlerts bool     `json:"osvVulnerabilityAlerts"`
		PlatformAutomerge      *bool    `json:"platformAutomerge"`
		AutomergeStrategy      string   `json:"automergeStrategy"`
		RebaseWhen             string   `json:"rebaseWhen"`
		PrHourlyLimit          int      `json:"prHourlyLimit"`
		PrConcurrentLimit      int      `json:"prConcurrentLimit"`
		PostUpdateOptions      []string `json:"postUpdateOptions"`
		Extends                []string `json:"extends"`
		VulnerabilityAlerts    struct {
			Schedule           []string        `json:"schedule"`
			MinimumReleaseAge  json.RawMessage `json:"minimumReleaseAge"`
			SemanticCommitType string          `json:"semanticCommitType"`
		} `json:"vulnerabilityAlerts"`
		PackageRules []struct {
			MatchManagers      []string `json:"matchManagers"`
			MatchDepTypes      []string `json:"matchDepTypes"`
			SemanticCommitType string   `json:"semanticCommitType"`
			MatchPackageNames  []string `json:"matchPackageNames"`
			MatchRepositories  []string `json:"matchRepositories"`
			MatchUpdateTypes   []string `json:"matchUpdateTypes"`
			GroupName          string   `json:"groupName"`
			Automerge          *bool    `json:"automerge"`
			AutomergeType      string   `json:"automergeType"`
			Enabled            *bool    `json:"enabled"`
		} `json:"packageRules"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Schedule, []string{"* * 8,22 * *"}) {
		t.Errorf("schedule = %v, want the 8th and 22nd", p.Schedule)
	}
	if !slices.Equal(p.VulnerabilityAlerts.Schedule, []string{"at any time"}) || string(p.VulnerabilityAlerts.MinimumReleaseAge) != "null" {
		t.Errorf("vulnerabilityAlerts = %+v, want any time and no minimum age", p.VulnerabilityAlerts)
	}
	// Renovate forces the vulnerabilityAlerts settings onto every security update, over the
	// packageRules, so each one is fix(deps) and releases whatever its depType (mock-plugin-2#14
	// came out as chore(deps), which release-please hides).
	if p.VulnerabilityAlerts.SemanticCommitType != "fix" {
		t.Errorf("vulnerabilityAlerts.semanticCommitType = %q, want fix", p.VulnerabilityAlerts.SemanticCommitType)
	}
	if p.MinimumReleaseAge != "7 days" || p.InternalChecksFilter != "strict" {
		t.Errorf("minimumReleaseAge %q, internalChecksFilter %q", p.MinimumReleaseAge, p.InternalChecksFilter)
	}
	if !p.OSVVulnerabilityAlerts || p.PrHourlyLimit <= 0 || p.PrConcurrentLimit <= 0 {
		t.Errorf("osv %v, prHourlyLimit %d, prConcurrentLimit %d", p.OSVVulnerabilityAlerts, p.PrHourlyLimit, p.PrConcurrentLimit)
	}
	// GitHub's native auto-merge never applies the ccf-review bypass, so Renovate merges the PRs
	// itself, through the API as ccf-release-bot, with the repos' only merge method.
	if p.PlatformAutomerge == nil || *p.PlatformAutomerge || p.AutomergeStrategy != "squash" {
		t.Errorf("platformAutomerge %v, automergeStrategy %q, want false and squash", p.PlatformAutomerge, p.AutomergeStrategy)
	}
	// With automerge on, Renovate's default rebaseWhen "auto" means behind-base-branch: after one
	// merge every other PR is behind main and gets rebased (a new CI run) instead of merged, so a
	// run merges at most one PR per repo. ccf-required doesn't require up-to-date branches.
	if p.RebaseWhen != "conflicted" {
		t.Errorf("rebaseWhen = %q, want conflicted", p.RebaseWhen)
	}
	if !slices.Contains(p.PostUpdateOptions, "gomodTidy") {
		t.Errorf("postUpdateOptions = %v, want gomodTidy", p.PostUpdateOptions)
	}
	for _, e := range []string{":semanticCommits", ":semanticPrefixFixDepsChoreOthers", "helpers:pinGitHubActionDigests"} {
		if !slices.Contains(p.Extends, e) {
			t.Errorf("extends = %v, want %s", p.Extends, e)
		}
	}

	var internalOff, grouped, opaOff, opaAPI, indirectFix bool
	for _, r := range p.PackageRules {
		off := r.Enabled != nil && !*r.Enabled
		switch {
		// Indirect modules are linked into the binary: their updates must release (fix), not
		// chore(deps), which :semanticPrefixFixDepsChoreOthers gives depType indirect.
		case slices.Equal(r.MatchDepTypes, []string{"indirect"}):
			indirectFix = slices.Equal(r.MatchManagers, []string{"gomod"}) && r.SemanticCommitType == "fix" && r.Enabled == nil
		case off && slices.Contains(r.MatchPackageNames, "github.com/compliance-framework/**") && slices.Contains(r.MatchPackageNames, "ghcr.io/compliance-framework/**"):
			internalOff = len(r.MatchRepositories) == 0
		case r.GroupName != "" && slices.Contains(r.MatchPackageNames, "*"):
			grouped = r.Automerge != nil && *r.Automerge && r.AutomergeType == "pr" && !slices.Contains(r.MatchUpdateTypes, "major") &&
				slices.Equal(r.MatchUpdateTypes, []string{"minor", "patch", "digest", "pin", "pinDigest"})
		case slices.Contains(r.MatchPackageNames, "github.com/open-policy-agent/opa"):
			if off {
				opaOff = slices.Equal(r.MatchRepositories, []string{"!compliance-framework/api", "!compliance-framework/mock-api"})
			} else {
				opaAPI = r.Automerge != nil && !*r.Automerge && r.GroupName != "" && slices.Contains(r.MatchRepositories, "compliance-framework/api")
			}
		}
	}
	if !internalOff || !grouped || !opaOff || !opaAPI || !indirectFix {
		t.Errorf("package rules: internal off %v, non-majors grouped and auto-merged %v, OPA off outside api %v, OPA own PR in api %v, indirect Go modules fix %v",
			internalOff, grouped, opaOff, opaAPI, indirectFix)
	}
}

func TestShard(t *testing.T) {
	repos := func(n int) []string {
		var out []string
		for i := range n {
			out = append(out, fmt.Sprintf("r%02d", i))
		}
		return out
	}
	for _, tc := range []struct {
		n, max      int
		slot        int64
		want        []string
		index, of   int
		description string
	}{
		{7, 15, 3, repos(7), 0, 1, "fits in one run"},
		{15, 15, 9, repos(15), 0, 1, "exactly max"},
		{16, 15, 0, []string{"r00", "r02", "r04", "r06", "r08", "r10", "r12", "r14"}, 0, 2, "two shards"},
		{16, 15, 1, []string{"r01", "r03", "r05", "r07", "r09", "r11", "r13", "r15"}, 1, 2, "the other shard"},
		{16, 15, 7, []string{"r01", "r03", "r05", "r07", "r09", "r11", "r13", "r15"}, 1, 2, "slot mod shards"},
		{16, 15, -1, []string{"r01", "r03", "r05", "r07", "r09", "r11", "r13", "r15"}, 1, 2, "negative slot"},
		{5, 0, 4, repos(5), 0, 1, "no limit"},
	} {
		got, index, of := Shard(repos(tc.n), tc.max, tc.slot)
		if !slices.Equal(got, tc.want) || index != tc.index || of != tc.of {
			t.Errorf("%s: Shard(%d repos, %d, %d) = %v, %d, %d; want %v, %d, %d", tc.description, tc.n, tc.max, tc.slot, got, index, of, tc.want, tc.index, tc.of)
		}
	}

	// renovate.yml passes its run number as the slot. Any n consecutive run numbers take every shard
	// exactly once, so every repo is visited exactly once per n runs, and no shard is over the
	// limit, for the manifest's sizes: today (7), with batch 1 (37), 1+2 (64), and all three (74).
	for _, n := range []int{7, 37, 64, 74} {
		_, _, of := Shard(repos(n), MaxLiveRepos, 0)
		for _, first := range []int64{1, 2, 4242} {
			seen := map[string]int{}
			shards := map[int]bool{}
			for run := first; run < first+int64(of); run++ {
				s, index, _ := Shard(repos(n), MaxLiveRepos, run)
				if len(s) > MaxLiveRepos {
					t.Errorf("%d repos: run %d has %d repos, over %d", n, run, len(s), MaxLiveRepos)
				}
				if shards[index] {
					t.Errorf("%d repos: runs %d..%d take shard %d twice", n, first, first+int64(of)-1, index)
				}
				shards[index] = true
				for _, r := range s {
					seen[r]++
				}
			}
			for _, r := range repos(n) {
				if seen[r] != 1 {
					t.Errorf("%d repos: runs %d..%d visit %s %d times, want 1", n, first, first+int64(of)-1, r, seen[r])
				}
			}
		}
	}
}

// TestThrottle: a live run takes at most MaxLiveRepos repos and each opens at most prHourlyLimit
// non-vulnerability PRs, so one run opens at most the plan's ~15 across the org.
func TestThrottle(t *testing.T) {
	data, err := os.ReadFile("../../renovate/default.json")
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		PrHourlyLimit int `json:"prHourlyLimit"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if p.PrHourlyLimit != 1 || MaxLiveRepos*p.PrHourlyLimit > 15 {
		t.Errorf("prHourlyLimit %d x MaxLiveRepos %d: want prHourlyLimit 1 and at most 15 PRs a run", p.PrHourlyLimit, MaxLiveRepos)
	}
}
