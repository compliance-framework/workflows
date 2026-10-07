package ciworkflows_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChooseChart(t *testing.T) {
	need(t, "go")
	src := script(t, "release-helm.yml", "publish", "Choose the chart")
	if script(t, "release-helm.yml", "publish", "Check the tools ref") != script(t, "preview.yml", "tags", "Check the tools ref") {
		t.Fatal(`step "Check the tools ref" differs between release-helm.yml and preview.yml`)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(root, filepath.Join(dir, ".ccf-workflows")); err != nil {
		t.Fatal(err)
	}
	for chart, name := range map[string]string{"mock-agent": "mock-agent", "mock-app": "mock"} {
		if err := os.MkdirAll(filepath.Join(dir, "charts", chart), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "charts", chart, "Chart.yaml"), []byte("apiVersion: v2\nname: "+name+"\nversion: 0.1.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := run(t, dir, src, "TAG=mock-app-v0.2.0-rc1", "CHARTS_DIR=charts")
	if r.failed || r.outputs["path"] != "charts/mock-app" || r.outputs["name"] != "mock" || r.outputs["version"] != "0.2.0-rc1" {
		t.Fatalf("failed=%v outputs=%v; output:\n%s", r.failed, r.outputs, r.out)
	}
	if r := run(t, dir, src, "TAG=v0.2.0", "CHARTS_DIR=charts"); !r.failed || !strings.Contains(r.out, "has 2 charts") {
		t.Fatalf("a tag without a chart in a multi-chart repo must fail; output:\n%s", r.out)
	}
}

func TestPackageChart(t *testing.T) {
	src := script(t, "release-helm.yml", "publish", "Package and push the chart")
	path := fakeBin(t, "helm", "#!/bin/sh\necho \"$@\" >> \"$RUNNER_TEMP/helm\"\ncase \"$*\" in *--password-stdin*) cat > \"$RUNNER_TEMP/password\" ;; esac\n")
	r := run(t, t.TempDir(), src, path, "CHART_PATH=charts/mock-app", "NAME=mock", "VERSION=0.2.0-rc1",
		"REGISTRY=oci://ghcr.io/compliance-framework/helm-charts", "ACTOR=ccf-release-bot", "GH_TOKEN=t0ken", "GITHUB_STEP_SUMMARY=/dev/null")
	if r.failed {
		t.Fatalf("failed; output:\n%s", r.out)
	}
	b, _ := os.ReadFile(filepath.Join(r.temp, "helm"))
	want := "registry login ghcr.io --username ccf-release-bot --password-stdin\n" +
		"package charts/mock-app --version 0.2.0-rc1 --destination " + r.temp + "/charts\n" +
		"push " + r.temp + "/charts/mock-0.2.0-rc1.tgz oci://ghcr.io/compliance-framework/helm-charts\n"
	if string(b) != want {
		t.Fatalf("helm calls:\n%s\nwant:\n%s", b, want)
	}
	if p, _ := os.ReadFile(filepath.Join(r.temp, "password")); strings.TrimSpace(string(p)) != "t0ken" || strings.Contains(r.out, "t0ken") {
		t.Fatalf("the token must reach helm on stdin only; stdin %q, output:\n%s", p, r.out)
	}
}

func TestMoveMajorTag(t *testing.T) {
	src := script(t, "release-action.yml", "move", "Move the major tag")
	if script(t, "release-action.yml", "tags", "Choose the tags") != script(t, "release-go-image.yml", "tags", "Choose the tags") {
		t.Fatal(`step "Choose the tags" differs between release-action.yml and release-go-image.yml`)
	}
	path := fakeBin(t, "gh", `#!/bin/sh
case "$*" in
*commits/v0.5.0*) echo 0123abc ;;
*"-X PATCH"* | *"-X POST"*) echo "$@" > "$RUNNER_TEMP/gh" ;;
*git/ref/tags/v0*)
  case "$FAKE" in
  exists) echo '{}' ;;
  missing) echo 'gh: Not Found (HTTP 404)' >&2; exit 1 ;;
  *) echo 'gh: Server Error (HTTP 500)' >&2; exit 1 ;;
  esac ;;
*) echo "unexpected gh $*" >&2; exit 1 ;;
esac
`)
	for _, tc := range []struct {
		fake, want string
		failed     bool
	}{
		{"exists", "api -X PATCH repos/o/mock-action/git/refs/tags/v0 -f sha=0123abc -F force=true", false},
		{"missing", "api -X POST repos/o/mock-action/git/refs -f ref=refs/tags/v0 -f sha=0123abc", false},
		{"error", "", true},
	} {
		t.Run(tc.fake, func(t *testing.T) {
			r := run(t, t.TempDir(), src, path, "FAKE="+tc.fake, "GITHUB_REPOSITORY=o/mock-action", "TAG=v0.5.0", "MAJOR=v0", "GITHUB_STEP_SUMMARY=/dev/null")
			b, _ := os.ReadFile(filepath.Join(r.temp, "gh"))
			if r.failed != tc.failed || strings.TrimSpace(string(b)) != tc.want {
				t.Fatalf("failed=%v (want %v), gh %q (want %q); output:\n%s", r.failed, tc.failed, b, tc.want, r.out)
			}
		})
	}
}
