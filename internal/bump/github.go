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
	"slices"
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
	Title  string `json:"title"`
	User   struct {
		Login string `json:"login"`
		Type  string `json:"type"` // "Bot" for a GitHub App
	} `json:"user"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"` // nil when the fork is gone
	} `json:"head"`
	Labels    []PRLabel `json:"labels"`
	AutoMerge any       `json:"auto_merge"` // non-nil while GitHub's native auto-merge is enabled
	// Mergeable and MergeableState are only in a single PR's response (PullRequest); Mergeable is
	// nil while GitHub computes it.
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
}

// PRLabel is a label on a PR.
type PRLabel struct {
	Name string `json:"name"`
}

// HasLabel reports whether the PR carries label name.
func (p PR) HasLabel(name string) bool {
	return slices.ContainsFunc(p.Labels, func(l PRLabel) bool { return l.Name == name })
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

// DisableAutoMerge turns off GitHub's native auto-merge on the PR with GraphQL node ID id.
// ccf-bump no longer enables it (it never applies the ccf-review bypass); this clears it from PRs
// opened by older runs.
func (g *GitHub) DisableAutoMerge(ctx context.Context, id string) error {
	q := map[string]any{
		"query":     `mutation($id: ID!) { disablePullRequestAutoMerge(input: {pullRequestId: $id}) { clientMutationId } }`,
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
		return errors.New("disable auto-merge: " + out.Errors[0].Message)
	}
	return nil
}

const (
	// NeedsHumanLabel marks a bot PR waiting for a person (also the train's hold, docs/attention.md).
	NeedsHumanLabel = "needs-human"
	// AutomergeLabel marks a ccf-bump PR that `ccf-bump merge` merges once its required check passes.
	AutomergeLabel = "ccf-bump:automerge"
)

// Label is a label ccf-bump adds, created in the repo when missing.
type Label struct{ Name, Color, Description string }

var (
	NeedsHuman = Label{NeedsHumanLabel, "d93f0b", "A bot PR waiting for a person"}
	Automerge  = Label{AutomergeLabel, "0e8a16", "ccf-bump merges this once CI is green"}
)

// AddLabel adds l to the PR, creating it in repo first when it is missing.
func (g *GitHub) AddLabel(ctx context.Context, repo string, number int, l Label) error {
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/labels/%s", g.Owner, repo, url.PathEscape(l.Name)), nil, nil)
	if se := (*StatusError)(nil); errors.As(err, &se) && se.Code == http.StatusNotFound {
		in := map[string]string{"name": l.Name, "color": l.Color, "description": l.Description}
		err = g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/labels", g.Owner, repo), in, nil)
		// 422: created meanwhile (already_exists).
		if se := (*StatusError)(nil); errors.As(err, &se) && se.Code == http.StatusUnprocessableEntity {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	in := map[string][]string{"labels": {l.Name}}
	return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/labels", g.Owner, repo, number), in, nil)
}

// RemoveLabel removes label name from the PR; a label it doesn't carry is not an error.
func (g *GitHub) RemoveLabel(ctx context.Context, repo string, number int, name string) error {
	err := g.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/%s/issues/%d/labels/%s", g.Owner, repo, number, url.PathEscape(name)), nil, nil)
	if se := (*StatusError)(nil); errors.As(err, &se) && se.Code == http.StatusNotFound {
		return nil
	}
	return err
}

// PullRequest returns PR number, with its mergeable state.
func (g *GitHub) PullRequest(ctx context.Context, repo string, number int) (*PR, error) {
	var pr PR
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d", g.Owner, repo, number), nil, &pr)
	return &pr, err
}

// CheckRun is the latest run of a named check on a commit.
type CheckRun struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`     // queued, in_progress, completed
	Conclusion string `json:"conclusion"` // success, failure, ... once completed
	URL        string `json:"html_url"`
}

// LatestCheck returns the latest check run named name on commit sha, or nil when there is none.
func (g *GitHub) LatestCheck(ctx context.Context, repo, sha, name string) (*CheckRun, error) {
	var r struct {
		CheckRuns []CheckRun `json:"check_runs"`
	}
	q := url.Values{"check_name": {name}, "filter": {"latest"}, "per_page": {"100"}}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?%s", g.Owner, repo, url.PathEscape(sha), q.Encode()), nil, &r); err != nil {
		return nil, err
	}
	var latest *CheckRun
	for i, c := range r.CheckRuns {
		if latest == nil || c.ID > latest.ID {
			latest = &r.CheckRuns[i]
		}
	}
	return latest, nil
}

// Merge squash-merges the PR if its head is still sha, with title as the commit title.
func (g *GitHub) Merge(ctx context.Context, repo string, number int, sha, title string) error {
	in := map[string]string{"merge_method": "squash", "sha": sha, "commit_title": title}
	return g.do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", g.Owner, repo, number), in, nil)
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
