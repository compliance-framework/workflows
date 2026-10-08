# `notify-failure.yml`: Slack incident threads

The reusable workflow `.github/workflows/notify-failure.yml` keeps one Slack thread per CI
incident. The logic is `cmd/notify`, with the rules in `internal/notify`; the workflow builds
it from this repo at the commit it is called at (`job.workflow_sha`, the caller's `@<sha>`).

## Which runs

A run is tracked when:

- (a) it's for a pull request opened by `ccf-release-bot[bot]`, or for a branch starting with
  `renovate/` or `ccf-bump/` (a pull request, or a push to that branch);
- (b) it's for a push to the default branch.

Every other run (a human's PR, a push to another branch, a tag, a schedule) does nothing.

## Incidents

An incident is keyed by the repo + the caller workflow's file + the pull request number for
`pull_request` runs, or + the branch for pushes. Each workflow file has its own incidents, so
two workflows of one repo don't close each other's.

The run's result comes from the `needs` input, the calling job's `toJSON(needs)`: any job
with result `failure` makes it a failure, listing those jobs; otherwise any `cancelled` job,
or no job having run, leaves the incident as it is; otherwise (successes, with or without
skips) it's a pass.

| Result | Incident | Posts | Then |
| --- | --- | --- | --- |
| failure | none, or closed | a top-level message in the channel | open, with the message's `ts` |
| failure | open, same commit and failed jobs | nothing (a re-run) | unchanged |
| failure | open, another commit or other failed jobs | a reply in the thread | open |
| pass | open | `✅ passing again` in the thread | closed: the next failure starts a new thread |
| pass | none, or closed | nothing | unchanged |
| cancelled / all skipped | any | nothing | unchanged |

Messages (Slack mrkdwn; the short SHA links to the commit, `run` to the run):

```text
❌ compliance-framework/mock-api PR #12 failed: ci, release-checks at 2222222 · run
Pull request #12 chore(deps): bump x · workflow ci

❌ compliance-framework/mock-api main failed: ci at abc1234 · run
workflow ci

❌ failed: ci at 3333333 · run                (a reply)
✅ passing again at 4444444 · run             (a reply)
```

The commit is the PR head for pull requests (not the test merge commit), so re-runs after
the base moves count as the same commit.

## State

The state of an incident (`channel`, the top-level message `ts`, `open`, the last posted
commit and failed jobs) is a small JSON file kept in the Actions cache:

- the key is `ccf-notify-incident-<hash of the incident key>-<run id>-<run attempt>`, so
  every save is a new entry;
- the job restores with that prefix as `restore-keys`, and the cache returns the newest
  entry with it, so the latest state wins;
- it saves only when it posted (so a pass with no incident, or a re-run, saves nothing).

Cache scoping does the rest: a `pull_request` run saves under the PR's merge ref, which
later runs of the same PR read; a push to `main` saves under `main`, which later `main` runs
read. A PR's key never matches `main`'s, so PRs don't pick up `main`'s incident.

Replies go to the incident's own channel; the `channel` input only picks where new incidents
start. No GitHub permission beyond `contents: read` and no Slack scope beyond `chat:write`
(replies are `chat.postMessage` with `thread_ts`).

Limits:

- Caches unused for 7 days are evicted. An incident with no run for a week loses its state,
  and its next failure starts a new thread (its next pass posts nothing).
- Two runs of the same incident finishing at the same moment both read the same state, so
  both may open a thread. CI workflows that cancel superseded PR runs make this rare.
- A state that can't be read (bad JSON, another key) is reported in the log and treated as
  no incident.

## Needs a human

A second rule, separate from incidents, posts a tracked bot PR that waits for a person to the
channel in the `SLACK_CHANNEL_NEEDS_HUMAN` variable, once per PR (see
[attention.md](attention.md)). With the variable empty the rule is off: nothing is posted, the
incidents work as before, and nothing fails.

For a `pull_request` run tracked by rule (a) (`notify.Route`):

| The PR | Posts to `SLACK_CHANNEL_NEEDS_HUMAN` | Incident |
| --- | --- | --- |
| carries the `needs-human` label (in the event payload) | `needs a human: <reason>`: `major update` (`renovate/*`), `OPA update in the api (never auto-merged)` (`renovate/opa`), `auto-merge off (...)` (`ccf-bump/*`), or `labelled needs-human` | as usual |
| a release-please PR (`release-please--*`) whose `release-checks` job failed | `release PR blocked by release-checks (e.g. needs release:major-approved, or an internal dep isn't final)` | none when `release-checks` is the only failed job (the incident sees the run as cancelled and stays as it is); as usual when other jobs failed too |
| anything else, or a human's PR | nothing | as usual |

```text
:raising_hand: mock-ui#13 chore(deps): update typescript to v7 — needs a human: major update
```

Once per PR: the job looks up the cache key `ccf-notify-needs-human-<hash of repo + PR>`
(`lookup-only`), posts only on a miss, and then saves a small record under that key. Re-runs,
pushes and other workflows of the same PR find it and post nothing. Like incident state, the
record is evicted after 7 days without use, so a PR idle for a week may be posted again; two
runs of one PR finishing at the same moment may both post. The weekly
[attention digest](attention.md) lists every PR still waiting either way.

The callers must run CI on `labeled` (`pull_request` `types: [opened, edited, synchronize,
reopened, labeled, unlabeled]`, as every mock and the caller templates do), because Renovate and
ccf-bump add the label after opening the PR: the `labeled` run is the one that sees it. The job's
`needs` must include the `release-checks` job under that name.

## Calling it

```yaml
# in the consuming repo's ci.yml, after the CI jobs
  notify:
    needs: [ci, release-checks]  # every job whose failure should notify
    if: always()
    uses: compliance-framework/workflows/.github/workflows/notify-failure.yml@<sha> # vX.Y.Z
    with:
      needs: ${{ toJSON(needs) }}
    permissions:
      contents: read
    secrets: inherit
```

| Input | Default | What |
| --- | --- | --- |
| `needs` | `""` | The calling job's `toJSON(needs)`: the jobs and their results. Empty means the run failed, with the failed jobs unknown (callers still on `if: failure()`). |
| `channel` | `""` | Slack channel ID for new incidents; empty means the `SLACK_CHANNEL_CI_FAILURES` variable. |
| `workflows-ref` | `""` | Ref of this repo to build `cmd/notify` from, to test another ref; empty means the commit the workflow is called at. Callers don't pass it: ccf-bump deletes it when it moves the job's pin. |

Secret: `SLACK_BOT_TOKEN` (optional, `chat:write`), via `secrets: inherit`. Without it (forks,
repos outside the secret's scope) the job does nothing and succeeds. Variables:
`SLACK_CHANNEL_CI_FAILURES` (incidents, unless `channel` is given) and `SLACK_CHANNEL_NEEDS_HUMAN`
(optional, [Needs a human](#needs-a-human)).

The job now runs after every CI run, so passes also pay for building `cmd/notify`; runs that
aren't tracked stop right after the build.

## Migrating from `if: failure()`

The earlier caller ran the job only on failure, with no inputs, and granted `actions: read`:

```yaml
  notify:
    needs: [ci]
    if: failure()
    uses: compliance-framework/workflows/.github/workflows/notify-failure.yml@<sha> # vX.Y.Z
    permissions:
      actions: read
      contents: read
    secrets: inherit
```

Change `if: failure()` to `if: always()`, add `with: {needs: ${{ toJSON(needs) }}}`, and drop
`actions: read` (no longer used; keeping it is harmless). Until a repo migrates, its failures
still open and reply in threads, without the failed job names, but its incidents never close:
the job doesn't run on passes, so nothing posts the recovery.

What changed for the default branch: it used to post only when the previous push run had
passed (one message when `main` broke). Now the open incident does that job: the first
failure opens a thread, each later failing commit replies in it, and the first pass closes it.
The old `ccf-notify-failure-*` dedupe cache entries are no longer read.
