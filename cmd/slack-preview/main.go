// Command slack-preview posts sample messages to the real CCF Slack channels, for the
// slack-preview workflow, so the message designs can be seen in Slack before the tools use them:
//
//	slack-preview [--which all|ci-failures|needs-human|releases|digests] [--pause 10s] [--dry-run]
//
// Every sample is built with internal/slackkit's cards, as the tools build theirs, and marked
// ":eyes: Preview". ci-failures, needs-human and releases also edit their card in place after
// --pause, as the tools will. It posts with SLACK_BOT_TOKEN to the channels in
// SLACK_CHANNEL_CI_FAILURES, SLACK_CHANNEL_NEEDS_HUMAN, SLACK_CHANNEL_RELEASES and
// SLACK_CHANNEL_DIGESTS; with --which all, a sample whose channel is unset is skipped.
// --dry-run prints the calls instead (docs/slack.md).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/slackkit"
)

func main() {
	err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, newClient, time.Sleep, time.Now)
	if err != nil {
		fmt.Fprintln(os.Stderr, "::error::slack-preview: "+err.Error())
		os.Exit(1)
	}
}

func newClient(getenv func(string) string) slackkit.API {
	return &slackkit.Client{BaseURL: getenv("SLACK_API_URL"), Token: getenv("SLACK_BOT_TOKEN")}
}

// sample is one channel's preview.
type sample struct {
	name, channelVar string
	post             func(p *previewer, channel string) error
}

var samples = []sample{
	{"ci-failures", "SLACK_CHANNEL_CI_FAILURES", (*previewer).ciFailures},
	{"needs-human", "SLACK_CHANNEL_NEEDS_HUMAN", (*previewer).needsHuman},
	{"releases", "SLACK_CHANNEL_RELEASES", (*previewer).releases},
	{"digests", "SLACK_CHANNEL_DIGESTS", (*previewer).digests},
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer,
	client func(func(string) string) slackkit.API, sleep func(time.Duration), now func() time.Time) error {
	fs := flag.NewFlagSet("slack-preview", flag.ContinueOnError)
	which := fs.String("which", "all", "the samples to post: all, ci-failures, needs-human, releases or digests")
	pause := fs.Duration("pause", 10*time.Second, "the wait before each edit in place")
	dryRun := fs.Bool("dry-run", false, "print the calls instead of posting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	names := []string{"all"}
	for _, s := range samples {
		names = append(names, s.name)
	}
	if !slices.Contains(names, *which) {
		return fmt.Errorf("--which %q: want one of %s", *which, strings.Join(names, ", "))
	}
	var api slackkit.API
	var fake *slackkit.Fake
	switch {
	case *dryRun:
		fake = &slackkit.Fake{}
		api = fake
	case getenv("SLACK_BOT_TOKEN") == "":
		return errors.New("SLACK_BOT_TOKEN is not set")
	default:
		api = client(getenv)
	}
	p := &previewer{ctx: ctx, api: api, sleep: sleep, pause: *pause, now: now().UTC()}
	posted := 0
	for _, s := range samples {
		if *which != "all" && *which != s.name {
			continue
		}
		channel := getenv(s.channelVar)
		if channel == "" {
			if *which != "all" {
				return fmt.Errorf("%s is not set", s.channelVar)
			}
			fmt.Fprintf(stdout, "::warning::%s is not set; skipping the %s sample\n", s.channelVar, s.name)
			continue
		}
		if err := s.post(p, channel); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		fmt.Fprintf(stdout, "posted the %s sample to %s\n", s.name, channel)
		posted++
	}
	if fake != nil {
		for _, c := range fake.Calls {
			fmt.Fprintf(stdout, "%s %s %s: %s\n", c.Method, c.Channel, c.TS, c.Message.Text)
		}
	}
	if posted == 0 {
		return errors.New("no sample channel is set")
	}
	return nil
}

type previewer struct {
	ctx   context.Context
	api   slackkit.API
	sleep func(time.Duration)
	pause time.Duration
	now   time.Time
}

const gh = "https://github.com/compliance-framework/"

func (p *previewer) reply(at slackkit.Posted, m slackkit.Message) error {
	_, err := p.api.Reply(p.ctx, at.Channel, at.TS, m.Preview())
	return err
}

// ciFailures posts a failing incident and a failure reply, then resolves it in place.
func (p *previewer) ciFailures(channel string) error {
	in := slackkit.Incident{
		Repo: "compliance-framework/mock-api", Ref: "compliance-framework/mock-api#12", RefURL: gh + "mock-api/pull/12",
		Title: "fix(deps): update module github.com/compliance-framework/mock-gooci to v0.4.0", Workflow: "ci",
		Status: slackkit.Failing, SHA: "2222222", SHAURL: gh + "mock-api/commit/2222222",
		FailedJobs: []string{"ci / go / test", "ci / required"}, OpenedAt: p.now.Add(-time.Hour),
		PRURL: gh + "mock-api/pull/12", RunURL: gh + "mock-api/actions", UpdatedAt: p.now,
	}
	card, err := p.api.Post(p.ctx, channel, slackkit.IncidentCard(in).Preview())
	if err != nil {
		return err
	}
	if err := p.reply(card, slackkit.Note(":x: Failed: `ci / go / test`, `ci / required` at `2222222`", slackkit.LinkTo(in.RunURL, "run"))); err != nil {
		return err
	}
	p.sleep(p.pause)
	if err := p.reply(card, slackkit.Note(":white_check_mark: Passing again at `4444444`", slackkit.LinkTo(in.RunURL, "run"))); err != nil {
		return err
	}
	in.Status, in.ResolvedAt, in.UpdatedAt = slackkit.Resolved, p.now, p.now
	return p.api.Update(p.ctx, card.Channel, card.TS, slackkit.IncidentCard(in).Preview())
}

// needsHuman posts a Renovate major's card, then marks it handled in place.
func (p *previewer) needsHuman(channel string) error {
	n := slackkit.NeedsHuman{
		Repo: "compliance-framework/mock-agent", Ref: "compliance-framework/mock-agent#34", URL: gh + "mock-agent/pull/34",
		Title: "fix(deps): update module github.com/compliance-framework/mock-api to v2", Why: "Renovate major update",
		CI: ":white_check_mark: Passing", OpenedBy: "ccf-release-bot[bot]", OpenedAt: p.now.Add(-51 * time.Hour), Now: p.now,
	}
	card, err := p.api.Post(p.ctx, channel, slackkit.NeedsHumanCard(n).Preview())
	if err != nil {
		return err
	}
	p.sleep(p.pause)
	n.Handled, n.HandledBy, n.HandledAt = "merged", "octocat", p.now
	return p.api.Update(p.ctx, card.Channel, card.TS, slackkit.NeedsHumanCard(n).Preview())
}

// releases posts a train board at stage 1 and moves it through stages 2 and 3 to finished, with
// a thread reply per event.
func (p *previewer) releases(channel string) error {
	repo := func(name string, pill slackkit.Pill, status, version string) slackkit.TrainRepo {
		return slackkit.TrainRepo{Name: name, URL: gh + name, Pill: pill, Status: status, Version: version}
	}
	done, run, wait := slackkit.PillDone, slackkit.PillRunning, slackkit.PillPending
	t := slackkit.Train{Title: "Release train " + p.now.Format("2006-01"), Stage: 1, IssueURL: gh + "workflows/issues", UpdatedAt: p.now,
		Stages: [][]slackkit.TrainRepo{
			{repo("mock-api", run, "publishing", "v1.4.0"), repo("mock-gooci", done, "released", "v0.4.0")},
			{repo("mock-agent", wait, "waiting", ""), repo("mock-ui", wait, "waiting", "")},
			{repo("mock-agent-action", wait, "waiting", ""), repo("mock-plugin-1", wait, "waiting", "")},
			{repo("mock-helm-charts", wait, "waiting", "")},
		}}
	board, err := p.api.Post(p.ctx, channel, slackkit.TrainBoard(t).Preview())
	if err != nil {
		return err
	}
	steps := []struct {
		event string
		apply func()
	}{
		{":large_green_circle: `mock-api` released `v1.4.0`; stage 2 started", func() {
			t.Stage, t.Stages[0][0] = 2, repo("mock-api", done, "released", "v1.4.0")
			t.Stages[1] = []slackkit.TrainRepo{repo("mock-agent", run, "merging", "v0.9.0"), repo("mock-ui", run, "release-pr", "")}
		}},
		{":large_green_circle: `mock-agent` and `mock-ui` released; stage 3 started", func() {
			t.Stage = 3
			t.Stages[1] = []slackkit.TrainRepo{repo("mock-agent", done, "released", "v0.9.0"), repo("mock-ui", done, "released", "v0.2.0")}
			t.Stages[2] = []slackkit.TrainRepo{repo("mock-agent-action", run, "bumping", ""), repo("mock-plugin-1", run, "publishing", "v0.3.0")}
		}},
		{":checkered_flag: Train finished: 6 repos released", func() {
			t.Stage, t.Finished = 4, true
			t.Stages[2] = []slackkit.TrainRepo{repo("mock-agent-action", done, "released", "v0.5.0"), repo("mock-plugin-1", done, "released", "v0.3.0")}
			t.Stages[3] = []slackkit.TrainRepo{repo("mock-helm-charts", done, "released", "v2.1.0")}
		}},
	}
	for _, s := range steps {
		p.sleep(p.pause)
		s.apply()
		t.UpdatedAt = p.now
		if err := p.api.Update(p.ctx, board.Channel, board.TS, slackkit.TrainBoard(t).Preview()); err != nil {
			return err
		}
		if err := p.reply(board, slackkit.Note(s.event)); err != nil {
			return err
		}
	}
	return nil
}

// digests posts a draft release digest.
func (p *previewer) digests(channel string) error {
	release := func(name, from, to string) slackkit.DigestRepo {
		return slackkit.DigestRepo{Name: name, From: from, To: to, ChangelogURL: gh + name + "/releases/tag/" + to}
	}
	d := slackkit.Digest{Title: "Release digest " + p.now.Format("2006-01"), Draft: true, IssueURL: gh + "workflows/issues",
		Repos: []slackkit.DigestRepo{
			release("mock-api", "v1.3.2", "v1.4.0"), release("mock-gooci", "v0.3.1", "v0.4.0"),
			release("mock-agent", "v0.8.4", "v0.9.0"), release("mock-ui", "v0.1.3", "v0.2.0"),
			release("mock-helm-charts", "v2.0.1", "v2.1.0"),
		},
		Highlights: []string{
			"*mock-api*: evidence can be streamed in batches",
			"*mock-agent*: plugins report the agent library they were built with",
			"*mock-ui*: the dashboard shows each control's latest evidence",
		}}
	_, err := p.api.Post(p.ctx, channel, slackkit.DigestCard(d).Preview())
	return err
}
