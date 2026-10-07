package train

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// The head branch prefix of release-please PRs, and the release-please manifest.
const (
	ReleaseBranchPrefix       = "release-please--branches--"
	ReleasePleaseManifestFile = ".release-please-manifest.json"
)

// issueHelp is shown in every tracking issue.
const issueHelp = "Updated by `train.yml` (cmd/train, docs/train.md). Org owners can comment `/skip <repo>`, " +
	"`/retry <repo>` or `/abort`. Don't edit the state comment at the end."

// Engine runs the train. Every step reads GitHub's current state, so a run can be repeated.
type Engine struct {
	Repos   Repos
	Tracker Tracker
	Members Members // nil: commands are refused, since no one's role can be checked
	Slack   Slack   // nil: messages are only logged
	Bumper  Bumper
	// Load reads a manifest; the train's own path is in its state.
	Load func(path string) (*manifest.Manifest, error)

	Channel               string // Slack channel for new trains
	RequiredCheck         string // a check every merged PR must pass, e.g. "ci / required"
	ReleasePleaseWorkflow string // file name of the repos' release-please workflow
	RunURL                string // this workflow run, linked from failure messages
	Now                   func() time.Time
	Log                   io.Writer
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		fmt.Fprintf(e.Log, format+"\n", args...)
	}
}

// StartOptions selects the repos of a new train.
type StartOptions struct {
	Manifest  string
	Repos     []string // default: every repo with release: true
	DryRun    bool
	Scheduled bool // only start on the month's first working weekday, once
}

// newState lists the selected repos by stage, with their versions on the default branch.
func (e *Engine) newState(ctx context.Context, m *manifest.Manifest, month string, o StartOptions) (*State, error) {
	stages, err := m.Stages()
	if err != nil {
		return nil, err
	}
	for _, name := range o.Repos {
		if i := slices.IndexFunc(m.Repos, func(r manifest.Repo) bool { return r.Name == name }); i < 0 || !m.Repos[i].Release {
			return nil, fmt.Errorf("repo %q is not in %s with release: true", name, o.Manifest)
		}
	}
	st := &State{Month: month, Manifest: o.Manifest, DryRun: o.DryRun, Status: StatusOpen, Channel: e.Channel}
	for _, stage := range stages {
		n := st.Stages() + 1
		for _, name := range stage {
			r := m.Repos[slices.IndexFunc(m.Repos, func(r manifest.Repo) bool { return r.Name == name })]
			if !r.Release || len(o.Repos) > 0 && !slices.Contains(o.Repos, name) {
				continue
			}
			_, sha, err := e.Repos.DefaultBranch(ctx, name)
			if err != nil {
				return nil, err
			}
			from, err := e.manifestAt(ctx, name, sha)
			if err != nil {
				return nil, err
			}
			st.Repos = append(st.Repos, &RepoState{Name: name, Stage: n, Phase: Waiting, From: from})
		}
	}
	if len(st.Repos) == 0 {
		return nil, errors.New("no repos selected")
	}
	return st, nil
}

// Reconcile moves every open train on. It does nothing when no train is open.
func (e *Engine) Reconcile(ctx context.Context) error {
	issues, err := e.Tracker.Issues(ctx, LabelOpen)
	if err != nil {
		return err
	}
	var errs []error
	for _, is := range issues {
		if is.Open {
			errs = append(errs, e.run(ctx, is))
		}
	}
	if len(issues) == 0 {
		e.logf("no open train; nothing to do")
	}
	return errors.Join(errs...)
}

// run reads a train's state and moves it on.
func (e *Engine) run(ctx context.Context, is Issue) error {
	st, err := Parse(is.Body)
	if err != nil {
		return fmt.Errorf("#%d: %w", is.Number, err)
	}
	if st.Status != StatusOpen || st.DryRun {
		e.logf("%s is %s; closing it", is.Title, st.Status)
		return e.save(ctx, is, st)
	}
	return e.step(ctx, is, st)
}

// step advances the open stages, finishes the train when every repo is done, posts the new
// holds and saves the state.
func (e *Engine) step(ctx context.Context, is Issue, st *State) error {
	var errs []error
	for stage := 1; stage <= st.Stages() && st.StageOpen(stage); stage++ {
		for _, r := range st.Repos {
			if r.Stage == stage {
				if err := e.advance(ctx, st, r); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", r.Name, err))
				}
			}
		}
	}
	if len(errs) == 0 && st.AllDone() {
		e.finish(ctx, st)
	}
	for _, r := range st.Repos {
		if r.Hold != NoHold {
			e.notify(ctx, st, r.Name+"|"+string(r.Phase)+"|"+string(r.Hold)+"|"+r.Detail,
				fmt.Sprintf(":warning: *%s* is %s (%s): %s <%s|tracking issue>", r.Name, r.Hold, r.Phase, r.Detail, is.URL))
		}
	}
	if err := errors.Join(errs...); err != nil {
		e.notify(ctx, st, "error|"+err.Error(), fmt.Sprintf(":x: The train run failed: %v. <%s|Run>, <%s|tracking issue>; the next run retries.", err, e.RunURL, is.URL))
	}
	return errors.Join(append(errs, e.save(ctx, is, st))...)
}

// advance moves one repo through as many phases as GitHub's state allows.
func (e *Engine) advance(ctx context.Context, st *State, r *RepoState) error {
	for !r.Phase.Done() && !(r.Hold != NoHold && r.Sticky) {
		before := r.Phase
		var err error
		switch r.Phase {
		case Waiting:
			r.next(Bumping)
		case Bumping:
			err = e.bump(ctx, st, r)
		case ReleasePR:
			err = e.releasePR(ctx, r)
		case Merging:
			err = e.merge(ctx, r)
		case Publishing:
			err = e.publish(ctx, r)
		}
		if err != nil || r.Phase == before {
			return err
		}
	}
	return nil
}

func (r *RepoState) next(p Phase) { r.Phase, r.Hold, r.Detail, r.Sticky = p, NoHold, "", false }

func (r *RepoState) wait(format string, args ...any) {
	r.Hold, r.Detail, r.Sticky = NoHold, fmt.Sprintf(format, args...), false
}

func (r *RepoState) hold(h Hold, sticky bool, format string, args ...any) {
	r.Hold, r.Detail, r.Sticky = h, fmt.Sprintf(format, args...), sticky
}

// released returns the --set versions for repos of stages before stage: every single-package
// repo this train released.
func (s *State) released(stage int) map[string]string {
	sets := map[string]string{}
	for _, r := range s.Repos {
		if r.Stage < stage && r.Version() != "" {
			sets[r.Name] = "v" + r.Version()
		}
	}
	return sets
}

// post posts text to the train's thread (or a new message when threadTS is ""), and logs it.
func (e *Engine) post(ctx context.Context, st *State, threadTS, text string) (string, error) {
	e.logf("slack: %s", text)
	if e.Slack == nil || st.Channel == "" {
		return "", errors.New("no Slack channel")
	}
	ts, err := e.Slack.Post(ctx, st.Channel, text, threadTS)
	if err != nil {
		e.logf("::warning::posting to Slack failed: %v", err)
	}
	return ts, err
}

// notify posts text in the train's thread once per key.
func (e *Engine) notify(ctx context.Context, st *State, key, text string) {
	if slices.Contains(st.Notified, key) {
		return
	}
	if _, err := e.post(ctx, st, st.ThreadTS, text); err == nil || e.Slack == nil || st.Channel == "" {
		st.MarkNotified(key)
	}
}

// save writes the state and the table to the issue, closing it once the train is over.
func (e *Engine) save(ctx context.Context, is Issue, st *State) error {
	body, err := Render(st, issueHelp)
	if err != nil {
		return err
	}
	labels := []string{LabelTrain}
	switch {
	case st.DryRun:
		labels = append(labels, LabelDryRun)
	case st.Status == StatusOpen:
		labels = append(labels, LabelOpen)
	case st.Status == StatusAborted:
		labels = append(labels, LabelAborted)
	default:
		labels = append(labels, LabelDone)
	}
	return e.Tracker.EditIssue(ctx, is.Number, body, labels, st.Status != StatusOpen)
}

func short(sha string) string { return sha[:min(7, len(sha))] }

// finish closes a train whose repos are all done and posts the versions.
func (e *Engine) finish(ctx context.Context, st *State) {
	st.Status = StatusFinished
	e.notify(ctx, st, "finished", fmt.Sprintf(":white_check_mark: *Release train %s finished*: %s.", st.Month, st.versions()))
}

// versions lists what the train released, e.g. "api v0.2.0, ui v1.0.1".
func (s *State) versions() string {
	var out []string
	for _, r := range s.Repos {
		if v := Versions(r.Versions); v != "" {
			out = append(out, r.Name+" "+v)
		}
	}
	if len(out) == 0 {
		return "nothing released"
	}
	return strings.Join(out, ", ")
}
