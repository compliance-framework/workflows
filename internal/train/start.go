package train

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/compliance-framework/workflows/internal/slackkit"
)

// Start opens this month's train, or reconciles the train still open. A dry run plans every
// stage and closes its issue in the same run.
func (e *Engine) Start(ctx context.Context, o StartOptions) error {
	now := e.Now().UTC()
	month := now.Format("2006-01")
	issues, err := e.Tracker.Issues(ctx, LabelTrain)
	if err != nil {
		return err
	}
	for _, is := range issues {
		if is.Open && slices.Contains(is.Labels, LabelOpen) {
			e.logf("%s (#%d) is still open; reconciling it instead of starting a train", is.Title, is.Number)
			return e.run(ctx, is)
		}
	}
	m, err := e.Load(o.Manifest)
	if err != nil {
		return err
	}
	var titles []string
	for _, is := range issues {
		titles = append(titles, is.Title)
	}
	if o.Scheduled {
		day := m.NextWorkingWeekday(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC))
		if day.YearDay() != now.YearDay() {
			e.logf("this month's train day is %s; nothing to do", day.Format(time.DateOnly))
			return nil
		}
		if t := Title(month, o.DryRun, nil); slices.Contains(titles, t) {
			e.logf("%q exists; nothing to do", t)
			return nil
		}
	}
	st, err := e.newState(ctx, m, month, o)
	if err != nil {
		return err
	}
	body, err := Render(st, e.ReposURL, issueHelp)
	if err != nil {
		return err
	}
	labels := []string{LabelTrain, LabelOpen}
	if o.DryRun {
		labels = append(labels, LabelDryRun)
	}
	is, err := e.Tracker.CreateIssue(ctx, Title(month, o.DryRun, titles), body, labels)
	if err != nil {
		return err
	}
	e.logf("opened %s: %s", is.Title, is.URL)
	if p, err := e.post(ctx, st, "", Board(st, e.ReposURL, is.URL, e.Now())); err == nil {
		st.Channel, st.ThreadTS, st.Board = cmp.Or(p.Channel, st.Channel), p.TS, boardHash(st, e.ReposURL, is.URL)
	}
	if st.DryRun {
		return errors.Join(e.plan(ctx, is, st), e.save(ctx, is, st))
	}
	return e.step(ctx, is, st)
}

// escalate posts once, as a new message in the channel, that a train is still open in a later
// month.
func (e *Engine) escalate(ctx context.Context, is Issue, st *State) {
	now := e.Now().UTC().Format("2006-01")
	if st.Month >= now || slices.Contains(st.Notified, "escalate|"+now) {
		return
	}
	if _, err := e.post(ctx, st, "", slackkit.Note(fmt.Sprintf(":rotating_light: *%s* is still open at the start of %s. Finish it, or `/abort` it, so the next train can start: <%s|tracking issue>.",
		is.Title, now, is.URL))); err == nil || e.Slack == nil || st.Channel == "" {
		st.MarkNotified("escalate|" + now)
	}
}
