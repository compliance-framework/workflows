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
| `internal/ciworkflows` | Tests that run the CI workflows' shell steps locally against fixtures. |
| `internal/release` | The release rules: the release-please PR checks. |
| `cmd/` | Go tools. `cmd/manifest` validates a manifest and prints its stages; `cmd/notify` is the logic behind `notify-failure.yml`; `cmd/release` runs the `internal/release` rules for the release workflows. |
| `.github/workflows/` | This repo's own CI (`ci.yml`) and the reusable workflows (`ci-common.yml`, `notify-failure.yml`, the `ci-<kind>.yml` kind CI workflows and the [release workflows](#release-workflows)). |
| `release-please/defaults.json` | The release-please settings every repo's `release-please-config.json` copies. |
| `.golangci.yml`, `.regal/config.yaml` | Shared lint base configs, used by the CI workflows when the calling repo has none. |

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

Each `kind` gets its own CI workflow (`ci-<kind>.yml`, see [Kind CI workflows](#kind-ci-workflows)),
plus release workflows later. The mock repos adopt each one before the
product repos do. Consumers pin a major tag (`@v1`) or a full commit SHA, never `@main`.
The `permissions` above are the ones `ci-common.yml` and `notify-failure.yml` need (see
below).

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

### Kind CI workflows

Each calls `ci-common.yml` and ends in a job named `required`, the one status check a repo
needs to require (branch protection shows it as `<caller job> / required`). `required` runs
with `if: always()`, needs every other job, and fails unless all of them succeeded. None of
those jobs is conditional, so a skipped job also fails it; the `pull_request`-only jobs inside
`ci-common.yml` don't count, because `common` still succeeds when they skip. Within a job,
every check runs even if an earlier one failed, so one run reports every problem. Callers
grant the permissions `ci-common.yml` needs (see the example above); none takes secrets.
Tool versions are pinned in the workflows.

`ci-common.yml` is called as `./.github/workflows/ci-common.yml`. In a called workflow, a
local reference means this repo at the same commit as the calling workflow file, not the
top-level caller's repo, so a caller pinned to `@v1` or a SHA gets the matching
`ci-common.yml` ("the called workflow is from the same commit as the caller workflow",
[Reusing workflows](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows)).
The shared lint configs are fetched the same way, from `job.workflow_repository` at
`job.workflow_sha` (the repo and commit of the workflow file defining the job; actionlint
v1.7.12 doesn't know these yet, so `.github/actionlint.yaml` silences that one message).

#### `ci-go-plugin.yml` (kind `go-plugin`)

Go comes from the caller's `go.mod`.

| Job | Checks |
| --- | --- |
| `golangci-lint` | golangci-lint (version pinned in the workflow) with the repo's config, or this repo's `.golangci.yml` when it has none. |
| `go` | `gofmt -l .` (prints the diff), `go mod tidy` leaves `go.mod`/`go.sum` unchanged, `go test ./...`. |
| `goreleaser` | GoReleaser (version pinned in the workflow): `goreleaser check` (deprecated properties only warn), `goreleaser build --snapshot --clean --single-target`. |

| Input | Default | What |
| --- | --- | --- |
| `lint-new-from-merge-base` | `true` | Report only issues the change adds: `--new-from-merge-base=origin/<base>` on PRs, `--new-from-rev=<before>` on pushes (everything when there is no such commit). `false` lints everything. |

#### `ci-policies.yml` (kind `policies`)

| Job | Checks |
| --- | --- |
| `opa` | `opa fmt --list --fail` (prints the diff), `opa check --strict`, `opa test`, `opa build --bundle`. |
| `regal` | `regal lint` with the repo's `.regal/config.yaml` (or `.regal.yaml`), or this repo's `.regal/config.yaml` when it has none. That base config ignores CCF layout rules (package/directory mismatch, tests in the policy's package, no entrypoint, line length, and `opa-fmt`, which the `opa` job checks) and makes idiomatic, performance and style findings warnings. |

| Input | Default | What |
| --- | --- | --- |
| `directory` | `policies` | The policies directory (the bundle root). |
| `opa-version` | `1.14.1` | OPA version. |
| `regal-version` | `0.43.0` | Regal version. |

A caller is the example above with the kind's workflow in `uses:`, for example:

```yaml
  ci:
    uses: compliance-framework/workflows/.github/workflows/ci-policies.yml@v1
    with:
      directory: policies  # the default; inputs are optional
    permissions:
      actions: read
      contents: read
      pull-requests: read
      security-events: write
```

The snippets below show only the `uses:` and `with:` keys of that `ci` job.

#### `ci-go-service.yml` (kind `go-service`)

| Job | Checks |
| --- | --- |
| `golangci-lint` | As in `ci-go-plugin.yml` (same job and `lint-new-from-merge-base` input). |
| `go` | `gofmt -l .`, then the prepare command, `go vet ./...`, `go mod tidy` leaves `go.mod`/`go.sum` unchanged, `go test -race ./...`, `go build ./...`. |
| `make` | The prepare command, then each of `make-targets` (all run even if one fails). With no targets it does nothing and succeeds. |

| Input | Default | What |
| --- | --- | --- |
| `make-targets` | `""` | Extra `make` targets, space-separated. |
| `prepare-command` | `""` | Shell command run first in `go` (after gofmt) and `make`. If it fails, the later checks are skipped. |

```yaml
    uses: compliance-framework/workflows/.github/workflows/ci-go-service.yml@v1
    with:  # api; agent passes make-targets: check-opa-version
      prepare-command: make swag
      make-targets: test-integration check-diff
```

#### `ci-go-lib.yml` (kind `go-lib`)

| Job | Checks |
| --- | --- |
| `golangci-lint` | As in `ci-go-plugin.yml` (same job and `lint-new-from-merge-base` input). |
| `go` | `gofmt -l .`, `go test ./...`. |
| `goreleaser` | `goreleaser check` (deprecated properties only warn). |

Caller: `uses: compliance-framework/workflows/.github/workflows/ci-go-lib.yml@v1`.

#### `ci-ui.yml` (kind `ui`)

One `node` job: Node from the repo's `.nvmrc` (else `node-version`), `npm ci`, then
`npm run lint` (ESLint fails on errors, not warnings, unless the script sets
`--max-warnings`), `npm run format:check`, `npm run type-check`, `npm run <test-script>`,
`npm run build` and the extra command.

| Input | Default | What |
| --- | --- | --- |
| `node-version` | `20` | Node version when the repo has no `.nvmrc`. |
| `test-script` | `test` | npm script that runs Vitest. |
| `extra-command` | `""` | Shell command run last, e.g. a drift check. |

```yaml
    uses: compliance-framework/workflows/.github/workflows/ci-ui.yml@v1
    with:  # ui
      test-script: test:unit
      extra-command: scripts/sync-agentconfig-conformance.sh --check main
```

#### `ci-helm.yml` (kind `helm`)

| Job | Checks |
| --- | --- |
| `helm` | `helm lint` on every chart; [kubeconform](https://github.com/yannh/kubeconform) `-strict` on each chart rendered with its default values; each of `make-targets` (the chart unit tests). |
| `ct` | `ct lint --all --check-version-increment=false`, plus `--config ct.yaml` when the repo has a `ct.yaml` (chart-testing-action points ct's config search at its own install dir, so ct would not find the repo's file otherwise). release-please owns chart versions, and `ccf-bump` PRs change only `appVersion` and image tags, so no version bump is required. |

| Input | Default | What |
| --- | --- | --- |
| `charts-dir` | `charts` | Directory with one subdirectory per chart. |
| `make-targets` | `""` | `make` targets that run the chart unit tests, space-separated. |

```yaml
    uses: compliance-framework/workflows/.github/workflows/ci-helm.yml@v1
    with:
      make-targets: helm.test
```

#### `ci-action.yml` (kind `action`)

| Job | Checks |
| --- | --- |
| `hadolint` | [hadolint](https://github.com/hadolint/hadolint) on every tracked `Dockerfile*`, with the repo's `.hadolint.yaml` if any. |
| `docker` | `docker build --file <dockerfile> <context>`. |

actionlint runs in `ci-common.yml`. Inputs: `dockerfile` (default `Dockerfile`) and `context`
(default `.`). Caller: `uses: compliance-framework/workflows/.github/workflows/ci-action.yml@v1`.

### Release workflows

Releases are driven by [release-please](https://github.com/googleapis/release-please) in
manifest mode: each repo has a `release-please-config.json` and a
`.release-please-manifest.json` (package path to current version) at its root.

#### `release-please.yml`

Runs `googleapis/release-please-action`, which opens or updates the release PR
(`release-please--branches--<branch>`) and, once it merges, tags the release and creates the
GitHub release. It runs as ccf-release-bot, so the PR and the release trigger the caller's
other workflows (GitHub doesn't start workflows for `GITHUB_TOKEN` events). The token comes
from `actions/create-github-app-token` with `owner: compliance-framework` and
`repositories: <the caller repo>`, and only `contents`, `pull-requests` and `issues` write.
The bot is installed on every org repo, so the job refuses to mint a token when the repo name
is empty or `github.repository` isn't `compliance-framework/<that name>`: a token minted with
an owner and no repositories would reach the whole org.

Secrets: `RELEASE_BOT_APP_ID` and `RELEASE_BOT_PRIVATE_KEY`, via `secrets: inherit`. Outputs:
`releases-created` and `paths-released` (JSON list of package paths), from the action.

**Shared defaults.** `release-please/defaults.json` holds the settings every repo uses:
`bump-minor-pre-major: true` (breaking changes bump the minor before 1.0), and changelog
sections where `feat`, `fix`, `perf`, `revert` and `deps` are shown and `chore`, `ci`,
`docs`, `test`, `refactor` and `build` are hidden. release-please has no remote `extends`,
and it reads its config through the API from the target branch, so the workflow can't merge
the defaults in at runtime. Callers copy every key of `defaults.json` into their
`release-please-config.json` and add `packages` (and any other keys); on each run the
workflow compares the caller's config with the defaults at the same commit as the workflow
and prints a warning for each key that differs.

```json
{
  "$schema": "...", "bump-minor-pre-major": true, "changelog-sections": ["... from defaults.json ..."],
  "packages": { ".": { "release-type": "go" } }
}
```

```yaml
# .github/workflows/release-please.yml in a consuming repo
name: release-please
on:
  push:
    branches: [main]
permissions:
  contents: read
concurrency:
  group: release-please
jobs:
  release-please:
    uses: compliance-framework/workflows/.github/workflows/release-please.yml@v1
    permissions:
      contents: read
    secrets: inherit
```

#### `release-checks.yml`

One `release-checks` job that runs only on PRs whose head branch starts with
`release-please--`; on any other PR it is skipped, which a required status check counts as
passing, so repos can call it from their CI workflow and require it. Each check runs even if
an earlier one failed (rules in `internal/release`):

| Check | Fails when |
| --- | --- |
| `internal-deps-final` | A `github.com/compliance-framework/*` requirement in `go.mod` (the `mock-*` repos included) isn't a final semver (a pre-release or pseudo-version), or `go.mod` replaces one. |
| `version-guard` | A package's major version is higher in the PR's `.release-please-manifest.json` than at the base (0.x to 1.0 included; a new package counts from 0.0.0), and the PR lacks the `release:major-approved` label. |
| `go-module-path` | The module path's major suffix doesn't match the root package's (`.`) new version: none for v0 and v1, `/vN` for vN with N >= 2. |

Repos without a root `go.mod` skip the two Go checks. The labels come from the event, so
the caller listens for `labeled` and `unlabeled` to re-check after adding the label. The job
needs `contents: read`; no inputs or secrets.

```yaml
# in the consuming repo's ci.yml (on: pull_request types include labeled, unlabeled)
  release-checks:
    uses: compliance-framework/workflows/.github/workflows/release-checks.yml@v1
    permissions:
      contents: read
```

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
