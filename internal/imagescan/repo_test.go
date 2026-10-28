package imagescan

import (
	"fmt"
	"strings"
	"testing"
)

// agentRelease and agentPreview are compliance-framework/agent's callers, as they are.
const agentRelease = `name: release
on:
  release:
    types: [published]
jobs:
  release:
    uses: compliance-framework/workflows/.github/workflows/release-go-image.yml@e85d35af2af2ddfcd4b92061d2063e665c1831f6 # v1.7.0
    with:
      # ghcr.io/compliance-framework/agent, agent-ci and agent-custodian, as released before.
      images: >-
        [{"name": "agent", "dockerfile": "Dockerfile", "context": "."},
         {"name": "agent-ci", "dockerfile": "Dockerfile-ci", "context": "."},
         {"name": "agent-custodian", "dockerfile": "Dockerfile-custodian", "context": "."}]
    secrets: inherit
`

const agentPreview = `name: preview
jobs:
  preview:
    uses: compliance-framework/workflows/.github/workflows/preview.yml@e85d35af2af2ddfcd4b92061d2063e665c1831f6 # v1.7.0
    with:
      kind: image
      images: >-
        [{"name": "agent", "dockerfile": "Dockerfile", "context": "."},
         {"name": "agent-ci", "dockerfile": "Dockerfile-ci", "context": "."},
         {"name": "agent-custodian", "dockerfile": "Dockerfile-custodian", "context": "."}]
`

func TestCallerImages(t *testing.T) {
	for _, tc := range []struct {
		name     string
		files    map[string]string
		want     string
		warnings int
		err      string
	}{
		{
			name:  "agent: three images, the same in both callers",
			files: map[string]string{"preview.yml": agentPreview, "release.yml": agentRelease, "ci.yml": "jobs:\n  ci:\n    uses: compliance-framework/workflows/.github/workflows/ci-go-service.yml@abc\n"},
			want:  "agent Dockerfile .|agent-ci Dockerfile-ci .|agent-custodian Dockerfile-custodian .",
		},
		{
			name:  "no images input: the default image",
			files: map[string]string{"release.yml": "jobs:\n  r:\n    uses: compliance-framework/workflows/.github/workflows/release-ui.yml@abc\n"},
			want:  "repo Dockerfile .",
		},
		{
			name:  "no image workflow at all: the default image",
			files: map[string]string{"deploy.yml": "jobs:\n  d:\n    runs-on: ubuntu-latest\n"},
			want:  "repo Dockerfile .",
		},
		{
			name: "a plugin preview and another owner's workflow publish no image",
			files: map[string]string{
				"preview.yml": "jobs:\n  p:\n    uses: compliance-framework/workflows/.github/workflows/preview.yml@abc\n    with:\n      kind: go-plugin\n",
				"other.yml":   "jobs:\n  p:\n    uses: someone/workflows/.github/workflows/publish-image.yml@abc\n    with:\n      images: '[{\"name\": \"x\", \"dockerfile\": \"X\"}]'\n",
			},
			want: "repo Dockerfile .",
		},
		{
			name: "the same image with two Dockerfiles: the first wins, with a warning",
			files: map[string]string{
				"a.yml": "jobs:\n  p:\n    uses: compliance-framework/workflows/.github/workflows/publish-image.yml@abc\n    with:\n      images: '[{\"dockerfile\": \"A\"}]'\n",
				"b.yml": "jobs:\n  p:\n    uses: compliance-framework/workflows/.github/workflows/publish-image.yml@abc\n    with:\n      images: '[{\"dockerfile\": \"B\", \"context\": \"sub\"}]'\n",
			},
			want:     "repo A .",
			warnings: 1,
		},
		{
			name:  "bad images JSON",
			files: map[string]string{"a.yml": "jobs:\n  p:\n    uses: compliance-framework/workflows/.github/workflows/publish-image.yml@abc\n    with:\n      images: '[{'\n"},
			err:   "images",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{}
			var names []string
			for _, n := range []string{"a.yml", "b.yml", "ci.yml", "deploy.yml", "other.yml", "preview.yml", "release.yml"} {
				if src, ok := tc.files[n]; ok {
					files[n] = []byte(src)
					names = append(names, n)
				}
			}
			imgs, warnings, err := CallerImages("compliance-framework", "repo", files, names)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want one containing %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, i := range imgs {
				got = append(got, fmt.Sprintf("%s %s %s", i.Name, i.Dockerfile, i.Context))
			}
			if strings.Join(got, "|") != tc.want || len(warnings) != tc.warnings {
				t.Errorf("images = %q, warnings %q; want %q and %d warning(s)", strings.Join(got, "|"), warnings, tc.want, tc.warnings)
			}
		})
	}
}
