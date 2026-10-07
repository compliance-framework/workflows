package ciworkflows_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBin writes an executable script named name into a new directory and returns the PATH
// that puts it first.
func fakeBin(t *testing.T, name, src string) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, name), []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	return "PATH=" + bin + ":" + os.Getenv("PATH")
}

// needBash4 skips a test whose script uses bash 4 features (the runners have bash 5).
func needBash4(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("bash", "-c", "echo ${BASH_VERSINFO[0]}").Output(); err != nil || strings.TrimSpace(string(out)) < "4" {
		t.Skip("bash 4 or later not installed")
	}
}

func TestPublishImageRefs(t *testing.T) {
	needBash4(t)
	src := script(t, "publish-image.yml", "merge", "Choose the image references")
	for _, tc := range []struct {
		name, tags, want string
		failed           bool
	}{
		{"release", "1.2.3 1.2 1 latest", "ghcr.io/compliance-framework/mock-agent:1.2.3 ghcr.io/compliance-framework/mock-agent:1.2 ghcr.io/compliance-framework/mock-agent:1 ghcr.io/compliance-framework/mock-agent:latest", false},
		{"invalid", "main .bad", "", true},
		{"none", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, t.TempDir(), src, "OWNER=Compliance-Framework", "NAME=Mock-Agent", "TAGS="+tc.tags)
			if r.failed != tc.failed || r.outputs["refs"] != tc.want {
				t.Fatalf("failed=%v refs=%q, want %v %q; output:\n%s", r.failed, r.outputs["refs"], tc.failed, tc.want, r.out)
			}
			if !tc.failed && r.outputs["image"] != "ghcr.io/compliance-framework/mock-agent" {
				t.Fatalf("image = %q", r.outputs["image"])
			}
		})
	}
}

func TestMergeManifests(t *testing.T) {
	src := script(t, "publish-image.yml", "merge", "Merge the manifests")
	path := fakeBin(t, "docker", "#!/bin/sh\necho \"$@\" >> \"$RUNNER_TEMP/docker\"\n")
	const image = "ghcr.io/compliance-framework/mock-agent"
	for _, tc := range []struct {
		name   string
		arches []string
		failed bool
	}{
		{"both", []string{"amd64", "arm64"}, false},
		{"no arm64", []string{"amd64"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// RUNNER_TEMP is set by run; the digests go where download-artifact puts them.
			dir := t.TempDir()
			wrapped := "for a in " + strings.Join(tc.arches, " ") + "; do mkdir -p \"$RUNNER_TEMP/digests/digests-$NAME-$a\"; touch \"$RUNNER_TEMP/digests/digests-$NAME-$a/d$a\"; done\n" +
				// Another image whose name starts with this one's must not be picked.
				"mkdir -p \"$RUNNER_TEMP/digests/digests-$NAME-ci-amd64\" && touch \"$RUNNER_TEMP/digests/digests-$NAME-ci-amd64/other\"\n" + src
			r := run(t, dir, wrapped, path, "NAME=mock-agent", "IMAGE="+image, "REFS="+image+":1.2.3 "+image+":latest",
				"SOURCE=https://github.com/compliance-framework/mock-agent", "GITHUB_SHA=abc")
			if r.failed != tc.failed {
				t.Fatalf("failed=%v, want %v; output:\n%s", r.failed, tc.failed, r.out)
			}
			if tc.failed {
				return
			}
			b, err := os.ReadFile(filepath.Join(r.temp, "docker"))
			if err != nil {
				t.Fatal(err)
			}
			want := "buildx imagetools create --annotation index:org.opencontainers.image.source=https://github.com/compliance-framework/mock-agent --annotation index:org.opencontainers.image.revision=abc " +
				"--tag " + image + ":1.2.3 --tag " + image + ":latest " + image + "@sha256:damd64 " + image + "@sha256:darm64\n" +
				"buildx imagetools inspect " + image + ":1.2.3\n"
			if string(b) != want {
				t.Fatalf("docker calls:\n%s\nwant:\n%s", b, want)
			}
		})
	}
}

func TestReleaseTagsStep(t *testing.T) {
	need(t, "go")
	src := script(t, "release-go-image.yml", "tags", "Choose the tags")
	for _, tc := range []struct{ tag, want string }{
		{"v1.2.3", "1.2.3 1.2 1 latest"},
		{"v1.2.3-rc1", "1.2.3-rc1"},
	} {
		r := run(t, filepath.Join("..", ".."), src, "TAG="+tc.tag, "PREFIX=v")
		if r.failed || r.outputs["tags"] != tc.want {
			t.Fatalf("%s: tags=%q, want %q; output:\n%s", tc.tag, r.outputs["tags"], tc.want, r.out)
		}
	}
	if r := run(t, filepath.Join("..", ".."), src, "TAG=", "PREFIX=v"); !r.failed {
		t.Fatalf("no tag: want a failure; output:\n%s", r.out)
	}
}

func TestDispatchTokenGuard(t *testing.T) {
	src := script(t, "release-finished.yml", "dispatch", "Check the dispatch token scope")
	for _, tc := range []struct {
		repo   string
		failed bool
	}{
		{"workflows", false},
		{"", true},
		{"workflows,api", true},
		{"workflows api", true},
	} {
		if r := run(t, t.TempDir(), src, "TOKEN_REPO="+tc.repo); r.failed != tc.failed {
			t.Fatalf("%q: failed=%v, want %v; output:\n%s", tc.repo, r.failed, tc.failed, r.out)
		}
	}
}

func TestSendReleaseFinished(t *testing.T) {
	need(t, "jq")
	src := script(t, "release-finished.yml", "dispatch", "Send release-finished")
	path := fakeBin(t, "gh", "#!/bin/sh\necho \"$@\" > \"$RUNNER_TEMP/gh-args\"\ncat > \"$RUNNER_TEMP/gh-input\"\n")
	for _, tc := range []struct{ name, needs, want string }{
		{"success", `{"tags":{"result":"success","outputs":{"tags":"1.2.3"}},"image":{"result":"success","outputs":{}}}`, "success"},
		{"failure", `{"tags":{"result":"failure"},"image":{"result":"skipped"}}`, "failure"},
		{"cancelled", `{"tags":{"result":"success"},"image":{"result":"cancelled"}}`, "cancelled"},
		{"skipped only", `{"tags":{"result":"success"},"image":{"result":"skipped"}}`, "success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, t.TempDir(), src, path, "NEEDS="+tc.needs, "TAG=v1.2.3", "TOKEN_REPO=workflows",
				"GITHUB_REPOSITORY=compliance-framework/mock-api", "GITHUB_STEP_SUMMARY=/dev/null")
			if r.failed {
				t.Fatalf("failed; output:\n%s", r.out)
			}
			args, _ := os.ReadFile(filepath.Join(r.temp, "gh-args"))
			if strings.TrimSpace(string(args)) != "api repos/compliance-framework/workflows/dispatches --input -" {
				t.Fatalf("gh %s", args)
			}
			var got struct {
				EventType     string            `json:"event_type"`
				ClientPayload map[string]string `json:"client_payload"`
			}
			b, _ := os.ReadFile(filepath.Join(r.temp, "gh-input"))
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("%v: %s", err, b)
			}
			want := map[string]string{"repo": "compliance-framework/mock-api", "tag": "v1.2.3", "conclusion": tc.want}
			if got.EventType != "release-finished" || len(got.ClientPayload) != 3 || got.ClientPayload["conclusion"] != tc.want ||
				got.ClientPayload["repo"] != want["repo"] || got.ClientPayload["tag"] != want["tag"] {
				t.Fatalf("payload %s, want conclusion %s", b, tc.want)
			}
		})
	}
}

func TestPrunePreviews(t *testing.T) {
	need(t, "jq")
	src := script(t, "release-finished.yml", "prune", "Prune old preview tags")
	// Versions of mock-agent: 1-3 are old previews (3 shares its digest with main, so stays),
	// 4 is a recent preview, 5 an old release, 6 untagged. mock-agent-ci has no package.
	const versions = `{"id":1,"updated_at":"2020-01-01T00:00:00Z","metadata":{"container":{"tags":["sha-0123456"]}}}
{"id":2,"updated_at":"2020-01-01T00:00:00Z","metadata":{"container":{"tags":["pr-7","sha-89abcde"]}}}
{"id":3,"updated_at":"2020-01-01T00:00:00Z","metadata":{"container":{"tags":["sha-1111111","main"]}}}
{"id":4,"updated_at":"2999-01-01T00:00:00Z","metadata":{"container":{"tags":["pr-8"]}}}
{"id":5,"updated_at":"2020-01-01T00:00:00Z","metadata":{"container":{"tags":["1.0.0","latest"]}}}
{"id":6,"updated_at":"2020-01-01T00:00:00Z","metadata":{"container":{"tags":[]}}}`
	path := fakeBin(t, "gh", `#!/bin/sh
case "$*" in
*"-X DELETE"*) echo "$@" >> "$RUNNER_TEMP/deleted" ;;
*/mock-agent/versions*) printf '%s\n' "$FAKE_VERSIONS" ;;
*/mock-agent-ci/versions*) echo 'gh: Not Found (HTTP 404)' >&2; exit 1 ;;
*) echo 'gh: Server Error (HTTP 500)' >&2; exit 1 ;;
esac
`)
	env := []string{path, "FAKE_VERSIONS=" + versions, "OWNER=compliance-framework", "REPO_NAME=mock-agent", "MAX_AGE_DAYS=30"}
	r := run(t, t.TempDir(), src, append(env, `IMAGES=[{}, {"name": "Mock-Agent-CI"}, {"name": "mock-agent"}]`)...)
	if r.failed {
		t.Fatalf("failed; output:\n%s", r.out)
	}
	b, _ := os.ReadFile(filepath.Join(r.temp, "deleted"))
	want := "api -X DELETE orgs/compliance-framework/packages/container/mock-agent/versions/1\n" +
		"api -X DELETE orgs/compliance-framework/packages/container/mock-agent/versions/2\n"
	if string(b) != want || !strings.Contains(r.out, "No package mock-agent-ci") {
		t.Fatalf("deleted:\n%s\nwant:\n%s\noutput:\n%s", b, want, r.out)
	}
	if r := run(t, t.TempDir(), src, append(env, `IMAGES=[{"name": "other"}]`)...); !r.failed {
		t.Fatalf("a server error must fail the step; output:\n%s", r.out)
	}
}
