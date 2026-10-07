package release

import (
	"slices"
	"testing"
)

func TestNonFinalDeps(t *testing.T) {
	gomod := `module github.com/compliance-framework/mock-agent

go 1.26

require (
	github.com/compliance-framework/api v1.4.0
	github.com/compliance-framework/mock-api v0.2.0-rc1
	github.com/compliance-framework/gooci v0.0.0-20260101000000-abcdefabcdef
	github.com/compliance-framework/old v2.0.0+incompatible
	github.com/other/lib v0.1.0-beta
)

replace github.com/compliance-framework/api => ../api
`
	got, err := NonFinalDeps([]byte(gomod))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"github.com/compliance-framework/mock-api@v0.2.0-rc1",
		"github.com/compliance-framework/gooci@v0.0.0-20260101000000-abcdefabcdef",
		"replace github.com/compliance-framework/api => ../api",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if _, err := NonFinalDeps([]byte("module (")); err == nil {
		t.Fatal("want a parse error")
	}
}

func TestCheckModulePath(t *testing.T) {
	for _, tc := range []struct {
		path, version string
		ok            bool
	}{
		{"github.com/compliance-framework/api", "0.9.0", true},
		{"github.com/compliance-framework/api", "1.0.0", true},
		{"github.com/compliance-framework/api", "2.0.0", false},
		{"github.com/compliance-framework/api/v2", "2.1.0", true},
		{"github.com/compliance-framework/api/v2", "3.0.0", false},
		{"github.com/compliance-framework/api/v2", "1.5.0", false},
		{"github.com/compliance-framework/api", "x", false},
	} {
		err := CheckModulePath([]byte("module "+tc.path+"\n"), tc.version)
		if (err == nil) != tc.ok {
			t.Errorf("%s at %s: err = %v, want ok=%v", tc.path, tc.version, err, tc.ok)
		}
	}
	if err := CheckModulePath([]byte("go 1.26\n"), "1.0.0"); err == nil {
		t.Error("want an error for a go.mod without a module directive")
	}
}

func TestMajorIncreases(t *testing.T) {
	for _, tc := range []struct {
		name       string
		base, head map[string]string
		want       []string
		err        bool
	}{
		{"minor", map[string]string{".": "0.4.1"}, map[string]string{".": "0.5.0"}, nil, false},
		{"0.x to 1.0", map[string]string{".": "0.9.0"}, map[string]string{".": "1.0.0"}, []string{".: v0.9.0 -> v1.0.0"}, false},
		{"1 to 2", map[string]string{".": "1.3.0"}, map[string]string{".": "2.0.0"}, []string{".: v1.3.0 -> v2.0.0"}, false},
		{"new package at 0.1", map[string]string{}, map[string]string{"charts/a": "0.1.0"}, nil, false},
		{"new package at 1.0", nil, map[string]string{"charts/a": "1.0.0"}, []string{"charts/a: v0.0.0 -> v1.0.0"}, false},
		{"one of two charts", map[string]string{"a": "1.0.0", "b": "1.0.0"}, map[string]string{"a": "1.1.0", "b": "2.0.0"}, []string{"b: v1.0.0 -> v2.0.0"}, false},
		{"invalid", map[string]string{".": "1.0.0"}, map[string]string{".": "two"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MajorIncreases(tc.base, tc.head)
			if (err != nil) != tc.err || !slices.Equal(got, tc.want) {
				t.Fatalf("got %q, %v; want %q, err=%v", got, err, tc.want, tc.err)
			}
		})
	}
}

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte(`{".": "1.2.3"}`))
	if err != nil || m["."] != "1.2.3" {
		t.Fatalf("got %v, %v", m, err)
	}
	if _, err := ParseManifest([]byte(`{".": 1}`)); err == nil {
		t.Fatal("want an error for a non-string version")
	}
}
