package train

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
)

// GitHub is the REST client behind Repos, Tracker and Members. Repo is the tracking issues'
// repo (Tracker only).
type GitHub struct {
	BaseURL string // GITHUB_API_URL
	Token   string
	Owner   string
	Repo    string
	HTTP    *http.Client
}

// StatusError is a non-2xx response.
type StatusError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s %s: %d %s: %.200s", e.Method, e.Path, e.Status, http.StatusText(e.Status), e.Body)
}

func notFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == http.StatusNotFound
}

// maxPages bounds every listing (100 per page).
const maxPages = 10

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
		return &StatusError{Method: method, Path: req.URL.Path, Status: resp.StatusCode, Body: string(bytes.TrimSpace(data))}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// pages GETs path page by page until a short page, appending to out.
func pages[T any](ctx context.Context, g *GitHub, path string, out *[]T) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; page <= maxPages; page++ {
		var items []T
		if err := g.do(ctx, http.MethodGet, fmt.Sprintf("%s%sper_page=100&page=%d", path, sep, page), nil, &items); err != nil {
			return err
		}
		*out = append(*out, items...)
		if len(items) < 100 {
			return nil
		}
	}
	return nil
}

func (g *GitHub) repoPath(repo string) string {
	return "/repos/" + url.PathEscape(g.Owner) + "/" + url.PathEscape(repo)
}

// DefaultBranch returns repo's default branch and its head commit.
func (g *GitHub) DefaultBranch(ctx context.Context, repo string) (string, string, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.do(ctx, http.MethodGet, g.repoPath(repo), nil, &r); err != nil {
		return "", "", err
	}
	var b struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	err := g.do(ctx, http.MethodGet, g.repoPath(repo)+"/branches/"+url.PathEscape(r.DefaultBranch), nil, &b)
	return r.DefaultBranch, b.Commit.SHA, err
}

// File returns path at ref, or nil when it doesn't exist.
func (g *GitHub) File(ctx context.Context, repo, ref, path string) ([]byte, error) {
	var f struct {
		Content []byte `json:"content"` // base64; encoding/json skips its newlines
	}
	err := g.do(ctx, http.MethodGet, g.repoPath(repo)+"/contents/"+path+"?ref="+url.QueryEscape(ref), nil, &f)
	if notFound(err) {
		return nil, nil
	}
	return f.Content, err
}

type ghPR struct {
	Number   int    `json:"number"`
	URL      string `json:"html_url"`
	State    string `json:"state"`
	MergedAt string `json:"merged_at"`
	MergeSHA string `json:"merge_commit_sha"`
	AutoMrg  any    `json:"auto_merge"`
	Head     struct {
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

func (p ghPR) pr() *PR {
	out := &PR{Number: p.Number, URL: p.URL, HeadSHA: p.Head.SHA, BaseSHA: p.Base.SHA, Open: p.State == "open",
		Merged: p.MergedAt != "", AutoMerge: p.AutoMrg != nil}
	if out.Merged {
		out.MergeSHA = p.MergeSHA
	}
	for _, l := range p.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	return out
}

// OpenPR returns the oldest open PR whose head branch starts with headPrefix, or nil.
func (g *GitHub) OpenPR(ctx context.Context, repo, headPrefix string) (*PR, error) {
	var prs []ghPR
	if err := pages(ctx, g, g.repoPath(repo)+"/pulls?state=open&sort=created&direction=asc", &prs); err != nil {
		return nil, err
	}
	for _, p := range prs {
		if strings.HasPrefix(p.Head.Ref, headPrefix) {
			return p.pr(), nil
		}
	}
	return nil, nil
}

// PR returns pull request number.
func (g *GitHub) PR(ctx context.Context, repo string, number int) (*PR, error) {
	var p ghPR
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d", g.repoPath(repo), number), nil, &p); err != nil {
		return nil, err
	}
	return p.pr(), nil
}

// Checks returns the check runs and commit statuses of sha.
func (g *GitHub) Checks(ctx context.Context, repo, sha string) ([]Check, error) {
	var out []Check
	for page := 1; page <= maxPages; page++ {
		var r struct {
			CheckRuns []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"check_runs"`
		}
		if err := g.do(ctx, http.MethodGet, fmt.Sprintf("%s/commits/%s/check-runs?per_page=100&page=%d", g.repoPath(repo), url.PathEscape(sha), page), nil, &r); err != nil {
			return nil, err
		}
		for _, c := range r.CheckRuns {
			out = append(out, Check{ID: c.ID, Name: c.Name, Status: c.Status, Conclusion: c.Conclusion})
		}
		if len(r.CheckRuns) < 100 {
			break
		}
	}
	var s struct {
		Statuses []struct {
			ID      int64  `json:"id"`
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if err := g.do(ctx, http.MethodGet, g.repoPath(repo)+"/commits/"+url.PathEscape(sha)+"/status?per_page=100", nil, &s); err != nil {
		return nil, err
	}
	for _, st := range s.Statuses {
		c := Check{ID: st.ID, Name: st.Context, Status: "completed", Conclusion: st.State}
		switch st.State {
		case "pending":
			c.Status, c.Conclusion = "in_progress", ""
		case "error":
			c.Conclusion = "failure"
		}
		out = append(out, c)
	}
	return out, nil
}

// Merge squash-merges the PR if its head is still sha.
func (g *GitHub) Merge(ctx context.Context, repo string, number int, sha string) error {
	in := map[string]string{"merge_method": "squash", "sha": sha}
	return g.do(ctx, http.MethodPut, fmt.Sprintf("%s/pulls/%d/merge", g.repoPath(repo), number), in, nil)
}

// Runs returns the workflow runs of event at head commit sha.
func (g *GitHub) Runs(ctx context.Context, repo, event, sha string) ([]Run, error) {
	var r struct {
		Runs []struct {
			ID         int64  `json:"id"`
			Path       string `json:"path"`
			HeadBranch string `json:"head_branch"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			URL        string `json:"html_url"`
		} `json:"workflow_runs"`
	}
	q := url.Values{"event": {event}, "head_sha": {sha}, "per_page": {"100"}}
	if err := g.do(ctx, http.MethodGet, g.repoPath(repo)+"/actions/runs?"+q.Encode(), nil, &r); err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(r.Runs))
	for _, x := range r.Runs {
		out = append(out, Run{ID: x.ID, Path: x.Path, HeadBranch: x.HeadBranch, Status: x.Status, Conclusion: x.Conclusion, URL: x.URL})
	}
	return out, nil
}

// TagsAt returns the tags that point at sha.
func (g *GitHub) TagsAt(ctx context.Context, repo, sha string) ([]string, error) {
	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := pages(ctx, g, g.repoPath(repo)+"/tags", &tags); err != nil {
		return nil, err
	}
	var out []string
	for _, t := range tags {
		if t.Commit.SHA == sha {
			out = append(out, t.Name)
		}
	}
	return out, nil
}

// Release returns the notes and page of the release of tag.
func (g *GitHub) Release(ctx context.Context, repo, tag string) (string, string, error) {
	var r struct {
		Body string `json:"body"`
		URL  string `json:"html_url"`
	}
	err := g.do(ctx, http.MethodGet, g.repoPath(repo)+"/releases/tags/"+url.PathEscape(tag), nil, &r)
	return r.Body, r.URL, err
}

// RerunFailed re-runs the failed jobs of a workflow run.
func (g *GitHub) RerunFailed(ctx context.Context, repo string, runID int64) error {
	return g.do(ctx, http.MethodPost, fmt.Sprintf("%s/actions/runs/%d/rerun-failed-jobs", g.repoPath(repo), runID), nil, nil)
}

type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"html_url"`
	State  string `json:"state"`
	PR     any    `json:"pull_request"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (i ghIssue) issue() Issue {
	out := Issue{Number: i.Number, Title: i.Title, Body: i.Body, URL: i.URL, Open: i.State == "open"}
	for _, l := range i.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	return out
}

// EnsureIssue opens an issue in repo unless an open one with the same title exists.
func (g *GitHub) EnsureIssue(ctx context.Context, repo, title, body string) (string, error) {
	var issues []ghIssue
	if err := pages(ctx, g, g.repoPath(repo)+"/issues?state=open", &issues); err != nil {
		return "", err
	}
	for _, i := range issues {
		if i.PR == nil && i.Title == title {
			return i.URL, nil
		}
	}
	var created ghIssue
	err := g.do(ctx, http.MethodPost, g.repoPath(repo)+"/issues", map[string]string{"title": title, "body": body}, &created)
	return created.URL, err
}

// Issues returns the tracker repo's issues with label, open and closed.
func (g *GitHub) Issues(ctx context.Context, label string) ([]Issue, error) {
	var issues []ghIssue
	if err := pages(ctx, g, g.repoPath(g.Repo)+"/issues?state=all&labels="+url.QueryEscape(label), &issues); err != nil {
		return nil, err
	}
	var out []Issue
	for _, i := range issues {
		if i.PR == nil {
			out = append(out, i.issue())
		}
	}
	return out, nil
}

// CreateIssue opens a tracking issue.
func (g *GitHub) CreateIssue(ctx context.Context, title, body string, labels []string) (Issue, error) {
	var i ghIssue
	err := g.do(ctx, http.MethodPost, g.repoPath(g.Repo)+"/issues", map[string]any{"title": title, "body": body, "labels": labels}, &i)
	return i.issue(), err
}

// EditIssue sets a tracking issue's body, labels and state.
func (g *GitHub) EditIssue(ctx context.Context, number int, body string, labels []string, closed bool) error {
	state := "open"
	if closed {
		state = "closed"
	}
	in := map[string]any{"body": body, "labels": labels, "state": state}
	return g.do(ctx, http.MethodPatch, fmt.Sprintf("%s/issues/%d", g.repoPath(g.Repo), number), in, nil)
}

// Comments returns a tracking issue's comments with an ID above after, oldest first.
func (g *GitHub) Comments(ctx context.Context, number int, after int64) ([]Comment, error) {
	var cs []struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := pages(ctx, g, fmt.Sprintf("%s/issues/%d/comments", g.repoPath(g.Repo), number), &cs); err != nil {
		return nil, err
	}
	var out []Comment
	for _, c := range cs {
		if c.ID > after {
			out = append(out, Comment{ID: c.ID, Author: c.User.Login, Body: c.Body})
		}
	}
	return out, nil
}

// Comment comments on a tracking issue and returns the comment's URL.
func (g *GitHub) Comment(ctx context.Context, number int, body string) (string, error) {
	var c struct {
		URL string `json:"html_url"`
	}
	err := g.do(ctx, http.MethodPost, fmt.Sprintf("%s/issues/%d/comments", g.repoPath(g.Repo), number), map[string]string{"body": body}, &c)
	return c.URL, err
}

// IsOrgAdmin reports whether user is an active owner of the org.
func (g *GitHub) IsOrgAdmin(ctx context.Context, user string) (bool, error) {
	var m struct {
		State string `json:"state"`
		Role  string `json:"role"`
	}
	err := g.do(ctx, http.MethodGet, "/orgs/"+url.PathEscape(g.Owner)+"/memberships/"+url.PathEscape(user), nil, &m)
	if notFound(err) {
		return false, nil
	}
	return err == nil && m.State == "active" && m.Role == "admin", err
}
