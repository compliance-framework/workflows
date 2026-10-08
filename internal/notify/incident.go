// The incident model behind the notify-failure workflow: a failure opens an incident (a
// top-level Slack card) or replies in its thread, a pass closes it with a reply, and every
// later transition also edits the card in place.

package notify

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/slackkit"
)

// Outcome is what a run means for its incident.
type Outcome string

const (
	OutcomeFailure Outcome = "failure"
	OutcomeSuccess Outcome = "success"
	OutcomeNone    Outcome = "none" // cancelled or entirely skipped: leaves the incident as it is
)

// Result is a run's outcome and, for a failure, its failed jobs (sorted; empty if unknown).
type Result struct {
	Outcome    Outcome
	FailedJobs []string
}

// ParseNeeds reads the caller's toJSON(needs). Any failed job makes the run a failure;
// otherwise any cancelled job, or no job having run, leaves the incident alone; otherwise
// (successes, maybe with skips) it is a success. An empty string is a caller that predates
// the needs input and calls the workflow only with if: failure(), so the run failed and
// its failed jobs are unknown.
func ParseNeeds(s string) (Result, error) {
	if strings.TrimSpace(s) == "" {
		return Result{Outcome: OutcomeFailure}, nil
	}
	var needs map[string]struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(s), &needs); err != nil {
		return Result{}, fmt.Errorf("parsing the needs input: %w", err)
	}
	if len(needs) == 0 {
		return Result{}, errors.New("the needs input lists no jobs: pass toJSON(needs) from a job that needs the CI jobs")
	}
	var failed []string
	cancelled, succeeded := false, false
	for name, job := range needs {
		switch job.Result {
		case "failure":
			failed = append(failed, name)
		case "cancelled":
			cancelled = true
		case "success":
			succeeded = true
		}
	}
	switch {
	case len(failed) > 0:
		slices.Sort(failed)
		return Result{Outcome: OutcomeFailure, FailedJobs: failed}, nil
	case cancelled || !succeeded:
		return Result{Outcome: OutcomeNone}, nil
	}
	return Result{Outcome: OutcomeSuccess}, nil
}

// Incident is the saved state of one incident key. The zero value is "no incident".
type Incident struct {
	Key        string   `json:"key"`     // IncidentKey, to reject a state restored for another key
	Channel    string   `json:"channel"` // where the thread is
	TS         string   `json:"ts"`      // the top-level message, the thread's thread_ts
	Open       bool     `json:"open"`
	SHA        string   `json:"sha"`         // the last commit posted about
	FailedJobs []string `json:"failed_jobs"` // the failed jobs last posted (kept once resolved, for the card)
	// For the card; a state saved before cards has none of these, and the card does without.
	OpenedSHA  string    `json:"opened_sha,omitempty"` // the commit the incident opened at
	OpenedAt   time.Time `json:"opened_at,omitzero"`
	Again      bool      `json:"again,omitempty"` // a later failure replied: "Failing again"
	ResolvedAt time.Time `json:"resolved_at,omitzero"`
	ClosedAs   string    `json:"closed_as,omitempty"` // "merged" or "closed": closed with its PR, unresolved
}

// LoadIncident reads the state at path. A missing file is no incident. Any other problem,
// including a state saved for another key, returns no incident and an error the caller
// can report and go on from: a broken state must not block notifications for good.
func LoadIncident(path, key string) (Incident, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Incident{}, nil
	}
	if err != nil {
		return Incident{}, err
	}
	var inc Incident
	if err := json.Unmarshal(data, &inc); err != nil {
		return Incident{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if inc.Key != key {
		return Incident{}, fmt.Errorf("%s is for incident %q, not %q", path, inc.Key, key)
	}
	return inc, nil
}

// Save writes the state to path.
func (inc Incident) Save(path string) error {
	data, err := json.Marshal(inc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// Action is what Handle did.
type Action string

const (
	ActionNone    Action = "none"    // nothing to post: no incident and a pass, or a cancelled run
	ActionDedupe  Action = "dedupe"  // the same commit and failed jobs were already posted
	ActionOpen    Action = "open"    // posted a new top-level message
	ActionReply   Action = "reply"   // posted another failure in the thread
	ActionRecover Action = "recover" // posted the recovery in the thread and closed the incident
	ActionClose   Action = "close"   // the PR was merged or closed: replied and closed the incident
)

// CardError is a failure to edit an incident's card after its thread reply was posted. The
// state Handle returns with it is still the next state, to save: the reply is the
// notification, and retrying it would post it twice.
type CardError struct{ Err error }

func (e *CardError) Error() string { return "updating the incident card: " + e.Err.Error() }
func (e *CardError) Unwrap() error { return e.Err }

// Handle applies the run r with result res to the incident prev (for key), posts what
// that needs, and returns the next state. The state changed, and must be saved, unless
// the action is ActionNone or ActionDedupe; it must also be saved with a *CardError. channel
// is where a new incident is posted; replies go to the incident's own channel. now is when.
func Handle(ctx context.Context, api slackkit.API, key string, prev Incident, r Run, res Result, channel string, now time.Time) (Incident, Action, error) {
	switch res.Outcome {
	case OutcomeFailure:
		if prev.Open && prev.SHA == r.SHA && slices.Equal(prev.FailedJobs, res.FailedJobs) {
			return prev, ActionDedupe, nil
		}
		if prev.Open {
			if _, err := api.Reply(ctx, prev.Channel, prev.TS, slackkit.Note(FailureReply(r, res.FailedJobs))); err != nil {
				return prev, ActionNone, err
			}
			next := prev
			next.SHA, next.FailedJobs, next.Again = r.SHA, res.FailedJobs, true
			return next, ActionReply, updateCard(ctx, api, r, next, now)
		}
		if channel == "" {
			return prev, ActionNone, errors.New("no Slack channel: pass the channel input or set the SLACK_CHANNEL_CI_FAILURES variable")
		}
		next := Incident{Key: key, Open: true, SHA: r.SHA, FailedJobs: res.FailedJobs, OpenedSHA: r.SHA, OpenedAt: now}
		posted, err := api.Post(ctx, channel, IncidentCard(r, next, now))
		if err != nil {
			return prev, ActionNone, err
		}
		if posted.TS == "" {
			return prev, ActionNone, errors.New("chat.postMessage returned no ts to thread replies on")
		}
		next.Channel, next.TS = cmp.Or(posted.Channel, channel), posted.TS
		return next, ActionOpen, nil
	case OutcomeSuccess:
		if !prev.Open {
			return prev, ActionNone, nil
		}
		if _, err := api.Reply(ctx, prev.Channel, prev.TS, slackkit.Note(RecoveryReply(r))); err != nil {
			return prev, ActionNone, err
		}
		next := prev
		next.Open, next.SHA, next.ResolvedAt = false, r.SHA, now
		return next, ActionRecover, updateCard(ctx, api, r, next, now)
	}
	return prev, ActionNone, nil
}

// HandleClosed closes the incident prev of the closed PR r, if it is open: a reply in the
// thread and the card marked closed. It runs no failure logic. Like Handle, a state that comes
// with a *CardError is still the next state.
func HandleClosed(ctx context.Context, api slackkit.API, prev Incident, r Run, now time.Time) (Incident, Action, error) {
	if !prev.Open {
		return prev, ActionNone, nil
	}
	if _, err := api.Reply(ctx, prev.Channel, prev.TS, slackkit.Note(ClosedReply(r))); err != nil {
		return prev, ActionNone, err
	}
	next := prev
	next.Open, next.ClosedAs, next.ResolvedAt = false, closedAs(r), now
	return next, ActionClose, updateCard(ctx, api, r, next, now)
}

// closedAs is "merged" or "closed".
func closedAs(r Run) string {
	if r.PRMerged {
		return "merged"
	}
	return "closed"
}

// updateCard edits the incident's top-level card to inc; a failure is a *CardError.
func updateCard(ctx context.Context, api slackkit.API, r Run, inc Incident, now time.Time) error {
	if err := api.Update(ctx, inc.Channel, inc.TS, IncidentCard(r, inc, now)); err != nil {
		return &CardError{Err: err}
	}
	return nil
}

// IncidentCard is the incident's top-level card for the run r: its status, the commit it
// opened at, the failed jobs (struck through once resolved), and links to the PR and the run.
func IncidentCard(r Run, inc Incident, now time.Time) slackkit.Message {
	repoURL := r.ServerURL + "/" + r.Repo
	status, note := slackkit.Failing, ""
	switch {
	case !inc.Open && inc.ClosedAs != "":
		status, note = slackkit.Closed, "PR "+inc.ClosedAs
	case !inc.Open:
		status = slackkit.Resolved
	case inc.Again:
		status = slackkit.FailingAgain
	}
	sha := cmp.Or(inc.OpenedSHA, inc.SHA)
	c := slackkit.Incident{
		Repo: r.Repo, Ref: r.Repo + "@" + r.Branch, RefURL: repoURL + "/tree/" + r.Branch, Workflow: r.Workflow,
		Status: status, StatusNote: note, SHA: sha, SHAURL: repoURL + "/commit/" + sha, FailedJobs: inc.FailedJobs,
		OpenedAt: inc.OpenedAt, ResolvedAt: inc.ResolvedAt, RunURL: repoURL + "/actions/runs/" + r.RunID, UpdatedAt: now,
	}
	if r.PRNumber != 0 {
		c.Ref, c.RefURL, c.Title = fmt.Sprintf("%s#%d", r.Repo, r.PRNumber), r.PRURL(), r.PRTitle
		c.PRURL = c.RefURL
	}
	return slackkit.IncidentCard(c)
}

// FailureReply is the thread reply for another failure of an open incident.
func FailureReply(r Run, failedJobs []string) string {
	return fmt.Sprintf("❌ failed%s at %s", jobList(failedJobs), commitAndRun(r))
}

// RecoveryReply is the thread reply that closes an incident.
func RecoveryReply(r Run) string {
	return "✅ passing again at " + commitAndRun(r)
}

// ClosedReply is the thread reply that closes an incident with its PR.
func ClosedReply(r Run) string {
	by := ""
	if r.PRClosedBy != "" {
		by = " by " + escape(r.PRClosedBy)
	}
	return fmt.Sprintf("⚪ PR %s%s; incident closed", closedAs(r), by)
}

// jobList is ": a, b", or "" when the failed jobs are unknown.
func jobList(jobs []string) string {
	if len(jobs) == 0 {
		return ""
	}
	escaped := make([]string, len(jobs))
	for i, j := range jobs {
		escaped[i] = escape(j)
	}
	return ": " + strings.Join(escaped, ", ")
}

// commitAndRun is the short SHA linked to the commit, then a link to the run.
func commitAndRun(r Run) string {
	repoURL := r.ServerURL + "/" + r.Repo
	short := r.SHA[:min(7, len(r.SHA))]
	return link(repoURL+"/commit/"+r.SHA, short) + " · " + link(repoURL+"/actions/runs/"+r.RunID, "run")
}
