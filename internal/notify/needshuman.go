// The needs-human rule of the notify-failure workflow, separate from CI incidents: a tracked
// bot PR that waits for a person is posted once to the needs-human channel (docs/attention.md).

package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
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

// NeedsHumanMessage is the post for r's PR.
func NeedsHumanMessage(r Run, reason string) string {
	url := fmt.Sprintf("%s/%s/pull/%d", r.ServerURL, r.Repo, r.PRNumber)
	return fmt.Sprintf(":raising_hand: %s %s — needs a human: %s",
		link(url, fmt.Sprintf("%s#%d", path.Base(r.Repo), r.PRNumber)), escape(r.PRTitle), escape(reason))
}

// Posted is the saved record of a needs-human post.
type Posted struct {
	Key     string `json:"key"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// PostNeedsHuman posts r's PR to channel and returns the record to save.
func PostNeedsHuman(ctx context.Context, p Poster, r Run, reason, channel string) (Posted, error) {
	if channel == "" {
		return Posted{}, errors.New("no needs-human channel")
	}
	posted, ts, err := p.Post(ctx, channel, NeedsHumanMessage(r, reason), "")
	if err != nil {
		return Posted{}, err
	}
	if posted == "" {
		posted = channel
	}
	return Posted{Key: NeedsHumanKey(r), Channel: posted, TS: ts}, nil
}

// Save writes the record to path.
func (p Posted) Save(path string) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
