# Vulnerability summary

`vuln-summary.yml` (run in this repo) posts a weekly summary of the open Dependabot alerts to
Slack: the total per severity, one line per repo with open alerts (its counts per severity,
linked to the repo's Dependabot alerts page), how many repos have none, the repos it could not
read, and links to the org's alerts page and the run. The logic is `cmd/vuln-summary`, with the
rules in `internal/vulnsummary`.

```text
:shield: 5 open Dependabot alerts in compliance-framework (10 repo(s) in repos.mock.yaml): 1 critical, 1 high, 1 medium, 2 low
• mock-api: 2 (1 critical, 1 medium)
• mock-ui: 3 (1 high, 2 low)
8 other repos have no open alerts.
All Dependabot alerts · run
```

Repos are ordered by their critical count, then high, medium and low (most first), then name.
Severity is the advisory's (`critical`, `high`, `medium`, `low`); anything else counts as `other`.

## When it runs

- **Schedule**: Mondays at 08:00 UTC, on the default manifest.
- **`workflow_dispatch`**: Actions, `vuln-summary`, Run workflow, or
  `gh workflow run vuln-summary.yml -f manifest=repos.mock.yaml`.

| Input | Default | What |
| --- | --- | --- |
| `manifest` | `repos.mock.yaml` | The repos to summarise: every repo in the manifest. `repos.yaml` for the product repos, once the summary is proven on the mocks (then change the default, which the schedule also uses). |

## Secrets and variables

| Name | Kind | What |
| --- | --- | --- |
| `RELEASE_BOT_APP_ID`, `RELEASE_BOT_PRIVATE_KEY` | secrets | `ccf-release-bot`, to read the alerts. |
| `SLACK_BOT_TOKEN` | secret | Bot token with `chat:write`. Without it the run prints the summary and posts nothing. |
| `SLACK_CHANNEL_VULNS` | variable | Channel ID to post to. Empty means `SLACK_CHANNEL_CI_FAILURES`. |

The job lists the manifest's repos first and mints a `ccf-release-bot` token scoped to exactly
those repos (`repositories:`), with only **Dependabot alerts: read** (`vulnerability-alerts`). It
refuses an empty selection, since the app is installed on every org repo and an empty list would
mint a token for all of them. Tokens are never printed; error messages leave out request headers.

## Per-repo reads, not the org endpoint

The task named `GET /orgs/compliance-framework/dependabot/alerts?state=open`. The tool reads
`GET /repos/compliance-framework/<repo>/dependabot/alerts?state=open` for each manifest repo
instead, following the `Link` header's cursor pagination (only to the API host):

| | Per repo (this tool) | Org endpoint |
| --- | --- | --- |
| Token | `ccf-release-bot`'s repo-level **Dependabot alerts: read**, scoped to the manifest's repos. | An org owner or security manager, or an installation token reaching every repo: the unscoped token the workflows never mint. |
| Repos covered | The manifest's repos only; repos picked up by `include_patterns` (workstream 3) once the manifest lists them. | Every repo the token reads, including ones outside the manifest. |
| "No alerts" | Known: the repo was read and returned nothing. | Unknown: only repos with alerts are returned. |
| Alerts disabled on a repo | Listed under "Could not read"; the run fails. | Silently absent. |
| Requests | One or more per repo. | One per 100 alerts. |

Switching needs an org-scoped token and a second listing call in `internal/vulnsummary`; the
summary and message don't change.

## Failures

A repo whose alerts can't be read (alerts disabled, a 404, a token that doesn't reach it) is
listed in the message under "Could not read" with the API's error, the others are still counted,
and the run fails after posting so it shows red in Actions. `repo-settings` turns Dependabot
alerts on for the manifest's repos (see the README, "Repo settings").

## Running it locally

```sh
go run ./cmd/vuln-summary list --manifest repos.mock.yaml
GH_TOKEN=<token> go run ./cmd/vuln-summary post --manifest repos.mock.yaml [--repos mock-api,mock-ui]
```

Without `SLACK_BOT_TOKEN` it only prints the message. `--owner` defaults to
`compliance-framework`. The tool also reads
`GITHUB_API_URL`, `GITHUB_SERVER_URL`, `SLACK_API_URL` and `SLACK_CHANNEL`, and links the run from
`GITHUB_REPOSITORY` and `GITHUB_RUN_ID` when set.
