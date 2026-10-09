# Bot PRs that need a human

Most bot PRs merge on their own: Renovate merges its non-major group, `ccf-bump merge` merges the
ccf-bump PRs labelled `ccf-bump:automerge`, and the train merges its bump and release PRs. The rest
wait for a person: a major update, an OPA bump in the api, a ccf-bump PR it doesn't merge. Without a nudge they sit there until the
next train trips over them (Renovate majors on `mock-ui`, #13 and #14, waited for weeks).

The team works in Slack, so these PRs are surfaced there, not through GitHub review requests.
Every bot PR that needs a human carries one label, **`needs-human`** (the same name as the train's
`needs-human` status, see [train.md](train.md)), color `d93f0b`, "A bot PR waiting for a person".

## Who adds the label

| Bot | PRs | How |
| --- | --- | --- |
| Renovate (`renovate/default.json`) | Majors, and OPA in the api (its own `renovate/opa` PR, never auto-merged) | The rules add `"addLabels": ["needs-human"]`, merged with any other labels. Renovate sets labels when it opens the PR; GitHub creates the label if the repo lacks it. |
| ccf-bump | A PR it doesn't merge: a major update, a pin that was not a version, or in a helm repo an app update of a minor or more; or a PR it failed to label `ccf-bump:automerge` | After opening or updating the PR, ccf-bump adds the label, creating it with the color and description above when the repo lacks it. A failure is a warning: the PR is open either way. `--dry-run` prints `would add label needs-human`. ccf-bump's other label, `ccf-bump:automerge`, is not a human signal: it marks the PRs `ccf-bump merge` merges ([ccf-bump.md](ccf-bump.md#the-merge-pass)). |

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

The post is a card: "Needs a human · mock-ui", the PR (`compliance-framework/mock-ui#13`, linked)
and its title, fields Why / CI / Opened by / Waiting, and a Review PR button
([notify.md](notify.md#needs-a-human)).

Human PRs are never posted.

## Weekly digest

`attention-digest.yml` (run in this repo; logic in `cmd/attention-digest`, rules in
`internal/attention`) posts one message to `SLACK_CHANNEL_NEEDS_HUMAN` every Monday at 08:30 UTC,
listing every open PR of the manifest's repos (this `workflows` repo included) that:

- carries `needs-human` (anyone's PR);
- is a bot PR (opened by `ccf-release-bot[bot]`, or from a `renovate/`, `ccf-bump/` or
  `release-please--` branch) open longer than 7 days (`--stale-days`), except a release-please PR:
  it waits for the monthly train by design, so its age never lists it, only the rules below or
  the label;
- is a bot PR whose required check (`ci / required`, `--required-checks`) failed;
- is a release-please PR whose `release-checks / release-checks` check failed: `needs
  release:major-approved (<package>: vA -> vB)` when the release-please manifest goes up a major
  at the head against the base and the PR lacks the label (what version-guard checks), else
  `release-checks / release-checks failed`.

The post is a Block Kit card ([slack.md](slack.md)) with an amber bar: the header ":raising_hand:
2 PRs need a human", then one compact section per repo, in manifest order, with a line per PR:

```text
compliance-framework/mock-ui
• compliance-framework/mock-ui#13 chore(deps): update typescript to v7 · labelled needs-human, open over 7d · 9d
compliance-framework/workflows
• compliance-framework/workflows#52 chore(main): release 2.0.0 · needs release:major-approved (.: v1.4.0 -> v2.0.0) · 3d
```

Each line is `<url|owner/repo#n> title · reasons · age`. A context line at the end says what could
not be read and links the run. With no PR to list it posts nothing (the run log says so). A repo or
PR it could not read is listed under "Could not read" and fails the run, after posting. The run log
(and a dry run) prints the same digest as text.

| Input (`workflow_dispatch`) | Default | What |
| --- | --- | --- |
| `manifest` | `repos.mock.yaml` | The repos to read. A scheduled run has no inputs and uses the `MANIFEST` fallback in the workflow, which equals this default: change both to move to `repos.yaml`. |
| `dry_run` | `true` | Print the digest instead of posting it. Scheduled runs are dry runs until the repo variable `ATTENTION_DIGEST_LIVE` is `true` (the `RENOVATE_LIVE` pattern). |

The job lists the manifest's repos, then mints a `ccf-release-bot` token scoped to exactly those
(`repositories:`) with **read-only** permissions: Pull requests (open PRs), Checks (check runs) and
Contents (release-please manifests). It refuses an empty selection, since an unscoped token would
reach every org repo. Every GitHub call is a `GET`.

```sh
GH_TOKEN=$(gh auth token) go run ./cmd/attention-digest post --manifest repos.mock.yaml --dry-run
```

## Setting it up

1. Create the Slack channel `#ccf-pr-needs-human` and invite the Slack bot (the app behind
   `SLACK_BOT_TOKEN`) to it.
2. Set the org variable `SLACK_CHANNEL_NEEDS_HUMAN` to the channel's ID, visible to the public
   repos like the other channel variables. Until it is set, nothing is posted and nothing fails.
3. When a manual digest run looks right, set the repo variable `ATTENTION_DIGEST_LIVE` to `true`
   in this repo, so the Monday run posts.
