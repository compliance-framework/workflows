package pluginhealth

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/pluginprobe"
)

func ptr[T any](v T) *T { return &v }

func TestProtocolAnnotation(t *testing.T) {
	if ProtocolAnnotation != pluginprobe.ProtocolAnnotation {
		t.Errorf("ProtocolAnnotation %q != pluginprobe's %q", ProtocolAnnotation, pluginprobe.ProtocolAnnotation)
	}
}

func TestEvaluate(t *testing.T) {
	yes, no := ptr(true), ptr(false)
	for _, tc := range []struct {
		name string
		row  Row
		want Status
	}{
		{"v2, level", Row{Behind: 0, Protocol: 2}, Healthy},
		{"v2, one minor behind", Row{Behind: 1, Protocol: 2}, Healthy},
		{"v2, two minors behind", Row{Behind: 2, Protocol: 2}, Behind},
		{"v2, three minors behind", Row{Behind: 3, Protocol: 2}, Candidate},
		{"older major", Row{Behind: OlderMajor, Protocol: 2}, Candidate},
		{"annotated 1, no Init", Row{Behind: 0, Protocol: 1, V2Init: no}, Candidate},
		{"unset, no Init", Row{Behind: 0, Protocol: 0, V2Init: no}, Candidate},
		{"unset, serves v2", Row{Behind: 0, Protocol: 0, V2Init: yes}, Healthy},
		{"unset, Init unknown", Row{Behind: 0, Protocol: 0}, Unknown},
		{"library unknown", Row{Behind: -1, Protocol: 2}, Unknown},
		{"protocol unknown", Row{Behind: 1, Protocol: -1}, Unknown},
		{"library unknown, no Init", Row{Behind: -1, Protocol: 1, V2Init: no}, Candidate},
		{"protocol unknown, far behind", Row{Behind: 4, Protocol: -1}, Candidate},
	} {
		if got := Evaluate(tc.row); got != tc.want {
			t.Errorf("%s: Evaluate = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestMinorsBehind(t *testing.T) {
	for _, tc := range []struct {
		lib, latest string
		want        int
	}{
		{"v0.9.0", "v0.9.0", 0},
		{"v0.7.2", "v0.9.0", 2},
		{"v0.8.0-rc4", "v0.9.0", 1},
		{"v0.9.1-0.20260101000000-abcdefabcdef", "v0.9.0", 0},
		{"v0.10.0", "v0.9.0", 0},
		{"v0.9.0", "v1.0.0", OlderMajor},
		{"v1.2.0", "v0.9.0", 0},
		{"latest", "v0.9.0", -1},
	} {
		if got := MinorsBehind(tc.lib, tc.latest); got != tc.want {
			t.Errorf("MinorsBehind(%s, %s) = %d, want %d", tc.lib, tc.latest, got, tc.want)
		}
	}
}

func TestLibVersion(t *testing.T) {
	const mod = "github.com/compliance-framework/agent"
	for _, tc := range []struct {
		name, gomod, want, err string
	}{
		{"require", "module x\nrequire github.com/compliance-framework/agent v0.8.1\n", "v0.8.1", ""},
		{"replaced by a version", "module x\nrequire github.com/compliance-framework/agent v0.8.1\nreplace github.com/compliance-framework/agent => github.com/compliance-framework/agent v0.9.0\n", "v0.9.0", ""},
		{"replace of another version", "module x\nrequire github.com/compliance-framework/agent v0.8.1\nreplace github.com/compliance-framework/agent v0.7.0 => ../agent\n", "v0.8.1", ""},
		{"replaced by a path", "module x\nrequire github.com/compliance-framework/agent v0.8.1\nreplace github.com/compliance-framework/agent => ../agent\n", "", "replaced by ../agent"},
		{"not required", "module x\nrequire github.com/compliance-framework/mock-agent v0.1.1\n", "", "doesn't require"},
	} {
		got, err := LibVersion([]byte(tc.gomod), mod)
		if got != tc.want || (err == nil) != (tc.err == "") || err != nil && !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%s: LibVersion = %q, %v; want %q, error %q", tc.name, got, err, tc.want, tc.err)
		}
	}
}

func TestBumpResult(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		pr   BumpPR
		want string
	}{
		{BumpPR{State: "closed", Merged: now}, "merged"},
		{BumpPR{State: "closed"}, "closed"},
		{BumpPR{State: "open"}, "open"},
		{BumpPR{State: "open", Required: "success"}, "open"},
		{BumpPR{State: "open", Required: "failure"}, "failed"},
		{BumpPR{State: "open", Required: "timed_out"}, "failed"},
		{BumpPR{State: "open", Required: "cancelled"}, "open"},
	} {
		if got := tc.pr.Result(); got != tc.want {
			t.Errorf("%+v: Result = %q, want %q", tc.pr, got, tc.want)
		}
	}
}

// fakeSource serves canned data per repo; a repo missing from releases has no release.
type fakeSource struct {
	agent       string
	agentErr    error
	releases    map[string]*Release
	releaseErrs map[string]error
	files       map[string]string // "repo@ref:path"
	bumps       map[string]*BumpPR
	images      map[string]map[string]string // ref -> annotations
	imageErrs   map[string]error
}

func (f *fakeSource) LatestFinal(_ context.Context, repo string) (string, error) {
	return f.agent, f.agentErr
}

func (f *fakeSource) LatestRelease(_ context.Context, repo string) (*Release, error) {
	return f.releases[repo], f.releaseErrs[repo]
}

func (f *fakeSource) File(_ context.Context, repo, ref, path string) ([]byte, error) {
	if s, ok := f.files[repo+"@"+ref+":"+path]; ok {
		return []byte(s), nil
	}
	return nil, nil
}

func (f *fakeSource) LatestBumpPR(_ context.Context, repo string) (*BumpPR, error) {
	return f.bumps[repo], nil
}

func (f *fakeSource) Annotations(_ context.Context, ref string) (map[string]string, bool, error) {
	if err := f.imageErrs[ref]; err != nil {
		return nil, false, err
	}
	a, ok := f.images[ref]
	return a, ok, nil
}

// addPlugin gives repo a release at tag requiring agent lib, a :latest image annotated with
// protocol ("" for none), and a main.go serving v2 or not.
func (f *fakeSource) addPlugin(repo, tag, lib, protocol string, v2 bool) {
	f.releases[repo] = &Release{Tag: tag, Date: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	f.files[repo+"@"+tag+":go.mod"] = "module x\nrequire github.com/compliance-framework/agent " + lib + "\n"
	main := "runner.RunnerGRPCPlugin{Impl: p}"
	if v2 {
		main = "runner.RunnerV2GRPCPlugin{Impl: p}"
	}
	f.files[repo+"@"+tag+":main.go"] = main
	ann := map[string]string{}
	if protocol != "" {
		ann["org.ccf.plugin.protocol.version"] = protocol
	}
	f.images["ghcr.io/compliance-framework/"+repo+":latest"] = ann
}

func newFake() *fakeSource {
	return &fakeSource{agent: "v0.9.0", releases: map[string]*Release{}, releaseErrs: map[string]error{},
		files: map[string]string{}, bumps: map[string]*BumpPR{}, images: map[string]map[string]string{}, imageErrs: map[string]error{}}
}

func TestCollect(t *testing.T) {
	f := newFake()
	f.addPlugin("p-healthy", "v1.0.0", "v0.9.0", "2", true)
	f.addPlugin("p-behind", "v1.0.0", "v0.7.0", "2", true)
	f.addPlugin("p-old", "v1.0.0", "v0.5.3", "2", true)
	f.addPlugin("p-v1", "v0.3.0", "v0.9.0", "1", false)
	f.addPlugin("p-unannotated-v2", "v0.3.0", "v0.8.0", "", true)
	f.addPlugin("p-bad-annotation", "v0.3.0", "v0.8.0", "two", false)
	f.images["ghcr.io/compliance-framework/p-unreadable:latest"] = map[string]string{"org.ccf.plugin.protocol.version": "2"}
	f.releaseErrs["p-unreadable"] = errors.New("boom")
	f.bumps["p-healthy"] = &BumpPR{Number: 7, State: "closed", Merged: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
	names := []string{"p-healthy", "p-behind", "p-old", "p-v1", "p-unannotated-v2", "p-bad-annotation", "p-norelease", "p-unreadable"}

	rep, err := Collect(context.Background(), f, "compliance-framework", "agent", "ghcr.io", names)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Status{
		"p-healthy": Healthy, "p-behind": Behind, "p-old": Candidate, "p-v1": Candidate,
		"p-unannotated-v2": Healthy, "p-bad-annotation": Candidate, "p-norelease": Unknown, "p-unreadable": Unknown,
	}
	for _, r := range rep.Rows {
		if r.Status != want[r.Repo] {
			t.Errorf("%s: status %q, want %q (row %+v)", r.Repo, r.Status, want[r.Repo], r)
		}
	}
	if got := rep.Candidates(); !slices.Equal(got, []string{"p-old", "p-v1", "p-bad-annotation"}) {
		t.Errorf("Candidates = %v", got)
	}
	if errs := rep.Errors(); len(errs) != 1 || !strings.HasPrefix(errs[0], "p-unreadable: latest release: boom") {
		t.Errorf("Errors = %v", errs)
	}
	row := rep.Rows[4]
	if row.Protocol != 0 || !strings.Contains(strings.Join(row.Notes, ";"), "annotate protocol 2") {
		t.Errorf("unannotated v2 row: %+v", row)
	}
	if rep.Rows[6].Protocol != 0 || !slices.Contains(rep.Rows[6].Notes, "no :latest image") {
		t.Errorf("no-release row: %+v", rep.Rows[6])
	}

	f.agent = ""
	if _, err := Collect(context.Background(), f, "compliance-framework", "agent", "ghcr.io", names); err == nil {
		t.Error("Collect without an agent release: want an error")
	}
}
