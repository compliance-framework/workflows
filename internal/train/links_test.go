package train

import (
	"strings"
	"testing"
)

func TestPRLink(t *testing.T) {
	if got, want := PRLink("https://github.com/compliance-framework/", "mock-agent", 14), "[mock-agent#14](https://github.com/compliance-framework/mock-agent/pull/14)"; got != want {
		t.Errorf("PRLink = %q, want %q", got, want)
	}
	// Without the repos' URL the reference still names its repo, so GitHub can't link a bare #14.
	if got := PRLink("", "mock-agent", 14); got != "mock-agent#14" {
		t.Errorf("PRLink without the repos' URL = %q", got)
	}
}

func TestSlackText(t *testing.T) {
	for in, want := range map[string]string{
		"checks failing on [mock-agent#14](https://github.com/compliance-framework/mock-agent/pull/14) at abc1234: ci / go": "checks failing on <https://github.com/compliance-framework/mock-agent/pull/14|mock-agent#14> at abc1234: ci / go",
		"bump [a#1](https://github.com/o/a/pull/1), release [a#2](https://github.com/o/a/pull/2)":                           "bump <https://github.com/o/a/pull/1|a#1>, release <https://github.com/o/a/pull/2|a#2>",
		"`/retry mock-api` runs it again [not a link] (x)":                                                                  "`/retry mock-api` runs it again [not a link] (x)",
	} {
		if got := SlackText(in); got != want {
			t.Errorf("SlackText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRenderLinksEveryPR: no PR reference in the table is a bare #n, which GitHub would link to
// the workflows repo's own issue or PR n.
func TestRenderLinksEveryPR(t *testing.T) {
	for reposURL, want := range map[string]string{
		"https://github.com/compliance-framework": "| bump [mock-agent#8](https://github.com/compliance-framework/mock-agent/pull/8), release [mock-agent#9](https://github.com/compliance-framework/mock-agent/pull/9) |",
		"": "| bump mock-agent#8, release mock-agent#9 |",
	} {
		s := testState()
		s.Repos[1].Detail = "x"
		body, err := Render(s, reposURL, "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body, want) || strings.Contains(body, " #") {
			t.Errorf("repos URL %q: body lacks %q or has a bare #n:\n%s", reposURL, want, body)
		}
	}
}
