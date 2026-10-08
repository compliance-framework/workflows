// Command notify keeps one Slack thread per CI incident for the notify-failure reusable
// workflow, which runs it in two steps around restoring the incident state from the
// Actions cache:
//
//	notify plan         write notify=true|false, reason, key, restore-key, needs-human=true|false
//	                    and needs-human-key to $GITHUB_OUTPUT
//	notify post         post to Slack as the incident state at $STATE_FILE and the run's result
//	                    say, write the next state there and save=true|false to $GITHUB_OUTPUT
//	notify needs-human  post the run's PR to NEEDS_HUMAN_CHANNEL, write the record to
//	                    $NEEDS_HUMAN_STATE_FILE and save=true|false to $GITHUB_OUTPUT
//
// NEEDS_HUMAN_CHANNEL (the SLACK_CHANNEL_NEEDS_HUMAN variable) turns on the needs-human rule,
// which takes a release PR's release-checks failure away from the incidents (notify.Route);
// the workflow posts a PR once, when the record's cache key is missing.
//
// Both read the run from the GITHUB_* variables and the event payload, and its result from
// NEEDS (the caller's toJSON(needs); empty means a failure). post uses SLACK_BOT_TOKEN and
// does nothing without it; it posts new incidents to SLACK_CHANNEL. The rules are in
// internal/notify.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/compliance-framework/workflows/internal/notify"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "notify:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	switch {
	case len(args) == 1 && args[0] == "plan":
		return plan(getenv, stdout)
	case len(args) == 1 && args[0] == "post":
		return post(ctx, getenv, stdout)
	case len(args) == 1 && args[0] == "needs-human":
		return needsHuman(ctx, getenv, stdout)
	}
	return errors.New("usage: notify plan | notify post | notify needs-human")
}

func plan(getenv func(string) string, stdout io.Writer) error {
	r, res, err := load(getenv)
	if err != nil {
		return err
	}
	res, human := notify.Route(r, res, getenv("NEEDS_HUMAN_CHANNEL") != "")
	reason := notify.Decide(r)
	track := reason != notify.ReasonNone && res.Outcome != notify.OutcomeNone
	fmt.Fprintf(stdout, "%s %s on %q at %s: reason %q, outcome %s, failed jobs %v, incident %s, needs a human: %q\n",
		r.Workflow, r.EventName, r.Branch, r.SHA, reason, res.Outcome, res.FailedJobs, notify.IncidentKey(r), human)
	return writeOutputs(getenv, fmt.Sprintf("notify=%t\nreason=%s\nkey=%s\nrestore-key=%s\nneeds-human=%t\nneeds-human-key=%s\n",
		track, reason, notify.StateKey(r), notify.IncidentKey(r), human != "", notify.NeedsHumanKey(r)))
}

func post(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	token := getenv("SLACK_BOT_TOKEN")
	if token == "" {
		fmt.Fprintln(stdout, "SLACK_BOT_TOKEN is not set; nothing to post")
		return nil
	}
	statePath := getenv("STATE_FILE")
	if statePath == "" {
		return errors.New("STATE_FILE is not set")
	}
	r, res, err := load(getenv)
	if err != nil {
		return err
	}
	if notify.Decide(r) == notify.ReasonNone {
		fmt.Fprintln(stdout, "this run is not tracked; nothing to post")
		return writeOutputs(getenv, "save=false\n")
	}
	res, _ = notify.Route(r, res, getenv("NEEDS_HUMAN_CHANNEL") != "")
	key := notify.IncidentKey(r)
	prev, err := notify.LoadIncident(statePath, key)
	if err != nil {
		fmt.Fprintf(stdout, "ignoring the restored incident state: %v\n", err)
	}
	slack := &notify.Slack{BaseURL: envOr(getenv, "SLACK_API_URL", "https://slack.com/api"), Token: token}
	next, action, err := notify.Handle(ctx, slack, key, prev, r, res, getenv("SLACK_CHANNEL"))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: %s (incident open: %t, thread %s in %s)\n", res.Outcome, action, next.Open, next.TS, next.Channel)
	save := action != notify.ActionNone && action != notify.ActionDedupe
	if save {
		if err := next.Save(statePath); err != nil {
			return err
		}
	}
	return writeOutputs(getenv, fmt.Sprintf("save=%t\n", save))
}

// needsHuman posts the run's PR to NEEDS_HUMAN_CHANNEL when it needs a human. The workflow runs
// it only when the PR was not posted before. Without the channel or the token it does nothing.
func needsHuman(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	token, channel := getenv("SLACK_BOT_TOKEN"), getenv("NEEDS_HUMAN_CHANNEL")
	if token == "" || channel == "" {
		fmt.Fprintln(stdout, "SLACK_BOT_TOKEN or the SLACK_CHANNEL_NEEDS_HUMAN variable is not set; nothing to post")
		return writeOutputs(getenv, "save=false\n")
	}
	statePath := getenv("NEEDS_HUMAN_STATE_FILE")
	if statePath == "" {
		return errors.New("NEEDS_HUMAN_STATE_FILE is not set")
	}
	r, res, err := load(getenv)
	if err != nil {
		return err
	}
	_, reason := notify.Route(r, res, true)
	if reason == "" {
		fmt.Fprintln(stdout, "this PR doesn't need a human; nothing to post")
		return writeOutputs(getenv, "save=false\n")
	}
	slack := &notify.Slack{BaseURL: envOr(getenv, "SLACK_API_URL", "https://slack.com/api"), Token: token}
	rec, err := notify.PostNeedsHuman(ctx, slack, r, reason, channel)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "posted PR #%d (%s) to %s\n", r.PRNumber, reason, rec.Channel)
	if err := rec.Save(statePath); err != nil {
		return err
	}
	return writeOutputs(getenv, "save=true\n")
}

// load reads the run and its result.
func load(getenv func(string) string) (notify.Run, notify.Result, error) {
	r, err := notify.RunFromEnv(getenv)
	if err != nil {
		return notify.Run{}, notify.Result{}, err
	}
	res, err := notify.ParseNeeds(getenv("NEEDS"))
	return r, res, err
}

func writeOutputs(getenv func(string) string, lines string) error {
	out := getenv("GITHUB_OUTPUT")
	if out == "" {
		return errors.New("GITHUB_OUTPUT is not set")
	}
	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(lines)
	return errors.Join(err, f.Close())
}

func envOr(getenv func(string) string, name, def string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return def
}
