package attention

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

// GitHub reads PRs, check runs and files with the REST API. The token needs, on the repos read,
// Pull requests: read, Checks: read and Contents: read.
type GitHub struct {
	BaseURL string // GITHUB_API_URL
	Token   string
	HTTP    *http.Client
}

var _ Client = (*GitHub)(nil)

// maxPages bounds the pages of open PRs read per repo (100 each).
const maxPages = 10

// OpenPRs lists repo's open PRs.
func (g *GitHub) OpenPRs(ctx context.Context, owner, repo string) ([]PR, error) {
	var out []PR
	for page := 1; ; page++ {
		if page > maxPages {
			return nil, fmt.Errorf("%s/%s: more than %d pages of open PRs", owner, repo, maxPages)
		}
		var prs []struct {
			Number  int       `json:"number"`
			Title   string    `json:"title"`
			URL     string    `json:"html_url"`
			Created time.Time `json:"created_at"`
			User    struct {
				Login string `json:"login"`
			} `json:"user"`
			Head struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				SHA string `json:"sha"`
			} `json:"base"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		}
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&per_page=100&page=%d", url.PathEscape(owner), url.PathEscape(repo), page)
		if err := g.get(ctx, path, &prs); err != nil {
			return nil, err
		}
		for _, p := range prs {
			pr := PR{Repo: repo, Number: p.Number, Title: p.Title, URL: p.URL, Author: p.User.Login,
				HeadRef: p.Head.Ref, HeadSHA: p.Head.SHA, BaseSHA: p.Base.SHA, Created: p.Created}
			for _, l := range p.Labels {
				pr.Labels = append(pr.Labels, l.Name)
			}
			out = append(out, pr)
		}
		if len(prs) < 100 {
			return out, nil
		}
	}
}

// failedConclusions are the check conclusions that block a merge.
var failedConclusions = []string{"failure", "timed_out", "action_required", "startup_failure"}

// FailedCheck reports whether the latest completed run of check name on sha failed.
func (g *GitHub) FailedCheck(ctx context.Context, owner, repo, sha, name string) (bool, error) {
	var r struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	q := url.Values{"check_name": {name}, "filter": {"latest"}, "per_page": {"100"}}
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(sha), q.Encode())
	if err := g.get(ctx, path, &r); err != nil {
		return false, err
	}
	for _, c := range r.CheckRuns {
		for _, f := range failedConclusions {
			if c.Status == "completed" && c.Conclusion == f {
				return true, nil
			}
		}
	}
	return false, nil
}

// File returns path in repo at ref, or nil when it doesn't exist.
func (g *GitHub) File(ctx context.Context, owner, repo, ref, path string) ([]byte, error) {
	p := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", url.PathEscape(owner), url.PathEscape(repo), path, url.QueryEscape(ref))
	data, status, err := g.raw(ctx, p, "application/vnd.github.raw")
	if status == http.StatusNotFound {
		return nil, nil
	}
	return data, err
}

// get GETs path and decodes the JSON body into out.
func (g *GitHub) get(ctx context.Context, path string, out any) error {
	data, _, err := g.raw(ctx, path, "application/vnd.github+json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("GET %s: parsing the response: %w", strings.SplitN(path, "?", 2)[0], err)
	}
	return nil
}

// raw GETs path and returns a 200 response's body, and the status. Errors never include the
// request headers, which carry the token.
func (g *GitHub) raw(ctx context.Context, path, accept string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	c := g.HTTP
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("GET %s: %s: %.200s", req.URL.Path, resp.Status, bytes.TrimSpace(data))
	}
	return data, resp.StatusCode, nil
}
