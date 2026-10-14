package pluginhealth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// IssueAuthor opens the issue (the workflow's GITHUB_TOKEN); only its issue is ever edited, so
// nobody else's issue titled the same, or a state marker they write, is trusted.
const IssueAuthor = "github-actions[bot]"

// Title is the issue's title for a manifest: "Plugin health" for repos.yaml; a run on any other
// manifest (--manifest, locally) never overwrites it.
func Title(manifestPath string) string {
	if manifestPath == manifest.DefaultPath {
		return "Plugin health"
	}
	return "Plugin health (" + manifestPath + ")"
}

// Plugins returns the kept plugins, the active go-plugin repos with release: true, in manifest
// order, and the agent repo they depend on (their one go-service dependency).
func Plugins(m *manifest.Manifest) (plugins []string, agent string, err error) {
	for _, r := range m.ActiveRepos() {
		if r.Kind != manifest.KindGoPlugin || !r.Release {
			continue
		}
		plugins = append(plugins, r.Name)
		for _, d := range r.DependsOn {
			if dep := m.Repo(d); dep == nil || dep.Kind != manifest.KindGoService {
				continue
			}
			if agent != "" && agent != d {
				return nil, "", fmt.Errorf("plugins depend on two agents: %s and %s", agent, d)
			}
			agent = d
		}
	}
	if len(plugins) == 0 {
		return nil, "", errors.New("no active go-plugin repo with release: true")
	}
	if agent == "" {
		return nil, "", errors.New("the plugins depend on no go-service repo (the agent)")
	}
	return plugins, agent, nil
}

// Due reports whether a run should regenerate the report: always, but after a train run, which
// happens hourly, only when a train issue has closed since the report was last regenerated.
func Due(ctx context.Context, t Tracker, title, event, workflow string) (bool, string, error) {
	if event != "workflow_run" || workflow != "train" {
		return true, "triggered by " + event + " " + workflow, nil
	}
	last, err := t.LastTrain(ctx)
	if err != nil {
		return false, "", err
	}
	if last == 0 {
		return false, "no train has finished", nil
	}
	is, err := t.FindIssue(ctx, title, IssueAuthor)
	if err != nil {
		return false, "", err
	}
	if is != nil {
		if s, ok := ParseState(is.Body); ok && s.Train == last {
			return false, fmt.Sprintf("already regenerated after train #%d", last), nil
		}
	}
	return true, fmt.Sprintf("train #%d finished", last), nil
}

// Poster posts a plain-text Slack message (notify.Slack).
type Poster interface {
	PostMessage(ctx context.Context, channel, text string) error
}

// Publish regenerates the issue with the report and posts to channel when the deprecation
// candidates differ from those the issue listed (nil slack or "" channel: no post). A new issue
// is pinned; failing to pin is only logged.
func Publish(ctx context.Context, t Tracker, slack Poster, channel, title string, rep Report, h Header, log io.Writer) (*Issue, error) {
	last, err := t.LastTrain(ctx)
	if err != nil {
		return nil, err
	}
	is, err := t.FindIssue(ctx, title, IssueAuthor)
	if err != nil {
		return nil, err
	}
	var before []string
	if is != nil {
		prev, _ := ParseState(is.Body)
		before = prev.Candidates
	}
	after := rep.Candidates()
	for _, row := range rep.Rows {
		// A candidate whose reads failed this time stays one, so a flaky read posts nothing.
		if row.Status == Unknown && len(row.Errors) > 0 && slices.Contains(before, row.Repo) {
			after = append(after, row.Repo)
		}
	}
	body := Markdown(rep, State{Candidates: after, Train: last}, h)
	if is == nil {
		if is, err = t.CreateIssue(ctx, title, body); err != nil {
			return nil, err
		}
		fmt.Fprintf(log, "opened %s\n", is.URL)
		if err := t.PinIssue(ctx, is.NodeID); err != nil {
			fmt.Fprintf(log, "::warning::could not pin %s: %v\n", is.URL, err)
		}
	} else {
		if err := t.EditIssue(ctx, is.Number, body); err != nil {
			return nil, err
		}
		fmt.Fprintf(log, "updated %s\n", is.URL)
	}
	if !Changed(before, after) {
		fmt.Fprintf(log, "deprecation candidates unchanged: %v\n", slices.Sorted(slices.Values(after)))
		return is, nil
	}
	if slack == nil || channel == "" {
		fmt.Fprintln(log, "deprecation candidates changed; no Slack token or channel, nothing posted")
		return is, nil
	}
	if err := slack.PostMessage(ctx, channel, SlackMessage(before, after, is.URL)); err != nil {
		return is, fmt.Errorf("posting to Slack: %w", err)
	}
	fmt.Fprintf(log, "deprecation candidates changed; posted to %s\n", channel)
	return is, nil
}
