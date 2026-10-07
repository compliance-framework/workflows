package train

import (
	"fmt"
	"slices"
	"strings"

	"golang.org/x/mod/semver"
)

// Notes is one release's notes, for the digest.
type Notes struct {
	Repo, Tag, URL, Body string
}

// Sections splits release-please release notes into their "### <section>" sections, in
// order, each with its lines (bullets and their continuations, blank lines dropped). Lines
// before the first section (the version heading) are left out.
func Sections(body string) (names []string, lines map[string][]string) {
	lines = map[string][]string{}
	current := ""
	for _, l := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if name, ok := strings.CutPrefix(l, "### "); ok {
			current = strings.TrimSpace(name)
			if !slices.Contains(names, current) {
				names = append(names, current)
			}
			continue
		}
		if strings.HasPrefix(l, "## ") {
			current = "" // a new version heading
			continue
		}
		if current != "" && strings.TrimSpace(l) != "" {
			lines[current] = append(lines[current], l)
		}
	}
	return names, lines
}

// firstSections lead the digest; other sections follow in the order they first appear.
var firstSections = []string{"Features", "Bug Fixes"}

// Digest returns the release digest draft for month: the versions, then the release notes
// aggregated by section across repos, features first.
func Digest(month string, repos []*RepoState, notes []Notes) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Release digest draft: %s\n\nDraft for #ccf-release-digests; edit before posting.\n\n", month)
	b.WriteString("| Repo | Version | From |\n| --- | --- | --- |\n")
	for _, r := range repos {
		v := Versions(r.Versions)
		switch {
		case r.Phase == Skipped:
			v = "skipped"
		case v == "":
			v = "no release"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", r.Name, v, Versions(r.From))
	}
	order := slices.Clone(firstSections)
	bySection := map[string][]string{}
	for _, n := range notes {
		names, lines := Sections(n.Body)
		for _, s := range names {
			if !slices.Contains(order, s) {
				order = append(order, s)
			}
			bySection[s] = append(bySection[s], fmt.Sprintf("**[%s %s](%s)**\n\n%s\n", n.Repo, n.Tag, n.URL, strings.Join(lines[s], "\n")))
		}
	}
	for _, s := range order {
		if len(bySection[s]) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n%s", s, strings.Join(bySection[s], "\n"))
	}
	return b.String()
}

// MinorUp reports whether to is a higher major.minor than from (X.Y.Z versions without "v").
func MinorUp(from, to string) bool {
	f, t := "v"+from, "v"+to
	return semver.IsValid(f) && semver.IsValid(t) && semver.Compare(semver.MajorMinor(t), semver.MajorMinor(f)) > 0
}
