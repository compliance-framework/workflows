package release

import (
	"strings"
	"testing"
)

func TestComponentTag(t *testing.T) {
	for _, tc := range []struct{ tag, component, version, err string }{
		{"v1.2.3", "", "1.2.3", ""},
		{"v1.2.3-rc1", "", "1.2.3-rc1", ""},
		{"ccf-agent-v0.3.0", "ccf-agent", "0.3.0", ""},
		{"mock-app-v0.2.0-rc2", "mock-app", "0.2.0-rc2", ""},
		{"my-vault-v1.0.0", "my-vault", "1.0.0", ""},
		{"x-v1.0.0-very", "x", "1.0.0-very", ""},
		{"ccf-agent-0.3.0", "", "", "is not"},
		{"ccf-agent-v0.3", "", "", "is not"},
		{"-v1.0.0", "", "", "is not"},
		{"v1.0.0+build", "", "", "is not"},
		{"", "", "", "is not"},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			c, v, err := ComponentTag(tc.tag)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %q %q, err = %v; want %q", c, v, err, tc.err)
				}
				return
			}
			if err != nil || c != tc.component || v != tc.version {
				t.Fatalf("got %q %q, %v; want %q %q", c, v, err, tc.component, tc.version)
			}
		})
	}
}

func TestPickChart(t *testing.T) {
	agent := Chart{Dir: "ccf-agent", Name: "ccf-agent"}
	app := Chart{Dir: "ccf-app", Name: "ccf"}
	both := []Chart{agent, app}
	for _, tc := range []struct {
		name, component string
		charts          []Chart
		want            Chart
		err             string
	}{
		{"by directory", "ccf-agent", both, agent, ""},
		{"by directory, name differs", "ccf-app", both, app, ""},
		{"by Chart.yaml name", "ccf", both, app, ""},
		{"directory wins", "ccf", []Chart{app, {Dir: "ccf", Name: "other"}}, Chart{Dir: "ccf", Name: "other"}, ""},
		{"single chart, no component", "", []Chart{agent}, agent, ""},
		{"no component, two charts", "", both, Chart{}, "has 2 charts"},
		{"unknown", "nope", both, Chart{}, "found 0"},
		{"ambiguous name", "ccf", []Chart{app, {Dir: "ccf-2", Name: "ccf"}}, Chart{}, "found 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PickChart(tc.charts, tc.component)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %v, err = %v; want %q", got, err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
