package notify

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

var defaultClient = &http.Client{Timeout: 30 * time.Second}

// GitHub reads workflow runs from the GitHub REST API.
type GitHub struct {
	BaseURL string // GITHUB_API_URL
	Token   string // needs actions: read
	HTTP    *http.Client
}

type workflowRun struct {
	WorkflowID int64  `json:"workflow_id"`
	RunNumber  int    `json:"run_number"`
	Conclusion string `json:"conclusion"`
}

// decisive conclusions say whether the branch was passing; cancelled, skipped, neutral
// and stale runs don't, so PreviousConclusion steps over them.
var decisive = map[string]bool{"success": true, "failure": true, "timed_out": true, "startup_failure": true}

// PreviousConclusion returns the conclusion of the latest completed push run of runID's
// workflow on branch that is older than runID and decisive, or "" if the latest 50 runs
// hold none.
func (g *GitHub) PreviousConclusion(ctx context.Context, repo, runID, branch string) (string, error) {
	var current workflowRun
	if err := g.get(ctx, fmt.Sprintf("/repos/%s/actions/runs/%s", repo, url.PathEscape(runID)), &current); err != nil {
		return "", err
	}
	q := url.Values{"branch": {branch}, "event": {"push"}, "status": {"completed"}, "per_page": {"50"}}
	var list struct {
		WorkflowRuns []workflowRun `json:"workflow_runs"`
	}
	if err := g.get(ctx, fmt.Sprintf("/repos/%s/actions/workflows/%d/runs?%s", repo, current.WorkflowID, q.Encode()), &list); err != nil {
		return "", err
	}
	var best workflowRun
	for _, run := range list.WorkflowRuns {
		if run.RunNumber < current.RunNumber && run.RunNumber > best.RunNumber && decisive[run.Conclusion] {
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
	req.Header.Set("Authorization", "Bearer "+g.Token)
	return doJSON(g.HTTP, req, out)
}

// Slack posts messages with the Slack Web API.
type Slack struct {
	BaseURL string // https://slack.com/api
	Token   string // bot token with chat:write
	HTTP    *http.Client
}

// PostMessage posts text to channel with chat.postMessage.
func (s *Slack) PostMessage(ctx context.Context, channel, text string) error {
	payload, err := json.Marshal(map[string]any{"channel": channel, "text": text, "unfurl_links": false, "unfurl_media": false})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.BaseURL, "/")+"/chat.postMessage", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+s.Token)
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := doJSON(s.HTTP, req, &result); err != nil {
		return fmt.Errorf("chat.postMessage: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("chat.postMessage: %s", result.Error)
	}
	return nil
}

// doJSON sends req and decodes a 200 response's JSON body into out. Errors never include
// the request headers, which carry the token.
func doJSON(c *http.Client, req *http.Request, out any) error {
	if c == nil {
		c = defaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: %s: %.200s", req.Method, req.URL.Path, resp.Status, bytes.TrimSpace(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s %s: parsing the response: %w", req.Method, req.URL.Path, err)
	}
	return nil
}
