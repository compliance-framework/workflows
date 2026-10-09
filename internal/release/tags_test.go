package release

import (
	"slices"
	"strings"
	"testing"
)

func TestNextRC(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		tags          []string
		want, err     string
	}{
		{"first", "1.2.0", []string{"v1.1.0", "v1.1.1-rc1"}, "v1.2.0-rc1", ""},
		{"after rc2", "1.2.0", []string{"v1.2.0-rc1", "v1.2.0-rc2", "v1.2.0-rc10x", "v1.2.0-rc.3", "v1.2.0-rc+4"}, "v1.2.0-rc3", ""},
		{"rc10", "1.2.0", []string{"v1.2.0-rc9", "v1.2.0-rc10"}, "v1.2.0-rc11", ""},
		{"released", "1.2.0", []string{"v1.2.0-rc1", "v1.2.0"}, "", "already released"},
		{"not X.Y.Z", "1.2", nil, "", "not X.Y.Z"},
		{"prerelease", "1.2.0-rc1", nil, "", "not X.Y.Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NextRC("v", tc.version, tc.tags)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestPreviewTags(t *testing.T) {
	const sha = "0123456789abcdef"
	push := Event{Name: "push", Ref: "refs/heads/main", SHA: sha, DefaultBranch: "main"}
	pr := Event{Name: "pull_request", Ref: "refs/pull/7/merge", SHA: sha, DefaultBranch: "main", PRNumber: 7, Labels: []string{"bug", "preview"}}
	fork := pr
	fork.Fork = true
	unlabelled := pr
	unlabelled.Labels = []string{"bug"}
	branch := push
	branch.Ref = "refs/heads/feature"
	for _, tc := range []struct {
		name   string
		e      Event
		onMain bool
		want   []string
	}{
		{"push to main", push, true, []string{"main", "sha-0123456"}},
		{"push to main, on-main false", push, false, nil},
		{"push to another branch", branch, true, nil},
		{"labelled PR", pr, false, []string{"pr-7"}},
		{"unlabelled PR", unlabelled, true, nil},
		{"fork PR", fork, true, nil},
		{"tag push", Event{Name: "push", Ref: "refs/tags/v1.0.0", SHA: sha, DefaultBranch: "main"}, true, nil},
		{"release", Event{Name: "release", SHA: sha}, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := PreviewTags(tc.e, tc.onMain)
			if !slices.Equal(got, tc.want) || (got == nil) == (reason == "") || slices.Contains(got, "latest") {
				t.Fatalf("got %q (reason %q), want %q", got, reason, tc.want)
			}
		})
	}
}

func TestPreviewVersion(t *testing.T) {
	const sha = "0123456789abcdef"
	push := Event{Name: "push", Ref: "refs/heads/main", SHA: sha, DefaultBranch: "main"}
	trunk := Event{Name: "push", Ref: "refs/heads/trunk", SHA: sha, DefaultBranch: "trunk"}
	pr := Event{Name: "pull_request", Ref: "refs/pull/7/merge", SHA: sha, DefaultBranch: "main", PRNumber: 7, Labels: []string{"preview"}}
	noSHA := pr
	noSHA.SHA = ""
	unlabelled := pr
	unlabelled.Labels = nil
	for _, tc := range []struct {
		name   string
		e      Event
		onMain bool
		want   string
	}{
		{"push to main", push, true, "main-0123456"},
		{"push to another default branch", trunk, true, "main-0123456"}, // like its :main tag
		{"push to main, on-main false", push, false, ""},
		{"labelled PR", pr, true, "pr-7-0123456"},
		{"labelled PR without a SHA", noSHA, true, "pr-7"},
		{"unlabelled PR", unlabelled, true, ""},
		{"release", Event{Name: "release", SHA: sha}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PreviewVersion(tc.e, tc.onMain); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReleaseVersion(t *testing.T) {
	for _, tc := range []struct{ tag, prefix, want, err string }{
		{"v1.2.3", "v", "1.2.3", ""},
		{"v1.2.3-rc1", "v", "1.2.3-rc1", ""},
		{"mock-chart-v0.2.0", "mock-chart-v", "0.2.0", ""},
		{"1.2.3", "v", "", "not vX.Y.Z"},
		{"v1.2", "v", "", "not vX.Y.Z"},
		{"v1.2.3+build", "v", "", "not vX.Y.Z"},
		{"", "v", "", "not vX.Y.Z"},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			got, err := ReleaseVersion(tc.tag, tc.prefix)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestReleaseTags(t *testing.T) {
	for _, tc := range []struct {
		tag, prefix, style string
		want               []string
		final              bool
		err                string
	}{
		{"v1.2.3", "v", ImageTags, []string{"1.2.3", "1.2", "1", "latest"}, true, ""},
		{"v0.4.0", "v", ImageTags, []string{"0.4.0", "0.4", "0", "latest"}, true, ""},
		{"v1.2.3-rc1", "v", ImageTags, []string{"1.2.3-rc1"}, false, ""},
		{"v1.2.3", "v", ArtifactTags, []string{"v1.2.3", "latest"}, true, ""},
		{"v1.2.3-rc.2", "v", ArtifactTags, []string{"v1.2.3-rc.2"}, false, ""},
		{"mock-chart-v0.2.0", "mock-chart-v", ImageTags, []string{"0.2.0", "0.2", "0", "latest"}, true, ""},
		{"1.2.3", "v", ImageTags, nil, false, "not vX.Y.Z"},
		{"v1.2", "v", ImageTags, nil, false, "not vX.Y.Z"},
		{"v1.2.3+build", "v", ImageTags, nil, false, "not vX.Y.Z"},
		{"v01.2.3", "v", ImageTags, nil, false, "not vX.Y.Z"},
		{"", "v", ImageTags, nil, false, "not vX.Y.Z"},
		{"v1.2.3", "v", "chart", nil, false, "unknown tag style"},
	} {
		t.Run(tc.tag+"/"+tc.style, func(t *testing.T) {
			got, final, err := ReleaseTags(tc.tag, tc.prefix, tc.style)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) || final != tc.final {
				t.Fatalf("got %q final=%v, %v; want %q final=%v", got, final, err, tc.want, tc.final)
			}
		})
	}
}
