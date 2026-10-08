# Slack messages: `internal/slackkit`

The CCF Slack channels get [Block Kit](https://docs.slack.dev/reference/block-kit/blocks)
cards built by one package, `internal/slackkit`; the tools move to it one by one from their
plain-text messages.

## The message shape

A `slackkit.Message` is:

- `Text`: the plain fallback. Slack shows it in notifications and to screen readers, not in
  the channel, so it repeats the gist of the card;
- `Header`: a header block (plain text, emoji shortcodes allowed);
- `Blocks`: the body, built with `Section`, `Fields` (two-column label/value cells, ten per
  section), `Context` (the small grey line), `Buttons` (link buttons) and `Divider`;
- `Color`: the status bar down the card's left side.

`Note(text, context...)` is the small card for thread replies, and `m.Preview()` marks a sample
with ":eyes: Preview" in its context line and fallback.

Block Kit has no color of its own. The side bar exists only on secondary attachments, which
Slack marks as legacy but still supports and documents with a `blocks` field
([legacy secondary attachments](https://docs.slack.dev/legacy/legacy-messaging/legacy-secondary-message-attachments)).
So a colored message is sent as:

```json
{
  "text": "CI failing: compliance-framework/mock-api#12 ci",
  "blocks": [{"type": "header", "text": {"type": "plain_text", "text": ":red_circle: CI failing · mock-api", "emoji": true}}],
  "attachments": [{"color": "#D1242F", "fallback": "...", "blocks": ["section", "fields", "actions", "context"]}]
}
```

The header stays a top-level block: when a message has no top-level blocks, Slack renders
`text` above the attachment, which would show the fallback twice. A message without `Color` has
all its blocks at the top level.

| Color | Meaning |
| --- | --- |
| red `#D1242F` | failing, or a train repo held |
| amber `#BF8700` | waiting for a person, or a train running |
| green `#1A7F37` | resolved, handled (merged), train finished, a published digest |
| grey `#8C959F` | closed without a result (PR closed, train aborted) |
| blue `#0969DA` | a draft (the release digest) |

Text rules: sentence case, no "!", GitHub refs always fully qualified (`owner/repo#N`) and linked,
text from GitHub escaped with `slackkit.Escape` (or `LinkTo`), dates with `slackkit.Date`
(`<!date^…>`, shown in the reader's time zone, UTC as the fallback). Builders truncate to
Block Kit's limits (header 150, section 3000, field 2000 characters, button 75), split fields
into sections of 10 and buttons into actions blocks of 25, and leave out empty fields, empty
context lines and buttons without a URL.

## The client

`slackkit.Client` calls the Web API with a bot token that has `chat:write`, the only scope
needed: `Post` (`chat.postMessage`), `Reply` (in a thread) and `Update` (`chat.update`; a bot
can edit its own messages with `chat:write`). `Post` returns the channel ID and `ts`; store
both, because `chat.update` takes the channel ID, not a name. `Update` always sends `blocks`
and `attachments`, as `chat.update` keeps whatever it isn't sent. Errors include Slack's
`response_metadata.messages` (which name the invalid block) and never the token.
`slackkit.Fake` records calls for tests; the `slackkit.API` interface covers both.

Link buttons only open their URL. If the Slack app has no interactivity request URL, Slack may
show a small warning icon after a click; that needs no scope, and setting any request URL in
the app's "Interactivity & Shortcuts" settings removes it.
