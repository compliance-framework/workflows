package reposettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// GitHub reads and writes repo settings with the GitHub REST API. Reads need Administration read;
// writes need Administration write.
type GitHub struct {
	BaseURL string // GITHUB_API_URL
	Token   string
	HTTP    *http.Client
}

var _ Client = (*GitHub)(nil)

var defaultClient = &http.Client{Timeout: 30 * time.Second}

const perPage = 100

// statusError is a response with an unexpected status.
type statusError struct {
	method, path string
	code         int
	body         string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s %s: %d %s: %.200s", e.method, e.path, e.code, http.StatusText(e.code), e.body)
}

func isNotFound(err error) bool { return hasStatus(err, http.StatusNotFound) }

func isForbidden(err error) bool { return hasStatus(err, http.StatusForbidden) }

func hasStatus(err error, code int) bool {
	var se *statusError
	return errors.As(err, &se) && se.code == code
}

// InstallationRepos lists the repos an installation token can reach.
func (g *GitHub) InstallationRepos(ctx context.Context) ([]string, error) {
	var list struct {
		TotalCount   int `json:"total_count"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/installation/repositories?per_page=%d", perPage), nil, &list); err != nil {
		return nil, err
	}
	if list.TotalCount > len(list.Repositories) {
		return nil, fmt.Errorf("the token reaches %d repos, more than one page", list.TotalCount)
	}
	names := make([]string, len(list.Repositories))
	for i, r := range list.Repositories {
		names[i] = r.FullName
	}
	return names, nil
}

// MergeSettings reads the merge settings; with Administration read only, GitHub leaves them out
// and the fields stay nil (unknown).
func (g *GitHub) MergeSettings(ctx context.Context, repo string) (CurrentMergeSettings, error) {
	var s CurrentMergeSettings
	err := g.do(ctx, http.MethodGet, repoPath(repo, ""), nil, &s)
	return s, err
}

func (g *GitHub) UpdateMergeSettings(ctx context.Context, repo string, s MergeSettings) error {
	return g.do(ctx, http.MethodPatch, repoPath(repo, ""), s, nil)
}

// VulnerabilityAlerts reports whether Dependabot alerts are on: 204 means on, 404 off.
func (g *GitHub) VulnerabilityAlerts(ctx context.Context, repo string) (bool, error) {
	err := g.do(ctx, http.MethodGet, repoPath(repo, "/vulnerability-alerts"), nil, nil)
	if isNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func (g *GitHub) SetVulnerabilityAlerts(ctx context.Context, repo string, enabled bool) error {
	return g.do(ctx, onOff(enabled), repoPath(repo, "/vulnerability-alerts"), nil, nil)
}

// SecurityUpdates reports whether Dependabot security updates (automated security fixes) are on,
// from GET /repos/{owner}/{repo}/automated-security-fixes. A 404 means off (GitHub returns it when
// Dependabot is off for the repo). nil is unknown: a 403, or a response without "enabled".
func (g *GitHub) SecurityUpdates(ctx context.Context, repo string) (*bool, error) {
	var s struct {
		Enabled *bool `json:"enabled"`
	}
	err := g.do(ctx, http.MethodGet, repoPath(repo, "/automated-security-fixes"), nil, &s)
	switch {
	case isNotFound(err):
		off := false
		return &off, nil
	case isForbidden(err):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return s.Enabled, nil
}

// SecurityConfiguration returns the org code security configuration attached to the repo, from
// GET /repos/{owner}/{repo}/code-security-configuration. nil means none is attached, or the token
// can't read it (404, 403 or an empty response).
func (g *GitHub) SecurityConfiguration(ctx context.Context, repo string) (*SecurityConfiguration, error) {
	var s struct {
		Status        string `json:"status"`
		Configuration *struct {
			Name                      string `json:"name"`
			Enforcement               string `json:"enforcement"`
			DependabotAlerts          string `json:"dependabot_alerts"`
			DependabotSecurityUpdates string `json:"dependabot_security_updates"`
		} `json:"configuration"`
	}
	err := g.do(ctx, http.MethodGet, repoPath(repo, "/code-security-configuration"), nil, &s)
	switch {
	case isNotFound(err) || isForbidden(err):
		return nil, nil
	case err != nil:
		return nil, err
	case s.Configuration == nil || slices.Contains(detachedStatuses, s.Status):
		return nil, nil
	}
	c := s.Configuration
	return &SecurityConfiguration{
		Name:                      c.Name,
		Enforced:                  c.Enforcement == "enforced" || s.Status == "enforced",
		DependabotAlerts:          c.DependabotAlerts,
		DependabotSecurityUpdates: c.DependabotSecurityUpdates,
	}, nil
}

// detachedStatuses are attachment statuses of a configuration that no longer applies to the repo.
var detachedStatuses = []string{"detached", "removed", "removed_by_enterprise"}

func (g *GitHub) SetSecurityUpdates(ctx context.Context, repo string, enabled bool) error {
	return g.do(ctx, onOff(enabled), repoPath(repo, "/automated-security-fixes"), nil, nil)
}

// Rulesets returns the repo's own rulesets, each read in full.
func (g *GitHub) Rulesets(ctx context.Context, repo string) (map[int64]Ruleset, error) {
	var list []struct {
		ID         int64  `json:"id"`
		SourceType string `json:"source_type"`
	}
	if err := g.do(ctx, http.MethodGet, repoPath(repo, fmt.Sprintf("/rulesets?includes_parents=false&per_page=%d", perPage)), nil, &list); err != nil {
		return nil, err
	}
	if len(list) >= perPage {
		return nil, fmt.Errorf("%s has %d or more rulesets, more than one page", repo, perPage)
	}
	out := map[int64]Ruleset{}
	for _, item := range list {
		if item.SourceType != "" && item.SourceType != "Repository" {
			continue
		}
		var r Ruleset
		if err := g.do(ctx, http.MethodGet, repoPath(repo, fmt.Sprintf("/rulesets/%d", item.ID)), nil, &r); err != nil {
			return nil, err
		}
		out[item.ID] = r
	}
	return out, nil
}

func (g *GitHub) CreateRuleset(ctx context.Context, repo string, r Ruleset) error {
	return g.do(ctx, http.MethodPost, repoPath(repo, "/rulesets"), r, nil)
}

func (g *GitHub) UpdateRuleset(ctx context.Context, repo string, id int64, r Ruleset) error {
	return g.do(ctx, http.MethodPut, repoPath(repo, fmt.Sprintf("/rulesets/%d", id)), r, nil)
}

func onOff(enabled bool) string {
	if enabled {
		return http.MethodPut
	}
	return http.MethodDelete
}

// repoPath is /repos/{owner}/{name}{suffix}, with owner and name escaped.
func repoPath(repo, suffix string) string {
	owner, name, _ := strings.Cut(repo, "/")
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + suffix
}

// do sends a request with in as the JSON body and decodes a 2xx response's body into out. Errors
// never include the request headers, which carry the token.
func (g *GitHub) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := g.HTTP
	if c == nil {
		c = defaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &statusError{method: method, path: req.URL.Path, code: resp.StatusCode, body: string(bytes.TrimSpace(data))}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: parsing the response: %w", method, req.URL.Path, err)
	}
	return nil
}
