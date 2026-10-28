package imagescan

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Rule is a Renovate package rule the plan adds for one repo (renovate.ParseOverrides checks it).
type Rule map[string]any

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
//     Renovate open that update at once, grouped as a security fix, and auto-merge it.
//  2. A base image of any stage on an end-of-life release (node:20) with actionable findings: a
//     rule that opens the lowest supported release whose current image scans clean of them, for a
//     human.
//  3. Anything else actionable is logged as a note: the distribution has the fix, the upstream
//     image hasn't been rebuilt with it yet.
type Planner struct {
	Owner    string
	Scanner  ImageScanner
	Registry Registry
	EOL      Cycler
	cycles   map[string][]Cycle
}

// Plan returns repo's rules (rule 1 by image, then rule 2) and the notes on what it left.
func (p *Planner) Plan(ctx context.Context, repo string, targets []Target) ([]Rule, []string) {
	var notes []string
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	type fix struct {
		findings []Finding
		images   []string
	}
	security := map[string]*fix{}
	var majors []Rule
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
		act := actionable(t.Scan.Findings)
		if len(act) == 0 {
			continue
		}
		ref := t.Base.Ref
		where := fmt.Sprintf("%s (%s, stage %s)", ref, t.Dockerfile, t.Base.Stage)
		if strings.HasPrefix(ref.Name, "ghcr.io/"+p.Owner+"/") {
			note("%s: %d actionable finding(s) in an internal image, which ccf-bump moves, not Renovate", where, len(act))
			continue
		}
		if rule, msg, eol := p.eolRule(ctx, repo, ref, act); eol {
			if rule != nil {
				majors = append(majors, rule)
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
		cand, why, err := p.candidate(ctx, ref)
		if err != nil {
			note("%s: %d actionable finding(s); no candidate: %v", where, len(act), err)
			continue
		}
		if cand == "" {
			note("%s: %d actionable finding(s) (%s) and %s; the distribution's fix waits for an upstream rebuild", where, len(act), strings.Join(cves(act), ", "), why)
			continue
		}
		cs, err := p.Scanner.Scan(ctx, cand)
		if err != nil {
			note("%s: scan of the candidate %s failed: %v", where, cand, err)
			continue
		}
		if left := remaining(act, cs.Findings); len(left) > 0 {
			note("%s: the candidate %s still has %s; the distribution's fix waits for an upstream rebuild", where, cand, strings.Join(cves(left), ", "))
			continue
		}
		f := security[ref.Name]
		if f == nil {
			f = &fix{}
			security[ref.Name] = f
		}
		f.findings = append(f.findings, act...)
		f.images = append(f.images, ref.String())
		note("%s: %s fixes %s (rule 1)", where, cand, strings.Join(cves(act), ", "))
	}

	var rules []Rule
	for _, dep := range slices.Sorted(maps.Keys(security)) {
		f := security[dep]
		rules = append(rules, Rule{
			"matchRepositories":     []string{p.Owner + "/" + repo},
			"matchManagers":         []string{"dockerfile"},
			"matchDepNames":         []string{dep},
			"minimumReleaseAge":     nil,
			"prHourlyLimit":         0,
			"prConcurrentLimit":     0,
			"branchConcurrentLimit": 0,
			"semanticCommitType":    "fix",
			"groupName":             SecurityGroupName,
			"groupSlug":             SecurityGroupSlug,
			"automerge":             true,
			"addLabels":             []string{"renovate:automerge"},
			"prPriority":            10,
			"prBodyNotes":           []string{bodyNote(fmt.Sprintf("this update of %s fixes", codeList(f.images)), f.findings)},
		})
	}
	return append(rules, majors...), notes
}

// eolRule applies rule 2 to an image of an eolLines product. eol is false when the image's release
// isn't known to be end-of-life (msg then says why, if it couldn't be checked); otherwise rule is
// the rule opening the lowest clean supported release, or nil with msg saying why there is none.
func (p *Planner) eolRule(ctx context.Context, repo string, ref Ref, act []Finding) (rule Rule, msg string, eol bool) {
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
		rule = Rule{
			"matchRepositories":   []string{p.Owner + "/" + repo},
			"matchManagers":       []string{"dockerfile"},
			"matchDepNames":       []string{ref.Name},
			"matchCurrentVersion": cycleRegex(current),
			"allowedVersions":     cycleRegex(c.Name),
			"prHourlyLimit":       0,
			"prConcurrentLimit":   0,
			"automerge":           false,
			"addLabels":           []string{"needs-human"},
			"prBodyNotes":         []string{bodyNote(fmt.Sprintf("`%s` is on %s %s, which is end-of-life; this update to %s %s fixes", ref, line.Product, current, line.Product, c.Name), trigger)},
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

// candidate returns the image Renovate would move ref to without a major: for an X.Y.Z tag the
// newest X.Y patch with the same suffix, else the tag's current digest when ref pins an older one.
// "" (with why) when there is none.
func (p *Planner) candidate(ctx context.Context, ref Ref) (cand, why string, err error) {
	if ref.Tag == "" {
		return "", "it has no tag to follow", nil
	}
	if v, ok := parseTagVersion(ref.Tag); ok && len(v.parts) >= 3 {
		tags, err := p.Registry.Tags(ctx, ref.Name)
		if err != nil {
			return "", "", fmt.Errorf("list tags of %s: %w", ref.Name, err)
		}
		if tag, ok := newestPatch(ref.Tag, tags); ok {
			return ref.WithTag(tag).String(), "", nil
		}
	}
	if ref.Digest == "" {
		return "", "the tag is not pinned by digest, so it already is the newest image of the tag", nil
	}
	tagRef := ref.WithTag(ref.Tag).String()
	d, err := p.Registry.Digest(ctx, tagRef)
	if err != nil {
		return "", "", fmt.Errorf("digest of %s: %w", tagRef, err)
	}
	if d == ref.Digest {
		return "", "the tag has not been rebuilt since the pinned digest", nil
	}
	return tagRef + "@" + d, "", nil
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
