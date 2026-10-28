# Image scan

CVEs inside the CCF container images (OS packages of a base image, a runtime such as Node in a
base image) are fixed by PRs from the same pipeline as Renovate's own vulnerability fixes:
`renovate.yml`'s `image-scan` job scans the images of the run's shard, and turns what a newer base
image fixes into Renovate package rules that the same run's Renovate applies. The fixes are then
ordinary Renovate PRs, opened by `ccf-release-bot` and merged by the same [merge
pass](renovate.md#the-merge-pass). OS package fixes that the distribution has but the base image
doesn't ship yet get an [OS package patch](#os-package-patches) PR of image-scan's own, merged by
the same pass. Only fixable HIGH and CRITICAL findings are acted on. The job log and the step
summary are the only report: there is no issue, SARIF upload, or Slack message.

The logic is `cmd/image-scan`, with the rules in `internal/imagescan`.

## The scanner

[osv-scanner](https://github.com/google/osv-scanner) v2 (`scan image`), the same tool as
`ci-common.yml`'s `vulns` job, run from its official image pinned by digest
(`imagescan.ScannerImage`, `ghcr.io/google/osv-scanner:v2.7.0@sha256:...`). This repo's
`renovate.json` tracks that constant with a regex custom manager, so Renovate updates it like any
other dependency (`TestScannerImageRenovate`).

No scanner inventories a language runtime installed from an upstream tarball: in October 2026
osv-scanner v2.7.0, Trivy 0.76.1 and Grype v0.121.0 all flagged CVE-2026-111497 (brotli) in the
Debian `libbrotli1` of `node:20` and in the `brotli-libs` of `nginx:stable-alpine`, but none in the
brotli built into `node:20`'s Node binary. So image-scan adds a **runtime check**:

- It reads the runtime's version from the image: the variable the official images set
  (`NODE_VERSION=20.20.2` in `node:20`), or else the output of the runtime's binary run with
  `--version` (`docker run --network none --entrypoint node <image> --version`), for a runtime
  installed another way. No binary means no runtime.
- It looks the version up in OSV (`POST https://api.osv.dev/v1/query` with
  `{"package": {"name": "node", "ecosystem": "Bitnami"}, "version": "20.20.2"}`): Bitnami's
  advisories track Node.js releases. For 20.20.2 that returns `BIT-node-2026-111497` (alias
  CVE-2026-111497), fixed in 22.23.4, 24.22.1 and 26.12.1: no fix in 20.
- Runtimes are a table (`imagescan.Runtimes`); Node is the only one today. Add another with the
  variable its images set, its binary and its OSV package.

Every finding has the image, the stage, the package, its type (`os`, `lang` or `binary` for a
runtime), the installed and fixed versions, the advisory IDs and their aliases, and a severity.
The severity is the highest CVSS base score of the advisory's vectors (CVSS v2, v3.0, v3.1 or v4.0,
computed with the library osv-scanner uses, so both agree): **CRITICAL** from 9.0, **HIGH** from
7.0, then MEDIUM and LOW; an advisory with no vector takes osv-scanner's own rating of its group,
and none at all is UNKNOWN. Distribution ratings (Grype's, Debian's urgency) are not used. A
finding is **actionable** when it is HIGH or CRITICAL and has a fixed version.

`internal/imagescan/testdata` holds the spike's osv-scanner reports of `node:20` and
`nginx:stable-alpine` (trimmed to a few packages) and the OSV and endoflife.date responses,
recorded; `TestSpikeAcceptance` checks that CVE-2026-111497 is flagged as actionable in `node:20`
(the Node runtime, by the runtime check, and Debian's `libbrotli1`) and in `nginx:stable-alpine`
(`brotli-libs`, fixed in `1.2.1-r0`). No test reaches the network.

## What is scanned

For each repo of the shard whose kind has Dockerfiles: `go-service` and `ui`, which publish images,
and `action` (agent-action's `action.yml` runs its `Dockerfile`, which the runner builds on each
use and never publishes):

- **Every base image of every Dockerfile.** A `go-service` or `ui` repo's Dockerfiles are those of
  the `images` input its caller workflows pass to `release-go-image.yml`, `release-ui.yml`,
  `preview.yml` (kind `image`) or `publish-image.yml`, read from its default branch (agent passes
  three: `Dockerfile`, `Dockerfile-ci`, `Dockerfile-custodian`). A call without `images`, or a repo
  with no such call, means the default: one image named after the repo, from `Dockerfile`. An
  `action` repo has `Dockerfile`. A repo without the Dockerfile is noted and skipped.
- **The published images**, for `go-service` and `ui`: `ghcr.io/compliance-framework/<name>:main`
  (the default branch's preview) and `:latest` (the latest release), for each image name. They are
  reported only (a repo without a release has no `:latest`, which is a note). Their findings include
  what the Dockerfile adds on top of the base image, such as the Go modules of the binary.

The Dockerfile parser reads the stages: `FROM [--platform=...] <image> [AS <name>]`, the `ARG`
defaults declared before the first `FROM` (`${VAR}`, `${VAR:-default}` and the other forms),
`FROM <stage>`, `scratch`, and references with a tag, a digest or both. The final stage is the last
`FROM` (`publish-image.yml` builds without `--target`); its base image, followed through
`FROM <stage>` (the ui's `FROM production`), is tagged `final`, every other one `build`.

Each image is pulled for `linux/amd64`, saved to an archive, scanned, and removed unless it was
already there (a local run keeps its images); an image used
by several repos is scanned once per run. osv-scanner runs with its offline databases
(`--offline-vulnerabilities --download-offline-databases`) in `~/.cache/osv-scanner`, which the job
caches (a new cache key a day, restored from the newest); each run refreshes them, downloading only
what changed.

## Remediation

The plan (`imagescan.Planner`) makes at most one rule 1 per repo and image name, and one rule 2 per
repo, image name and release, written as a JSON list of Renovate `packageRules`:

1. **Rule 1: a newer image of the same tag fixes it.** A final-stage base image with actionable
   findings, whose candidate scans clean of all of them. The candidate is the closest newer image:
   for an `X.Y.Z` tag, the newest `X.Y` patch with the same suffix (a `patch` update); otherwise,
   when the reference pins a digest, the tag's current digest (a `digest` update:
   `nginx:stable-alpine@sha256:<old>` moves to the tag's rebuild). The rule:

   ```json
   {
     "matchRepositories": ["compliance-framework/ui"],
     "matchManagers": ["dockerfile"],
     "matchDepNames": ["nginx"],
     "matchCurrentVersion": "/^(stable-alpine)$/",
     "matchUpdateTypes": ["digest"],
     "minimumReleaseAge": null,
     "prHourlyLimit": 0, "prConcurrentLimit": 0, "branchConcurrentLimit": 0,
     "semanticCommitType": "fix",
     "groupName": "security fixes (dockerfile)", "groupSlug": "security-dockerfile",
     "automerge": true, "addLabels": ["renovate:automerge"],
     "prPriority": 10,
     "prBodyNotes": ["**image-scan** (compliance-framework/workflows): this update of `nginx:stable-alpine@sha256:...` fixes: ..."]
   }
   ```

   It mirrors the preset's `vulnerabilityAlerts`: no waiting period, no PR limit, `fix(deps)`, one
   `renovate/security-dockerfile` PR per repo, auto-merged by Renovate and the merge pass. The PR
   body lists the CVEs with the package, versions and severity of each. `matchCurrentVersion`
   limits it to the tags scanned, so another image of the same name in the repo (a `node:20` build
   stage next to a `node:22` final stage) keeps its own rules.

   The rule applies only to the update that was scanned. `matchUpdateTypes` keeps it off
   Renovate's other updates of the image, which keep the preset's rules: a major still waits for
   its release age, stays under the preset's caps and gets `needs-human`. For a `patch` candidate
   (`golang:1.26.1` to `golang:1.26.2`) the rule also has `"matchNewValue": "/^(1\\.26\\.2)$/"`,
   the scanned tag, so a patch released after the scan isn't fast-tracked. Without more, Renovate
   would not open that patch at all: `separateMinorPatch` is off by default, so the patch shares
   the `non-major` bucket with the minors, and Renovate proposes the newest minor (`1.27.x`). So
   a `patch` candidate gets a second rule, written before the first, that turns
   `separateMinorPatch` on for the scanned tags; the minor then gets its own PR under the preset's
   rules. It has to be its own rule: Renovate refuses `matchUpdateTypes` in a rule together with
   `separateMinorPatch` or `allowedVersions`, which act before the update type is known.

   ```json
   {
     "matchRepositories": ["compliance-framework/ui"],
     "matchManagers": ["dockerfile"],
     "matchDepNames": ["golang"],
     "matchCurrentVersion": "/^(1\\.26\\.1)$/",
     "separateMinorPatch": true
   }
   ```
2. **Rule 2: the image is on an end-of-life release.** A base image of any stage whose tag names an
   end-of-life release of `node`, `debian` or `alpine` (`imagescan.eolLines`; [endoflife.date](https://endoflife.date/)'s
   API, failing soft: when it can't be read the image goes through rules 1 and 3) with actionable
   findings that a newer release can fix: for a runtime image, the runtime's own findings (its
   packages are fixed by a rebuild of the same release); for an OS image, all of them. The rule
   opens the **lowest supported release whose current image scans clean of them** (`node:22` for
   `node:20`, not 24), keeping the tag's suffix (`20-alpine` moves to `22-alpine`):

   ```json
   {
     "matchRepositories": ["compliance-framework/ui"],
     "matchManagers": ["dockerfile"],
     "matchDepNames": ["node"],
     "matchCurrentVersion": "/^20(\\.|-|$)/",
     "allowedVersions": "/^22(\\.|-|$)/",
     "prHourlyLimit": 0, "prConcurrentLimit": 0,
     "automerge": false, "addLabels": ["needs-human"],
     "prBodyNotes": ["**image-scan** (compliance-framework/workflows): `node:20` is on nodejs 20, which is end-of-life; this update to nodejs 22 fixes: ..."]
   }
   ```

   `matchCurrentVersion` keeps the rule off the repo's other `node` images (a `node:24` stage). A
   major is for a human, so it never auto-merges and gets `needs-human`. Rule 1 is not applied to
   an image rule 2 moves. A codename tag (`bookworm-slim`) or an image the table doesn't know
   (`nginx`, distroless) is never moved to another release.
3. **Anything else actionable is noted**: the distribution has the fix but the upstream image
   hasn't been rebuilt with it yet (the candidate still has the finding, the tag wasn't rebuilt
   since the pinned digest, or the tag isn't pinned by digest, so it already is the newest image),
   or the finding is in a build stage (not shipped), or in an internal `ghcr.io/compliance-framework`
   image (ccf-bump moves those). The OS package findings of a final image get an [OS package
   patch](#os-package-patches).

A finding is "still there" in a candidate when the candidate has a finding of the same package
(the advisory's package, or the installed one) with a shared ID or alias, fixable or not: the
distribution advisory IDs differ between releases (`ALPINE-CVE-...` per Alpine branch), the CVE
alias doesn't.

### How Renovate gets the rules

`image-scan scan` writes the rules to `--overrides` (the job uploads it as the
`image-scan-overrides` artifact), and `renovate-config config --overrides <file>` appends them after
the preset's `packageRules`, so they win over the preset's (Renovate applies matching rules in
order, later ones last). The file is generated input, so `renovate.ParseOverrides` checks it
strictly and the config step fails on anything else:

- only the keys of `renovate.PackageRule`, which the planner builds its rules from:
  `matchRepositories`, `matchManagers`, `matchDepNames`, `matchCurrentVersion`,
  `matchUpdateTypes`, `matchNewValue`, `allowedVersions`, `separateMinorPatch`,
  `minimumReleaseAge`, `prHourlyLimit`, `prConcurrentLimit`, `branchConcurrentLimit`,
  `semanticCommitType`, `groupName`, `groupSlug`, `automerge`, `addLabels`, `prPriority` and
  `prBodyNotes`; each with a value of its type that is neither empty nor null, except
  `minimumReleaseAge`, which must be null;
- every rule names exactly one `matchRepositories` entry, `compliance-framework/<repo>`, with the
  repo in the run's selection;
- `matchManagers` is exactly `["dockerfile"]`, and `matchDepNames` lists at least one image, each
  by its plain name (no tag, digest, glob, `!` or `/regex/`), so a rule can't reach every Docker
  dependency of a repo;
- the limits and `prPriority` are not negative; `matchCurrentVersion`, `matchNewValue` and
  `allowedVersions` are regexes anchored at the start (`/^.../`);
- `matchUpdateTypes` lists only `digest` or `patch`, never next to `allowedVersions` or
  `separateMinorPatch` (Renovate refuses that), and a rule that auto-merges must list it, so no
  override can auto-merge a minor or a major.

A missing or empty file adds no rules.

## OS package patches

When upstream hasn't rebuilt the base image but the distribution has the fix (in October 2026,
`brotli-libs` 1.2.1-r0 in `nginx:stable-alpine`), image-scan patches the Dockerfile itself. For each
Dockerfile whose final image's base has actionable `os` findings that rule 1 doesn't fix, it writes
a managed block right after the `FROM` of the stage the final image starts from (followed back
through `FROM <stage>`; after a `FROM`'s continuation lines):

```dockerfile
FROM nginx:stable-alpine AS production
# image-scan:os-patches begin
# Managed by compliance-framework/workflows image-scan (docs/image-scan.md#os-package-patches); removed once the base image ships the fixes.
RUN apk upgrade --no-cache brotli-libs curl libcurl
# image-scan:os-patches end
```

- **Alpine** (`apk`): `RUN apk upgrade --no-cache <packages>`.
- **Debian and Ubuntu** (`apt-get`): `RUN apt-get update && apt-get install -y --no-install-recommends --only-upgrade <packages> && rm -rf /var/lib/apt/lists/*`,
  after `# hadolint ignore=DL3008`: the upgrade takes the distribution's current fixed version and
  pins none, which hadolint (run on the action repos' Dockerfiles by `ci-action.yml`) would fail.
- **Distroless and scratch**, with no shell or package manager, are skipped with a note. The scan
  finds the package manager by running `command -v apk || command -v apt-get` with the image's
  `/bin/sh` (no network).

The packages are the installed ones with the findings (`brotli-libs`, `libbrotli1`), each checked
against the Alpine/Debian package name grammar before it goes into the `RUN` line. Right after
`FROM`, the block runs as the base image's user, root in the images it patches; a `USER`
instruction of the stage applies only after it. The block is regenerated each run from the base
image's current findings, and **removed** once the base image has none left (it ships the fixes);
while rule 1 is moving the image to a newer one that fixes them, the Dockerfile is left as it is,
block included, until that update merges. A Dockerfile whose base image couldn't be scanned is left
alone.

Each repo gets one branch, **`image-scan/os-patches`**, for all its Dockerfiles. It is not under
`renovate/`: Renovate deletes the `renovate/*` branches it doesn't know. On a live run, image-scan:

1. leaves the repo alone if the PR wasn't opened by the bot, or if the branch has a commit that
   isn't on the default branch by anyone else, open PR or not (Renovate's modified-branch rule; a
   person takes it over by pushing to it, and closing the PR doesn't hand it back);
2. leaves the repo alone, until the next run scans it again, if a patched Dockerfile at the default
   branch's head (read by the commit's SHA) is no longer the content the scan read: the patch
   replaces the whole file, so committing it would revert a change merged during the run;
3. rebuilds the branch from the default branch's head with the patched Dockerfiles, one commit
   through the API, authored and committed as `ccf-release-bot[bot]` (the job's "Commit as the
   release bot" step, as in `ccf-bump-sync.yml`), and force-pushes it; when the branch already is
   that change on the current head, it pushes nothing, so CI doesn't run again;
4. opens or updates the PR, labelled `renovate:automerge` when it opens it (a person who takes the
   label off keeps it off): **`fix(deps): patch OS packages in
   <image> (<CVE ids>)`**, so the fix releases, or **`chore(deps): remove the OS package patches of
   <image>`** when it only removes blocks. The body lists each Dockerfile, base image, package,
   installed and fixed version, severity and CVEs;
5. closes the PR and deletes the branch when nothing is left to patch.

The [merge pass](renovate.md#the-merge-pass) merges `image-scan/*` PRs like `renovate/*` ones:
labelled `renovate:automerge`, opened by the bot, every commit by the bot, not `needs-human`, once
`ci / required` passes. A dry run writes nothing: it logs the branch, the PR title and the lines it
would add or remove. A repo whose scan was incomplete is skipped.

## The CI check: `ci-image.yml`

The scheduled scan sees base images and published images; a Dockerfile change is only seen once
it is published. So each repo's CI checks the image it builds, on every PR and every push to the
default branch. `ci-image.yml` (`workflow_call`) takes the `images` input of `publish-image.yml`
(same shape and default `[{}]`) and runs one job per image, on `ubuntu-latest`, amd64 only:

1. It checks out this repo at `job.workflow_sha` (the commit the caller pinned, the `preview.yml`
   pattern), for `cmd/release` and `cmd/image-scan`, and the caller's code under `caller/`.
2. Without the image's Dockerfile it writes a notice and skips the rest.
3. `go run ./cmd/release build-version` picks the single `VERSION` build arg, in the preview's form
   (`pr-<n>-<sha7>` on a PR, `main-<sha7>` on a push) but whatever the PR's labels or fork.
4. `docker/build-push-action` builds it with `load: true` and `push: false` (nothing is pushed, so
   fork PRs work, and the job needs only `contents: read`), with the GitHub Actions layer cache
   (one scope per image).
5. `go run ./cmd/image-scan check --image ci-image:local` scans the **full built image** with the
   same scanner, parser and runtime check as the scheduled job, so `FROM safe` followed by
   `RUN apk add unsafe` is caught. Every finding, fixable or not, goes to the step summary (the
   fixable HIGH and CRITICAL ones first), and the job **fails on any fixable HIGH or CRITICAL
   finding**, on PRs and on pushes to the default branch alike. There is no diff against `:main`:
   a red run on `main` is the signal that the image has an issue.

`ci-go-service.yml` and `ci-ui.yml` call it as job `image`, with a new `images` input (default
`[{}]`) passed through; agent's `ci.yml` must pass the same `images` list as its release and
preview workflows. `ci-action.yml` calls it with its `dockerfile` and `context` inputs (the image
the runner builds for agent-action on each use). The `image` job is **not** in `required`'s needs:
it never gates a merge (Renovate, ccf-bump and the train gate on `ci / required`). Like the other
CI jobs it is skipped on a closed PR. A failed `image` still fails the caller's `ci` job (the
reusable workflow's result), so the three workflows also report their `required` check's outcome
(the `required-result` output), and `notify-failure.yml` tells a failure of `image` alone from one
that gates ([notify.md](notify.md#the-image-check)): on a pull request (Renovate, ccf-bump,
release-please, `image-scan/os-patches`) it posts nothing, and on a push to the default branch it
opens an incident of its own, so a build or test failure on `main` still gets its own card. The
osv-scanner image comes from `ghcr.io`, and the runtime check queries `api.osv.dev`, so the job
needs network access but no token.

## The job

`image-scan` runs in `renovate.yml` between `select` and `renovate` ([renovate.md](renovate.md#the-workflow-renovateyml)):

1. `select` lists the shard's image repos with `go run ./cmd/image-scan list` (the selected repos of
   kind `go-service`, `ui` or `action`). With none, `image-scan` is skipped.
2. It mints a `ccf-release-bot` token scoped with `repositories:` to exactly those repos, with
   **Contents** (the caller workflows and the Dockerfiles; the `image-scan/os-patches` branch) and
   **Pull requests** (its PR and label): write on a live run, read on a dry run, which writes
   nothing. The list is never empty here, and an empty list would reach every repo. The next step
   sets the bot's commit identity.
3. `go run ./cmd/image-scan scan` scans, plans, writes the overrides, logs every finding to the job
   log (one line each), and writes the step summary: per repo, a table of the images scanned
   (findings, HIGH/CRITICAL, actionable), a table of their HIGH and CRITICAL findings (fixable or
   not; the first 150), the number of rules, and the notes. On a live run it then keeps each repo's
   [OS package patch](#os-package-patches) PR in line. It exits non-zero, after all that, when an
   image or Dockerfile could not be scanned or a PR could not be updated; the rules for the rest
   still apply.
4. The overrides are uploaded, also after a failed scan.

`renovate` runs once `select` succeeded, whether `image-scan` succeeded, failed or was skipped
(`if: ${{ !cancelled() && needs.select.result == 'success' }}`); it downloads the overrides when
there are any. Dry runs scan too: only their Renovate config is a dry run's, so the log shows the
PRs the rules would make.

The job has 50 minutes, within its token's hour. A run now takes the scan's time before Renovate
starts (about a minute per image, more for the large `node` and `golang` images); a run still in
progress when the next one is due delays it (the `renovate` concurrency group queues one), which
slows the rotation but never skips a shard.

Locally, with Docker and a token that reads the repos:

```sh
GH_TOKEN=$(gh auth token) go run ./cmd/image-scan scan --repos ui --overrides /tmp/overrides.json --summary /tmp/summary.md
go run ./cmd/renovate-config config --repos ui --overrides /tmp/overrides.json | jq '.packageRules[-1]'
```
