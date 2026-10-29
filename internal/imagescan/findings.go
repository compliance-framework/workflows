package imagescan

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	gocvss20 "github.com/pandatix/go-cvss/20"
	gocvss30 "github.com/pandatix/go-cvss/30"
	gocvss31 "github.com/pandatix/go-cvss/31"
	gocvss40 "github.com/pandatix/go-cvss/40"
)

// Finding types.
const (
	TypeOS     = "os"     // an OS package (apk, dpkg)
	TypeLang   = "lang"   // a language package (Go module, npm package, ...)
	TypeBinary = "binary" // a language runtime binary, from the runtime check
)

// Finding is one vulnerability of one package in one image.
type Finding struct {
	Image     string `json:"image"`           // the scanned reference
	Stage     string `json:"stage,omitempty"` // the Dockerfile stage, for a base image
	Package   string `json:"package"`         // the installed package ("brotli-libs", "node")
	Source    string `json:"source"`          // the advisory's package (the source package of an OS package)
	Type      string `json:"type"`            // TypeOS, TypeLang or TypeBinary
	Ecosystem string `json:"ecosystem"`       // "Alpine:v3.24", "Debian:12", "Bitnami"
	Installed string `json:"installed"`
	// Fixed lists the versions that fix it: for a package, the advisory's fixed version for the
	// installed release; for a runtime, every fixed version above the installed one, lowest first,
	// which may all be in other majors.
	Fixed   []string `json:"fixed,omitempty"`
	IDs     []string `json:"ids"`               // the advisory IDs ("ALPINE-CVE-2026-111497")
	Aliases []string `json:"aliases,omitempty"` // their aliases ("CVE-2026-111497")
	Score   float64  `json:"score"`             // the highest CVSS base score of the advisories; 0 if none
	// ScoreFrom is the alias whose OSV record gave Score, when the advisories themselves have no
	// CVSS (a GO- advisory rated by its BIT-golang- alias); "" otherwise.
	ScoreFrom string `json:"score_from,omitempty"`
}

// Severity is the rating of Score: CRITICAL from 9.0, HIGH from 7.0, MEDIUM from 4.0, LOW above
// 0, UNKNOWN without a CVSS score.
func (f Finding) Severity() string {
	switch {
	case f.Score >= 9:
		return "CRITICAL"
	case f.Score >= 7:
		return "HIGH"
	case f.Score >= 4:
		return "MEDIUM"
	case f.Score > 0:
		return "LOW"
	}
	return "UNKNOWN"
}

// GoStdlib reports whether the finding is in the Go standard library compiled into a binary: what
// fixes it is building the binary with a newer Go.
func (f Finding) GoStdlib() bool { return f.Ecosystem == "Go" && f.Source == "stdlib" }

// Actionable reports whether the finding has a fix and is HIGH or CRITICAL.
func (f Finding) Actionable() bool { return len(f.Fixed) > 0 && f.Score >= 7 }

// FixedInMajor reports whether a fix exists in the installed version's major: always for a
// package, whose fixed version is for its own release; for a runtime, only if a fixed version
// has the installed major.
func (f Finding) FixedInMajor() bool {
	if f.Type != TypeBinary {
		return len(f.Fixed) > 0
	}
	return slices.ContainsFunc(f.Fixed, func(v string) bool { return major(v) == major(f.Installed) })
}

// CVEs returns the finding's CVE IDs, or its advisory IDs when it has none.
func (f Finding) CVEs() []string {
	var out []string
	for _, id := range append(slices.Clone(f.IDs), f.Aliases...) {
		if strings.HasPrefix(id, "CVE-") && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return slices.Clone(f.IDs)
	}
	slices.Sort(out)
	return out
}

// sameVuln reports whether f and g are the same vulnerability of the same package: the same
// advisory package (or installed package) and a shared ID or alias. Advisory IDs differ between
// releases of a distribution, so the CVE alias is what usually matches.
func sameVuln(f, g Finding) bool {
	if f.Source != g.Source && f.Package != g.Package {
		return false
	}
	ids := append(slices.Clone(f.IDs), f.Aliases...)
	for _, id := range append(slices.Clone(g.IDs), g.Aliases...) {
		if slices.Contains(ids, id) {
			return true
		}
	}
	return false
}

// Vuln is an OSV advisory, as osv-scanner reports it and api.osv.dev returns it.
type Vuln struct {
	ID       string     `json:"id"`
	Aliases  []string   `json:"aliases"`
	Upstream []string   `json:"upstream"`
	Severity []Severity `json:"severity"`
	Affected []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Type   string `json:"type"`
			Events []struct {
				Introduced string `json:"introduced,omitempty"`
				Fixed      string `json:"fixed,omitempty"`
			} `json:"events"`
		} `json:"ranges"`
		Severity []Severity `json:"severity"`
	} `json:"affected"`
}

// Severity is an OSV severity entry: a CVSS vector.
type Severity struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}

// score is the highest CVSS base score of the advisory's vectors (top level and per affected
// package); 0 when it has none that parses.
func (v Vuln) score() float64 {
	all := slices.Clone(v.Severity)
	for _, a := range v.Affected {
		all = append(all, a.Severity...)
	}
	best := 0.0
	for _, s := range all {
		if n, ok := cvssScore(s.Type, s.Score); ok && n > best {
			best = n
		}
	}
	return best
}

// fixed returns the fixed versions the advisory gives for package name in ecosystem.
func (v Vuln) fixed(ecosystem, name string) []string {
	var out []string
	for _, a := range v.Affected {
		if a.Package.Ecosystem != ecosystem || a.Package.Name != name {
			continue
		}
		for _, r := range a.Ranges {
			for _, e := range r.Events {
				if e.Fixed != "" && !slices.Contains(out, e.Fixed) {
					out = append(out, e.Fixed)
				}
			}
		}
	}
	return out
}

func (v Vuln) aliases() []string {
	var out []string
	for _, a := range append(slices.Clone(v.Aliases), v.Upstream...) {
		if a != v.ID && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// cvssScore returns the base score of a CVSS v2, v3.0, v3.1 or v4.0 vector, computed with the
// library osv-scanner uses for its own severities.
func cvssScore(typ, vector string) (float64, bool) {
	switch {
	case typ == "CVSS_V4" || strings.HasPrefix(vector, "CVSS:4.0/"):
		c, err := gocvss40.ParseVector(vector)
		if err != nil {
			return 0, false
		}
		return c.Score(), true
	case strings.HasPrefix(vector, "CVSS:3.1/"):
		c, err := gocvss31.ParseVector(vector)
		if err != nil {
			return 0, false
		}
		return c.BaseScore(), true
	case strings.HasPrefix(vector, "CVSS:3.0/"):
		c, err := gocvss30.ParseVector(vector)
		if err != nil {
			return 0, false
		}
		return c.BaseScore(), true
	case typ == "CVSS_V2":
		c, err := gocvss20.ParseVector(vector)
		if err != nil {
			return 0, false
		}
		return c.BaseScore(), true
	}
	return 0, false
}

// osvReport is the part of osv-scanner's JSON output (`scan image --format json`) the tool reads.
type osvReport struct {
	Results []struct {
		Source struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"source"`
		Packages []struct {
			Package struct {
				Name          string `json:"name"`
				OSPackageName string `json:"os_package_name"`
				Version       string `json:"version"`
				Ecosystem     string `json:"ecosystem"`
			} `json:"package"`
			Vulnerabilities []Vuln `json:"vulnerabilities"`
			Groups          []struct {
				IDs         []string `json:"ids"`
				Aliases     []string `json:"aliases"`
				MaxSeverity string   `json:"max_severity"`
			} `json:"groups"`
		} `json:"packages"`
	} `json:"results"`
	ImageMetadata struct {
		OS string `json:"os"`
	} `json:"image_metadata"`
}

// ParseOSVScanner normalizes osv-scanner's JSON report of image into findings, one per package
// and vulnerability group (osv-scanner groups an advisory with its aliases), and returns the
// image's OS as osv-scanner names it ("Alpine Linux v3.24").
func ParseOSVScanner(data []byte, image string) ([]Finding, string, error) {
	var r osvReport
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, "", fmt.Errorf("osv-scanner report of %s: %w", image, err)
	}
	var out []Finding
	for _, res := range r.Results {
		typ := TypeLang
		if res.Source.Type == "os" {
			typ = TypeOS
		}
		for _, p := range res.Packages {
			pkg := p.Package.OSPackageName
			if pkg == "" {
				pkg = p.Package.Name
			}
			for _, g := range p.Groups {
				f := Finding{
					Image:     image,
					Package:   pkg,
					Source:    p.Package.Name,
					Type:      typ,
					Ecosystem: p.Package.Ecosystem,
					Installed: p.Package.Version,
					IDs:       slices.Clone(g.IDs),
				}
				for _, v := range p.Vulnerabilities {
					if !slices.Contains(g.IDs, v.ID) {
						continue
					}
					for _, fx := range v.fixed(p.Package.Ecosystem, p.Package.Name) {
						if !slices.Contains(f.Fixed, fx) {
							f.Fixed = append(f.Fixed, fx)
						}
					}
					f.Score = max(f.Score, v.score())
				}
				// osv-scanner's own rating of the group, if the advisories carry no vector it reads.
				if s, err := strconv.ParseFloat(g.MaxSeverity, 64); err == nil && f.Score == 0 {
					f.Score = s
				}
				for _, a := range g.Aliases {
					if !slices.Contains(f.IDs, a) {
						f.Aliases = append(f.Aliases, a)
					}
				}
				out = append(out, f)
			}
		}
	}
	return out, r.ImageMetadata.OS, nil
}
