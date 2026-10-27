# Plugin health

`plugin-health.yml` (logic in `cmd/plugin-health` and `internal/pluginhealth`) regenerates one
pinned issue in this repo, **Plugin health**, with a row per kept plugin. It posts to
`#ccf-releases` (`vars.SLACK_CHANNEL_RELEASES`) only when the list of deprecation candidates
changes.

## When it runs

| Trigger | What happens |
| --- | --- |
| `workflow_run` of `ccf-bump-sync` (completed, on `main`, not cancelled) | Regenerates the report. |
| `workflow_run` of `train` (completed, on `main`, not cancelled) | The train runs every hour, so `plan` regenerates the report only when a train issue (label `train`) has closed since the last report: the issue keeps the number of the last closed train it saw. Otherwise the `report` job is skipped. |
| `workflow_dispatch` | Regenerates the report. `dry_run` prints the table to the job summary only. |

There is no schedule. Triggering by `workflow_run` leaves `train.yml` and `ccf-bump-sync.yml`
untouched.

## Rows

One row per **kept plugin**: every active repo (`manifest.ActiveRepos()`) of kind `go-plugin`
with `release: true`. The templates (`release: false`) have no row. Each row reads:

| Column | Source |
| --- | --- |
| Agent library | The `github.com/<owner>/agent` version that `go.mod` requires at the plugin's latest release tag. A `replace` to a version wins, and a `replace` to a local path leaves it unknown. The report reads `go.mod` rather than the release binary's build info, because that needs no download. `plugin-probe` reads the build info. |
| Minors behind | The minor-version gap to the agent's latest final release (the highest `vX.Y.Z`, as `ccf-bump sync` targets it); `older major` when the major is lower. |
| Protocol | The `org.ccf.plugin.protocol.version` annotation on `ghcr.io/<owner>/<plugin>:latest`, read as the agent reads it: `v1`, `v2` or `unset` (missing, not 1 or 2, or no `:latest` image). GHCR is read anonymously, so a private package shows as a failed read. |
| Last ccf-bump | The newest of the repo's 100 newest PRs from a `ccf-bump/` branch of the repo itself: `merged <date>`, `closed` (unmerged), `failed` (open, and its newest `ci / required` run concluded other than success, skipped or neutral) or `open`. |
| Last release | GitHub's latest release (not a draft or prerelease): tag and publication date. |

## Status rules

Applied in this order (`pluginhealth.Evaluate`):

1. **deprecation candidate**: either the protocol is `v1` or `unset` and the plugin has no v2
   `Init`, or the plugin is **3 or more** agent minors behind (or on an older major). A plugin has
   a v2 `Init` when `main.go` at its release tag serves `runner.RunnerV2GRPCPlugin`.
2. **unknown**: the agent library, the protocol or (for `v1`/`unset`) the `Init` check couldn't
   be read, for example when there is no release yet or a read failed.
3. **behind**: **2 or more** agent minors behind.
4. **healthy**: everything else.

A plugin that serves v2 but is annotated `v1` or `unset` stays healthy with a note: the agent
runs it as v1, without `Init`, until its release annotates protocol 2.

The report doesn't run plugins. `plugin-probe`, which loads each plugin through the agent's
runner, is the authority on what a binary speaks.

## The issue and Slack

- The issue is found by its title among the open issues opened by `github-actions[bot]` (the
  workflow's `GITHUB_TOKEN`). Issues with the same title opened by anyone else are ignored. If
  none is found, a new issue is opened and pinned. If the pin fails (`GITHUB_TOKEN` may lack the
  right), the run only warns, and a maintainer pins the issue once by hand.
- The body starts with a hidden state line,
  `<!-- plugin-health {"candidates":[...],"train":N} -->`. The next run compares its candidates
  with this list and posts to Slack only when they differ (order is ignored). A candidate whose
  reads failed this time stays on the list, so a flaky read doesn't post a change.
- Without `SLACK_BOT_TOKEN` or a channel, nothing is posted.

## Tokens

- `plan` uses `GITHUB_TOKEN` with `issues: read`. It needs no secrets, so the hourly
  train-triggered runs never enter the `release` environment.
- `report` runs in the `release` environment. It mints a ccf-release-bot token scoped to the agent
  and the plugins `plan` listed, with `contents`, `pull-requests` and `checks` set to read. It
  writes the issue with `GITHUB_TOKEN` (`issues: write`).
- After it writes the issue, the run fails if any read failed. The failed reads are listed in the
  row's notes and in the error.

```text
plugin-health plan   [--manifest repos.yaml] [--event E --workflow W]   # repos=..., due=...
plugin-health report [--manifest repos.yaml] [--dry-run] [--owner org] [--registry ghcr.io]
```
