package release

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// PreviewLabel is the PR label that makes preview.yml publish a :pr-<number> image.
const PreviewLabel = "preview"

// NextRC returns the next release-candidate tag of version given the repo's tags:
// <prefix><version>-rc<N+1>, where N is the highest existing <prefix><version>-rcN (0 if none).
// It fails when the final tag <prefix><version> already exists.
func NextRC(prefix, version string, tags []string) (string, error) {
	if v := "v" + version; !semver.IsValid(v) || semver.Canonical(v) != v || semver.Prerelease(v) != "" {
		return "", fmt.Errorf("version %q is not X.Y.Z", version)
	}
	final := prefix + version
	n := 0
	for _, tag := range tags {
		if tag == final {
			return "", fmt.Errorf("%s is already released", final)
		}
		num, ok := strings.CutPrefix(tag, final+"-rc")
		if !ok || num == "" || strings.Trim(num, "0123456789") != "" {
			continue
		}
		if i, err := strconv.Atoi(num); err == nil && i > n {
			n = i
		}
	}
	return fmt.Sprintf("%s-rc%d", final, n+1), nil
}

// Event is what PreviewTags needs to know about the triggering GitHub event.
type Event struct {
	Name          string // GITHUB_EVENT_NAME
	Ref           string // GITHUB_REF
	SHA           string // GITHUB_SHA
	DefaultBranch string
	PRNumber      int
	Labels        []string
	Fork          bool // the PR comes from another repository
}

// PreviewTags returns the image tags preview.yml publishes for an event, or none and why.
// It never returns latest: that tag belongs to final releases.
func PreviewTags(e Event, onMain bool) ([]string, string) {
	switch e.Name {
	case "push":
		if e.Ref != "refs/heads/"+e.DefaultBranch {
			return nil, "push to " + e.Ref + ", not the default branch"
		}
		if !onMain {
			return nil, "on-main is false"
		}
		if len(e.SHA) < 7 {
			return nil, "no commit SHA"
		}
		return []string{"main", "sha-" + e.SHA[:7]}, ""
	case "pull_request":
		switch {
		case !slices.Contains(e.Labels, PreviewLabel):
			return nil, "the PR has no " + PreviewLabel + " label"
		case e.Fork:
			return nil, "PRs from forks can't publish"
		case e.PRNumber <= 0:
			return nil, "no PR number"
		}
		return []string{"pr-" + strconv.Itoa(e.PRNumber)}, ""
	}
	return nil, "event " + e.Name + " publishes no preview"
}
