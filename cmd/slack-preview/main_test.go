package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/compliance-framework/workflows/internal/slackkit"
)

var allChannels = map[string]string{
	"SLACK_BOT_TOKEN":           "xoxb-test",
	"SLACK_CHANNEL_CI_FAILURES": "CCI",
	"SLACK_CHANNEL_NEEDS_HUMAN": "CNH",
	"SLACK_CHANNEL_RELEASES":    "CREL",
	"SLACK_CHANNEL_DIGESTS":     "CDIG",
}

func preview(t *testing.T, env map[string]string, args ...string) (*slackkit.Fake, []time.Duration, string, error) {
	t.Helper()
	fake := &slackkit.Fake{}
	var pauses []time.Duration
	var out bytes.Buffer
	now := func() time.Time { return time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC) }
	err := run(context.Background(), args, func(k string) string { return env[k] }, &out,
		func(func(string) string) slackkit.API { return fake }, func(d time.Duration) { pauses = append(pauses, d) }, now)
	return fake, pauses, out.String(), err
}

// calls summarises the calls as "method channel ts header-or-text".
func calls(f *slackkit.Fake) []string {
	var got []string
	for _, c := range f.Calls {
		what := c.Message.Header
		if what == "" {
			what = c.Message.Text
		}
		got = append(got, fmt.Sprintf("%s %s %s %s", c.Method, c.Channel, c.TS, what))
	}
	return got
}

func TestPreviewAll(t *testing.T) {
	fake, pauses, out, err := preview(t, allChannels)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"post CCI  :red_circle: CI failing · mock-api",
		"reply CCI 1.000001 [Preview] :x: Failed: `ci / go / test`, `ci / required` at `2222222`",
		"reply CCI 1.000001 [Preview] :white_check_mark: Passing again at `4444444`",
		"update CCI 1.000001 :large_green_circle: CI resolved · mock-api",
		"post CNH  :raising_hand: Needs a human · mock-agent",
		"update CNH 1.000005 :white_check_mark: Handled · mock-agent",
		"post CREL  :steam_locomotive: Release train 2026-10",
		"update CREL 1.000007 :steam_locomotive: Release train 2026-10",
		"reply CREL 1.000007 [Preview] :large_green_circle: `mock-api` released `v1.4.0`; stage 2 started",
		"update CREL 1.000007 :steam_locomotive: Release train 2026-10",
		"reply CREL 1.000007 [Preview] :large_green_circle: `mock-agent` and `mock-ui` released; stage 3 started",
		"update CREL 1.000007 :steam_locomotive: Release train 2026-10",
		"reply CREL 1.000007 [Preview] :checkered_flag: Train finished: 6 repos released",
		"post CDIG  :memo: Release digest 2026-10 (draft)",
	}
	if got := calls(fake); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(pauses) != 5 || pauses[0] != 10*time.Second {
		t.Errorf("pauses = %v, want 5 of 10s", pauses)
	}
	for _, c := range fake.Calls {
		if !strings.HasPrefix(c.Message.Text, "[Preview] ") {
			t.Errorf("%s %s isn't marked as a preview: %q", c.Method, c.Channel, c.Message.Text)
		}
	}
	colors := map[string]string{}
	for _, c := range fake.Calls {
		colors[c.Method+" "+c.Channel] = c.Message.Color
	}
	if colors["post CCI"] != slackkit.ColorRed || colors["update CCI"] != slackkit.ColorGreen || colors["update CREL"] != slackkit.ColorGreen ||
		colors["post CREL"] != slackkit.ColorAmber || colors["post CDIG"] != slackkit.ColorBlue {
		t.Errorf("colors = %v", colors)
	}
	if strings.Count(out, "posted the ") != 4 {
		t.Errorf("output:\n%s", out)
	}
}

func TestPreviewWhich(t *testing.T) {
	fake, pauses, _, err := preview(t, allChannels, "--which", "needs-human", "--pause", "1s")
	if err != nil || len(fake.Calls) != 2 || fake.Calls[0].Channel != "CNH" || len(pauses) != 1 || pauses[0] != time.Second {
		t.Errorf("err %v, calls %v, pauses %v", err, calls(fake), pauses)
	}

	env := map[string]string{"SLACK_BOT_TOKEN": "x", "SLACK_CHANNEL_DIGESTS": "CDIG"}
	fake, _, out, err := preview(t, env)
	if err != nil || len(fake.Calls) != 1 || strings.Count(out, "::warning::") != 3 {
		t.Errorf("all with one channel: err %v, calls %v, output:\n%s", err, calls(fake), out)
	}
	if _, _, _, err := preview(t, env, "--which", "releases"); err == nil || !strings.Contains(err.Error(), "SLACK_CHANNEL_RELEASES is not set") {
		t.Errorf("a named sample without its channel: err = %v", err)
	}
	if _, _, _, err := preview(t, map[string]string{"SLACK_BOT_TOKEN": "x"}); err == nil || !strings.Contains(err.Error(), "no sample channel") {
		t.Errorf("no channels: err = %v", err)
	}
	if _, _, _, err := preview(t, map[string]string{"SLACK_CHANNEL_DIGESTS": "C"}); err == nil || !strings.Contains(err.Error(), "SLACK_BOT_TOKEN") {
		t.Errorf("no token: err = %v", err)
	}
	if _, _, _, err := preview(t, allChannels, "--which", "train"); err == nil || !strings.Contains(err.Error(), "want one of all, ci-failures") {
		t.Errorf("unknown sample: err = %v", err)
	}
	if _, _, _, err := preview(t, allChannels, "extra"); err == nil {
		t.Error("extra arguments are accepted")
	}
	failing := &slackkit.Fake{Err: fmt.Errorf("channel_not_found")}
	err = run(context.Background(), []string{"--which", "digests"}, func(k string) string { return allChannels[k] }, &bytes.Buffer{},
		func(func(string) string) slackkit.API { return failing }, func(time.Duration) {}, time.Now)
	if err == nil || err.Error() != "digests: channel_not_found" {
		t.Errorf("a Slack error: err = %v", err)
	}
}

func TestPreviewDryRun(t *testing.T) {
	env := map[string]string{"SLACK_CHANNEL_DIGESTS": "CDIG"} // no token needed
	var out bytes.Buffer
	err := run(context.Background(), []string{"--dry-run", "--which", "digests"}, func(k string) string { return env[k] }, &out,
		func(func(string) string) slackkit.API { t.Error("a dry run uses the real client"); return nil },
		func(time.Duration) {}, time.Now)
	if err != nil || !strings.Contains(out.String(), "post CDIG : [Preview] Release digest") {
		t.Errorf("err %v, output:\n%s", err, out.String())
	}
}
