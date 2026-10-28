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

// TestFollowGo: once go get has raised the go line (fixture goimage: go 1.26.3), the root
// Dockerfiles' golang images below it move up, keeping their variants and, re-resolved, their
// digests; floating tags that already meet it, newer images, tags that are not versions and
// Dockerfiles outside the root stay. Decide never plans them.
func TestFollowGo(t *testing.T) {
	const newDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	dir := copyDir(t, "goimage")
	var asked []string
	cfg := Config{Owner: "compliance-framework", Run: (&recorder{}).run, Digest: func(_ context.Context, ref string) (string, error) {
		asked = append(asked, ref)
		return newDigest, nil
	}}
	us := Updaters(cfg)
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

	// Without a Go module change, nothing follows.
	same, followed, err := FollowGo(context.Background(), dir, us, plan)
	if err != nil || followed != nil || len(same.UpToDate) != 1 {
		t.Fatalf("FollowGo without a go.mod change = %+v, %v, %v", same, followed, err)
	}

	mod := Change{Ref: Ref{Updater: goMod{}.Name(), Dep: "mock-api", Key: "github.com/compliance-framework/mock-api", Current: "v0.0.9", File: "go.mod"}, To: "v0.1.0"}
	plan.Changes = []Change{mod}
	got, followed, err := FollowGo(context.Background(), dir, us, plan)
	if err != nil {
		t.Fatal(err)
	}
	changes, _, upToDate := describe(Plan{Changes: followed, UpToDate: got.UpToDate})
	check(t, "changes", changes, []string{
		"golang image Dockerfile golang: 1.26.1 -> v1.26.3",
		"golang image Dockerfile golang: 1.26.1-alpine -> v1.26.3",
		"golang image Dockerfile-ci golang: 1.25-bookworm -> v1.26",
		"golang image Dockerfile-ci docker.io/library/golang: 1.26.1-alpine3.22 -> v1.26.3",
	})
	check(t, "up to date", upToDate, []string{
		"go.mod github.com/compliance-framework/mock-api: v0.1.0",
		"Dockerfile-ci golang: 1.26",
		"Dockerfile-ci docker.io/library/golang: 1.27.0",
		"Dockerfile-ci golang: 1",
	})
	if len(got.Changes) != 5 || got.Changes[0] != mod {
		t.Errorf("plan changes = %+v, want the go.mod change and the images'", got.Changes)
	}
	check(t, "digests asked for", asked, []string{"golang:1.26.3-alpine", "docker.io/library/golang:1.26.3-alpine3.22"})
	compareGolden(t, dir, "goimage")

	// Settle counts them as applied: their files changed and their pins moved.
	after, err := ScanAll(dir, us)
	if err != nil {
		t.Fatal(err)
	}
	settled, _, dropped := got.Settle(after, func(f string) bool { return strings.HasPrefix(f, "Dockerfile") || f == "go.mod" })
	if len(dropped) != 0 || len(settled.Changes) != 5 {
		t.Errorf("Settle dropped %+v", dropped)
	}
	if r := settled.HoldReasons("go-service"); r != nil {
		t.Errorf("HoldReasons = %q, want none", r)
	}

	// A second run finds them at the go line.
	if _, again, err := FollowGo(context.Background(), dir, us, plan); err != nil || again != nil {
		t.Errorf("second FollowGo = %+v, %v", again, err)
	}
}

// TestFollowGoDigestError: an image pinned by digest is not moved without its new digest.
func TestFollowGoDigestError(t *testing.T) {
	mod := Change{Ref: Ref{Updater: goMod{}.Name(), Dep: "mock-api", File: "go.mod"}, To: "v0.1.0"}
	for name, digest := range map[string]func(context.Context, string) (string, error){
		"no registry": nil,
		"registry error": func(context.Context, string) (string, error) {
			return "", errors.New("429 Too Many Requests")
		},
		"not a digest": func(context.Context, string) (string, error) { return "latest", nil },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"go.mod":     "module example.com/m\n\ngo 1.26.3\n",
				"Dockerfile": "FROM golang:1.26.1@sha256:" + strings.Repeat("a", 64) + " AS builder\n",
			}
			for f, content := range files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			us := Updaters(Config{Owner: "compliance-framework", Digest: digest})
			if _, _, err := FollowGo(context.Background(), dir, us, Plan{Changes: []Change{mod}}); err == nil {
				t.Fatal("FollowGo succeeded")
			}
			if got, _ := os.ReadFile(filepath.Join(dir, "Dockerfile")); string(got) != files["Dockerfile"] {
				t.Errorf("Dockerfile changed:\n%s", got)
			}
		})
	}
}
