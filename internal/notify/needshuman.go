// The needs-human rule of the notify-failure workflow, separate from CI incidents: a tracked
// bot PR that waits for a person is posted once to the needs-human channel, as a card
// (docs/attention.md).

package notify

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/compliance-framework/workflows/internal/slackkit"
)

const (
	// NeedsHumanLabel marks a bot PR waiting for a person (Renovate and ccf-bump add it).
	NeedsHumanLabel = "needs-human"
	// ReleaseChecksJob is the caller's job that runs release-checks.yml.
	ReleaseChecksJob = "release-checks"
	// ReleaseBranchPrefix is the head branch prefix of release-please PRs.
	ReleaseBranchPrefix = "release-please--"
	// renovateOPABranch is the api's OPA PR (renovate/default.json's groupSlug opa).
	renovateOPABranch = "renovate/opa"
)

// ReasonReleaseBlocked is the needs-human reason of a release PR whose release-checks failed.
const ReasonReleaseBlocked = "release PR blocked by release-checks (e.g. needs release:major-approved, or an internal dep isn't final)"

// Route splits a run's result between the two rules. needsHuman is why the run's PR needs a
// human ("" for none); incident is the result the incident rules see. A tracked bot PR (rule
// (a) of Decide) needs a human when it carries the needs-human label, or when it is a
// release-please PR whose release-checks job failed. That release-checks failure is the
// needs-human channel's, not an incident: when it is the only failed job, the incident sees
// OutcomeNone and stays as it is. Without a needs-human channel (channel false) nothing is
// routed there and the incident sees the result unchanged.
func Route(r Run, res Result, channel bool) (incident Result, needsHuman string) {
	if !channel || r.PRNumber == 0 {
		return res, ""
	}
	if reason := Decide(r); reason != ReasonReleaseBotPR && reason != ReasonAutomationBranch {
		return res, ""
	}
	if strings.HasPrefix(r.Branch, ReleaseBranchPrefix) && res.Outcome == OutcomeFailure && slices.Contains(res.FailedJobs, ReleaseChecksJob) {
		if slices.Equal(res.FailedJobs, []string{ReleaseChecksJob}) {
			res = Result{Outcome: OutcomeNone}
		}
		return res, ReasonReleaseBlocked
	}
	if !r.PRNeedsHuman {
		return res, ""
	}
	switch {
	case r.Branch == renovateOPABranch:
		return res, "OPA update in the api (never auto-merged)"
	case strings.HasPrefix(r.Branch, "renovate/"):
		return res, "major update"
	case strings.HasPrefix(r.Branch, "ccf-bump/"):
		return res, "auto-merge off (a major update or an unversioned pin)"
	}
	return res, "labelled " + NeedsHumanLabel
}

// NeedsHumanKey is the Actions cache key of the record that r's PR was posted: one per repo
// + pull request, whatever the workflow, so the PR is posted once.
func NeedsHumanKey(r Run) string {
	sum := sha256.Sum256([]byte(r.Repo + "\x00pr\x00" + strconv.Itoa(r.PRNumber)))
	return "ccf-notify-needs-human-" + hex.EncodeToString(sum[:16])
}

// NeedsHumanCard is the card for r's PR: why it needs a person, its CI (res, the run's result)
// and how long it has been open.
func NeedsHumanCard(r Run, res Result, reason string, now time.Time) slackkit.Message {
	return slackkit.NeedsHumanCard(slackkit.NeedsHuman{
		Repo: r.Repo, Ref: fmt.Sprintf("%s#%d", r.Repo, r.PRNumber), URL: r.PRURL(), Title: r.PRTitle,
		Why: upperFirst(slackkit.Escape(reason)), CI: ciStatus(res), OpenedBy: r.PRAuthor, OpenedAt: r.PRCreatedAt, Now: now,
	})
}

// ciStatus is the CI field of a needs-human card.
func ciStatus(res Result) string {
	switch {
	case res.Outcome == OutcomeSuccess:
		return ":white_check_mark: Passing"
	case res.Outcome == OutcomeFailure && len(res.FailedJobs) > 0:
		return ":x: Failing: " + slackkit.Escape(strings.Join(res.FailedJobs, ", "))
	case res.Outcome == OutcomeFailure:
		return ":x: Failing"
	}
	return ":grey_question: Cancelled or skipped"
}

// Posted is the saved record of a needs-human post: where its card is, and what the card
// showed, to edit it later.
type Posted struct {
	Key     string `json:"key"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
	Reason  string `json:"reason,omitempty"`
	CI      string `json:"ci,omitempty"`
}

// PostNeedsHuman posts r's PR card to channel and returns the record to save.
func PostNeedsHuman(ctx context.Context, api slackkit.API, r Run, res Result, reason, channel string, now time.Time) (Posted, error) {
	if channel == "" {
		return Posted{}, errors.New("no needs-human channel")
	}
	posted, err := api.Post(ctx, channel, NeedsHumanCard(r, res, reason, now))
	if err != nil {
		return Posted{}, err
	}
	return Posted{Key: NeedsHumanKey(r), Channel: cmp.Or(posted.Channel, channel), TS: posted.TS, Reason: reason, CI: ciStatus(res)}, nil
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Save writes the record to path.
func (p Posted) Save(path string) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
