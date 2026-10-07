package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The release train threads its messages with Post (the same method as the CI-failure threads
// in the notify-threads stack, so the two merge cleanly).
func TestPostThreadReplyReturnsTS(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C9","ts":"1.2"}`))
	}))
	defer srv.Close()
	s := &Slack{BaseURL: srv.URL, Token: "xoxb-test"}
	if ch, ts, err := s.Post(context.Background(), "C9", "reply", "1.1"); err != nil || ch != "C9" || ts != "1.2" || body["thread_ts"] != "1.1" {
		t.Errorf("Post = %q, %q, %v; body %v", ch, ts, err, body)
	}
	if _, _, err := s.Post(context.Background(), "C9", "parent", ""); err != nil || body["thread_ts"] != nil {
		t.Errorf("parent: %v; body %v", err, body)
	}
}
