// Package renovate builds the self-hosted Renovate global configuration that renovate.yml runs
// with: the shared preset (renovate/default.json) plus the repository list from a manifest.
package renovate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// DryRunOff runs Renovate live: it pushes branches and opens and merges PRs.
const DryRunOff = "off"

// dryRunModes are the values Renovate's dryRun option takes, plus DryRunOff.
var dryRunModes = []string{"full", "lookup", "extract", DryRunOff}

// Options select the repos and the mode of one run.
type Options struct {
	// Owner is the repos' owner, e.g. "compliance-framework".
	Owner string
	// Repos are the repo names, without the owner.
	Repos []string
	// DryRun is "full", "lookup", "extract" or DryRunOff.
	DryRun string
	// Overrides are package rules appended after the preset's (ParseOverrides).
	Overrides []PackageRule
}

// runnerKeys are the settings GlobalConfig owns. The preset must not set them: they decide which
// repos the token reaches and whether anything is written.
var runnerKeys = []string{"platform", "autodiscover", "repositories", "onboarding", "requireConfig", "dryRun", "statusCheckWhen"}

// AutomergeLabel marks the PRs the preset auto-merges (the non-major group and non-major
// vulnerability fixes), which `renovate-config merge` merges after each live run, so a repo gets
// more than Renovate's one merge per run (docs/renovate.md#the-merge-pass).
const AutomergeLabel = "renovate:automerge"

// MaxLiveRepos is the most repos one live run works on (Shard). With the preset's prHourlyLimit of 4
// (non-vulnerability PRs per repo per visit, at most one of them a major), it bounds what one
// renovate.yml run opens across the org at MaxLiveRepos x 4; vulnerability fixes are not capped
// (docs/renovate.md#the-org-wide-throttle). It also keeps a live run within the app token's hour.
const MaxLiveRepos = 15

// CheckDryRun returns an error unless mode is one of Renovate's dryRun values or DryRunOff.
func CheckDryRun(mode string) error {
	if !slices.Contains(dryRunModes, mode) {
		return fmt.Errorf("dry run %q: want one of %s", mode, strings.Join(dryRunModes, ", "))
	}
	return nil
}

// SelectRepos returns the repos named in list (comma- or space-separated), or every active repo in
// the manifest (the workflows repo included; manifest.ActiveRepos) if list is empty, in manifest
// order. Named repos must be in the manifest. They must also be active unless dryRun is a dry run
// (not DryRunOff): a batch is checked with a dry run before it is switched on.
func SelectRepos(m *manifest.Manifest, list, dryRun string) ([]string, error) {
	if err := CheckDryRun(dryRun); err != nil {
		return nil, err
	}
	names := strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
	repos, err := m.Select(names, dryRun != DryRunOff)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range repos {
		out = append(out, r.Name)
	}
	if len(out) == 0 {
		return nil, errors.New("no repos selected")
	}
	return out, nil
}

// Shard splits repos into the fewest shards of at most size repos (repo i goes to shard i mod n) and
// returns shard slot mod n, its index and n. renovate.yml passes its run number as slot, so
// consecutive runs take the shards in turn and each repo is visited every n runs, however late or
// far apart GitHub starts them.
// size <= 0 returns every repo as one shard.
func Shard(repos []string, size int, slot int64) (shard []string, index, count int) {
	if size <= 0 || len(repos) <= size {
		return repos, 0, 1
	}
	count = (len(repos) + size - 1) / size
	index = int(((slot % int64(count)) + int64(count)) % int64(count))
	for i, r := range repos {
		if i%count == index {
			shard = append(shard, r)
		}
	}
	return shard, index, count
}

// GlobalConfig returns the Renovate global config: every setting of preset (a JSON object), plus
// the settings the runner owns. Renovate applies the global config to every listed repo as its
// base; a repo's own renovate.json only overrides it.
func GlobalConfig(preset []byte, opts Options) ([]byte, error) {
	if opts.Owner == "" {
		return nil, errors.New("no owner")
	}
	if len(opts.Repos) == 0 {
		return nil, errors.New("no repos")
	}
	if err := CheckDryRun(opts.DryRun); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(preset))
	dec.UseNumber() // keep the preset's numbers as written
	var cfg map[string]any
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("preset: %w", err)
	}
	if cfg == nil {
		return nil, errors.New("preset: want a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("preset: trailing data after the JSON object")
	}
	for _, k := range runnerKeys {
		if _, ok := cfg[k]; ok {
			return nil, fmt.Errorf("preset sets %q, which the runner owns", k)
		}
	}

	if len(opts.Overrides) > 0 {
		rules, ok := cfg["packageRules"].([]any)
		if _, set := cfg["packageRules"]; set && !ok {
			return nil, errors.New("preset: packageRules is not a list")
		}
		// Later rules win in Renovate, so the overrides take precedence over the preset's.
		for _, r := range opts.Overrides {
			rules = append(rules, r)
		}
		cfg["packageRules"] = rules
	}

	repos := make([]string, len(opts.Repos))
	for i, r := range opts.Repos {
		repos[i] = opts.Owner + "/" + r
	}
	cfg["platform"] = "github"
	// Only the listed repos, never every repo the app is installed on (all org repos).
	cfg["autodiscover"] = false
	cfg["repositories"] = repos
	// No onboarding PRs: the preset applies to a repo with no renovate.json of its own.
	cfg["onboarding"] = false
	cfg["requireConfig"] = "optional"
	// ccf-release-bot has no Commit statuses permission, so Renovate must not set its
	// renovate/stability-days status. internalChecksFilter "strict" already keeps a branch from
	// being created before the update is old enough, so the status would add nothing.
	cfg["statusCheckWhen"] = map[string]any{"minimumReleaseAge": "never"}
	if opts.DryRun != DryRunOff {
		cfg["dryRun"] = opts.DryRun
	}

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// PackageRule is a Renovate package rule image-scan adds for one repo (internal/imagescan plans
// it, docs/image-scan.md#remediation). Its JSON keys are the only keys an override may set: the
// planner builds this type and ParseOverrides decodes into it, so the two can't drift apart. A
// field left zero is not written; the pointers write a 0 or false that must reach Renovate.
type PackageRule struct {
	MatchRepositories   []string `json:"matchRepositories"`
	MatchManagers       []string `json:"matchManagers"`
	MatchDepNames       []string `json:"matchDepNames"`
	MatchCurrentVersion string   `json:"matchCurrentVersion,omitempty"`
	MatchUpdateTypes    []string `json:"matchUpdateTypes,omitempty"`
	MatchNewValue       string   `json:"matchNewValue,omitempty"`
	AllowedVersions     string   `json:"allowedVersions,omitempty"`
	SeparateMinorPatch  *bool    `json:"separateMinorPatch,omitempty"`
	// MinimumReleaseAge is only ever null, Renovate's "no waiting period" (NoReleaseAge).
	MinimumReleaseAge     json.RawMessage `json:"minimumReleaseAge,omitempty"`
	PRHourlyLimit         *int            `json:"prHourlyLimit,omitempty"`
	PRConcurrentLimit     *int            `json:"prConcurrentLimit,omitempty"`
	BranchConcurrentLimit *int            `json:"branchConcurrentLimit,omitempty"`
	SemanticCommitType    string          `json:"semanticCommitType,omitempty"`
	GroupName             string          `json:"groupName,omitempty"`
	GroupSlug             string          `json:"groupSlug,omitempty"`
	Automerge             *bool           `json:"automerge,omitempty"`
	AddLabels             []string        `json:"addLabels,omitempty"`
	PRPriority            *int            `json:"prPriority,omitempty"`
	PRBodyNotes           []string        `json:"prBodyNotes,omitempty"`
}

// NoReleaseAge is PackageRule.MinimumReleaseAge's one value: null.
var NoReleaseAge = json.RawMessage("null")

// overrideUpdateTypes are the update types an override may fast-track: the ones image-scan
// verifies (a rebuild of the same tag, the newest patch of an X.Y.Z tag).
var overrideUpdateTypes = []string{"digest", "patch"}

// overrideKeys are PackageRule's JSON keys, by field index.
var overrideKeys = func() map[string]int {
	keys := map[string]int{}
	t := reflect.TypeFor[PackageRule]()
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		keys[name] = i
	}
	return keys
}()

// ImageName is Docker's grammar of an image's repository name, without tag or digest
// (distribution/reference): a name read from a repo's file can't start with "-" and be read as a
// docker flag, and an override's matchDepNames can't be a glob or a regex.
var ImageName = regexp.MustCompile(`^[A-Za-z0-9]+(?:[._-][A-Za-z0-9]+)*(?:\.[A-Za-z0-9-]+)*(?::[0-9]+)?(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)

// ParseOverrides reads the package rules image-scan writes (a JSON list) for a run on owner's
// repos. The file is generated, so it is checked strictly, keys and values: every rule may set
// only PackageRule's keys, each with a value of its type that isn't empty or null (except
// minimumReleaseAge, which must be null); it must name exactly one repository in
// matchRepositories that is one of repos, match only the dockerfile manager, and name at least
// one image by its plain name (no tag, digest, glob or regex). Limits and prPriority are not
// negative, the version matchers are regexes anchored at the start ("/^.../"), matchUpdateTypes
// only lists digest or patch, and a rule that auto-merges must list its update types (so it never
// reaches a major or a minor). Empty data is no overrides.
func ParseOverrides(data []byte, owner string, repos []string) ([]PackageRule, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var raws []json.RawMessage
	if err := dec.Decode(&raws); err != nil {
		return nil, fmt.Errorf("overrides: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("overrides: trailing data after the JSON list")
	}
	out := make([]PackageRule, 0, len(raws))
	for i, raw := range raws {
		r, err := parseOverride(raw, owner, repos)
		if err != nil {
			return nil, fmt.Errorf("overrides: rule %d: %w", i, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func parseOverride(raw json.RawMessage, owner string, repos []string) (PackageRule, error) {
	var r PackageRule
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return r, errors.New("want a JSON object")
	}
	v := reflect.ValueOf(&r).Elem()
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		i, ok := overrideKeys[k]
		if !ok {
			return r, fmt.Errorf("key %q is not allowed (want only %s)", k, strings.Join(slices.Sorted(maps.Keys(overrideKeys)), ", "))
		}
		f := v.Field(i)
		if err := json.Unmarshal(fields[k], f.Addr().Interface()); err != nil {
			return r, fmt.Errorf("%s: want a %s", k, f.Type())
		}
		// Left zero, the key would not be written, and the rule would be wider than the file says.
		if f.IsZero() || (f.Kind() == reflect.Slice && f.Len() == 0) {
			return r, fmt.Errorf("%s is empty or null", k)
		}
	}

	repo, err := single(r.MatchRepositories, "matchRepositories")
	if err != nil {
		return r, err
	}
	name, ok := strings.CutPrefix(repo, owner+"/")
	if !ok || !slices.Contains(repos, name) {
		return r, fmt.Errorf("repository %q is not in this run (%s/%s)", repo, owner, strings.Join(repos, ","))
	}
	if m, err := single(r.MatchManagers, "matchManagers"); err != nil || m != "dockerfile" {
		return r, errors.New(`matchManagers must be ["dockerfile"]`)
	}
	if len(r.MatchDepNames) == 0 {
		return r, errors.New("matchDepNames must list the images")
	}
	for _, d := range r.MatchDepNames {
		if !IsImageName(d) {
			return r, fmt.Errorf("matchDepNames: %q is not an image name (no tag, digest, glob or regex)", d)
		}
	}
	for k, n := range map[string]*int{"prHourlyLimit": r.PRHourlyLimit, "prConcurrentLimit": r.PRConcurrentLimit, "branchConcurrentLimit": r.BranchConcurrentLimit, "prPriority": r.PRPriority} {
		if n != nil && *n < 0 {
			return r, fmt.Errorf("%s is negative", k)
		}
	}
	if r.MinimumReleaseAge != nil && string(r.MinimumReleaseAge) != "null" {
		return r, errors.New("minimumReleaseAge must be null")
	}
	for k, s := range map[string]string{"matchCurrentVersion": r.MatchCurrentVersion, "matchNewValue": r.MatchNewValue, "allowedVersions": r.AllowedVersions} {
		if s != "" && !isAnchoredRegex(s) {
			return r, fmt.Errorf("%s %q: want a regex anchored at the start, /^.../", k, s)
		}
	}
	for _, u := range r.MatchUpdateTypes {
		if !slices.Contains(overrideUpdateTypes, u) {
			return r, fmt.Errorf("matchUpdateTypes: %q is not allowed (want only %s)", u, strings.Join(overrideUpdateTypes, ", "))
		}
	}
	// Renovate refuses both in one rule: allowedVersions and separateMinorPatch act before the
	// update type is known.
	if len(r.MatchUpdateTypes) > 0 && (r.AllowedVersions != "" || r.SeparateMinorPatch != nil) {
		return r, errors.New("matchUpdateTypes can't be combined with allowedVersions or separateMinorPatch")
	}
	if r.Automerge != nil && *r.Automerge && len(r.MatchUpdateTypes) == 0 {
		return r, errors.New("a rule that auto-merges must list matchUpdateTypes")
	}
	return r, nil
}

// IsImageName reports whether s is an image's repository name alone (ImageName, no tag).
func IsImageName(s string) bool {
	// A colon after the last slash starts a tag; before it, a registry port.
	if strings.LastIndexByte(s, ':') > strings.LastIndexByte(s, '/') {
		return false
	}
	return ImageName.MatchString(s)
}

// isAnchoredRegex reports whether s is a Renovate regex matcher, /^.../, that compiles.
func isAnchoredRegex(s string) bool {
	if len(s) < 3 || !strings.HasPrefix(s, "/^") || !strings.HasSuffix(s, "/") {
		return false
	}
	_, err := regexp.Compile(s[1 : len(s)-1])
	return err == nil
}

// single returns the one entry of list.
func single(list []string, key string) (string, error) {
	if len(list) != 1 {
		return "", fmt.Errorf("%s must list exactly one entry", key)
	}
	return list[0], nil
}
