package reposettings

import (
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
	MergeSettings(ctx context.Context, repo string) (MergeSettings, error)
	VulnerabilityAlerts(ctx context.Context, repo string) (bool, error)
	SecurityUpdates(ctx context.Context, repo string) (bool, error)
	// Rulesets returns the repo's own rulesets (not inherited ones) by ID.
	Rulesets(ctx context.Context, repo string) (map[int64]Ruleset, error)
}

// Writer changes a repo's settings. Only Apply calls it.
type Writer interface {
	UpdateMergeSettings(ctx context.Context, repo string, s MergeSettings) error
	SetVulnerabilityAlerts(ctx context.Context, repo string, enabled bool) error
	SetSecurityUpdates(ctx context.Context, repo string, enabled bool) error
	CreateRuleset(ctx context.Context, repo string, r Ruleset) error
	UpdateRuleset(ctx context.Context, repo string, id int64, r Ruleset) error
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

// Plan is what bringing one repo to the desired state takes.
type Plan struct {
	Repo    string
	Changes []Change

	merge               *MergeSettings
	vulnerabilityAlerts *bool
	securityUpdates     *bool
	rulesets            []rulesetWrite
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
	if c := diff("repo", toMap(merge), toMap(d.Merge)); len(c) > 0 {
		p.Changes = append(p.Changes, c...)
		p.merge = &d.Merge
	}

	alerts, err := r.VulnerabilityAlerts(ctx, repo)
	if err != nil {
		return nil, err
	}
	if alerts != d.VulnerabilityAlerts {
		p.Changes = append(p.Changes, boolChange("security.vulnerability_alerts", alerts))
		p.vulnerabilityAlerts = &d.VulnerabilityAlerts
	}
	updates, err := r.SecurityUpdates(ctx, repo)
	if err != nil {
		return nil, err
	}
	if updates != d.SecurityUpdates {
		p.Changes = append(p.Changes, boolChange("security.dependabot_security_updates", updates))
		p.securityUpdates = &d.SecurityUpdates
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
		if c := diff(prefix, rulesetView(current[id]), rulesetView(want)); len(c) > 0 {
			p.Changes = append(p.Changes, c...)
			p.rulesets = append(p.rulesets, rulesetWrite{id: id, ruleset: want})
		}
	}
	return p, nil
}

func boolChange(key string, current bool) Change {
	on := map[bool]string{true: "enabled", false: "disabled"}
	return Change{Key: key, Current: on[current], Desired: on[!current]}
}

// Apply writes the changes in p.
func Apply(ctx context.Context, w Writer, p *Plan) error {
	if p.merge != nil {
		if err := w.UpdateMergeSettings(ctx, p.Repo, *p.merge); err != nil {
			return err
		}
	}
	if p.vulnerabilityAlerts != nil {
		if err := w.SetVulnerabilityAlerts(ctx, p.Repo, *p.vulnerabilityAlerts); err != nil {
			return err
		}
	}
	if p.securityUpdates != nil {
		if err := w.SetSecurityUpdates(ctx, p.Repo, *p.securityUpdates); err != nil {
			return err
		}
	}
	for _, rw := range p.rulesets {
		var err error
		if rw.id == 0 {
			err = w.CreateRuleset(ctx, p.Repo, rw.ruleset)
		} else {
			err = w.UpdateRuleset(ctx, p.Repo, rw.id, rw.ruleset)
		}
		if err != nil {
			return err
		}
	}
	return nil
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
}

// Run prints each repo's diff to out and, with o.Apply, writes the changes. A failure on one repo
// doesn't stop the others; Run returns every error.
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
	total, changed := 0, 0
	for _, repo := range full {
		p, err := PlanRepo(ctx, gh, repo, o.Desired)
		if err != nil {
			fmt.Fprintf(out, "%s: error: %v\n", repo, err)
			errs = append(errs, fmt.Errorf("%s: %w", repo, err))
			continue
		}
		if len(p.Changes) == 0 {
			fmt.Fprintf(out, "%s: up to date\n", repo)
			continue
		}
		total += len(p.Changes)
		changed++
		fmt.Fprintf(out, "%s: %d change(s)\n", repo, len(p.Changes))
		for _, c := range p.Changes {
			fmt.Fprintf(out, "  %s\n", c)
		}
		if !o.Apply {
			continue
		}
		if err := Apply(ctx, gh, p); err != nil {
			fmt.Fprintf(out, "%s: apply failed: %v\n", repo, err)
			errs = append(errs, fmt.Errorf("%s: apply: %w", repo, err))
			continue
		}
		fmt.Fprintf(out, "%s: applied\n", repo)
	}
	mode := "dry run, nothing written; re-run with apply to write"
	if o.Apply {
		mode = "applied"
	}
	fmt.Fprintf(out, "%d change(s) in %d of %d repo(s) (%s)\n", total, changed, len(full), mode)
	return errors.Join(errs...)
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
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(fmt.Sprintf("%v", v))
	}
	out[prefix] = string(b)
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
