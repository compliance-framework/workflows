# ccf-bump

`ccf-bump` (`cmd/ccf-bump`, logic in `internal/bump`) moves the pinned versions of internal
dependencies in the manifest's repos and opens one PR per repo, titled `fix(deps): bump <list>`, or
`ci(deps): bump workflows to vX.Y.Z` when it moves only shared-workflow pins: `ci` is a hidden
section in `release-please/defaults.json`, so such a PR proposes no release. A PR that also moves
anything that ships (Go modules, images, helm, the action's Dockerfile, OPA) stays `fix(deps)`.

```text
ccf-bump --repo NAME [--set dep=version]... [--mode sync|train] [--pr] [--dry-run] [flags]
ccf-bump sync --all|--repos a,b [--batch 10] [--pr] [--dry-run] [flags]
ccf-bump merge --all|--repos a,b [--wait 20m] [--author NAME] [--dry-run]
ccf-bump list [--repos a,b]
```

- **sync** mode (the default; `ccf-bump sync`) targets each dependency's latest **final** release:
  the highest `vX.Y.Z` release that is neither a draft nor a prerelease, read from the GitHub API.
- **train** mode targets only the `--set` versions (the release train passes what it released).
- `--set dep=version`: `dep` is a repo name, a `github.com/<owner>/...` module path, `opa` or
  `workflows`; it overrides the resolved target in either mode. Versions must be semver
  (`v1.2.3` or `1.2.3`); a `workflows` target can also be any other ref, used as given.
- `--workflows-ref REF` (same as `--set workflows=REF`): the shared-workflow pins' target. In sync
  mode it defaults to the latest final release of the manifest's `kind: workflows` repo; train
  mode moves them only when given. See [Shared-workflow pins](#shared-workflow-pins).
- Without `--pr`, ccf-bump prints the plan and the diff. `--pr` pushes branch
  `ccf-bump/<mode>-<YYYY-MM-DD>` (forced, so a rerun the same day updates it) and opens the PR, or
  updates the open one. `--pr --dry-run` prints the PR instead.
- `--batch N` (sync): at most N PRs per hour; it waits for the hour to pass.
- A repo that fails is reported (`::error::`) and the others still run; the exit code is non-zero.
  Failures are a branch push, a PR create/update, or resolving the manifest or a version.
- **Warnings** don't fail the run: they are printed as `::warning::` and repeated at the end of the
  output and in `$GITHUB_STEP_SUMMARY` (when set). Failing to add a label is one; the PR stays
  open.
- **Superseded PRs**: after opening or updating a repo's bump PR, ccf-bump closes that repo's other
  open PRs it opened in the same mode: head branch `ccf-bump/<mode>-*` in the repo itself (not a
  fork), opened by the same account as the new PR (the release bot). Each gets the comment
  `Superseded by #N.` and its branch is deleted. Other PRs, including a `sync` PR during a `train`
  run, are never touched. A failure to close one is a warning. `--dry-run` only lists the PRs it
  would close; with nothing opened to tell who it runs as, it lists any bot's.
- `--manifest` (default `repos.yaml`; `repos.mock.yaml` for the mocks) gives the repos, their kinds
  and the dependency edges. `sync --all` takes every repo with `release: true`, in stage order.
- `--clones DIR` reads `DIR/<repo>` (its committed `HEAD`) instead of cloning from GitHub, for dry
  runs against local clones.
- `list` prints the selected repos comma-separated, to scope the token.
- Environment: `GH_TOKEN` (API, clone and push), `GITHUB_API_URL`, and the git identity in
  `GIT_AUTHOR_*`/`GIT_COMMITTER_*`.

## Updaters

Each repo is scanned for the pins below; the Dockerfile, OPA and ui pins only in repos of that kind
(`action`, `policies`, `ui`). A dependency is internal when it lives under
`github.com/compliance-framework/`, inside or outside the manifest (the mock plugins also pin the
product `agent` module; its releases are only read).

| Pin | Where | Target |
| --- | --- | --- |
| Go modules | direct `require`s in the root `go.mod`; `go get <mod>@<ver>`, then `go mod tidy` | the module's repo |
| `go install github.com/<owner>/<repo>[/cmd/x]@<ver>` | `.github/workflows/*.y*ml` | the repo |
| `FROM <image> AS source` | `Dockerfile` of an `action` repo; any image is replaced by `ghcr.io/<owner>/<dep>:<ver>` (no `v`) | the action's one `depends_on` |
| `opa-version:` inputs | workflow files of a `policies` repo; expressions are left alone | the OPA version in the go.mod of its `go-service` dependency (else `agent`) at that repo's target tag |
| ui sync script | `scripts/sync-agentconfig-conformance.sh <tag>` (ui) or `scripts/sync-mock-api-version.sh <tag>` (mock-ui) | the ui's `go-service` dependency |
| helm | `charts/*/values.yaml` image tags next to `repository: ghcr.io/<owner>/<repo>` (empty tags follow appVersion and stay empty); `Chart.yaml` `appVersion` | the image's repo; appVersion tracks the first org image whose tag is empty or equals appVersion |
| `uses: <owner>/workflows/...@<ref>` in a job | workflow files; the job's `workflows-ref` input is deleted when its pin moves | the latest final release of the `kind: workflows` repo (sync), or `--set workflows=<ref>` |

Files are edited in place, so comments and layout stay. Chart versions are left to release-please.

## Rules

- **Never downgrade.** A pin newer than the target is a conflict and stays as it is, and so is a
  pseudo-version whose commit is newer than the target tag's commit. A pin that is not a version
  (`alpine:3.20`, a branch, a SHA, `latest`) is replaced. Conflicts are printed and listed in the PR.
- **No target, no change**: a dependency with no final release yet is left alone.
- **Merged by ccf-bump** when every change stays within its major version (and, in a `helm`
  repo, every app version within its minor, below): the PR gets the
  `ccf-bump:automerge` label (color `0e8a16`, "ccf-bump merges this once CI is green", created in
  the repo when missing) and [`ccf-bump merge`](#the-merge-pass) merges it once `ci / required`
  passes. Never GitHub's auto-merge ([why](#why-ccf-bump-merges-not-github)); updating a PR that an
  older run left with GitHub's auto-merge on turns it off (a failure is a warning). A move from a
  pin that is not a version, or whose current version is unknown (the ui conformance file), needs
  a human. So does, in a `helm` repo, an app version (an org image tag in `values.yaml` or the
  `Chart.yaml` `appVersion`) moved by a minor or more, `0.21.0 → 0.22.0` as much as
  `2.12.1 → 2.13.0`: chart CI can't tell that the new app needs new values or templates, and on 0.x
  a minor is where breaking changes land; a patch is still auto-merged. The PR body and the output
  say why it is held.
- **A PR that needs a human** gets the `needs-human` label instead (and loses
  `ccf-bump:automerge` if an earlier run the same day added it); so does a PR whose
  `ccf-bump:automerge` label could not be added. A failure is a warning. See
  [attention.md](attention.md).
- Shared-workflow pins follow their own rules, below.

## Shared-workflow pins

Callers pin this repo's reusable workflows to a release's commit, with the version in a comment:
`uses: compliance-framework/workflows/.github/workflows/<f>.yml@<sha> # vX.Y.Z`. A SHA can't move
under the caller, and each update is a reviewable PR; the repo's tags never move.

The target is the release `vX.Y.Z`: in sync mode the latest final release of the manifest's
`kind: workflows` repo (none in the manifest, or no release: the pins are left alone), or one given
with `--set workflows=vX.Y.Z` / `--workflows-ref`. ccf-bump reads the commit the tag points at
(an annotated tag is peeled to its commit) and plans each job's pin:

| Pin | Plan |
| --- | --- |
| `@<target sha> # vX.Y.Z` | up to date |
| `@<target sha>` without the comment, or with another version | the comment is fixed; auto-merged |
| `@<sha> # vA.B.C` or a tag `@vA[.B.C]` of the same major, older | moved to `@<target sha> # vX.Y.Z`; auto-merged |
| `@main`, or a SHA without a version comment | moved; needs a human (the version it replaces is unknown) |
| another major (`v1` when the latest is `v2.0.0`) | conflict: a new major may change the inputs, a human moves it |
| a newer version | conflict: ccf-bump never downgrades |
| another branch (not `main`), or anything else | conflict: a human decides |

The release must have been published by the release bot (`--author`, default
`ccf-release-bot[bot]`, as release-please does) and its commit must be on the workflows repo's
default branch. The moved pins put every caller on that code and same-major moves auto-merge, so a
release someone published by hand, or cut from an unreviewed branch, doesn't move them: in sync
mode the pins are left as they are with a warning, and a release given with `--set` fails the run.

The new pin keeps the line's layout: the comment's version is added or replaced, and any other
comment text is kept after it (`# pinned` becomes `# vX.Y.Z pinned`). When a job's pin moves,
its `workflows-ref:` input and the comment above it are deleted (the reusable workflows build
their tools at the commit they are called at), and so is the `with:` it leaves empty; an input
inside a flow mapping
(`with: {...}`) fails the repo's bump, for a human to remove. A ref that is not a release
(`--set workflows=main`, a SHA) is used as given: every other pin moves to it, needing a human,
and its version comment is dropped.

## Why ccf-bump merges, not GitHub

GitHub's native auto-merge never applies the `ccf-review` bypass of `ccf-release-bot`, so a bump PR
with a green `ci / required` stays `BLOCKED` / `REVIEW_REQUIRED` (seen on all 10 mocks on
2026-10-08), as Renovate's did: see [renovate.md](renovate.md#why-renovate-merges-not-github). A
merge through the API as the bot does use the bypass, and `ccf-required` has no bypass, so the
required check still gates it.

## The merge pass

`ccf-bump merge` lists each repo's open PRs and merges those that are all of: labelled
`ccf-bump:automerge`, from a `ccf-bump/*` branch of the repo itself (not a fork), opened by
`--author` (default `ccf-release-bot[bot]`; the workflows pass the token's app), and not labelled
`needs-human` (a person can hold a PR by adding it). Renovate, release-please and people's PRs never
qualify. For each, at its head commit it reads every run of `ci / required` (`--required-check`):
a commit can have several, e.g. when adding the label re-triggers the callers' `ci.yml` on
`labeled`. While any run is queued or in progress the PR is pending; otherwise the newest run (by
start time, then ID) decides.

| State | What merge does |
| --- | --- |
| the newest run concluded `success`, and GitHub reports the PR mergeable | squash-merges it as the token's identity, with the PR title plus ` (#N)` as the commit title, if its head is still that commit. Behind its base is fine (`ccf-required` is not strict). |
| the newest run concluded otherwise | leaves it, with a warning: `notify-failure.yml` already posted the failure and the attention digest lists the PR |
| conflicting with its base | leaves it, with a warning |
| a run queued or running, no run yet, mergeability not computed, or GitHub refusing the merge with 405 "Required status check … is expected" (or `queued`, `in progress`, `pending`: a run started after the runs were read) | pending: with `--wait D` it looks again every 30s for up to `D`, then reports what is still pending (a later pass merges it) |

Any other merge GitHub refuses is a warning; failing to list a repo's PRs, or to read a PR or its check,
fails the run (the other repos still run). It removes no label. `--dry-run` prints what it would
merge and doesn't wait. The train merges its own train-mode bump PRs; one merge pass merging
them first is harmless.

## The `ccf-bump-sync` workflow

`ccf-bump-sync.yml` runs `ccf-bump sync --batch 10 --pr` on the 8th and 22nd at 04:00 UTC, and on
`workflow_dispatch` with inputs `manifest` (default `repos.mock.yaml`, or `repos.yaml`) and `dry_run`
(default `true`). Scheduled runs read `repos.mock.yaml` until go-live (W1-S4-T03), like renovate,
train and attention-digest (the fallback equals the input's default), and are dry runs until the repo variable
`CCF_BUMP_SYNC_LIVE` is `true`. It lists the repos, then mints a `ccf-release-bot` token scoped to
exactly those (`RELEASE_BOT_APP_ID`/`RELEASE_BOT_PRIVATE_KEY`, this repo's `release` environment secrets; main only): read-only for dry runs; contents,
pull requests and workflows write otherwise (bumps edit workflow files), and checks read. Commits
are authored by the bot. A live run then runs `ccf-bump merge --wait 20m` over the same repos.

`ccf-bump-merge.yml` runs the merge pass alone (`--wait 0`) daily at 05:41 UTC, for PRs whose CI
finished later, and on `workflow_dispatch` with inputs `manifest` (default `repos.mock.yaml`) and
`dry_run` (default `true`). A scheduled run uses `repos.mock.yaml` (the fallback equals the input's
default: change both to move to `repos.yaml`) and is a dry run until `CCF_BUMP_SYNC_LIVE` is
`true`. Its token has the same permissions as the sync's (merging a PR that edits workflow files
needs workflows write).

```sh
# Dry run against local clones of the mocks (DIR/mock-*):
GH_TOKEN=$(gh auth token) go run ./cmd/ccf-bump sync --all --manifest repos.mock.yaml --clones DIR --pr --dry-run
```
