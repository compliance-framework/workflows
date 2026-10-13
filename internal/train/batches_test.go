package train

import (
	"reflect"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
)

// TestNewStateByBatch: a train plans today's repos and stages until a batch is active; then that
// batch's released repos join agent-action in stage 3 and helm-charts stays last.
func TestNewStateByBatch(t *testing.T) {
	tests := []struct {
		active []int
		repos  []string
		want   map[int][]string // stage -> repos
		err    string
	}{
		{nil, nil, map[int][]string{1: {"api", "gooci"}, 2: {"agent", "ui"}, 3: {"agent-action"}, 4: {"helm-charts"}}, ""},
		{[]int{1}, nil, map[int][]string{1: {"api", "gooci"}, 2: {"agent", "ui"}, 3: {"agent-action", "plugin-one", "plugin-one-policies"}, 4: {"helm-charts"}}, ""},
		{[]int{1}, []string{"plugin-one", "helm-charts"}, map[int][]string{1: {"plugin-one"}, 2: {"helm-charts"}}, ""}, // a subset numbers only its own stages
		{nil, []string{"plugin-one"}, nil, `repo "plugin-one" is in batch 1, which is not active in batches.yaml`},
		{[]int{1}, []string{"plugin-two"}, nil, "batch 2, which is not active"},
	}
	for _, tt := range tests {
		m, err := manifest.Load("../manifest/testdata/batches.yaml")
		if err != nil {
			t.Fatal(err)
		}
		m.ActiveBatches = tt.active
		w := newWorld(t)
		for _, r := range m.Repos {
			w.repo(r.Name, "0.1.0")
		}
		st, err := w.engine(trainDay).newState(ctx, m, "2026-12", StartOptions{Manifest: "batches.yaml", Repos: tt.repos})
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("active_batches %v, repos %v: err = %v, want %q", tt.active, tt.repos, err, tt.err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("active_batches %v: %v", tt.active, err)
		}
		got := map[int][]string{}
		for _, r := range st.Repos {
			got[r.Stage] = append(got[r.Stage], r.Name)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("active_batches %v, repos %v: stages = %v, want %v", tt.active, tt.repos, got, tt.want)
		}
	}
}
