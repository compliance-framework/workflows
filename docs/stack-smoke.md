# Stack smoke test

`stack-smoke.yml` starts the product stack from its published images (postgres, the API, the UI
and one agent) and checks that the parts work together. It answers "do these three tags run
together?" before a release, without local-dev and without any secret. The stack lives in
`smoke/`:

| File | What |
| --- | --- |
| `smoke/compose.yaml` | postgres 17.5, `ghcr.io/compliance-framework/{api,ui,agent}` at `SMOKE_API_TAG`, `SMOKE_UI_TAG`, `SMOKE_AGENT_TAG`. |
| `smoke/run.sh` | Starts the stack, runs the checks, prints the container logs on failure, tears the stack down. |
| `smoke/agent-config.yml` | The agent's config: no plugins, `remote_config.mode: report`. |
| `smoke/ui-config.json` | The UI's `config.json`. Its `API_URL` is for a browser on the default API port; the checks do not use it. |

## The checks

`smoke/run.sh` runs them in order and stops at the first failure:

1. **API**: `GET /api/health/ready` returns 200 (database reachable). The API runs its
   migrations before it listens, and the script also checks in postgres that the tables the
   later checks use (`ccf_users`, `ccf_agents`, `ccf_agent_service_account_keys`,
   `ccf_agent_instances`) exist.
2. **UI**: `GET /` returns 200 with the app's `index.html`.
3. **Agent**: the script creates an admin user with the API's CLI (`/api users add`), logs in,
   creates the agent `smoke-agent` and a key for it (the flow of local-dev's
   `scripts/register-agents.sh`), then starts the agent with that key. The check passes when
   `GET /api/admin/agents/{id}/instances` lists an instance with `heartbeat-config-revision`
   set: the instance registered (config report at startup) and the API recorded a heartbeat
   from it. The agent heartbeats once a minute at a random second, so this check takes up to
   about a minute.

On a failure the script prints `SMOKE FAILED: <what>` (an `::error::` annotation on Actions),
then `docker compose ps -a` and every container's logs, then tears the stack down
(`down -v`).

## Credentials

There are none to provide. Everything is throwaway and generated per run:

- postgres uses the local default `postgres`/`postgres`; its port is not published.
- The API's `CCF_JWT_SECRET` comes from `openssl rand`. No JWT key files are configured, so
  the API generates its RSA key pair in memory.
- The admin password, the admin's token and the agent's key come from `openssl rand` and the
  API. On Actions they are masked in the log. The key's secret is never printed. The API's
  `users add` command logs the password it is given, so the script prints that output only
  when the command fails.

The API and UI ports are bound to `127.0.0.1` only.

## Run it

On Actions: `stack-smoke`, Run workflow (`workflow_dispatch`), or call it from another
workflow:

```yaml
jobs:
  smoke:
    uses: compliance-framework/workflows/.github/workflows/stack-smoke.yml@<sha> # vX.Y.Z
    with:
      api-tag: 0.21.0
      ui-tag: 2.12.1
      agent-tag: 0.9.0
```

| Input | Default | What |
| --- | --- | --- |
| `api-tag` | `0.21.0` | Tag of `ghcr.io/compliance-framework/api`. |
| `ui-tag` | `2.12.1` | Tag of `ghcr.io/compliance-framework/ui`. |
| `agent-tag` | `0.9.0` | Tag of `ghcr.io/compliance-framework/agent`. |

The workflow needs only `contents: read`: it checks out this repo at the commit the workflow
was called at (`job.workflow_sha`) and pulls public images. It pushes nothing.

Locally (Docker with compose v2, curl, jq, openssl):

```sh
SMOKE_API_TAG=0.21.0 SMOKE_UI_TAG=2.12.1 SMOKE_AGENT_TAG=0.9.0 smoke/run.sh
```

| Variable | Default | What |
| --- | --- | --- |
| `SMOKE_API_TAG`, `SMOKE_UI_TAG`, `SMOKE_AGENT_TAG` | (required) | Image tags. |
| `SMOKE_API_PORT`, `SMOKE_UI_PORT` | `18080`, `18000` | Host ports, on `127.0.0.1`. |
| `SMOKE_PROJECT` | `ccf-smoke` | Compose project name. |
| `SMOKE_TIMEOUT` | `180` | Seconds each check waits. |
| `SMOKE_KEEP` | (unset) | `1` leaves the stack up after the run, to debug. |

`docker compose up` on `smoke/compose.yaml` alone needs the three tags, and its agent runs
without a key. Use `smoke/run.sh`.

## Later

W3 adds a plugin step: a plugin in `smoke/agent-config.yml`, a check in `smoke/run.sh`
(`check_plugin`, still empty) that its evidence reached the API, and a plugin input in
`stack-smoke.yml`. Both files have a `TODO(W3)` marker where it goes.
