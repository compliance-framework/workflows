package manifest

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadRepoManifests(t *testing.T) {
	tests := []struct {
		path   string
		stages [][]string
	}{
		{
			path: "../../repos.yaml",
			stages: [][]string{
				{"api", "gooci"},
				{"agent", "ui"},
				{"agent-action"},
				{"helm-charts"},
			},
		},
		{
			path: "../../repos.mock.yaml",
			stages: [][]string{
				{"mock-api", "mock-gooci"},
				{"mock-agent", "mock-ui"},
				{"mock-agent-action", "mock-plugin-1", "mock-plugin-2", "mock-plugin-policies-1", "mock-plugin-policies-2"},
				{"mock-helm-charts"},
			},
		},
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
			if want := []string{"2026-12-25", "2027-01-01"}; !reflect.DeepEqual(m.Holidays, want) {
				t.Errorf("Holidays = %v, want %v", m.Holidays, want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("does-not-exist.yaml"); err == nil {
		t.Fatal("Load of a missing file succeeded")
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string // empty: must succeed
		want    *Manifest
	}{
		{
			name: "all fields",
			yaml: `
holidays: ["2026-12-25", 2027-01-01]
include_patterns: ["plugin-*"]
exclude: [plugin-template]
repos:
  - {name: api, kind: go-service, release: true}
  - {name: charts, kind: helm, depends_on: [api], release: false, charts: [a, b]}
`,
			want: &Manifest{
				Holidays:        []string{"2026-12-25", "2027-01-01"},
				IncludePatterns: []string{"plugin-*"},
				Exclude:         []string{"plugin-template"},
				Repos: []Repo{
					{Name: "api", Kind: KindGoService, Release: true},
					{Name: "charts", Kind: KindHelm, DependsOn: []string{"api"}, Charts: []string{"a", "b"}},
				},
			},
		},
		{
			name:    "empty document",
			yaml:    "",
			wantErr: "manifest is empty",
		},
		{
			name:    "no repos",
			yaml:    "holidays: []\n",
			wantErr: "no repos",
		},
		{
			name:    "unknown field",
			yaml:    "repos:\n  - {name: api, kind: go-service, release: true, wave: 1}\n",
			wantErr: "field wave not found",
		},
		{
			name:    "missing release",
			yaml:    "repos:\n  - {name: api, kind: go-service}\n",
			wantErr: "release must be set",
		},
		{
			name:    "missing name",
			yaml:    "repos:\n  - {kind: go-service, release: true}\n",
			wantErr: "name is required",
		},
		{
			name:    "duplicate name",
			yaml:    "repos:\n  - {name: api, kind: go-service, release: true}\n  - {name: api, kind: go-lib, release: true}\n",
			wantErr: `"api" is listed more than once`,
		},
		{
			name:    "unknown kind",
			yaml:    "repos:\n  - {name: api, kind: rust, release: true}\n",
			wantErr: `unknown kind "rust"`,
		},
		{
			name:    "charts on non-helm repo",
			yaml:    "repos:\n  - {name: api, kind: go-service, release: true, charts: [x]}\n",
			wantErr: "charts are only allowed",
		},
		{
			name:    "unknown dependency",
			yaml:    "repos:\n  - {name: agent, kind: go-service, release: true, depends_on: [api]}\n",
			wantErr: `unknown repo "api"`,
		},
		{
			name:    "self dependency",
			yaml:    "repos:\n  - {name: api, kind: go-service, release: true, depends_on: [api]}\n",
			wantErr: "depends on itself",
		},
		{
			name:    "duplicate dependency",
			yaml:    "repos:\n  - {name: api, kind: go-service, release: true}\n  - {name: ui, kind: ui, release: true, depends_on: [api, api]}\n",
			wantErr: `dependency "api" more than once`,
		},
		{
			name:    "cycle",
			yaml:    "repos:\n  - {name: a, kind: go-lib, release: true, depends_on: [b]}\n  - {name: b, kind: go-lib, release: true, depends_on: [a]}\n",
			wantErr: "dependency cycle: cannot order repos a, b",
		},
		{
			name:    "non-helm repo depends on a helm repo",
			yaml:    "repos:\n  - {name: charts, kind: helm, release: true}\n  - {name: api, kind: go-service, release: true, depends_on: [charts]}\n",
			wantErr: `depends on helm repo "charts"`,
		},
		{
			name:    "bad holiday",
			yaml:    "holidays: [25/12/2026]\nrepos:\n  - {name: api, kind: go-service, release: true}\n",
			wantErr: "not an ISO date",
		},
		{
			name:    "bad include pattern",
			yaml:    "include_patterns: [\"plugin-[\"]\nrepos:\n  - {name: api, kind: go-service, release: true}\n",
			wantErr: "include pattern",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.yaml))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Parse() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStages(t *testing.T) {
	repo := func(name string, deps ...string) Repo {
		return Repo{Name: name, Kind: KindGoLib, DependsOn: deps}
	}
	helm := func(name string, deps ...string) Repo {
		return Repo{Name: name, Kind: KindHelm, DependsOn: deps}
	}
	tests := []struct {
		name    string
		repos   []Repo
		want    [][]string
		wantErr string
	}{
		{
			name:  "single repo",
			repos: []Repo{repo("a")},
			want:  [][]string{{"a"}},
		},
		{
			name:  "independent repos share a sorted stage",
			repos: []Repo{repo("c"), repo("a"), repo("b")},
			want:  [][]string{{"a", "b", "c"}},
		},
		{
			name:  "chain",
			repos: []Repo{repo("c", "b"), repo("b", "a"), repo("a")},
			want:  [][]string{{"a"}, {"b"}, {"c"}},
		},
		{
			name:  "repo waits for its deepest dependency",
			repos: []Repo{repo("a"), repo("b", "a"), repo("c", "a", "b")},
			want:  [][]string{{"a"}, {"b"}, {"c"}},
		},
		{
			name:  "diamond",
			repos: []Repo{repo("top", "left", "right"), repo("left", "base"), repo("right", "base"), repo("base")},
			want:  [][]string{{"base"}, {"left", "right"}, {"top"}},
		},
		{
			name:  "helm repo releases after every non-helm repo",
			repos: []Repo{repo("a"), repo("b", "a"), helm("charts", "a")},
			want:  [][]string{{"a"}, {"b"}, {"charts"}},
		},
		{
			name:  "helm repo with no depends_on still releases last",
			repos: []Repo{helm("charts"), repo("a"), repo("b", "a")},
			want:  [][]string{{"a"}, {"b"}, {"charts"}},
		},
		{
			name:  "helm-only manifest",
			repos: []Repo{helm("x"), helm("y", "x")},
			want:  [][]string{{"x"}, {"y"}},
		},
		{
			name:  "duplicate dependency counted once",
			repos: []Repo{repo("a"), repo("b", "a", "a")},
			want:  [][]string{{"a"}, {"b"}},
		},
		{
			name:    "two-repo cycle",
			repos:   []Repo{repo("a", "b"), repo("b", "a")},
			wantErr: "dependency cycle: cannot order repos a, b",
		},
		{
			name:    "cycle reports dependents too",
			repos:   []Repo{repo("root"), repo("x", "root", "z"), repo("y", "x"), repo("z", "y"), repo("leaf", "z")},
			wantErr: "dependency cycle: cannot order repos leaf, x, y, z",
		},
		{
			name:    "self cycle",
			repos:   []Repo{repo("a", "a")},
			wantErr: "dependency cycle: cannot order repos a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Manifest{Repos: tt.repos}
			got, err := m.Stages()
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
	noHolidays := &Manifest{}
	date := func(s string) time.Time {
		d, err := time.Parse(dateLayout, s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	tests := []struct {
		name string
		m    *Manifest
		in   string
		want string
	}{
		{"working weekday is returned as is", m, "2026-12-01", "2026-12-01"},
		{"saturday moves to monday", m, "2026-12-05", "2026-12-07"},
		{"sunday moves to monday", m, "2026-12-06", "2026-12-07"},
		{"holiday on a friday moves to monday", m, "2026-12-25", "2026-12-28"},
		{"holiday then weekend then holiday", m, "2027-01-01", "2027-01-05"},
		{"2027-01-01 to 2027-01-04 with no extra holiday", &Manifest{Holidays: []string{"2027-01-01"}}, "2027-01-01", "2027-01-04"},
		{"no holidays: friday stays", noHolidays, "2027-01-01", "2027-01-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.m.NextWorkingWeekday(date(tt.in))
			if got.Format(dateLayout) != tt.want {
				t.Errorf("NextWorkingWeekday(%s) = %s, want %s", tt.in, got.Format(dateLayout), tt.want)
			}
		})
	}
}

func TestNextWorkingWeekdayKeepsTimeAndLocation(t *testing.T) {
	loc := time.FixedZone("UTC-5", -5*3600)
	// 08:00 on Fri 2027-01-01 in UTC-5; the holiday check uses the local date.
	in := time.Date(2027, 1, 1, 8, 0, 0, 0, loc)
	m := &Manifest{Holidays: []string{"2027-01-01"}}
	got := m.NextWorkingWeekday(in)
	want := time.Date(2027, 1, 4, 8, 0, 0, 0, loc)
	if !got.Equal(want) || got.Location() != loc {
		t.Errorf("NextWorkingWeekday(%v) = %v, want %v", in, got, want)
	}
}

func TestNextWorkingWeekdayRealManifest(t *testing.T) {
	m, err := Load("../../repos.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got := m.NextWorkingWeekday(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if want := "2027-01-04"; got.Format(dateLayout) != want {
		t.Errorf("NextWorkingWeekday(2027-01-01) = %s, want %s", got.Format(dateLayout), want)
	}
}
