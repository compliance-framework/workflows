package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreviousConclusion(t *testing.T) {
	tests := []struct{ name, runs, want string }{
		{"latest older decisive run", `[{"run_number":11,"conclusion":"failure"},{"run_number":9,"conclusion":"success"},{"run_number":8,"conclusion":"failure"}]`, "success"},
		{"steps over cancelled and skipped", `[{"run_number":9,"conclusion":"cancelled"},{"run_number":7,"conclusion":"failure"},{"run_number":8,"conclusion":"skipped"}]`, "failure"},
		{"ignores the current run", `[{"run_number":10,"conclusion":"failure"}]`, ""},
		{"no runs", `[]`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer gh-token" {
					t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
				}
				switch r.URL.Path {
				case "/repos/o/r/actions/runs/100":
					_, _ = w.Write([]byte(`{"id":100,"workflow_id":9,"run_number":10}`))
				case "/repos/o/r/actions/workflows/9/runs":
					if q := r.URL.Query(); q.Get("branch") != "main" || q.Get("event") != "push" || q.Get("status") != "completed" {
						t.Errorf("query = %s", r.URL.RawQuery)
					}
					_, _ = w.Write([]byte(`{"workflow_runs":` + tt.runs + `}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			gh := &GitHub{BaseURL: srv.URL + "/", Token: "gh-token"}
			got, err := gh.PreviousConclusion(context.Background(), "o/r", "100", "main")
			if err != nil || got != tt.want {
				t.Errorf("PreviousConclusion = %q, %v; want %q", got, err, tt.want)
			}
			if _, err := gh.PreviousConclusion(context.Background(), "o/r", "404", "main"); err == nil || !strings.Contains(err.Error(), "404") {
				t.Errorf("err = %v, want a 404 error", err)
			}
		})
	}
}

func TestPostMessage(t *testing.T) {
	tests := []struct {
		name, response, wantErr string
		status                  int
	}{
		{"ok", `{"ok":true}`, "", http.StatusOK},
		{"Slack error", `{"ok":false,"error":"channel_not_found"}`, "channel_not_found", http.StatusOK},
		{"HTTP error", `oops`, "500", http.StatusInternalServerError},
		{"not JSON", `oops`, "parsing", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/chat.postMessage" || r.Header.Get("Authorization") != "Bearer xoxb-test" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				calls = append(calls, body)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer srv.Close()
			err := (&Slack{BaseURL: srv.URL + "/api", Token: "xoxb-test"}).PostMessage(context.Background(), "C123", "hello")
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "xoxb-test") {
				t.Error("the error leaks the token")
			}
			if len(calls) != 1 || calls[0]["channel"] != "C123" || calls[0]["text"] != "hello" || calls[0]["unfurl_links"] != false {
				t.Errorf("calls = %v", calls)
			}
		})
	}
}

func TestPostInThread(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C123","ts":"1700000000.000200"}`))
	}))
	defer srv.Close()
	slack := &Slack{BaseURL: srv.URL, Token: "xoxb-test"}
	channel, ts, err := slack.Post(context.Background(), "C123", "reply", "1700000000.000100")
	if err != nil || channel != "C123" || ts != "1700000000.000200" {
		t.Errorf("Post = %q, %q, %v", channel, ts, err)
	}
	if body["thread_ts"] != "1700000000.000100" || body["text"] != "reply" {
		t.Errorf("body = %v", body)
	}
	if _, _, err := slack.Post(context.Background(), "C123", "top", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["thread_ts"]; ok {
		t.Errorf("a top-level message has a thread_ts: %v", body)
	}
}
