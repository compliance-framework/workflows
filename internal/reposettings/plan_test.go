package reposettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fakeRepo is one repo's state in fakeGitHub.
type fakeRepo struct {
	merge MergeSettings
	// hideMerge reads the merge settings as a token with Administration read does: all unknown.
	hideMerge       bool
	alerts, updates bool
	// hideUpdates reads Dependabot security updates as unknown.
	hideUpdates bool
	// config is the attached org code security configuration (nil: none).
	config   *SecurityConfiguration
	rulesets map[int64]Ruleset
}

// fakeGitHub keeps repo state in memory, records every call and applies writes to the state. Rulesets
// are stored as GitHub returns them: JSON round-tripped, with an unmanaged parameter added.
type fakeGitHub struct {
	repos         map[string]*fakeRepo
	installation  []string
	nextID        int64
	reads, writes []string
	// fail makes the write "op repo" (as recorded in writes) fail with this error.
	fail map[string]error
}

func (f *fakeGitHub) repo(op, repo string, write bool) (*fakeRepo, error) {
	if write {
		f.writes = append(f.writes, op+" "+repo)
		if err := f.fail[op+" "+repo]; err != nil {
			return nil, err
		}
	} else {
		f.reads = append(f.reads, op+" "+repo)
	}
	r, ok := f.repos[repo]
	if !ok {
		return nil, fmt.Errorf("%s: not found", repo)
	}
	return r, nil
}

func (f *fakeGitHub) InstallationRepos(context.Context) ([]string, error) {
	f.reads = append(f.reads, "installation")
	return f.installation, nil
}

func (f *fakeGitHub) MergeSettings(_ context.Context, repo string) (CurrentMergeSettings, error) {
	r, err := f.repo("merge", repo, false)
	if err != nil || r.hideMerge {
		return CurrentMergeSettings{}, err
	}
	var s CurrentMergeSettings
	b, _ := json.Marshal(r.merge)
	_ = json.Unmarshal(b, &s)
	return s, nil
}

func (f *fakeGitHub) VulnerabilityAlerts(_ context.Context, repo string) (bool, error) {
	r, err := f.repo("alerts", repo, false)
	return err == nil && r.alerts, err
}

func (f *fakeGitHub) SecurityUpdates(_ context.Context, repo string) (*bool, error) {
	r, err := f.repo("updates", repo, false)
	if err != nil || r.hideUpdates {
		return nil, err
	}
	on := r.updates
	return &on, nil
}

func (f *fakeGitHub) SecurityConfiguration(_ context.Context, repo string) (*SecurityConfiguration, error) {
	r, err := f.repo("config", repo, false)
	if err != nil {
		return nil, err
	}
	return r.config, nil
}

func (f *fakeGitHub) Rulesets(_ context.Context, repo string) (map[int64]Ruleset, error) {
	r, err := f.repo("rulesets", repo, false)
	if err != nil {
		return nil, err
	}
	out := map[int64]Ruleset{}
	for id, rs := range r.rulesets {
		out[id] = rs
	}
	return out, nil
}

func (f *fakeGitHub) UpdateMergeSettings(_ context.Context, repo string, s MergeSettings) error {
	r, err := f.repo("PATCH repo", repo, true)
	if err == nil {
		r.merge = s
	}
	return err
}

func (f *fakeGitHub) SetVulnerabilityAlerts(_ context.Context, repo string, on bool) error {
	r, err := f.repo(fmt.Sprintf("alerts=%t", on), repo, true)
	if err == nil {
		r.alerts = on
	}
	return err
}

func (f *fakeGitHub) SetSecurityUpdates(_ context.Context, repo string, on bool) error {
	r, err := f.repo(fmt.Sprintf("updates=%t", on), repo, true)
	if err == nil {
		r.updates = on
	}
	return err
}

func (f *fakeGitHub) CreateRuleset(_ context.Context, repo string, rs Ruleset) error {
	r, err := f.repo("create "+rs.Name, repo, true)
	if err == nil {
		f.nextID++
		r.rulesets[f.nextID] = asReturned(rs)
	}
	return err
}

func (f *fakeGitHub) UpdateRuleset(_ context.Context, repo string, id int64, rs Ruleset) error {
	r, err := f.repo(fmt.Sprintf("update %d %s", id, rs.Name), repo, true)
	if err == nil {
		r.rulesets[id] = asReturned(rs)
	}
	return err
}

// asReturned is rs as a GET returns it: numbers as float64 and a preview parameter added.
func asReturned(rs Ruleset) Ruleset {
	var out Ruleset
	b, _ := json.Marshal(rs)
	_ = json.Unmarshal(b, &out)
	for _, rule := range out.Rules {
		if rule.Type == "pull_request" {
			rule.Parameters["required_reviewers"] = []any{}
		}
	}
	return out
}

// githubDefaults is a new repo's state: every merge method, alerts off, security updates on, an
// old ccf-review ruleset without bypass and an unrelated ruleset.
func githubDefaults() *fakeRepo {
	return &fakeRepo{
		merge: MergeSettings{
			AllowSquashMerge: true, AllowMergeCommit: true, AllowRebaseMerge: true,
			SquashMergeCommitTitle: "COMMIT_OR_PR_TITLE", SquashMergeCommitMessage: "COMMIT_MESSAGES",
		},
		updates: true,
		rulesets: map[int64]Ruleset{
			7: asReturned(defaultBranchRuleset(ReviewRuleset, nil, Rule{Type: "pull_request", Parameters: pullRequestParams(0)})),
			8: asReturned(defaultBranchRuleset("other", nil, Rule{Type: "deletion"})),
		},
	}
}

func newFake(names ...string) *fakeGitHub {
	f := &fakeGitHub{repos: map[string]*fakeRepo{}, nextID: 100}
	for _, n := range names {
		f.repos["o/"+n] = githubDefaults()
		f.installation = append(f.installation, "o/"+n)
	}
	return f
}

func desired(t *testing.T) Desired {
	t.Helper()
	d, err := DesiredState(Config{RequiredCheck: DefaultRequiredCheck, RequiredCheckAppID: GitHubActionsAppID, BypassAppID: 42})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDesiredStateErrors(t *testing.T) {
	for _, c := range []Config{{BypassAppID: 42}, {RequiredCheck: "x"}, {RequiredCheck: "x", BypassAppID: 1, RequiredCheckAppID: -1}} {
		if _, err := DesiredState(c); err == nil {
			t.Errorf("DesiredState(%+v): want an error", c)
		}
	}
}

func TestPlanRepoDiff(t *testing.T) {
	f := newFake("a")
	review := f.repos["o/a"].rulesets[7]
	review.Rules = append(review.Rules, Rule{Type: "creation"}) // added by hand; the write drops it
	f.repos["o/a"].rulesets[7] = review
	p, err := PlanRepo(context.Background(), f, "o/a", desired(t))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range p.Changes {
		got = append(got, c.String())
	}
	for _, want := range []string{
		`repo.allow_merge_commit: true -> false`,
		`repo.allow_rebase_merge: true -> false`,
		`repo.squash_merge_commit_title: "COMMIT_OR_PR_TITLE" -> "PR_TITLE"`,
		`repo.squash_merge_commit_message: "COMMIT_MESSAGES" -> "PR_BODY"`,
		`repo.allow_auto_merge: false -> true`,
		`repo.delete_branch_on_merge: false -> true`,
		`security.vulnerability_alerts: disabled -> enabled`,
		`security.dependabot_security_updates: enabled -> disabled`,
		`ruleset[ccf-required]: missing -> create`,
		`ruleset[ccf-required].rules.required_status_checks.required_status_checks: (unset) -> [{"context":"ci / required","integration_id":15368}]`,
		`ruleset[ccf-required].rules.non_fast_forward: (unset) -> "on"`,
		`ruleset[ccf-review].bypass: "none" -> (unset)`,
		`ruleset[ccf-review].bypass.Integration:42: (unset) -> "always"`,
		`ruleset[ccf-review].rules.pull_request.required_approving_review_count: 0 -> 1`,
		`ruleset[ccf-review].rules.creation: "on" -> (unset)`,
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing change %s", want)
		}
	}
	for _, c := range got {
		if strings.Contains(c, "other") || strings.Contains(c, "required_reviewers") || strings.HasPrefix(c, "repo.allow_squash_merge") {
			t.Errorf("unexpected change %s", c)
		}
	}
	if len(f.writes) > 0 {
		t.Errorf("PlanRepo wrote: %v", f.writes)
	}
}

func TestPlanRepoDuplicateRuleset(t *testing.T) {
	f := newFake("a")
	f.repos["o/a"].rulesets[9] = f.repos["o/a"].rulesets[7]
	if _, err := PlanRepo(context.Background(), f, "o/a", desired(t)); err == nil || !strings.Contains(err.Error(), "more than one ruleset") {
		t.Fatalf("err = %v, want a duplicate ruleset error", err)
	}
}

func TestRunDryRunMakesNoWrites(t *testing.T) {
	f := newFake("a", "b")
	var out bytes.Buffer
	if err := Run(context.Background(), f, Options{Owner: "o", Repos: []string{"a", "b"}, Desired: desired(t), CheckTokenScope: true}, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) > 0 {
		t.Fatalf("dry run wrote: %v", f.writes)
	}
	if len(f.reads) == 0 {
		t.Fatal("dry run read nothing")
	}
	for _, want := range []string{"o/a: 28 change(s)", "o/b: 28 change(s)", "  repo.allow_merge_commit: true -> false", "56 change(s) in 2 of 2 repo(s) (dry run"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRunApplyIsIdempotent(t *testing.T) {
	f := newFake("a")
	opts := Options{Owner: "o", Repos: []string{"a"}, Desired: desired(t), Apply: true}
	if err := Run(context.Background(), f, opts, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"PATCH repo o/a", "create ccf-required o/a", "update 7 ccf-review o/a", "alerts=true o/a", "updates=false o/a"}
	if !slices.Equal(f.writes, want) {
		t.Fatalf("writes = %v, want %v", f.writes, want)
	}
	if _, ok := f.repos["o/a"].rulesets[8]; !ok {
		t.Error("the unrelated ruleset was removed")
	}

	f.writes = nil
	var out bytes.Buffer
	if err := Run(context.Background(), f, opts, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) > 0 {
		t.Errorf("second apply wrote: %v", f.writes)
	}
	if !strings.Contains(out.String(), "o/a: up to date") {
		t.Errorf("second apply output:\n%s", out.String())
	}
}

func TestRunTokenScope(t *testing.T) {
	f := newFake("a", "b")
	err := Run(context.Background(), f, Options{Owner: "o", Repos: []string{"a"}, Desired: desired(t), CheckTokenScope: true}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "o/b") {
		t.Fatalf("err = %v, want the extra repo o/b named", err)
	}
	if !slices.Equal(f.reads, []string{"installation"}) {
		t.Errorf("reads = %v, want only the installation check", f.reads)
	}
}

func TestRunContinuesAfterRepoError(t *testing.T) {
	f := newFake("a")
	var out bytes.Buffer
	err := Run(context.Background(), f, Options{Owner: "o", Repos: []string{"missing", "a"}, Desired: desired(t), Apply: true}, &out)
	if err == nil || !strings.Contains(err.Error(), "o/missing") {
		t.Fatalf("err = %v, want o/missing named", err)
	}
	if !strings.Contains(out.String(), "o/a: applied") {
		t.Errorf("o/a was not applied:\n%s", out.String())
	}
}

func changeStrings(cs []Change) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.String()
	}
	return out
}

func TestPlanRepoUnknownMergeSettings(t *testing.T) {
	visible, hidden := newFake("a"), newFake("a")
	hidden.repos["o/a"].hideMerge = true
	pv, err := PlanRepo(context.Background(), visible, "o/a", desired(t))
	if err != nil {
		t.Fatal(err)
	}
	ph, err := PlanRepo(context.Background(), hidden, "o/a", desired(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"repo.allow_auto_merge: unknown (not readable with Administration read) -> true",
		"repo.allow_merge_commit: unknown (not readable with Administration read) -> false",
		"repo.allow_rebase_merge: unknown (not readable with Administration read) -> false",
		"repo.allow_squash_merge: unknown (not readable with Administration read) -> true",
		"repo.delete_branch_on_merge: unknown (not readable with Administration read) -> true",
		`repo.squash_merge_commit_message: unknown (not readable with Administration read) -> "PR_BODY"`,
		`repo.squash_merge_commit_title: unknown (not readable with Administration read) -> "PR_TITLE"`,
	}
	if got := changeStrings(ph.Unknown); !slices.Equal(got, want) {
		t.Errorf("unknown = %v, want %v", got, want)
	}
	if len(pv.Unknown) > 0 {
		t.Errorf("readable settings reported unknown: %v", pv.Unknown)
	}
	// Unknowns are not changes; the security and ruleset changes are the same either way.
	var rest []string
	for _, c := range changeStrings(pv.Changes) {
		if !strings.HasPrefix(c, "repo.") {
			rest = append(rest, c)
		}
	}
	if len(rest) == 0 {
		t.Fatal("the fixture produces no security or ruleset changes to compare")
	}
	if got := changeStrings(ph.Changes); !slices.Equal(got, rest) {
		t.Errorf("changes with hidden merge settings = %v, want %v", got, rest)
	}
	if ph.merge == nil || *ph.merge != desired(t).Merge {
		t.Errorf("merge write = %v, want the full desired merge settings", ph.merge)
	}
}

func TestRunApplyWritesUnknownMergeSettings(t *testing.T) {
	f := newFake("a")
	r := f.repos["o/a"]
	r.merge, r.hideMerge = desired(t).Merge, true // already right, but the token can't see it

	var out bytes.Buffer
	opts := Options{Owner: "o", Repos: []string{"a"}, Desired: desired(t)}
	if err := Run(context.Background(), f, opts, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"o/a: 22 change(s), 7 unknown", "22 change(s), 7 unknown in 1 of 1 repo(s) (dry run", unknownHint} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	opts.Apply = true
	if err := Run(context.Background(), f, opts, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) == 0 || f.writes[0] != "PATCH repo o/a" {
		t.Fatalf("writes = %v, want the merge settings patched first", f.writes)
	}

	// Still hidden: everything else is up to date, the merge settings stay unknown and are written again.
	f.writes, out = nil, bytes.Buffer{}
	if err := Run(context.Background(), f, opts, &out); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.writes, []string{"PATCH repo o/a"}) || !strings.Contains(out.String(), "o/a: 0 change(s), 7 unknown") {
		t.Errorf("writes = %v, output:\n%s", f.writes, out.String())
	}

	// Readable (a token with Administration write): up to date, no writes.
	r.hideMerge = false
	f.writes, out = nil, bytes.Buffer{}
	if err := Run(context.Background(), f, opts, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) > 0 || !strings.Contains(out.String(), "o/a: up to date") || strings.Contains(out.String(), "unknown") {
		t.Errorf("writes = %v, output:\n%s", f.writes, out.String())
	}
}

// baseline is the org configuration on the CCF repos: alerts on, security updates off, enforced.
func baseline() *SecurityConfiguration {
	return &SecurityConfiguration{Name: "Baseline Security Profile", Enforced: true, DependabotAlerts: "enabled", DependabotSecurityUpdates: "disabled"}
}

func TestPlanRepoSecurityUpdatesRead(t *testing.T) {
	f := newFake("a")
	r := f.repos["o/a"]
	r.updates = false // read as {"enabled":false}: already desired
	p, err := PlanRepo(context.Background(), f, "o/a", desired(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range slices.Concat(p.Changes, p.Unknown) {
		if strings.HasPrefix(c.Key, "security.dependabot_security_updates") {
			t.Errorf("unexpected %s", c)
		}
	}
	if p.securityUpdates != nil {
		t.Error("plans a security updates write for a setting already off")
	}

	r.hideUpdates = true // unreadable: unknown, never assumed on
	p, err = PlanRepo(context.Background(), f, "o/a", desired(t))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := changeStrings(p.Unknown), []string{"security.dependabot_security_updates: unknown (not readable) -> disabled"}; !slices.Equal(got, want) {
		t.Errorf("unknown = %v, want %v", got, want)
	}
	if slices.ContainsFunc(p.Changes, func(c Change) bool { return c.Key == "security.dependabot_security_updates" }) {
		t.Error("an unknown setting is reported as a change")
	}
	if p.securityUpdates == nil || *p.securityUpdates {
		t.Errorf("security updates write = %v, want disabled", p.securityUpdates)
	}
}

func TestRunOrgConfigMatchingIsSkipped(t *testing.T) {
	f := newFake("a")
	r := f.repos["o/a"]
	r.config = baseline()
	r.updates = true // what the API reports doesn't matter: the org configuration owns it
	var out bytes.Buffer
	if err := Run(context.Background(), f, Options{Owner: "o", Repos: []string{"a"}, Desired: desired(t), Apply: true}, &out); err != nil {
		t.Fatal(err)
	}
	for _, w := range f.writes {
		if strings.HasPrefix(w, "alerts=") || strings.HasPrefix(w, "updates=") {
			t.Errorf("wrote an org-managed setting: %s", w)
		}
	}
	if slices.Contains(f.reads, "updates o/a") || slices.Contains(f.reads, "alerts o/a") {
		t.Errorf("read an org-managed setting: %v", f.reads)
	}
	for _, want := range []string{
		`  security.dependabot_security_updates: managed by org configuration "Baseline Security Profile" (disabled)`,
		`  security.vulnerability_alerts: managed by org configuration "Baseline Security Profile" (enabled)`,
		"o/a: applied 3 step(s)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "warning") {
		t.Errorf("a matching configuration is reported as a warning:\n%s", out.String())
	}

	f.writes, out = nil, bytes.Buffer{}
	if err := Run(context.Background(), f, Options{Owner: "o", Repos: []string{"a"}, Desired: desired(t), Apply: true}, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) > 0 || !strings.Contains(out.String(), "o/a: up to date") {
		t.Errorf("second apply: writes = %v, output:\n%s", f.writes, out.String())
	}
}

func TestRunOrgConfigDifferingWarns(t *testing.T) {
	f := newFake("a")
	r := f.repos["o/a"]
	r.config = baseline()
	r.config.DependabotSecurityUpdates = "enabled"
	r.alerts, r.updates = true, true
	opts := Options{Owner: "o", Repos: []string{"a"}, Desired: desired(t), Apply: true}
	var out bytes.Buffer
	if err := Run(context.Background(), f, opts, &out); err != nil {
		t.Fatalf("a differing org configuration fails the run: %v", err)
	}
	if slices.ContainsFunc(f.writes, func(w string) bool { return strings.HasPrefix(w, "updates=") }) {
		t.Errorf("wrote the org-enforced setting: %v", f.writes)
	}
	for _, want := range []string{
		`  security.dependabot_security_updates: managed by org configuration "Baseline Security Profile": wants disabled, org enforces enabled — change it in the org configuration`,
		", 1 warning(s) in 1 of 1 repo(s) (applied)",
		warningHint,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	// Nothing else to do: the warning stays, nothing is written.
	f.writes, out = nil, bytes.Buffer{}
	if err := Run(context.Background(), f, opts, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) > 0 || !strings.Contains(out.String(), "o/a: 0 change(s), 1 warning(s)") {
		t.Errorf("writes = %v, output:\n%s", f.writes, out.String())
	}
}

func TestRunApplyContinuesAfterFailedStep(t *testing.T) {
	f := newFake("a", "b")
	refused := errors.New("422 An enforced security configuration prevented modifying dependabot security updates enablement")
	f.fail = map[string]error{"updates=false o/a": refused}
	var out bytes.Buffer
	err := Run(context.Background(), f, Options{Owner: "o", Repos: []string{"a", "b"}, Desired: desired(t), Apply: true}, &out)
	if !errors.Is(err, refused) || !strings.Contains(err.Error(), "o/a: apply: security.dependabot_security_updates") {
		t.Fatalf("err = %v, want the refused step named", err)
	}
	if strings.Contains(err.Error(), "o/b") {
		t.Errorf("o/b failed: %v", err)
	}
	for _, w := range []string{"PATCH repo o/a", "create ccf-required o/a", "update 7 ccf-review o/a", "alerts=true o/a", "updates=false o/b"} {
		if !slices.Contains(f.writes, w) {
			t.Errorf("missing write %q after the failed step: %v", w, f.writes)
		}
	}
	for _, want := range []string{
		"o/a: security.dependabot_security_updates failed: 422",
		"o/a: partly applied: 1 of 5 step(s) failed",
		"o/b: applied 5 step(s)",
		"(applied; steps failed in 1 repo(s))",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if _, ok := f.repos["o/a"].rulesets[101]; !ok {
		t.Error("ccf-required was not created on o/a")
	}
}
