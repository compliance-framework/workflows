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
| Vulnerability fixes | Any day (`vulnerabilityAlerts.schedule: at any time`), with no minimum age. `osvVulnerabilityAlerts: true` finds CVEs from osv.dev even without a Dependabot alert. Each fix gets its own `[SECURITY]` PR, outside the group and the concurrent-PR limit, and auto-merges unless it is a major. |
| Waiting period | `minimumReleaseAge: "7 days"` with `internalChecksFilter: "strict"`: no branch or PR before a release is a week old. An update with no release timestamp waits too (Renovate's default `minimumReleaseAgeBehaviour`). |
| Grouping | Every `minor`, `patch`, `digest`, `pin` and `pinDigest` update of a repo goes into one PR, `renovate/all-non-major` ("all non-major dependencies"), so a run costs one CI run per repo. The Go toolchain and the `golang` image are in it. Majors get their own PRs. |
| Auto-merge | The non-major group auto-merges with `platformAutomerge`: GitHub merges it once the `ci / required` check passes (needs the repo's "allow auto-merge" setting, which `repo-settings.yml` turns on). Majors wait for a human. |
| Limits | `prHourlyLimit: 4`, `prConcurrentLimit: 8` per repo. |
| Commits | Semantic: `fix(deps)` for runtime dependencies (Go `require`, npm `dependencies`, a Dockerfile's final stage, the Go `toolchain` directive), which release-please releases, and `chore(deps)` for everything else (dev, CI, actions). A group takes the highest type of its updates, so a group with any runtime update is `fix(deps)`. |
| Go | `postUpdateOptions: ["gomodTidy"]`. Indirect modules are left to `go mod tidy` (Renovate's default). |
| Actions | Pinned by digest (`helpers:pinGitHubActionDigests`). Dockerfile base images and images in helm values are tracked by Renovate's own managers. |
| OPA | Off in every repo except `api` (and `mock-api`), so in agent, the policy repos and the plugins it follows the api through `ccf-bump`. In `api` it gets its own PR, `renovate/opa`, which never auto-merges, because a bump forces agent and the policies to follow. |
| Internal packages | Off: `github.com/compliance-framework/**` (Go modules), `ghcr.io/compliance-framework/**` (images) and `compliance-framework/**` (actions and reusable workflows, such as this repo's). The mocks use the same prefixes. |

It extends `config:recommended`, which adds the Dependency Dashboard issue (where majors and pending
updates are listed) and Renovate's standard monorepo groups.

A repo may add its own `renovate.json` for overrides only: Renovate applies the preset to every
listed repo as its base, and the repo's file is merged on top. Repos need no onboarding PR.

Validate the preset after editing it, as a preset and as global config:

```sh
npx --yes --package renovate -- renovate-config-validator --strict --no-global renovate/default.json
npx --yes --package renovate -- renovate-config-validator --strict renovate/default.json
```

## The workflow: `renovate.yml`

Runs daily at 06:17 UTC and on `workflow_dispatch`, as one job:

1. `go run ./cmd/renovate-config list` selects the repos from the manifest (every repo in it, or the
   `repos` subset) and fails on an empty selection.
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
   This repo is not in Renovate's list, so bump `renovate-version` and the action by hand.

| Input | Default | What |
| --- | --- | --- |
| `manifest` | `repos.mock.yaml` | The repos to run on. `repos.yaml` once Renovate goes live on the product repos (W1-S4-T03). |
| `repos` | every repo in the manifest | Comma-separated subset; each must be in the manifest. |
| `dry_run` | `full` | Renovate's `dryRun`: `full` logs every branch and PR it would create, `lookup` stops after finding updates, `extract` after reading the package files. `off` runs live. |
| `log_level` | `info` | `debug` for a full trace. |

A scheduled run has no inputs and uses these defaults, so until the defaults change it is a daily
`full` dry run on the mocks. Secrets: `RELEASE_BOT_APP_ID` and `RELEASE_BOT_PRIVATE_KEY`.

### Commit statuses

`ccf-release-bot` has no Commit statuses permission, so the generated config sets
`statusCheckWhen.minimumReleaseAge: "never"`: Renovate doesn't set its `renovate/stability-days`
status. It isn't needed, because `internalChecksFilter: "strict"` already keeps a branch from being
created before the update is a week old. Renovate still reads statuses (the repos are public). If
the app is later granted Commit statuses write, this can go, and Renovate will also mark a failed
artifact update (`renovate/artifacts`) on the branch.

### Failures

A Renovate branch starts with `renovate/`, so when its CI fails, the repo's `notify-failure.yml`
posts it to `#ccf-ci-failures` (rule (a)), and the failed check blocks auto-merge.

## Running it

- **Dry run**: Actions, `renovate`, Run workflow, or
  `gh workflow run renovate.yml -f manifest=repos.mock.yaml -f dry_run=full`. The log lists each
  `DRY-RUN: Would create PR` per repo; expect about one grouped PR per repo plus its majors.
- **Live on the mocks**: the same with `-f dry_run=off`. Renovate opens the grouped PRs, and GitHub
  merges them once `ci / required` passes.
- **Live on the product repos** (W1-S4-T03, a human): change the `manifest` and `dry_run` defaults
  and the scheduled-run fallbacks in `renovate.yml` to `repos.yaml` and `off`.

Locally, `go run ./cmd/renovate-config config --manifest repos.mock.yaml` prints the config the
workflow would use (the preset path defaults to `renovate/default.json`, run from the repo root).
