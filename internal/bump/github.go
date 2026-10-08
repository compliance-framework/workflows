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
	User   struct {
		Login string `json:"login"`
		Type  string `json:"type"` // "Bot" for a GitHub App
	} `json:"user"`
	Head struct {
		Ref  string `json:"ref"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"` // nil when the fork is gone
	} `json:"head"`
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

// TagCommit returns the SHA of the commit tag points at (an annotated tag is peeled to its commit).
func (g *GitHub) TagCommit(ctx context.Context, repo, tag string) (string, error) {
	var c struct {
		SHA string `json:"sha"`
	}
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/commits/%s", g.Owner, repo, url.PathEscape(tag)), nil, &c)
	if err == nil && !fullSHA.MatchString(c.SHA) {
		err = fmt.Errorf("commit of %s %s: unexpected sha %q", repo, tag, c.SHA)
	}
	return c.SHA, err
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

// OpenPRs lists repo's open PRs.
func (g *GitHub) OpenPRs(ctx context.Context, repo string) ([]PR, error) {
	var all []PR
	for page := 1; page <= 10; page++ {
		var prs []PR
		if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls?state=open&per_page=100&page=%d", g.Owner, repo, page), nil, &prs); err != nil {
			return nil, err
		}
		all = append(all, prs...)
		if len(prs) < 100 {
			break
		}
	}
	return all, nil
}

// ClosePR comments on the PR, then closes it.
func (g *GitHub) ClosePR(ctx context.Context, repo string, number int, comment string) error {
	if err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/comments", g.Owner, repo, number), map[string]string{"body": comment}, nil); err != nil {
		return err
	}
	return g.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/pulls/%d", g.Owner, repo, number), map[string]string{"state": "closed"}, nil)
}

// DeleteBranch deletes branch from repo.
func (g *GitHub) DeleteBranch(ctx context.Context, repo, branch string) error {
	return g.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", g.Owner, repo, branch), nil, nil)
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

// NeedsHumanLabel marks a bot PR waiting for a person (also the train's hold, docs/attention.md).
const (
	NeedsHumanLabel       = "needs-human"
	needsHumanColor       = "d93f0b"
	needsHumanDescription = "A bot PR waiting for a person"
)

// LabelNeedsHuman adds the needs-human label to the PR, creating the label in repo first when
// it is missing.
func (g *GitHub) LabelNeedsHuman(ctx context.Context, repo string, number int) error {
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/labels/%s", g.Owner, repo, url.PathEscape(NeedsHumanLabel)), nil, nil)
	if se := (*StatusError)(nil); errors.As(err, &se) && se.Code == http.StatusNotFound {
		in := map[string]string{"name": NeedsHumanLabel, "color": needsHumanColor, "description": needsHumanDescription}
		err = g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/labels", g.Owner, repo), in, nil)
		// 422: created meanwhile (already_exists).
		if se := (*StatusError)(nil); errors.As(err, &se) && se.Code == http.StatusUnprocessableEntity {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	in := map[string][]string{"labels": {NeedsHumanLabel}}
	return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/labels", g.Owner, repo, number), in, nil)
}

// StatusError is a non-2xx API response.
type StatusError struct {
	Method, Path string
	Code         int
	Body         []byte
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s %s: %d %s: %.200s", e.Method, e.Path, e.Code, http.StatusText(e.Code), e.Body)
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
		return &StatusError{Method: method, Path: path, Code: resp.StatusCode, Body: data}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}
