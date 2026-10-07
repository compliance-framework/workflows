package vulnsummary

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type fakeClient struct {
	repo map[string][]Alert
	errs map[string]error
}

func (f *fakeClient) RepoAlerts(_ context.Context, owner, repo string) ([]Alert, error) {
	if owner != "o" {
		return nil, errors.New("wrong owner " + owner)
	}
	return f.repo[repo], f.errs[repo]
}

func alerts(repo string, sevs ...string) []Alert {
	out := make([]Alert, len(sevs))
	for i, s := range sevs {
		out[i] = Alert{Repo: repo, Severity: s}
	}
	return out
}

func TestFromReposCountsAndOrders(t *testing.T) {
	c := &fakeClient{
		repo: map[string][]Alert{
			"a": alerts("a", "low", "low", "high"),
			"b": alerts("b", "critical", "medium"),
			"c": alerts("c", "high", "LOW", "moderate"),
		},
		errs: map[string]error{"e": errors.New("GET /repos/o/e/dependabot/alerts: 403 Forbidden")},
	}
	s := FromRepos(context.Background(), c, "o", "5 repo(s) in m.yaml", []string{"a", "b", "c", "d", "e"})
	if s.Checked != 4 {
		t.Errorf("Checked = %d, want 4", s.Checked)
	}
	var order []string
	for _, r := range s.Repos {
		order = append(order, r.Repo)
	}
	// b has a critical; a and c tie on critical, high and medium, and a has more low.
	if want := []string{"b", "a", "c"}; !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	if got := s.Repos[2].Counts; got != (Counts{0, 1, 0, 1, 1}) {
		t.Errorf("c counts = %v", got)
	}
	if got := s.Total(); got != (Counts{1, 2, 1, 3, 1}) || got.Total() != 8 {
		t.Errorf("total = %v", got)
	}
	if len(s.Failed) != 1 || s.Failed[0].Repo != "e" {
		t.Errorf("Failed = %v", s.Failed)
	}
}

func TestCountsString(t *testing.T) {
	if got := (Counts{1, 0, 2, 0, 3}).String(); got != "1 critical, 2 medium, 3 other" {
		t.Errorf("String = %q", got)
	}
}

func TestMessage(t *testing.T) {
	s := Summary{
		Owner: "o", Scope: "4 repo(s) in repos.mock.yaml", Checked: 3,
		Repos: []RepoCounts{
			{Repo: "b", Counts: Counts{1, 0, 1}},
			{Repo: "a", Counts: Counts{0, 1, 0, 2}},
		},
		Failed: []Failure{{Repo: "e", Err: errors.New("GET /x: 403 Forbidden: <disabled>")}},
	}
	got := Message(s, Links{Server: "https://github.com/", Run: "https://github.com/o/workflows/actions/runs/1"})
	want := ":shield: *5 open Dependabot alerts* in o (4 repo(s) in repos.mock.yaml): 1 critical, 1 high, 1 medium, 2 low\n" +
		"• <https://github.com/o/b/security/dependabot|b>: 2 (1 critical, 1 medium)\n" +
		"• <https://github.com/o/a/security/dependabot|a>: 3 (1 high, 2 low)\n" +
		"1 other repo has no open alerts.\n" +
		":warning: Could not read 1 repo:\n" +
		"• e: GET /x: 403 Forbidden: &lt;disabled&gt;\n" +
		"<https://github.com/orgs/o/security/alerts/dependabot|All Dependabot alerts> · <https://github.com/o/workflows/actions/runs/1|run>"
	if got != want {
		t.Errorf("Message =\n%s\nwant\n%s", got, want)
	}
}

func TestMessageNoAlerts(t *testing.T) {
	got := Message(Summary{Owner: "o", Scope: "2 repo(s) in m.yaml", Checked: 2}, Links{Server: "https://github.com"})
	want := ":shield: *No open Dependabot alerts* in o (2 repo(s) in m.yaml).\n" +
		"<https://github.com/orgs/o/security/alerts/dependabot|All Dependabot alerts>"
	if got != want {
		t.Errorf("Message =\n%s\nwant\n%s", got, want)
	}
}
