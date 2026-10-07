package bump

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"golang.org/x/mod/semver"
)

// GitHub is the REST/GraphQL client ccf-bump uses: release lookups (read-only, also on repos
// outside the manifest) and, with --pr, pull requests on the bumped repo.
type GitHub struct {
	BaseURL string // GITHUB_API_URL
	Token   string
	Owner   string
	HTTP    *http.Client
}

// PR is an open pull request.
type PR struct {
	Number int    `json:"number"`
	NodeID string `json:"node_id"`
	URL    string `json:"html_url"`
}

var finalTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// LatestFinal returns the highest final release tag (vX.Y.Z, not a draft or prerelease) of repo,
// or "" when it has none.
func (g *GitHub) LatestFinal(ctx context.Context, repo string) (string, error) {
	best := ""
	for page := 1; page <= 10; page++ {
		var rels []struct {
			Tag        string `json:"tag_name"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/releases?per_page=100&page=%d", g.Owner, repo, page), nil, &rels); err != nil {
			return "", err
		}
		for _, r := range rels {
			if !r.Draft && !r.Prerelease && finalTag.MatchString(r.Tag) && (best == "" || semver.Compare(r.Tag, best) > 0) {
				best = r.Tag
			}
		}
		if len(rels) < 100 {
			break
		}
	}
	return best, nil
}

// TagTime returns the committer time of the commit tag points at.
func (g *GitHub) TagTime(ctx context.Context, repo, tag string) (time.Time, error) {
	var c struct {
		Commit struct {
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/commits/%s", g.Owner, repo, url.PathEscape(tag)), nil, &c)
	return c.Commit.Committer.Date, err
}

// File returns the content of path in repo at ref.
func (g *GitHub) File(ctx context.Context, repo, ref, path string) ([]byte, error) {
	var f struct {
		Content []byte `json:"content"` // base64, decoded by encoding/json
	}
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", g.Owner, repo, path, url.QueryEscape(ref)), nil, &f)
	return f.Content, err
}

// DefaultBranch returns repo's default branch.
func (g *GitHub) DefaultBranch(ctx context.Context, repo string) (string, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s", g.Owner, repo), nil, &r)
	return r.DefaultBranch, err
}

// OpenPR returns the open PR from branch, or nil.
func (g *GitHub) OpenPR(ctx context.Context, repo, branch string) (*PR, error) {
	var prs []PR
	q := url.Values{"head": {g.Owner + ":" + branch}, "state": {"open"}}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls?%s", g.Owner, repo, q.Encode()), nil, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

// CreatePR opens a PR from head to base.
func (g *GitHub) CreatePR(ctx context.Context, repo, base, head, title, body string) (*PR, error) {
	var pr PR
	in := map[string]string{"base": base, "head": head, "title": title, "body": body}
	err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", g.Owner, repo), in, &pr)
	return &pr, err
}

// UpdatePR sets an open PR's title and body.
func (g *GitHub) UpdatePR(ctx context.Context, repo string, number int, title, body string) error {
	in := map[string]string{"title": title, "body": body}
	return g.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/pulls/%d", g.Owner, repo, number), in, nil)
}

// EnableAutoMerge turns on squash auto-merge for the PR with GraphQL node ID id.
func (g *GitHub) EnableAutoMerge(ctx context.Context, id string) error {
	q := map[string]any{
		"query":     `mutation($id: ID!) { enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: SQUASH}) { clientMutationId } }`,
		"variables": map[string]string{"id": id},
	}
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := g.do(ctx, http.MethodPost, "/graphql", q, &out); err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		return errors.New("enable auto-merge: " + out.Errors[0].Message)
	}
	return nil
}

func (g *GitHub) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %d %s: %.200s", method, path, resp.StatusCode, http.StatusText(resp.StatusCode), data)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}
