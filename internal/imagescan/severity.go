package imagescan

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Go vulnerability database publishes no CVSS, so osv-scanner rates a Go standard library or
// module finding from a GO- advisory UNKNOWN, and it would never count as HIGH or CRITICAL.
// Bitnami's copies of the same advisories (BIT-golang-..., BIT-<package>-...), which the
// finding lists as aliases, do carry a vector. So a finding without a score takes the highest
// score of its aliases' OSV records (docs/image-scan.md#severity).

// maxLookupFailures is how many alias lookups in a row may fail before the scan of an image stops
// looking aliases up: with api.osv.dev unreachable, each would wait for the client's timeout. It
// is per image (RateFromAliases resets it), so an outage early in a long run doesn't leave every
// later image unrated.
const maxLookupFailures = 3

// maxTrippedImages bounds the whole run: after the breaker trips on this many images in a row
// (no lookup answered in between), alias lookups are off for the rest of the run, so an outage
// costs at most maxTrippedImages*maxLookupFailures lookups however many images the run scans.
const maxTrippedImages = 2

// vulnLookupTimeout bounds one alias lookup (each attempt), well under the client's 30s.
const vulnLookupTimeout = 10 * time.Second

// defaultRetryWait is how long a lookup answered 429 or 5xx waits before its one retry, unless the
// answer's Retry-After says otherwise (up to maxRetryWait).
const (
	defaultRetryWait = 2 * time.Second
	maxRetryWait     = 10 * time.Second
)

// vulnCache holds the advisories one run has fetched from api.osv.dev by ID.
type vulnCache struct {
	mu       sync.Mutex
	vulns    map[string]*Vuln // nil: OSV has no such record
	failures int              // lookups that failed in a row, for the image being rated
	lastErr  error
	tripped  int  // images in a row whose breaker tripped
	off      bool // alias lookups are off for the rest of the run (maxTrippedImages)
}

// Vuln returns advisory id from api.osv.dev (GET /v1/vulns/<id>), once per run: the result is
// cached. It is nil, with no error, when OSV has no record of that ID. An answer of 429 or 5xx is
// retried once, after its Retry-After or defaultRetryWait. After maxLookupFailures failed lookups
// in a row it fails at once, without asking, until RateFromAliases starts on the next image.
func (o *OSV) Vuln(ctx context.Context, id string) (*Vuln, error) {
	c := &o.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.vulns[id]; ok {
		return v, nil
	}
	if c.off {
		return nil, fmt.Errorf("OSV advisory %s not fetched: alias lookups are off for the rest of the run, after %d images in a row whose lookups failed, the last with: %w", id, maxTrippedImages, c.lastErr)
	}
	if c.failures >= maxLookupFailures {
		return nil, fmt.Errorf("OSV advisory %s not fetched: %d lookups failed in a row, the last with: %w", id, c.failures, c.lastErr)
	}
	v, err := o.fetchVuln(ctx, id)
	if err != nil {
		c.failures++
		c.lastErr = err
		return nil, fmt.Errorf("OSV advisory %s: %w", id, err)
	}
	c.failures, c.tripped = 0, 0
	if c.vulns == nil {
		c.vulns = map[string]*Vuln{}
	}
	c.vulns[id] = v
	return v, nil
}

// fetchVuln gets advisory id, retrying a 429 or 5xx once, each attempt within vulnLookupTimeout;
// a 404 is no record (nil).
func (o *OSV) fetchVuln(ctx context.Context, id string) (*Vuln, error) {
	timeout := cmp.Or(o.lookupTimeout, vulnLookupTimeout)
	for attempt := 0; ; attempt++ {
		var v *Vuln
		actx, cancel := context.WithTimeout(ctx, timeout)
		err := getJSON(actx, o.HTTP, strings.TrimRight(o.BaseURL, "/")+"/v1/vulns/"+url.PathEscape(id), &v)
		cancel()
		he := (*httpError)(nil)
		if !errors.As(err, &he) {
			return v, err
		}
		if he.Code == http.StatusNotFound {
			return nil, nil
		}
		if attempt > 0 || (he.Code != http.StatusTooManyRequests && he.Code < 500) {
			return nil, err
		}
		wait := cmp.Or(o.retryWait, defaultRetryWait)
		if secs, perr := strconv.Atoi(he.RetryAfter); perr == nil && secs >= 0 {
			wait = min(time.Duration(secs)*time.Second, maxRetryWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// RateFromAliases gives each finding without a CVSS score (UNKNOWN) the highest score of its
// aliases' OSV records, with the same rating as the finding's own advisories: Bitnami's (BIT-)
// first; only when every one of them was fetched and none has a score, the others (CVE-, GHSA-,
// ...). The finding's own IDs are not fetched again: osv-scanner's report has their records.
//
// A lookup that fails never fails the scan, but it is never silent either: a finding whose
// deciding tier had a failed lookup keeps what the rest of that tier gave (UNKNOWN when nothing),
// doesn't fall through to the next tier, and is counted in the warning returned, unless it is
// already CRITICAL, which no other alias can raise.
//
// The breaker (maxLookupFailures) is per image, and the run is bounded too: after it trips on
// maxTrippedImages images in a row, no image asks again.
func (o *OSV) RateFromAliases(ctx context.Context, fs []Finding) []string {
	c := &o.cache
	c.mu.Lock()
	c.failures = 0 // the breaker is per image
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.failures >= maxLookupFailures && !c.off {
			c.tripped++
			c.off = c.tripped >= maxTrippedImages
		}
	}()
	failed := 0
	var firstErr error
	for i := range fs {
		f := &fs[i]
		if f.Score > 0 {
			continue
		}
		for _, tier := range aliasTiers(*f) {
			var tierErr error
			for _, id := range tier {
				v, err := o.Vuln(ctx, id)
				if err != nil {
					if tierErr == nil {
						tierErr = err
					}
					continue
				}
				if v == nil {
					continue
				}
				if s := v.score(); s > f.Score {
					f.Score, f.ScoreFrom = s, id
				}
			}
			if tierErr != nil {
				if f.Score < 9 {
					failed++
					if firstErr == nil {
						firstErr = tierErr
					}
				}
				break
			}
			if f.Score > 0 {
				break
			}
		}
	}
	if failed == 0 {
		return nil
	}
	return []string{fmt.Sprintf("the severity of %d finding(s) may be too low (UNKNOWN, or rated from only some of their aliases) because aliases could not be looked up, so HIGH or CRITICAL ones may not be counted: %v", failed, firstErr)}
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
