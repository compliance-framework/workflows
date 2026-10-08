package slackkit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClient(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		paths, bodies = append(paths, r.URL.Path), append(bodies, body)
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C123","ts":"1700000000.000100"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/api", Token: "xoxb-test"}
	ctx := context.Background()
	card := Message{Text: "fallback", Header: "Title", Blocks: []Block{Section("body")}, Color: ColorRed}

	got, err := c.Post(ctx, "#ccf-ci-failures", card)
	if err != nil || got != (Posted{Channel: "C123", TS: "1700000000.000100"}) {
		t.Fatalf("Post = %+v, %v", got, err)
	}
	if _, err := c.Reply(ctx, got.Channel, got.TS, Note("reply")); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(ctx, got.Channel, got.TS, Message{Text: "plain"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/api/chat.postMessage", "/api/chat.postMessage", "/api/chat.update"}; fmt.Sprint(paths) != fmt.Sprint(want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
	post, reply, upd := bodies[0], bodies[1], bodies[2]
	if post["channel"] != "#ccf-ci-failures" || post["text"] != "fallback" || post["unfurl_links"] != false || post["thread_ts"] != nil {
		t.Errorf("post = %v", post)
	}
	if blocks := post["blocks"].([]any); len(blocks) != 1 || blocks[0].(map[string]any)["type"] != "header" {
		t.Errorf("post blocks = %v", blocks)
	}
	att := post["attachments"].([]any)[0].(map[string]any)
	if att["color"] != ColorRed || len(att["blocks"].([]any)) != 1 {
		t.Errorf("post attachment = %v", att)
	}
	if reply["thread_ts"] != "1700000000.000100" || reply["attachments"] != nil {
		t.Errorf("reply = %v", reply)
	}
	// chat.update keeps blocks and attachments it isn't sent: an update clears them explicitly.
	if upd["channel"] != "C123" || upd["ts"] != "1700000000.000100" || fmt.Sprint(upd["blocks"]) != "[]" ||
		fmt.Sprint(upd["attachments"]) != "[]" || upd["unfurl_links"] != nil {
		t.Errorf("update = %v", upd)
	}
}

func TestClientErrors(t *testing.T) {
	tests := []struct {
		name, response, wantErr string
		status                  int
	}{
		{"Slack error", `{"ok":false,"error":"invalid_blocks","response_metadata":{"messages":["[ERROR] bad header"]}}`, "chat.postMessage: invalid_blocks ([ERROR] bad header)", http.StatusOK},
		{"HTTP error", `oops`, "500", http.StatusInternalServerError},
		{"not JSON", `oops`, "parsing", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer srv.Close()
			_, err := (&Client{BaseURL: srv.URL, Token: "xoxb-test"}).Post(context.Background(), "C1", Note("x"))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || strings.Contains(err.Error(), "xoxb-test") {
				t.Errorf("err = %v, want %q and no token", err, tt.wantErr)
			}
		})
	}
}

func TestBuilders(t *testing.T) {
	var many []Field
	for i := range 12 {
		many = append(many, Field{Label: fmt.Sprint(i), Value: "v"})
	}
	many = append(many, Field{Label: "empty"})
	if fs := Fields("t", many...); len(fs) != 2 || len(fs[0].Fields) != 10 || len(fs[1].Fields) != 2 || fs[0].Text.Text != "t" || fs[1].Text != nil {
		t.Errorf("Fields = %+v", fs)
	}
	if fs := Fields(""); fs != nil {
		t.Errorf("Fields with nothing = %+v", fs)
	}
	var links []Link
	for i := range 27 {
		links = append(links, Link{Text: fmt.Sprint(i), URL: "https://x/" + fmt.Sprint(i)})
	}
	links = append(links, Link{Text: "no url"})
	if bs := Buttons(links...); len(bs) != 2 || len(bs[0].Elements) != 25 || len(bs[1].Elements) != 2 || bs[1].Elements[1].(Button).ActionID != "link-1" {
		t.Errorf("Buttons = %+v", bs)
	}
	if bs := Buttons(Link{Text: "none"}); bs != nil {
		t.Errorf("Buttons without URLs = %+v", bs)
	}
	var parts []string
	for i := range 12 {
		parts = append(parts, fmt.Sprint(i))
	}
	if c := Context(append(parts, "")...); len(c) != 1 || len(c[0].Elements) != 10 || c[0].Elements[9].(Text).Text != "9 · 10 · 11" {
		t.Errorf("Context = %+v", c)
	}
	if c := Context("", ""); c != nil {
		t.Errorf("Context with only empty parts = %+v", c)
	}
	if h := Header(strings.Repeat("é", 200)); len([]rune(h.Text.Text)) != 150 || !strings.HasSuffix(h.Text.Text, "…") {
		t.Errorf("Header not truncated to 150 runes: %d", len([]rune(h.Text.Text)))
	}
	if got := LinkTo("https://x", "a <b> & c"); got != "<https://x|a &lt;b&gt; &amp; c>" {
		t.Errorf("LinkTo = %q", got)
	}
	if got := LinkTo("", "<t>"); got != "&lt;t&gt;" {
		t.Errorf("LinkTo without URL = %q", got)
	}
	for d, want := range map[time.Duration]string{
		30 * time.Second: "<1m", 45 * time.Minute: "45m", time.Hour: "1h", 72 * time.Minute: "1h 12m",
		24 * time.Hour: "1d", 76*time.Hour + 5*time.Minute: "3d 4h",
	} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
	if got := Date(time.Date(2026, 10, 8, 9, 5, 0, 0, time.UTC)); got != "<!date^1791450300^{date_short_pretty} at {time}|2026-10-08 09:05 UTC>" {
		t.Errorf("Date = %q", got)
	}
}

func TestFake(t *testing.T) {
	f := &Fake{}
	ctx := context.Background()
	p, _ := f.Post(ctx, "C1", Note("a"))
	r, _ := f.Reply(ctx, "C1", p.TS, Note("b"))
	_ = f.Update(ctx, "C1", p.TS, Note("c"))
	if p.TS != "1.000001" || r.TS != "1.000002" || len(f.Calls) != 3 || f.Calls[2].Method != "update" || f.Calls[2].TS != p.TS {
		t.Errorf("calls = %+v", f.Calls)
	}
}

func TestPreview(t *testing.T) {
	orig := Message{Text: "t", Blocks: append([]Block{Section("body")}, Context("updated")...)}
	p := orig.Preview()
	last := p.Blocks[len(p.Blocks)-1]
	if p.Text != "[Preview] t" || len(p.Blocks) != 2 || len(last.Elements) != 2 || last.Elements[0].(Text).Text != ":eyes: Preview" {
		t.Errorf("preview = %+v", p)
	}
	if orig.Text != "t" || len(orig.Blocks[1].Elements) != 1 {
		t.Errorf("Preview changed the original: %+v", orig)
	}
	var ten []string
	for i := range 10 {
		ten = append(ten, fmt.Sprint(i))
	}
	full := Message{Blocks: Context(ten...)}.Preview()
	if n := len(full.Blocks[0].Elements); n != 10 {
		t.Errorf("preview of a full context line has %d elements, want 10", n)
	}
	bare := Message{Text: "t", Blocks: []Block{Section("body")}}.Preview()
	if len(bare.Blocks) != 2 || bare.Blocks[1].Type != "context" || bare.Blocks[1].Elements[0].(Text).Text != ":eyes: Preview" {
		t.Errorf("preview of a message without context = %+v", bare)
	}
}
