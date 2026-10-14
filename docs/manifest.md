# The manifest: batches and coverage

`repos.yaml` lists every repo the shared tools act on. The README ("The manifest") has the schema;
this page covers the two parts added for the plugins (workstream 3): batches, which gate when the
tools start acting on a repo, and the coverage check, which keeps the list complete.

## Batches

The plugin and policy repos join the release flow in batches. Each one is listed with a `batch`:

```yaml
active_batches: []      # the batches the tools act on

repos:
  - name: plugin-jira
    kind: go-plugin
    depends_on: [agent]
    release: true
    batch: 1
```

- A repo **without** `batch` is a core repo (the product repos and `workflows`) and is always
  active.
- A repo **with** `batch: N` is active only while `N` is in `active_batches`. Until then no tool
  touches it: listing a batch changes nothing on its own.
- Only kinds `go-plugin` and `policies` take a batch. Batches are numbered from 1.
- Every number in `active_batches` must have repos, and appear once.
- No repo depends on a repo of a later batch, and a core repo depends on no batched repo, so an
  active repo never waits on an inactive one.

`manifest.ActiveRepos()` returns the core repos plus the active batches' repos. Each tool starts
from it:

| Tool | Default selection | Named repos (`repos` input) |
| --- | --- | --- |
| Renovate (`renovate.yml`) | active repos | must be active; a dry run may name inactive ones |
| `ccf-bump` (sync, merge, list) | active repos with `release: true`, in stage order | must be active, `release: true`; a dry run may name inactive ones |
| Train (`train.yml`) | active repos with `release: true` (`Stages()`) | must be active, `release: true` |
| Attention digest | active repos | must be active |
| Vulnerability summary | active repos but `workflows` | must be active |
| `repo-settings` | active repos | **may be in an inactive batch**; must be in the manifest |

`repo-settings` is the one exception because a batch's settings (rulesets, the `release`
environment and its secrets) are applied before the batch is switched on: dispatch it with the
batch's repos in one `repos=` list. A name that isn't in the manifest is still an error.

Renovate and `ccf-bump` take a named repo of an inactive batch in a **dry run** only (Renovate
`dry_run` other than `off`; `ccf-bump` `dry_run: true`), so a batch can be checked before it is
switched on. Their live runs still reject it, and their default selection is always the active repos.

### Stages

`Stages()` orders the active repos with `release: true`. Plugin and policy repos depend on `agent`,
so an active batch lands in stage 3 with `agent-action`, and `helm-charts` (kind `helm`, which
implicitly depends on every non-helm repo) stays last in stage 4:

```console
stage 1: api gooci
stage 2: agent ui
stage 3: agent-action plugin-... plugin-...-policies
stage 4: helm-charts
```

### Switching a batch on

Order for each batch (workstream 3, "Rules for every batch"):

1. The batch's release-please and shared release workflow PRs are open and green.
2. Legacy rulesets or classic branch protection are removed.
3. `repo-settings` dry run (`apply: false`) with the batch's repos in `repos=`, then the apply.
4. The release workflow PRs are merged.
5. Dry runs with the batch's repos named, and read their plans:
   - `gh workflow run renovate.yml -f repos=<the batch's repos> -f dry_run=full` (in groups of
     about 15 repos: a dry run isn't sharded and its token lasts an hour). Expect one
     `DRY-RUN: Would create PR` for the non-major group per repo with updates, plus its majors.
   - `gh workflow run ccf-bump-sync.yml -f repos=<the batch's repos with release: true> -f dry_run=true`.
     Expect one `dry run: would push ... and open` per repo whose internal pins are behind.
6. One PR to this repo adds the batch number to `active_batches`. From the next scheduled run,
   Renovate, `ccf-bump`, the train, the attention digest and the vulnerability summary act on the
   batch's repos.

To take a batch out again, remove its number from `active_batches`. A train already open with
the batch's repos fails its repo check until the train is closed.

### `release: false` repos

`release: false` keeps a repo out of the release tooling only; other tools still act on it once it
is active:

| Tool | `release: false` repo (`workflows`, the templates) |
| --- | --- |
| Train, `ccf-bump` | Left out: no release, no internal pin bumps. |
| Renovate | Included (third-party dependencies). |
| Attention digest | Included. |
| Vulnerability summary | Included, except `workflows`. |
| `repo-settings` | Included, with the same settings as every repo, the `release` environment too. |

## Coverage check

`include_patterns` and `exclude` don't select repos for any tool; they feed one check:

```sh
go run ./cmd/manifest --manifest repos.yaml --org compliance-framework
```

It lists the organisation's repos from the GitHub API (read-only, public metadata; `GH_TOKEN` if
set, for the rate limit) and fails when:

- a listed repo doesn't exist, or is archived;
- a repo that isn't archived matches an `include_patterns` glob (`path.Match`) but is neither
  listed nor in `exclude`.

Archived repos are never required, so archiving a repo only needs its entry removed. A repo can't
be both listed and excluded (loading the manifest fails). Unit tests run the check against a
fixture (`internal/manifest/testdata/batches.yaml` and fake API pages).

CI runs it in its own job, `manifest-coverage` in `ci.yml`, on `repos.yaml` with the workflow's
own token, which sees the organisation's public repos: a private repo matching a pattern isn't
required until it is made public, and a listed repo must be public.

The coverage job is advisory: it isn't in the `ci / required` job's `needs`, so it doesn't gate
merges. A new, renamed or archived `plugin-*` repo (or a GitHub API error) turns it red on every PR
and on `main` without blocking unrelated changes; fix it by editing `repos.yaml`. The `go` job
still validates the manifest offline (`go run ./cmd/manifest --manifest repos.yaml`), and that part
is required.
