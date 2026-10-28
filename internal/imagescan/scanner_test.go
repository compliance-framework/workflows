package imagescan

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// regexpTemp matches the scan's temporary directory.
var regexpTemp = regexp.MustCompile(`\S*/image-scan-\d+`)

var update = flag.Bool("update", false, "rewrite the testdata/*.golden.json files")

// fakeDocker answers the docker commands Scanner runs from fixtures: images maps a reference to
// its osv-scanner report (testdata/osv-scanner/<file>) and its environment.
type fakeDocker struct {
	images map[string]fakeImage
	local  []string // images present before a pull
	calls  []string
}

type fakeImage struct {
	report string   // testdata/osv-scanner file; "" for an image with no packages (exit 128)
	env    []string // Config.Env
	node   string   // what `node --version` prints; "" when there is no node binary
	pm     string   // APK or APT; "" for an image without a shell or package manager
}

func (f *fakeDocker) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name != "docker" {
		return nil, fmt.Errorf("unexpected command %s", name)
	}
	last := args[len(args)-1]
	switch {
	case args[0] == "pull", args[0] == "image" && args[1] == "rm":
		return nil, nil
	case args[0] == "save":
		return nil, os.WriteFile(args[2], []byte(last), 0o644)
	case args[0] == "image" && args[1] == "inspect" && args[3] == "{{.Id}}":
		if slices.Contains(f.local, last) {
			return []byte("sha256:local\n"), nil
		}
		return nil, &ExitError{Code: 1, Stderr: "No such image: " + last}
	case args[0] == "image" && args[1] == "inspect":
		img, ok := f.images[last]
		if !ok {
			return nil, &ExitError{Code: 1, Stderr: "No such image: " + last}
		}
		return json.Marshal(img.env)
	case args[0] == "run" && slices.Contains(args, "/bin/sh"):
		if pm := f.images[args[len(args)-3]].pm; pm != "" {
			return []byte("/sbin/" + pm + "\n"), nil
		}
		return nil, &ExitError{Code: 127, Stderr: `exec: "/bin/sh": stat /bin/sh: no such file or directory`}
	case args[0] == "run" && slices.Contains(args, "--entrypoint"):
		ref := args[len(args)-2]
		if v := f.images[ref].node; v != "" {
			return []byte(v + "\n"), nil
		}
		return nil, &ExitError{Code: 127, Stderr: `exec: "node": executable file not found in $PATH`}
	case args[0] == "run":
		// The archive docker save wrote names the image.
		ref, err := os.ReadFile(strings.Replace(f.mount(args), ":/work:ro", "", 1) + "/image.tar")
		if err != nil {
			return nil, err
		}
		img := f.images[string(ref)]
		if img.report == "" {
			return nil, &ExitError{Code: 128, Stderr: "No package sources found"}
		}
		data, err := os.ReadFile(filepath.Join("testdata", "osv-scanner", img.report))
		if err != nil {
			return nil, err
		}
		return data, &ExitError{Code: 1}
	}
	return nil, fmt.Errorf("unexpected docker %v", args)
}

func (f *fakeDocker) mount(args []string) string {
	for i, a := range args {
		if a == "--volume" && strings.HasSuffix(args[i+1], ":/work:ro") {
			return args[i+1]
		}
	}
	return ""
}

// osvServer serves api.osv.dev's /v1/query from testdata/osv/<package>-<version>.json, recorded
// from the real API; an unrecorded query fails the test.
func osvServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Package struct{ Name, Ecosystem string } `json:"package"`
			Version string                           `json:"version"`
		}
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/query" || json.Unmarshal(body, &q) != nil || q.Package.Ecosystem != "Bitnami" {
			t.Errorf("unexpected OSV request %s %s", r.URL.Path, body)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		data, err := os.ReadFile(filepath.Join("testdata", "osv", q.Package.Name+"-"+q.Version+".json"))
		if err != nil {
			t.Errorf("no recorded OSV response for %s@%s", q.Package.Name, q.Version)
			http.Error(w, "not recorded", http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// spikeImages are the spike's two images (docs/image-scan.md#the-scanner), as recorded.
var spikeImages = map[string]fakeImage{
	"node:20":             {report: "node_20.json", env: []string{"PATH=/usr/local/bin:/usr/bin", "NODE_VERSION=20.20.2", "YARN_VERSION=1.22.22"}, pm: APT},
	"nginx:stable-alpine": {report: "nginx_stable-alpine.json", env: []string{"PATH=/usr/sbin:/usr/bin", "NGINX_VERSION=1.30.0"}, pm: APK},
}

func newScanner(t *testing.T, docker *fakeDocker) *Scanner {
	return &Scanner{Run: docker, Image: ScannerImage, OSV: &OSV{BaseURL: osvServer(t).URL}, TempDir: t.TempDir(), Pull: true}
}

// TestSpikeAcceptance is the scanner decision's acceptance check, on the spike's recorded
// reports: CVE-2026-111497 (brotli) is flagged as actionable in node:20, both in the Node runtime
// (by the runtime check) and in Debian's brotli package, and in nginx:stable-alpine's
// brotli-libs, fixed in 1.2.1-r0.
func TestSpikeAcceptance(t *testing.T) {
	docker := &fakeDocker{images: spikeImages}
	s := newScanner(t, docker)
	const cve = "CVE-2026-111497"
	for _, tc := range []struct {
		image string
		want  []string // "<type> <package> <installed> -> <fixed> <severity>"
	}{
		{"node:20", []string{
			"binary node 20.20.2 -> 22.23.4,24.22.1,26.12.1 CRITICAL",
			"os libbrotli1 1.0.9-2+b6 -> 1.0.9-2+deb12u1 CRITICAL",
		}},
		{"nginx:stable-alpine", []string{
			"os brotli-libs 1.2.0-r1 -> 1.2.1-r0 CRITICAL",
		}},
	} {
		scan, err := s.Scan(context.Background(), tc.image)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range scan.Findings {
			if slices.Contains(f.CVEs(), cve) {
				if !f.Actionable() {
					t.Errorf("%s: %s %s is not actionable: %+v", tc.image, cve, f.Package, f)
				}
				got = append(got, fmt.Sprintf("%s %s %s -> %s %s", f.Type, f.Package, f.Installed, strings.Join(f.Fixed, ","), f.Severity()))
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: %s findings:\n%s\nwant:\n%s", tc.image, cve, strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
		}
		golden(t, filepath.Join("testdata", "findings", strings.NewReplacer(":", "_").Replace(tc.image)+".golden.json"), scan)
	}
	// The node runtime of node:20 has no fix in 20: the EOL rule's case.
	scan, _ := s.Scan(context.Background(), "node:20")
	for _, f := range scan.Findings {
		if f.Type == TypeBinary && slices.Contains(f.CVEs(), cve) && f.FixedInMajor() {
			t.Errorf("node 20.20.2's %s fix is in another major, FixedInMajor = true", cve)
		}
	}
}

// TestScanCommands pins how the scanner runs Docker: the platform, the archive mounted read-only,
// the user, the database cache, and the runtime fallback to `node --version` with no network.
func TestScanCommands(t *testing.T) {
	docker := &fakeDocker{images: map[string]fakeImage{
		"app:local": {report: "", env: []string{"PATH=/bin"}, node: "v22.23.4"},
	}}
	s := newScanner(t, docker)
	s.CacheDir, s.Pull = "/cache-dir", false
	scan, err := s.Scan(context.Background(), "app:local")
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Findings) != 0 {
		t.Errorf("findings = %+v, want none (no packages, node 22.23.4 clean)", scan.Findings)
	}
	var got []string
	for _, c := range docker.calls {
		got = append(got, regexpTemp.ReplaceAllString(c, "<tmp>"))
	}
	uid := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	want := []string{
		"docker save --output <tmp>/image.tar app:local",
		"docker run --rm --user " + uid + " --volume <tmp>:/work:ro --volume /cache-dir:/cache --env OSV_SCANNER_LOCAL_DB_CACHE_DIRECTORY=/cache " + ScannerImage + " scan image --archive --format json --offline-vulnerabilities --download-offline-databases /work/image.tar",
		"docker image inspect --format {{json .Config.Env}} app:local",
		"docker run --rm --network none --entrypoint node app:local --version",
		"docker run --rm --network none --entrypoint /bin/sh app:local -c command -v apk || command -v apt-get",
	}
	if !slices.Equal(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	docker = &fakeDocker{images: spikeImages}
	s = newScanner(t, docker)
	if _, err := s.Scan(context.Background(), "nginx:stable-alpine"); err != nil {
		t.Fatal(err)
	}
	if docker.calls[1] != "docker pull --quiet --platform linux/amd64 nginx:stable-alpine" || docker.calls[len(docker.calls)-1] != "docker image rm nginx:stable-alpine" {
		t.Errorf("pull mode: calls %q, want a pull first and the image removed last", docker.calls)
	}
	// An image that was already there is pulled (updated) but kept.
	docker = &fakeDocker{images: spikeImages, local: []string{"nginx:stable-alpine"}}
	s = newScanner(t, docker)
	if _, err := s.Scan(context.Background(), "nginx:stable-alpine"); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(docker.calls, func(c string) bool { return strings.HasPrefix(c, "docker image rm") }) {
		t.Errorf("a local image was removed: %q", docker.calls)
	}
}

func TestScanRejectsBadRefs(t *testing.T) {
	docker := &fakeDocker{images: spikeImages}
	s := newScanner(t, docker)
	if _, err := s.Scan(context.Background(), "--privileged"); err == nil || len(docker.calls) > 0 {
		t.Errorf("err = %v, docker calls %q; want an error and no docker call", err, docker.calls)
	}
}

func golden(t *testing.T, path string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
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
