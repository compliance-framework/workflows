# workflows

Shared CI and release automation for the compliance-framework (CCF) repos.

## Purpose

This repo is the single home for:

- **Reusable GitHub Actions workflows** (`on: workflow_call`) that every CCF repo calls for CI and
  releases, so the rules live in one place instead of being copied into each repo.
- **Go tools** that drive releases across repos: `ccf-bump` (dependency bumps), `train` (the
  release train), `plugin-probe` and `repo-settings`. Later tasks add them.
- **The repo manifest** (`repos.yaml`), which lists the repos these workflows and tools act on,
  with each repo's kind and dependencies.

The plan for this work lives in `local-dev/docs/release-automation/`.

## Layout

| Path | What |
| --- | --- |
| `repos.yaml` | Manifest of the product repos: `name`, `kind`, `depends_on`, `release`, `charts`, plus top-level `holidays`, `include_patterns` and `exclude`. |
| `repos.mock.yaml` | The same schema for the `mock-*` repos, used to develop and test changes without touching product repos. |
| `internal/manifest` | Loads and validates a manifest; `Stages()` (release order) and `NextWorkingWeekday()`. |
| `internal/notify` | The CI-failure notification rules, dedupe key, Slack message and API clients. |
| `cmd/` | Go tools. `cmd/manifest` validates a manifest and prints its stages; `cmd/notify` is the logic behind `notify-failure.yml`. |
| `.github/workflows/` | This repo's own CI (`ci.yml`) and the reusable workflows (`ci-common.yml`, `notify-failure.yml`). |

### The manifest

```yaml
holidays: ["2026-12-25", "2027-01-01"]   # ISO dates that are not working days
include_patterns: []                     # repo name globs, used from workstream 3
exclude: []                              # repo names the globs must not pick up
repos:
  - name: agent
    kind: go-service          # go-service | go-plugin | go-lib | policies | ui | helm | action
    depends_on: [api, gooci]  # repos that release before this one
    release: true             # required; whether the release tooling acts on it
  - name: helm-charts
    kind: helm
    depends_on: [api, ui, agent]
    release: true
    charts: [ccf-agent, ccf-app]  # helm repos only
```

Loading rejects unknown fields and kinds, duplicate names, missing `release` values,
dependencies on unknown repos, cycles, and a non-helm repo depending on a helm repo.

`Stages()` groups repos into release stages. Every repo's dependencies are in earlier stages,
and each stage is sorted by name. Repos of kind `helm` always release last, after every
non-helm repo, because the charts pin the versions of everything else. For `repos.yaml`:

```console
$ go run ./cmd/manifest
stage 1: api gooci
stage 2: agent ui
stage 3: agent-action
stage 4: helm-charts
```

`NextWorkingWeekday(date)` returns `date` if it is a working weekday, and otherwise the next day
that is neither a weekend nor a manifest holiday (2027-01-01 becomes 2027-01-04).

Every tool takes `--manifest` (default `repos.yaml`), and every workflow takes a `manifest`
input, so the same code runs against `repos.mock.yaml`.

## How repos consume the workflows

A repo calls a reusable workflow from a thin workflow of its own, pinned to a tag of this repo:

```yaml
# .github/workflows/ci.yml in a consuming repo
name: ci
on:
  pull_request:
  push:
    branches: [main]
jobs:
  ci:
    uses: compliance-framework/workflows/.github/workflows/ci-go-service.yml@v1
    secrets: inherit
```

The next tasks add one CI workflow per `kind`, plus release workflows; the building blocks
below are in place. The mock repos adopt each one before the product repos do. Consumers
pin a major tag (`@v1`) or a full commit SHA, never `@main`.

## Reusable workflows

### `ci-common.yml`

The checks every repo runs, whatever its kind. The kind-specific CI workflows call it, so a
repo normally gets it through them rather than calling it directly.

| Job | Runs on | What |
| --- | --- | --- |
| `pr-title` | `pull_request` | [`amannn/action-semantic-pull-request`](https://github.com/amannn/action-semantic-pull-request): the PR title must be a conventional commit (`feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`, optional scope). |
| `vulns` | `pull_request` | osv-scanner's reusable PR workflow ([`osv-scanner-reusable-pr.yml`](https://github.com/google/osv-scanner-action)), nested as a job. It scans the base branch and the PR head and fails only on vulnerabilities the PR adds. Findings show as annotations, and are uploaded to code scanning except for PRs from forks. |
| `actionlint` | every event | [actionlint](https://github.com/rhysd/actionlint) v1.7.12 over `.github/workflows`, with the caller's `.github/actionlint.yaml` if it has one. |

| Input | Type | Default | What |
| --- | --- | --- | --- |
| `vulns-scan-args` | string | `-r` / `./` | osv-scanner arguments, one per line (not `--format` or `--output`). |
| `vulns-upload-sarif` | boolean | `true` | Upload the SARIF to code scanning (never for PRs from forks). |

No secrets. A called workflow can only use the token permissions its caller grants, and the
nested osv-scanner job asks for `security-events: write`, so the calling job must grant:

```yaml
permissions:
  actions: read
  contents: read
  pull-requests: read     # pr-title reads the current PR title
  security-events: write  # vulns uploads its SARIF
```

To re-check the title when it's edited, the caller listens for `edited` too:
`pull_request: {types: [opened, edited, synchronize, reopened]}`.

### `notify-failure.yml`

Posts a failed CI run to Slack with `chat.postMessage`. It posts when the run failed and:

- (a) it's for a pull request opened by `ccf-release-bot[bot]`, or for a branch whose name
  starts with `renovate/` or `ccf-bump/` (a pull request, or a push to that branch);
- (b) it's for a push to the default branch, and the previous completed push run of the same
  workflow on that branch passed (or there is none). Cancelled and skipped runs are stepped
  over, so a broken `main` posts once, when it breaks, not on every later push.

It posts at most once per repo + commit + workflow: after posting it saves an Actions cache
entry keyed on those three, and skips posting when the entry exists (re-runs, or another event
for the same commit). For pull requests the commit is the PR head, not the test merge commit.
Without a `SLACK_BOT_TOKEN` (a fork, or a repo outside the secret's scope) it does nothing and
succeeds.

The logic is `cmd/notify` (rules in `internal/notify`): the workflow checks out this repo at
`workflows-ref`, builds it, runs `notify plan` (decision and dedupe key), looks up the cache
entry, then runs `notify post`.

| Input | Type | Default | What |
| --- | --- | --- | --- |
| `channel` | string | `""` | Slack channel ID. Empty means the `SLACK_CHANNEL_CI_FAILURES` variable. |
| `workflows-ref` | string | `v1` | Ref of this repo to run `cmd/notify` from. A reusable workflow can't tell which ref it was called at, so set this to the same ref when you call the workflow at anything but `@v1` (a SHA, or a branch while developing). |

| Secret | Required | What |
| --- | --- | --- |
| `SLACK_BOT_TOKEN` | no | Bot token with `chat:write`, passed with `secrets: inherit`. |

The calling job decides that the run failed (`if: failure()`), and grants `actions: read`
(for rule (b)'s lookup of the previous run).

### Example caller

```yaml
# .github/workflows/ci.yml in a consuming repo
name: ci
on:
  pull_request:
    types: [opened, edited, synchronize, reopened]
  push:
    branches: [main]
permissions:
  contents: read
jobs:
  ci:
    uses: compliance-framework/workflows/.github/workflows/ci-go-service.yml@v1  # calls ci-common.yml
    permissions:
      actions: read
      contents: read
      pull-requests: read
      security-events: write
    secrets: inherit
  notify:
    needs: [ci]
    if: failure()
    uses: compliance-framework/workflows/.github/workflows/notify-failure.yml@v1
    permissions:
      actions: read
    secrets: inherit
```

A repo that calls `ci-common.yml` directly uses the same `permissions` block on that job.

## Development

CI (`.github/workflows/ci.yml`) runs `go test ./...`, `go vet ./...`, a `gofmt -l .` check and
`go run ./cmd/manifest` on both manifests, and calls this commit's `ci-common.yml` (pr-title,
vulns, and actionlint at the version pinned there). Run actionlint locally with
`go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`. Third-party actions are pinned by
full commit SHA, with the version in a comment; check a pin with
`gh api repos/<owner>/<repo>/git/matching-refs/tags/<tag>`.

## Legacy files

These files predate the repo's current purpose. A code search found no consumers. A later task
removes them, and until then they stay unchanged:

- `.github/workflows/build.yml`: builds the old assessment-runtime stack with Docker Compose
  (needs the `GH_DEPL_KEY_CONF_SVC` deploy-key secret).
- `.github/workflows/plugin-release.yml`: the old reusable plugin release workflow (GoReleaser
  and a `gooci` upload). `.github/actionlint.yaml` silences one shellcheck finding in it.
- `docker-compose.yml`: the stack that `build.yml` starts.
