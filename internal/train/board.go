package train

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/slackkit"
)

// Board is the train's #ccf-releases board, its start message: every stage with each repo's
// pill, status and version, "Stage n of m", and the tracking issue. The engine edits it
// whenever it changes.
func Board(st *State, reposURL, issueURL string, now time.Time) slackkit.Message {
	t := slackkit.Train{
		Title: "Release train " + st.Month, Stage: st.Stages(), DryRun: st.DryRun, IssueURL: issueURL, UpdatedAt: now,
		Finished: st.Status == StatusFinished, Aborted: st.Status == StatusAborted,
	}
	for n := 1; n <= st.Stages(); n++ { // the running stage: the first not done
		if !stageDone(st, n) {
			t.Stage = n
			break
		}
	}
	for n := 1; n <= st.Stages(); n++ {
		var repos []slackkit.TrainRepo
		for _, r := range st.Repos {
			if r.Stage != n {
				continue
			}
			tr := slackkit.TrainRepo{Name: r.Name, Status: r.Status(), Version: Versions(r.Versions), Pill: slackkit.PillRunning}
			if reposURL != "" {
				tr.URL = strings.TrimRight(reposURL, "/") + "/" + r.Name
			}
			switch {
			case r.Phase == Skipped:
				tr.Pill = slackkit.PillSkipped
			case r.Phase == Released:
				tr.Pill = slackkit.PillDone
			case r.Hold != NoHold:
				tr.Pill = slackkit.PillHeld
			case r.Phase == Waiting:
				tr.Pill = slackkit.PillPending
			}
			repos = append(repos, tr)
		}
		t.Stages = append(t.Stages, repos)
	}
	return slackkit.TrainBoard(t)
}

// stageDone reports whether every repo of stage n is released or skipped.
func stageDone(st *State, n int) bool {
	for _, r := range st.Repos {
		if r.Stage == n && !r.Phase.Done() {
			return false
		}
	}
	return true
}

// boardHash identifies the board's content, its update time left out.
func boardHash(st *State, reposURL, issueURL string) string {
	data, _ := json.Marshal(Board(st, reposURL, issueURL, time.Time{})) // plain structs: never fails
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// updateBoard edits the board when its content changed since it was last posted. A failed edit
// is logged and retried on the next run.
func (e *Engine) updateBoard(ctx context.Context, is Issue, st *State) {
	if e.Slack == nil || st.Channel == "" || st.ThreadTS == "" {
		return
	}
	hash := boardHash(st, e.ReposURL, is.URL)
	if hash == st.Board {
		return
	}
	if err := e.Slack.Update(ctx, st.Channel, st.ThreadTS, Board(st, e.ReposURL, is.URL, e.Now())); err != nil {
		e.logf("::warning::updating the Slack board failed: %v", err)
		return
	}
	st.Board = hash
}
