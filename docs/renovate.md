# Renovate

Renovate owns every third-party dependency of the manifest's repos. `ccf-bump` owns every
`compliance-framework` one, so the two tools never touch the same version. Renovate runs
self-hosted from this repo (`renovate.yml`), with the `ccf-release-bot` app's token and the shared
preset in `renovate/default.json`. It needs no Mend subscription, and Actions minutes are free on
public repos.

## The preset: `renovate/default.json`

| Rule | Setting |
| --- | --- |
| Schedule | `at any time`: normal updates every day, on every visit. Until October 2026 they ran only on the 8th and 22nd (`* * 8,22 * *`), away from the monthly train (which starts on the month's first working weekday), so each update spent at least a week on `main` before the next release. But a repo then opened about one PR per visit on two days a month, and updates piled up "Awaiting Schedule" in its Dependency Dashboard ([ui#354](https://github.com/compliance-framework/ui/issues/354) had ~28, ~20 of them majors). With a daily schedule the remaining guard is the waiting period below: no update before its release is 7 days old, though an update merged the day before a train now ships in it. `ccf-bump`'s internal updates keep their own 8th/22nd schedule ([ccf-bump](ccf-bump.md)). |
| Vulnerability fixes | Any day (`vulnerabilityAlerts.schedule: at any time`), with no minimum age. `osvVulnerabilityAlerts: true` finds CVEs from osv.dev even without a Dependabot alert. A repo's fixes go into one `[SECURITY]` PR per package manager, `renovate/security-<manager>` (e.g. `renovate/security-gomod`), outside the non-major group; it auto-merges. A fix is never held by a PR limit: Renovate skips `prHourlyLimit` for vulnerability fixes, and `vulnerabilityAlerts` sets `prConcurrentLimit: 0` and `branchConcurrentLimit: 0` (no limit). Without these a fix would take the top-level `prConcurrentLimit: 8` (against the security PRs' own count), and a major fix, which also matches the majors rule, its `prConcurrentLimit: 3`: Renovate applies `vulnerabilityAlerts` over the package rules, and a branch takes the lowest limit of its updates, `0` meaning none. A major fix gets its own, `renovate/major-security-<manager>`, for a human. See [Why security fixes are grouped](#why-security-fixes-are-grouped). Every fix is `fix(deps)` (`vulnerabilityAlerts.semanticCommitType: "fix"`), so it releases: Renovate forces the `vulnerabilityAlerts` settings onto each security update over the package rules, while the depType rules alone still let one through as `chore(deps)`, which release-please hides ([mock-plugin-2#14](https://github.com/compliance-framework/mock-plugin-2/pull/14)). |
| Waiting period | `minimumReleaseAge: "7 days"` with `internalChecksFilter: "strict"`: no branch or PR before a release is a week old. An update with no release timestamp waits too (Renovate's default `minimumReleaseAgeBehaviour`), except a `docker` `minor`, `patch`, `digest` or `pinDigest` update, which goes ahead without one (`timestamp-optional`) and still waits when it has one (see [Why docker updates skip a missing timestamp](#why-docker-updates-skip-a-missing-timestamp)). A docker major keeps waiting for a timestamp. |
| Grouping | Every `minor`, `patch`, `digest`, `pin` and `pinDigest` update of a repo goes into one PR, `renovate/all-non-major` ("all non-major dependencies"), so a run costs one CI run per repo. The Go toolchain, the `go.mod` `go` directive and the `golang` image are in it, so a Go bump moves the `go` line and the builder image in the same PR. Majors get their own PRs. |
| Auto-merge | The non-major group (and every non-major vulnerability fix) auto-merges, but Renovate merges it itself (`platformAutomerge: false`, `automergeType: "pr"`, `automergeStrategy: "squash"`): on its next run after the checks pass, Renovate squash-merges the PR through the API as `ccf-release-bot`. These PRs are also labelled `renovate:automerge` (one rule, `addLabels`), and `renovate.yml`'s [merge pass](#the-merge-pass) merges them right after each live run, since Renovate merges only one per repo per run (it squash-merges with the same commit title and message Renovate's merge gets). Majors and OPA updates don't get the label: majors, and OPA in the api, wait for a human. See [Why Renovate merges, not GitHub](#why-renovate-merges-not-github). |
| Rebasing | `rebaseWhen: "conflicted"`: a branch is rebased only when it conflicts with `main`, not every time it falls behind. See [Why security fixes are grouped](#why-security-fixes-are-grouped). |
| Limits | `prHourlyLimit: 4`, `prConcurrentLimit: 8` per repo, and on the majors rule `prConcurrentLimit: 3` and `prHourlyLimit: 1`: at most 2 open majors next to the non-major group, and at most one new major per visit. Capped updates wait under "Rate-Limited" in the Dependency Dashboard, where a checkbox creates one past the limits. Vulnerability fixes are never capped. With a live run's shard of at most 15 repos, that bounds the PRs across the org; see [The org-wide throttle](#the-org-wide-throttle). |
| Commits | Semantic: `fix(deps)` for runtime dependencies (Go `require`, npm `dependencies`, a Dockerfile's final stage, the Go `toolchain` directive), which release-please releases, and `chore(deps)` for everything else (dev, CI, actions). A group takes the highest type of its updates, so a group with any runtime update is `fix(deps)`. Security fixes are always `fix(deps)`, whatever their dependency type (see Vulnerability fixes). |
| Go | `postUpdateOptions: ["gomodTidy"]`. Indirect modules are left to `go mod tidy` (Renovate's default). |
| Go directive | The `go.mod` `go` line (depType `golang`, datasource `golang-version`) moves to the latest Go, minor and patch alike (`matchManagers: ["gomod"], matchDepTypes: ["golang"], rangeStrategy: "bump"`). By default Renovate reads it as a minimum that every newer Go already meets and never moves it, so a repo stayed on the Go it started with while its `golang` builder image moved on. The update is `fix(deps)`: `setup-go` builds releases with the `go` line's Go, so a bump ships its standard-library fixes. It joins `renovate/all-non-major` with the `golang` image update, and keeps the 7-day wait: `golang-version` releases carry a date (go 1.27.3: 2026-10-19). Docker tags have none and don't wait, so the image is never behind the `go` line Renovate writes. When an org module raises its `go` line, `ccf-bump` moves its consumers' `go` line and `golang` images instead ([ccf-bump.md](ccf-bump.md#golang-builder-images)). |
| Indirect Go modules | When Renovate does update one, it is `fix(deps)`, not the `chore(deps)` that `depType: indirect` gets by default: indirect modules are linked into the binary, so the update must release (`matchManagers: ["gomod"], matchDepTypes: ["indirect"], semanticCommitType: "fix"`). A security fix to one is `fix(deps)` through `vulnerabilityAlerts` regardless of this rule. |
| Actions | Pinned by digest (`helpers:pinGitHubActionDigests`). Images in helm values and docker-compose files are tracked by Renovate's own managers, by tag. |
| Dockerfile images | Every `FROM` image is pinned by digest (`matchManagers: ["dockerfile"], pinDigests: true`): Renovate opens a `pinDigest` update that adds the digest of the current tag (`nginx:stable-alpine` becomes `nginx:stable-alpine@sha256:...`), then a `digest` update whenever upstream rebuilds that tag. A floating tag such as `nginx:stable-alpine`, `gcr.io/distroless/base-debian12` or `debian:bookworm-slim` never changes its name when upstream ships OS or runtime CVE fixes in a rebuild, so without a digest those fixes reach the image only when it happens to be rebuilt, unreviewed and with no PR to release them. With one, a rebuild is a PR: in the final stage it is `fix(deps)` (the `final` depType rule) and joins `renovate/all-non-major`, which auto-merges and releases; in a build stage it is `chore(deps)` in the same group. A docker update has no release timestamp to age against: a floating tag is no version to date, and Renovate's docker datasource gives Docker Hub tags (`golang`, `postgres`, `node`) none either (see [Why docker updates skip a missing timestamp](#why-docker-updates-skip-a-missing-timestamp)). So a sibling rule sets `minimumReleaseAgeBehaviour: "timestamp-optional"` on `docker` `minor`, `patch`, `digest` and `pinDigest` updates: one without a timestamp goes ahead, and one with a timestamp still waits 7 days. Internal images (`ghcr.io/compliance-framework/**`) stay off (Internal packages). |
| OPA | Off in every repo except `api`, so in agent, the policy repos and the plugins it follows the api through `ccf-bump`. In `api` it gets its own PR, `renovate/opa`, which never auto-merges, because a bump forces agent and the policies to follow. An OPA security fix gets its own security PR too, `renovate/security-gomod-opa`, so that in `api` it doesn't hold the other Go fixes (a group auto-merges only if every update in it does). Elsewhere Renovate still opens one, since it forces vulnerability fixes on over `enabled: false` (as for internal packages), and it auto-merges there. |
| Needs a human | Majors and the api's `renovate/opa` and `renovate/security-gomod-opa` PRs get the `needs-human` label (`addLabels`). See [attention.md](attention.md). |
| Internal packages | Off: `github.com/compliance-framework/**` (Go modules), `ghcr.io/compliance-framework/**` (images) and `compliance-framework/**` (actions and reusable workflows, such as this repo's). |

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
The `schedule` doesn't delay this: it is `at any time` now, and even when it was the 8th and 22nd
it only limited when new branches were created, while Renovate kept updating and merging existing
PRs on the other days (its defaults `updateNotScheduled: true` and
`automergeSchedule: ["at any time"]`, which the preset leaves alone). A PR whose checks fail stays
open, and Renovate updates or recreates it on later runs as usual. The repo's "allow auto-merge" setting is no longer used by Renovate.

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
group can still conflict with each other when both are open, and then merge on consecutive
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

### Why docker updates skip a missing timestamp

With `minimumReleaseAge` and `internalChecksFilter: "strict"`, Renovate (44.145.1) marks an update
that has no release timestamp as pending under its default `minimumReleaseAgeBehaviour`
(`timestamp-required`), and a pending update never gets a branch: it is dropped from its group
("Branch is not pending, removing pending upgrades"). Docker updates have none. A floating
tag (`stable-alpine`, `base-debian12`, `bookworm-slim`) is no version, so Renovate has nothing to
date its `digest` updates with; and the docker datasource gives a versioned Docker Hub tag
(`golang:1.26.1`, `postgres:17`, `node:22`) no timestamp either. A dry run in October 2026 first
blamed Docker Hub's tag list (`403` on page 11 of
`hub.docker.com/v2/repositories/library/golang/tags`, then the registry's tag list, which has no
dates); either way every docker update had `releaseTimestamp: null`.

That held two kinds of update for good:

- **Digests.** In a dry run (`--dry-run=lookup` on api and ui) the `pinDigest` updates of
  `golang:1.26` and `node:22` were `pendingChecks: true` ("pinDigest update of golang has no
  releaseTimestamp to age against"), and only those of `nginx:stable-alpine` and
  `gcr.io/distroless/base-debian12` went through, because Renovate skips the age check for a
  `pinDigest` of an unversioned tag. Their later `digest` updates get no such skip, so the rebuilds
  the pinning is for would never have opened a PR.
- **Tags.** agent's `golang:1.26.1` had `1.26.2` to `1.27.3` all pending ("Marking 13 release(s) as
  pending, as they do not have a releaseTimestamp"), and api's `postgres` 17.x and 18.x too: the
  Dependency Dashboard listed the update, but `renovate/all-non-major` never got it. In agent the
  pending `1.27.3` also took the `pinDigest` of the same `FROM` line with it ("Ignoring upgrade
  collision"), so the image got neither.

So a rule sets `minimumReleaseAgeBehaviour: "timestamp-optional"` for `docker` `minor`, `patch`,
`digest` and `pinDigest` updates: an update without a timestamp goes ahead (Renovate logs a
warning), and one that has a timestamp still waits 7 days. That is preferred to
`minimumReleaseAge: null`, which would drop the wait for those updates even when a timestamp
exists. Docker majors keep `timestamp-required`, and every other datasource keeps it too. The
`golang-version` datasource behind the `go` directive has dates, so it needs no exception.

The same lookup on agent with the rule (and the `go` directive rule):

| `renovate/all-non-major` gets | before | after |
| --- | --- | --- |
| `golang` `1.26.1` → `1.27.3` (`Dockerfile`, `Dockerfile-ci`, `Dockerfile-custodian`) | no: pending | yes, pinned at `sha256:2920bb124f1a…` |
| `go.mod` `go 1.26.1` → `1.27.3` (released 2026-10-19) | no update found | yes |

It extends `config:recommended`, which adds the Dependency Dashboard issue (where majors and pending
updates are listed) and Renovate's standard monorepo groups.

A repo may add its own `renovate.json` for overrides only: Renovate applies the preset to every
listed repo as its base, and the repo's file is merged on top. Repos need no onboarding PR.

Validate the preset after editing it, as a preset and as global config:

```sh
npx --yes --package renovate -- renovate-config-validator --strict --no-global renovate/default.json
npx --yes --package renovate -- renovate-config-validator --strict renovate/default.json
```

### This repo's `renovate.json`

This repo has one, for a version only this repo holds, tracked by a regex custom manager: the
osv-scanner image that [image-scan](image-scan.md) runs, the Go constant
`ScannerImage` in `internal/imagescan/scanner.go` (`ghcr.io/google/osv-scanner:<tag>@sha256:<digest>`).
A Go string is nothing to Renovate's managers, so a manager on that file reads its image, tag and
digest as a `docker` dependency, and Renovate moves both in the non-major group.
`TestScannerImageRenovate` checks the manager finds exactly the constant. The file is merged on
top of the preset as any repo's would be (`requireConfig: "optional"`) and sets nothing else.
Validate it like the preset: `renovate-config-validator --strict --no-global renovate.json`.

### Workflow YAML must be valid YAML 1.2

Renovate's github-actions manager finds `uses:` lines with a regex, but everything else it reads
from the parsed workflow: the versions in `with:` (`goreleaser-action`'s `version`,
`golangci-lint-action`'s `version`, `setup-node`'s `node-version`, `chart-testing`), job containers,
services and runners. Its YAML parser is strict YAML 1.2, and when a file doesn't parse, Renovate
drops all of those for the file without a warning, while still updating its actions. GitHub and Go's
yaml are laxer: until October 2026, ten release workflows had `"the caller\'s ..."` in an input
description, which GitHub accepts but YAML 1.2 doesn't (`\'` is no escape in double quotes; write
`'`), so Renovate bumped goreleaser in the CI workflows but not in `release-go-lib.yml` and
`release-go-plugin.yml`, and `TestGoreleaserPins` failed on its PR (#146). `TestGitHubYAMLEscapes`
keeps `\'` out of every YAML file under `.github`.

## The org-wide throttle

`prHourlyLimit` is per repo in Renovate (each repo counts the PRs it opened since the top of the
hour), so on its own it can't bound the org's PRs an hour: with 74 repos active, even `1` would
allow 74 (the original plan aimed at ~15 an hour with `prHourlyLimit: 1`; the daily schedule
trades that for `4`, with majors capped apart, see below). Renovate 44.145.1 (the pinned version)
has one global limit, the self-hosted `prCommitsPerRunLimit`, but it counts commits per run rather
than PRs per hour, counts vulnerability fixes and rebases, and when it is reached Renovate stops
processing the remaining repos for that run. Those repos then get no vulnerability fixes and no auto-merges until a
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
- The preset's `prHourlyLimit: 4` lets each repo of the shard open at most 4 non-vulnerability
  PRs per visit, and at most 4 per clock hour. Renovate (44.145.1) compares every limit with a
  repo-wide count: `prHourlyLimit` with every Renovate PR the repo opened since the top of the hour,
  vulnerability fixes included, and `prConcurrentLimit` with every open non-vulnerability Renovate
  PR. A limit set in a package rule applies to the branches it matches (a branch takes the lowest
  limit of its updates, `0` meaning none). Renovate works the non-major branches before the majors
  in each visit, so the non-major group comes first.
- **Majors are capped.** The majors rule sets `prHourlyLimit: 1`, so a major opens only in a clock
  hour in which the repo has opened no other Renovate PR yet: at most one per visit, and none in the
  visit that opens the non-major group or a security PR (it waits for the next visit). It also sets
  `prConcurrentLimit: 3`, so a major opens only while the repo has fewer than 3 open
  non-vulnerability Renovate PRs: at most 2 open majors while the non-major group is open (3 once
  it merges), one fewer in the api while its `renovate/opa` PR is open. A human merging or closing
  a major frees a slot for the next one. The top-level `prHourlyLimit: 4` leaves the non-major group
  room even when a major and security PRs were opened earlier in the same hour (a dispatch next to
  a scheduled run). Capped updates are listed under "Rate-Limited" in the repo's Dependency
  Dashboard, with a checkbox that creates the PR on the next visit, past the limits.
- **Vulnerability fixes keep flowing.** Renovate exempts them from `prHourlyLimit` (and from
  `schedule` and `prCommitsPerRunLimit`; docs: "vulnerability alerts skip the line") and counts
  their open PRs apart from the others, and the preset's `vulnerabilityAlerts` sets
  `prConcurrentLimit` and `branchConcurrentLimit` to `0` (no limit), so a major fix doesn't take the
  majors rule's cap. Each visit opens every fix a repo needs. They wait at most one rotation for
  their repo's shard.
- Dry runs write nothing, so they aren't sharded and take every selected repo.

Worst case for non-vulnerability PRs opened across the org, from `repos.yaml` (7 core repos, this
one included; batch 1 has 30 repos, batch 2 27, batch 3 10):

| Active batches | Active repos | Shards | Repos per shard | PRs in one run (of them majors) | Two runs close together | Each repo visited every |
| --- | --- | --- | --- | --- | --- | --- |
| none | 7 | 1 | 7 | 28 (7) | 28 (56 across the top of the hour) | 1 run |
| 1 | 37 | 3 | 13, 12, 12 | 52 (13) | 104 | 3 runs |
| 1, 2 | 64 | 5 | 13, 13, 13, 13, 12 | 52 (13) | 104 | 5 runs |
| 1, 2, 3 (today) | 74 | 5 | 15, 15, 15, 15, 14 | 60 (15) | 120 | 5 runs |

These are bounds: `prHourlyLimit` (4) per repo of the shard, at most one of them a major. Since a
major opens only in an hour with no other Renovate PR, a visit in practice opens either the
non-major group (with, in the api, `renovate/opa`) or one major, so a run opens about one PR per
repo with an update due. Runs don't overlap (the `renovate` concurrency group queues the next one),
but GitHub can start two of them close together: a late run followed by an on-time one, or a
manual live dispatch next to a scheduled run. Two such runs take different shards, so a short
window can see two shards' worth. With one shard, both runs take the same repos: `prHourlyLimit`
stops the second within the same clock hour, but not across the top of the hour. Vulnerability PRs
come on top of these and are deliberately not capped. A live run's shard also keeps it within the
app token's one-hour life.

Every day, a repo gets its non-major group PR on the first visit that finds a non-major update due
(its release 7 days old), and a major on each later visit while fewer than 3 of its
non-vulnerability Renovate PRs are open. With 5 shards and 24 runs, that is about five visits a
day, fewer when GitHub delays or drops runs. On the old 8th/22nd `schedule` a repo had about ten
visits a month to open anything, one PR each.

## The workflow: `renovate.yml`

Runs hourly at minute 17 and on `workflow_dispatch`, as three jobs:

1. **`select`**: `go run ./cmd/renovate-config list` selects the repos from the manifest (every
   active repo, this `workflows` repo included, or the `repos` subset) and fails on an empty
   selection. A live run then keeps only one shard of the selection ([The org-wide
   throttle](#the-org-wide-throttle)). `go run ./cmd/image-scan list` then picks the shard's repos
   that have Dockerfiles (kinds `go-service`, `ui` and `action`). The job outputs both lists and the
   slot.
2. **`image-scan`**, when the shard has image repos: scans their container images and writes the
   Renovate package rules that fix what a newer base image fixes, with a `ccf-release-bot` token
   scoped to those repos, on dry runs too ([image-scan.md](image-scan.md)). It uploads the rules as
   the `image-scan-overrides` artifact and writes its report to the step summary. A live run also
   opens or updates each repo's `image-scan/os-patches` PR for OS package fixes the base image
   doesn't ship yet (Contents and Pull requests write; a dry run's token reads only).
3. **`renovate`**, once `select` succeeded, whether `image-scan` succeeded, failed or was skipped:
   1. It downloads the `image-scan-overrides` artifact when there is one.
   2. `go run ./cmd/renovate-config config` writes the Renovate global config: the preset's settings
      plus `platform: github`, `autodiscover: false`, `repositories` (the selection, as
      `compliance-framework/<name>`), `onboarding: false`, `requireConfig: "optional"` and `dryRun`.
      It refuses a preset that sets any of those itself. With `--overrides`, image-scan's rules are
      appended after the preset's `packageRules`, so they win; the file is checked strictly
      ([image-scan.md](image-scan.md#how-renovate-gets-the-rules)), and a missing one adds none.
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

The scan runs before Renovate, so a run takes up to its 50 minutes longer; a run still going when
the next is due delays that one (the `renovate` concurrency group queues it), which slows the
rotation but never skips a shard.

| Input | Default | What |
| --- | --- | --- |
| `repos` | every active repo in the manifest | Comma-separated subset; each must be in the manifest. A live run takes only active repos, and one shard of at most 15 of them. A dry run may name repos of an inactive batch, to check the batch before it is switched on ([manifest.md](manifest.md#switching-a-batch-on)). |
| `dry_run` | `full` | Renovate's `dryRun`: `full` logs every branch and PR it would create, `lookup` stops after finding updates, `extract` after reading the package files. `off` runs live. |
| `log_level` | `info` | `debug` for a full trace. |

A scheduled run has no inputs and uses these defaults, except `dry_run`: it is `off` (live) when the
repo variable `RENOVATE_LIVE` is `"true"`, and `full` otherwise. Every run reads `repos.yaml`: the
product repos and this one. With `RENOVATE_LIVE` set, a scheduled run is
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
- from a `renovate/*` branch of the repo itself (not a fork), or an `image-scan/*` one (image-scan's
  [OS package patches](image-scan.md#os-package-patches)), opened by `--author` (the workflow
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

Locally, `GH_TOKEN=... go run ./cmd/renovate-config merge --repos plugin-github-settings` prints
what it would merge (`--dry-run` defaults to `full`; `off` merges).

## Running it

- **Dry run of a batch before it is switched on**:
  `gh workflow run renovate.yml -f repos=plugin-a,plugin-a-policies,... -f dry_run=full` with the
  batch's repos. The log lists each `DRY-RUN: Would create PR` per repo. A dry run isn't sharded and
  its read-only token lasts an hour, so dispatch a large batch in groups of about 15 repos.
- **Dry run of a preset change**: Actions, `renovate`, Run workflow, or
  `gh workflow run renovate.yml -f repos=api,ui -f dry_run=full`. The log lists each
  `DRY-RUN: Would create PR` per repo; expect about one grouped PR per repo plus its majors. There
  is no mock test bed: dry runs and unit tests are the check.
- **Scheduled runs live**: set the repo variable `RENOVATE_LIVE` to `true`
  (`gh variable set RENOVATE_LIVE --body true --repo compliance-framework/workflows`); unset it to go
  back to dry runs.
- **Live on the product repos**: done (W1-S4-T03). Scheduled runs read `repos.yaml`, the product
  repos and this one, and run live while `RENOVATE_LIVE` is set. A Go-module update here is
  `fix(deps)` and makes a workflows release; `ccf-bump` moves the callers' pins on its next sync.

Locally, `go run ./cmd/renovate-config config` prints the config the
workflow would use (the preset path defaults to `renovate/default.json`, run from the repo root).
