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

// PreviewVersion returns the VERSION build arg preview.yml passes to the image builds of an
// event that publishes a preview (PreviewTags returns tags): main-<sha7> on a push to the
// default branch, pr-<number>-<sha7> on a PR, where sha7 is GITHUB_SHA's (the commit the
// image is built from, also its org.opencontainers.image.revision label). It is "" when the
// event publishes no preview. It is not a semantic version on purpose: a preview never
// claims to be a release.
func PreviewVersion(e Event, onMain bool) string {
	if tags, _ := PreviewTags(e, onMain); len(tags) == 0 {
		return ""
	}
	sha := e.SHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	if e.Name == "pull_request" {
		if sha == "" {
			return "pr-" + strconv.Itoa(e.PRNumber)
		}
		return "pr-" + strconv.Itoa(e.PRNumber) + "-" + sha
	}
	return "main-" + sha
}

// ReleaseVersion returns the version a release's git tag names: the tag without prefix, which
// must be X.Y.Z or X.Y.Z-<pre-release> (v1.2.3 -> 1.2.3, v1.2.3-rc1 -> 1.2.3-rc1).
func ReleaseVersion(tag, prefix string) (string, error) {
	version, ok := strings.CutPrefix(tag, prefix)
	if !ok || !valid(version) {
		return "", fmt.Errorf("tag %q is not %sX.Y.Z or %sX.Y.Z-<pre-release>", tag, prefix, prefix)
	}
	return version, nil
}

// Registry tag styles for ReleaseTags.
const (
	// ImageTags is for container images: X.Y.Z, plus X.Y, X and latest for a final release.
	ImageTags = "image"
	// ArtifactTags is for gooci OCI artifacts (plugins, policies), which keep the field's
	// vX.Y.Z tags: vX.Y.Z, plus latest for a final release.
	ArtifactTags = "artifact"
)

// ReleaseTags returns the registry tags a release publishes for the git tag tag, which must
// be prefix followed by X.Y.Z or X.Y.Z-<pre-release>. final reports whether the release is
// final (no pre-release part), decided from the tag name alone, never from the GitHub
// release's prerelease flag. Only a final release gets latest and the floating X.Y and X: a
// release candidate publishes its own version tag and nothing that other users follow.
func ReleaseTags(tag, prefix, style string) (tags []string, final bool, err error) {
	version, err := ReleaseVersion(tag, prefix)
	if err != nil {
		return nil, false, err
	}
	v := "v" + version
	final = semver.Prerelease(v) == ""
	switch style {
	case ImageTags:
		tags = []string{version}
		if final {
			tags = append(tags, semver.MajorMinor(v)[1:], semver.Major(v)[1:])
		}
	case ArtifactTags:
		tags = []string{v}
	default:
		return nil, false, fmt.Errorf("unknown tag style %q", style)
	}
	if final {
		tags = append(tags, "latest")
	}
	return tags, final, nil
}
