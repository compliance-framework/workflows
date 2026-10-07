package pluginprobe

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/compliance-framework/agent/runner"
	"github.com/compliance-framework/agent/runner/proto"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	goplugin "github.com/hashicorp/go-plugin"
)

// fakeEnv makes the test binary serve a fake plugin instead of running the tests; its value picks
// the fake's behaviour (see serveFake).
const fakeEnv = "PLUGIN_PROBE_FAKE"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		serveFake(mode)
		return
	}
	os.Exit(m.Run())
}

type fakeV1 struct{}

func (fakeV1) Configure(*proto.ConfigureRequest) (*proto.ConfigureResponse, error) {
	return &proto.ConfigureResponse{}, nil
}
func (fakeV1) Eval(*proto.EvalRequest, runner.ApiHelper) (*proto.EvalResponse, error) {
	return &proto.EvalResponse{}, nil
}

type fakeV2 struct {
	fakeV1
	mode string
}

func (f fakeV2) Init(_ *proto.InitRequest, a runner.ApiHelper) (*proto.InitResponse, error) {
	switch f.mode {
	case "v2-error":
		return nil, errors.New("no policy paths")
	case "v2-crash":
		os.Exit(3)
	case "v2-hang":
		select {}
	}
	// A real v2 plugin registers its templates through the API helper during Init.
	if err := a.UpsertSubjectTemplates(context.Background(), []*proto.SubjectTemplate{{Name: "fake"}}); err != nil {
		return nil, err
	}
	return &proto.InitResponse{}, nil
}

func serveFake(mode string) {
	var impl runner.Runner = fakeV1{}
	switch {
	case mode == "not-a-plugin":
		fmt.Println("hello")
		os.Exit(0)
	case strings.HasPrefix(mode, "v2"):
		impl = fakeV2{mode: mode}
	}
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: runner.HandshakeConfig,
		Plugins:         map[string]goplugin.Plugin{"runner": &runner.RunnerGRPCPlugin{Impl: impl}},
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}

func testProber(t *testing.T, mode string) *Prober {
	return &Prober{WorkDir: t.TempDir(), Platform: DefaultPlatform(), Timeout: 5 * time.Second, PluginEnv: []string{fakeEnv + "=" + mode}}
}

func self(t *testing.T) string {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestProbeLocalPlugin(t *testing.T) {
	exe := self(t)
	for _, tc := range []struct {
		mode, initErr, err string
		protocol           int
	}{
		{mode: "v1", protocol: 1},
		{mode: "v2", protocol: 2},
		{mode: "v2-error", protocol: 2, initErr: "no policy paths"},
		{mode: "v2-crash", err: "exited during Init"},
		{mode: "v2-hang", err: "did not return within"},
		{mode: "not-a-plugin", err: "starting the plugin"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			p := testProber(t, tc.mode)
			if tc.mode == "v2-hang" {
				p.Timeout = 3 * time.Second
			}
			got := p.ProbePlugin(context.Background(), exe, t.TempDir())
			if got.Protocol != tc.protocol || (tc.initErr == "") != (got.InitError == "") || !strings.Contains(got.InitError, tc.initErr) ||
				(tc.err == "") != (got.Error == "") || !strings.Contains(got.Error, tc.err) {
				t.Errorf("ProbePlugin = %+v; want protocol %d, init error %q, error %q", got, tc.protocol, tc.initErr, tc.err)
			}
		})
	}
}

func TestProbePluginPaths(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "plugin")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec "+self(t)+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := testProber(t, "v2")

	// A directory holding `plugin` runs that; a script has no build info, which is only a warning.
	got := p.ProbePlugin(context.Background(), dir, t.TempDir())
	if got.Error != "" || got.Protocol != 2 || got.LibVersion != "" || len(got.Warnings) != 1 {
		t.Errorf("ProbePlugin(dir) = %+v", got)
	}
	// A bare relative name is a file in the working directory, not a command in PATH.
	t.Chdir(dir)
	if got := p.ProbePlugin(context.Background(), "plugin", t.TempDir()); got.Error != "" || got.Protocol != 2 {
		t.Errorf("ProbePlugin(\"plugin\") = %+v", got)
	}
	for src, want := range map[string]string{
		t.TempDir():         "no plugin executable",
		"no/such/plugin":    "neither a local path nor an OCI tag",
		"ghcr.io/x/y":       "neither a local path nor an OCI tag", // no tag
		"127.0.0.1:1/x/y:z": "resolving",
	} {
		if got := p.ProbePlugin(context.Background(), src, t.TempDir()); !strings.Contains(got.Error, want) {
			t.Errorf("ProbePlugin(%q).Error = %q, want it to contain %q", src, got.Error, want)
		}
	}
}

// image is a single-layer image holding files (name -> content), all mode 0755.
func image(t *testing.T, files map[string]string) v1.Image {
	fsys := fstest.MapFS{}
	for name, content := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(content), Mode: 0o755}
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := errors.Join(tw.AddFS(fsys), tw.Close()); err != nil {
		t.Fatal(err)
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf.Bytes())), nil })
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// pushPluginIndex pushes a plugin index to ref: this platform's image runs the test binary, and a
// second platform's image is a decoy.
func pushPluginIndex(t *testing.T, ref string, annotations map[string]string) {
	plat, err := v1.ParsePlatform(DefaultPlatform())
	if err != nil {
		t.Fatal(err)
	}
	other := v1.Platform{OS: "plan9", Architecture: "mips"}
	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: image(t, map[string]string{"plugin": "#!/bin/sh\nexit 1\n"}), Descriptor: v1.Descriptor{Platform: &other}},
		mutate.IndexAddendum{Add: image(t, map[string]string{"plugin": "#!/bin/sh\nexec " + self(t) + " \"$@\"\n"}), Descriptor: v1.Descriptor{Platform: plat}},
	)
	idx = mutate.Annotations(idx, annotations).(v1.ImageIndex)
	if err := remote.WriteIndex(mustRef(t, ref), idx); err != nil {
		t.Fatal(err)
	}
}

func mustRef(t *testing.T, ref string) name.Reference {
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestProbeOCIPlugin(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	for _, tc := range []struct {
		mode, annotation, err, warning string
		protocol, warnings             int // every case warns that the script has no build info
	}{
		{mode: "v2", annotation: "2", protocol: 2, warnings: 1},
		{mode: "v1", annotation: "", protocol: 1, warnings: 1},
		{mode: "v1", annotation: "2", protocol: 1, warnings: 1, err: "annotated as protocol 2"},
		{mode: "v2", annotation: "", protocol: 2, warnings: 2, warning: "not annotated"},
		{mode: "v2", annotation: "two", protocol: 2, warnings: 3, warning: "ignoring"},
	} {
		ref := fmt.Sprintf("%s/plugin-%s:a%s", host, tc.mode, tc.annotation)
		annotations := map[string]string{}
		if tc.annotation != "" {
			annotations[ProtocolAnnotation] = tc.annotation
		}
		pushPluginIndex(t, ref, annotations)
		got := testProber(t, tc.mode).ProbePlugin(context.Background(), ref, t.TempDir())
		if got.Protocol != tc.protocol || !strings.HasPrefix(got.Digest, "sha256:") ||
			(tc.err == "") != (got.Error == "") || !strings.Contains(got.Error, tc.err) ||
			!strings.Contains(strings.Join(got.Warnings, ";"), tc.warning) || len(got.Warnings) != tc.warnings {
			t.Errorf("ProbePlugin(%s) = %+v; want protocol %d, error %q, warning %q", ref, got, tc.protocol, tc.err, tc.warning)
		}
	}
}

func TestRunAndReport(t *testing.T) {
	p := testProber(t, "v1")
	r := p.Run(context.Background(), []string{self(t), "missing|plugin"})
	if len(r.Plugins) != 2 || !r.Failed() {
		t.Fatalf("Run = %+v", r)
	}
	if r.Plugins[0].Protocol != 1 || r.Plugins[0].Error != "" {
		t.Errorf("first plugin = %+v", r.Plugins[0])
	}
	var md bytes.Buffer
	if err := r.WriteMarkdown(&md); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| Plugin | Protocol |", "| v1 | - | ok | the binary does not depend on ", "`missing\\|plugin` | - | - | **failed**: "} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown lacks %q:\n%s", want, md.String())
		}
	}
	if (Report{Plugins: []PluginResult{{Protocol: 1}}}).Failed() {
		t.Error("a report without errors failed")
	}
}

func TestDepVersion(t *testing.T) {
	info := &debug.BuildInfo{Deps: []*debug.Module{
		{Path: "github.com/hashicorp/go-plugin", Version: "v1.7.0"},
		{Path: AgentModule, Version: "v0.9.0"},
		{Path: "github.com/open-policy-agent/opa", Version: "v1.14.1", Replace: &debug.Module{Path: "../opa"}},
	}}
	for path, want := range map[string]string{AgentModule: "v0.9.0", "github.com/open-policy-agent/opa": "v1.14.1 => ../opa", "x": ""} {
		if got := DepVersion(info, path); got != want {
			t.Errorf("DepVersion(%s) = %q, want %q", path, got, want)
		}
	}
	if _, err := LibVersion(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("LibVersion of a missing file succeeded")
	}
}

// TestLibVersion reads the agent version from a binary built against it. Test binaries carry no
// dependency versions, so it builds testdata/agentlib, which imports the agent's runner.
func TestLibVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "probe")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/agentlib").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	want, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", AgentModule).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := LibVersion(bin); err != nil || got == "" || got != strings.TrimSpace(string(want)) {
		t.Errorf("LibVersion = %q, %v; want %s", got, err, want)
	}
}
