package slackkit

import (
	"fmt"
	"strings"
	"time"
)

// The cards of each channel. The tools fill these from their state, and previews from sample
// data, so a preview shows exactly what the tools post.

// IncidentStatus is where a CI incident stands.
type IncidentStatus string

// The incident statuses.
const (
	Failing      IncidentStatus = "Failing"
	FailingAgain IncidentStatus = "Failing again"
	Resolved     IncidentStatus = "Resolved"
	Closed       IncidentStatus = "Closed" // the PR was merged or closed while failing
)

// Incident is a #ccf-ci-failures incident card, posted on the first failure and edited on
// every later transition.
type Incident struct {
	Repo       string // owner/name
	Ref        string // what failed, fully qualified: "owner/name#12" or "owner/name@main"
	RefURL     string // the PR, or the branch
	Title      string // the PR's title; "" for a branch
	Workflow   string
	Status     IncidentStatus
	StatusNote string // after the status, e.g. "PR merged"
	SHA        string // the commit the incident opened at
	SHAURL     string
	FailedJobs []string // the latest failure's; struck through once resolved or closed
	OpenedAt   time.Time
	ResolvedAt time.Time // when resolved or closed
	PRURL      string
	RunURL     string // the latest run
	UpdatedAt  time.Time
}

// IncidentCard is the incident's top-level card: red while failing, green once resolved,
// grey when closed with the PR.
func IncidentCard(in Incident) Message {
	emoji, color := ":red_circle:", ColorRed
	switch in.Status {
	case Resolved:
		emoji, color = ":large_green_circle:", ColorGreen
	case Closed:
		emoji, color = ":white_circle:", ColorGrey
	}
	var note string
	if in.StatusNote != "" {
		note = " (" + in.StatusNote + ")"
	}
	summary := "*" + LinkTo(in.RefURL, in.Ref) + "*"
	if in.Title != "" {
		summary += " " + Escape(in.Title)
	}
	jobs := make([]string, len(in.FailedJobs))
	for i, j := range in.FailedJobs {
		jobs[i] = Escape(j)
		if in.Status == Resolved || in.Status == Closed {
			jobs[i] = "~" + jobs[i] + "~"
		}
	}
	var opened []string
	if in.SHA != "" {
		opened = append(opened, LinkTo(in.SHAURL, short(in.SHA)))
	}
	if !in.OpenedAt.IsZero() {
		opened = append(opened, Date(in.OpenedAt))
	}
	var resolved string
	if !in.ResolvedAt.IsZero() {
		resolved = Date(in.ResolvedAt)
		if !in.OpenedAt.IsZero() {
			resolved += " · after " + Duration(in.ResolvedAt.Sub(in.OpenedAt))
		}
	}
	blocks := []Block{Section(summary)}
	blocks = append(blocks, Fields("",
		Field{"Status", string(in.Status) + Escape(note)},
		Field{"Workflow", Escape(in.Workflow)},
		Field{"Opened at", strings.Join(opened, " · ")},
		Field{"Resolved", resolved},
		Field{"Failed jobs", strings.Join(jobs, "\n")},
	)...)
	blocks = append(blocks, Buttons(Link{Text: "View PR", URL: in.PRURL}, Link{Text: "Latest run", URL: in.RunURL})...)
	blocks = append(blocks, Context(updated(in.UpdatedAt))...)
	return Message{
		Text:   fmt.Sprintf("CI %s%s: %s %s", strings.ToLower(string(in.Status)), note, in.Ref, Escape(in.Workflow)),
		Header: fmt.Sprintf("%s CI %s · %s", emoji, strings.ToLower(string(in.Status)), repoName(in.Repo)),
		Blocks: blocks,
		Color:  color,
	}
}

// NeedsHuman is a #ccf-pr-needs-human card for one PR, edited to handled when it's merged or
// closed.
type NeedsHuman struct {
	Repo      string // owner/name
	Ref       string // "owner/name#34"
	URL       string // the PR
	Title     string
	Why       string // mrkdwn: why it needs a person
	CI        string // mrkdwn: the PR's checks
	OpenedBy  string
	OpenedAt  time.Time
	Now       time.Time // for how long it has waited
	Handled   string    // "" while open, else "merged" or "closed"
	HandledBy string
	HandledAt time.Time
}

// NeedsHumanCard is the PR's card: amber while it waits, green once merged, grey once closed.
func NeedsHumanCard(n NeedsHuman) Message {
	header, color := ":raising_hand: Needs a human · "+repoName(n.Repo), ColorAmber
	end, waitLabel := n.Now, "Waiting"
	var handled string
	if n.Handled != "" {
		header, color, end, waitLabel = ":white_check_mark: Handled · "+repoName(n.Repo), ColorGreen, n.HandledAt, "Waited"
		if n.Handled != "merged" {
			color = ColorGrey
		}
		handled = upperFirst(n.Handled)
		if n.HandledBy != "" {
			handled += " by " + Escape(n.HandledBy)
		}
		if !n.HandledAt.IsZero() {
			handled += " · " + Date(n.HandledAt)
		}
	}
	var waited string
	if !n.OpenedAt.IsZero() && !end.IsZero() {
		waited = Duration(end.Sub(n.OpenedAt))
	}
	blocks := []Block{Section("*" + LinkTo(n.URL, n.Ref) + "* " + Escape(n.Title))}
	blocks = append(blocks, Fields("",
		Field{"Why", n.Why},
		Field{"CI", n.CI},
		Field{"Opened by", Escape(n.OpenedBy)},
		Field{waitLabel, waited},
		Field{"Handled", handled},
	)...)
	blocks = append(blocks, Buttons(Link{Text: "Review PR", URL: n.URL, Primary: n.Handled == ""})...)
	text := fmt.Sprintf("Needs a human: %s %s", n.Ref, Escape(n.Title))
	if n.Handled != "" {
		text = fmt.Sprintf("Handled (%s): %s %s", n.Handled, n.Ref, Escape(n.Title))
	}
	return Message{Text: text, Header: header, Blocks: blocks, Color: color}
}

// Pill is a train repo's state, shown as an emoji.
type Pill string

// The pills.
const (
	PillPending Pill = ":white_circle:"
	PillRunning Pill = ":large_yellow_circle:"
	PillHeld    Pill = ":red_circle:"
	PillDone    Pill = ":large_green_circle:"
	PillSkipped Pill = ":fast_forward:"
)

// TrainRepo is one repo on the release board.
type TrainRepo struct {
	Name    string
	URL     string
	Pill    Pill
	Status  string // the phase or hold: "publishing", "blocked: release checks failed"
	Version string // released or planned, e.g. "v1.4.0"
}

// Train is the #ccf-releases board: the train's start message, edited at each step.
type Train struct {
	Title     string // "Release train 2026-10"
	Stage     int    // the running stage, 1-based
	Stages    [][]TrainRepo
	Finished  bool
	Aborted   bool
	DryRun    bool
	IssueURL  string // the tracking issue
	UpdatedAt time.Time
}

// TrainBoard is the board: amber while running, red while a repo is held, green finished,
// grey aborted.
func TrainBoard(t Train) Message {
	held := 0
	for _, stage := range t.Stages {
		for _, r := range stage {
			if r.Pill == PillHeld {
				held++
			}
		}
	}
	status, color := fmt.Sprintf("*Stage %d of %d* · running", t.Stage, len(t.Stages)), ColorAmber
	switch {
	case t.Aborted:
		status, color = fmt.Sprintf("*Aborted* at stage %d of %d", t.Stage, len(t.Stages)), ColorGrey
	case t.Finished:
		status, color = fmt.Sprintf("*Finished* · %d stages", len(t.Stages)), ColorGreen
	case held > 0:
		status, color = fmt.Sprintf("*Stage %d of %d* · %d held", t.Stage, len(t.Stages), held), ColorRed
	}
	blocks := []Block{Section(status), Divider()}
	for i, stage := range t.Stages {
		fields := make([]Field, len(stage))
		for j, r := range stage {
			v := string(r.Pill) + " " + Escape(r.Status)
			if r.Version != "" {
				v += " `" + Escape(r.Version) + "`"
			}
			fields[j] = Field{Value: "*" + LinkTo(r.URL, r.Name) + "*\n" + v}
		}
		blocks = append(blocks, Fields(fmt.Sprintf("*Stage %d*", i+1), fields...)...)
	}
	blocks = append(blocks, Buttons(Link{Text: "Tracking issue", URL: t.IssueURL})...)
	dry := ""
	if t.DryRun {
		dry = ":test_tube: Dry run"
	}
	blocks = append(blocks, Context(dry, updated(t.UpdatedAt))...)
	return Message{
		Text:   fmt.Sprintf("%s: %s", t.Title, strings.ReplaceAll(status, "*", "")),
		Header: ":steam_locomotive: " + t.Title,
		Blocks: blocks,
		Color:  color,
	}
}

// DigestRepo is one repo's release in the digest.
type DigestRepo struct {
	Name, From, To string
	ChangelogURL   string // the release notes or the compare view
}

// Digest is the #ccf-release-digests release digest.
type Digest struct {
	Title      string // "Release digest 2026-10"
	Repos      []DigestRepo
	Highlights []string // mrkdwn, one per bullet
	IssueURL   string   // the train's tracking issue
	Draft      bool
}

// DigestCard is the digest: the from → to versions, highlights and changelog buttons. A draft
// is blue and says so in its header and first line.
func DigestCard(d Digest) Message {
	header, color, text := ":newspaper: "+d.Title, ColorGreen, d.Title
	var blocks []Block
	if d.Draft {
		header, color, text = ":memo: "+d.Title+" (draft)", ColorBlue, d.Title+" (draft)"
		blocks = append(blocks, Context(":construction: Draft for review: edit it, then share it")...)
	}
	fields := make([]Field, len(d.Repos))
	links := []Link{}
	for i, r := range d.Repos {
		from := "new"
		if r.From != "" {
			from = "`" + Escape(r.From) + "`"
		}
		fields[i] = Field{Label: Escape(r.Name), Value: from + " → `" + Escape(r.To) + "`"}
		links = append(links, Link{Text: r.Name + " changelog", URL: r.ChangelogURL})
	}
	blocks = append(blocks, Fields("*Versions*", fields...)...)
	if len(d.Highlights) > 0 {
		blocks = append(blocks, Divider(), Section("*Highlights*\n• "+strings.Join(d.Highlights, "\n• ")))
	}
	links = append(links, Link{Text: "Tracking issue", URL: d.IssueURL})
	blocks = append(blocks, Buttons(links...)...)
	return Message{Text: text, Header: header, Blocks: blocks, Color: color}
}

func updated(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return "Updated " + Date(t)
}

func repoName(fullName string) string {
	_, name, ok := strings.Cut(fullName, "/")
	if !ok {
		return fullName
	}
	return name
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
