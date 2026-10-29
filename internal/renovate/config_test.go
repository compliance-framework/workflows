package renovate

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
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
			Schedule              []string        `json:"schedule"`
			MinimumReleaseAge     json.RawMessage `json:"minimumReleaseAge"`
			PrConcurrentLimit     json.RawMessage `json:"prConcurrentLimit"`
			BranchConcurrentLimit json.RawMessage `json:"branchConcurrentLimit"`
			SemanticCommitType    string          `json:"semanticCommitType"`
			GroupName             string          `json:"groupName"`
			GroupSlug             string          `json:"groupSlug"`
			Labels                []string        `json:"labels"`
			AddLabels             []string        `json:"addLabels"`
		} `json:"vulnerabilityAlerts"`
		PackageRules []struct {
			MatchManagers              []string `json:"matchManagers"`
			MatchDepTypes              []string `json:"matchDepTypes"`
			SemanticCommitType         string   `json:"semanticCommitType"`
			MatchPackageNames          []string `json:"matchPackageNames"`
			MatchRepositories          []string `json:"matchRepositories"`
			MatchUpdateTypes           []string `json:"matchUpdateTypes"`
			GroupName                  string   `json:"groupName"`
			Automerge                  *bool    `json:"automerge"`
			AutomergeType              string   `json:"automergeType"`
			Enabled                    *bool    `json:"enabled"`
			AddLabels                  []string `json:"addLabels"`
			PrConcurrentLimit          *int     `json:"prConcurrentLimit"`
			PrHourlyLimit              *int     `json:"prHourlyLimit"`
			BranchConcurrentLimit      *int     `json:"branchConcurrentLimit"`
			PinDigests                 *bool    `json:"pinDigests"`
			MatchDatasources           []string `json:"matchDatasources"`
			MinimumReleaseAgeBehaviour string   `json:"minimumReleaseAgeBehaviour"`
			RangeStrategy              string   `json:"rangeStrategy"`
		} `json:"packageRules"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	// Normal updates run every day: on the old 8th/22nd schedule a repo's ~5 visits a day opened
	// about one PR each on two days a month, and majors piled up "Awaiting Schedule" (ui#354).
	// minimumReleaseAge is what keeps a release off main for its first week now.
	if !slices.Equal(p.Schedule, []string{"at any time"}) {
		t.Errorf("schedule = %v, want at any time", p.Schedule)
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
	// Renovate merges at most one PR per repo per run (two with its restart), and Go fixes conflict
	// on go.sum once one merges, so separate fixes took one visit each. Grouped per manager they are
	// one PR. groupSlug is what keeps them out of all-non-major; OPA gets its own slug, or in the api
	// (never auto-merged, needs-human) it would hold every Go fix. Renovate 44.145.1 renders these as
	// renovate/security-gomod and renovate/security-gomod-opa (docs/renovate.md).
	const opaSuffix = "{{#if (containsString packageName 'open-policy-agent/opa')}}"
	if va := p.VulnerabilityAlerts; !strings.Contains(va.GroupName, "{{{manager}}}") ||
		va.GroupSlug != "security-{{{manager}}}"+opaSuffix+"-opa{{/if}}" || strings.Contains(va.GroupSlug, "all-non-major") {
		t.Errorf("vulnerabilityAlerts groupName %q, groupSlug %q: want one group per manager, OPA apart, outside all-non-major", va.GroupName, va.GroupSlug)
	}
	if p.MinimumReleaseAge != "7 days" || p.InternalChecksFilter != "strict" {
		t.Errorf("minimumReleaseAge %q, internalChecksFilter %q", p.MinimumReleaseAge, p.InternalChecksFilter)
	}
	// prHourlyLimit 4 leaves the non-major group room in a visit even when a major and security PRs
	// were opened earlier in the same clock hour (Renovate counts every PR the repo opened this hour).
	if !p.OSVVulnerabilityAlerts || p.PrHourlyLimit != 4 || p.PrConcurrentLimit != 8 {
		t.Errorf("osv %v, prHourlyLimit %d, prConcurrentLimit %d: want true, 4 and 8", p.OSVVulnerabilityAlerts, p.PrHourlyLimit, p.PrConcurrentLimit)
	}
	// A security fix must always open. Renovate applies vulnerabilityAlerts over the package rules
	// and calcLimit takes the lowest limit of a branch's upgrades, 0 meaning none, so without these
	// a major security fix would take the majors rule's prConcurrentLimit 3 (and every fix the
	// top-level 8, against the security PRs' own count).
	if va := p.VulnerabilityAlerts; string(va.PrConcurrentLimit) != "0" || string(va.BranchConcurrentLimit) != "0" {
		t.Errorf("vulnerabilityAlerts prConcurrentLimit %s, branchConcurrentLimit %s: want both 0 (no limit)", va.PrConcurrentLimit, va.BranchConcurrentLimit)
	}
	// GitHub's native auto-merge never applies the ccf-review bypass, so Renovate merges the PRs
	// itself, through the API as ccf-release-bot, with the repos' only merge method.
	if p.PlatformAutomerge == nil || *p.PlatformAutomerge || p.AutomergeStrategy != "squash" {
		t.Errorf("platformAutomerge %v, automergeStrategy %q, want false and squash", p.PlatformAutomerge, p.AutomergeStrategy)
	}
	// With automerge on, Renovate's default rebaseWhen "auto" means behind-base-branch: after each
	// merge every other PR is behind main and gets rebased, with a new CI run. "conflicted" rebases a
	// branch only when it conflicts; ccf-required doesn't require up-to-date branches. (It doesn't
	// lift Renovate's one merge per repo per run; grouping the security fixes works around that.)
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

	// Renovate forces vulnerabilityAlerts over the merged config (a plain override, not a merge), so
	// labels there would replace a major fix's needs-human.
	if va := p.VulnerabilityAlerts; va.Labels != nil || va.AddLabels != nil {
		t.Errorf("vulnerabilityAlerts labels %v, addLabels %v: want none", va.Labels, va.AddLabels)
	}

	var internalOff, grouped, opaOff, opaAPI, indirectFix, majorsCapped, digestsPinned, dockerUndated, toolchainFix bool
	var labelRules int
	nonMajor := []string{"minor", "patch", "digest", "pin", "pinDigest"}
	opaNames := []string{"github.com/open-policy-agent/opa", "open-policy-agent/opa", "openpolicyagent/opa"}
	for _, r := range p.PackageRules {
		off := r.Enabled != nil && !*r.Enabled
		// The go directive is the minimum language version, which consumers inherit: Renovate
		// leaves it alone (it reads it as a range every newer Go meets) and so must the preset.
		if r.RangeStrategy != "" || slices.Contains(r.MatchDepTypes, "golang") {
			t.Errorf("a rule matching depTypes %v sets rangeStrategy %q: the go directive is not bumped", r.MatchDepTypes, r.RangeStrategy)
		}
		// Only the majors rule sets PR limits: through calcLimit's lowest-limit-wins, a limit on
		// another rule would cap the non-major group (the renovate:automerge rule matches every
		// non-major update) or a security fix.
		if (r.PrConcurrentLimit != nil || r.PrHourlyLimit != nil || r.BranchConcurrentLimit != nil) && !slices.Equal(r.MatchUpdateTypes, []string{"major"}) {
			t.Errorf("a rule matching %v of %v sets a PR or branch limit: want limits only on the majors rule", r.MatchUpdateTypes, r.MatchPackageNames)
		}
		// renovate:automerge marks what the merge pass merges: exactly the auto-merged non-major
		// updates (the group, and non-major security fixes through it), never a major or OPA (in the
		// api its own needs-human PR). Renovate unions a group's labels, so one rule carries it.
		if slices.Contains(r.AddLabels, AutomergeLabel) {
			labelRules++
			var notOPA []string
			for _, n := range opaNames {
				notOPA = append(notOPA, "!"+n)
			}
			if !slices.Equal(r.MatchUpdateTypes, nonMajor) || !slices.Equal(r.MatchPackageNames, notOPA) || r.GroupName != "" || r.Automerge != nil {
				t.Errorf("the %s rule matches %v of %v: want only the non-major updates, OPA excluded, and nothing else set", AutomergeLabel, r.MatchUpdateTypes, r.MatchPackageNames)
			}
			continue
		}
		switch {
		// Majors wait for a human, so they are capped: a new major opens only while the repo has
		// fewer than 3 open non-vulnerability Renovate PRs (at most 2 majors next to the non-major
		// group) and no Renovate PR yet this clock hour (at most one a visit). The rest wait under
		// Rate-Limited in the Dependency Dashboard.
		case slices.Equal(r.MatchUpdateTypes, []string{"major"}):
			majorsCapped = r.Automerge != nil && !*r.Automerge && slices.Contains(r.AddLabels, "needs-human") &&
				r.PrConcurrentLimit != nil && *r.PrConcurrentLimit == 3 && r.PrHourlyLimit != nil && *r.PrHourlyLimit == 1
		// Indirect modules are linked into the binary: their updates must release (fix), not
		// chore(deps), which :semanticPrefixFixDepsChoreOthers gives depType indirect.
		case slices.Equal(r.MatchDepTypes, []string{"indirect"}):
			indirectFix = slices.Equal(r.MatchManagers, []string{"gomod"}) && r.SemanticCommitType == "fix" && r.Enabled == nil
		// Dockerfile base images are pinned by digest, so an upstream rebuild of a floating tag (OS
		// and runtime CVE fixes) arrives as a digest PR. For every image: the rule must not narrow
		// to some, nor turn any back on (internal images stay off through the rule below).
		case slices.Equal(r.MatchManagers, []string{"dockerfile"}):
			digestsPinned = r.PinDigests != nil && *r.PinDigests && r.Enabled == nil && r.MatchPackageNames == nil &&
				r.MatchDepTypes == nil && r.MatchUpdateTypes == nil && r.MatchRepositories == nil
		// Docker updates have no release timestamp (a floating tag is no version, and the docker
		// datasource dates no Docker Hub tag: agent's golang 1.27.3 and api's postgres stayed
		// pending), and with internalChecksFilter strict the default timestamp-required keeps them
		// pending for good. Docker only, majors included: the rule sets nothing but the behaviour,
		// so a major still gets the majors rule (no auto-merge, needs-human, capped).
		case r.MinimumReleaseAgeBehaviour != "":
			dockerUndated = r.MinimumReleaseAgeBehaviour == "timestamp-optional" && slices.Equal(r.MatchDatasources, []string{"docker"}) &&
				slices.Equal(r.MatchUpdateTypes, []string{"minor", "patch", "major", "digest", "pinDigest"}) && r.MatchPackageNames == nil &&
				r.MatchRepositories == nil && r.MatchManagers == nil && r.MatchDepTypes == nil && r.Automerge == nil &&
				r.AddLabels == nil && r.GroupName == "" && r.PrConcurrentLimit == nil && r.PrHourlyLimit == nil
		// Go standard-library fixes arrive through the go.mod toolchain line: Renovate matches OSV's
		// "stdlib" vulnerabilities only for depType toolchain (vulnerabilities.js), so a toolchain
		// update must release (fix). It sets nothing else: it joins the non-major group, and its
		// security fixes get vulnerabilityAlerts' PR.
		case slices.Contains(r.MatchDepTypes, "toolchain"):
			toolchainFix = slices.Equal(r.MatchDepTypes, []string{"final", "toolchain"}) && r.SemanticCommitType == "fix" &&
				r.MatchUpdateTypes == nil && r.MatchManagers == nil && r.MatchPackageNames == nil && r.GroupName == "" && r.Enabled == nil
		case off && slices.Contains(r.MatchPackageNames, "github.com/compliance-framework/**") && slices.Contains(r.MatchPackageNames, "ghcr.io/compliance-framework/**"):
			internalOff = len(r.MatchRepositories) == 0
		case r.GroupName != "" && slices.Contains(r.MatchPackageNames, "*"):
			grouped = r.Automerge != nil && *r.Automerge && r.AutomergeType == "pr" && !slices.Contains(r.MatchUpdateTypes, "major") &&
				slices.Equal(r.MatchUpdateTypes, nonMajor)
		case slices.Equal(r.MatchPackageNames, opaNames):
			if off {
				opaOff = slices.Equal(r.MatchRepositories, []string{"!compliance-framework/api"})
			} else {
				opaAPI = r.Automerge != nil && !*r.Automerge && r.GroupName != "" && slices.Contains(r.MatchRepositories, "compliance-framework/api")
			}
		}
	}
	if labelRules != 1 {
		t.Errorf("%d rules add %s, want 1", labelRules, AutomergeLabel)
	}
	if !internalOff || !grouped || !opaOff || !opaAPI || !indirectFix || !majorsCapped || !digestsPinned || !dockerUndated || !toolchainFix {
		t.Errorf("package rules: internal off %v, non-majors grouped and auto-merged %v, OPA off outside api %v, OPA own PR in api %v, indirect Go modules fix %v, majors needs-human and capped at 3 open / 1 an hour %v, Dockerfile digests pinned %v, undated docker updates not held %v, toolchain line fix(deps) %v",
			internalOff, grouped, opaOff, opaAPI, indirectFix, majorsCapped, digestsPinned, dockerUndated, toolchainFix)
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

// TestThrottle: a visit is one run on one repo, and Renovate counts a repo's PRs per clock hour, so
// per repo per visit it opens at most prHourlyLimit non-vulnerability PRs, of which at most the
// majors rule's prHourlyLimit are majors. A live run takes at most MaxLiveRepos repos, so it opens
// at most MaxLiveRepos x prHourlyLimit across the org, 60 with today's values: a worst case that
// needs every repo of the shard to have four non-vulnerability updates due at once (the majors'
// own caps make it rarer; docs/renovate.md#the-org-wide-throttle). Vulnerability fixes come on top
// and are deliberately not capped (TestSharedPreset).
func TestThrottle(t *testing.T) {
	data, err := os.ReadFile("../../renovate/default.json")
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		PrHourlyLimit int `json:"prHourlyLimit"`
		PackageRules  []struct {
			MatchUpdateTypes []string `json:"matchUpdateTypes"`
			PrHourlyLimit    *int     `json:"prHourlyLimit"`
		} `json:"packageRules"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if p.PrHourlyLimit != 4 {
		t.Errorf("prHourlyLimit = %d, want 4 non-vulnerability PRs per repo per visit", p.PrHourlyLimit)
	}
	majorsPerVisit := -1
	for _, r := range p.PackageRules {
		if slices.Equal(r.MatchUpdateTypes, []string{"major"}) && r.PrHourlyLimit != nil {
			majorsPerVisit = *r.PrHourlyLimit
		}
	}
	if majorsPerVisit != 1 {
		t.Errorf("the majors rule's prHourlyLimit = %d, want at most 1 major per repo per visit", majorsPerVisit)
	}
	if perRun := MaxLiveRepos * p.PrHourlyLimit; perRun > 60 {
		t.Errorf("MaxLiveRepos %d x prHourlyLimit %d = %d non-vulnerability PRs a run, want at most 60", MaxLiveRepos, p.PrHourlyLimit, perRun)
	}
}

func TestGlobalConfigOverrides(t *testing.T) {
	preset := []byte(`{"packageRules": [{"matchUpdateTypes": ["major"], "automerge": false}]}`)
	overrides, err := ParseOverrides([]byte(`[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": ["nginx"], "matchUpdateTypes": ["digest"], "minimumReleaseAge": null, "prHourlyLimit": 0}]`), "o", []string{"r"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := GlobalConfig(preset, Options{Owner: "o", Repos: []string{"r"}, DryRun: "full", Overrides: overrides})
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := decode(t, out)["packageRules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("packageRules = %v, want the preset's then the override", rules)
	}
	last, _ := rules[1].(map[string]any)
	if v, set := last["minimumReleaseAge"]; !set || v != nil || last["prHourlyLimit"] != float64(0) {
		t.Errorf("override = %v, want minimumReleaseAge null and prHourlyLimit 0 kept", last)
	}
	if !strings.Contains(string(out), `"prHourlyLimit": 0`) {
		t.Errorf("override number not kept as written:\n%s", out)
	}

	// A preset without packageRules gets the overrides alone; with none, nothing changes.
	out, err = GlobalConfig([]byte(`{}`), Options{Owner: "o", Repos: []string{"r"}, DryRun: "full", Overrides: overrides})
	if err != nil {
		t.Fatal(err)
	}
	if rules, _ := decode(t, out)["packageRules"].([]any); len(rules) != 1 {
		t.Errorf("packageRules = %v, want the override", rules)
	}
	out, err = GlobalConfig([]byte(`{}`), Options{Owner: "o", Repos: []string{"r"}, DryRun: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decode(t, out)["packageRules"]; ok {
		t.Errorf("no overrides added packageRules:\n%s", out)
	}
	if _, err := GlobalConfig([]byte(`{"packageRules": {}}`), Options{Owner: "o", Repos: []string{"r"}, DryRun: "full", Overrides: overrides}); err == nil {
		t.Error("packageRules not a list: want an error")
	}
}

func TestParseOverrides(t *testing.T) {
	const ok = `"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": ["nginx"]`
	rule := func(extra string) string { return `[{` + ok + `, ` + extra + `}]` }
	for _, tc := range []struct {
		name, data string
		n          int
		err        string
	}{
		{"empty", "", 0, ""},
		{"blank", " \n", 0, ""},
		{"empty list", "[]", 0, ""},
		{"rule 1, digest", rule(`"matchCurrentVersion": "/^(stable-alpine)$/", "matchUpdateTypes": ["digest"], "minimumReleaseAge": null, "prHourlyLimit": 0, "prConcurrentLimit": 0, "branchConcurrentLimit": 0, "semanticCommitType": "fix", "groupName": "g", "groupSlug": "s", "automerge": true, "addLabels": ["renovate:automerge"], "prPriority": 10, "prBodyNotes": ["n"]`), 1, ""},
		{"rule 1, patch", `[{` + ok + `, "matchCurrentVersion": "/^(1\\.26\\.1)$/", "separateMinorPatch": true}, {` + ok + `, "matchCurrentVersion": "/^(1\\.26\\.1)$/", "matchUpdateTypes": ["patch"], "matchNewValue": "/^(1\\.26\\.2)$/", "automerge": true}]`, 2, ""},
		{"rule 2", rule(`"matchCurrentVersion": "/^20(\\.|-|$)/", "allowedVersions": "/^22(\\.|-|$)/", "automerge": false, "addLabels": ["needs-human"]`), 1, ""},
		{"a registry port", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": ["localhost:5000/team/app"]}]`, 1, ""},
		{"not JSON", `[{`, 0, "overrides"},
		{"not a list", `{}`, 0, "overrides"},
		{"trailing data", `[] []`, 0, "trailing"},
		{"null rule", `[null]`, 0, "JSON object"},
		{"not an object", `[1]`, 0, "JSON object"},
		{"unknown key", rule(`"enabled": false`), 0, `"enabled" is not allowed`},
		{"extends", rule(`"extends": ["x"]`), 0, `"extends" is not allowed`},
		{"a key in another case", rule(`"AutoMerge": true`), 0, `"AutoMerge" is not allowed`},
		{"no repository", `[{"matchManagers": ["dockerfile"], "matchDepNames": ["nginx"]}]`, 0, "matchRepositories must list exactly one"},
		{"two repositories", `[{"matchRepositories": ["o/r", "o/s"], "matchManagers": ["dockerfile"], "matchDepNames": ["nginx"]}]`, 0, "exactly one"},
		{"repository not in the run", `[{"matchRepositories": ["o/s"], "matchManagers": ["dockerfile"], "matchDepNames": ["nginx"]}]`, 0, "not in this run"},
		{"other owner", `[{"matchRepositories": ["x/r"], "matchManagers": ["dockerfile"], "matchDepNames": ["nginx"]}]`, 0, "not in this run"},
		{"a pattern", `[{"matchRepositories": ["/.*/"], "matchManagers": ["dockerfile"], "matchDepNames": ["nginx"]}]`, 0, "not in this run"},
		{"repository not a string", `[{"matchRepositories": [1], "matchManagers": ["dockerfile"], "matchDepNames": ["nginx"]}]`, 0, "matchRepositories: want a []string"},
		{"other manager", `[{"matchRepositories": ["o/r"], "matchManagers": ["npm"], "matchDepNames": ["nginx"]}]`, 0, "matchManagers"},
		{"two managers", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile", "npm"], "matchDepNames": ["nginx"]}]`, 0, "matchManagers"},
		{"no deps", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": []}]`, 0, "matchDepNames is empty"},
		{"deps missing", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"]}]`, 0, "matchDepNames must list"},
		{"deps null", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": null}]`, 0, "matchDepNames is empty or null"},
		{"dep glob", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": ["*"]}]`, 0, "not an image name"},
		{"dep glob in a path", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": ["library/**"]}]`, 0, "not an image name"},
		{"dep regex", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": ["/.*/"]}]`, 0, "not an image name"},
		{"dep negation", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": ["!nginx"]}]`, 0, "not an image name"},
		{"dep with a tag", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": ["nginx:1"]}]`, 0, "not an image name"},
		{"dep with a digest", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": ["nginx@sha256:` + strings.Repeat("1", 64) + `"]}]`, 0, "not an image name"},
		{"dep not a string", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": [1]}]`, 0, "matchDepNames: want a []string"},
		{"automerge not a bool", rule(`"automerge": "true"`), 0, "automerge: want a *bool"},
		{"automerge null", rule(`"automerge": null`), 0, "automerge is empty or null"},
		{"limit a string", rule(`"prHourlyLimit": "0"`), 0, "prHourlyLimit: want a *int"},
		{"limit a fraction", rule(`"prConcurrentLimit": 0.5`), 0, "prConcurrentLimit: want a *int"},
		{"negative limit", rule(`"branchConcurrentLimit": -1`), 0, "branchConcurrentLimit is negative"},
		{"negative priority", rule(`"prPriority": -1`), 0, "prPriority is negative"},
		{"release age set", rule(`"minimumReleaseAge": "0 days"`), 0, "minimumReleaseAge must be null"},
		{"labels not strings", rule(`"addLabels": [true]`), 0, "addLabels: want a []string"},
		{"body notes a string", rule(`"prBodyNotes": "n"`), 0, "prBodyNotes: want a []string"},
		{"commit type a list", rule(`"semanticCommitType": ["fix"]`), 0, "semanticCommitType: want a string"},
		{"group name empty", rule(`"groupName": ""`), 0, "groupName is empty"},
		{"current version empty", rule(`"matchCurrentVersion": ""`), 0, "matchCurrentVersion is empty"},
		{"current version a glob", rule(`"matchCurrentVersion": "*"`), 0, "anchored at the start"},
		{"current version unanchored", rule(`"matchCurrentVersion": "/.*/"`), 0, "anchored at the start"},
		{"new value a glob", rule(`"matchUpdateTypes": ["patch"], "matchNewValue": "**"`), 0, "anchored at the start"},
		{"allowed versions a range", rule(`"allowedVersions": "<99"`), 0, "anchored at the start"},
		{"regex that doesn't compile", rule(`"allowedVersions": "/^(22/"`), 0, "anchored at the start"},
		{"major update type", rule(`"matchUpdateTypes": ["major"]`), 0, `"major" is not allowed`},
		{"update types empty", rule(`"matchUpdateTypes": []`), 0, "matchUpdateTypes is empty"},
		{"update types with allowedVersions", rule(`"matchUpdateTypes": ["patch"], "allowedVersions": "/^22/"`), 0, "can't be combined"},
		{"update types with separateMinorPatch", rule(`"matchUpdateTypes": ["patch"], "separateMinorPatch": true`), 0, "can't be combined"},
		{"automerge without update types", rule(`"automerge": true`), 0, "must list matchUpdateTypes"},
		{"automerge every docker dep", `[{"matchRepositories": ["o/r"], "matchManagers": ["dockerfile"], "matchDepNames": ["*"], "matchUpdateTypes": ["digest"], "automerge": true, "minimumReleaseAge": null}]`, 0, "not an image name"},
	} {
		rules, err := ParseOverrides([]byte(tc.data), "o", []string{"r"})
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.err)
			}
			continue
		}
		if err != nil || len(rules) != tc.n {
			t.Errorf("%s: %d rules, %v; want %d", tc.name, len(rules), err, tc.n)
		}
	}
}

// A parsed rule is written back as it was read: the same keys, null and zero values kept.
func TestParseOverridesRoundTrip(t *testing.T) {
	in := `[{"matchRepositories":["o/r"],"matchManagers":["dockerfile"],"matchDepNames":["nginx"],"matchCurrentVersion":"/^(1\\.26\\.1)$/","matchUpdateTypes":["patch"],"matchNewValue":"/^(1\\.26\\.2)$/","minimumReleaseAge":null,"prHourlyLimit":0,"prConcurrentLimit":0,"branchConcurrentLimit":0,"semanticCommitType":"fix","groupName":"g","groupSlug":"s","automerge":true,"addLabels":["l"],"prPriority":10,"prBodyNotes":["n"]},` +
		`{"matchRepositories":["o/r"],"matchManagers":["dockerfile"],"matchDepNames":["node"],"matchCurrentVersion":"/^20/","allowedVersions":"/^22/","separateMinorPatch":true,"automerge":false}]`
	rules, err := ParseOverrides([]byte(in), "o", []string{"r"})
	if err != nil {
		t.Fatal(err)
	}
	var want, got []map[string]any
	if err := json.Unmarshal([]byte(in), &want); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n got %s\nwant %s", out, in)
	}
}
