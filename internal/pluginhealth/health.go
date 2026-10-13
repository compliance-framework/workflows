// Package pluginhealth builds the plugin health report: one row per kept plugin with the agent
// library its latest release requires, how many minors that is behind the agent's latest final
// release, the protocol its :latest image is annotated with, its latest ccf-bump PR, its latest
// release and a status. cmd/plugin-health drives it; docs/plugin-health.md has the rules.
package pluginhealth

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

// Status is a plugin's health.
type Status string

// The statuses, from best to worst; Unknown is a row the rules can't settle.
const (
	Healthy   Status = "healthy"
	Behind    Status = "behind"
	Candidate Status = "deprecation candidate"
	Unknown   Status = "unknown"
)

const (
	// BehindMinors and CandidateMinors are the agent minors behind that make a plugin behind and a
	// deprecation candidate.
	BehindMinors    = 2
	CandidateMinors = 3
	// OlderMajor is Row.Behind for a plugin on an older agent major.
	OlderMajor = 1000
	// V2Marker is what a v2 plugin's main.go serves: runner.RunnerV2GRPCPlugin, which needs Init.
	V2Marker = "RunnerV2GRPCPlugin"
	// ProtocolAnnotation is pluginprobe.ProtocolAnnotation, which the agent reads; it is not
	// imported, so that plan doesn't build the probe (a test keeps the two equal).
	ProtocolAnnotation = "org.ccf.plugin.protocol.version"
	// BumpPrefix is the head branch prefix of ccf-bump's PRs.
	BumpPrefix = "ccf-bump/"
)

// Release is a repo's latest final release.
type Release struct {
	Tag  string
	Date time.Time
}

// BumpPR is a repo's latest ccf-bump PR.
type BumpPR struct {
	Number   int
	URL      string
	State    string // open, closed
	Merged   time.Time
	Required string // the CICheck conclusion of an open PR; "" when pending
}

// failing are the conclusions that fail a required check; cancelled and stale wait for a newer
// run (as the train and ccf-bump merge read them).
var failing = map[string]bool{"failure": true, "timed_out": true, "action_required": true, "startup_failure": true}

// Result is the PR's outcome: merged, closed (unmerged), failed (open, its CICheck failing) or
// open.
func (p BumpPR) Result() string {
	switch {
	case !p.Merged.IsZero():
		return "merged"
	case p.State == "closed":
		return "closed"
	case failing[p.Required]:
		return "failed"
	}
	return "open"
}

// Source reads what the report needs. GitHub plus Registry is the real one; tests use a fake.
type Source interface {
	// LatestFinal returns the highest vX.Y.Z release of repo, "" when there is none.
	LatestFinal(ctx context.Context, repo string) (string, error)
	// LatestRelease returns repo's latest final release, nil when there is none.
	LatestRelease(ctx context.Context, repo string) (*Release, error)
	// File returns path in repo at ref, nil when it doesn't exist.
	File(ctx context.Context, repo, ref, path string) ([]byte, error)
	// LatestBumpPR returns repo's newest ccf-bump PR (Required set when open), nil when none.
	LatestBumpPR(ctx context.Context, repo string) (*BumpPR, error)
	// Annotations returns the top-level manifest annotations of an OCI tag; found is false when
	// the tag doesn't exist.
	Annotations(ctx context.Context, ref string) (annotations map[string]string, found bool, err error)
}

// Row is one plugin's line in the report.
type Row struct {
	Repo    string
	Release *Release
	// Lib is the agent library the latest release's go.mod requires; "" when unknown.
	Lib string
	// Behind is how many minors Lib is behind the latest agent final release: OlderMajor for an
	// older major, -1 when unknown.
	Behind int
	// Protocol is the :latest image's protocol annotation: 1 or 2, 0 when unset (or no :latest
	// image), -1 when it couldn't be read.
	Protocol int
	// V2Init says whether main.go at the release serves a v2 runner (V2Marker); read only when
	// Protocol is 0 or 1, nil when not read.
	V2Init *bool
	Bump   *BumpPR
	Status Status
	Notes  []string
	// Errors are the reads that failed; the command fails after publishing when there are any.
	Errors []string
}

// Report is the whole table.
type Report struct {
	Owner, AgentRepo, Agent string
	Rows                    []Row
}

// Candidates returns the names of the deprecation candidates, in row order.
func (r Report) Candidates() []string {
	var out []string
	for _, row := range r.Rows {
		if row.Status == Candidate {
			out = append(out, row.Repo)
		}
	}
	return out
}

// Errors returns every failed read as "repo: error".
func (r Report) Errors() []string {
	var out []string
	for _, row := range r.Rows {
		for _, e := range row.Errors {
			out = append(out, row.Repo+": "+e)
		}
	}
	return out
}

// Evaluate applies the status rules (docs/plugin-health.md) to a row:
//   - deprecation candidate: annotated protocol 1 or unset and main.go serves no v2 runner, or
//     CandidateMinors or more agent minors behind (or an older major);
//   - unknown: otherwise, if the agent library, the protocol or (for protocol 1 or unset) the v2
//     check couldn't be read;
//   - behind: BehindMinors or more minors behind;
//   - healthy: the rest.
func Evaluate(r Row) Status {
	v1 := r.Protocol == 0 || r.Protocol == 1
	if (v1 && r.V2Init != nil && !*r.V2Init) || r.Behind >= CandidateMinors {
		return Candidate
	}
	if r.Behind < 0 || r.Protocol < 0 || (v1 && r.V2Init == nil) {
		return Unknown
	}
	if r.Behind >= BehindMinors {
		return Behind
	}
	return Healthy
}

// Collect reads every plugin's row. agentRepo is the agent repo in owner (its module is
// github.com/<owner>/<agentRepo>); registry is the plugins' OCI registry host. Only the agent's
// latest release is fatal; a plugin's failed reads are kept in its row.
func Collect(ctx context.Context, src Source, owner, agentRepo, registry string, plugins []string) (Report, error) {
	agent, err := src.LatestFinal(ctx, agentRepo)
	if err != nil {
		return Report{}, fmt.Errorf("latest %s release: %w", agentRepo, err)
	}
	if agent == "" {
		return Report{}, fmt.Errorf("%s has no final release", agentRepo)
	}
	rep := Report{Owner: owner, AgentRepo: agentRepo, Agent: agent}
	module := "github.com/" + owner + "/" + agentRepo
	for _, p := range plugins {
		rep.Rows = append(rep.Rows, collectRow(ctx, src, p, module, agent, registry+"/"+owner+"/"+p+":latest"))
	}
	return rep, nil
}

func collectRow(ctx context.Context, src Source, repo, module, agent, ref string) Row {
	row := Row{Repo: repo, Behind: -1, Protocol: -1}
	fail := func(what string, err error) { row.Errors = append(row.Errors, what+": "+err.Error()) }

	rel, err := src.LatestRelease(ctx, repo)
	switch {
	case err != nil:
		fail("latest release", err)
	case rel == nil:
		row.Notes = append(row.Notes, "no final release")
	default:
		row.Release = rel
		if gomod, err := src.File(ctx, repo, rel.Tag, "go.mod"); err != nil {
			fail("go.mod at "+rel.Tag, err)
		} else if lib, err := LibVersion(gomod, module); err != nil {
			row.Notes = append(row.Notes, "go.mod at "+rel.Tag+": "+err.Error())
		} else {
			row.Lib, row.Behind = lib, MinorsBehind(lib, agent)
		}
	}

	if ann, found, err := src.Annotations(ctx, ref); err != nil {
		fail(ref, err)
	} else {
		row.Protocol = protocol(ann)
		if !found {
			row.Notes = append(row.Notes, "no :latest image")
		}
	}
	if (row.Protocol == 0 || row.Protocol == 1) && rel != nil {
		if main, err := src.File(ctx, repo, rel.Tag, "main.go"); err != nil {
			fail("main.go at "+rel.Tag, err)
		} else if main == nil {
			row.Notes = append(row.Notes, "no main.go at "+rel.Tag+" to look for Init")
		} else {
			v2 := bytes.Contains(main, []byte(V2Marker))
			row.V2Init = &v2
			if v2 {
				row.Notes = append(row.Notes, "serves v2 but the agent runs it as v1: annotate protocol 2")
			}
		}
	}

	if pr, err := src.LatestBumpPR(ctx, repo); err != nil {
		fail("ccf-bump PRs", err)
	} else {
		row.Bump = pr
	}
	row.Status = Evaluate(row)
	return row
}

// protocol reads the protocol annotation as the agent does: 1 or 2, else 0 (unset or ignored).
func protocol(ann map[string]string) int {
	n, err := strconv.Atoi(ann[ProtocolAnnotation])
	if err != nil || (n != 1 && n != 2) {
		return 0
	}
	return n
}

// LibVersion returns the version of module that a go.mod requires: a replace with a version wins,
// a replace with a local path is an error.
func LibVersion(gomod []byte, module string) (string, error) {
	f, err := modfile.Parse("go.mod", gomod, nil)
	if err != nil {
		return "", err
	}
	version := ""
	for _, r := range f.Require {
		if r.Mod.Path == module {
			version = r.Mod.Version
		}
	}
	if version == "" {
		return "", fmt.Errorf("doesn't require %s", module)
	}
	for _, r := range f.Replace {
		if r.Old.Path == module && (r.Old.Version == "" || r.Old.Version == version) {
			if r.New.Version == "" {
				return "", fmt.Errorf("%s is replaced by %s", module, r.New.Path)
			}
			return r.New.Version, nil
		}
	}
	return version, nil
}

// MinorsBehind returns how many minors lib is behind latest (0 when it is level or ahead),
// OlderMajor for an older major, -1 when either isn't semver.
func MinorsBehind(lib, latest string) int {
	if !semver.IsValid(lib) || !semver.IsValid(latest) {
		return -1
	}
	if semver.Major(lib) != semver.Major(latest) {
		if semver.Compare(lib, latest) < 0 {
			return OlderMajor
		}
		return 0
	}
	return max(0, minor(latest)-minor(lib))
}

func minor(v string) int {
	_, m, _ := strings.Cut(semver.MajorMinor(v), ".")
	n, _ := strconv.Atoi(m)
	return n
}
