package train

import (
	"reflect"
	"strings"
	"testing"
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
