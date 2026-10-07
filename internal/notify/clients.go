package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var defaultClient = &http.Client{Timeout: 30 * time.Second}

// Slack posts messages with the Slack Web API.
type Slack struct {
	BaseURL string // https://slack.com/api
	Token   string // bot token with chat:write
	HTTP    *http.Client
}

// PostMessage posts text to channel with chat.postMessage.
func (s *Slack) PostMessage(ctx context.Context, channel, text string) error {
	_, _, err := s.Post(ctx, channel, text, "")
	return err
}

// Post posts text to channel with chat.postMessage, as a reply in the thread threadTS
// unless it is empty, and returns the channel ID and ts of the posted message.
func (s *Slack) Post(ctx context.Context, channel, text, threadTS string) (postedChannel, ts string, err error) {
	msg := map[string]any{"channel": channel, "text": text, "unfurl_links": false, "unfurl_media": false}
	if threadTS != "" {
		msg["thread_ts"] = threadTS
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.BaseURL, "/")+"/chat.postMessage", bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+s.Token)
	var result struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := doJSON(s.HTTP, req, &result); err != nil {
		return "", "", fmt.Errorf("chat.postMessage: %w", err)
	}
	if !result.OK {
		return "", "", fmt.Errorf("chat.postMessage: %s", result.Error)
	}
	return result.Channel, result.TS, nil
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
