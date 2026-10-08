package train

import (
	"reflect"
	"strings"
	"testing"
)

// publishing returns a repo that merged its release PR at "m" proposing versions, with tags on
// "m" and a release run per tag in runs (tag -> status, "completed" means it succeeded).
func publishing(t *testing.T, versions map[string]string, tags []string, runs map[string]string) (*Engine, *RepoState) {
	t.Helper()
	w := newWorld(t)
	fr := w.repo("r", "0.1.0")
	fr.tags["m"] = tags
	fr.runs["pushm"] = []Run{{ID: w.nextID(), Path: ".github/workflows/release-please.yml", Status: "completed", Conclusion: "success"}}
	for tag, status := range runs {
		run := Run{ID: w.nextID(), Path: ".github/workflows/release.yml", HeadBranch: tag, Status: status, URL: "run-url"}
		if status == "completed" {
			run.Conclusion = "success"
		}
		fr.runs["releasem"] = append(fr.runs["releasem"], run)
	}
	return w.engine(trainDay), &RepoState{Name: "r", Phase: Publishing, MergeSHA: "m", Versions: versions}
}

func TestPublishIgnoresFloatingMajorTag(t *testing.T) {
	// The release workflow moved v0 onto the release commit; no release run will ever exist for it.
	e, r := publishing(t, map[string]string{RootPackage: "0.1.2"}, []string{"v0", "v0.1.2", "latest"},
		map[string]string{"v0.1.2": "in_progress"})
	if err := e.publish(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.Phase != Publishing || r.Detail != "waiting for the release workflow of v0.1.2" {
		t.Fatalf("phase %s detail %q", r.Phase, r.Detail)
	}
	e, r = publishing(t, map[string]string{RootPackage: "0.1.2"}, []string{"v0", "v0.1.2"},
		map[string]string{"v0.1.2": "completed"})
	if err := e.publish(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.Phase != Released || !reflect.DeepEqual(r.Tags, []string{"v0.1.2"}) {
		t.Fatalf("phase %s tags %v detail %q", r.Phase, r.Tags, r.Detail)
	}
}

func TestPublishWaitsForReleasePleaseWhenOnlyAFloatingTagIsThere(t *testing.T) {
	e, r := publishing(t, map[string]string{RootPackage: "0.1.2"}, []string{"v0"}, nil)
	if err := e.publish(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.Phase != Publishing || !strings.HasPrefix(r.Detail, "waiting for release-please to tag") {
		t.Fatalf("phase %s detail %q", r.Phase, r.Detail)
	}
}

func TestPublishHelmComponentTags(t *testing.T) {
	versions := map[string]string{"charts/a": "1.2.0", "charts/my-vault": "0.3.0"}
	tags := []string{"a-v1.2.0", "my-vault-v0.3.0", "v1", "latest"}
	e, r := publishing(t, versions, tags, map[string]string{"a-v1.2.0": "completed"})
	if err := e.publish(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.Phase != Publishing || r.Detail != "waiting for the release workflow of my-vault-v0.3.0" {
		t.Fatalf("phase %s detail %q", r.Phase, r.Detail)
	}
	e, r = publishing(t, versions, tags, map[string]string{"a-v1.2.0": "completed", "my-vault-v0.3.0": "completed"})
	if err := e.publish(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.Phase != Released || !reflect.DeepEqual(r.Tags, []string{"a-v1.2.0", "my-vault-v0.3.0"}) {
		t.Fatalf("phase %s tags %v detail %q", r.Phase, r.Tags, r.Detail)
	}
}

func TestPublishPrereleaseTag(t *testing.T) {
	e, r := publishing(t, map[string]string{RootPackage: "1.0.0-rc.1"}, []string{"v1", "v1.0.0-rc.1"},
		map[string]string{"v1.0.0-rc.1": "completed"})
	if err := e.publish(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.Phase != Released || !reflect.DeepEqual(r.Tags, []string{"v1.0.0-rc.1"}) {
		t.Fatalf("phase %s tags %v detail %q", r.Phase, r.Tags, r.Detail)
	}
}

func TestReleaseTags(t *testing.T) {
	for _, c := range []struct {
		tags     []string
		versions map[string]string
		want     []string
	}{
		{[]string{"v0", "v0.1", "latest", "v0.1.2"}, map[string]string{RootPackage: "0.1.2"}, []string{"v0.1.2"}},
		// A release tag of another version on the same commit isn't this train's.
		{[]string{"v0.1.1", "v0.1.2"}, map[string]string{RootPackage: "0.1.2"}, []string{"v0.1.2"}},
		{[]string{"v1", "v1.0.0-rc1", "chart-v2.0.0"}, nil, []string{"v1.0.0-rc1", "chart-v2.0.0"}},
		{[]string{"v0", "latest"}, nil, []string{}},
	} {
		if got := releaseTags(c.tags, c.versions); !reflect.DeepEqual(got, c.want) {
			t.Errorf("releaseTags(%v, %v) = %v, want %v", c.tags, c.versions, got, c.want)
		}
	}
}
