package vulnsummary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitHub lists open Dependabot alerts with the GitHub REST API.
//
// The token needs the Dependabot alerts (vulnerability_alerts) repository permission, read.
type GitHub struct {
	BaseURL string // GITHUB_API_URL
	Token   string
	HTTP    *http.Client
}

var _ Client = (*GitHub)(nil)

var defaultClient = &http.Client{Timeout: 30 * time.Second}

// maxPages bounds the pages read for one listing (100 alerts each).
const maxPages = 100

type apiAlert struct {
	SecurityAdvisory struct {
		Severity string `json:"severity"`
	} `json:"security_advisory"`
	SecurityVulnerability struct {
		Severity string `json:"severity"`
	} `json:"security_vulnerability"`
}

// RepoAlerts returns the open Dependabot alerts of owner/repo, reading every page by following
// the Link header's next URL (the endpoint paginates with cursors).
func (g *GitHub) RepoAlerts(ctx context.Context, owner, repo string) ([]Alert, error) {
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/dependabot/alerts"
	base, err := url.Parse(strings.TrimRight(g.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("GITHUB_API_URL: %w", err)
	}
	next := base.String() + path + "?" + url.Values{"state": {"open"}, "per_page": {"100"}}.Encode()
	var alerts []Alert
	for page := 0; next != ""; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET %s: more than %d pages", path, maxPages)
		}
		var items []apiAlert
		link, err := g.get(ctx, next, &items)
		if err != nil {
			return nil, err
		}
		for _, a := range items {
			sev := a.SecurityAdvisory.Severity
			if sev == "" {
				sev = a.SecurityVulnerability.Severity
			}
			alerts = append(alerts, Alert{Repo: repo, Severity: sev})
		}
		if next, err = nextURL(link, base); err != nil {
			return nil, err
		}
	}
	return alerts, nil
}

// nextURL returns the rel="next" URL of a Link header, or "" if there is none. The URL must be
// on the API host, so the token is never sent anywhere else.
func nextURL(header string, base *url.URL) (string, error) {
	for part := range strings.SplitSeq(header, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(strings.ReplaceAll(params, " ", ""), `rel="next"`) {
			continue
		}
		u, err := url.Parse(strings.Trim(strings.TrimSpace(target), "<>"))
		if err != nil {
			return "", fmt.Errorf("Link header: %w", err)
		}
		if u.Scheme != base.Scheme || u.Host != base.Host {
			return "", fmt.Errorf("Link header: next page %s://%s is not on the API host %s", u.Scheme, u.Host, base.Host)
		}
		return u.String(), nil
	}
	return "", nil
}

// get decodes a 200 response's JSON body into out and returns its Link header. Errors never
// include the request headers, which carry the token.
func (g *GitHub) get(ctx context.Context, rawURL string, out any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	c := g.HTTP
	if c == nil {
		c = defaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s: %.200s", req.URL.Path, resp.Status, bytes.TrimSpace(data))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return "", fmt.Errorf("GET %s: parsing the response: %w", req.URL.Path, err)
	}
	return resp.Header.Get("Link"), nil
}
