// Package vulnsummary counts open Dependabot alerts per repo and severity and formats the
// weekly Slack summary that cmd/vuln-summary posts.
package vulnsummary

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
)

// Alert is one open Dependabot alert.
type Alert struct {
	Repo     string // repo name, without the owner
	Severity string // the advisory's severity: critical, high, medium or low
}

// Client reads open Dependabot alerts.
type Client interface {
	RepoAlerts(ctx context.Context, owner, repo string) ([]Alert, error)
}

// Severities are the advisory severities, most severe first. Counts are indexed the same way,
// with any other severity counted at index len(Severities).
var Severities = []string{"critical", "high", "medium", "low"}

// Counts holds alert counts per severity, indexed as Severities plus one slot for others.
type Counts [5]int

func (c *Counts) add(severity string) {
	i := slices.Index(Severities, strings.ToLower(severity))
	if i < 0 {
		i = len(Severities)
	}
	c[i]++
}

// Total is the number of alerts.
func (c Counts) Total() int {
	n := 0
	for _, v := range c {
		n += v
	}
	return n
}

// String lists the non-zero counts, most severe first: "1 critical, 3 high".
func (c Counts) String() string {
	var parts []string
	for i, v := range c {
		if v == 0 {
			continue
		}
		name := "other"
		if i < len(Severities) {
			name = Severities[i]
		}
		parts = append(parts, fmt.Sprintf("%d %s", v, name))
	}
	return strings.Join(parts, ", ")
}

// RepoCounts is a repo's open alerts.
type RepoCounts struct {
	Repo   string
	Counts Counts
}

// Failure is a repo whose alerts could not be read.
type Failure struct {
	Repo string
	Err  error
}

// Summary is the open alerts of a set of repos.
type Summary struct {
	Owner string
	// Scope says which repos were read, e.g. "10 repos in repos.mock.yaml".
	Scope string
	// Checked is the number of repos read successfully.
	Checked int
	// Repos are the repos with open alerts, most severe first.
	Repos []RepoCounts
	// Failed are the repos that could not be read, in the order given.
	Failed []Failure
}

// Total sums the counts of every repo.
func (s Summary) Total() Counts {
	var t Counts
	for _, r := range s.Repos {
		for i, v := range r.Counts {
			t[i] += v
		}
	}
	return t
}

// FromRepos reads the open alerts of each of owner's repos. A repo that can't be read is
// listed in Failed; the others are still counted.
func FromRepos(ctx context.Context, c Client, owner, scope string, repos []string) Summary {
	s := Summary{Owner: owner, Scope: scope}
	var alerts []Alert
	for _, repo := range repos {
		a, err := c.RepoAlerts(ctx, owner, repo)
		if err != nil {
			s.Failed = append(s.Failed, Failure{Repo: repo, Err: err})
			continue
		}
		s.Checked++
		alerts = append(alerts, a...)
	}
	s.Repos = group(alerts)
	return s
}

// group counts alerts per repo, ordered by critical, then high, medium and low counts
// (descending), then name.
func group(alerts []Alert) []RepoCounts {
	byRepo := map[string]*RepoCounts{}
	var out []RepoCounts
	for _, a := range alerts {
		if byRepo[a.Repo] == nil {
			byRepo[a.Repo] = &RepoCounts{Repo: a.Repo}
		}
		byRepo[a.Repo].Counts.add(a.Severity)
	}
	for _, r := range byRepo {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b RepoCounts) int {
		for i := range a.Counts {
			if c := cmp.Compare(b.Counts[i], a.Counts[i]); c != 0 {
				return c
			}
		}
		return strings.Compare(a.Repo, b.Repo)
	})
	return out
}

// Links are the URLs the message points to.
type Links struct {
	Server string // GITHUB_SERVER_URL, e.g. https://github.com
	Run    string // the workflow run, or ""
}

// Message formats the summary as Slack mrkdwn: the totals, one line per repo with alerts
// linking to its Dependabot alerts page, the repos that could not be read, and links to the
// org's alerts and the run.
func Message(s Summary, l Links) string {
	server := strings.TrimRight(l.Server, "/")
	var b strings.Builder
	total := s.Total()
	scope := ""
	if s.Scope != "" {
		scope = " (" + escape(s.Scope) + ")"
	}
	if total.Total() == 0 {
		fmt.Fprintf(&b, ":shield: *No open Dependabot alerts* in %s%s.\n", escape(s.Owner), scope)
	} else {
		fmt.Fprintf(&b, ":shield: *%d open Dependabot alert%s* in %s%s: %s\n",
			total.Total(), plural(total.Total()), escape(s.Owner), scope, total)
		for _, r := range s.Repos {
			repoURL := server + "/" + s.Owner + "/" + r.Repo + "/security/dependabot"
			fmt.Fprintf(&b, "• %s: %d (%s)\n", link(repoURL, r.Repo), r.Counts.Total(), r.Counts)
		}
	}
	if clean := s.Checked - len(s.Repos); clean > 0 && len(s.Repos) > 0 {
		verb := "have"
		if clean == 1 {
			verb = "has"
		}
		fmt.Fprintf(&b, "%d other repo%s %s no open alerts.\n", clean, plural(clean), verb)
	}
	if len(s.Failed) > 0 {
		fmt.Fprintf(&b, ":warning: Could not read %d repo%s:\n", len(s.Failed), plural(len(s.Failed)))
		for _, f := range s.Failed {
			fmt.Fprintf(&b, "• %s: %s\n", escape(f.Repo), escape(f.Err.Error()))
		}
	}
	b.WriteString(link(server+"/orgs/"+s.Owner+"/security/alerts/dependabot", "All Dependabot alerts"))
	if l.Run != "" {
		b.WriteString(" · " + link(l.Run, "run"))
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// escape escapes the characters Slack mrkdwn treats as control characters.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func link(url, text string) string {
	return "<" + escape(url) + "|" + escape(text) + ">"
}
