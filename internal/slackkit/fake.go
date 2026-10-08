package slackkit

import (
	"context"
	"fmt"
	"sync"
)

// Call is one call a Fake recorded.
type Call struct {
	Method   string // post, reply or update
	Channel  string
	TS       string // the thread for a reply, the edited message for an update
	Message  Message
	PostedTS string // the ts a post or reply returned
}

// Fake is an API that records its calls, for tests. Posts return ts "1.000001", "1.000002", ...
type Fake struct {
	mu    sync.Mutex
	Calls []Call
	Err   error // returned by every call when set
}

var _ API = (*Fake)(nil)

func (f *Fake) record(c Call) (Posted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return Posted{}, f.Err
	}
	if c.Method != "update" {
		c.PostedTS = fmt.Sprintf("1.%06d", len(f.Calls)+1)
	}
	f.Calls = append(f.Calls, c)
	return Posted{Channel: c.Channel, TS: c.PostedTS}, nil
}

// Post records a post.
func (f *Fake) Post(_ context.Context, channel string, m Message) (Posted, error) {
	return f.record(Call{Method: "post", Channel: channel, Message: m})
}

// Reply records a reply.
func (f *Fake) Reply(_ context.Context, channel, threadTS string, m Message) (Posted, error) {
	return f.record(Call{Method: "reply", Channel: channel, TS: threadTS, Message: m})
}

// Update records an update.
func (f *Fake) Update(_ context.Context, channel, ts string, m Message) error {
	_, err := f.record(Call{Method: "update", Channel: channel, TS: ts, Message: m})
	return err
}
