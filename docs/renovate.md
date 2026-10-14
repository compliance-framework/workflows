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
| Vulnerability fixes | Any day (`vulnerabilityAlerts.schedule: at any time`), with no minimum age. `osvVulnerabilityAlerts: true` finds CVEs from osv.dev even without a Dependabot alert. A repo's fixes go into one `[SECURITY]` PR per package manager, `renovate/security-<manager>` (e.g. `renovate/security-gomod`), outside the non-major group and the concurrent-PR limit; it auto-merges. A major fix gets its own, `renovate/major-security-<manager>`, for a human. See [Why security fixes are grouped](#why-security-fixes-are-grouped). Every fix is `fix(deps)` (`vulnerabilityAlerts.semanticCommitType: "fix"`), so it releases: Renovate forces the `vulnerabilityAlerts` settings onto each security update over the package rules, while the depType rules alone still let one through as `chore(deps)`, which release-please hides ([mock-plugin-2#14](https://github.com/compliance-framework/mock-plugin-2/pull/14)). |
| Waiting period | `minimumReleaseAge: "7 days"` with `internalChecksFilter: "strict"`: no branch or PR before a release is a week old. An update with no release timestamp waits too (Renovate's default `minimumReleaseAgeBehaviour`). |
| Grouping | Every `minor`, `patch`, `digest`, `pin` and `pinDigest` update of a repo goes into one PR, `renovate/all-non-major` ("all non-major dependencies"), so a run costs one CI run per repo. The Go toolchain and the `golang` image are in it. Majors get their own PRs. |
| Auto-merge | The non-major group (and every non-major vulnerability fix) auto-merges, but Renovate merges it itself (`platformAutomerge: false`, `automergeType: "pr"`, `automergeStrategy: "squash"`): on its next run after the checks pass, Renovate squash-merges the PR through the API as `ccf-release-bot`. These PRs are also labelled `renovate:automerge` (one rule, `addLabels`), and `renovate.yml`'s [merge pass](#the-merge-pass) merges them right after each live run, since Renovate merges only one per repo per run (it squash-merges with the same commit title and message Renovate's merge gets). Majors and OPA updates don't get the label: majors, and OPA in the api, wait for a human. See [Why Renovate merges, not GitHub](#why-renovate-merges-not-github). |
| Rebasing | `rebaseWhen: "conflicted"`: a branch is rebased only when it conflicts with `main`, not every time it falls behind. See [Why security fixes are grouped](#why-security-fixes-are-grouped). |
| Limits | `prHourlyLimit: 1`, `prConcurrentLimit: 8` per repo. With a live run's shard of at most 15 repos, that bounds the PRs across the org; see [The org-wide throttle](#the-org-wide-throttle). |
| Commits | Semantic: `fix(deps)` for runtime dependencies (Go `require`, npm `dependencies`, a Dockerfile's final stage, the Go `toolchain` directive), which release-please releases, and `chore(deps)` for everything else (dev, CI, actions). A group takes the highest type of its updates, so a group with any runtime update is `fix(deps)`. Security fixes are always `fix(deps)`, whatever their dependency type (see Vulnerability fixes). |
| Go | `postUpdateOptions: ["gomodTidy"]`. Indirect modules are left to `go mod tidy` (Renovate's default). |
| Indirect Go modules | When Renovate does update one, it is `fix(deps)`, not the `chore(deps)` that `depType: indirect` gets by default: indirect modules are linked into the binary, so the update must release (`matchManagers: ["gomod"], matchDepTypes: ["indirect"], semanticCommitType: "fix"`). A security fix to one is `fix(deps)` through `vulnerabilityAlerts` regardless of this rule. |
| Actions | Pinned by digest (`helpers:pinGitHubActionDigests`). Dockerfile base images and images in helm values are tracked by Renovate's own managers. |
| OPA | Off in every repo except `api` (and `mock-api`), so in agent, the policy repos and the plugins it follows the api through `ccf-bump`. In `api` it gets its own PR, `renovate/opa`, which never auto-merges, because a bump forces agent and the policies to follow. An OPA security fix gets its own security PR too, `renovate/security-gomod-opa`, so that in `api` it doesn't hold the other Go fixes (a group auto-merges only if every update in it does). Elsewhere Renovate still opens one, since it forces vulnerability fixes on over `enabled: false` (as for internal packages), and it auto-merges there. |
| Needs a human | Majors and the api's `renovate/opa` and `renovate/security-gomod-opa` PRs get the `needs-human` label (`addLabels`). See [attention.md](attention.md). |
| Internal packages | Off: `github.com/compliance-framework/**` (Go modules), `ghcr.io/compliance-framework/**` (images) and `compliance-framework/**` (actions and reusable workflows, such as this repo's). The mocks use the same prefixes. |

### Why Renovate merges, not GitHub

GitHub's native auto-merge (`platformAutomerge: true`) can't merge these PRs. The `ccf-review`
ruleset requires one approving review, and only `ccf-release-bot` may bypass it. GitHub's
auto-merge never applies that bypass, so a Renovate PR with a green `ci / required` stays
`BLOCKED` / `REVIEW_REQUIRED` until a human approves it (seen on the mocks in run 37754738550).
A merge through the API as the bot does use the bypass. The `ccf-required` ruleset has no bypass,
so the required check still gates every merge.

The cost is merge lag: GitHub would merge as soon as the check passes, while Renovate merges on
its next run that finds the checks green, and only one PR per repo. `renovate.yml` runs hourly and
visits each repo's shard in turn, so without more a PR would merge up to one rotation (at most 5
runs with every batch active, about 5 hours when GitHub starts the runs on time) after its checks
pass. The [merge pass](#the-merge-pass) after each live run closes most of that gap: it waits up to
20 minutes for the checks of the PRs Renovate just opened or rebased, and merges every green one.
The 8th/22nd `schedule` doesn't delay this: it only limits when new branches are created, while
Renovate keeps updating and merging existing PRs on the other days
(its defaults `updateNotScheduled: true` and `automergeSchedule: ["at any time"]`, which the preset
leaves alone). A PR whose checks fail stays open, and Renovate updates or recreates it
on later runs as usual. The repo's "allow auto-merge" setting is no longer used by Renovate.

### Why security fixes are grouped

Renovate (44.145.1) merges at most one PR per repo per run: once it merges a branch it stops
working on that repo, then starts the repo over once, so a repo gets at most two merges in a run.
Go security fixes all touch `go.mod` and `go.sum`, so after one merges the others conflict and
are rebased, and Renovate never merges a branch it has just pushed. Each further fix then waits for
the repo's next visit, about `n` hours with `n` shards, so `k` separate fixes need up to `k` visits:
in October 2026 four fixes (`grpc`, `x/net`, `x/text`, `x/crypto`) opened 102 PRs across 74 repos,
and a day later 74 were still open.

So the preset puts a repo's fixes into one PR per package manager (`vulnerabilityAlerts.groupName`
and `groupSlug`): one PR, one CI run and one merge, on the first visit after its checks pass. Only
`vulnerabilityAlerts` can do this: Renovate applies it to each security update after the package
rules and over them (as with `semanticCommitType`), so a package rule's `groupName` can't. The
`groupSlug` must be set too, or the fixes would join `renovate/all-non-major`. A major fix still gets
its own PR (`renovate/major-security-<manager>`, `needs-human`). An OPA fix gets its own as well
(`renovate/security-gomod-opa`, a `groupSlug` template on the package name): a group auto-merges
only if every update in it does, and in `api` OPA never auto-merges, so it would hold every Go fix.

The trade-off: one bad fix keeps the group's other fixes from merging until it is fixed or dropped.
When the group changes (a fix added, or one no longer needed), Renovate updates the branch; PRs
opened before the grouping, one per package, are closed by Renovate as `autoclosed` on the repo's
next visit, since no update maps to their branches any more. The non-major group and the security
group can still conflict with each other on the 8th and 22nd, and then merge on consecutive
visits.

`rebaseWhen: "conflicted"` keeps the other open PRs from being rebased, with a CI run each, every
time `main` moves: Renovate's default `auto` acts as `behind-base-branch` with auto-merge on. A
branch is rebased only when it really conflicts with `main` (`go.mod` and `go.sum` conflicts are
textual, so they are caught). This works because the `ccf-required` ruleset doesn't require
branches to be up to date (`strict_required_status_checks_policy: false`), and a squash merge still
checks for conflicts when it merges. A PR's CI may then have run against an older `main` than the
one it merges into; the push to `main` runs CI again on the merged result, and if that breaks, the
repo's `notify-failure.yml` opens an incident, as for any red `main` (rule (b) in
[notify](notify.md)).

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
  The run takes shard `slot mod n`, where `slot` is the workflow's run number (`github.run_number`).
  The same slot scopes the token and builds the config, and a re-run keeps its number and its shard.
- The rotation is **per run**: each run takes the next shard, so each repo is visited every `n`
  runs. `renovate.yml` is scheduled **hourly** (`17 * * * *`), so that is every `n` hours when the
  runs are on time. GitHub runs scheduled workflows best-effort: a 06:17 cron in this repo has
  started as late as 15:29, and a busy hour can drop a run. A late or dropped run only slows the
  rotation; it never skips a shard, because the shard follows the runs, not the clock.
- A dispatch takes a run number too. A live dispatch on every active repo is simply the next step of
  the rotation. A dry run, or a live run with `repos` set, leaves the shard of its number unvisited,
  so that shard waits one more rotation.
- The preset's `prHourlyLimit: 1` lets each repo of the shard open at most one non-vulnerability
  PR per visit, and at most one per clock hour. The non-major group comes first, then majors one
  per visit.
- **Vulnerability fixes keep flowing.** Renovate exempts them from `prHourlyLimit` (and from
  `schedule` and `prCommitsPerRunLimit`; docs: "vulnerability alerts skip the line"), so each
  visit opens every fix a repo needs. They wait at most one rotation for their repo's shard.
- Dry runs write nothing, so they aren't sharded and take every selected repo.

Worst case for non-vulnerability PRs opened across the org, from `repos.yaml` (7 core repos, this
one included; batch 1 has 30 repos, batch 2 27, batch 3 10):

| Active batches | Active repos | Shards | Repos per shard | PRs in one run | Two runs close together | Each repo visited every |
| --- | --- | --- | --- | --- | --- | --- |
| none (today) | 7 | 1 | 7 | 7 | 7 (14 across the top of the hour) | 1 run |
| 1 | 37 | 3 | 13, 12, 12 | 13 | 25 | 3 runs |
| 1, 2 | 64 | 5 | 13, 13, 13, 13, 12 | 13 | 26 | 5 runs |
| 1, 2, 3 | 74 | 5 | 15, 15, 15, 15, 14 | 15 | 30 | 5 runs |

One run opens at most about 15, and a repo at most one per clock hour. Runs don't overlap (the `renovate`
concurrency group queues the next one), but GitHub can start two of them close together: a late
run followed by an on-time one, or a manual live dispatch next to a scheduled run. Two such runs
take different shards, so a short window can see about two shards' worth, ~30 with every batch
active. With one shard, both runs take the same repos: `prHourlyLimit` stops the second within the
same clock hour, but not across the top of the hour. Vulnerability PRs come on top of these and are
deliberately not capped. A live run's shard also keeps it within the app token's one-hour life.

On the 8th and 22nd (the preset's `schedule`), a repo gets its non-major group PR on its first visit
that day and a major on each later one. With 5 shards and 24 runs, that is about five visits a day,
fewer when GitHub delays or drops runs.

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
   action; check that `renovate-version` moves with it, and bump it by hand if it doesn't. The step
   has 55 minutes, within the token's hour.
5. A live run only, also after a failed or timed-out Renovate step: a second token, minted fresh
   because Renovate may have used most of the first one's hour, for the same repos (Contents, Pull
   requests and Workflows write, Checks read), then `go run ./cmd/renovate-config merge` with it:
   the [merge pass](#the-merge-pass), with `--wait 20m` and 25 minutes. The job's limit is 90 minutes.

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
A dispatch always follows its `dry_run` input. A live scheduled run is also what merges Renovate's green PRs (Renovate and the merge pass), so leave it on once
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

## The merge pass

Renovate merges at most one PR per repo per run ([Why security fixes are
grouped](#why-security-fixes-are-grouped)), and never one it has just pushed. So after each live
run, `renovate.yml` runs `renovate-config merge` on the run's shard. It shares its logic with
`ccf-bump merge` (`internal/automerge`; see [ccf-bump.md](ccf-bump.md#the-merge-pass) for how a
PR's checks are read and what is pending), and merges each open PR that is all of:

- labelled `renovate:automerge`: the preset adds it to what it auto-merges, the non-major group and
  non-major security fixes, and never to majors or OPA updates;
- from a `renovate/*` branch of the repo itself (not a fork), opened by `--author` (the workflow
  passes the token's app, `ccf-release-bot[bot]`);
- with every commit authored by `--author`: Renovate commits through the API as the app, so a commit
  by anyone else means a person pushed to the branch, and the PR is left for them (Renovate's own
  rule for a modified branch);
- not labelled `needs-human` (a person can hold a PR by adding it);

once the newest run of `ci / required` on its head succeeded and GitHub reports it mergeable. It
squash-merges as the bot, with the PR title plus ` (#N)` as the commit title, so the `ccf-review`
bypass applies as it does for Renovate's own merges. With `--wait 20m` it looks again every 30s at
PRs whose check is still running, for up to 20 minutes, then leaves the rest to a later run. A
failing or conflicting PR is a warning; Renovate rebases a conflicting one on its next visit.
Sibling PRs that touch the same files can conflict once one merges, so the pass merges the first
and the others wait for Renovate's rebase; that is why the security fixes are grouped too.

A repo's own `renovate.json` that turns `automerge` off for an update doesn't remove the label, so
it should add `needs-human` to that update as well.

Locally, `GH_TOKEN=... go run ./cmd/renovate-config merge --manifest repos.mock.yaml --repos
mock-plugin-1` prints what it would merge (`--dry-run` defaults to `full`; `off` merges).

## Running it

- **Dry run of a batch before it is switched on**:
  `gh workflow run renovate.yml -f repos=plugin-a,plugin-a-policies,... -f dry_run=full` with the
  batch's repos. The log lists each `DRY-RUN: Would create PR` per repo. A dry run isn't sharded and
  its read-only token lasts an hour, so dispatch a large batch in groups of about 15 repos.
- **Dry run on the mocks**: Actions, `renovate`, Run workflow, or
  `gh workflow run renovate.yml -f manifest=repos.mock.yaml -f dry_run=full`. The log lists each
  `DRY-RUN: Would create PR` per repo; expect about one grouped PR per repo plus its majors.
- **Live on the mocks**: the same with `-f dry_run=off`. Renovate opens the grouped PRs, and the
  merge pass merges the ones whose checks pass within its 20 minutes; a later live dispatch merges
  the rest. `repos.mock.yaml` lists this
  `workflows` repo too, so name the mocks with `-f repos=...` to leave it out.
- **Scheduled runs live**: set the repo variable `RENOVATE_LIVE` to `true`
  (`gh variable set RENOVATE_LIVE --body true --repo compliance-framework/workflows`); unset it to go
  back to dry runs.
- **Live on the product repos**: done (W1-S4-T03). Scheduled runs read `repos.yaml`, the product
  repos and this one, and run live while `RENOVATE_LIVE` is set. A Go-module update here is
  `fix(deps)` and makes a workflows release; `ccf-bump` moves the callers' pins on its next sync.

Locally, `go run ./cmd/renovate-config config --manifest repos.mock.yaml` prints the config the
workflow would use (the preset path defaults to `renovate/default.json`, run from the repo root).
