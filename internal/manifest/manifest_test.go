package manifest

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func date(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLoadRepoManifests(t *testing.T) {
	tests := []struct {
		path   string
		stages [][]string
	}{
		{"../../repos.yaml", [][]string{{"api", "gooci"}, {"agent", "ui"}, {"agent-action"}, {"helm-charts"}}},
		{"../../repos.mock.yaml", [][]string{
			{"mock-api", "mock-gooci"},
			{"mock-agent", "mock-ui"},
			{"mock-agent-action", "mock-plugin-1", "mock-plugin-2", "mock-plugin-policies-1", "mock-plugin-policies-2"},
			{"mock-helm-charts"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			m, err := Load(tt.path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got, err := m.Stages()
			if err != nil {
				t.Fatalf("Stages: %v", err)
			}
			if !reflect.DeepEqual(got, tt.stages) {
				t.Errorf("Stages() = %v, want %v", got, tt.stages)
			}
			if got := m.NextWorkingWeekday(date(t, "2027-01-01")).Format(dateLayout); got != "2027-01-04" {
				t.Errorf("NextWorkingWeekday(2027-01-01) = %s, want 2027-01-04", got)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("does-not-exist.yaml"); err == nil || !strings.Contains(err.Error(), "read manifest") {
		t.Fatalf("Load() error = %v, want a read error", err)
	}
}

func TestParseAllFields(t *testing.T) {
	got, err := Parse([]byte(`
holidays: ["2026-12-25", 2027-01-01]
include_patterns: ["plugin-*"]
exclude: [plugin-template]
repos:
  - {name: api, kind: go-service, release: true}
  - {name: charts, kind: helm, depends_on: [api], release: false, charts: [a, b]}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := &Manifest{
		Holidays:        []string{"2026-12-25", "2027-01-01"},
		IncludePatterns: []string{"plugin-*"},
		Exclude:         []string{"plugin-template"},
		Repos: []Repo{
			{Name: "api", Kind: KindGoService, Release: true},
			{Name: "charts", Kind: KindHelm, DependsOn: []string{"api"}, Charts: []string{"a", "b"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse() = %+v, want %+v", got, want)
	}
}

func TestParseErrors(t *testing.T) {
	const api = "  - {name: api, kind: go-service, release: true}\n"
	tests := []struct {
		name, yaml, wantErr string
	}{
		{"empty document", "", "manifest is empty"},
		{"second document", "repos:\n" + api + "---\nrepos: []\n", "single YAML document"},
		{"no repos", "holidays: []\n", "no repos"},
		{"unknown field", "repos:\n  - {name: api, kind: go-service, release: true, wave: 1}\n", "field wave not found"},
		{"missing release", "repos:\n  - {name: api, kind: go-service}\n", "release must be set"},
		{"missing name", "repos:\n  - {kind: go-service, release: true}\n", "name is required"},
		{"duplicate name", "repos:\n" + api + api, `"api" is listed more than once`},
		{"unknown kind", "repos:\n  - {name: api, kind: rust, release: true}\n", `unknown kind "rust"`},
		{"charts on non-helm repo", "repos:\n  - {name: api, kind: go-lib, release: true, charts: [x]}\n", "charts are only allowed"},
		{"unknown dependency", "repos:\n  - {name: ui, kind: ui, release: true, depends_on: [api]}\n", `unknown repo "api"`},
		{"self dependency", "repos:\n  - {name: api, kind: go-lib, release: true, depends_on: [api]}\n", "depends on itself"},
		{"duplicate dependency", "repos:\n" + api + "  - {name: ui, kind: ui, release: true, depends_on: [api, api]}\n", `dependency "api" more than once`},
		{"non-helm depends on helm", "repos:\n  - {name: c, kind: helm, release: true}\n  - {name: ui, kind: ui, release: true, depends_on: [c]}\n", `depends on helm repo "c"`},
		{"cycle", "repos:\n  - {name: a, kind: go-lib, release: true, depends_on: [b]}\n  - {name: b, kind: go-lib, release: true, depends_on: [a]}\n", "dependency cycle: cannot order repos a, b"},
		{"bad holiday", "holidays: [25/12/2026]\nrepos:\n" + api, "not an ISO date"},
		{"bad include pattern", "include_patterns: [\"plugin-[\"]\nrepos:\n" + api, "include pattern"},
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

func TestStages(t *testing.T) {
	repo := func(name string, deps ...string) Repo { return Repo{Name: name, Kind: KindGoLib, DependsOn: deps} }
	helm := func(name string, deps ...string) Repo { return Repo{Name: name, Kind: KindHelm, DependsOn: deps} }
	tests := []struct {
		name    string
		repos   []Repo
		want    [][]string
		wantErr string
	}{
		{"independent repos share a sorted stage", []Repo{repo("c"), repo("a"), repo("b")}, [][]string{{"a", "b", "c"}}, ""},
		{"chain", []Repo{repo("c", "b"), repo("b", "a"), repo("a")}, [][]string{{"a"}, {"b"}, {"c"}}, ""},
		{"waits for deepest dependency", []Repo{repo("a"), repo("b", "a"), repo("c", "a", "b")}, [][]string{{"a"}, {"b"}, {"c"}}, ""},
		{"diamond", []Repo{repo("top", "l", "r"), repo("l", "base"), repo("r", "base"), repo("base")}, [][]string{{"base"}, {"l", "r"}, {"top"}}, ""},
		{"duplicate dependency counted once", []Repo{repo("a"), repo("b", "a", "a")}, [][]string{{"a"}, {"b"}}, ""},
		{"helm releases after every non-helm repo", []Repo{repo("a"), repo("b", "a"), helm("charts", "a")}, [][]string{{"a"}, {"b"}, {"charts"}}, ""},
		{"helm without depends_on still last", []Repo{helm("charts"), repo("a"), repo("b", "a")}, [][]string{{"a"}, {"b"}, {"charts"}}, ""},
		{"helm-only manifest", []Repo{helm("x"), helm("y", "x")}, [][]string{{"x"}, {"y"}}, ""},
		{"two-repo cycle", []Repo{repo("a", "b"), repo("b", "a")}, nil, "dependency cycle: cannot order repos a, b"},
		{"cycle reports dependents too", []Repo{repo("root"), repo("x", "root", "z"), repo("y", "x"), repo("z", "y"), repo("leaf", "z")}, nil, "dependency cycle: cannot order repos leaf, x, y, z"},
		{"self cycle", []Repo{repo("a", "a")}, nil, "dependency cycle: cannot order repos a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (&Manifest{Repos: tt.repos}).Stages()
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("Stages() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Stages: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Stages() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNextWorkingWeekday(t *testing.T) {
	m := &Manifest{Holidays: []string{"2026-12-25", "2027-01-01", "2027-01-04"}}
	tests := []struct {
		name    string
		m       *Manifest
		in, out string
	}{
		{"working weekday is returned as is", m, "2026-12-01", "2026-12-01"},
		{"saturday moves to monday", m, "2026-12-05", "2026-12-07"},
		{"sunday moves to monday", m, "2026-12-06", "2026-12-07"},
		{"holiday on a friday moves to monday", m, "2026-12-25", "2026-12-28"},
		{"holiday, weekend, holiday", m, "2027-01-01", "2027-01-05"},
		{"2027-01-01 to 2027-01-04", &Manifest{Holidays: []string{"2027-01-01"}}, "2027-01-01", "2027-01-04"},
		{"no holidays: friday stays", &Manifest{}, "2027-01-01", "2027-01-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.NextWorkingWeekday(date(t, tt.in)).Format(dateLayout); got != tt.out {
				t.Errorf("NextWorkingWeekday(%s) = %s, want %s", tt.in, got, tt.out)
			}
		})
	}
}

func TestNextWorkingWeekdayUsesLocalDate(t *testing.T) {
	loc := time.FixedZone("UTC-5", -5*3600)
	// 20:00 on Thu 2026-12-24 in UTC-5 is already the 25th (a holiday) in UTC;
	// the local date counts, so the time is returned unchanged.
	in := time.Date(2026, 12, 24, 20, 0, 0, 0, loc)
	got := (&Manifest{Holidays: []string{"2026-12-25"}}).NextWorkingWeekday(in)
	if !got.Equal(in) || got.Location() != loc {
		t.Errorf("NextWorkingWeekday(%v) = %v, want it unchanged", in, got)
	}
}
