package slackkit

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

// Message is one Slack message.
type Message struct {
	// Text is the plain fallback for notifications and screen readers. Slack doesn't show it
	// when the message has blocks, so it repeats the gist of the header and body.
	Text string
	// Header is the header block, above the status bar; a colored message needs one, or
	// Slack would render Text above the bar.
	Header string
	Blocks []Block // the body, inside the status bar when Color is set
	Color  string  // the status bar's color; "" for none
}

// Preview marks m as a sample: ":eyes: Preview" leads its last context block (a new one at the
// end if it has none) and its fallback text.
func (m Message) Preview() Message {
	const mark = ":eyes: Preview"
	m.Text = "[Preview] " + m.Text
	m.Blocks = append([]Block(nil), m.Blocks...)
	for i := len(m.Blocks) - 1; i >= 0; i-- {
		if m.Blocks[i].Type == "context" {
			parts := []string{mark}
			for _, e := range m.Blocks[i].Elements {
				if t, ok := e.(Text); ok {
					parts = append(parts, t.Text)
				}
			}
			m.Blocks[i] = Context(parts...)[0] // re-applies the ten-element limit
			return m
		}
	}
	m.Blocks = append(m.Blocks, Context(mark)...)
	return m
}

// Note is a small card for a thread reply: one mrkdwn line and an optional context line.
func Note(mrkdwn string, context ...string) Message {
	return Message{Text: mrkdwn, Blocks: append([]Block{Section(mrkdwn)}, Context(context...)...)}
}

// payload is the message's chat.postMessage / chat.update arguments. update also sends empty
// blocks and attachments, which chat.update would otherwise keep from the old message.
func (m Message) payload(update bool) map[string]any {
	p := map[string]any{"text": m.Text}
	top := []Block{}
	if m.Header != "" {
		top = append(top, Header(m.Header))
	}
	attachments := []any{}
	if m.Color != "" && len(m.Blocks) > 0 {
		attachments = append(attachments, map[string]any{"color": m.Color, "fallback": m.Text, "blocks": m.Blocks})
	} else {
		top = append(top, m.Blocks...)
	}
	if len(top) > 0 || update {
		p["blocks"] = top
	}
	if len(attachments) > 0 || update {
		p["attachments"] = attachments
	}
	if !update {
		p["unfurl_links"], p["unfurl_media"] = false, false
	}
	return p
}

// Posted is where a message went: the channel ID (chat.update needs the ID, not a name) and
// the message's ts.
type Posted struct {
	Channel, TS string
}

// API posts and edits messages; *Client is the real one and *Fake records calls for tests.
type API interface {
	Post(ctx context.Context, channel string, m Message) (Posted, error)
	Reply(ctx context.Context, channel, threadTS string, m Message) (Posted, error)
	Update(ctx context.Context, channel, ts string, m Message) error
}

// DefaultBaseURL is the Slack Web API.
const DefaultBaseURL = "https://slack.com/api"

// Client calls the Slack Web API with a bot token that has chat:write; a bot can edit only
// its own messages.
type Client struct {
	BaseURL string // DefaultBaseURL when empty
	Token   string
	HTTP    *http.Client // a 30s-timeout client when nil
}

var defaultHTTP = &http.Client{Timeout: 30 * time.Second}

// Post posts m to channel (an ID or a name) with chat.postMessage.
func (c *Client) Post(ctx context.Context, channel string, m Message) (Posted, error) {
	return c.post(ctx, channel, "", m)
}

// Reply posts m in the thread of the message threadTS.
func (c *Client) Reply(ctx context.Context, channel, threadTS string, m Message) (Posted, error) {
	return c.post(ctx, channel, threadTS, m)
}

func (c *Client) post(ctx context.Context, channel, threadTS string, m Message) (Posted, error) {
	p := m.payload(false)
	p["channel"] = channel
	if threadTS != "" {
		p["thread_ts"] = threadTS
	}
	var out Posted
	err := c.call(ctx, "chat.postMessage", p, &out)
	return out, err
}

// Update replaces the message ts in channel (an ID) with m, with chat.update.
func (c *Client) Update(ctx context.Context, channel, ts string, m Message) error {
	p := m.payload(true)
	p["channel"], p["ts"] = channel, ts
	return c.call(ctx, "chat.update", p, nil)
}

// call posts args to method. Errors never include the request headers, which carry the token.
func (c *Client) call(ctx context.Context, method string, args map[string]any, out *Posted) error {
	body, err := marshal(args)
	if err != nil {
		return err
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s: %.200s", method, resp.Status, bytes.TrimSpace(raw))
	}
	var result struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Channel  string `json:"channel"`
		TS       string `json:"ts"`
		Metadata struct {
			Messages []string `json:"messages"`
		} `json:"response_metadata"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("%s: parsing the response: %w", method, err)
	}
	if !result.OK {
		if len(result.Metadata.Messages) > 0 {
			return fmt.Errorf("%s: %s (%s)", method, result.Error, strings.Join(result.Metadata.Messages, "; "))
		}
		return fmt.Errorf("%s: %s", method, result.Error)
	}
	if out != nil {
		*out = Posted{Channel: result.Channel, TS: result.TS}
	}
	return nil
}

// marshal is json.Marshal without HTML escaping, so payloads keep mrkdwn's <url|text> readable.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
