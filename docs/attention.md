# Bot PRs that need a human

Most bot PRs merge on their own: Renovate merges its non-major group, ccf-bump turns on auto-merge,
and the train merges its bump and release PRs. The rest wait for a person: a major update, an OPA
bump in the api, a ccf-bump PR whose auto-merge is off. Without a nudge they sit there until the
next train trips over them (Renovate majors on `mock-ui`, #13 and #14, waited for weeks).

The team works in Slack, so these PRs are surfaced there, not through GitHub review requests.
Every bot PR that needs a human carries one label, **`needs-human`** (the same name as the train's
`needs-human` status, see [train.md](train.md)), color `d93f0b`, "A bot PR waiting for a person".

## Who adds the label

| Bot | PRs | How |
| --- | --- | --- |
| Renovate (`renovate/default.json`) | Majors, and OPA in the api (its own `renovate/opa` PR, never auto-merged) | The rules add `"addLabels": ["needs-human"]`, merged with any other labels. Renovate sets labels when it opens the PR; GitHub creates the label if the repo lacks it. |
| ccf-bump | A PR whose auto-merge is off: a major update or a pin that was not a version, or a PR on which GitHub refused auto-merge | After opening or updating the PR, ccf-bump adds the label, creating it with the color and description above when the repo lacks it. A failure is a warning, as for auto-merge: the PR is open either way. `--dry-run` prints `would add label needs-human`. |

Both run as `ccf-release-bot`; adding and creating a label needs **Pull requests: write** (or
Issues: write), which their live tokens already have.

## Immediate post

[`notify-failure.yml`](notify.md#needs-a-human), which runs after every CI run of every repo,
posts a tracked bot PR to the `SLACK_CHANNEL_NEEDS_HUMAN` channel, once per PR, when:

- it carries `needs-human` (the run for the `labeled` event is the first to see it), with the
  reason from its branch: a major update, the api's OPA update, ccf-bump's auto-merge off;
- it is a release-please PR whose `release-checks` job failed: it needs `release:major-approved`
  from a maintainer, or an internal dependency isn't final yet. This goes to the needs-human
  channel instead of a CI incident in `SLACK_CHANNEL_CI_FAILURES` (an incident still opens when
  other jobs failed too). `release-checks` itself stays read-only: it never labels or comments.

```text
:raising_hand: mock-ui#13 chore(deps): update typescript to v7 — needs a human: major update
```

Human PRs are never posted.

## Setting it up

1. Create the Slack channel `#ccf-pr-needs-human` and invite the Slack bot (the app behind
   `SLACK_BOT_TOKEN`) to it.
2. Set the org variable `SLACK_CHANNEL_NEEDS_HUMAN` to the channel's ID, visible to the public
   repos like the other channel variables. Until it is set, nothing is posted and nothing fails.
