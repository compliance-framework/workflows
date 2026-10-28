package imagescan

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/compliance-framework/workflows/internal/renovate"
)

// Rule is a Renovate package rule the plan adds for one repo. renovate.ParseOverrides decodes the
// same type, so the plan can only write keys the check allows.
type Rule = renovate.PackageRule

// Group names of the rules: they mirror the preset's vulnerabilityAlerts grouping
// ("security fixes (<manager>)", "security-<manager>").
const (
	SecurityGroupName = "security fixes (dockerfile)"
	SecurityGroupSlug = "security-dockerfile"
)

// ImageScanner scans an image (*Scanner, behind a cache).
type ImageScanner interface {
	Scan(ctx context.Context, ref string) (*Scan, error)
}

// Cycler lists a product's release cycles (*EOL).
type Cycler interface {
	Cycles(ctx context.Context, product string) ([]Cycle, error)
}

// Target is a base image of one of a repo's Dockerfiles and its scan.
type Target struct {
	Dockerfile string
	Base       Base
	Scan       *Scan
	Err        error
}

// Planner turns a repo's base image scans into Renovate rules (docs/image-scan.md#remediation):
//
//  1. A final-stage base image with actionable findings, whose newer candidate (the tag's
//     current digest; for an X.Y.Z tag the newest X.Y patch) scans clean of them: a rule that lets
//     Renovate open that update, and only that one, at once, grouped as a security fix, and
//     auto-merge it.
//  2. A base image of any stage on an end-of-life release (node:20) with actionable findings: a
//     rule that opens the lowest supported release whose current image scans clean of them, for a
//     human.
//  3. Anything else actionable is logged as a note: the distribution has the fix, the upstream
//     image hasn't been rebuilt with it yet.
//
// Go standard library findings are left out of all three (docs/image-scan.md#go-standard-library).
type Planner struct {
	Owner    string
	Scanner  ImageScanner
	Registry Registry
	EOL      Cycler
	cycles   map[string][]Cycle
}

// Planned is a repo's plan.
type Planned struct {
	Rules []Rule   // rule 1 by image, then rule 2
	Notes []string // what each image got, or why it got nothing
	// Fixed holds the base images (Ref.String()) rule 1 moves to a newer image that fixes their
	// findings, which the OS package patches leave alone.
	Fixed map[string]bool
}

// Plan returns repo's plan.
func (p *Planner) Plan(ctx context.Context, repo string, targets []Target) Planned {
	var notes []string
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	type fix struct {
		dep      string
		kind     string // the candidate's update type: "digest" or "patch"
		findings []Finding
		images   []string
		tags     []string
		cands    []string // the candidates
		newTags  []string // the candidates' tags, for "patch"
	}
	security := map[string]*fix{} // by dep and kind
	fixed := map[string]bool{}
	var majors []Rule
	majorKeys := map[string]bool{}
	seen := map[string]bool{}
	for _, t := range targets {
		if t.Scan == nil {
			continue
		}
		key := t.Base.Ref.String() + " " + string(t.Base.Role)
		if seen[key] {
			continue
		}
		seen[key] = true
		ref := t.Base.Ref
		where := fmt.Sprintf("%s (%s, stage %s)", ref, t.Dockerfile, t.Base.Stage)
		act := actionable(t.Scan.Findings)
		if std := slices.DeleteFunc(slices.Clone(act), func(f Finding) bool { return !f.GoStdlib() }); len(std) > 0 {
			note("%s: %d actionable Go standard library finding(s), in the Go binaries of the image, left out of the plan: a newer Go fixes them, not a base image update (the go.mod toolchain line does for CCF's own binaries)", where, len(std))
			act = slices.DeleteFunc(act, Finding.GoStdlib)
		}
		if len(act) == 0 {
			continue
		}
		if strings.HasPrefix(ref.Name, "ghcr.io/"+p.Owner+"/") {
			note("%s: %d actionable finding(s) in an internal image, which ccf-bump moves, not Renovate", where, len(act))
			continue
		}
		if rule, msg, eol := p.eolRule(ctx, repo, ref, act); eol {
			// One rule per image and release, however many stages use it.
			if rule != nil {
				if key := fmt.Sprint(rule.MatchDepNames, rule.MatchCurrentVersion); !majorKeys[key] {
					majorKeys[key] = true
					majors = append(majors, *rule)
				}
			}
			note("%s: %s", where, msg)
			continue
		} else if msg != "" {
			note("%s: %s", where, msg)
		}
		if t.Base.Role != RoleFinal {
			note("%s: %d actionable finding(s) in a build stage, which the image doesn't ship; left to Renovate's normal updates", where, len(act))
			continue
		}
		cand, kind, why, err := p.candidate(ctx, ref)
		if err != nil {
			note("%s: %d actionable finding(s); no candidate: %v", where, len(act), err)
			continue
		}
		if kind == "" {
			note("%s: %d actionable finding(s) (%s) and %s; the distribution's fix waits for an upstream rebuild", where, len(act), strings.Join(cves(act), ", "), why)
			continue
		}
		cs, err := p.Scanner.Scan(ctx, cand.String())
		if err != nil {
			note("%s: scan of the candidate %s failed: %v", where, cand, err)
			continue
		}
		if left := remaining(act, cs.Findings); len(left) > 0 {
			note("%s: the candidate %s still has %s; the distribution's fix waits for an upstream rebuild", where, cand, strings.Join(cves(left), ", "))
			continue
		}
		f := security[ref.Name+"\x00"+kind]
		if f == nil {
			f = &fix{dep: ref.Name, kind: kind}
			security[ref.Name+"\x00"+kind] = f
		}
		f.findings = append(f.findings, act...)
		f.images = append(f.images, ref.String())
		f.tags = append(f.tags, ref.Tag)
		f.cands = append(f.cands, cand.String())
		if kind == "patch" {
			f.newTags = append(f.newTags, cand.Tag)
		}
		fixed[ref.String()] = true
		note("%s: %s fixes %s (rule 1)", where, cand, strings.Join(cves(act), ", "))
	}

	var rules []Rule
	for _, key := range slices.Sorted(maps.Keys(security)) {
		f := security[key]
		match := Rule{
			MatchRepositories: []string{p.Owner + "/" + repo},
			MatchManagers:     []string{"dockerfile"},
			MatchDepNames:     []string{f.dep},
			// Only the tags scanned: another image of the same name (a build stage on an
			// end-of-life release) keeps its own rules.
			MatchCurrentVersion: tagsRegex(f.tags),
		}
		header := fmt.Sprintf("this update of %s fixes", codeList(f.images))
		// Only the update scanned is fast-tracked; Renovate's other updates of the image (a
		// major, a newer minor) keep the preset's rules.
		rule := match
		rule.MatchUpdateTypes = []string{f.kind}
		if f.kind == "patch" {
			// Without it, Renovate folds the patch into its newest minor (one non-major update).
			// It must be a rule of its own: Renovate refuses separateMinorPatch next to
			// matchUpdateTypes.
			split := match
			split.SeparateMinorPatch = new(true)
			rules = append(rules, split)
			rule.MatchNewValue = tagsRegex(f.newTags)
			header = fmt.Sprintf("this update of %s to %s fixes", codeList(f.images), codeList(f.cands))
		}
		rule.MinimumReleaseAge = renovate.NoReleaseAge
		rule.PRHourlyLimit, rule.PRConcurrentLimit, rule.BranchConcurrentLimit = new(0), new(0), new(0)
		rule.SemanticCommitType = "fix"
		rule.GroupName, rule.GroupSlug = SecurityGroupName, SecurityGroupSlug
		rule.Automerge = new(true)
		rule.AddLabels = []string{renovate.AutomergeLabel}
		rule.PRPriority = new(10)
		rule.PRBodyNotes = []string{bodyNote(header, f.findings)}
		rules = append(rules, rule)
	}
	return Planned{Rules: append(rules, majors...), Notes: notes, Fixed: fixed}
}

// eolRule applies rule 2 to an image of an eolLines product. eol is false when the image's release
// isn't known to be end-of-life (msg then says why, if it couldn't be checked); otherwise rule is
// the rule opening the lowest clean supported release, or nil with msg saying why there is none.
func (p *Planner) eolRule(ctx context.Context, repo string, ref Ref, act []Finding) (rule *Rule, msg string, eol bool) {
	line, ok := lineOf(ref.Name)
	if !ok {
		return nil, "", false
	}
	v, ok := parseTagVersion(ref.Tag)
	if !ok || len(v.parts) < line.Parts {
		return nil, "", false // a codename tag (bookworm-slim): Renovate has no release to move it to
	}
	current := v.prefix(line.Parts)
	cycles, err := p.cyclesOf(ctx, line.Product)
	if err != nil {
		// Fail soft: without endoflife.date the image goes through the other rules.
		return nil, fmt.Sprintf("endoflife.date %s unavailable, EOL rule skipped: %v", line.Product, err), false
	}
	i := slices.IndexFunc(cycles, func(c Cycle) bool { return c.Name == current })
	if i < 0 || !cycles[i].EOL {
		return nil, "", false
	}
	// A runtime image's packages are fixed by a rebuild of the same release (rules 1 and 3); what
	// a newer release fixes is the runtime itself. An OS image's release is everything in it.
	trigger := act
	if slices.ContainsFunc(Runtimes, func(rt Runtime) bool { return rt.Name == line.Image }) {
		trigger = slices.DeleteFunc(slices.Clone(act), func(f Finding) bool { return f.Type != TypeBinary })
	}
	if len(trigger) == 0 {
		return nil, "", false
	}
	var supported []Cycle
	for _, c := range cycles {
		if !c.EOL && slices.Compare(numbers(c.Name), numbers(current)) > 0 {
			supported = append(supported, c)
		}
	}
	slices.SortFunc(supported, func(a, b Cycle) int { return slices.Compare(numbers(a.Name), numbers(b.Name)) })
	var tried []string
	for _, c := range supported {
		cand := ref.WithTag(c.Name + v.suffix).String()
		cs, err := p.Scanner.Scan(ctx, cand)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s (scan failed: %v)", cand, err))
			continue
		}
		if left := remaining(trigger, cs.Findings); len(left) > 0 {
			tried = append(tried, fmt.Sprintf("%s (still has %s)", cand, strings.Join(cves(left), ", ")))
			continue
		}
		rule = &Rule{
			MatchRepositories:   []string{p.Owner + "/" + repo},
			MatchManagers:       []string{"dockerfile"},
			MatchDepNames:       []string{ref.Name},
			MatchCurrentVersion: cycleRegex(current),
			AllowedVersions:     cycleRegex(c.Name),
			PRHourlyLimit:       new(0),
			PRConcurrentLimit:   new(0),
			Automerge:           new(false),
			AddLabels:           []string{"needs-human"},
			PRBodyNotes:         []string{bodyNote(fmt.Sprintf("`%s` is on %s %s, which is end-of-life; this update to %s %s fixes", ref, line.Product, current, line.Product, c.Name), trigger)},
		}
		return rule, fmt.Sprintf("%s %s is end-of-life; %s fixes %s (rule 2)", line.Product, current, cand, strings.Join(cves(trigger), ", ")), true
	}
	if len(tried) == 0 {
		return nil, fmt.Sprintf("%s %s is end-of-life and no newer release is supported", line.Product, current), true
	}
	return nil, fmt.Sprintf("%s %s is end-of-life and no supported release fixes %s: %s", line.Product, current, strings.Join(cves(trigger), ", "), strings.Join(tried, "; ")), true
}

func (p *Planner) cyclesOf(ctx context.Context, product string) ([]Cycle, error) {
	if c, ok := p.cycles[product]; ok {
		return c, nil
	}
	c, err := p.EOL.Cycles(ctx, product)
	if err != nil {
		return nil, err
	}
	if p.cycles == nil {
		p.cycles = map[string][]Cycle{}
	}
	p.cycles[product] = c
	return c, nil
}

// candidate returns the newer image of ref that rule 1 can fast-track, and its Renovate update
// type: for an X.Y.Z tag the newest X.Y patch with the same suffix ("patch"), else the tag's
// current digest when ref pins an older one ("digest"). A zero Ref (with why) when there is none.
func (p *Planner) candidate(ctx context.Context, ref Ref) (cand Ref, kind, why string, err error) {
	if ref.Tag == "" {
		return Ref{}, "", "it has no tag to follow", nil
	}
	if v, ok := parseTagVersion(ref.Tag); ok && len(v.parts) >= 3 {
		tags, err := p.Registry.Tags(ctx, ref.Name)
		if err != nil {
			return Ref{}, "", "", fmt.Errorf("list tags of %s: %w", ref.Name, err)
		}
		if tag, ok := newestPatch(ref.Tag, tags); ok {
			return ref.WithTag(tag), "patch", "", nil
		}
	}
	if ref.Digest == "" {
		return Ref{}, "", "the tag is not pinned by digest, so it already is the newest image of the tag", nil
	}
	tagRef := ref.WithTag(ref.Tag)
	d, err := p.Registry.Digest(ctx, tagRef.String())
	if err != nil {
		return Ref{}, "", "", fmt.Errorf("digest of %s: %w", tagRef, err)
	}
	if d == ref.Digest {
		return Ref{}, "", "the tag has not been rebuilt since the pinned digest", nil
	}
	tagRef.Digest = d
	return tagRef, "digest", "", nil
}

// actionable returns the findings with a fix that are HIGH or CRITICAL.
func actionable(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Actionable() {
			out = append(out, f)
		}
	}
	return out
}

// remaining returns the findings of want that are still in got (fixable or not).
func remaining(want, got []Finding) []Finding {
	var out []Finding
	for _, f := range want {
		if slices.ContainsFunc(got, func(g Finding) bool { return sameVuln(f, g) }) {
			out = append(out, f)
		}
	}
	return out
}

// cves returns the sorted, distinct CVE IDs (or advisory IDs) of fs.
func cves(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		for _, id := range f.CVEs() {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	slices.Sort(out)
	return out
}

// tagsRegex matches exactly the given tags.
func tagsRegex(tags []string) string {
	tags = slices.Clone(tags)
	slices.Sort(tags)
	tags = slices.Compact(tags)
	for i, t := range tags {
		tags[i] = regexp.QuoteMeta(t)
	}
	return "/^(" + strings.Join(tags, "|") + ")$/"
}

// cycleRegex matches a docker tag of release cycle c: "c", "c.x...", "c-suffix".
func cycleRegex(c string) string {
	return "/^" + regexp.QuoteMeta(c) + `(\.|-|$)/`
}

// bodyNote is the prBodyNotes entry: header, then the CVEs with the package, versions and
// severity of each.
func bodyNote(header string, fs []Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**image-scan** (compliance-framework/workflows): %s:\n", header)
	seen := map[string]bool{}
	for _, f := range fs {
		line := fmt.Sprintf("\n- %s: %s %s %s, fixed in %s (%s)", strings.Join(f.CVEs(), ", "), f.Type, f.Package, f.Installed, strings.Join(f.Fixed, ", "), f.Severity())
		if !seen[line] {
			seen[line] = true
			b.WriteString(line)
		}
	}
	return b.String()
}

// codeList is the sorted, distinct images as Markdown code spans.
func codeList(images []string) string {
	images = slices.Clone(images)
	slices.Sort(images)
	return "`" + strings.Join(slices.Compact(images), "`, `") + "`"
}
