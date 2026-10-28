package bump

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestToolchainTarget(t *testing.T) {
	stable := []string{"go1.27.1", "go1.27.0", "go1.26.10", "go1.26.9", "go1.26.3", "go1.25.12"}
	for _, tc := range []struct{ goLine, old, want string }{
		{"1.26.3", "go1.26.1", "go1.27.1"},  // the newest release, of any minor
		{"1.26.10", "go1.26.2", "go1.27.1"}, // the newest 1.26 patch: the next minor's
		{"1.27.0", "go1.26.10", "go1.27.1"},
		{"1.27rc1", "go1.26.10", "go1.27.1"},
		{"1.27.1", "go1.26.2", ""}, // the newest release: nothing above it
		{"1.27.1", "go1.27.2", "go1.27.2"},
		{"1.28.0", "", ""},
	} {
		if got := toolchainTarget(tc.goLine, tc.old, stable); got != tc.want {
			t.Errorf("toolchainTarget(%s, %s) = %q, want %q", tc.goLine, tc.old, got, tc.want)
		}
	}
}

// TestKeepToolchain: a toolchain line that go get dropped is put back above the new go line and
// survives go mod tidy; with nothing to put back the plan is held; a repo without one gets none.
func TestKeepToolchain(t *testing.T) {
	const cloned = "module example.com/m\n\ngo 1.26.1\n\ntoolchain go1.26.2\n"
	const raised = "module example.com/m\n\ngo 1.26.3\n" // what go get left
	mod := Change{Ref: Ref{Updater: goMod{}.Name(), Dep: "mock-api", Current: "v0.0.9", File: "go.mod"}, To: "v0.1.0"}
	releases := func(vs ...string) GoReleases {
		return func(context.Context) ([]string, error) { return vs, nil }
	}
	for name, tc := range map[string]struct {
		before, after string // go.mod as cloned, and after go get
		changes       []Change
		releases      GoReleases
		tidy          func(dir string) error // what go mod tidy does
		want          string                 // go.mod after; "" = after unchanged
		change        string
		hold          bool
		err           string
		regErr        bool
	}{
		"restored": {before: cloned, after: raised, releases: releases("go1.26.10", "go1.26.3"),
			want: raised[:len(raised)-1] + "\n\ntoolchain go1.26.10\n", change: "go toolchain go.mod toolchain: go1.26.2 -> go1.26.10"},
		"restored at the next minor": {before: cloned, after: raised, releases: releases("go1.27.0", "go1.26.3"),
			want: raised[:len(raised)-1] + "\n\ntoolchain go1.27.0\n", change: "go toolchain go.mod toolchain: go1.26.2 -> go1.27.0"},
		"nothing above the go line": {before: cloned, after: raised, releases: releases("go1.26.3", "go1.25.12"), hold: true},
		"never had one":             {before: "module example.com/m\n\ngo 1.26.1\n", after: raised, releases: releases("go1.26.10")},
		"still there": {before: cloned, after: "module example.com/m\n\ngo 1.26.3\n\ntoolchain go1.26.6\n",
			releases: func(context.Context) ([]string, error) { panic("go.dev read") }},
		"no Go module change": {before: cloned, after: raised, changes: []Change{}, releases: releases("go1.26.10")},
		"go.dev down": {before: cloned, after: raised, regErr: true, err: "registry: the list of Go releases: 503",
			releases: func(context.Context) ([]string, error) { return nil, errors.New("503") }},
		"tidy drops it again": {before: cloned, after: raised, releases: releases("go1.26.10"), err: "go mod tidy removed toolchain go1.26.10 again",
			tidy: func(dir string) error { return os.WriteFile(filepath.Join(dir, "go.mod"), []byte(raised), 0o644) }},
	} {
		t.Run(name, func(t *testing.T) {
			dir := goImageFiles(t, map[string]string{"go.mod": tc.before})
			var ran []string
			run := func(_ context.Context, dir, name string, args ...string) error {
				ran = append(ran, strings.Join(append([]string{name}, args...), " "))
				if tc.tidy != nil {
					return tc.tidy(dir)
				}
				return nil
			}
			us := Updaters(Config{Owner: "compliance-framework", Run: run, GoReleases: tc.releases})
			before, err := ScanAll(dir, us)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(tc.after), 0o644); err != nil {
				t.Fatal(err)
			}
			changes := []Change{mod}
			if tc.changes != nil {
				changes = tc.changes
			}
			p, kept, err := KeepToolchain(context.Background(), dir, us, before, Plan{Changes: changes})
			var regErr *RegistryError
			switch {
			case tc.err != "":
				if err == nil || !strings.Contains(err.Error(), tc.err) || errors.As(err, &regErr) != tc.regErr {
					t.Fatalf("err = %v, want %q (a RegistryError: %v)", err, tc.err, tc.regErr)
				}
				if tc.regErr {
					if got, _ := os.ReadFile(filepath.Join(dir, "go.mod")); string(got) != tc.after {
						t.Errorf("go.mod written:\n%s", got)
					}
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			want := tc.want
			if want == "" {
				want = tc.after
			}
			if got, _ := os.ReadFile(filepath.Join(dir, "go.mod")); string(got) != want {
				t.Errorf("go.mod:\n%s\nwant:\n%s", got, want)
			}
			changesGot, _, _ := describe(Plan{Changes: kept})
			if tc.change == "" {
				check(t, "changes", changesGot, nil)
			} else {
				check(t, "changes", changesGot, []string{tc.change})
				if !slices.Equal(ran, []string{"go mod tidy"}) || len(p.Changes) != 2 {
					t.Errorf("ran %q; plan %+v", ran, p.Changes)
				}
				if r := p.HoldReasons("go-service"); r != nil {
					t.Errorf("HoldReasons = %q, want none (a patch)", r)
				}
				after, _ := ScanAll(dir, us)
				if settled, _, dropped := p.Settle(after, func(string) bool { return true }); len(dropped) != 0 || len(settled.Changes) != 2 {
					t.Errorf("Settle dropped %+v", dropped)
				}
			}
			if got := slices.Equal(p.HoldReasons("go-service"), []string{ToolchainDropped}); got != tc.hold {
				t.Errorf("held %v (%q), want %v", got, p.HoldReasons("go-service"), tc.hold)
			}
		})
	}
}
