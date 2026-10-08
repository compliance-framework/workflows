package reposettings

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// Reader reads a repo's current settings. repo is "owner/name".
type Reader interface {
	// InstallationRepos lists the "owner/name" repos the token can reach.
	InstallationRepos(ctx context.Context) ([]string, error)
	// MergeSettings returns the merge settings; a nil field is one the token can't read.
	MergeSettings(ctx context.Context, repo string) (CurrentMergeSettings, error)
	VulnerabilityAlerts(ctx context.Context, repo string) (bool, error)
	// SecurityUpdates reports whether Dependabot security updates are on; nil is unknown.
	SecurityUpdates(ctx context.Context, repo string) (*bool, error)
	// SecurityConfiguration returns the org code security configuration attached to the repo, or
	// nil if there is none or the token can't read it.
	SecurityConfiguration(ctx context.Context, repo string) (*SecurityConfiguration, error)
	// Rulesets returns the repo's own rulesets (not inherited ones) by ID; nil bypass actors are
	// ones the token can't read.
	Rulesets(ctx context.Context, repo string) (map[int64]CurrentRuleset, error)
	WorkflowPermissions(ctx context.Context, repo string) (WorkflowPermissions, error)
	// ForkPRApproval returns the fork PR contributor approval policy, or "" if the repo has none
	// (a private repo).
	ForkPRApproval(ctx context.Context, repo string) (string, error)
	DefaultBranch(ctx context.Context, repo string) (string, error)
	// Environment returns the named environment, or nil if the repo has none by that name.
	Environment(ctx context.Context, repo, name string) (*CurrentEnvironment, error)
	// DeploymentPolicies lists an environment's branch and tag policies; it has some only with
	// custom policies (CurrentEnvironment.Custom).
	DeploymentPolicies(ctx context.Context, repo, env string) ([]DeploymentPolicy, error)
	// EnvironmentSecrets lists the names of an environment's secrets (never their values).
	EnvironmentSecrets(ctx context.Context, repo, env string) ([]string, error)
}

// CurrentEnvironment is an environment as GET /repos/{owner}/{repo}/environments/{name} returns
// it. A nil DeploymentBranchPolicy lets every branch use it.
type CurrentEnvironment struct {
	DeploymentBranchPolicy *struct {
		ProtectedBranches    bool `json:"protected_branches"`
		CustomBranchPolicies bool `json:"custom_branch_policies"`
	} `json:"deployment_branch_policy"`
}

// Custom reports whether only the environment's branch and tag policies may use it.
func (e *CurrentEnvironment) Custom() bool {
	return e.DeploymentBranchPolicy != nil && e.DeploymentBranchPolicy.CustomBranchPolicies
}

// policyMode describes the environment's deployment branch policy.
func (e *CurrentEnvironment) policyMode() string {
	switch {
	case e.DeploymentBranchPolicy == nil:
		return "all branches"
	case e.DeploymentBranchPolicy.ProtectedBranches:
		return "protected branches"
	default:
		return "custom"
	}
}

// SecurityConfiguration is the part of an org code security configuration this tool checks. Each
// setting is "enabled", "disabled" or "not_set".
type SecurityConfiguration struct {
	Name string
	// Enforced configurations stop repos changing the settings they set; GitHub refuses the write
	// with a 422.
	Enforced                                    bool
	DependabotAlerts, DependabotSecurityUpdates string
}

// Writer changes a repo's settings. Only Apply calls it.
type Writer interface {
	UpdateMergeSettings(ctx context.Context, repo string, s MergeSettings) error
	SetVulnerabilityAlerts(ctx context.Context, repo string, enabled bool) error
	SetSecurityUpdates(ctx context.Context, repo string, enabled bool) error
	CreateRuleset(ctx context.Context, repo string, r Ruleset) error
	UpdateRuleset(ctx context.Context, repo string, id int64, r Ruleset) error
	SetWorkflowPermissions(ctx context.Context, repo string, p WorkflowPermissions) error
	SetForkPRApproval(ctx context.Context, repo, policy string) error
	// PutEnvironment creates the environment, or switches it, to custom branch and tag policies
	// (none yet: no ref may use it until AddDeploymentPolicy).
	PutEnvironment(ctx context.Context, repo, name string) error
	AddDeploymentPolicy(ctx context.Context, repo, env string, p DeploymentPolicy) error
	DeleteDeploymentPolicy(ctx context.Context, repo, env string, id int64) error
	SetEnvironmentSecret(ctx context.Context, repo, env, name, value string) error
}

// Client reads and writes repo settings.
type Client interface {
	Reader
	Writer
}

// Change is one setting whose current value differs from the desired one.
type Change struct {
	Key, Current, Desired string
}

func (c Change) String() string { return fmt.Sprintf("%s: %s -> %s", c.Key, c.Current, c.Desired) }

// unset stands for a value the repo doesn't have.
const unset = "(unset)"

// unknown stands for a merge setting or ruleset bypass list the token can't read.
const unknown = "unknown (not readable with Administration read)"

// unknownSecurity stands for a security setting the token can't read.
const unknownSecurity = "unknown (not readable)"

// unknownHint explains unknown settings once, under the totals.
const unknownHint = "unknown: GitHub hides merge settings and ruleset bypass actors from a token with Administration read; apply writes the full desired merge settings, each ruleset whose bypass actors are unknown (and any unknown security setting)"

// warningHint explains warnings once, under the totals.
const warningHint = "warning: an enforced org code security configuration sets a different value; change it in the org configuration (this tool can't)"

// Plan is what bringing one repo to the desired state takes.
type Plan struct {
	Repo    string
	Changes []Change
	// Unknown are settings the token can't read (merge settings, ruleset bypass actors, Dependabot
	// security updates), so whether they differ isn't known. They are not counted as changes, but Apply writes them.
	Unknown []Change
	// Managed are settings left alone: security settings an enforced org code security
	// configuration already sets to the desired value, and settings the repo doesn't have.
	Managed []string
	// Warnings are security settings an enforced org code security configuration sets to another
	// value. They are skipped, not failed: only the org configuration can change them.
	Warnings []string

	merge               *MergeSettings
	vulnerabilityAlerts *bool
	securityUpdates     *bool
	workflowPermissions *WorkflowPermissions
	forkPRApproval      *string
	rulesets            []rulesetWrite
	environments        []environmentWrite
}

// environmentWrite is what Apply does to one environment, in this order: put it (create, or
// switch to custom policies), delete and add policies, then write the secrets, so a secret never
// lands in an environment that more refs may use than the desired ones.
type environmentWrite struct {
	name        string
	put         bool
	delete, add []DeploymentPolicy
	secrets     []string
}

type rulesetWrite struct {
	id      int64 // 0 creates the ruleset
	ruleset Ruleset
}

// PlanRepo reads repo's current settings and compares them with d. It only reads.
func PlanRepo(ctx context.Context, r Reader, repo string, d Desired) (*Plan, error) {
	p := &Plan{Repo: repo}
	merge, err := r.MergeSettings(ctx, repo)
	if err != nil {
		return nil, err
	}
	// Compare only the fields the token could read; the others are unknown.
	cur, want := toMap(merge), toMap(d.Merge)
	readable, hidden := map[string]any{}, map[string]any{}
	for k, v := range want {
		if _, ok := cur[k]; ok {
			readable[k] = v
		} else {
			hidden[k] = v
		}
	}
	// diff treats an empty map as one leaf, so skip it for an empty side.
	if len(readable) > 0 {
		p.Changes = append(p.Changes, diff("repo", cur, readable)...)
	}
	if len(hidden) > 0 {
		for _, c := range diff("repo", nil, hidden) {
			c.Current = unknown
			p.Unknown = append(p.Unknown, c)
		}
	}
	if len(p.Changes) > 0 || len(p.Unknown) > 0 {
		// The PATCH sends every managed merge field, so it is the same whether some or all of
		// them differ or are unknown, and repeating it is harmless.
		p.merge = &d.Merge
	}

	cfg, err := r.SecurityConfiguration(ctx, repo)
	if err != nil {
		return nil, err
	}
	if !p.orgManaged("security.vulnerability_alerts", cfg, cfg.dependabotAlerts(), d.VulnerabilityAlerts) {
		alerts, err := r.VulnerabilityAlerts(ctx, repo)
		if err != nil {
			return nil, err
		}
		p.vulnerabilityAlerts = p.compareSecurity("security.vulnerability_alerts", &alerts, d.VulnerabilityAlerts)
	}
	if !p.orgManaged("security.dependabot_security_updates", cfg, cfg.dependabotSecurityUpdates(), d.SecurityUpdates) {
		updates, err := r.SecurityUpdates(ctx, repo)
		if err != nil {
			return nil, err
		}
		p.securityUpdates = p.compareSecurity("security.dependabot_security_updates", updates, d.SecurityUpdates)
	}

	if err := p.planActions(ctx, r, d.Actions); err != nil {
		return nil, err
	}
	if err := p.planEnvironments(ctx, r, d.Environments); err != nil {
		return nil, err
	}

	current, err := r.Rulesets(ctx, repo)
	if err != nil {
		return nil, err
	}
	byName := map[string]int64{}
	for _, id := range slices.Sorted(maps.Keys(current)) {
		name := current[id].Name
		if _, dup := byName[name]; dup {
			return nil, fmt.Errorf("%s: more than one ruleset is named %q", repo, name)
		}
		byName[name] = id
	}
	for _, want := range d.Rulesets {
		prefix := "ruleset[" + want.Name + "]"
		id, ok := byName[want.Name]
		if !ok {
			p.Changes = append(p.Changes, Change{Key: prefix, Current: "missing", Desired: "create"})
			p.Changes = append(p.Changes, diff(prefix, nil, rulesetView(want))...)
			p.rulesets = append(p.rulesets, rulesetWrite{ruleset: want})
			continue
		}
		cur, bypassKnown := current[id].known()
		curView, wantView := rulesetView(cur), rulesetView(want)
		if !bypassKnown {
			// Compare the rest; the PUT sends the full desired ruleset, so writing it is harmless.
			p.Unknown = append(p.Unknown, Change{Key: prefix + ".bypass", Current: unknown, Desired: jsonString(wantView["bypass"])})
			delete(curView, "bypass")
			delete(wantView, "bypass")
		}
		c := diff(prefix, curView, wantView)
		p.Changes = append(p.Changes, c...)
		if len(c) > 0 || !bypassKnown {
			p.rulesets = append(p.rulesets, rulesetWrite{id: id, ruleset: want})
		}
	}
	return p, nil
}

// planActions compares the Actions settings with want.
func (p *Plan) planActions(ctx context.Context, r Reader, want ActionsSettings) error {
	perms, err := r.WorkflowPermissions(ctx, p.Repo)
	if err != nil {
		return err
	}
	if perms.CanApprovePullRequestReviews != want.CanApprovePullRequestReviews {
		p.Changes = append(p.Changes, Change{Key: "actions.can_approve_pull_request_reviews",
			Current: fmt.Sprint(perms.CanApprovePullRequestReviews), Desired: fmt.Sprint(want.CanApprovePullRequestReviews)})
		perms.CanApprovePullRequestReviews = want.CanApprovePullRequestReviews
		p.workflowPermissions = &perms // keeps the default permissions as they are
	}
	if want.ForkPRApproval == "" {
		return nil
	}
	policy, err := r.ForkPRApproval(ctx, p.Repo)
	switch {
	case err != nil:
		return err
	case policy == "":
		p.Managed = append(p.Managed, "actions.fork_pr_approval: the repo has no such setting (private); skipped")
	case policy != want.ForkPRApproval:
		p.Changes = append(p.Changes, Change{Key: "actions.fork_pr_approval", Current: policy, Desired: want.ForkPRApproval})
		p.forkPRApproval = &want.ForkPRApproval
	}
	return nil
}

// planEnvironments compares the environments with want.
func (p *Plan) planEnvironments(ctx context.Context, r Reader, want []Environment) error {
	if len(want) == 0 {
		return nil
	}
	branch, err := r.DefaultBranch(ctx, p.Repo)
	if err != nil {
		return err
	}
	for _, e := range want {
		prefix := "environment[" + e.Name + "]"
		w := environmentWrite{name: e.Name}
		var policies []DeploymentPolicy
		for _, b := range e.Branches {
			if b == DefaultBranchPattern {
				b = branch
			}
			policies = append(policies, DeploymentPolicy{Name: b, Type: "branch"})
		}
		for _, t := range e.Tags {
			policies = append(policies, DeploymentPolicy{Name: t, Type: "tag"})
		}

		cur, err := r.Environment(ctx, p.Repo, e.Name)
		if err != nil {
			return err
		}
		var current []DeploymentPolicy
		var secrets []string
		switch {
		case cur == nil:
			p.Changes = append(p.Changes, Change{Key: prefix, Current: "missing", Desired: "create"})
			w.put = true
		case !cur.Custom():
			p.Changes = append(p.Changes, Change{Key: prefix + ".deployment_branch_policy", Current: cur.policyMode(), Desired: "custom"})
			w.put = true
			fallthrough
		default:
			if cur.Custom() {
				if current, err = r.DeploymentPolicies(ctx, p.Repo, e.Name); err != nil {
					return err
				}
			}
			if secrets, err = r.EnvironmentSecrets(ctx, p.Repo, e.Name); err != nil {
				return err
			}
		}

		for _, c := range current {
			if c.Type == "" {
				c.Type = "branch" // policies made before tag policies existed have no type
			}
			if !slices.ContainsFunc(policies, func(d DeploymentPolicy) bool { return d.Name == c.Name && d.Type == c.Type }) {
				p.Changes = append(p.Changes, Change{Key: prefix + ".policy[" + c.String() + "]", Current: "present", Desired: "delete"})
				w.delete = append(w.delete, c)
			}
		}
		for _, d := range policies {
			if !slices.ContainsFunc(current, func(c DeploymentPolicy) bool { return c.Name == d.Name && cmp.Or(c.Type, "branch") == d.Type }) {
				p.Changes = append(p.Changes, Change{Key: prefix + ".policy[" + d.String() + "]", Current: unset, Desired: "add"})
				w.add = append(w.add, d)
			}
		}
		for _, s := range e.Secrets {
			switch {
			case !slices.Contains(secrets, s):
				p.Changes = append(p.Changes, Change{Key: prefix + ".secret." + s, Current: "missing", Desired: "set"})
			case e.RotateSecrets:
				p.Changes = append(p.Changes, Change{Key: prefix + ".secret." + s, Current: "present", Desired: "rotate"})
			default:
				continue
			}
			w.secrets = append(w.secrets, s)
		}
		if w.put || len(w.delete) > 0 || len(w.add) > 0 || len(w.secrets) > 0 {
			p.environments = append(p.environments, w)
		}
	}
	return nil
}

// stateName names a bool security setting as GitHub does.
var stateName = map[bool]string{true: "enabled", false: "disabled"}

// orgManaged reports whether an enforced org configuration sets key (to orgValue, "enabled" or
// "disabled"), and records it as managed or, if orgValue isn't the desired value, as a warning.
func (p *Plan) orgManaged(key string, cfg *SecurityConfiguration, orgValue string, want bool) bool {
	if cfg == nil || !cfg.Enforced || (orgValue != "enabled" && orgValue != "disabled") {
		return false
	}
	if orgValue == stateName[want] {
		p.Managed = append(p.Managed, fmt.Sprintf("%s: managed by org configuration %q (%s)", key, cfg.Name, orgValue))
	} else {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s: managed by org configuration %q: wants %s, org enforces %s — change it in the org configuration",
			key, cfg.Name, stateName[want], orgValue))
	}
	return true
}

// compareSecurity records key as a change if current differs from want, or as unknown if current
// is nil, and returns the value Apply writes (nil: none).
func (p *Plan) compareSecurity(key string, current *bool, want bool) *bool {
	switch {
	case current == nil:
		p.Unknown = append(p.Unknown, Change{Key: key, Current: unknownSecurity, Desired: stateName[want]})
	case *current != want:
		p.Changes = append(p.Changes, Change{Key: key, Current: stateName[*current], Desired: stateName[want]})
	default:
		return nil
	}
	return &want
}

func (c *SecurityConfiguration) dependabotAlerts() string {
	if c == nil {
		return ""
	}
	return c.DependabotAlerts
}

func (c *SecurityConfiguration) dependabotSecurityUpdates() string {
	if c == nil {
		return ""
	}
	return c.DependabotSecurityUpdates
}

// StepResult is the outcome of one Apply step; Err is nil if the step succeeded.
type StepResult struct {
	Step string
	Err  error
}

// Apply writes the changes in p, one step per setting: the merge settings, each ruleset, the
// Actions settings, each environment, then the security settings, which an org configuration can
// refuse. Steps are independent: a failed step doesn't stop the others, except that an
// environment's secrets are skipped if its policies failed. secrets holds the secrets' values;
// they are never printed. Apply returns every step's result, in order.
func Apply(ctx context.Context, w Writer, p *Plan, secrets map[string]string) []StepResult {
	var results []StepResult
	step := func(name string, err error) { results = append(results, StepResult{Step: name, Err: err}) }
	if p.merge != nil {
		step("repo merge settings", w.UpdateMergeSettings(ctx, p.Repo, *p.merge))
	}
	for _, rw := range p.rulesets {
		name := "ruleset[" + rw.ruleset.Name + "]"
		if rw.id == 0 {
			step(name, w.CreateRuleset(ctx, p.Repo, rw.ruleset))
		} else {
			step(name, w.UpdateRuleset(ctx, p.Repo, rw.id, rw.ruleset))
		}
	}
	if p.workflowPermissions != nil {
		step("actions.workflow_permissions", w.SetWorkflowPermissions(ctx, p.Repo, *p.workflowPermissions))
	}
	if p.forkPRApproval != nil {
		step("actions.fork_pr_approval", w.SetForkPRApproval(ctx, p.Repo, *p.forkPRApproval))
	}
	for _, e := range p.environments {
		prefix := "environment[" + e.name + "]"
		ok := true
		policyStep := func(name string, err error) {
			step(name, err)
			ok = ok && err == nil
		}
		if e.put {
			policyStep(prefix, w.PutEnvironment(ctx, p.Repo, e.name))
		}
		for _, d := range e.delete {
			policyStep(prefix+".policy["+d.String()+"] delete", w.DeleteDeploymentPolicy(ctx, p.Repo, e.name, d.ID))
		}
		for _, d := range e.add {
			policyStep(prefix+".policy["+d.String()+"]", w.AddDeploymentPolicy(ctx, p.Repo, e.name, d))
		}
		for _, s := range e.secrets {
			name := prefix + ".secret." + s
			switch v := secrets[s]; {
			case !ok:
				step(name, errors.New("skipped: the environment's deployment policies are not in place"))
			case v == "":
				step(name, fmt.Errorf("no value for %s: set it in the job's environment (README: Repo settings)", s))
			default:
				step(name, w.SetEnvironmentSecret(ctx, p.Repo, e.name, s, v))
			}
		}
	}
	if p.vulnerabilityAlerts != nil {
		step("security.vulnerability_alerts", w.SetVulnerabilityAlerts(ctx, p.Repo, *p.vulnerabilityAlerts))
	}
	if p.securityUpdates != nil {
		step("security.dependabot_security_updates", w.SetSecurityUpdates(ctx, p.Repo, *p.securityUpdates))
	}
	return results
}

// Options configure Run.
type Options struct {
	Owner string
	// Repos are the repo names (without owner) to process.
	Repos   []string
	Desired Desired
	// Apply writes the changes; otherwise Run only reads (dry run).
	Apply bool
	// CheckTokenScope fails before reading any repo if the token reaches a repo outside Repos.
	CheckTokenScope bool
	// Secrets are the environment secrets' values, by name; Apply writes the missing ones (all of
	// them with Environment.RotateSecrets). A dry run needs none.
	Secrets map[string]string
}

// Run prints each repo's diff to out and, with o.Apply, writes the changes. A failure on one repo,
// or on one step of a repo's apply, doesn't stop the others; Run returns every error.
func Run(ctx context.Context, gh Client, o Options, out io.Writer) error {
	if len(o.Repos) == 0 {
		return errors.New("no repos to process")
	}
	full := make([]string, len(o.Repos))
	for i, name := range o.Repos {
		full[i] = o.Owner + "/" + name
	}
	if o.CheckTokenScope {
		reachable, err := gh.InstallationRepos(ctx)
		if err != nil {
			return fmt.Errorf("listing the token's repos: %w", err)
		}
		var extra []string
		for _, r := range reachable {
			if !slices.ContainsFunc(full, func(f string) bool { return strings.EqualFold(f, r) }) {
				extra = append(extra, r)
			}
		}
		if len(extra) > 0 {
			return fmt.Errorf("the token reaches repos it should not: %s; scope it to the processed repos", strings.Join(extra, ", "))
		}
	}

	var errs []error
	total, unknowns, warnings, changed, failedRepos := 0, 0, 0, 0, 0
	for _, repo := range full {
		p, err := PlanRepo(ctx, gh, repo, o.Desired)
		if err != nil {
			fmt.Fprintf(out, "%s: error: %v\n", repo, err)
			errs = append(errs, fmt.Errorf("%s: %w", repo, err))
			continue
		}
		total += len(p.Changes)
		unknowns += len(p.Unknown)
		warnings += len(p.Warnings)
		if len(p.Changes) == 0 && len(p.Unknown) == 0 && len(p.Warnings) == 0 {
			fmt.Fprintf(out, "%s: up to date\n", repo)
		} else {
			fmt.Fprintf(out, "%s: %d change(s)%s%s\n", repo, len(p.Changes), unknownCount(len(p.Unknown)), warningCount(len(p.Warnings)))
		}
		for _, c := range slices.Concat(p.Changes, p.Unknown) {
			fmt.Fprintf(out, "  %s\n", c)
		}
		for _, line := range slices.Concat(p.Warnings, p.Managed) {
			fmt.Fprintf(out, "  %s\n", line)
		}
		if len(p.Changes) == 0 && len(p.Unknown) == 0 {
			continue
		}
		changed++
		if !o.Apply {
			continue
		}
		var failed []error
		results := Apply(ctx, gh, p, o.Secrets)
		for _, r := range results {
			if r.Err != nil {
				fmt.Fprintf(out, "%s: %s failed: %v\n", repo, r.Step, r.Err)
				failed = append(failed, fmt.Errorf("%s: %w", r.Step, r.Err))
			}
		}
		if len(failed) > 0 {
			failedRepos++
			fmt.Fprintf(out, "%s: partly applied: %d of %d step(s) failed\n", repo, len(failed), len(results))
			errs = append(errs, fmt.Errorf("%s: apply: %w", repo, errors.Join(failed...)))
			continue
		}
		fmt.Fprintf(out, "%s: applied %d step(s)\n", repo, len(results))
	}
	mode := "dry run, nothing written; re-run with apply to write"
	if o.Apply {
		mode = "applied"
		if failedRepos > 0 {
			mode = fmt.Sprintf("applied; steps failed in %d repo(s)", failedRepos)
		}
	}
	fmt.Fprintf(out, "%d change(s)%s%s in %d of %d repo(s) (%s)\n", total, unknownCount(unknowns), warningCount(warnings), changed, len(full), mode)
	if unknowns > 0 {
		fmt.Fprintln(out, unknownHint)
	}
	if warnings > 0 {
		fmt.Fprintln(out, warningHint)
	}
	return errors.Join(errs...)
}

// unknownCount is ", N unknown", or "" for none.
func unknownCount(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", %d unknown", n)
}

// warningCount is ", N warning(s)", or "" for none.
func warningCount(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", %d warning(s)", n)
}

// diff flattens current and desired into dotted keys and returns the keys whose values differ,
// sorted by key. A nil current means every desired key is unset.
func diff(prefix string, current, desired map[string]any) []Change {
	cur, want := map[string]string{}, map[string]string{}
	flatten(prefix, current, cur)
	flatten(prefix, desired, want)
	union := maps.Clone(want)
	maps.Copy(union, cur)
	keys := slices.Sorted(maps.Keys(union))
	var changes []Change
	for _, k := range keys {
		c, ok1 := cur[k]
		w, ok2 := want[k]
		if !ok1 {
			c = unset
		}
		if !ok2 {
			w = unset
		}
		if c != w {
			changes = append(changes, Change{Key: k, Current: c, Desired: w})
		}
	}
	return changes
}

// flatten writes v's leaves to out as JSON, keyed by their dotted path. Arrays and empty maps
// are leaves; a nil map has none.
func flatten(prefix string, v any, out map[string]string) {
	if m, ok := v.(map[string]any); ok && (m == nil || len(m) > 0) {
		for k, x := range m {
			flatten(prefix+"."+k, x, out)
		}
		return
	}
	out[prefix] = jsonString(v)
}

// jsonString is v as JSON, or as %v if it doesn't marshal.
func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// rulesetView is r as a map that diffs well: rules keyed by type with only managed parameters,
// bypass actors keyed by "type:id".
func rulesetView(r Ruleset) map[string]any {
	rules := map[string]any{}
	for _, rule := range r.Rules {
		params := map[string]any{}
		for k, v := range rule.Parameters {
			if isManaged(rule.Type, k) {
				params[k] = v
			}
		}
		if len(params) == 0 {
			rules[rule.Type] = "on"
			continue
		}
		rules[rule.Type] = toAny(params)
	}
	actors := map[string]any{}
	for _, a := range r.BypassActors {
		actors[fmt.Sprintf("%s:%d", a.ActorType, a.ActorID)] = a.BypassMode
	}
	return map[string]any{
		"target":      r.Target,
		"enforcement": r.Enforcement,
		"include":     r.Conditions.RefName.Include,
		"exclude":     r.Conditions.RefName.Exclude,
		"bypass":      actorsOrNone(actors),
		"rules":       rules,
	}
}

func actorsOrNone(actors map[string]any) any {
	if len(actors) == 0 {
		return "none"
	}
	return actors
}

// toMap converts a struct to a JSON-shaped map.
func toMap(v any) map[string]any {
	m, _ := toAny(v).(map[string]any)
	return m
}

// toAny round-trips v through JSON, so desired values and API responses compare alike (numbers
// become float64, nil slices stay null).
func toAny(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return string(b)
	}
	return out
}
