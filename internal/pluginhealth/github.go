package pluginhealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/compliance-framework/workflows/internal/bump"
)

// CICheck is the required check whose conclusion makes an open ccf-bump PR "failed".
const CICheck = "ci / required"

// GitHub is the REST client: read-only on the plugin and agent repos (Source), and on Repo, the
// repo holding the issue, it reads and writes issues (Tracker). Errors are *bump.StatusError.
type GitHub struct {
	BaseURL string // GITHUB_API_URL
	Token   string
	Owner   string
	Repo    string // the issue's repo, owner/name (Tracker only)
	HTTP    *http.Client
}

func (g *GitHub) bump() *bump.GitHub {
	return &bump.GitHub{BaseURL: strings.TrimRight(g.BaseURL, "/"), Token: g.Token, Owner: g.Owner, HTTP: g.HTTP}
}

func (g *GitHub) repoPath(repo string) string {
	return "/repos/" + url.PathEscape(g.Owner) + "/" + url.PathEscape(repo)
}

func notFound(err error) bool {
	var se *bump.StatusError
	return errors.As(err, &se) && se.Code == http.StatusNotFound
}

// LatestFinal returns repo's highest vX.Y.Z release, as ccf-bump's sync targets it.
func (g *GitHub) LatestFinal(ctx context.Context, repo string) (string, error) {
	return g.bump().LatestFinal(ctx, repo)
}

// LatestRelease returns repo's latest release (GitHub's "latest": not a draft or prerelease).
func (g *GitHub) LatestRelease(ctx context.Context, repo string) (*Release, error) {
	var r struct {
		Tag       string    `json:"tag_name"`
		Published time.Time `json:"published_at"`
	}
	err := g.do(ctx, http.MethodGet, g.repoPath(repo)+"/releases/latest", nil, &r)
	if notFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Release{Tag: r.Tag, Date: r.Published}, nil
}

// File returns path in repo at ref, nil when it doesn't exist.
func (g *GitHub) File(ctx context.Context, repo, ref, path string) ([]byte, error) {
	b, err := g.bump().File(ctx, repo, ref, path)
	if notFound(err) {
		return nil, nil
	}
	return b, err
}

// LatestBumpPR returns the newest of repo's 100 newest PRs whose head is a ccf-bump/ branch of
// repo itself, with Required read when it is open.
func (g *GitHub) LatestBumpPR(ctx context.Context, repo string) (*BumpPR, error) {
	var prs []struct {
		Number   int        `json:"number"`
		URL      string     `json:"html_url"`
		State    string     `json:"state"`
		MergedAt *time.Time `json:"merged_at"`
		Head     struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo *struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
	}
	q := url.Values{"state": {"all"}, "sort": {"created"}, "direction": {"desc"}, "per_page": {"100"}}
	if err := g.do(ctx, http.MethodGet, g.repoPath(repo)+"/pulls?"+q.Encode(), nil, &prs); err != nil {
		return nil, err
	}
	for _, p := range prs {
		if !strings.HasPrefix(p.Head.Ref, BumpPrefix) || p.Head.Repo == nil || p.Head.Repo.FullName != g.Owner+"/"+repo {
			continue
		}
		out := &BumpPR{Number: p.Number, URL: p.URL, State: p.State}
		if p.MergedAt != nil {
			out.Merged = *p.MergedAt
		}
		if p.State == "open" {
			runs, err := g.bump().CheckRuns(ctx, repo, p.Head.SHA, CICheck)
			if err != nil {
				return nil, err
			}
			if running, newest := bump.RequiredCheck(runs); running == nil && newest != nil {
				out.Required = newest.Conclusion
			}
		}
		return out, nil
	}
	return nil, nil
}

// Issue is the report's issue.
type Issue struct {
	Number int
	NodeID string
	URL    string
	Body   string
}

// Tracker finds and writes the report's issue, and finds the last closed train.
type Tracker interface {
	// FindIssue returns the open issue titled title that author opened, nil when there is none.
	FindIssue(ctx context.Context, title, author string) (*Issue, error)
	CreateIssue(ctx context.Context, title, body string) (*Issue, error)
	EditIssue(ctx context.Context, number int, body string) error
	PinIssue(ctx context.Context, nodeID string) error
	// LastTrain returns the number of the most recently closed issue labelled train, 0 if none.
	LastTrain(ctx context.Context) (int, error)
}

type ghIssue struct {
	Number int        `json:"number"`
	NodeID string     `json:"node_id"`
	URL    string     `json:"html_url"`
	Title  string     `json:"title"`
	Body   string     `json:"body"`
	PR     any        `json:"pull_request"`
	Closed *time.Time `json:"closed_at"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (i ghIssue) issue() *Issue {
	return &Issue{Number: i.Number, NodeID: i.NodeID, URL: i.URL, Body: i.Body}
}

func (g *GitHub) issuesPath() string { return "/repos/" + g.Repo + "/issues" }

// FindIssue returns the oldest open issue titled title opened by author, nil when there is none.
func (g *GitHub) FindIssue(ctx context.Context, title, author string) (*Issue, error) {
	var found *Issue
	for page := 1; page <= 10; page++ {
		var issues []ghIssue
		q := url.Values{"state": {"open"}, "per_page": {"100"}, "page": {fmt.Sprint(page)}}
		if err := g.do(ctx, http.MethodGet, g.issuesPath()+"?"+q.Encode(), nil, &issues); err != nil {
			return nil, err
		}
		for _, i := range issues {
			if i.PR == nil && i.Title == title && i.User.Login == author {
				found = i.issue() // newest first: keep the last, the oldest
			}
		}
		if len(issues) < 100 {
			break
		}
	}
	return found, nil
}

// CreateIssue opens the issue.
func (g *GitHub) CreateIssue(ctx context.Context, title, body string) (*Issue, error) {
	var i ghIssue
	if err := g.do(ctx, http.MethodPost, g.issuesPath(), map[string]string{"title": title, "body": body}, &i); err != nil {
		return nil, err
	}
	return i.issue(), nil
}

// EditIssue replaces the issue's body.
func (g *GitHub) EditIssue(ctx context.Context, number int, body string) error {
	return g.do(ctx, http.MethodPatch, fmt.Sprintf("%s/%d", g.issuesPath(), number), map[string]string{"body": body}, nil)
}

// PinIssue pins the issue (GraphQL pinIssue).
func (g *GitHub) PinIssue(ctx context.Context, nodeID string) error {
	in := map[string]any{
		"query":     "mutation($id: ID!) { pinIssue(input: {issueId: $id}) { issue { number } } }",
		"variables": map[string]string{"id": nodeID},
	}
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := g.do(ctx, http.MethodPost, "/graphql", in, &out); err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("pinIssue: %s", out.Errors[0].Message)
	}
	return nil
}

// LastTrain returns the most recently closed of the 30 most recently updated closed train issues.
func (g *GitHub) LastTrain(ctx context.Context) (int, error) {
	var issues []ghIssue
	q := url.Values{"state": {"closed"}, "labels": {"train"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"30"}}
	if err := g.do(ctx, http.MethodGet, g.issuesPath()+"?"+q.Encode(), nil, &issues); err != nil {
		return 0, err
	}
	last, at := 0, time.Time{}
	for _, i := range issues {
		if i.PR == nil && i.Closed != nil && i.Closed.After(at) {
			last, at = i.Number, *i.Closed
		}
	}
	return last, nil
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
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.BaseURL, "/")+path, body)
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &bump.StatusError{Method: method, Path: req.URL.Path, Code: resp.StatusCode, Body: bytes.TrimSpace(data)}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Registry reads OCI manifests anonymously (public GHCR packages need no token).
type Registry struct {
	Options []remote.Option
}

// Annotations returns the annotations of ref's top-level manifest (for a plugin, the index, where
// the agent reads the protocol); found is false when the tag doesn't exist.
func (r Registry) Annotations(ctx context.Context, ref string) (map[string]string, bool, error) {
	tag, err := name.NewTag(ref, name.StrictValidation)
	if err != nil {
		return nil, false, err
	}
	desc, err := remote.Get(tag, append([]remote.Option{remote.WithContext(ctx)}, r.Options...)...)
	var te *transport.Error
	if errors.As(err, &te) && te.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var m struct {
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(desc.Manifest, &m); err != nil {
		return nil, true, fmt.Errorf("%s: parsing the manifest: %w", ref, err)
	}
	return m.Annotations, true, nil
}

// Live is the real Source: GitHub for the repos, Registry for the images.
type Live struct {
	*GitHub
	Registry
}
