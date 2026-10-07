package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub serves GET runs/{id} and the workflow's run list.
func fakeGitHub(t *testing.T, runs string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer gh-token" {
			t.Errorf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/repos/o/r/actions/runs/100":
			_, _ = w.Write([]byte(`{"id":100,"workflow_id":9,"run_number":10,"status":"in_progress"}`))
		case "/repos/o/r/actions/workflows/9/runs":
			q := r.URL.Query()
			if q.Get("branch") != "main" || q.Get("event") != "push" || q.Get("status") != "completed" {
				t.Errorf("unexpected query %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(runs))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPreviousConclusion(t *testing.T) {
	tests := []struct {
		name string
		runs string
		want string
	}{
		{
			name: "latest earlier run passed",
			runs: `{"workflow_runs":[
				{"id":101,"run_number":11,"conclusion":"failure"},
				{"id":99,"run_number":9,"conclusion":"success"},
				{"id":98,"run_number":8,"conclusion":"failure"}]}`,
			want: "success",
		},
		{
			name: "steps over cancelled and skipped runs",
			runs: `{"workflow_runs":[
				{"id":99,"run_number":9,"conclusion":"cancelled"},
				{"id":97,"run_number":7,"conclusion":"failure"},
				{"id":98,"run_number":8,"conclusion":"skipped"}]}`,
			want: "failure",
		},
		{
			name: "ignores the current run",
			runs: `{"workflow_runs":[{"id":100,"run_number":10,"conclusion":"failure"}]}`,
			want: "",
		},
		{
			name: "no runs",
			runs: `{"workflow_runs":[]}`,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakeGitHub(t, tt.runs)
			gh := &GitHub{BaseURL: srv.URL + "/", Token: "gh-token", HTTP: srv.Client()}
			got, err := gh.PreviousConclusion(context.Background(), "o/r", "100", "main")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("PreviousConclusion = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("API error", func(t *testing.T) {
		srv := fakeGitHub(t, "")
		gh := &GitHub{BaseURL: srv.URL, Token: "gh-token", HTTP: srv.Client()}
		if _, err := gh.PreviousConclusion(context.Background(), "o/r", "404", "main"); err == nil || !strings.Contains(err.Error(), "404") {
			t.Errorf("err = %v, want a 404 error", err)
		}
	})
}

// fakeSlack records chat.postMessage calls and answers with response.
type fakeSlack struct {
	*httptest.Server
	calls []map[string]any
}

func newFakeSlack(t *testing.T, status int, response string) *fakeSlack {
	t.Helper()
	f := &fakeSlack{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat.postMessage" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer xoxb-test" {
			t.Errorf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding body: %v", err)
		}
		f.calls = append(f.calls, body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(f.Close)
	return f
}

func TestPostMessage(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		f := newFakeSlack(t, http.StatusOK, `{"ok":true,"ts":"1.2"}`)
		s := &Slack{BaseURL: f.URL + "/api", Token: "xoxb-test", HTTP: f.Client()}
		if err := s.PostMessage(context.Background(), "C123", "hello"); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 1 || f.calls[0]["channel"] != "C123" || f.calls[0]["text"] != "hello" || f.calls[0]["unfurl_links"] != false {
			t.Errorf("calls = %v", f.calls)
		}
	})
	t.Run("Slack error", func(t *testing.T) {
		f := newFakeSlack(t, http.StatusOK, `{"ok":false,"error":"channel_not_found"}`)
		s := &Slack{BaseURL: f.URL + "/api", Token: "xoxb-test", HTTP: f.Client()}
		if err := s.PostMessage(context.Background(), "C123", "hello"); err == nil || !strings.Contains(err.Error(), "channel_not_found") {
			t.Errorf("err = %v, want channel_not_found", err)
		}
	})
	t.Run("HTTP error", func(t *testing.T) {
		f := newFakeSlack(t, http.StatusInternalServerError, `oops`)
		s := &Slack{BaseURL: f.URL + "/api", Token: "xoxb-test", HTTP: f.Client()}
		if err := s.PostMessage(context.Background(), "C123", "hello"); err == nil || !strings.Contains(err.Error(), "500") {
			t.Errorf("err = %v, want a 500 error", err)
		}
	})
	t.Run("no channel", func(t *testing.T) {
		s := &Slack{BaseURL: "http://127.0.0.1:0", Token: "xoxb-test"}
		if err := s.PostMessage(context.Background(), "", "hello"); err == nil {
			t.Error("want an error")
		}
	})
}
