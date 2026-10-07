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
      contents: read
    secrets: inherit
```

The next tasks add one CI workflow per `kind`, plus release workflows; the building blocks
below are in place. The mock repos adopt each one before the product repos do. Consumers
pin a major tag (`@v1`) or a full commit SHA, never `@main`. The `permissions` above are the
ones `ci-common.yml` and `notify-failure.yml` need (see below).

## Reusable workflows

### `ci-common.yml`

The checks every repo runs, whatever its kind; the kind-specific CI workflows call it.

| Job | Events | What |
| --- | --- | --- |
| `pr-title` | `pull_request` | [`amannn/action-semantic-pull-request`](https://github.com/amannn/action-semantic-pull-request): the title is a conventional commit (`feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`). |
| `vulns` | `pull_request` | osv-scanner's reusable PR workflow, nested as a job: it scans the base branch and the PR head, and fails only on vulnerabilities the PR adds. Findings show as annotations and, except for PRs from forks, in code scanning. |
| `actionlint` | all | [actionlint](https://github.com/rhysd/actionlint) at the version pinned there, with the caller's `.github/actionlint.yaml` if any. |

No inputs or secrets. A called workflow gets only the token permissions its caller grants,
and the nested osv-scanner job asks for `security-events: write`, so the calling job must
grant `actions: read`, `contents: read`, `pull-requests: read` and `security-events: write`
(see the example). To re-check edited titles, the caller also listens for `edited`.

### `notify-failure.yml`

Posts a failed run to Slack (`chat.postMessage`) when:

- (a) it's for a pull request opened by `ccf-release-bot[bot]`, or for a branch starting with
  `renovate/` or `ccf-bump/`;
- (b) it's for a push to the default branch, and the previous completed push run of the same
  workflow there passed, or there is none (cancelled and skipped runs are stepped over). A
  broken `main` posts when it breaks, not on every later push.

It posts once per repo + commit (the PR head for pull requests) + workflow: after posting it
saves an Actions cache entry with that key, and skips the post when the entry exists. Without
`SLACK_BOT_TOKEN` (forks, repos outside the secret's scope) it does nothing and succeeds. The
logic is `cmd/notify` (rules in `internal/notify`), built from this repo at `workflows-ref`.

| Input | Default | What |
| --- | --- | --- |
| `channel` | `""` | Slack channel ID; empty means the `SLACK_CHANNEL_CI_FAILURES` variable. |
| `workflows-ref` | `v1` | Ref of this repo to build `cmd/notify` from. A reusable workflow can't see the ref it was called at, so pass the same ref when calling it at anything but `@v1`. |

Secret: `SLACK_BOT_TOKEN` (optional, `chat:write`), via `secrets: inherit`. The calling job
runs `if: failure()` after the CI jobs and grants `actions: read` and `contents: read`.

## Development

CI (`.github/workflows/ci.yml`) runs `go test ./...`, `go vet ./...`, a `gofmt -l .` check,
`go run ./cmd/manifest` on both manifests, and actionlint (at the version pinned in `ci.yml`).
Third-party actions are pinned by full commit SHA, with the version in a comment.

## Legacy files

These files predate the repo's current purpose. A code search found no consumers. A later task
removes them, and until then they stay unchanged:

- `.github/workflows/build.yml`: builds the old assessment-runtime stack with Docker Compose
  (needs the `GH_DEPL_KEY_CONF_SVC` deploy-key secret).
- `.github/workflows/plugin-release.yml`: the old reusable plugin release workflow (GoReleaser
  and a `gooci` upload). `.github/actionlint.yaml` silences one shellcheck finding in it.
- `docker-compose.yml`: the stack that `build.yml` starts.
