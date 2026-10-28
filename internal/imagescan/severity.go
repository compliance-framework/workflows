package imagescan

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// The Go vulnerability database publishes no CVSS, so osv-scanner rates a Go standard library or
// module finding from a GO- advisory UNKNOWN, and it would never count as HIGH or CRITICAL.
// Bitnami's copies of the same advisories (BIT-golang-..., BIT-<package>-...), which the
// finding lists as aliases, do carry a vector. So a finding without a score takes the highest
// score of its aliases' OSV records (docs/image-scan.md#severity).

// maxLookupFailures is how many alias lookups in a row may fail before the run stops looking
// aliases up: with api.osv.dev unreachable, each would wait for the client's timeout.
const maxLookupFailures = 3

// vulnCache holds the advisories one run has fetched from api.osv.dev by ID.
type vulnCache struct {
	mu       sync.Mutex
	vulns    map[string]*Vuln // nil: OSV has no such record
	failures int              // lookups that failed in a row
	lastErr  error
}

// Vuln returns advisory id from api.osv.dev (GET /v1/vulns/<id>), once per run: the result is
// cached. It is nil, with no error, when OSV has no record of that ID. After maxLookupFailures
// failed lookups in a row it fails at once, without asking.
func (o *OSV) Vuln(ctx context.Context, id string) (*Vuln, error) {
	c := &o.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.vulns[id]; ok {
		return v, nil
	}
	if c.failures >= maxLookupFailures {
		return nil, fmt.Errorf("OSV advisory %s not fetched: %d lookups failed in a row, the last with: %w", id, c.failures, c.lastErr)
	}
	var v *Vuln
	err := getJSON(ctx, o.HTTP, strings.TrimRight(o.BaseURL, "/")+"/v1/vulns/"+url.PathEscape(id), &v)
	if he := (*HTTPError)(nil); errors.As(err, &he) && he.Code == http.StatusNotFound {
		v, err = nil, nil
	}
	if err != nil {
		c.failures++
		c.lastErr = err
		return nil, fmt.Errorf("OSV advisory %s: %w", id, err)
	}
	c.failures = 0
	if c.vulns == nil {
		c.vulns = map[string]*Vuln{}
	}
	c.vulns[id] = v
	return v, nil
}

// RateFromAliases gives each finding without a CVSS score (UNKNOWN) the highest score of its
// aliases' OSV records, with the same rating as the finding's own advisories: Bitnami's (BIT-)
// first; only when none of them has a score, the others (CVE-, GHSA-, ...). The finding's own IDs
// are not fetched again: osv-scanner's report has their records. A finding whose lookups fail
// stays UNKNOWN, and the warnings returned say how many did; the scan goes on.
func (o *OSV) RateFromAliases(ctx context.Context, fs []Finding) []string {
	failed := 0
	var firstErr error
	for i := range fs {
		f := &fs[i]
		if f.Score > 0 {
			continue
		}
		var lookupErr error
		for _, tier := range aliasTiers(*f) {
			for _, id := range tier {
				v, err := o.Vuln(ctx, id)
				if err != nil {
					lookupErr = err
					continue
				}
				if v == nil {
					continue
				}
				if s := v.score(); s > f.Score {
					f.Score, f.ScoreFrom = s, id
				}
			}
			if f.Score > 0 {
				break
			}
		}
		if f.Score == 0 && lookupErr != nil {
			failed++
			if firstErr == nil {
				firstErr = lookupErr
			}
		}
	}
	if failed == 0 {
		return nil
	}
	return []string{fmt.Sprintf("the severity of %d finding(s) is UNKNOWN because their aliases could not be looked up, so they may be HIGH or CRITICAL and are not counted: %v", failed, firstErr)}
}

// aliasTiers returns the aliases of f to fetch, in the order they are tried: the Bitnami ones,
// then the rest.
func aliasTiers(f Finding) [][]string {
	var bit, rest []string
	for _, a := range f.Aliases {
		switch {
		case slices.Contains(f.IDs, a), slices.Contains(bit, a), slices.Contains(rest, a):
		case strings.HasPrefix(a, "BIT-"):
			bit = append(bit, a)
		default:
			rest = append(rest, a)
		}
	}
	return [][]string{bit, rest}
}
