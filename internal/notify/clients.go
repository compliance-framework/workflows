package notify

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

var defaultClient = &http.Client{Timeout: 30 * time.Second}

// GitHub reads workflow runs from the GitHub REST API.
type GitHub struct {
	BaseURL string // e.g. https://api.github.com (GITHUB_API_URL)
	Token   string // needs actions: read
	HTTP    *http.Client
}

type workflowRun struct {
	ID         int64  `json:"id"`
	WorkflowID int64  `json:"workflow_id"`
	RunNumber  int    `json:"run_number"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// decisive lists the conclusions that say whether the branch was passing. Runs that were
// cancelled, skipped, neutral or stale say nothing, so the lookup steps over them.
var decisive = map[string]bool{
	"success":         true,
	"failure":         true,
	"timed_out":       true,
	"startup_failure": true,
}

// PreviousConclusion returns the conclusion of the most recent completed push run of the
// same workflow as runID on branch, with a lower run number and a decisive conclusion, or
// "" if there is none among the latest 50.
func (g *GitHub) PreviousConclusion(ctx context.Context, repo, runID, branch string) (string, error) {
	var current workflowRun
	if err := g.get(ctx, fmt.Sprintf("/repos/%s/actions/runs/%s", repo, url.PathEscape(runID)), &current); err != nil {
		return "", err
	}
	q := url.Values{
		"branch":                {branch},
		"event":                 {"push"},
		"status":                {"completed"},
		"exclude_pull_requests": {"true"},
		"per_page":              {"50"},
	}
	var list struct {
		WorkflowRuns []workflowRun `json:"workflow_runs"`
	}
	path := fmt.Sprintf("/repos/%s/actions/workflows/%d/runs?%s", repo, current.WorkflowID, q.Encode())
	if err := g.get(ctx, path, &list); err != nil {
		return "", err
	}
	best := workflowRun{}
	for _, run := range list.WorkflowRuns {
		if run.ID == current.ID || run.RunNumber >= current.RunNumber || !decisive[run.Conclusion] {
			continue
		}
		if run.RunNumber > best.RunNumber {
			best = run
		}
	}
	return best.Conclusion, nil
}

func (g *GitHub) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := client(g.HTTP).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, bytes.TrimSpace(body))
	}
	return json.Unmarshal(body, out)
}

// Slack posts messages with the Web API.
type Slack struct {
	BaseURL string // e.g. https://slack.com/api
	Token   string // bot token with chat:write
	HTTP    *http.Client
}

// PostMessage posts text to channel with chat.postMessage.
func (s *Slack) PostMessage(ctx context.Context, channel, text string) error {
	if channel == "" {
		return errors.New("no Slack channel")
	}
	payload, err := json.Marshal(map[string]any{
		"channel":      channel,
		"text":         text,
		"unfurl_links": false,
		"unfurl_media": false,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(s.BaseURL, "/")+"/chat.postMessage", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := client(s.HTTP).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("chat.postMessage: %s", resp.Status)
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("chat.postMessage: parsing response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("chat.postMessage: %s", result.Error)
	}
	return nil
}

func client(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return defaultClient
}
