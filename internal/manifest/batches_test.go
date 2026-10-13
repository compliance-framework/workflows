package manifest

import (
	"reflect"
	"slices"
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

func TestCheckCoverage(t *testing.T) {
	m := loadBatches(t)
	org := func(extra ...OrgRepo) []OrgRepo {
		out := []OrgRepo{{Name: "plugin-old", Archived: true}, {Name: "plugin-gone", Archived: true}, {Name: "local-dev"}}
		for _, n := range names(m.Repos) {
			out = append(out, OrgRepo{Name: n})
		}
		return append(out, extra...)
	}
	if err := m.CheckCoverage(org()); err != nil {
		t.Errorf("CheckCoverage(every listed repo, archived and unmatched others) = %v, want nil", err)
	}
	if err := m.CheckCoverage(org(OrgRepo{Name: "plugin-old"})); err != nil {
		t.Errorf("an unarchived excluded repo: %v, want nil", err)
	}

	var changed []OrgRepo
	for _, r := range org(OrgRepo{Name: "plugin-new-policies"}, OrgRepo{Name: "plugin-a"}, OrgRepo{Name: "policy-new"}) {
		switch r.Name {
		case "api": // deleted
			continue
		case "plugin-three-policies":
			r.Archived = true
		}
		changed = append(changed, r)
	}
	err := m.CheckCoverage(changed)
	want := "listed but not in the organisation: api\n" +
		"listed but archived: plugin-three-policies\n" +
		"match include_patterns but are neither listed nor excluded: plugin-a, plugin-new-policies"
	if err == nil || err.Error() != want {
		t.Errorf("CheckCoverage() = %v, want:\n%s", err, want)
	}
}

// TestReposYAMLBatches: repos.yaml's plugin and policy repos, batch by batch.
func TestReposYAMLBatches(t *testing.T) {
	m, err := Load("../../repos.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(m.ActiveRepos()); len(m.ActiveBatches) != 0 || !reflect.DeepEqual(got, coreRepos) {
		t.Fatalf("active_batches %v: ActiveRepos() = %v, want the core repos %v", m.ActiveBatches, got, coreRepos)
	}
	if len(m.Exclude) != 20 || !reflect.DeepEqual(m.IncludePatterns, []string{"plugin-*"}) {
		t.Errorf("include_patterns %v, %d excluded; want plugin-* and the 20 repos archived in workstream 2", m.IncludePatterns, len(m.Exclude))
	}
	plugins := map[int]int{}
	for _, r := range m.Repos {
		if r.Batch == 0 {
			continue
		}
		policies := strings.HasSuffix(r.Name, "-policies") || r.Name == "policy-template"
		if policies != (r.Kind == KindPolicies) || !reflect.DeepEqual(r.DependsOn, []string{"agent"}) {
			t.Errorf("%s: kind %s, depends_on %v; want %s and [agent]", r.Name, r.Kind, r.DependsOn, map[bool]Kind{true: KindPolicies, false: KindGoPlugin}[policies])
		}
		if template := strings.Contains(r.Name, "template"); template == r.Release || template && r.Batch != 1 {
			t.Errorf("%s: release %v, batch %d; want the templates in batch 1 with release: false, every other repo released", r.Name, r.Release, r.Batch)
		}
		if r.Kind == KindGoPlugin && r.Release {
			plugins[r.Batch]++
		}
	}
	if want := map[int]int{1: 10, 2: 10, 3: 3}; !reflect.DeepEqual(plugins, want) {
		t.Errorf("released plugins per batch = %v, want %v", plugins, want)
	}
	for b := 1; b <= 3; b++ {
		m.ActiveBatches = []int{b}
		if err := m.Validate(); err != nil {
			t.Fatalf("batch %d: %v", b, err)
		}
		stages, err := m.Stages()
		if err != nil {
			t.Fatal(err)
		}
		third := []string{"agent-action"}
		for _, r := range m.Repos {
			if r.Batch == b && r.Release {
				third = append(third, r.Name)
			}
		}
		slices.Sort(third)
		want := [][]string{{"api", "gooci"}, {"agent", "ui"}, third, {"helm-charts"}}
		if !reflect.DeepEqual(stages, want) {
			t.Errorf("active_batches [%d]: Stages() = %v, want %v", b, stages, want)
		}
	}
}
