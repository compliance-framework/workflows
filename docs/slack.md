# Slack messages: `internal/slackkit` and `slack-preview.yml`

The CCF Slack channels get [Block Kit](https://docs.slack.dev/reference/block-kit/blocks)
cards built by one package, `internal/slackkit`; the tools move to it one by one from their
plain-text messages. `slack-preview.yml` posts samples of each card to the real channels, so a
design change can be seen in Slack before the tools use it.

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

## The cards

| Card | Channel | Edited in place |
| --- | --- | --- |
| `IncidentCard` | #ccf-ci-failures | on every transition: Failing → Failing again → Resolved (resolved time, duration, failed jobs struck through), or Closed when the PR is merged or closed |
| `NeedsHumanCard` | #ccf-pr-needs-human | to Handled, with "Merged by …" or "Closed by …" and how long it waited |
| `TrainBoard` | #ccf-releases | at every train step: stages, per-repo pills and versions, "Stage n of m" |
| `DigestCard` | #ccf-release-digests | no: posted once at train end, marked draft |
| `Note` | thread replies | no |

Each tool fills its card from its state as it moves onto the kit, so each channel's design
lives in one place. On the train board each repo has a pill: :large_green_circle: released,
:large_yellow_circle: in progress, :red_circle: held, :white_circle: waiting, :fast_forward:
skipped.
`testdata/*.golden` in `internal/slackkit` holds each card's `chat.postMessage` payload, one
JSON value per line; after an intended change, rewrite them with
`go test ./internal/slackkit -update` and review the diff.

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

## `slack-preview.yml`

`workflow_dispatch` only, with input `which` (`all`, `ci-failures`, `needs-human`, `releases`,
`digests`; default `all`). It runs `go run ./cmd/slack-preview --which <which>`, which posts to
the channels in the `SLACK_CHANNEL_CI_FAILURES`, `SLACK_CHANNEL_NEEDS_HUMAN`,
`SLACK_CHANNEL_RELEASES` and `SLACK_CHANNEL_DIGESTS` variables with `SLACK_BOT_TOKEN`. Every
message says ":eyes: Preview" in its context line and fallback.

| Sample | What it shows |
| --- | --- |
| `ci-failures` | an incident card (Failing) and a failure reply in its thread; ~10s later a "passing again" reply, and the card edited to Resolved |
| `needs-human` | a Renovate major's card (Why / CI / Opened by / Waiting, Review PR); ~10s later edited to Handled |
| `releases` | a train board at stage 1, edited three times (stage 2, stage 3, finished) ~10s apart with a reply per event |
| `digests` | a draft release digest (from → to versions, highlights, changelog buttons) |

With `which: all`, a sample whose channel variable is unset is skipped with a warning; naming
that sample fails. Locally, `go run ./cmd/slack-preview --dry-run` lists the calls without
posting (no token needed); `--pause` changes the wait before each edit.
