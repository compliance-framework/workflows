// Package reposettings compares a repo's merge settings, security settings and rulesets with the
// state every CCF repo should have, prints the difference and, when asked, writes the desired state.
// cmd/repo-settings and the repo-settings workflow drive it.
package reposettings

import (
	"errors"
	"slices"
	"strings"
)

// Ruleset names this tool owns. Rulesets with other names are left alone.
const (
	RequiredRuleset = "ccf-required"
	ReviewRuleset   = "ccf-review"
)

// DefaultRequiredCheck is the check context the `required` job of a caller's `ci` job reports.
const DefaultRequiredCheck = "ci / required"

// GitHubActionsAppID is the app that reports Actions check runs; pinning the required check to it
// stops a commit status from another source satisfying the check.
const GitHubActionsAppID = 15368

// MergeSettings are the repository fields this tool manages (PATCH /repos/{owner}/{repo}).
type MergeSettings struct {
	AllowSquashMerge         bool   `json:"allow_squash_merge"`
	AllowMergeCommit         bool   `json:"allow_merge_commit"`
	AllowRebaseMerge         bool   `json:"allow_rebase_merge"`
	SquashMergeCommitTitle   string `json:"squash_merge_commit_title"`
	SquashMergeCommitMessage string `json:"squash_merge_commit_message"`
	AllowAutoMerge           bool   `json:"allow_auto_merge"`
	DeleteBranchOnMerge      bool   `json:"delete_branch_on_merge"`
}

// CurrentMergeSettings are MergeSettings as GET /repos/{owner}/{repo} returns them. A nil field is
// one the response left out (or sent as null): GitHub omits the merge settings for a token with
// Administration read only, so nil means unknown, not false or "". The omitempty tags keep unknown
// fields out of toMap.
type CurrentMergeSettings struct {
	AllowSquashMerge         *bool   `json:"allow_squash_merge,omitempty"`
	AllowMergeCommit         *bool   `json:"allow_merge_commit,omitempty"`
	AllowRebaseMerge         *bool   `json:"allow_rebase_merge,omitempty"`
	SquashMergeCommitTitle   *string `json:"squash_merge_commit_title,omitempty"`
	SquashMergeCommitMessage *string `json:"squash_merge_commit_message,omitempty"`
	AllowAutoMerge           *bool   `json:"allow_auto_merge,omitempty"`
	DeleteBranchOnMerge      *bool   `json:"delete_branch_on_merge,omitempty"`
}

// Ruleset is the part of a repository ruleset this tool manages. Decoding drops the read-only
// fields GitHub adds (id, source, links, ...).
type Ruleset struct {
	Name         string        `json:"name"`
	Target       string        `json:"target"`
	Enforcement  string        `json:"enforcement"`
	Conditions   Conditions    `json:"conditions"`
	BypassActors []BypassActor `json:"bypass_actors"`
	Rules        []Rule        `json:"rules"`
}

// CurrentRuleset is a Ruleset as GET /repos/{owner}/{repo}/rulesets/{id} returns it. GitHub leaves
// bypass_actors out for a token that can't edit the ruleset (Administration read), so a nil
// BypassActors (absent or null) means unknown, while a pointer to an empty slice means none. This
// field shadows the embedded Ruleset.BypassActors, which stays nil.
type CurrentRuleset struct {
	Ruleset
	BypassActors *[]BypassActor `json:"bypass_actors"`
}

// known returns the ruleset with its bypass actors, and whether they were readable.
func (c CurrentRuleset) known() (Ruleset, bool) {
	r := c.Ruleset
	if c.BypassActors == nil {
		return r, false
	}
	r.BypassActors = *c.BypassActors
	return r, true
}

// Conditions select the refs a ruleset applies to.
type Conditions struct {
	RefName RefName `json:"ref_name"`
}

// RefName lists ref patterns to include and exclude.
type RefName struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

// BypassActor may bypass a ruleset.
type BypassActor struct {
	ActorID    int64  `json:"actor_id"`
	ActorType  string `json:"actor_type"`
	BypassMode string `json:"bypass_mode"`
}

// Rule is one ruleset rule; Parameters is nil for rules that take none.
type Rule struct {
	Type       string         `json:"type"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// Config holds the inputs of the desired state.
type Config struct {
	// RequiredCheck is the status check context ccf-required requires.
	RequiredCheck string
	// RequiredCheckAppID pins the check to the app that reports it; 0 accepts any source.
	RequiredCheckAppID int64
	// BypassAppID is the ccf-release-bot app's ID, the only actor that bypasses ccf-review.
	BypassAppID int64
}

// Desired is the state every repo should have.
type Desired struct {
	Merge               MergeSettings
	VulnerabilityAlerts bool
	SecurityUpdates     bool
	Rulesets            []Ruleset
}

// DesiredState builds the desired state from c.
func DesiredState(c Config) (Desired, error) {
	check := strings.TrimSpace(c.RequiredCheck)
	switch {
	case check == "":
		return Desired{}, errors.New("the required check context is empty")
	case c.BypassAppID <= 0:
		return Desired{}, errors.New("the ccf-release-bot app ID (the ccf-review bypass actor) is not set")
	case c.RequiredCheckAppID < 0:
		return Desired{}, errors.New("the required check app ID is negative")
	}
	statusCheck := map[string]any{"context": check}
	if c.RequiredCheckAppID > 0 {
		statusCheck["integration_id"] = c.RequiredCheckAppID
	}
	return Desired{
		Merge: MergeSettings{
			AllowSquashMerge:         true,
			SquashMergeCommitTitle:   "PR_TITLE",
			SquashMergeCommitMessage: "PR_BODY",
			AllowAutoMerge:           true,
			DeleteBranchOnMerge:      true,
		},
		VulnerabilityAlerts: true,
		SecurityUpdates:     false,
		Rulesets: []Ruleset{
			defaultBranchRuleset(RequiredRuleset, []BypassActor{},
				Rule{Type: "deletion"},
				Rule{Type: "non_fast_forward"},
				Rule{Type: "pull_request", Parameters: pullRequestParams(0)},
				Rule{Type: "required_status_checks", Parameters: map[string]any{
					"strict_required_status_checks_policy": false,
					"do_not_enforce_on_create":             false,
					"required_status_checks":               []any{statusCheck},
				}},
			),
			// "always" and "pull_request" bypass modes act the same here: ccf-required (no bypass)
			// already forbids pushing to the default branch without a PR.
			defaultBranchRuleset(ReviewRuleset,
				[]BypassActor{{ActorID: c.BypassAppID, ActorType: "Integration", BypassMode: "always"}},
				Rule{Type: "pull_request", Parameters: pullRequestParams(1)},
			),
		},
	}, nil
}

func defaultBranchRuleset(name string, bypass []BypassActor, rules ...Rule) Ruleset {
	return Ruleset{
		Name:         name,
		Target:       "branch",
		Enforcement:  "active",
		Conditions:   Conditions{RefName: RefName{Include: []string{"~DEFAULT_BRANCH"}, Exclude: []string{}}},
		BypassActors: bypass,
		Rules:        rules,
	}
}

func pullRequestParams(approvals int) map[string]any {
	return map[string]any{
		"required_approving_review_count":   approvals,
		"dismiss_stale_reviews_on_push":     false,
		"require_code_owner_review":         false,
		"require_last_push_approval":        false,
		"required_review_thread_resolution": false,
		"allowed_merge_methods":             []any{"squash"},
	}
}

// managedParams lists, per rule type, the parameters compared with the desired state. Others that
// GitHub returns (newer or preview settings) are ignored; a write replaces the rule's parameters.
var managedParams = map[string][]string{
	"pull_request": {
		"required_approving_review_count", "dismiss_stale_reviews_on_push", "require_code_owner_review",
		"require_last_push_approval", "required_review_thread_resolution", "allowed_merge_methods",
	},
	"required_status_checks": {
		"strict_required_status_checks_policy", "do_not_enforce_on_create", "required_status_checks",
	},
}

func isManaged(ruleType, param string) bool {
	keys, ok := managedParams[ruleType]
	return !ok || slices.Contains(keys, param)
}
