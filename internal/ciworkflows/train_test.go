package ciworkflows_test

import (
	"slices"
	"strings"
	"testing"
)

// TestTrainMembersToken: the token that checks commenters' org roles is minted in every run that
// may read comment commands, including the one that starts a live train and keeps reconciling
// it, and in no dry run (a dry run closes its train in the run that starts it).
func TestTrainMembersToken(t *testing.T) {
	type step struct {
		ID              string            `yaml:"id"`
		If              string            `yaml:"if"`
		ContinueOnError bool              `yaml:"continue-on-error"`
		With            map[string]string `yaml:"with"`
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	read(t, "train.yml", &wf)
	i := slices.IndexFunc(wf.Jobs["train"].Steps, func(s step) bool { return s.ID == "members" })
	if i < 0 {
		t.Fatal("train.yml: no members step in job train")
	}
	members := wf.Jobs["train"].Steps[i]
	if members.If != "steps.plan.outputs.write == 'true'" {
		t.Errorf("members step if = %q, want it minted whenever the run writes", members.If)
	}
	if !members.ContinueOnError {
		t.Error("members step must not fail the run when the app lacks Members read")
	}
	var perms []string
	for k, v := range members.With {
		if strings.HasPrefix(k, "permission-") {
			perms = append(perms, k+": "+v)
		}
	}
	if members.With["repositories"] != "workflows" || !slices.Equal(perms, []string{"permission-members: read"}) {
		t.Errorf("members token scope: repositories %q, %v; want workflows with members read only", members.With["repositories"], perms)
	}

	// The plan step's write output, which the condition reads, for each kind of run.
	src := script(t, "train.yml", "train", "Plan the run")
	path := fakeBin(t, "go", `#!/bin/sh
[ "$1" = build ] && [ "$2" = -o ] || exit 1
printf '#!/bin/sh\necho open=$FAKE_OPEN\necho manifest=repos.yaml\necho repos=a\n' > "$3"
chmod +x "$3"
`)
	for _, tc := range []struct {
		name, event, action, dryRun, open, write string
	}{
		{"dry-run start", "workflow_dispatch", "start", "true", "false", "false"},
		{"live start", "workflow_dispatch", "start", "false", "false", "true"},
		{"reconcile, open train", "repository_dispatch", "", "false", "true", "true"},
		{"reconcile, no train", "repository_dispatch", "", "false", "false", "false"},
		{"comment on an open train", "issue_comment", "", "false", "true", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, t.TempDir(), src, path, "EVENT="+tc.event, "ACTION="+tc.action, "DRY_RUN="+tc.dryRun, "FAKE_OPEN="+tc.open, "SCHEDULE=", "PAYLOAD={}", "MANIFEST=repos.yaml", "REPOS=")
			if r.failed || r.outputs["write"] != tc.write {
				t.Fatalf("write = %q, want %q; failed=%v; output:\n%s", r.outputs["write"], tc.write, r.failed, r.out)
			}
		})
	}
}
