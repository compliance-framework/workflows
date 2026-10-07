// The incident model behind the notify-failure workflow: a failure opens an incident (a
// top-level Slack message) or replies in its thread, a pass closes it with a reply.

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
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
	FailedJobs []string `json:"failed_jobs"` // the failed jobs last posted
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

// Poster posts a Slack message, in the thread threadTS unless it is empty, and returns
// the channel ID and ts of the posted message.
type Poster interface {
	Post(ctx context.Context, channel, text, threadTS string) (postedChannel, ts string, err error)
}

// Action is what Handle did.
type Action string

const (
	ActionNone    Action = "none"    // nothing to post: no incident and a pass, or a cancelled run
	ActionDedupe  Action = "dedupe"  // the same commit and failed jobs were already posted
	ActionOpen    Action = "open"    // posted a new top-level message
	ActionReply   Action = "reply"   // posted another failure in the thread
	ActionRecover Action = "recover" // posted the recovery in the thread and closed the incident
)

// Handle applies the run r with result res to the incident prev (for key), posts what
// that needs, and returns the next state. The state changed, and must be saved, unless
// the action is ActionNone or ActionDedupe. channel is where a new incident is posted;
// replies go to the incident's own channel.
func Handle(ctx context.Context, p Poster, key string, prev Incident, r Run, res Result, channel string) (Incident, Action, error) {
	switch res.Outcome {
	case OutcomeFailure:
		if prev.Open && prev.SHA == r.SHA && slices.Equal(prev.FailedJobs, res.FailedJobs) {
			return prev, ActionDedupe, nil
		}
		if prev.Open {
			if _, _, err := p.Post(ctx, prev.Channel, FailureReply(r, res.FailedJobs), prev.TS); err != nil {
				return prev, ActionNone, err
			}
			next := prev
			next.SHA, next.FailedJobs = r.SHA, res.FailedJobs
			return next, ActionReply, nil
		}
		if channel == "" {
			return prev, ActionNone, errors.New("no Slack channel: pass the channel input or set the SLACK_CHANNEL_CI_FAILURES variable")
		}
		posted, ts, err := p.Post(ctx, channel, OpenMessage(r, res.FailedJobs), "")
		if err != nil {
			return prev, ActionNone, err
		}
		if ts == "" {
			return prev, ActionNone, errors.New("chat.postMessage returned no ts to thread replies on")
		}
		if posted == "" {
			posted = channel
		}
		return Incident{Key: key, Channel: posted, TS: ts, Open: true, SHA: r.SHA, FailedJobs: res.FailedJobs}, ActionOpen, nil
	case OutcomeSuccess:
		if !prev.Open {
			return prev, ActionNone, nil
		}
		if _, _, err := p.Post(ctx, prev.Channel, RecoveryReply(r), prev.TS); err != nil {
			return prev, ActionNone, err
		}
		next := prev
		next.Open, next.SHA, next.FailedJobs = false, r.SHA, nil
		return next, ActionRecover, nil
	}
	return prev, ActionNone, nil
}

// subject names what failed: "PR #12", or the pushed branch.
func subject(r Run) string {
	if r.PRNumber != 0 {
		return fmt.Sprintf("PR #%d", r.PRNumber)
	}
	return r.Branch
}

// OpenMessage is the top-level message of a new incident.
func OpenMessage(r Run, failedJobs []string) string {
	repoURL := r.ServerURL + "/" + r.Repo
	var b strings.Builder
	fmt.Fprintf(&b, "❌ %s %s failed%s at %s\n", escape(r.Repo), escape(subject(r)), jobList(failedJobs), commitAndRun(r))
	if r.PRNumber != 0 {
		label := strings.TrimSpace(fmt.Sprintf("#%d %s", r.PRNumber, r.PRTitle))
		fmt.Fprintf(&b, "Pull request %s · ", link(fmt.Sprintf("%s/pull/%d", repoURL, r.PRNumber), label))
	}
	fmt.Fprintf(&b, "workflow *%s*", escape(r.Workflow))
	return b.String()
}

// FailureReply is the thread reply for another failure of an open incident.
func FailureReply(r Run, failedJobs []string) string {
	return fmt.Sprintf("❌ failed%s at %s", jobList(failedJobs), commitAndRun(r))
}

// RecoveryReply is the thread reply that closes an incident.
func RecoveryReply(r Run) string {
	return "✅ passing again at " + commitAndRun(r)
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
