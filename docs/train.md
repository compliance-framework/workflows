# The release train

The release train (`train.yml`, `cmd/train`, logic in `internal/train`) releases the manifest's
repos once a month, stage by stage in `Stages()` order: each stage starts once every repo of the
earlier stages is released or skipped, and helm repos release last. Before a repo releases, the
train bumps its internal dependencies to what the earlier stages released, with
[`ccf-bump`](ccf-bump.md).

## A repo's way through the train

| Status | What the train does, and what it waits for |
| --- | --- |
| `waiting` | An earlier stage isn't done. |
| `bumping` | Runs `ccf-bump --repo <repo> --mode train --set <dep>=<version>... --pr` with every version the earlier stages released, then merges the bump PR once its required checks pass (see What gates a merge). No release in an earlier stage, or nothing to move, skips the bump. |
| `release-pr` | Waits for release-please's run on the default branch's head (the workflow file `release-please.yml`), then takes its PR (`release-please--branches--<default branch>`). No PR means nothing to release: the repo is `released` with no new version. The run is the signal because release-please leaves its PR behind the branch for commits that release nothing (`ci:`, `chore:`). |
| `merging` | Merges the release PR (squash, as ccf-release-bot, the `ccf-review` bypass actor) once its required checks pass (`ci / required` and `release-checks / release-checks`), and unless `version-guard` would flag it: a major increase without the `release:major-approved` label. |
| `publishing` | Waits for release-please's tags on the merge commit and for every release workflow run of each tag (`event: release`) to succeed. Only release tags count (`vX.Y.Z[-pre]` or `<component>-vX.Y.Z[-pre]` with a version the release PR proposed); floating tags a release workflow moves there (`v0`, `latest`) are ignored. |
| `released` | Done. |
| `skipped` | An org owner commented `/skip <repo>`. The next stage doesn't wait for it, and its version isn't bumped anywhere. |
| `blocked` | Something failed: the PR's required checks, ccf-bump, release-please, a merge or a release workflow. |
| `needs-human` | A decision: a major version, a bump PR without the `ccf-bump:automerge` label (ccf-bump leaves it off for a major update or an unversioned pin), or a closed bump PR. |

Every run reads GitHub's current state, so running it again is always safe, and `blocked` and
`needs-human` clear themselves once the cause is gone: a fixed release PR whose checks pass merges
on the next run. A failed `ccf-bump` stays blocked until `/retry`, because running it again would
fail again. A blocked repo holds up the stages after it, never the other repos of its stage.

### What gates a merge

Only the **required checks** gate a merge (`train.CICheck` and `train.ReleaseCheck` in
`internal/train/api.go`): `ci / required`, the check the `ccf-required` ruleset requires, and on
release PRs the `release-checks` job of each caller's `ci.yml` (version-guard), named too so that a
release PR never merges before it reports:

| PR | Required checks |
| --- | --- |
| bump PR | `ci / required` |
| release PR (release-please) | `ci / required` and `release-checks / release-checks` |

Every other check on the PR (`preview / *`, CodeRabbit, `osv-scanner`, `notify / notify`, the jobs
`ci / required` sums up) never makes a merge wait or hold, whatever it reports.
`--required-check` replaces `ci / required`.

Each required check is judged over every run on the PR's head commit (a re-run, a run for a
`labeled` or `edited` event, or one that superseded a cancelled run adds one):

- a run queued or in progress: **pending**;
- never reported on the head commit: **pending**, not green;
- otherwise its newest run (by start time, then ID) decides: `success`, `skipped` or `neutral`
  passes; `cancelled` (a caller's concurrency group, such as `preview.yml`'s, cancels an older run
  when a newer one starts) or `stale` is **pending**, waiting for a newer run, never a failure; `failure`,
  `timed_out`, `action_required` or `startup_failure` is **failing** and holds the repo `blocked`.

A merge GitHub refuses with 405 "Required status check … is expected" (a run that started after
the checks were read) is waiting, not `blocked`: the next run retries it.

## Tracking issue

Each train has an issue in this repo titled `Release train YYYY-MM` (a second manual train in a
month gets ` (2)`, a dry run ` (dry run)`), labelled `train` and `train:open` while it runs, then
`train:done`, `train:aborted` or `train:dry-run` when closed. The body has a table of every repo's
stage, status, versions and PRs, and the train's state as JSON in a hidden comment at the end.
Don't edit that comment. Every PR reference, in the issue, its comments and the Slack thread, is a
link labelled `<repo>#<n>` (e.g. `mock-agent#14`): a bare `#14` would link to this repo's own #14.

When every repo is done the train:

1. comments the **digest draft**: the versions, then the release notes of every release grouped
   by section across repos, features first, then bug fixes, then the rest;
2. opens an issue in each helm repo of the manifest listing its `depends_on` repos that went up a
   minor (`api`, `ui` and `agent` for `helm-charts`), so the charts pick up their new settings;
3. posts the versions and the digest link in the Slack thread, posts the digest draft card to
   `vars.SLACK_CHANNEL_DIGESTS` (see Slack), and closes the issue.

### Comment commands

Org owners (`GET /orgs/compliance-framework/memberships/<user>` says `role: admin`) can comment
on an open train's issue:

| Command | Effect |
| --- | --- |
| `/skip <repo>` | Skips the repo; the next stage can start without it. |
| `/retry <repo>` | Clears the repo's hold and tries again: runs ccf-bump again (`bumping`), re-runs the failed jobs of a failed release workflow (`publishing`), or un-skips a skipped repo. |
| `/abort` | Stops the train and closes the issue. Nothing is reverted. |

The train replies to each command. Commands from anyone else are refused, and so is every command
when the run has no members token (see Tokens).

## Slack

The train posts to `vars.SLACK_CHANNEL_RELEASES`. Its parent message is a **live board**
(`train.Board`, a `slackkit` card, [slack.md](slack.md)): the title, "Stage n of m · running" (or
"· k held", Finished, Aborted), one section per stage with each repo's pill (:large_green_circle:
released, :large_yellow_circle: in progress, :red_circle: held, :white_circle: waiting,
:fast_forward: skipped), its status and released version, a Tracking issue button, and a bar that is
amber while running, red while a repo is held, green finished and grey aborted. The board's channel
ID and `ts` are kept in the state with a hash of its content, and every run whose save changes the
content edits it with `chat.update` (a failed edit is a warning, retried next run). Thread replies,
small cards, follow when a repo becomes blocked or needs a human (once per cause), when a run
fails, when a repo is skipped and when the train finishes or is aborted. A train still open when a
new month starts gets one message in the channel itself, on the first run of that month. Without
`SLACK_BOT_TOKEN` messages are only printed.

When the train finishes it also posts the **digest draft** card, once, to
`vars.SLACK_CHANNEL_DIGESTS` (#ccf-release-digests): the title "Release digest YYYY-MM (draft)"
with a draft line and a blue bar, each released repo's from → to versions, up to 10 features as
highlights, and buttons to each release's notes and the tracking issue (whose digest comment has
the full draft). People edit and share it from there. Without the variable it is skipped quietly;
the issue comment is posted either way.

## Dry run

`--dry-run` plans every stage as if the earlier ones had released what their release PRs propose
(a bump alone means a patch), runs `ccf-bump --pr --dry-run` with those planned versions, and
comments the plan on the issue and in the Slack thread, with the chart issues it would open. It
merges, pushes and opens nothing, and closes the issue in the same run. ccf-bump can't plan a pin
that needs the planned tag to exist (a pseudo-version, an OPA version); the plan says so.

## `train.yml`

| Trigger | Runs |
| --- | --- |
| `schedule` `0 8 1-7 * *` | `start --scheduled`: starts a train only on `NextWorkingWeekday` of the 1st (holidays from the manifest), once per month, over `repos.mock.yaml` for now. A dry run unless the repo variable `TRAIN_LIVE` is `true`. |
| `schedule` `17 * * * *` | `reconcile`; nothing to do when no train is open. |
| `repository_dispatch` `release-finished`, `bump-merged` | `reconcile`. `release-finished.yml` sends `release-finished` at the end of every release. |
| `issue_comment` | `reconcile`, for a comment starting with `/` on an open train's issue. |
| `workflow_dispatch` | `action` (`start` or `reconcile`), `manifest` (`repos.mock.yaml` or `repos.yaml`), `repos` and `dry_run` (default `true`). |

Runs share one concurrency group (at the job level, so comments that aren't commands don't
queue). A run with an open train keeps reconciling every minute for up
to 30 minutes while a repo waits on GitHub (release-please, CI, a release workflow), and stops
early once every repo left is held for a human.

A train started while another is open only moves the open one on: one train at a time.

### Tokens

- `GITHUB_TOKEN` with `issues: write`: the tracking issue.
- ccf-release-bot (`RELEASE_BOT_APP_ID`/`RELEASE_BOT_PRIVATE_KEY`), scoped to exactly the train's
  repos (`train select` lists them; an open train's repos come from its state and must be in its
  manifest with `release: true`). Read-only for a dry run; otherwise contents, pull requests,
  issues (chart issues) and actions (`/retry`) write, workflows write (bumps edit workflow files),
  and checks and statuses read. Its commits are authored by the bot.
- ccf-release-bot scoped to `workflows` with organization Members read, for comment commands. If
  the app lacks that permission, minting fails without failing the run, and commands are refused.

## Running it

```sh
go build -o /tmp/ccf-bump ./cmd/ccf-bump
# Plan a train over the mocks (GH_TOKEN reads the repos; TRACKER_TOKEN writes issues here):
GH_TOKEN=... TRACKER_TOKEN=... go run ./cmd/train start --manifest repos.mock.yaml --dry-run --ccf-bump /tmp/ccf-bump
go run ./cmd/train select --for reconcile   # the open train's manifest and repos
```

Flags: `--owner` (default `compliance-framework`), `--tracker-repo` (`workflows`),
`--required-check` (`ci / required`), `--release-please-workflow` (`release-please.yml`),
`--watch` and `--interval`.
