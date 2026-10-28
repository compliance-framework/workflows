package bump

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoImageTarget(t *testing.T) {
	for _, tc := range []struct {
		tag, goLine, to string
		move            bool
	}{
		{"1.26.1", "1.26.3", "1.26.3", true},
		{"1.26.1-alpine", "1.26.3", "1.26.3", true},
		{"1.26.1-alpine3.22", "1.27.0", "1.27.0", true},
		{"1.26.3", "1.26.3", "1.26.3", false},
		{"1.26.5-bookworm", "1.26.3", "1.26.3", false}, // newer: never downgraded
		{"1.27.0", "1.26.3", "1.26.3", false},
		// Floating tags keep their precision and move only when they are older.
		{"1.26", "1.26.3", "1.26", false},
		{"1.26-alpine", "1.26.3", "1.26", false},
		{"1.25", "1.26.3", "1.26", true},
		{"1.25-bookworm", "1.27.1", "1.27", true},
		{"1.27", "1.26.3", "1.26", false},
		{"1", "1.26.3", "1", false},
		// A go line without a patch (go 1.26) is met by any 1.26.x.
		{"1.26.0", "1.26", "1.26", false},
		{"1.25.7", "1.26", "1.26", true},
		// Release candidates.
		{"1.26.3", "1.27rc1", "1.27rc1", true},
		{"1.26", "1.27rc1", "1.27rc1", true},
		{"1.27rc1", "1.27.0", "1.27.0", true},
		// Not versions.
		{"alpine", "1.26.3", "", false},
		{"latest", "1.26.3", "", false},
		{"1.26.1", "not-a-version", "", false},
	} {
		to, move := goImageTarget(tc.tag, tc.goLine)
		if to != tc.to || move != tc.move {
			t.Errorf("goImageTarget(%q, %q) = %q, %v; want %q, %v", tc.tag, tc.goLine, to, move, tc.to, tc.move)
		}
	}
}

// goImageFiles is a checkout whose go line go get has just raised to 1.26.3. Its Dockerfiles are
// written by the tests, not kept in testdata: Renovate reads this repo's testdata Dockerfiles and
// would move their images.
func goImageFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fakeRegistry serves digests (name:tag -> digest) and GOLANG_VERSIONs (name:tag or
// name@digest -> version), and records what was read.
type fakeRegistry struct {
	digests, versions map[string]string
	err               error // every read fails with it
	asked             []string
}

func (f *fakeRegistry) Digest(_ context.Context, ref string) (string, error) {
	f.asked = append(f.asked, "digest "+ref)
	if f.err != nil {
		return "", f.err
	}
	if d, ok := f.digests[ref]; ok {
		return d, nil
	}
	return "", errors.New("MANIFEST_UNKNOWN: manifest unknown")
}

func (f *fakeRegistry) GoVersion(_ context.Context, ref string) (string, error) {
	f.asked = append(f.asked, "go "+ref)
	if f.err != nil {
		return "", f.err
	}
	if v, ok := f.versions[ref]; ok {
		return v, nil
	}
	return "", errors.New("MANIFEST_UNKNOWN: manifest unknown")
}

// digest is a fake sha256 digest made of c.
func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

// TestFollowGo: once go get has raised the go line to 1.26.3, the root Dockerfiles' golang images
// that don't build it move, keeping their variants, with the new tag's digest when they had one;
// a new tag must exist. A floating tag that meets the line by name is checked against the Go it
// ships: pinned to an old build, it moves to the tag's current digest, or to the go line when even
// that is behind. Newer images, tags that are not versions, placeholders and Dockerfiles outside
// the root stay. Decide never plans them.
func TestFollowGo(t *testing.T) {
	a, b, c, d, e, f := digest("a"), digest("b"), digest("c"), digest("d"), digest("e"), digest("f")
	files := map[string]string{
		"go.mod": "module github.com/compliance-framework/mock-agent\n\ngo 1.26.3\n\nrequire github.com/compliance-framework/mock-api v0.1.0\n",
		"Dockerfile": `# The builder must build the go line (the golang image sets GOTOOLCHAIN=local).
FROM golang:1.26.1 AS builder
RUN go build -o /agent .

FROM --platform=$BUILDPLATFORM golang:1.26.1-alpine@` + a + ` AS tools
FROM golang:1.26@` + a + ` AS floating-stale
FROM golang:1.26-bookworm@` + c + ` AS floating-current
FROM golang:1.26-alpine@` + d + ` AS floating-lagging

FROM gcr.io/distroless/base-debian12 AS final
COPY --from=builder /agent /agent
`,
		"Dockerfile-ci": `ARG GO_VERSION=1.25.0
from golang:1.26 as floating
FROM golang:1.25-bookworm AS older-floating
FROM docker.io/library/golang:1.26.1-alpine3.22@` + a + ` AS same-image-again
FROM docker.io/library/golang:1.27.0 AS newer
FROM golang:1 AS major
FROM golang:alpine AS no-version
FROM golang:${GO_VERSION} AS templated
FROM golangci/golangci-lint:v2.15.0 AS lint
FROM debian:bookworm-slim
`,
		"build/Dockerfile": "# Not in the root: left alone.\nFROM golang:1.20\n",
	}
	want := map[string]string{
		"Dockerfile": strings.NewReplacer(
			"golang:1.26.1 ", "golang:1.26.3 ",
			"golang:1.26.1-alpine@"+a, "golang:1.26.3-alpine@"+b,
			"golang:1.26@"+a, "golang:1.26@"+c, // the tag's current build ships 1.26.4
			"golang:1.26-alpine@"+d, "golang:1.26.3-alpine@"+b, // even its current build ships 1.26.2
		).Replace(files["Dockerfile"]),
		"Dockerfile-ci": strings.NewReplacer("golang:1.25-bookworm", "golang:1.26-bookworm",
			"golang:1.26.1-alpine3.22@"+a, "golang:1.26.3-alpine3.22@"+f).Replace(files["Dockerfile-ci"]),
	}
	dir := goImageFiles(t, files)
	reg := &fakeRegistry{
		digests: map[string]string{
			"golang:1.26.3": e, "golang:1.26.3-alpine": b, "golang:1.26": c, "golang:1.26-alpine": e,
			"golang:1.26-bookworm": c, "docker.io/library/golang:1.26.3-alpine3.22": f,
		},
		versions: map[string]string{
			"golang@" + a: "1.26.1", "golang@" + c: "1.26.4", "golang@" + d: "1.26.1", "golang@" + e: "1.26.2",
			"golang:1.26": "1.26.4", "golang:1": "1.27.3", "golang:1.26-bookworm": "1.26.4",
		},
	}
	us := Updaters(Config{Owner: "compliance-framework", Run: (&recorder{}).run, Registry: reg})
	refs, err := ScanAll(dir, us)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Decide(refs, map[string]string{"mock-api": "v0.1.0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.NoTarget) != 0 || len(plan.Changes) != 0 || len(plan.UpToDate) != 1 {
		t.Fatalf("Decide planned the golang images: %+v", plan)
	}

	// Without a Go module change, nothing follows, and the registry isn't read.
	same, followed, err := FollowGo(context.Background(), dir, us, plan)
	if err != nil || followed != nil || len(same.UpToDate) != 1 || reg.asked != nil {
		t.Fatalf("FollowGo without a go.mod change = %+v, %v, %v; read %q", same, followed, err, reg.asked)
	}

	mod := Change{Ref: Ref{Updater: goMod{}.Name(), Dep: "mock-api", Key: "github.com/compliance-framework/mock-api", Current: "v0.0.9", File: "go.mod"}, To: "v0.1.0"}
	plan.Changes = []Change{mod}
	got, followed, err := FollowGo(context.Background(), dir, us, plan)
	if err != nil {
		t.Fatal(err)
	}
	changes, _, upToDate := describe(Plan{Changes: followed, UpToDate: got.UpToDate})
	check(t, "changes", changes, []string{
		"golang image Dockerfile golang: 1.26.1 -> 1.26.3",
		"golang image Dockerfile golang: 1.26.1-alpine@" + a + " -> 1.26.3-alpine@" + b,
		"golang image Dockerfile golang: 1.26@" + a + " -> 1.26@" + c,
		"golang image Dockerfile golang: 1.26-alpine@" + d + " -> 1.26.3-alpine@" + b,
		"golang image Dockerfile-ci golang: 1.25-bookworm -> 1.26-bookworm",
		"golang image Dockerfile-ci docker.io/library/golang: 1.26.1-alpine3.22@" + a + " -> 1.26.3-alpine3.22@" + f,
	})
	check(t, "up to date", upToDate, []string{
		"go.mod github.com/compliance-framework/mock-api: v0.1.0",
		"Dockerfile golang: 1.26-bookworm@" + c,
		"Dockerfile-ci golang: 1.26",
		"Dockerfile-ci docker.io/library/golang: 1.27.0",
		"Dockerfile-ci golang: 1",
	})
	if len(got.Changes) != 7 || got.Changes[0] != mod {
		t.Errorf("plan changes = %+v, want the go.mod change and the images'", got.Changes)
	}
	// Every new tag is checked to exist, pinned or not; each reference is read once.
	check(t, "registry reads", reg.asked, []string{
		"digest golang:1.26.3", "digest golang:1.26.3-alpine",
		"go golang@" + a, "digest golang:1.26", "go golang@" + c, // floating-current's build too: read once
		"go golang@" + d, "digest golang:1.26-alpine", "go golang@" + e,
		"go golang:1.26", "go golang:1.26-bookworm", "digest golang:1.26-bookworm",
		"digest docker.io/library/golang:1.26.3-alpine3.22", "go golang:1",
	})
	for name, content := range files {
		w, ok := want[name]
		if !ok {
			w = content
		}
		if b, _ := os.ReadFile(filepath.Join(dir, name)); string(b) != w {
			t.Errorf("%s:\n%s\nwant:\n%s", name, b, w)
		}
	}

	// Settle counts them as applied, the digest-only move too: their files changed and their pins
	// moved.
	after, err := ScanAll(dir, us)
	if err != nil {
		t.Fatal(err)
	}
	settled, _, dropped := got.Settle(after, func(f string) bool { return strings.HasPrefix(f, "Dockerfile") || f == "go.mod" })
	if len(dropped) != 0 || len(settled.Changes) != 7 {
		t.Errorf("Settle dropped %+v", dropped)
	}
	if r := settled.HoldReasons("go-service"); r != nil {
		t.Errorf("HoldReasons = %q, want none", r)
	}

	// A second run finds them building the go line.
	if _, again, err := FollowGo(context.Background(), dir, us, plan); err != nil || again != nil {
		t.Errorf("second FollowGo = %+v, %v", again, err)
	}
}

// TestFollowGoRegistryError: when the registry can't confirm a pin, FollowGo fails with a
// *RegistryError and writes nothing, in any Dockerfile.
func TestFollowGoRegistryError(t *testing.T) {
	mod := Change{Ref: Ref{Updater: goMod{}.Name(), Dep: "mock-api", File: "go.mod"}, To: "v0.1.0"}
	ok := map[string]string{"golang:1.26.3": digest("b")}
	for name, tc := range map[string]struct {
		image string // the FROM of Dockerfile-ci; Dockerfile has a move that would apply
		reg   Registry
	}{
		"no registry":           {"golang:1.26.1@" + digest("a"), nil},
		"registry error":        {"golang:1.26.1@" + digest("a"), &fakeRegistry{err: errors.New("429 Too Many Requests")}},
		"not a digest":          {"golang:1.26.1@" + digest("a"), &fakeRegistry{digests: map[string]string{"golang:1.26.3": "latest"}}},
		"unpinned tag missing":  {"golang:1.26.1-alpine3.10", &fakeRegistry{digests: ok}},
		"floating, no version":  {"golang:1.26", &fakeRegistry{digests: ok}},
		"floating, bad version": {"golang:1.26", &fakeRegistry{digests: ok, versions: map[string]string{"golang:1.26": "devel"}}},
		// golang:1.26 still ships 1.26.2, so the move falls back to golang:1.26.3-alpine, not published.
		"new line behind, release missing": {"golang:1.25-alpine", &fakeRegistry{digests: ok, versions: map[string]string{"golang:1.26-alpine": "1.26.2"}}},
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{
				"go.mod":        "module example.com/m\n\ngo 1.26.3\n",
				"Dockerfile":    "FROM golang:1.26.1 AS builder\n",
				"Dockerfile-ci": "FROM " + tc.image + " AS builder\n",
			}
			dir := goImageFiles(t, files)
			us := Updaters(Config{Owner: "compliance-framework", Registry: tc.reg})
			_, _, err := FollowGo(context.Background(), dir, us, Plan{Changes: []Change{mod}})
			var regErr *RegistryError
			if !errors.As(err, &regErr) {
				t.Fatalf("FollowGo = %v, want a *RegistryError", err)
			}
			for _, f := range []string{"Dockerfile", "Dockerfile-ci"} {
				if got, _ := os.ReadFile(filepath.Join(dir, f)); string(got) != files[f] {
					t.Errorf("%s changed:\n%s", f, got)
				}
			}
		})
	}
}
