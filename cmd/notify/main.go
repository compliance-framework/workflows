// Command notify posts failed CI runs to Slack for the notify-failure reusable workflow,
// which runs it in two steps around a dedupe-marker lookup:
//
//	notify plan                    write notify=true|false, reason and key to $GITHUB_OUTPUT
//	notify post --reason <reason>  post the message to $SLACK_CHANNEL
//
// Both read the run from the GITHUB_* variables and the event payload. plan uses GH_TOKEN
// (actions: read) to find the previous run on the default branch. post uses
// SLACK_BOT_TOKEN and does nothing without it. The rules are in internal/notify.
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
		return plan(ctx, getenv, stdout)
	case len(args) == 3 && args[0] == "post" && args[1] == "--reason":
		return post(ctx, args[2], getenv, stdout)
	}
	return errors.New("usage: notify plan | notify post --reason <reason>")
}

func plan(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	out := getenv("GITHUB_OUTPUT")
	if out == "" {
		return errors.New("GITHUB_OUTPUT is not set")
	}
	r, err := notify.RunFromEnv(getenv)
	if err != nil {
		return err
	}
	gh := &notify.GitHub{BaseURL: envOr(getenv, "GITHUB_API_URL", "https://api.github.com"), Token: getenv("GH_TOKEN")}
	reason, err := notify.Decide(r, func() (string, error) {
		return gh.PreviousConclusion(ctx, r.Repo, r.RunID, r.Branch)
	})
	if err != nil {
		return err
	}
	key := notify.DedupeKey(r)
	fmt.Fprintf(stdout, "%s %s on %q at %s: reason %q, dedupe key %s\n", r.Workflow, r.EventName, r.Branch, r.SHA, reason, key)
	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "notify=%t\nreason=%s\nkey=%s\n", reason != notify.ReasonNone, reason, key)
	return errors.Join(err, f.Close())
}

func post(ctx context.Context, reasonArg string, getenv func(string) string, stdout io.Writer) error {
	token := getenv("SLACK_BOT_TOKEN")
	if token == "" {
		fmt.Fprintln(stdout, "SLACK_BOT_TOKEN is not set; nothing to post")
		return nil
	}
	reason, err := notify.ParseReason(reasonArg)
	if err != nil {
		return err
	}
	channel := getenv("SLACK_CHANNEL")
	if channel == "" {
		return errors.New("SLACK_CHANNEL is empty: pass the channel input or set the SLACK_CHANNEL_CI_FAILURES variable")
	}
	r, err := notify.RunFromEnv(getenv)
	if err != nil {
		return err
	}
	slack := &notify.Slack{BaseURL: envOr(getenv, "SLACK_API_URL", "https://slack.com/api"), Token: token}
	if err := slack.PostMessage(ctx, channel, notify.Message(r, reason)); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "posted to %s\n", channel)
	return nil
}

func envOr(getenv func(string) string, name, def string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return def
}
