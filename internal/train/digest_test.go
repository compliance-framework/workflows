package train

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/slackkit"
)

const apiNotes = `## [0.2.0](https://github.com/o/mock-api/compare/v0.1.0...v0.2.0) (2026-12-01)


### Bug Fixes

* handle empty input ([#3](u)) ([abc](u))


### Features

* add the widgets endpoint ([#2](u)) ([def](u))
  with a continuation line
`

func TestSections(t *testing.T) {
	names, lines := Sections(apiNotes)
	if want := []string{"Bug Fixes", "Features"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	if want := []string{"* add the widgets endpoint ([#2](u)) ([def](u))", "  with a continuation line"}; !reflect.DeepEqual(lines["Features"], want) {
		t.Errorf("Features = %q", lines["Features"])
	}
}

func TestDigest(t *testing.T) {
	repos := []*RepoState{
		{Name: "mock-api", Phase: Released, From: map[string]string{".": "0.1.0"}, Versions: map[string]string{".": "0.2.0"}},
		{Name: "mock-gooci", Phase: Released, From: map[string]string{".": "0.0.7"}},
		{Name: "mock-ui", Phase: Skipped},
		{Name: "mock-agent", Phase: Released, Versions: map[string]string{".": "0.3.1"}},
	}
	notes := []Notes{
		{Repo: "mock-api", Tag: "v0.2.0", URL: "https://r/api", Body: apiNotes},
		{Repo: "mock-agent", Tag: "v0.3.1", URL: "https://r/agent", Body: "### Dependencies\n\n* bump mock-api\n\n### Bug Fixes\n\n* agent fix\n"},
	}
	got := Digest("2026-12", repos, notes)
	for _, want := range []string{
		"| mock-api | v0.2.0 | v0.1.0 |", "| mock-gooci | no release | v0.0.7 |", "| mock-ui | skipped |  |",
		"**[mock-api v0.2.0](https://r/api)**\n\n* add the widgets endpoint",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("digest lacks %q:\n%s", want, got)
		}
	}
	features, fixes, deps := strings.Index(got, "### Features"), strings.Index(got, "### Bug Fixes"), strings.Index(got, "### Dependencies")
	if features < 0 || !(features < fixes && fixes < deps) {
		t.Errorf("want Features, then Bug Fixes, then Dependencies:\n%s", got)
	}
	if strings.Index(got, "handle empty input") > strings.Index(got, "agent fix") {
		t.Error("want repos within a section in train order")
	}
}

func TestDigestCard(t *testing.T) {
	repos := []*RepoState{
		{Name: "mock-api", Phase: Released, From: map[string]string{".": "0.1.0"}, Versions: map[string]string{".": "0.2.0"}},
		{Name: "mock-gooci", Phase: Released, From: map[string]string{".": "0.0.7"}}, // nothing released
		{Name: "mock-ui", Phase: Skipped},
		{Name: "mock-agent", Phase: Released, Versions: map[string]string{".": "0.3.1"}},
	}
	notes := []Notes{
		{Repo: "mock-api", Tag: "v0.2.0", URL: "https://r/api", Body: "### Features\n\n* **api:** add <widgets> ([#2](https://github.com/o/mock-api/issues/2))\n  continued\n"},
		{Repo: "mock-agent", Tag: "v0.3.1", URL: "https://r/agent", Body: "### Bug Fixes\n\n* agent fix\n"},
	}
	got := DigestCard("2026-12", repos, notes, "https://issue")
	want := slackkit.DigestCard(slackkit.Digest{
		Title: "Release digest 2026-12", Draft: true, IssueURL: "https://issue",
		Repos: []slackkit.DigestRepo{
			{Name: "mock-api", From: "v0.1.0", To: "v0.2.0", ChangelogURL: "https://r/api"},
			{Name: "mock-agent", To: "v0.3.1", ChangelogURL: "https://r/agent"},
		},
		Highlights: []string{"*mock-api*: *api:* add &lt;widgets&gt; (<https://github.com/o/mock-api/issues/2|#2>)"},
	})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("card = %+v\nwant %+v", got, want)
	}

	var many strings.Builder
	many.WriteString("### Features\n\n")
	for i := range maxHighlights + 3 {
		fmt.Fprintf(&many, "- feature %d\n", i)
	}
	got = DigestCard("2026-12", repos[:1], []Notes{{Repo: "mock-api", Body: many.String()}}, "")
	if text := got.Blocks[len(got.Blocks)-1].Text.Text; !strings.Contains(text, "feature 9") || strings.Contains(text, "feature 10") || !strings.Contains(text, "and 3 more in the tracking issue's digest") {
		t.Errorf("highlights past the cap: %s", text)
	}
}

func TestMinorUp(t *testing.T) {
	for _, tt := range []struct {
		from, to string
		want     bool
	}{{"0.1.0", "0.2.0", true}, {"0.1.0", "0.1.5", false}, {"1.9.3", "2.0.0", true}, {"", "0.1.0", false}, {"0.2.0", "0.1.0", false}} {
		if got := MinorUp(tt.from, tt.to); got != tt.want {
			t.Errorf("MinorUp(%q, %q) = %v", tt.from, tt.to, got)
		}
	}
}
