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
ccf-bump list [--repos a,b] [--chunk 10] [--dry-run]
```

- **sync** mode (the default; `ccf-bump sync`) targets each dependency's latest **final** release:
  the highest `vX.Y.Z` release that is neither a draft nor a prerelease, read from the GitHub API.
- **train** mode targets only the `--set` versions (the release train passes what it released, and the latest final release of a dependency that released nothing in it).
- `--set dep=version`: `dep` is a repo name, a `github.com/<owner>/...` module path, `opa` or
  `workflows`; it overrides the resolved target in either mode. Versions must be semver
  (`v1.2.3` or `1.2.3`); a `workflows` target can also be any other ref, used as given.
- `--workflows-ref REF` (same as `--set workflows=REF`): the shared-workflow pins' target. In sync
  mode it defaults to the latest final release of the manifest's `kind: workflows` repo; train
  mode moves them only when given. See [Shared-workflow pins](#shared-workflow-pins).
- Without `--pr`, ccf-bump prints the plan and the diff. `--pr` pushes branch
  `ccf-bump/<mode>-<YYYY-MM-DD>` (forced, so a rerun the same day updates it) and opens the PR, or
  updates the open one. `--pr --dry-run` prints the PR instead.
- `--batch N` (sync): at most N PRs per hour; it waits for the hour to pass. The wait outlives
  an app installation token (one hour), so `ccf-bump-sync.yml` doesn't rely on it: it runs chunks
  of 10 repos as separate jobs, each with its own token ([The `ccf-bump-sync` workflow](#the-ccf-bump-sync-workflow)).
- `--dry-run` may name repos of an inactive batch (`--repos`, `--repo`), to check a batch before it
  is switched on ([manifest.md](manifest.md#switching-a-batch-on)). Without it, named repos must be
  active. A dry run orders such repos by stage as if their batch were active.
- In a workflow step (`$GITHUB_OUTPUT` set), `sync` writes `prs=<N>`, the number of branches it
  pushed (PRs opened or updated).
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
- `--manifest` (default `repos.yaml`) gives the repos, their kinds
  and the dependency edges. `sync --all` takes every active repo with `release: true` ([manifest.md](manifest.md)), in stage order.
- `--clones DIR` reads `DIR/<repo>` (its committed `HEAD`) instead of cloning from GitHub, for dry
  runs against local clones.
- `list` prints the selected repos comma-separated, to scope the token. `list --chunk N` prints
  them as JSON chunks of at most N repos, in stage order:
  `[{"index":1,"of":4,"repos":"api,gooci,..."},...]`.
- Environment: `GH_TOKEN` (API, clone and push), `GITHUB_API_URL`, and the git identity in
  `GIT_AUTHOR_*`/`GIT_COMMITTER_*`. The commands it runs (`go`, the ui sync script) get neither
  token, and get `GOPRIVATE=github.com/<owner>/*`
  ([why](#why-org-modules-are-fetched-directly)).

## Updaters

Each repo is scanned for the pins below; the Dockerfile, OPA and ui pins only in repos of that kind
(`action`, `policies`, `ui`). A dependency is internal when it lives under
`github.com/compliance-framework/`, inside or outside the manifest (a module outside it is only read
for its releases).

| Pin | Where | Target |
| --- | --- | --- |
| Go modules | direct `require`s in the root `go.mod`; `go get <mod>@<ver>`, then `go mod tidy` | the module's repo |
| `go install github.com/<owner>/<repo>[/cmd/x]@<ver>` | `.github/workflows/*.y*ml` | the repo |
| `FROM <image> AS source` | `Dockerfile` of an `action` repo; any image is replaced by `ghcr.io/<owner>/<dep>:<ver>` (no `v`) | the action's one `depends_on` |
| `opa-version:` inputs | workflow files of a `policies` repo; expressions are left alone | the OPA version in the go.mod of its `go-service` dependency (else `agent`) at that repo's target tag |
| ui sync script | `scripts/sync-agentconfig-conformance.sh <tag>` (writes the conformance fixture; its current version is unknown) | the ui's `go-service` dependency |
| helm | `charts/*/values.yaml` image tags next to `repository: ghcr.io/<owner>/<repo>` (empty tags follow appVersion and stay empty); `Chart.yaml` `appVersion` | the image's repo; appVersion tracks the first org image whose tag is empty or equals appVersion |
| `uses: <owner>/workflows/...@<ref>` in a job | workflow files; the job's `workflows-ref` input is deleted when its pin moves | the latest final release of the `kind: workflows` repo (sync), or `--set workflows=<ref>` |

Files are edited in place, so comments and layout stay. Chart versions are left to release-please.

### What counts as a pin

A pin is a literal ref: a version (`v0.21.0`, `0.21.0`, a pseudo-version), or a ref that is not a
version (a branch like `main`, a commit SHA, `latest`, an image like `alpine:3.20`), which the
[rules](#rules) below replace. A ref that is a placeholder, filled in when the file is used, is
not a pin and is skipped: it is neither planned, listed in the PR nor a reason to hold it. That is
any ref with `$`, `{`, `}`, `%`, `<` or `>`: a shell or Actions expansion (`${ref}`, `$VERSION`,
`${{ inputs.version }}`, `$(cat VERSION)`), a Go, Helm or Jinja template (`{{ .Ref }}`), a printf
verb (`%s`) or a `<version>` placeholder. The ui sync scripts are not read for their refs: the
updater runs the script, and the pin is the file the script writes.

### Only applied changes count

After applying the plan, ccf-bump scans the checkout again. A planned change is in the PR only
when its file changed and its pin moved. A change that did not apply is dropped from the PR and
printed as a warning, so the PR's title, table and auto-merge decision describe its diff and
nothing else. A sync script whose current version is unknown (the ui conformance file) applies
when it changes its file; when it leaves it as it was, the file already matched the target: the
output says `unchanged`, and the PR neither lists it nor is held for it. With no change applied,
nothing is pushed.

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
  pin that is not a version, or whose current version is unknown (the ui conformance file when
  the script changes it), needs a human. So does, in a `helm` repo, an app version (an org image tag in `values.yaml` or the
  `Chart.yaml` `appVersion`) moved by a minor or more, `0.21.0 → 0.22.0` as much as
  `2.12.1 → 2.13.0`: chart CI can't tell that the new app needs new values or templates, and on 0.x
  a minor is where breaking changes land; a patch is still auto-merged. The PR body and the output
  say why it is held. These reasons count only the changes applied to the PR's files
  ([Only applied changes count](#only-applied-changes-count)).
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

## Why org modules are fetched directly

Every command ccf-bump runs gets `github.com/<owner>/*` in `GOPRIVATE` (appended to a value
already set, and to `GONOPROXY` and `GONOSUMDB` when those are set, since they override it). So
`go get` and `go mod tidy` fetch the org's modules from GitHub with git and skip
proxy.golang.org and sum.golang.org; `go.sum` is still checked. The repos are public, so the fetch
needs no credentials, and ccf-bump passes it none.

Through the public proxy, a version asked for before its tag exists is cached as "unknown
revision" (404) for about 30 minutes. On 2026-10-14 a train dry run asked for `api` v0.21.1,
`gooci` v0.0.9 and `agent` v0.9.2 before they were released; the live train's bumps then failed
with `go get ... exit status 1` for half an hour, while a direct fetch of the same versions
worked at once.

A dry run (`--dry-run`) also checks that each Go module version it would move to is tagged in its
repo, and fails that repo, after printing its plan, without running `go get` when one isn't. The
train's dry run plans versions its stages haven't released yet, so it never asks for them; its
plan says ccf-bump couldn't plan the repo.

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
them first is harmless. `renovate.yml` runs the same logic (`internal/automerge`) on Renovate's
PRs with `renovate-config merge` ([renovate.md](renovate.md#the-merge-pass)).

## The `ccf-bump-sync` workflow

`ccf-bump-sync.yml` runs `ccf-bump sync --pr` on the 8th and 22nd at 04:00 UTC, and on
`workflow_dispatch` with inputs `repos` (default every active repo with `release: true`; a dry run
may name repos of an inactive batch) and `dry_run` (default `true`). Every run reads `repos.yaml`.
Scheduled runs are dry runs until the repo variable `CCF_BUMP_SYNC_LIVE` is `true`.

The `select` job runs `ccf-bump list --chunk 10` and fails on an empty selection. The chunks are in
stage order. One `sync` job per chunk runs, one at a time (`max-parallel: 1`), and a failed chunk
doesn't stop the others. Each mints its own `ccf-release-bot` token scoped to exactly its chunk's repos
(`RELEASE_BOT_APP_ID`/`RELEASE_BOT_PRIVATE_KEY`, this repo's `release` environment secrets; main
only): read-only for dry runs; contents, pull requests and workflows write otherwise (bumps edit
workflow files), and checks read. Commits are authored by the bot. A live chunk then runs
`ccf-bump merge --wait 20m` over its repos. Unless it is the last chunk, it then waits until an hour
after it started before the next chunk begins. It skips the wait only when it knows it pushed
nothing (`prs=0`); a Bump step that failed without reporting may have pushed PRs, so it waits.

So the org gets at most 10 bump PRs an hour (one chunk's), and no token is used past its hour.
Before, one job ran `sync --batch 10` over every repo: its wait after 10 PRs outlived the job's
one-hour token, and with every batch active its waits outlived the 300-minute timeout.
Worst case, with every repo getting a PR:

| Active batches | Repos (`release: true`) | Chunks | PRs per hour | Run length (live) |
| --- | --- | --- | --- | --- |
| none (today) | 6 | 1 | 6 | under an hour |
| 1 | 33 | 4 | 10 | about 3 to 4 hours |
| 1, 2 | 60 | 6 | 10 | about 5 to 6 hours |
| 1, 2, 3 | 70 | 7 | 10 | about 6 to 7 hours |

A chunk that pushed nothing (every repo current) doesn't wait, and a dry run never does.

`ccf-bump-merge.yml` runs the merge pass alone (`--wait 0`) daily at 05:41 UTC, for PRs whose CI
finished later, and on `workflow_dispatch` with the input `dry_run` (default `true`). Every run
reads `repos.yaml`. A scheduled run is a dry run until `CCF_BUMP_SYNC_LIVE` is `true`. Its token has the
same permissions as the sync's (merging a PR that edits workflow files needs workflows write).

```sh
# Dry run against local clones of the selected repos (DIR/<repo>):
GH_TOKEN=$(gh auth token) go run ./cmd/ccf-bump sync --repos api,ui --clones DIR --pr --dry-run
```
