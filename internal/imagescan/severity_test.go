package imagescan

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// goImage is agent's image as published, built with Go 1.26.1: its recorded osv-scanner report
// (testdata/osv-scanner/agent.json, trimmed to five advisories), whose GO- advisories have no
// CVSS. Distroless: no shell, no node.
var goImage = map[string]fakeImage{
	"ghcr.io/compliance-framework/agent:latest": {report: "agent.json", env: []string{"PATH=/usr/local/bin:/usr/bin"}},
	"ghcr.io/compliance-framework/agent:main":   {report: "agent.json", env: []string{"PATH=/usr/local/bin:/usr/bin"}},
}

// TestRateFromAliases: a finding osv-scanner leaves UNKNOWN takes the highest CVSS of its aliases'
// OSV records, Bitnami's first, then the others; each record is fetched once per run.
func TestRateFromAliases(t *testing.T) {
	osv := &fakeOSV{}
	s := newScannerOSV(t, &fakeDocker{images: goImage}, osv)
	scan, err := s.Scan(context.Background(), "ghcr.io/compliance-framework/agent:latest")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range scan.Findings {
		got = append(got, fmt.Sprintf("%s %s %s %s actionable=%v", f.IDs[0], f.Package, f.Severity(), f.ScoreFrom, f.Actionable()))
	}
	want := []string{
		// BIT-golang-2026-32282: AV:L/AC:H/PR:H, 6.4. CVE-2026-32282 is not fetched.
		"GO-2026-4864 stdlib MEDIUM BIT-golang-2026-32282 actionable=false",
		// No Bitnami alias: the CVE record's vector, S:C/C:H/I:H, 9.6.
		"GO-2026-5026 stdlib CRITICAL CVE-2026-39821 actionable=true",
		// BIT-golang-2026-97032: AV:N/AC:L/PR:N, A:H, 7.5.
		"GO-2026-6617 stdlib HIGH BIT-golang-2026-97032 actionable=true",
		// CVE-2026-111943 has no OSV record (404), the RUSTSEC ones no vector.
		"GO-2026-6809 stdlib UNKNOWN  actionable=false",
		// No alias at all.
		"GO-2026-5932 golang.org/x/crypto UNKNOWN  actionable=false",
	}
	if !slices.Equal(got, want) {
		t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(scan.Warnings) != 0 {
		t.Errorf("warnings = %q, want none (a 404 is no record, not a failure)", scan.Warnings)
	}
	wantFetched := []string{
		"BIT-golang-2026-32282",
		"CVE-2026-39821",
		"BIT-golang-2026-97032",
		"CVE-2026-111943", "RUSTSEC-2026-0381", "RUSTSEC-2026-0382", "RUSTSEC-2026-0383",
	}
	if !slices.Equal(osv.requests(), wantFetched) {
		t.Errorf("fetched %q, want %q", osv.requests(), wantFetched)
	}
	golden(t, filepath.Join("testdata", "findings", "agent_latest.golden.json"), scan)

	// Another image with the same advisories: nothing is fetched again, 404s included.
	if _, err := s.Scan(context.Background(), "ghcr.io/compliance-framework/agent:main"); err != nil {
		t.Fatal(err)
	}
	if len(osv.requests()) != len(wantFetched) {
		t.Errorf("after the second scan fetched %q, want nothing more than the first scan (the run's cache)", osv.requests())
	}
}

// TestRateFromAliasesOSVDown: with api.osv.dev failing, the findings stay UNKNOWN, the scan goes
// on with a warning, and the image's scan stops asking after maxLookupFailures failures in a row
// (each retried once). The breaker is per image: the next image is rated once OSV is back.
func TestRateFromAliasesOSVDown(t *testing.T) {
	osv := &fakeOSV{down: true}
	s := newScannerOSV(t, &fakeDocker{images: goImage}, osv)
	scan, err := s.Scan(context.Background(), "ghcr.io/compliance-framework/agent:latest")
	if err != nil {
		t.Fatalf("Scan failed with OSV down: %v", err)
	}
	for _, f := range scan.Findings {
		if f.Severity() != "UNKNOWN" || f.ScoreFrom != "" {
			t.Errorf("%s: %s from %q, want UNKNOWN", f.IDs[0], f.Severity(), f.ScoreFrom)
		}
	}
	if len(osv.requests()) != 2*maxLookupFailures {
		t.Errorf("fetched %q, want %d lookups, each retried once, and then none", osv.requests(), maxLookupFailures)
	}
	if len(scan.Warnings) != 1 || !strings.Contains(scan.Warnings[0], "the severity of 4 finding(s) may be too low") || !strings.Contains(scan.Warnings[0], "503") {
		t.Errorf("warnings = %q, want one for the 4 findings with aliases, with the error", scan.Warnings)
	}

	osv.down = false
	scan, err = s.Scan(context.Background(), "ghcr.io/compliance-framework/agent:main")
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Warnings) != 0 || !slices.ContainsFunc(scan.Findings, func(f Finding) bool { return f.ScoreFrom == "BIT-golang-2026-97032" }) {
		t.Errorf("the next image after an outage: warnings %q, findings %+v; want it rated", scan.Warnings, scan.Findings)
	}
}

// TestRateFromAliasesOutageBound: an outage that lasts the whole run costs at most
// maxTrippedImages images' worth of lookups, however many images the run scans, whether OSV
// answers 503 (retried once) or not at all (each attempt cut at the lookup timeout); every image
// still warns.
func TestRateFromAliasesOutageBound(t *testing.T) {
	images := map[string]fakeImage{}
	var refs []string
	for i := range 6 {
		ref := fmt.Sprintf("ghcr.io/compliance-framework/agent:t%d", i)
		images[ref] = goImage["ghcr.io/compliance-framework/agent:latest"]
		refs = append(refs, ref)
	}
	for _, tc := range []struct {
		name     string
		osv      fakeOSV
		requests int
	}{
		{"503", fakeOSV{down: true}, maxTrippedImages * maxLookupFailures * 2},
		{"no answer", fakeOSV{hang: true}, maxTrippedImages * maxLookupFailures},
	} {
		t.Run(tc.name, func(t *testing.T) {
			osv := &tc.osv
			s := newScannerOSV(t, &fakeDocker{images: images}, osv)
			s.OSV.lookupTimeout = 50 * time.Millisecond
			start := time.Now()
			for _, ref := range refs {
				scan, err := s.Scan(context.Background(), ref)
				if err != nil {
					t.Fatal(err)
				}
				if len(scan.Warnings) != 1 {
					t.Errorf("%s: warnings %q, want one", ref, scan.Warnings)
				}
			}
			if len(osv.requests()) != tc.requests {
				t.Errorf("%d images made %d requests (%q), want %d", len(refs), len(osv.requests()), osv.requests(), tc.requests)
			}
			// maxTrippedImages*maxLookupFailures attempts of 50ms, with room for a slow runner.
			if d := time.Since(start); d > 3*time.Second {
				t.Errorf("the run took %v, want it bounded", d)
			}
		})
	}
}

// TestRateFromAliasesTiers: the highest score among the Bitnami aliases wins; the other aliases
// are tried only when every Bitnami one was fetched and none has a vector (or a record), and the
// one with a vector rates it; a failed lookup in the deciding tier is warned about unless the
// finding is CRITICAL anyway; a 429 or 5xx is retried once; a finding with a score is left alone.
func TestRateFromAliasesTiers(t *testing.T) {
	const bit6, bit7, cve9 = "BIT-golang-2026-32282", "BIT-golang-2026-97032", "CVE-2026-39821"
	for _, tc := range []struct {
		name     string
		f        Finding
		osv      fakeOSV
		score    float64
		from     string
		fetched  []string
		severity string
		warn     bool
	}{
		{name: "the higher of two Bitnami aliases", f: Finding{IDs: []string{"GO-1"}, Aliases: []string{bit6, bit7, cve9}},
			score: 7.5, from: bit7, fetched: []string{bit6, bit7}, severity: "HIGH"},
		{name: "a Bitnami alias without a record falls through", f: Finding{IDs: []string{"GO-1"}, Aliases: []string{"BIT-golang-2026-00000", cve9}},
			score: 9.6, from: cve9, fetched: []string{"BIT-golang-2026-00000", cve9}, severity: "CRITICAL"},
		{name: "the other aliases: the one with a vector", f: Finding{IDs: []string{"GO-1"}, Aliases: []string{"BIT-x", "RUSTSEC-2026-0381", cve9}},
			score: 9.6, from: cve9, fetched: []string{"BIT-x", "RUSTSEC-2026-0381", cve9}, severity: "CRITICAL"},
		{name: "a finding with a score is not looked up", f: Finding{IDs: []string{"GO-1"}, Aliases: []string{bit7}, Score: 5},
			score: 5, fetched: nil, severity: "MEDIUM"},
		{name: "one of two Bitnami aliases fails: the other's score, with a warning", f: Finding{IDs: []string{"GO-1"}, Aliases: []string{bit6, "BIT-golang-2026-99999", cve9}},
			osv:   fakeOSV{fail: map[string]int{"BIT-golang-2026-99999": -1}},
			score: 6.4, from: bit6, fetched: []string{bit6, "BIT-golang-2026-99999", "BIT-golang-2026-99999"}, severity: "MEDIUM", warn: true},
		{name: "every Bitnami alias fails: no fall-through, UNKNOWN with a warning", f: Finding{IDs: []string{"GO-1"}, Aliases: []string{bit7, cve9}},
			osv:   fakeOSV{fail: map[string]int{bit7: -1}},
			score: 0, fetched: []string{bit7, bit7}, severity: "UNKNOWN", warn: true},
		{name: "a failure next to a CRITICAL score: no warning", f: Finding{IDs: []string{"GO-1"}, Aliases: []string{cve9, "GHSA-xxxx"}},
			osv:   fakeOSV{fail: map[string]int{"GHSA-xxxx": -1}},
			score: 9.6, from: cve9, fetched: []string{cve9, "GHSA-xxxx", "GHSA-xxxx"}, severity: "CRITICAL"},
		{name: "a 503 is retried once", f: Finding{IDs: []string{"GO-1"}, Aliases: []string{bit7}},
			osv:   fakeOSV{fail: map[string]int{bit7: 1}},
			score: 7.5, from: bit7, fetched: []string{bit7, bit7}, severity: "HIGH"},
		{name: "a 429 is retried after its Retry-After", f: Finding{IDs: []string{"GO-1"}, Aliases: []string{bit7}},
			osv:   fakeOSV{fail: map[string]int{bit7: 1}, failCode: http.StatusTooManyRequests, retryAfter: "0"},
			score: 7.5, from: bit7, fetched: []string{bit7, bit7}, severity: "HIGH"},
		{name: "a 400 is not retried", f: Finding{IDs: []string{"GO-1"}, Aliases: []string{bit7}},
			osv:   fakeOSV{fail: map[string]int{bit7: 1}, failCode: http.StatusBadRequest},
			score: 0, fetched: []string{bit7}, severity: "UNKNOWN", warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &tc.osv
			osv := &OSV{BaseURL: fake.serve(t).URL, retryWait: time.Millisecond}
			fs := []Finding{tc.f}
			w := osv.RateFromAliases(context.Background(), fs)
			if (len(w) > 0) != tc.warn {
				t.Errorf("warnings %q, want a warning: %v", w, tc.warn)
			}
			if fs[0].Score != tc.score || fs[0].ScoreFrom != tc.from || fs[0].Severity() != tc.severity {
				t.Errorf("score %v from %q (%s), want %v from %q (%s)", fs[0].Score, fs[0].ScoreFrom, fs[0].Severity(), tc.score, tc.from, tc.severity)
			}
			if !slices.Equal(fake.requests(), tc.fetched) {
				t.Errorf("fetched %q, want %q", fake.requests(), tc.fetched)
			}
		})
	}
}

func TestAliasTiers(t *testing.T) {
	f := Finding{IDs: []string{"GO-1"}, Aliases: []string{"CVE-1", "GO-1", "BIT-golang-1", "GHSA-x", "BIT-golang-1", "BIT-node-1"}}
	got := aliasTiers(f)
	if want := [][]string{{"BIT-golang-1", "BIT-node-1"}, {"CVE-1", "GHSA-x"}}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("aliasTiers = %q, want %q", got, want)
	}
}
