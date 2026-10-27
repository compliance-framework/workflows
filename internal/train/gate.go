package train

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/compliance-framework/workflows/internal/manifest"
	"github.com/compliance-framework/workflows/internal/slackkit"
)

// The gate (docs/train.md, "The gate") runs before the first stage with a helm repo, or before the
// train finishes when it has none: plugin-probe.yml on every go-plugin and policies repo of the
// earlier stages, and stack-smoke.yml with the train's api, ui, agent and smoke plugin. It runs both workflows
// in this repo with workflow_dispatch, one run per check, and reads their results on later runs.
const (
	ProbeWorkflow = "plugin-probe.yml"
	SmokeWorkflow = "stack-smoke.yml"
	// SmokeCheck names the stack-smoke check, in the state and in /retry and /skip.
	SmokeCheck = "stack-smoke"
	// ProbeReportMarker starts the log line where plugin-probe.yml prints its JSON report.
	ProbeReportMarker = "ccf-plugin-probe-report "
	// AgentRepo is the repo of the agent module (github.com/compliance-framework/agent) that
	// plugin-probe.yml builds the probe against (its agent-ref input).
	AgentRepo = "agent"
)

// SmokePlugin is the plugin stack-smoke.yml runs in the stack (with its "-policies" bundle).
const SmokePlugin = "plugin-file-attestation"

// smokeInputs maps stack-smoke.yml's inputs to the repos whose artifacts they tag, and the tag's
// prefix: the images (ghcr.io/compliance-framework/<repo>) are tagged X.Y.Z, the plugin and policy
// OCI artifacts vX.Y.Z. The first three are the stack: stack-smoke runs only in a train that holds
// all three. The others are passed when the train holds the repo; else the workflow's default
// applies (plugin-tag and policy-tag are W3-S1-T05's inputs).
var smokeInputs = []struct{ input, repo, prefix string }{
	{"api-tag", "api", ""}, {"ui-tag", "ui", ""}, {"agent-tag", "agent", ""},
	{"plugin-tag", SmokePlugin, "v"}, {"policy-tag", SmokePlugin + "-policies", "v"},
}

// GateResult is where a gate check stands.
type GateResult string

// The results.
const (
	GatePending GateResult = ""        // not dispatched yet
	GateRunning GateResult = "running" // its run hasn't finished
	GatePassed  GateResult = "passed"
	GateFailed  GateResult = "failed"
	GateSkipped GateResult = "skipped" // /skip, or nothing to check
)

// GateCheck is one workflow run of the gate.
type GateCheck struct {
	Name     string            `json:"name"` // the repo probed, or SmokeCheck
	Workflow string            `json:"workflow"`
	Version  string            `json:"version,omitempty"` // the repo version probed
	Inputs   map[string]string `json:"inputs,omitempty"`
	RunID    int64             `json:"run_id,omitempty"`
	RunURL   string            `json:"run_url,omitempty"`
	Result   GateResult        `json:"result,omitempty"`
	Detail   string            `json:"detail,omitempty"`
	// Candidate: the probe found a protocol v1 plugin, a deprecation candidate (never blocking).
	Candidate bool `json:"candidate,omitempty"`
}

// Done reports whether the check lets the gate pass.
func (g *GateCheck) Done() bool { return g.Result == GatePassed || g.Result == GateSkipped }

// Gate runs the gate's workflows in the workflows repo (with GITHUB_TOKEN, actions: write).
type Gate interface {
	// Dispatch runs workflow on the repo's default branch with inputs; the run's ID is 0 when
	// GitHub didn't return it.
	Dispatch(ctx context.Context, workflow string, inputs map[string]string) (Run, error)
	WorkflowRun(ctx context.Context, id int64) (Run, error)
	// RunLog returns the logs of the run's jobs, one after the other.
	RunLog(ctx context.Context, id int64) (string, error)
}

// GateCheck returns the named gate check, or nil.
func (s *State) GateCheck(name string) *GateCheck {
	for _, g := range s.Gate {
		if g.Name == name {
			return g
		}
	}
	return nil
}

// gateStage is the stage the gate runs before: the first with a helm repo, else Stages()+1.
func gateStage(m *manifest.Manifest, st *State) int {
	gs := st.Stages() + 1
	for _, r := range st.Repos {
		if mr := m.Repo(r.Name); mr != nil && mr.Kind == manifest.KindHelm {
			gs = min(gs, r.Stage)
		}
	}
	return gs
}

// trainVersion is what the train released of r (planned, in a dry run), else its version when
// the train started.
func trainVersion(r *RepoState) string {
	if v := r.Version(); v != "" {
		return v
	}
	return r.From[RootPackage]
}

// repoVersion is trainVersion for a repo of the train, else "": the release-bot token reaches
// only the train's repos.
func repoVersion(st *State, name string) string {
	if r := st.Repo(name); r != nil {
		return trainVersion(r)
	}
	return ""
}

// currentVersion is the root package's version in the release-please manifest on the default
// branch.
func (e *Engine) currentVersion(ctx context.Context, repo string) (string, error) {
	_, sha, err := e.Repos.DefaultBranch(ctx, repo)
	if err != nil {
		return "", err
	}
	m, err := e.manifestAt(ctx, repo, sha)
	return m[RootPackage], err
}

// gateChecks lists the gate's checks: a probe of each go-plugin and policies repo before stage
// gs, at its train version, built against the train's agent (the go.mod pin when the train has no
// agent repo); and stack-smoke when the train has the api, ui and agent repos (the product images).
func gateChecks(registry string, m *manifest.Manifest, st *State, gs int) []*GateCheck {
	agentRef := "" // plugin-probe.yml's default: the agent in this repo's go.mod
	if v := repoVersion(st, AgentRepo); v != "" {
		agentRef = "v" + v
	}
	var out []*GateCheck
	for _, r := range st.Repos {
		mr := m.Repo(r.Name)
		if r.Stage >= gs || mr == nil || mr.Kind != manifest.KindGoPlugin && mr.Kind != manifest.KindPolicies {
			continue
		}
		g := &GateCheck{Name: r.Name, Workflow: ProbeWorkflow, Inputs: map[string]string{"agent-ref": agentRef}}
		switch v := trainVersion(r); {
		case r.Phase == Skipped:
			g.Result, g.Detail = GateSkipped, "the repo was skipped"
		case v == "":
			g.Result, g.Detail = GateSkipped, "no release-please version to probe"
		default:
			input := "plugins"
			if mr.Kind == manifest.KindPolicies {
				input = "policies"
			}
			g.Inputs[input] = strings.TrimRight(registry, "/") + "/" + strings.ToLower(r.Name)
			g.setVersion(v)
		}
		out = append(out, g)
	}
	smoke := &GateCheck{Name: SmokeCheck, Workflow: SmokeWorkflow, Inputs: map[string]string{}}
	for i, si := range smokeInputs {
		if i < 3 && st.Repo(si.repo) == nil {
			return out
		}
		if v := repoVersion(st, si.repo); v != "" {
			smoke.Inputs[si.input] = si.prefix + v
		}
	}
	return append(out, smoke)
}

// setVersion points a probe's OCI tag at version.
func (g *GateCheck) setVersion(version string) {
	for _, k := range []string{"plugins", "policies"} {
		if ref := g.Inputs[k]; ref != "" {
			g.Inputs[k] = strings.TrimSuffix(ref, ":v"+g.Version) + ":v" + version
		}
	}
	g.Version = version
}

// describe says what the check runs, for the dry-run plan.
func (g *GateCheck) describe() string {
	switch {
	case g.Result == GateSkipped:
		return fmt.Sprintf("%s skipped (%s)", g.Name, g.Detail)
	case g.Workflow == SmokeWorkflow:
		var in []string
		for _, si := range smokeInputs {
			if v := g.Inputs[si.input]; v != "" {
				in = append(in, si.input+" "+v)
			}
		}
		return "stack-smoke with " + strings.Join(in, ", ")
	}
	agent := g.Inputs["agent-ref"]
	if agent == "" {
		agent = "the go.mod pin"
	}
	return fmt.Sprintf("plugin-probe %s (agent %s)", strings.TrimSpace(g.Inputs["plugins"]+" "+g.Inputs["policies"]), agent)
}

// gate runs the gate's checks and reports whether every one passed or was skipped. Its checks
// are listed once, when the stages before it are done, from what the train released.
func (e *Engine) gate(ctx context.Context, st *State, m *manifest.Manifest, gs int) (bool, error) {
	if e.Gate == nil {
		return true, nil
	}
	if st.Gate == nil {
		checks := gateChecks(e.Registry, m, st, gs)
		if len(checks) == 0 {
			return true, nil
		}
		st.Gate = checks
	}
	var errs []error
	passed := true
	for _, g := range st.Gate {
		if err := e.runCheck(ctx, st, g); err != nil {
			errs = append(errs, fmt.Errorf("gate %s: %w", g.Name, err))
		}
		passed = passed && g.Done()
	}
	if passed {
		e.notify(ctx, st, "gate|passed", fmt.Sprintf(":white_check_mark: The gate passed (%d checks).", len(st.Gate)))
	}
	return passed, errors.Join(errs...)
}

// runCheck moves one check on: it dispatches its run, or reads the result of the run.
func (e *Engine) runCheck(ctx context.Context, st *State, g *GateCheck) error {
	switch g.Result {
	case GatePassed, GateSkipped:
		return nil
	case GateFailed, GatePending:
		// A new release of the repo (a fix) is probed instead, once it is published: its artifact
		// is pushed by its release workflow, after the release PR merged and moved the manifest.
		if g.Workflow == ProbeWorkflow && st.Repo(g.Name) != nil {
			v, err := e.currentVersion(ctx, g.Name)
			if err != nil {
				return err
			}
			if v != "" && v != g.Version {
				published, failed, detail, err := e.published(ctx, g.Name, v)
				switch {
				case err != nil:
					return err
				case failed:
					e.failCheck(ctx, st, g, detail, "")
					return nil
				case !published:
					g.Detail = detail
					return nil
				}
				g.setVersion(v)
				g.Result = GatePending
			}
		}
		if g.Result == GateFailed {
			return nil
		}
		run, err := e.Gate.Dispatch(ctx, g.Workflow, g.Inputs)
		if err != nil {
			return fmt.Errorf("dispatching %s: %w", g.Workflow, err)
		}
		g.RunID, g.RunURL, g.Result, g.Detail = run.ID, run.URL, GateRunning, ""
		if run.ID == 0 {
			// Dispatching again on every run could start many; a person retries.
			e.failCheck(ctx, st, g, "dispatched, but GitHub returned no run ID: find the run in the Actions tab", "")
		}
		return nil
	}
	run, err := e.Gate.WorkflowRun(ctx, g.RunID)
	if err != nil {
		return err
	}
	if run.Status != "completed" {
		return nil
	}
	log, err := e.Gate.RunLog(ctx, g.RunID)
	if err != nil {
		// The result waits for its log (the protocol and the output tail): the next run reads it.
		return fmt.Errorf("reading the log of %s: %w", g.RunURL, err)
	}
	if g.Workflow == ProbeWorkflow && v1Plugin(log) {
		g.Candidate = true
		e.notify(ctx, st, "gate-candidate|"+g.Name, fmt.Sprintf(":information_source: *%s* v%s speaks plugin protocol v1: a deprecation candidate (not blocking). <%s|Probe run>", g.Name, g.Version, g.RunURL))
	}
	if run.Conclusion != "success" {
		e.failCheck(ctx, st, g, fmt.Sprintf("%s %s: %s", g.Workflow, run.Conclusion, g.RunURL), logTail(log))
		return nil
	}
	g.Result, g.Detail = GatePassed, ""
	clearGateHold(st, g.Name)
	return nil
}

// published reports whether version of repo is published, as publish decides a repo is released:
// its release tag exists and the tag's release workflows succeeded. Otherwise detail says what it
// waits for, or that the release workflow failed (failed).
func (e *Engine) published(ctx context.Context, repo, version string) (published, failed bool, detail string, err error) {
	all, err := e.Repos.Tags(ctx, repo)
	if err != nil {
		return false, false, "", err
	}
	tags := releaseTags(slices.Collect(maps.Keys(all)), map[string]string{RootPackage: version})
	if len(tags) == 0 {
		return false, false, fmt.Sprintf("v%s found; waiting for its release tag", version), nil
	}
	slices.Sort(tags)
	sha := all[tags[0]]
	tags = slices.DeleteFunc(tags, func(t string) bool { return all[t] != sha })
	pending, run, err := e.releaseWorkflows(ctx, repo, sha, tags)
	switch {
	case err != nil:
		return false, false, "", err
	case run != nil:
		return false, true, fmt.Sprintf("the release workflow of v%s failed: %s; re-run it and the gate probes v%s", version, run.URL, version), nil
	case len(pending) > 0:
		return false, false, fmt.Sprintf("v%s found; waiting for its release workflow", version), nil
	}
	return true, false, "", nil
}

// failCheck fails g, blocks its repo, and posts the output tail in the train's thread.
func (e *Engine) failCheck(ctx context.Context, st *State, g *GateCheck, detail, tail string) {
	g.Result, g.Detail = GateFailed, detail
	what := g.Name
	if r := st.Repo(g.Name); r != nil {
		what = fmt.Sprintf("%s v%s", g.Name, g.Version)
		r.hold(Blocked, false, "the gate failed: %s; `/retry %s` runs it again, `/skip %s` passes the gate", detail, g.Name, g.Name)
	}
	text := fmt.Sprintf(":x: The gate failed for *%s*: %s. Helm doesn't start until it passes: a new release of the repo probes again, `/retry %s` runs it again, `/skip %s` passes it.",
		what, detail, g.Name, g.Name)
	if tail != "" {
		text += "\n```\n" + slackkit.Escape(strings.ReplaceAll(tail, "```", "'''")) + "\n```"
	}
	e.notify(ctx, st, fmt.Sprintf("gate|%s|%d|%s", g.Name, g.RunID, g.Version), text)
}

// clearGateHold lifts the gate's hold on a released repo (a repo that is done holds only for the gate).
func clearGateHold(st *State, name string) {
	if r := st.Repo(name); r != nil && r.Phase.Done() && r.Hold != NoHold {
		r.hold(NoHold, false, "")
	}
}

// gateCommand applies /retry or /skip to a gate check.
func (e *Engine) gateCommand(ctx context.Context, st *State, author string, c Command, g *GateCheck) {
	if c.Name == "skip" {
		g.Result, g.Detail = GateSkipped, "skipped by "+author
		e.notify(ctx, st, "gate|skipped|"+g.Name, fmt.Sprintf(":fast_forward: The gate check of *%s* was skipped by %s.", g.Name, author))
	} else {
		g.Result, g.RunID, g.RunURL, g.Detail = GatePending, 0, "", "retried by "+author
	}
	clearGateHold(st, g.Name)
}

// logLine is the timestamp GitHub starts each log line with.
var logLine = regexp.MustCompile(`^\d{4}-\d\d-\d\dT[0-9:.]+Z `)

func logLines(log string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(log))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		out = append(out, logLine.ReplaceAllString(strings.TrimRight(sc.Text(), "\r"), ""))
	}
	return out
}

// v1Plugin reports whether the probe report in a plugin-probe.yml log lists a protocol v1 plugin.
func v1Plugin(log string) bool {
	for _, line := range logLines(log) {
		data, ok := strings.CutPrefix(line, ProbeReportMarker)
		if !ok {
			continue
		}
		var report struct {
			Plugins []struct {
				Protocol int `json:"protocol"`
			} `json:"plugins"`
		}
		if json.Unmarshal([]byte(data), &report) != nil {
			continue
		}
		for _, p := range report.Plugins {
			if p.Protocol == 1 {
				return true
			}
		}
	}
	return false
}

// maxTailLines bounds the output tail posted for a failed check.
const maxTailLines = 20

// logTail is the end of a failed run's output: the lines before the step's exit, without the
// log's group markers and timestamps.
func logTail(log string) string {
	lines := logLines(log)
	end := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "##[error]Process completed with exit code") {
			end = i
			break
		}
	}
	var tail []string
	for i := end - 1; i >= 0 && len(tail) < maxTailLines; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "##[endgroup]") {
			tail = append([]string{lines[i]}, tail...)
		}
	}
	return clip(strings.Join(tail, "\n"), 2500)
}
