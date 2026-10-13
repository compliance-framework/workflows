package manifest

import (
	"reflect"
	"strings"
	"testing"
)

// batchesFixture has repos.yaml's core repos plus plugin and policy repos in batches 1 to 3.
const batchesFixture = "testdata/batches.yaml"

var coreRepos = []string{"api", "gooci", "agent", "ui", "agent-action", "helm-charts", "workflows"}

func names(repos []Repo) []string {
	var out []string
	for _, r := range repos {
		out = append(out, r.Name)
	}
	return out
}

func loadBatches(t *testing.T, active ...int) *Manifest {
	t.Helper()
	m, err := Load(batchesFixture)
	if err != nil {
		t.Fatal(err)
	}
	m.ActiveBatches = active
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestActiveRepos(t *testing.T) {
	tests := []struct {
		active []int
		want   []string
	}{
		{nil, coreRepos},
		{[]int{1}, append(coreRepos[:len(coreRepos):len(coreRepos)], "plugin-one", "plugin-one-policies", "plugin-template")},
		{[]int{3, 2}, append(coreRepos[:len(coreRepos):len(coreRepos)], "plugin-two", "plugin-three-policies")},
	}
	for _, tt := range tests {
		if got := names(loadBatches(t, tt.active...).ActiveRepos()); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("active_batches %v: ActiveRepos() = %v, want %v", tt.active, got, tt.want)
		}
	}
}

func TestSelect(t *testing.T) {
	m := loadBatches(t, 1)
	tests := []struct {
		names    []string
		inactive bool
		want     []string
		err      string
	}{
		{nil, false, names(m.ActiveRepos()), ""},
		{[]string{"plugin-one", "api", "api"}, false, []string{"api", "plugin-one"}, ""},
		{[]string{"plugin-two"}, false, nil, "in a batch that is not active (active_batches: [1]): plugin-two (batch 2)"},
		{[]string{"plugin-two", "plugin-three-policies"}, true, []string{"plugin-two", "plugin-three-policies"}, ""},
		{[]string{"plugin-two", "nope"}, true, nil, "not in the manifest: nope"},
	}
	for _, tt := range tests {
		got, err := m.Select(tt.names, tt.inactive)
		if tt.err != "" {
			if err == nil || err.Error() != tt.err {
				t.Errorf("Select(%v, %v) err = %v, want %q", tt.names, tt.inactive, err, tt.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(names(got), tt.want) {
			t.Errorf("Select(%v, %v) = %v, %v; want %v", tt.names, tt.inactive, names(got), err, tt.want)
		}
	}
}

// TestStagesWithBatches: plugin and policy repos depend on agent, so an active batch joins
// agent-action in stage 3, and helm-charts stays last in stage 4.
func TestStagesWithBatches(t *testing.T) {
	tests := []struct {
		active []int
		want   [][]string
	}{
		{nil, [][]string{{"api", "gooci"}, {"agent", "ui"}, {"agent-action"}, {"helm-charts"}}},
		{[]int{1}, [][]string{{"api", "gooci"}, {"agent", "ui"}, {"agent-action", "plugin-one", "plugin-one-policies"}, {"helm-charts"}}},
		{[]int{1, 2, 3}, [][]string{{"api", "gooci"}, {"agent", "ui"},
			{"agent-action", "plugin-one", "plugin-one-policies", "plugin-three-policies", "plugin-two"}, {"helm-charts"}}},
	}
	for _, tt := range tests {
		got, err := loadBatches(t, tt.active...).Stages()
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("active_batches %v: Stages() = %v, %v; want %v", tt.active, got, err, tt.want)
		}
	}
}

func TestParseBatchErrors(t *testing.T) {
	const agent = "  - {name: agent, kind: go-service, release: true}\n"
	const plugin = "  - {name: plugin-a, kind: go-plugin, depends_on: [agent], release: true, batch: 1}\n"
	tests := []struct {
		name, yaml, wantErr string
	}{
		{"batch 0", "repos:\n" + agent + "  - {name: p, kind: go-plugin, release: true, batch: 0}\n", "batch 0: batches are numbered from 1"},
		{"negative batch", "repos:\n" + agent + "  - {name: p, kind: policies, release: true, batch: -1}\n", "batch -1: batches are numbered from 1"},
		{"batch on a core repo", "repos:\n  - {name: agent, kind: go-service, release: true, batch: 1}\n", `repo "agent": kind "go-service" is a core repo`},
		{"batch on the workflows repo", "repos:\n  - {name: workflows, kind: workflows, release: false, batch: 2}\n", "is a core repo"},
		{"listed and excluded", "exclude: [plugin-a]\nrepos:\n" + agent + plugin, `repo "plugin-a" is both listed and excluded`},
		{"active batch without repos", "active_batches: [2]\nrepos:\n" + agent + plugin, "no repo is in batch 2"},
		{"active batch listed twice", "active_batches: [1, 1]\nrepos:\n" + agent + plugin, "batch 1 is listed more than once"},
		{"core repo depends on a batch", "repos:\n" + agent + plugin + "  - {name: x, kind: go-lib, release: true, depends_on: [plugin-a]}\n",
			`repo "x" depends on "plugin-a", which is in a later batch (1)`},
		{"earlier batch depends on a later one", "repos:\n" + agent + "  - {name: p1, kind: go-plugin, release: true, batch: 1, depends_on: [p2]}\n" +
			"  - {name: p2, kind: policies, release: true, batch: 2}\n", `repo "p1" depends on "p2", which is in a later batch (2)`},
		{"non-integer batch", "repos:\n" + agent + "  - {name: p, kind: go-plugin, release: true, batch: one}\n", "decode manifest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseBatches(t *testing.T) {
	m, err := Parse([]byte("active_batches: [1]\nrepos:\n  - {name: agent, kind: go-service, release: true}\n" +
		"  - {name: p, kind: go-plugin, depends_on: [agent], release: true, batch: 1}\n" +
		"  - {name: q, kind: policies, depends_on: [agent, p], release: true, batch: 2}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.ActiveBatches, []int{1}) || m.Repo("agent").Batch != 0 || m.Repo("p").Batch != 1 || m.Repo("q").Batch != 2 || m.Repo("x") != nil {
		t.Errorf("Parse() = %+v", m)
	}
}
