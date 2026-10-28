package imagescan

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
	if !slices.Equal(osv.fetched, wantFetched) {
		t.Errorf("fetched %q, want %q", osv.fetched, wantFetched)
	}
	golden(t, filepath.Join("testdata", "findings", "agent_latest.golden.json"), scan)

	// Another image with the same advisories: nothing is fetched again, 404s included.
	if _, err := s.Scan(context.Background(), "ghcr.io/compliance-framework/agent:main"); err != nil {
		t.Fatal(err)
	}
	if len(osv.fetched) != len(wantFetched) {
		t.Errorf("second scan fetched %q again, want the run's cache", osv.fetched[len(wantFetched):])
	}
}

// TestRateFromAliasesOSVDown: with api.osv.dev failing, the findings stay UNKNOWN, the scan goes
// on with a warning, and the run stops asking after maxLookupFailures failures in a row.
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
	if len(osv.fetched) != maxLookupFailures {
		t.Errorf("fetched %q, want %d lookups and then none", osv.fetched, maxLookupFailures)
	}
	if len(scan.Warnings) != 1 || !strings.Contains(scan.Warnings[0], "the severity of 4 finding(s) is UNKNOWN because their aliases could not be looked up") || !strings.Contains(scan.Warnings[0], "503") {
		t.Errorf("warnings = %q, want one for the 4 findings with aliases, with the error", scan.Warnings)
	}
}

func TestAliasTiers(t *testing.T) {
	f := Finding{IDs: []string{"GO-1"}, Aliases: []string{"CVE-1", "GO-1", "BIT-golang-1", "GHSA-x", "BIT-golang-1", "BIT-node-1"}}
	got := aliasTiers(f)
	if want := [][]string{{"BIT-golang-1", "BIT-node-1"}, {"CVE-1", "GHSA-x"}}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("aliasTiers = %q, want %q", got, want)
	}
}
