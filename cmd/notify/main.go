// Command notify reports failed CI runs to Slack. The notify-failure reusable workflow runs
// it in two steps, with a dedupe-marker lookup in between:
//
//	notify plan   decide whether this failed run is reported; writes notify=true|false,
//	              reason=<reason> and key=<dedupe key> to $GITHUB_OUTPUT
//	notify post   post the message for --reason to $SLACK_CHANNEL
//
// Both read the run from the GITHUB_* variables and the event payload. plan uses GH_TOKEN
// (actions: read) to look up the previous run on the default branch; post uses
// SLACK_BOT_TOKEN and does nothing when it is empty. See internal/notify for the rules.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/compliance-framework/workflows/internal/notify"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "notify:", err)
		os.Exit(1)
	}
}

const usage = "usage: notify plan | notify post --reason <reason>"

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "plan":
		return plan(ctx, args[1:], getenv, stdout, stderr)
	case "post":
		return post(ctx, args[1:], getenv, stdout, stderr)
	}
	return fmt.Errorf("unknown command %q; %s", args[0], usage)
}

func plan(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("github-api-url", envOr(getenv, "GITHUB_API_URL", "https://api.github.com"), "GitHub REST API base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	r, err := notify.RunFromEnv(getenv)
	if err != nil {
		return err
	}
	gh := &notify.GitHub{BaseURL: *apiURL, Token: getenv("GH_TOKEN")}
	reason, err := notify.Decide(r, func() (string, error) {
		return gh.PreviousConclusion(ctx, r.Repo, r.RunID, r.Branch)
	})
	if err != nil {
		return err
	}
	key := notify.DedupeKey(r.Repo, r.SHA, r.Workflow)
	if reason == notify.ReasonNone {
		fmt.Fprintf(stdout, "not reporting: %s %s on %q (%s) is not a release-bot PR, an automation branch, or a newly broken %s\n",
			r.Workflow, r.EventName, r.Branch, r.SHA, r.DefaultBranch)
	} else {
		fmt.Fprintf(stdout, "reporting (%s): %s on %q (%s), dedupe key %s\n", reason, r.Workflow, r.Branch, r.SHA, key)
	}
	return writeOutputs(getenv("GITHUB_OUTPUT"),
		"notify", fmt.Sprint(reason != notify.ReasonNone),
		"reason", string(reason),
		"key", key)
}

func post(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("post", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reasonFlag := fs.String("reason", "", "why the run is reported (the reason output of plan)")
	apiURL := fs.String("slack-api-url", envOr(getenv, "SLACK_API_URL", "https://slack.com/api"), "Slack Web API base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	token := getenv("SLACK_BOT_TOKEN")
	if token == "" {
		fmt.Fprintln(stdout, "SLACK_BOT_TOKEN is not set; nothing to post")
		return nil
	}
	reason, err := notify.ParseReason(*reasonFlag)
	if err != nil {
		return err
	}
	channel := getenv("SLACK_CHANNEL")
	if channel == "" {
		return errors.New("SLACK_CHANNEL is not set: pass the channel input or set the SLACK_CHANNEL_CI_FAILURES variable")
	}
	r, err := notify.RunFromEnv(getenv)
	if err != nil {
		return err
	}
	slack := &notify.Slack{BaseURL: *apiURL, Token: token}
	if err := slack.PostMessage(ctx, channel, notify.Message(r, reason)); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "posted to %s\n", channel)
	return nil
}

// writeOutputs appends name=value pairs (given as alternating arguments) to the step's
// GITHUB_OUTPUT file. Values never contain newlines.
func writeOutputs(path string, pairs ...string) error {
	if path == "" {
		return errors.New("GITHUB_OUTPUT is not set")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		if _, err := fmt.Fprintf(f, "%s=%s\n", pairs[i], pairs[i+1]); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

func envOr(getenv func(string) string, name, def string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return def
}
