package imagescan

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/compliance-framework/workflows/internal/renovate"
)

// Ref is an image reference as a Dockerfile writes it.
type Ref struct {
	// Name is the repository as written, without tag or digest: "node",
	// "gcr.io/distroless/base-debian12". It is the depName Renovate's dockerfile manager gives it.
	Name   string
	Tag    string // "" without one
	Digest string // "sha256:...", "" without one
}

// ParseRef splits an image reference into name, tag and digest.
func ParseRef(s string) (Ref, error) {
	var r Ref
	rest, digest, ok := strings.Cut(s, "@")
	if ok {
		if !refDigest.MatchString(digest) {
			return r, fmt.Errorf("image %q: bad digest", s)
		}
		r.Digest = digest
	}
	// A tag follows the last colon after the last slash (a colon before it is a registry port).
	if i := strings.LastIndexByte(rest, ':'); i > strings.LastIndexByte(rest, '/') {
		rest, r.Tag = rest[:i], rest[i+1:]
		if r.Tag == "" {
			return r, fmt.Errorf("image %q: empty tag", s)
		}
	}
	if !refName.MatchString(rest) {
		return r, fmt.Errorf("image %q: bad name", s)
	}
	if r.Tag != "" && !refTag.MatchString(r.Tag) {
		return r, fmt.Errorf("image %q: bad tag", s)
	}
	r.Name = rest
	return r, nil
}

// refName, refDigest and refTag are Docker's reference grammar (distribution/reference), so a
// reference read from a repo's file can't start with "-" and be read as a docker flag. The name's
// is renovate.ImageName, which also checks the image names of the rules the plan writes.
var (
	refName   = renovate.ImageName
	refDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	refTag    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

// String is the reference as Docker takes it.
func (r Ref) String() string {
	s := r.Name
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}

// WithTag returns the reference to tag, without a digest.
func (r Ref) WithTag(tag string) Ref { return Ref{Name: r.Name, Tag: tag} }

// tagVersion is a tag read as Renovate's docker versioning reads it: a numeric version, then an
// optional suffix that a compatible update keeps ("22-alpine", "1.26.1", "3.20").
type tagVersion struct {
	parts  []int
	suffix string
}

var versionTag = regexp.MustCompile(`^v?(\d+(?:\.\d+)*)(-.*)?$`)

func parseTagVersion(tag string) (tagVersion, bool) {
	m := versionTag.FindStringSubmatch(tag)
	if m == nil {
		return tagVersion{}, false
	}
	var v tagVersion
	for p := range strings.SplitSeq(m[1], ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return tagVersion{}, false
		}
		v.parts = append(v.parts, n)
	}
	v.suffix = m[2]
	return v, true
}

// prefix is the first n parts joined with dots: the release cycle ("20", "3.20").
func (v tagVersion) prefix(n int) string {
	if n > len(v.parts) {
		return ""
	}
	s := make([]string, n)
	for i, p := range v.parts[:n] {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ".")
}

// newestPatch returns the highest tag of tags with the same major.minor, the same number of parts
// and the same suffix as current, if it is newer: the patch update Renovate would make.
func newestPatch(current string, tags []string) (string, bool) {
	cur, ok := parseTagVersion(current)
	if !ok || len(cur.parts) < 3 {
		return "", false
	}
	best, bestTag := cur, ""
	for _, t := range tags {
		v, ok := parseTagVersion(t)
		if !ok || v.suffix != cur.suffix || len(v.parts) != len(cur.parts) || !slices.Equal(v.parts[:2], cur.parts[:2]) {
			continue
		}
		if slices.Compare(v.parts, best.parts) > 0 {
			best, bestTag = v, t
		}
	}
	return bestTag, bestTag != ""
}

// semverLess compares dotted numeric versions ("20.20.2" < "22.23.4"); a part that is not a
// number compares as 0.
func semverLess(a, b string) bool {
	return slices.Compare(numbers(a), numbers(b)) < 0
}

func numbers(v string) []int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out []int
	for p := range strings.SplitSeq(v, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

// major is the first number of a version.
func major(v string) int {
	if n := numbers(v); len(n) > 0 {
		return n[0]
	}
	return 0
}
