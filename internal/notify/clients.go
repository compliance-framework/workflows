package notify

import (
	"context"

	"github.com/compliance-framework/workflows/internal/slackkit"
)

// Slack posts plain-text messages, for the tools not yet on slackkit's cards; it uses
// slackkit's client.
type Slack struct {
	BaseURL string // https://slack.com/api
	Token   string // bot token with chat:write
}

// PostMessage posts text to channel with chat.postMessage.
func (s *Slack) PostMessage(ctx context.Context, channel, text string) error {
	_, _, err := s.Post(ctx, channel, text, "")
	return err
}

// Post posts text to channel with chat.postMessage, as a reply in the thread threadTS
// unless it is empty, and returns the channel ID and ts of the posted message.
func (s *Slack) Post(ctx context.Context, channel, text, threadTS string) (postedChannel, ts string, err error) {
	c := s.Client()
	m := slackkit.Message{Text: text}
	var p slackkit.Posted
	if threadTS == "" {
		p, err = c.Post(ctx, channel, m)
	} else {
		p, err = c.Reply(ctx, channel, threadTS, m)
	}
	return p.Channel, p.TS, err
}

// Client is the slackkit client for the same API and token.
func (s *Slack) Client() *slackkit.Client {
	return &slackkit.Client{BaseURL: s.BaseURL, Token: s.Token}
}
