# workflows

Shared CI and release automation for the compliance-framework (CCF) repos.

## Purpose

This repo is the single home for:

- **Reusable GitHub Actions workflows** (`on: workflow_call`) that every CCF repo calls for CI and
  releases, so the rules live in one place instead of being copied into each repo.
- **Go tools** that drive releases across repos: `ccf-bump` (dependency bumps), `train` (the
  release train), `plugin-probe` and `repo-settings` (see [Repo settings](#repo-settings)).
  Later tasks add the others.
- **The repo manifest** (`repos.yaml`), which lists the repos these workflows and tools act on,
  with each repo's kind and dependencies.

The plan for this work lives in `local-dev/docs/release-automation/`.

## Layout

| Path | What |
| --- | --- |
| `repos.yaml` | Manifest of the product repos: `name`, `kind`, `depends_on`, `release`, `charts`, plus top-level `holidays`, `include_patterns` and `exclude`. |
| `repos.mock.yaml` | The same schema for the `mock-*` repos, used to develop and test changes without touching product repos. |
| `internal/manifest` | Loads and validates a manifest; `Stages()` (release order) and `NextWorkingWeekday()`. |
| `internal/notify` | The CI incident rules (which runs, incident key, transitions), the Slack messages and the Slack client ([docs/notify.md](docs/notify.md)). |
| `internal/ciworkflows` | Tests that run the CI workflows' shell steps locally against fixtures. |
| `internal/reposettings` | The desired repo settings and rulesets, the current-vs-desired diff, and the GitHub client [`repo-settings.yml`](#repo-settings) uses. |
| `internal/release` | The release rules: the release-please PR checks, the next release-candidate tag, the preview tags, the release tags and the chart a helm release tag is for. |
| `cmd/` | Go tools. `cmd/manifest` validates a manifest and prints its stages; `cmd/notify` is the logic behind `notify-failure.yml`; `cmd/release` runs the `internal/release` rules for the release workflows; `cmd/repo-settings` syncs repo settings. |
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
    if: always()
    uses: compliance-framework/workflows/.github/workflows/notify-failure.yml@v1
    with:
      needs: ${{ toJSON(needs) }}
    permissions:
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

Keeps one Slack thread per CI incident (a pull request, or a branch for pushes) for release-bot
PRs, `renovate/` and `ccf-bump/` branches and the default branch: the first failure posts a
top-level message, later failures reply in its thread, and the first pass after them replies
`✅ passing again` and closes the incident. The calling job runs `if: always()` and passes
`needs: ${{ toJSON(needs) }}`. State lives in the Actions cache; without `SLACK_BOT_TOKEN` it
does nothing. Callers still on `if: failure()` keep working but never get recoveries. Inputs,
caller snippet, state and migration: [docs/notify.md](docs/notify.md).

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
`bump-minor-pre-major: true` (breaking changes bump the minor before 1.0),
`initial-version: "0.1.0"` (a repo's first release; without it release-please proposes
1.0.0 for a repo with no release yet, which `version-guard` blocks as a major bump), and
changelog sections where `feat`, `fix`, `perf`, `revert` and `deps` are shown and `chore`, `ci`,
`docs`, `test`, `refactor` and `build` are hidden. release-please has no remote `extends`,
and it reads its config through the API from the target branch, so the workflow can't merge
the defaults in at runtime. Callers copy every key of `defaults.json` into their
`release-please-config.json` and add `packages` (and any other keys); on each run the
workflow compares the caller's config with the defaults at the same commit as the workflow
and prints a warning for each key that differs.

```json
{
  "$schema": "...", "bump-minor-pre-major": true, "initial-version": "0.1.0",
  "changelog-sections": ["... from defaults.json ..."],
  "packages": { ".": { "release-type": "go" } }
}
```

A `node` package (the ui) needs `"include-component-in-tag": false` in its package entry.
release-please takes a node package's component from the `name` in `package.json` and tags
its releases `<name>-vX.Y.Z` by default, and `release-ui.yml` and `cut-prerelease.yml` only
accept `vX.Y.Z`. The setting goes in the package, not at the top level, so the defaults
check doesn't warn about it:

```json
{
  "$schema": "...", "bump-minor-pre-major": true, "initial-version": "0.1.0",
  "changelog-sections": ["... from defaults.json ..."],
  "packages": { ".": { "release-type": "node", "include-component-in-tag": false } }
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

#### `preview.yml`

Publishes previews to `ghcr.io/compliance-framework/<name>` with `GITHUB_TOKEN` (rules in
`internal/release`, `PreviewTags`):

| Event | Tags |
| --- | --- |
| push to the default branch | `main` and `sha-<first 7 of the commit>`, unless `on-main` is `false` |
| `pull_request` with the `preview` label (not from a fork) | `pr-<number>` |
| anything else | none; the publishing job is skipped |

It never publishes `latest`, which only final releases move. What it publishes depends on
`kind`, each the same way as the kind's release workflow:

| `kind` | Kinds | Publishes |
| --- | --- | --- |
| `image` | `go-service`, `ui`, `action` | Container images, `linux/amd64` and `linux/arm64` (`publish-image.yml`). |
| `go-plugin` | `go-plugin` | `goreleaser release --snapshot --clean` (nothing is released), then `gooci upload` of `dist/` with the `org.ccf.plugin.protocol.version` annotation. |
| `policies` | `policies` | `opa build` of `directory`, then `gooci upload-single` of the bundle. |

A plugin or policy repo that wants PR previews only sets `on-main: false`.

| Input | Default | What |
| --- | --- | --- |
| `on-main` | `true` | Publish `:main` and `:sha-<7>` on pushes to the default branch. |
| `images` | `[{}]` | JSON list of `{"name", "dockerfile", "context"}`, one image each; `name` defaults to the repo name, `dockerfile` to `Dockerfile`, `context` to `.`. |
| `kind` | `image` | `image`, `go-plugin` or `policies`; anything else fails. |
| `protocol-version` | `2` | `go-plugin`: the agent plugin protocol the plugin implements. |
| `directory` | `policies` | `policies`: the bundle root. |
| `opa-version` | `1.14.1` | `policies`: OPA version, without the leading `v`. |

```yaml
# .github/workflows/preview.yml in a consuming repo
name: preview
on:
  push:
    branches: [main]
  pull_request:
    types: [opened, synchronize, reopened, labeled]
permissions:
  contents: read
concurrency:
  group: preview-${{ github.ref }}
  cancel-in-progress: true
jobs:
  preview:
    uses: compliance-framework/workflows/.github/workflows/preview.yml@v1
    with:  # agent's three images
      images: >-
        [{"name": "agent"}, {"name": "agent-ci", "dockerfile": "Dockerfile-ci"},
         {"name": "agent-custodian", "dockerfile": "Dockerfile-custodian"}]
    permissions:
      contents: read
      packages: write
```

A plugin repo calls it with `with: {kind: go-plugin, on-main: false}` (a policy repo with
`kind: policies`), and the same permissions.

The image build is a separate reusable workflow, `publish-image.yml`, that `preview.yml`
and `release-go-image.yml` call as `./.github/workflows/publish-image.yml`: inputs `images`
(as above) and `tags` (space-separated tag names without the repository, each checked
against the registry's tag syntax), permissions `contents: read` and `packages: write`. It
builds each image natively, `linux/amd64` on `ubuntu-latest` and `linux/arm64` on
`ubuntu-24.04-arm` (no QEMU), with the OCI `source` and `revision` labels, and pushes each
build by digest only. A `merge` job per image then creates one manifest list from the two
digests (passed between jobs as `digests-<name>-<arch>` artifacts, kept a day) and tags it
`ghcr.io/<owner>/<name>:<tag>` for every tag, with the `source` and `revision` annotations
on the index, which GHCR uses to link the package to the repo. It publishes whatever tags it
is given; the caller decides them. If any build fails, no image is tagged, and two
`images` entries with the same name (case-insensitive) fail the run before any build.

#### `cut-prerelease.yml`

Cuts a release candidate on demand. It reads the next version from the open release-please
PR's `.release-please-manifest.json` (package `path`, default `.`), lists the existing
`<tag-prefix><version>-rcN` tags, and creates the GitHub prerelease
`<tag-prefix><version>-rc<N+1>` (not marked latest), tagging the default branch's head. It
fails when there isn't exactly one open release-please PR, or when the final tag already
exists. It runs as ccf-release-bot with the same scoped token as `release-please.yml`
(`contents: write`, `pull-requests: read`), so the prerelease triggers the caller's release
workflows. Inputs: `path` and `tag-prefix` (default `v`). Output: `tag`. Secrets as for
`release-please.yml`.

```yaml
# .github/workflows/cut-prerelease.yml in a consuming repo
name: cut-prerelease
on:
  workflow_dispatch:
permissions:
  contents: read
jobs:
  cut:
    uses: compliance-framework/workflows/.github/workflows/cut-prerelease.yml@v1
    permissions:
      contents: read
    secrets: inherit
```

#### Release workflows per kind

A repo calls its kind's release workflow when a release is published: by release-please
(`release-please.yml`) for a final release or by `cut-prerelease.yml` for a release
candidate, both as ccf-release-bot so the event starts workflows. Each one decides its tags
from the release's tag name (`github.event.release.tag_name`), never from the release's
`prerelease` flag (rules in `internal/release`, `ReleaseTags`), and ends with
[`release-finished.yml`](#release-finishedyml). They need `secrets: inherit` for the
release-bot secrets.

| Workflow | Kind | Publishes |
| --- | --- | --- |
| `release-go-image.yml` | `go-service` | Container images (one or more), native `linux/amd64` and `linux/arm64`. |
| `release-ui.yml` | `ui` | The same as `release-go-image.yml` (it calls it), with the ui's single image. |
| `release-go-plugin.yml` | `go-plugin` | `goreleaser release --clean` (the archives go on the GitHub release; the config needs `release.prerelease: auto`), then `gooci upload` of `dist/` with `--annotate="org.ccf.plugin.protocol.version=<protocol-version>"`. |
| `release-go-lib.yml` | `go-lib` | `goreleaser release --clean` of the release tag: the binaries and archives go on the GitHub release (the config needs `release.prerelease: auto`). |
| `release-policies.yml` | `policies` | `opa build` of `directory` at `opa-version`, then `gooci upload-single` of the bundle. |
| `release-helm.yml` | `helm` | `helm package` of the released chart at the tag's version, then `helm push` to `registry`. |
| `release-action.yml` | `action` | Nothing: it moves the major tag (`v0` today) to the release commit. |

Image tags, for the tag `v1.2.3` (prefix `tag-prefix`, default `v`): `1.2.3`, `1.2`, `1` and
`latest`. A tag with a pre-release part (`v1.2.3-rc1`) publishes `1.2.3-rc1` only: a release
candidate never moves `latest` or the floating `X.Y` and `X` that users follow. A tag that is
not `<prefix>X.Y.Z[-pre-release]` fails the release.

Plugin and policy (gooci) tags keep the `v`, as the agent configs reference them
(`plugin-x:v0.4.0`): `v1.2.3` and `latest` for a final tag, `v1.2.3-rc1` only for a
pre-release. gooci is built from source at the pinned version (`go install
github.com/compliance-framework/gooci@v0.0.7`) and reads the registry login from
`docker/login-action`.

| Input | Default | What |
| --- | --- | --- |
| `images` | `[{}]` | As for `preview.yml`: one entry per image (agent has three). |
| `tag-prefix` | `v` | Prefix of the release tags. |
| `protocol-version` | `2` | `release-go-plugin.yml`: the agent plugin protocol the plugin implements. |
| `directory`, `opa-version` | `policies`, `1.14.1` | `release-policies.yml`: the bundle root and OPA version. |
| `charts-dir`, `registry` | `charts`, `oci://ghcr.io/compliance-framework/helm-charts` | `release-helm.yml`: the directory holding one directory per chart, and where charts are pushed. |

`release-go-plugin.yml` and `release-go-lib.yml` need `contents: write` (goreleaser attaches
the archives to the release) and `packages: write`, `release-action.yml` `contents: write`
(the tag) and `packages: write` (the prune); the others need `contents: read` and
`packages: write`. `release-go-lib.yml` publishes no package, but `release-finished.yml`'s
prune needs `packages: write` (it finds no package and prunes nothing).

**Charts.** In a multi-chart repo release-please tags each chart's release
`<component>-vX.Y.Z` (`ccf-agent-v0.3.0`); `release-helm.yml` releases the chart whose
directory, or failing that whose `Chart.yaml` name, is the component (`ccf` finds
`charts/ccf-app`), and a repo with a single chart may use plain `vX.Y.Z` tags. Rules in
`internal/release` (`ComponentTag`, `PickChart`). The chart is packaged with `--version` set
to the tag's version, because a release candidate is tagged on the default branch, where
`Chart.yaml` still has the previous version until the release PR merges; so
`ccf-agent-v0.4.0-rc1` pushes `helm-charts/ccf-agent:0.4.0-rc1`. Charts have no `latest`.
The repo calls `cut-prerelease.yml` with the chart's `path` and `tag-prefix: <chart>-v`.

**Go libraries.** `release-go-lib.yml` (no inputs) checks out the release tag, sets up Go
from the repo's `go.mod` and runs `goreleaser release --clean` (goreleaser-action and
goreleaser at the versions `ci-go-lib.yml` pins) with `GITHUB_TOKEN` and
`GORELEASER_CURRENT_TAG` set to the release tag, so two release candidates on one commit
can't make it release the other tag. Before the build it runs the same goreleaser config
check as `release-go-plugin.yml` (see **goreleaser config** below). goreleaser also resets
the release's name to `release.name_template` (the tag by default).

**Actions.** For a final tag `vX.Y.Z`, `release-action.yml` points `vX` at the release
commit (creating it the first time) with `GITHUB_TOKEN`, so callers pinned to `@v0` get the
release; a release candidate moves nothing.

**goreleaser config.** `release-go-plugin.yml` and `release-go-lib.yml` run
`goreleaser release` after release-please (or `cut-prerelease.yml`) has created the
GitHub release, so goreleaser finds it by tag and updates it: it uploads the
archives and checksums and, with the default `release.mode` (`keep-existing`), keeps the
notes release-please wrote. It also resets the release's pre-release flag to what the config
says, `false` unless `release.prerelease` is `auto` or `true`, which would turn a release
candidate into a full release that can be marked latest. So before building, the publish job
checks the repo's goreleaser config, found the way goreleaser finds it
(`.config/goreleaser.y[a]ml`, `.goreleaser.y[a]ml`, `goreleaser.y[a]ml`):

- `release.prerelease` must be `auto`, so the flag comes from the tag (`v1.2.3-rc1` stays
  a pre-release); anything else fails the release.
- `release.mode` other than `keep-existing` warns (`append`, `prepend` and `replace` add
  goreleaser's changelog to release-please's notes or replace them).
- `release.replace_existing_artifacts` not `true` warns: without it a re-run after a
  partial upload fails on the archives already attached.

The repo must not turn on immutable releases: goreleaser can't add assets to an immutable
release, and release-please publishes the release before these workflows run.

```yaml
# .goreleaser.yaml in a plugin or library repo: the release settings these workflows expect
version: 2
release:
  prerelease: auto
  replace_existing_artifacts: true
```

```yaml
# .github/workflows/release.yml in a consuming repo
name: release
on:
  release:
    types: [published]
permissions:
  contents: read
jobs:
  release:
    uses: compliance-framework/workflows/.github/workflows/release-go-image.yml@v1  # or release-ui.yml
    with:  # agent's three images
      images: >-
        [{"name": "agent"}, {"name": "agent-ci", "dockerfile": "Dockerfile-ci"},
         {"name": "agent-custodian", "dockerfile": "Dockerfile-custodian"}]
    permissions:
      contents: read
      packages: write
    secrets: inherit
```

```yaml
# the same, in a plugin repo
jobs:
  release:
    uses: compliance-framework/workflows/.github/workflows/release-go-plugin.yml@v1  # or release-policies.yml
    permissions:
      contents: write  # release-policies.yml: read
      packages: write
    secrets: inherit
```

```yaml
# the same, in a library repo (gooci)
jobs:
  release:
    uses: compliance-framework/workflows/.github/workflows/release-go-lib.yml@v1
    permissions:
      contents: write
      packages: write
    secrets: inherit
```

```yaml
# the same, in a chart or action repo
jobs:
  release:
    uses: compliance-framework/workflows/.github/workflows/release-helm.yml@v1  # or release-action.yml
    permissions:
      contents: read  # release-action.yml: write
      packages: write
    secrets: inherit
```

#### `release-finished.yml`

The tail of every release workflow, called as a job with `needs` on every other job and
`if: always()`, so it runs whatever the release's result, cancellation included:

1. **`dispatch`** sends `repository_dispatch` with `event_type: release-finished` and
   `client_payload: {repo, tag, conclusion}` to `compliance-framework/workflows`, where the
   release train listens. `conclusion` is `failure` if any job failed, else `cancelled` if
   any was cancelled, else `success`. It runs as ccf-release-bot with a token scoped to the
   `workflows` repo only (`contents: write`, which `repository_dispatch` needs), and, like
   `release-please.yml`, refuses to mint one unless the scope is exactly one repo.
2. **`prune`** then deletes the caller's GHCR package versions whose tags are all preview
   tags (`sha-*`, `pr-*`) and that were last updated more than 30 days ago, with
   `GITHUB_TOKEN` (`packages: write`) through the packages API. A version that also carries
   `main`, a release tag or `latest` stays, and so do untagged versions (a multi-arch image's
   platform manifests). A package that doesn't exist yet is skipped.

Inputs: `needs` (the caller's `toJSON(needs)`) and `images` (the packages to prune, as for
`publish-image.yml`; only `name` is read). Secrets: `RELEASE_BOT_APP_ID` and
`RELEASE_BOT_PRIVATE_KEY`. Permissions: `packages: write`.

## Repo settings

`repo-settings.yml` (`workflow_dispatch`, run in this repo) brings each repo's settings to one
desired state, defined in `internal/reposettings`:

- **Merges**: squash only, with the PR title and body as the commit title and message
  (`PR_TITLE`, `PR_BODY`); auto-merge allowed; head branches deleted on merge.
- **Security**: Dependabot alerts on; Dependabot security updates off (Renovate handles dependency updates).
- **Ruleset `ccf-required`**, on the default branch, with no bypass: a pull request is required,
  the status check `ci / required` must pass (reported by GitHub Actions, app 15368), and force
  pushes and deletion are blocked.
- **Ruleset `ccf-review`**, on the default branch: one approving review, bypassed by the
  `ccf-release-bot` app only (its release and Renovate PRs).

Rulesets are matched by name; other rulesets are left alone. Settings outside this list are not
read or changed. A repo's required check exists once it calls the shared CI with a `required` job
(the caller job is named `ci`, so the check is `ci / required`, as on the mocks); until then
`ccf-required` blocks its merges.

Inputs:

| Input | Default | What |
| --- | --- | --- |
| `manifest` | `repos.yaml` | `repos.mock.yaml` for the mocks. |
| `repos` | every repo with `release: true` | Comma-separated subset; each must be in the manifest with `release: true`. |
| `apply` | `false` | `false` prints the diff and writes nothing; `true` writes it (from `main` only). |
| `release-bot-app-id` | org variable `RELEASE_BOT_APP_ID` | `ccf-release-bot`'s app ID, the `ccf-review` bypass actor. Set the org variable (Settings, Secrets and variables, Actions, Variables) to the app's ID from its settings page, or pass the input; the run fails if both are empty. |
| `required-check` | `ci / required` | The status check context `ccf-required` requires. |

Secrets: `REPO_ADMIN_APP_ID` and `REPO_ADMIN_PRIVATE_KEY` (`ccf-repo-admin`). The app is installed
on every org repo, so the job lists the selected repos first and mints a token scoped to exactly
those (`repositories:`, with Administration `read`, or `write` when applying). It refuses an empty
selection, and the tool checks the token reaches no other repo before reading anything.

Output, per repo, is one line per differing setting, `key: current -> desired`, then a total:

```console
compliance-framework/mock-api: 28 change(s)
  repo.allow_merge_commit: true -> false
  security.dependabot_security_updates: enabled -> disabled
  ruleset[ccf-required]: missing -> create
  ruleset[ccf-review].rules.pull_request.required_approving_review_count: 0 -> 1
  ...
compliance-framework/mock-ui: up to date
28 change(s) in 1 of 2 repo(s) (dry run, nothing written; re-run with apply to write)
```

GitHub leaves the merge settings out of `GET /repos/{owner}/{repo}` for a token with
Administration **read**, so a dry run can't see them. They print as
`repo.allow_merge_commit: unknown (not readable with Administration read) -> false`, count as
`N unknown` next to the changes (`<repo>: <n> change(s), 7 unknown`), and a line under the
totals says apply writes them. Apply PATCHes the full desired merge settings whenever any of them
is unknown or differs; the PATCH is idempotent.

Dependabot security updates are read from `GET /repos/{owner}/{repo}/automated-security-fixes`
(`enabled`); if the token can't read it, it prints as `unknown (not readable)`, never as enabled.

**Org-managed security settings.** An org code security configuration (Settings, Code security,
Configurations) that is attached with enforcement on owns the Dependabot settings it sets, and
GitHub refuses a repo-level write with a 422. The tool reads
`GET /repos/{owner}/{repo}/code-security-configuration` (404 or 403: none) and skips those
settings. If the configuration already sets the desired value it prints
`security.dependabot_security_updates: managed by org configuration "Baseline Security Profile" (disabled)`;
if it sets another value it prints a warning (`... wants disabled, org enforces enabled — change it
in the org configuration`), counted as `N warning(s)`, which doesn't fail the run. Change those in
the org configuration, not here.

Apply runs each step on its own: the merge settings, each ruleset, then the security settings. A
failed step (say a 422) is printed (`<repo>: <step> failed: ...`) and doesn't stop the others; the
repo ends with `applied N step(s)` or `partly applied: F of N step(s) failed`, and the run fails at
the end if any step failed.

How to run it:

1. **Dry run** (anyone, any time): Actions, `repo-settings`, Run workflow, with `apply` off. Or
   `gh workflow run repo-settings.yml -f manifest=repos.mock.yaml`. `ccf-repo-admin` has
   Administration **read** only, so a dry run can't change anything. Review the diff.
2. **Apply** (a human, never an agent): in the `ccf-repo-admin` app settings, temporarily grant
   **Administration: Read and write** and accept the new permission on the org installation. Run
   the workflow from `main` with the same inputs and `apply` on. Run it again with `apply` on:
   every repo should be `up to date` and nothing is written (the sync is idempotent). A dry run
   can't show this, since its token is always read-only: a repo that is otherwise in sync shows
   `0 change(s), 7 unknown`. Then set Administration back to **read**.
   Do the mocks first.

The workflow calls `go run ./cmd/repo-settings sync`. Flags it doesn't expose: `--owner`
(default `compliance-framework`), `--required-check-app-id` (default `15368`, GitHub Actions; `0`
accepts the check from any source) and `--check-token-scope` (default `true`). Locally, the tool
runs against a token in `GH_TOKEN` (a personal token isn't an installation token, so pass
`--check-token-scope=false`):

```sh
go run ./cmd/repo-settings sync --manifest repos.mock.yaml --repos mock-api --bypass-app-id <id> --check-token-scope=false
```

## Renovate

`renovate.yml` runs self-hosted Renovate daily on the manifest's repos, with the shared preset
`renovate/default.json` (third-party updates on the 8th and 22nd, vulnerability fixes any day,
non-majors grouped and auto-merged). See [docs/renovate.md](docs/renovate.md).

## Vulnerability summary

`vuln-summary.yml` posts the open Dependabot alerts of the manifest's repos, per repo and
severity, to Slack every Monday (and on `workflow_dispatch`). See
[docs/vuln-summary.md](docs/vuln-summary.md).

## ccf-bump

`ccf-bump` moves internal dependency pins (Go modules, `go install` pins, the action's source image,
policy OPA versions, the ui sync, helm appVersions and image tags, shared-workflow refs) to their
latest final releases, one `fix(deps)` PR per repo; `ccf-bump-sync.yml` runs it on the 8th and 22nd.
See [docs/ccf-bump.md](docs/ccf-bump.md).

## Release train

`train.yml` releases the manifest's repos once a month, stage by stage: it bumps each repo's
internal dependencies with `ccf-bump`, merges the release PRs, waits for the releases, and tracks it
all in a `Release train YYYY-MM` issue and a Slack thread. See [docs/train.md](docs/train.md).

## Stack smoke test

`stack-smoke.yml` (reusable and `workflow_dispatch`) runs postgres, the API, the UI and one agent
from their published images at given tags and checks they work together (`smoke/run.sh`). See
[docs/stack-smoke.md](docs/stack-smoke.md).

## Plugin probe

`cmd/plugin-probe` and the reusable `plugin-probe.yml` load plugins through the agent's runner
library, report each one's protocol (v1 or v2) and agent library version, and check policy
bundles with the agent's OPA version. See [docs/plugin-probe.md](docs/plugin-probe.md).

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
