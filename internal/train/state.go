// Package train is the monthly release train (cmd/train, train.yml, docs/train.md): it releases
// the manifest's repos stage by stage, bumping each repo's internal dependencies to what the
// earlier stages released, and keeps its state in a tracking issue in the workflows repo.
package train

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Phase is how far a repo has got in the train. A repo moves through waiting, bumping,
// release-pr, merging and publishing to released, unless an org owner skips it.
type Phase string

// The phases, in order.
const (
	Waiting    Phase = "waiting"    // an earlier stage hasn't finished
	Bumping    Phase = "bumping"    // ccf-bump PR for what earlier stages released
	ReleasePR  Phase = "release-pr" // waiting for release-please's PR to include main
	Merging    Phase = "merging"    // release PR checks and the merge
	Publishing Phase = "publishing" // release-please tags it; the release workflows run
	Released   Phase = "released"   // released, or nothing to release
	Skipped    Phase = "skipped"    // /skip
)

// Done reports whether p lets the next stage start.
func (p Phase) Done() bool { return p == Released || p == Skipped }

// Hold says why a repo can't move on in its phase; "" when it can.
type Hold string

// The holds.
const (
	NoHold     Hold = ""
	Blocked    Hold = "blocked"     // something failed: checks, ccf-bump, a release workflow
	NeedsHuman Hold = "needs-human" // a decision: a major version, a closed PR
)

// Train statuses.
const (
	StatusOpen     = "open"
	StatusFinished = "finished"
	StatusAborted  = "aborted"
)

// Labels on tracking issues.
const (
	LabelTrain   = "train"
	LabelOpen    = "train:open"
	LabelDone    = "train:done"
	LabelAborted = "train:aborted"
	LabelDryRun  = "train:dry-run"
)

// RootPackage is the release-please package of a single-package repo.
const RootPackage = "."

// RepoState is one repo's progress.
type RepoState struct {
	Name  string `json:"name"`
	Stage int    `json:"stage"` // 1-based, among the train's repos
	Phase Phase  `json:"phase"`
	Hold  Hold   `json:"hold,omitempty"`
	// Detail explains the hold, or what the phase is waiting for.
	Detail string `json:"detail,omitempty"`
	// Sticky holds stay until /retry instead of being re-checked on every run (a failed
	// ccf-bump would only fail again).
	Sticky bool `json:"sticky,omitempty"`
	// From is the release-please manifest (package path to version) on the default branch
	// when the repo left waiting; Versions what this train released (or plans, in a dry run).
	From      map[string]string `json:"from,omitempty"`
	Versions  map[string]string `json:"versions,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	BumpPR    int               `json:"bump_pr,omitempty"`
	ReleasePR int               `json:"release_pr,omitempty"`
	MergeSHA  string            `json:"merge_sha,omitempty"`
	FailedRun int64             `json:"failed_run,omitempty"` // a failed release run, re-run by /retry
}

// Status is the repo's displayed status: its hold if any, else its phase.
func (r *RepoState) Status() string {
	if r.Hold != NoHold {
		return string(r.Hold)
	}
	return string(r.Phase)
}

// Version returns the version this train released for a single-package repo, or "".
func (r *RepoState) Version() string { return r.Versions[RootPackage] }

// State is the train, stored as hidden JSON in the tracking issue.
type State struct {
	Month    string       `json:"month"` // YYYY-MM
	Manifest string       `json:"manifest"`
	DryRun   bool         `json:"dry_run,omitempty"`
	Status   string       `json:"status"`
	Channel  string       `json:"channel,omitempty"`   // Slack channel ID
	ThreadTS string       `json:"thread_ts,omitempty"` // the parent message
	Repos    []*RepoState `json:"repos"`               // in stage order
	// Notified holds the dedupe keys of the Slack messages already posted.
	Notified []string `json:"notified,omitempty"`
	// LastComment is the ID of the last issue comment read for commands.
	LastComment int64  `json:"last_comment,omitempty"`
	Digest      string `json:"digest,omitempty"` // URL of the digest draft comment
}

// Repo returns the named repo's state, or nil.
func (s *State) Repo(name string) *RepoState {
	for _, r := range s.Repos {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// Stages returns the number of stages.
func (s *State) Stages() int {
	n := 0
	for _, r := range s.Repos {
		n = max(n, r.Stage)
	}
	return n
}

// StageOpen reports whether every repo in a stage before stage is done.
func (s *State) StageOpen(stage int) bool {
	for _, r := range s.Repos {
		if r.Stage < stage && !r.Phase.Done() {
			return false
		}
	}
	return true
}

// AllDone reports whether every repo is released or skipped.
func (s *State) AllDone() bool {
	for _, r := range s.Repos {
		if !r.Phase.Done() {
			return false
		}
	}
	return true
}

// MarkNotified records key and reports whether it is new.
func (s *State) MarkNotified(key string) bool {
	if slices.Contains(s.Notified, key) {
		return false
	}
	s.Notified = append(s.Notified, key)
	return true
}

// ForgetNotified drops the dedupe keys of repo, so its next hold is posted again.
func (s *State) ForgetNotified(repo string) {
	s.Notified = slices.DeleteFunc(s.Notified, func(k string) bool { return strings.HasPrefix(k, repo+"|") })
}

const (
	stateOpen  = "<!-- train-state\n"
	stateClose = "\n-->"
)

// Title returns the tracking issue title for month: "Release train YYYY-MM", with " (dry run)"
// for a dry run, and " (N)" from the second train of a kind in a month (manual runs).
func Title(month string, dryRun bool, existing []string) string {
	base := "Release train " + month
	suffix := func(n int) string {
		switch {
		case dryRun && n == 1:
			return " (dry run)"
		case dryRun:
			return fmt.Sprintf(" (dry run %d)", n)
		case n == 1:
			return ""
		}
		return fmt.Sprintf(" (%d)", n)
	}
	for n := 1; ; n++ {
		if t := base + suffix(n); !slices.Contains(existing, t) {
			return t
		}
	}
}

// Render returns the tracking issue body: a summary, the table and the hidden state. reposURL
// is the web URL of the repos' owner, for the PR links (see PRLink).
func Render(s *State, reposURL, issueHelp string) (string, error) {
	data, err := json.Marshal(s) // escapes < and >, so the JSON can't close the comment
	if err != nil {
		return "", err
	}
	var b strings.Builder
	mode := ""
	if s.DryRun {
		mode = " Dry run: nothing is merged."
	}
	fmt.Fprintf(&b, "Release train %s over `%s`: **%s**.%s\n\n", s.Month, s.Manifest, s.Status, mode)
	if issueHelp != "" {
		b.WriteString(issueHelp + "\n\n")
	}
	version := "Released"
	if s.DryRun {
		version = "Planned"
	}
	fmt.Fprintf(&b, "| Stage | Repo | Status | From | %s | PRs | Detail |\n| --- | --- | --- | --- | --- | --- | --- |\n", version)
	for _, r := range s.Repos {
		var prs []string
		if r.BumpPR > 0 {
			prs = append(prs, "bump "+PRLink(reposURL, r.Name, r.BumpPR))
		}
		if r.ReleasePR > 0 {
			prs = append(prs, "release "+PRLink(reposURL, r.Name, r.ReleasePR))
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %s |\n", r.Stage, r.Name, r.Status(),
			Versions(r.From), Versions(r.Versions), strings.Join(prs, ", "), cell(r.Detail))
	}
	b.WriteString("\n" + stateOpen + string(data) + stateClose + "\n")
	return b.String(), nil
}

// Parse reads the state from a tracking issue body.
func Parse(body string) (*State, error) {
	_, rest, ok := strings.Cut(body, stateOpen)
	if !ok {
		return nil, errors.New("the issue has no train state")
	}
	data, _, ok := strings.Cut(rest, stateClose)
	if !ok {
		return nil, errors.New("the train state is not terminated")
	}
	var s State
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		return nil, fmt.Errorf("the train state: %w", err)
	}
	return &s, nil
}

// Versions formats a release-please manifest: "v1.2.3" for the root package, else
// "<package base name> v1.2.3" per package, sorted.
func Versions(m map[string]string) string {
	var out []string
	for _, pkg := range slices.Sorted(maps.Keys(m)) {
		if pkg == RootPackage {
			out = append(out, "v"+m[pkg])
			continue
		}
		out = append(out, pkg[strings.LastIndex(pkg, "/")+1:]+" v"+m[pkg])
	}
	return strings.Join(out, ", ")
}

// cell makes s safe for a markdown table cell.
func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}

// Command is a comment command on the tracking issue.
type Command struct {
	Name string // skip, retry or abort
	Repo string // skip and retry
}

// ParseCommand reads a command from a comment's first line: /skip <repo>, /retry <repo> or
// /abort. ok is false when the comment is not a command; err is set for a malformed one.
func ParseCommand(body string) (c Command, ok bool, err error) {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	f := strings.Fields(line)
	if len(f) == 0 || !strings.HasPrefix(f[0], "/") {
		return Command{}, false, nil
	}
	switch name := strings.TrimPrefix(f[0], "/"); name {
	case "skip", "retry":
		if len(f) != 2 {
			return Command{}, true, fmt.Errorf("usage: /%s <repo>", name)
		}
		return Command{Name: name, Repo: f[1]}, true, nil
	case "abort":
		if len(f) != 1 {
			return Command{}, true, errors.New("usage: /abort")
		}
		return Command{Name: name}, true, nil
	}
	return Command{}, false, nil
}
