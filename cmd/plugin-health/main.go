// Command plugin-health regenerates the "Plugin health" issue, one row per kept plugin, for the
// plugin-health workflow:
//
//	plugin-health plan [--manifest repos.yaml] [--event E --workflow W]
//	plugin-health report [--manifest repos.yaml] [--dry-run] [--owner org] [--registry ghcr.io]
//
// plan prints, as GITHUB_OUTPUT lines, repos= (the agent and the plugins, to scope the read-only
// token) and due= (whether this run regenerates the report: after a train run only once a train
// issue has closed since the last report; it reads the issues with ISSUE_TOKEN then).
//
// report reads the plugins with the read-only token in GH_TOKEN and the images anonymously,
// prints the table, and unless --dry-run writes it to the issue in GITHUB_REPOSITORY with
// ISSUE_TOKEN and posts to SLACK_CHANNEL with SLACK_BOT_TOKEN when the deprecation candidates
// changed. It fails, after publishing, if a read failed. The rules are in internal/pluginhealth;
// docs/plugin-health.md has the details.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/manifest"
	"github.com/compliance-framework/workflows/internal/notify"
	"github.com/compliance-framework/workflows/internal/pluginhealth"
)

func main() {
	if err := run(context.Background(), os.Args[1:], live(os.Getenv), os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "::error::plugin-health: "+err.Error())
		os.Exit(1)
	}
}

// deps are the environment and the clients run uses; tests replace the clients with fakes.
type deps struct {
	getenv  func(string) string
	now     func() time.Time
	source  func(owner string) pluginhealth.Source
	tracker func(owner, repo string) pluginhealth.Tracker
	slack   func() pluginhealth.Poster // nil without SLACK_BOT_TOKEN
}

func live(getenv func(string) string) deps {
	api := envOr(getenv, "GITHUB_API_URL", "https://api.github.com")
	return deps{
		getenv: getenv,
		now:    time.Now,
		source: func(owner string) pluginhealth.Source {
			return pluginhealth.Live{GitHub: &pluginhealth.GitHub{BaseURL: api, Token: getenv("GH_TOKEN"), Owner: owner}}
		},
		tracker: func(owner, repo string) pluginhealth.Tracker {
			return &pluginhealth.GitHub{BaseURL: api, Token: getenv("ISSUE_TOKEN"), Owner: owner, Repo: repo}
		},
		slack: func() pluginhealth.Poster {
			if getenv("SLACK_BOT_TOKEN") == "" {
				return nil
			}
			return &notify.Slack{BaseURL: envOr(getenv, "SLACK_API_URL", "https://slack.com/api"), Token: getenv("SLACK_BOT_TOKEN")}
		},
	}
}

const usage = "usage: plugin-health plan|report [flags]"

func run(ctx context.Context, args []string, d deps, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "plan" && args[0] != "report") {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("plugin-health "+args[0], flag.ContinueOnError)
	path := fs.String("manifest", manifest.DefaultPath, "path to the repo manifest")
	owner := fs.String("owner", "compliance-framework", "the repos' owner")
	event := fs.String("event", "", "plan: the event that started the run (github.event_name)")
	workflow := fs.String("workflow", "", "plan: for workflow_run, the workflow that completed")
	dryRun := fs.Bool("dry-run", false, "report: print the table only")
	registry := fs.String("registry", "ghcr.io", "report: the plugins' OCI registry")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	m, err := manifest.Load(*path)
	if err != nil {
		return err
	}
	plugins, agent, err := pluginhealth.Plugins(m)
	if err != nil {
		return err
	}
	title := pluginhealth.Title(*path)
	issueRepo := d.getenv("GITHUB_REPOSITORY")

	if args[0] == "plan" {
		due, reason := true, "triggered by "+*event
		if *event == "workflow_run" && *workflow == "train" {
			if issueRepo == "" || d.getenv("ISSUE_TOKEN") == "" {
				return errors.New("GITHUB_REPOSITORY and ISSUE_TOKEN are needed after a train run")
			}
			if due, reason, err = pluginhealth.Due(ctx, d.tracker(*owner, issueRepo), title, *event, *workflow); err != nil {
				return err
			}
		}
		fmt.Fprintf(stderr, "due=%t: %s\n", due, reason)
		_, err := fmt.Fprintf(stdout, "repos=%s\ndue=%t\n", strings.Join(append([]string{agent}, plugins...), ","), due)
		return err
	}

	if d.getenv("GH_TOKEN") == "" {
		return errors.New("GH_TOKEN is not set")
	}
	rep, err := pluginhealth.Collect(ctx, d.source(*owner), *owner, agent, *registry, plugins)
	if err != nil {
		return err
	}
	server := strings.TrimRight(envOr(d.getenv, "GITHUB_SERVER_URL", "https://github.com"), "/")
	h := pluginhealth.Header{Manifest: *path, Batches: m.ActiveBatches, Now: d.now(),
		Docs: server + "/" + envOr(d.getenv, "GITHUB_REPOSITORY", *owner+"/workflows") + "/blob/main/docs/plugin-health.md"}
	if id := d.getenv("GITHUB_RUN_ID"); issueRepo != "" && id != "" {
		h.Run = server + "/" + issueRepo + "/actions/runs/" + id
	}
	fmt.Fprint(stdout, pluginhealth.Markdown(rep, pluginhealth.State{Candidates: rep.Candidates()}, h))

	if *dryRun {
		fmt.Fprintln(stderr, "dry run: the issue is not touched and nothing is posted")
	} else {
		if issueRepo == "" || d.getenv("ISSUE_TOKEN") == "" {
			return errors.New("GITHUB_REPOSITORY and ISSUE_TOKEN are needed to write the issue (or pass --dry-run)")
		}
		var slack pluginhealth.Poster
		if d.slack != nil {
			slack = d.slack()
		}
		if _, err := pluginhealth.Publish(ctx, d.tracker(*owner, issueRepo), slack, d.getenv("SLACK_CHANNEL"), title, rep, h, stderr); err != nil {
			return err
		}
	}
	if errs := rep.Errors(); len(errs) > 0 {
		return fmt.Errorf("could not read: %s", strings.Join(errs, "; "))
	}
	return nil
}

func envOr(getenv func(string) string, name, def string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return def
}
