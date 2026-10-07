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
| `cmd/` | Go tools. `cmd/manifest` validates a manifest and prints its stages. |
| `.github/workflows/` | This repo's own CI (`ci.yml`) and, from later tasks, the reusable workflows. |

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

The next tasks add the reusable workflows: one CI workflow per `kind`, plus release and
notification workflows. The mock repos adopt each one before the product repos do. Consumers
pin a major tag (`@v1`) or a full commit SHA, never `@main`.

## Development

CI (`.github/workflows/ci.yml`) runs `go test ./...`, `go vet ./...`, a `gofmt -l .` check,
`go run ./cmd/manifest` on both manifests, and
`go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`. Third-party actions are pinned by
full commit SHA, with the version in a comment.

## Legacy files

These files predate the repo's current purpose. A code search found no consumers. A later task
removes them, and until then they stay unchanged:

- `.github/workflows/build.yml`: builds the old assessment-runtime stack with Docker Compose
  (needs the `GH_DEPL_KEY_CONF_SVC` deploy-key secret).
- `.github/workflows/plugin-release.yml`: the old reusable plugin release workflow (GoReleaser
  and a `gooci` upload). `.github/actionlint.yaml` silences one shellcheck finding in it.
- `docker-compose.yml`: the stack that `build.yml` starts.
