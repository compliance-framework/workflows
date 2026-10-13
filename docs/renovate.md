# Renovate

Renovate owns every third-party dependency of the manifest's repos. `ccf-bump` owns every
`compliance-framework` one, so the two tools never touch the same version. Renovate runs
self-hosted from this repo (`renovate.yml`), with the `ccf-release-bot` app's token and the shared
preset in `renovate/default.json`. It needs no Mend subscription, and Actions minutes are free on
public repos.

## The preset: `renovate/default.json`

| Rule | Setting |
| --- | --- |
| Schedule | `* * 8,22 * *` (UTC): normal updates only on the 8th and 22nd, away from the monthly train, so each one spends at least a week on `main` before the next release. |
| Vulnerability fixes | Any day (`vulnerabilityAlerts.schedule: at any time`), with no minimum age. `osvVulnerabilityAlerts: true` finds CVEs from osv.dev even without a Dependabot alert. Each fix gets its own `[SECURITY]` PR, outside the group and the concurrent-PR limit, and auto-merges unless it is a major. Every fix is `fix(deps)` (`vulnerabilityAlerts.semanticCommitType: "fix"`), so it releases: Renovate forces the `vulnerabilityAlerts` settings onto each security update over the package rules, while the depType rules alone still let one through as `chore(deps)`, which release-please hides ([mock-plugin-2#14](https://github.com/compliance-framework/mock-plugin-2/pull/14)). |
| Waiting period | `minimumReleaseAge: "7 days"` with `internalChecksFilter: "strict"`: no branch or PR before a release is a week old. An update with no release timestamp waits too (Renovate's default `minimumReleaseAgeBehaviour`). |
| Grouping | Every `minor`, `patch`, `digest`, `pin` and `pinDigest` update of a repo goes into one PR, `renovate/all-non-major` ("all non-major dependencies"), so a run costs one CI run per repo. The Go toolchain and the `golang` image are in it. Majors get their own PRs. |
| Auto-merge | The non-major group (and every non-major vulnerability fix) auto-merges, but Renovate merges it itself (`platformAutomerge: false`, `automergeType: "pr"`, `automergeStrategy: "squash"`): on its next run after the checks pass, Renovate squash-merges the PR through the API as `ccf-release-bot`. Majors wait for a human. See [Why Renovate merges, not GitHub](#why-renovate-merges-not-github). |
| Rebasing | `rebaseWhen: "conflicted"`: a branch is rebased only when it conflicts with `main`, not every time it falls behind. See [One run merges every green PR](#one-run-merges-every-green-pr). |
| Limits | `prHourlyLimit: 1`, `prConcurrentLimit: 8` per repo. With a live run's shard of at most 15 repos, that bounds the PRs across the org; see [The org-wide throttle](#the-org-wide-throttle). |
| Commits | Semantic: `fix(deps)` for runtime dependencies (Go `require`, npm `dependencies`, a Dockerfile's final stage, the Go `toolchain` directive), which release-please releases, and `chore(deps)` for everything else (dev, CI, actions). A group takes the highest type of its updates, so a group with any runtime update is `fix(deps)`. Security fixes are always `fix(deps)`, whatever their dependency type (see Vulnerability fixes). |
| Go | `postUpdateOptions: ["gomodTidy"]`. Indirect modules are left to `go mod tidy` (Renovate's default). |
| Indirect Go modules | When Renovate does update one, it is `fix(deps)`, not the `chore(deps)` that `depType: indirect` gets by default: indirect modules are linked into the binary, so the update must release (`matchManagers: ["gomod"], matchDepTypes: ["indirect"], semanticCommitType: "fix"`). A security fix to one is `fix(deps)` through `vulnerabilityAlerts` regardless of this rule. |
| Actions | Pinned by digest (`helpers:pinGitHubActionDigests`). Dockerfile base images and images in helm values are tracked by Renovate's own managers. |
| OPA | Off in every repo except `api` (and `mock-api`), so in agent, the policy repos and the plugins it follows the api through `ccf-bump`. In `api` it gets its own PR, `renovate/opa`, which never auto-merges, because a bump forces agent and the policies to follow. |
| Needs a human | Majors and the api's `renovate/opa` PR get the `needs-human` label (`addLabels`). See [attention.md](attention.md). |
| Internal packages | Off: `github.com/compliance-framework/**` (Go modules), `ghcr.io/compliance-framework/**` (images) and `compliance-framework/**` (actions and reusable workflows, such as this repo's). The mocks use the same prefixes. |

### Why Renovate merges, not GitHub

GitHub's native auto-merge (`platformAutomerge: true`) can't merge these PRs. The `ccf-review`
ruleset requires one approving review, and only `ccf-release-bot` may bypass it. GitHub's
auto-merge never applies that bypass, so a Renovate PR with a green `ci / required` stays
`BLOCKED` / `REVIEW_REQUIRED` until a human approves it (seen on the mocks in run 37754738550).
A merge through the API as the bot does use the bypass. The `ccf-required` ruleset has no bypass,
so the required check still gates every merge.

The cost is merge lag: GitHub would merge as soon as the check passes, while Renovate merges on
its next run that finds the checks green. `renovate.yml` runs hourly and visits each repo's shard in
turn, so a PR merges up to one rotation (at most 5 hours with every batch active) after its checks
pass. The 8th/22nd `schedule` doesn't delay this: it only limits when new
branches are created, while Renovate keeps updating and merging existing PRs on the other days
(its defaults `updateNotScheduled: true` and `automergeSchedule: ["at any time"]`, which the preset
leaves alone). A PR whose checks fail stays open, and Renovate updates or recreates it
on later runs as usual. The repo's "allow auto-merge" setting is no longer used by Renovate.

### One run merges every green PR

With auto-merge on, Renovate's default `rebaseWhen: "auto"` acts as `behind-base-branch`. Once it
merges one PR, every other PR of that repo is behind `main`, so it rebases them instead of merging
them, and their CI starts again. A run then merges at most one PR per repo: a repo with three
security PRs takes three runs, and every rebase costs a CI run (seen on `mock-plugin-1` in
run 37758663162). The preset sets `rebaseWhen: "conflicted"`, so Renovate merges every PR whose
checks are green in the same run and rebases a branch only when it really conflicts with `main`
(`go.mod` and `go.sum` conflicts are textual, so they are caught). This works because the
`ccf-required` ruleset doesn't require branches to be up to date
(`strict_required_status_checks_policy: false`), and a squash merge still checks for conflicts
when it merges.

The trade-off: a PR's CI may have run against an older `main` than the one it merges into. The
push to `main` runs CI again on the merged result, and if that breaks, the repo's
`notify-failure.yml` opens an incident, as for any red `main` (rule (b) in [notify](notify.md)).

It extends `config:recommended`, which adds the Dependency Dashboard issue (where majors and pending
updates are listed) and Renovate's standard monorepo groups.

A repo may add its own `renovate.json` for overrides only: Renovate applies the preset to every
listed repo as its base, and the repo's file is merged on top. Repos need no onboarding PR.

Validate the preset after editing it, as a preset and as global config:

```sh
npx --yes --package renovate -- renovate-config-validator --strict --no-global renovate/default.json
npx --yes --package renovate -- renovate-config-validator --strict renovate/default.json
```

## The org-wide throttle

`prHourlyLimit` is per repo in Renovate (each repo counts the PRs it opened since the top of the
hour), so on its own it can't hold the org to the plan's ~15 PRs an hour: with 37 repos active
after batch 1, even `1` would allow 37. Renovate 44.145.1 (the pinned version) has one global
limit, the self-hosted `prCommitsPerRunLimit`, but it counts commits per run rather than PRs per
hour, counts vulnerability fixes and rebases, and when it is reached Renovate stops processing the
remaining repos for that run. Those repos then get no vulnerability fixes and no auto-merges until a
later run, and the same repos at the head of the list would always go first. So the workflow shards
instead:

- A **live** run works on one shard of at most 15 repos (`renovate.MaxLiveRepos`). The active repos,
  in manifest order, are split into `n = ceil(repos / 15)` shards, with repo `i` in shard `i mod n`.
  The run takes shard `slot mod n`, where `slot` is the number of hours since the Unix epoch. The
  same slot scopes the token and builds the config.
- `renovate.yml` runs **hourly** (`17 * * * *`), so consecutive runs take the shards in turn and
  each repo is visited every `n` hours.
- The preset's `prHourlyLimit: 1` lets each repo of the shard open at most one non-vulnerability
  PR per visit. The non-major group comes first, then majors one per visit.
- **Vulnerability fixes keep flowing.** Renovate exempts them from `prHourlyLimit` (and from
  `schedule` and `prCommitsPerRunLimit`; docs: "vulnerability alerts skip the line"), so each
  visit opens every fix a repo needs. They wait at most one rotation for their repo's shard.
- Dry runs write nothing, so they aren't sharded and take every selected repo.

Worst case for non-vulnerability PRs opened across the org in one hour, from `repos.yaml` (7 core
repos, this one included; batch 1 has 30 repos, batch 2 27, batch 3 10):

| Active batches | Active repos | Shards | Repos per run | Non-vulnerability PRs per hour | Each repo visited every |
| --- | --- | --- | --- | --- | --- |
| none (today) | 7 | 1 | 7 | 7 | 1 hour |
| 1 | 37 | 3 | 13 | 13 | 3 hours |
| 1, 2 | 64 | 5 | 13 | 13 | 5 hours |
| 1, 2, 3 | 74 | 5 | 15 | 15 | 5 hours |

Vulnerability PRs come on top of these and are deliberately not capped. A manual live dispatch in
the same hour as a scheduled run can add one more shard's worth: a repo already visited that hour
opens nothing more (its `prHourlyLimit` is spent), but a run that lands on another shard does. A
run is late only when the previous one overruns (the `renovate` concurrency group queues it). A
live run's shard also keeps it within the app token's one-hour life.

On the 8th and 22nd (the preset's `schedule`), a repo gets its non-major group PR on its first visit
that day and a major on each later one. With 5 shards, that is about five visits a day.

## The workflow: `renovate.yml`

Runs hourly at minute 17 and on `workflow_dispatch`, as one job:

1. `go run ./cmd/renovate-config list` selects the repos from the manifest (every active repo,
   this `workflows` repo included, or the `repos` subset) and fails on an empty selection. A live
   run then keeps only one shard of the selection ([The org-wide throttle](#the-org-wide-throttle)).
2. `go run ./cmd/renovate-config config` writes the Renovate global config: the preset's settings
   plus `platform: github`, `autodiscover: false`, `repositories` (the selection, as
   `compliance-framework/<name>`), `onboarding: false`, `requireConfig: "optional"` and `dryRun`.
   It refuses a preset that sets any of those itself.
3. `actions/create-github-app-token` mints a `ccf-release-bot` token scoped with `repositories:` to
   exactly the selected repos (the app is installed on every org repo, so an unscoped token would
   reach all of them). A dry run gets read-only permissions; a live run gets Contents, Pull requests
   and Issues write, Workflows write (to update actions in `.github/workflows`), and Checks and
   Dependabot alerts read.
4. `renovatebot/github-action` runs Renovate, pinned with its `renovate-version` input, with that config and token.
   This repo is in Renovate's list, so Renovate updates the action's SHA pin like any third-party
   action; check that `renovate-version` moves with it, and bump it by hand if it doesn't.

| Input | Default | What |
| --- | --- | --- |
| `manifest` | `repos.yaml` | The repos to run on: the product repos and this one. `repos.mock.yaml` for the mocks. |
| `repos` | every active repo in the manifest | Comma-separated subset; each must be in the manifest. A live run takes only active repos, and one shard of at most 15 of them. A dry run may name repos of an inactive batch, to check the batch before it is switched on ([manifest.md](manifest.md#switching-a-batch-on)). |
| `dry_run` | `full` | Renovate's `dryRun`: `full` logs every branch and PR it would create, `lookup` stops after finding updates, `extract` after reading the package files. `off` runs live. |
| `log_level` | `info` | `debug` for a full trace. |

A scheduled run has no inputs and uses these defaults, except `dry_run`: it is `off` (live) when the
repo variable `RENOVATE_LIVE` is `"true"`, and `full` otherwise. Scheduled runs read `repos.yaml`;
dispatch with `manifest=repos.mock.yaml` for the mocks. With `RENOVATE_LIVE` set, a scheduled run is
live on the product repos and this one, the same switch as `CCF_BUMP_SYNC_LIVE` and `TRAIN_LIVE`.
A dispatch always follows its `dry_run` input. A live scheduled run is also what merges Renovate's green PRs (see above), so leave it on once
Renovate is live. Secrets: `RELEASE_BOT_APP_ID` and `RELEASE_BOT_PRIVATE_KEY`, from this repo's `release` environment (main only). Variable:
`RENOVATE_LIVE`.

### Commit statuses

`ccf-release-bot` has no Commit statuses permission, so the generated config sets
`statusCheckWhen.minimumReleaseAge: "never"`: Renovate doesn't set its `renovate/stability-days`
status. It isn't needed, because `internalChecksFilter: "strict"` already keeps a branch from being
created before the update is a week old. Renovate still reads statuses (the repos are public). If
the app is later granted Commit statuses write, this can go, and Renovate will also mark a failed
artifact update (`renovate/artifacts`) on the branch.

### Failures

A Renovate branch starts with `renovate/`, so when its CI fails, the repo's `notify-failure.yml`
posts it to `#ccf-ci-failures` (rule (a)), and the failed check keeps Renovate from merging it.

## Running it

- **Dry run of a batch before it is switched on**:
  `gh workflow run renovate.yml -f repos=plugin-a,plugin-a-policies,... -f dry_run=full` with the
  batch's repos. The log lists each `DRY-RUN: Would create PR` per repo. A dry run isn't sharded and
  its read-only token lasts an hour, so dispatch a large batch in groups of about 15 repos.
- **Dry run on the mocks**: Actions, `renovate`, Run workflow, or
  `gh workflow run renovate.yml -f manifest=repos.mock.yaml -f dry_run=full`. The log lists each
  `DRY-RUN: Would create PR` per repo; expect about one grouped PR per repo plus its majors.
- **Live on the mocks**: the same with `-f dry_run=off`. Renovate opens the grouped PRs; a later
  dispatch with `dry_run=off` merges the ones whose checks have passed. `repos.mock.yaml` lists this
  `workflows` repo too, so name the mocks with `-f repos=...` to leave it out.
- **Scheduled runs live**: set the repo variable `RENOVATE_LIVE` to `true`
  (`gh variable set RENOVATE_LIVE --body true --repo compliance-framework/workflows`); unset it to go
  back to dry runs.
- **Live on the product repos**: done (W1-S4-T03). Scheduled runs read `repos.yaml`, the product
  repos and this one, and run live while `RENOVATE_LIVE` is set. A Go-module update here is
  `fix(deps)` and makes a workflows release; `ccf-bump` moves the callers' pins on its next sync.

Locally, `go run ./cmd/renovate-config config --manifest repos.mock.yaml` prints the config the
workflow would use (the preset path defaults to `renovate/default.json`, run from the repo root).
