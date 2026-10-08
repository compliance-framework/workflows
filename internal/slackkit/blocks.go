// Package slackkit builds the Block Kit messages every CCF Slack channel gets, and posts and
// edits them with the Slack Web API (docs/slack.md).
//
// A Message is a header, a body of blocks and a status-bar color. Block Kit has no color of
// its own, so a colored body goes in a secondary attachment ({color, blocks}), the one place
// Slack still draws the side bar; the header stays a top-level block so Slack never renders the
// plain-text fallback above the bar.
package slackkit

import (
	"fmt"
	"strings"
	"time"
)

// Status-bar colors.
const (
	ColorRed   = "#D1242F" // failing, held
	ColorAmber = "#BF8700" // waiting for a person, running
	ColorGreen = "#1A7F37" // resolved, handled, finished
	ColorGrey  = "#8C959F" // closed without a result
	ColorBlue  = "#0969DA" // drafts
)

// Block Kit limits (https://docs.slack.dev/reference/block-kit/blocks).
const (
	maxHeader      = 150
	maxSection     = 3000
	maxField       = 2000
	maxFields      = 10
	maxContext     = 10
	maxButtonText  = 75
	maxActionItems = 25
)

// Text is a Block Kit text object.
type Text struct {
	Type  string `json:"type"` // plain_text or mrkdwn
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"`
}

// Button is a link button: it opens URL, and the app handles no interaction.
type Button struct {
	Type     string `json:"type"` // button
	Text     Text   `json:"text"`
	URL      string `json:"url"`
	ActionID string `json:"action_id"`
	Style    string `json:"style,omitempty"`
}

// Block is a layout block: header, section, context, actions or divider.
type Block struct {
	Type     string `json:"type"`
	Text     *Text  `json:"text,omitempty"`
	Fields   []Text `json:"fields,omitempty"`
	Elements []any  `json:"elements,omitempty"` // Text for context, Button for actions
}

// Field is one label/value cell of a section's two-column fields.
type Field struct {
	Label, Value string // Value is mrkdwn
}

// Link is a link button.
type Link struct {
	Text, URL string
	Primary   bool
}

// Header is a header block: bold plain text, emoji shortcodes allowed.
func Header(s string) Block {
	return Block{Type: "header", Text: &Text{Type: "plain_text", Text: truncate(s, maxHeader), Emoji: true}}
}

// Section is a section block of mrkdwn text.
func Section(mrkdwn string) Block {
	return Block{Type: "section", Text: &Text{Type: "mrkdwn", Text: truncate(mrkdwn, maxSection)}}
}

// Fields returns sections of fields, ten per section (Slack's limit), with an optional mrkdwn
// title on the first. Fields with an empty value are left out.
func Fields(title string, fields ...Field) []Block {
	var blocks []Block
	cur := Block{Type: "section"}
	if title != "" {
		cur.Text = &Text{Type: "mrkdwn", Text: truncate(title, maxSection)}
	}
	for _, f := range fields {
		if f.Value == "" {
			continue
		}
		if len(cur.Fields) == maxFields {
			blocks, cur = append(blocks, cur), Block{Type: "section"}
		}
		v := f.Value
		if f.Label != "" {
			v = "*" + f.Label + "*\n" + v
		}
		cur.Fields = append(cur.Fields, Text{Type: "mrkdwn", Text: truncate(v, maxField)})
	}
	if cur.Text != nil || len(cur.Fields) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks
}

// Context returns a context block of small grey mrkdwn parts, or nil when every part is empty
// (Slack rejects an empty one). Empty parts are left out, and parts past Slack's ten are
// joined into the last.
func Context(parts ...string) []Block {
	b := Block{Type: "context"}
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) > maxContext {
		kept = append(kept[:maxContext-1], strings.Join(kept[maxContext-1:], " · "))
	}
	for _, p := range kept {
		b.Elements = append(b.Elements, Text{Type: "mrkdwn", Text: truncate(p, maxField)})
	}
	if len(b.Elements) == 0 {
		return nil
	}
	return []Block{b}
}

// Buttons returns actions blocks of link buttons, 25 per block. Links without a URL are left
// out; with none left it returns nil.
func Buttons(links ...Link) []Block {
	var blocks []Block
	for _, l := range links {
		if l.URL == "" {
			continue
		}
		if len(blocks) == 0 || len(blocks[len(blocks)-1].Elements) == maxActionItems {
			blocks = append(blocks, Block{Type: "actions"})
		}
		last := &blocks[len(blocks)-1]
		btn := Button{Type: "button", Text: Text{Type: "plain_text", Text: truncate(l.Text, maxButtonText), Emoji: true},
			URL: l.URL, ActionID: fmt.Sprintf("link-%d", len(last.Elements))}
		if l.Primary {
			btn.Style = "primary"
		}
		last.Elements = append(last.Elements, btn)
	}
	return blocks
}

// Divider is a divider block.
func Divider() Block { return Block{Type: "divider"} }

// Escape escapes the characters Slack's mrkdwn treats as control characters, for text from
// GitHub (PR titles, job names).
func Escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// LinkTo is a mrkdwn link; with no URL it's the escaped text alone.
func LinkTo(url, text string) string {
	if url == "" {
		return Escape(text)
	}
	return "<" + url + "|" + Escape(text) + ">"
}

// Date is a mrkdwn date Slack shows in the reader's time zone, with UTC text as the fallback.
func Date(t time.Time) string {
	return fmt.Sprintf("<!date^%d^{date_short_pretty} at {time}|%s>", t.Unix(), t.UTC().Format("2006-01-02 15:04 UTC"))
}

// Duration is d rounded down to minutes, at most two units: "3d 4h", "1h 12m", "45m", "<1m".
func Duration(d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m < 1:
		return "<1m"
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m < 24*60:
		return strings.TrimSuffix(fmt.Sprintf("%dh %dm", m/60, m%60), " 0m")
	}
	return strings.TrimSuffix(fmt.Sprintf("%dd %dh", m/(24*60), m%(24*60)/60), " 0h")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
