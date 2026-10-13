# Stack smoke test

`stack-smoke.yml` starts the product stack from its published images (postgres, the API, the UI
and one agent running one plugin) and checks that the parts work together. It answers "do these
tags run together?" before a release, without local-dev and without any secret. The stack lives
in `smoke/`:

| File | What |
| --- | --- |
| `smoke/compose.yaml` | postgres 17.5, `ghcr.io/compliance-framework/{api,ui,agent}` at `SMOKE_API_TAG`, `SMOKE_UI_TAG`, `SMOKE_AGENT_TAG`. |
| `smoke/run.sh` | Starts the stack, runs the checks, prints the container logs on failure, tears the stack down. |
| `smoke/agent-config.yml` | The agent's config template: `remote_config.mode: report` and one plugin, `plugin-file-attestation` at `${SMOKE_PLUGIN_TAG}` with its policies at `${SMOKE_POLICY_TAG}`, every minute. `smoke/run.sh` fills in the tags and mounts the result. |
| `smoke/fixtures/attested-file.txt` | The file the plugin checks, mounted read-only into the agent at `/etc/ccf-agent/fixtures/`. |
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
4. **Plugin**: the agent pulls `ghcr.io/compliance-framework/plugin-file-attestation` and its
   policy bundle `plugin-file-attestation-policies` (public, anonymous pulls) and runs the
   plugin at the start of every minute against `smoke/fixtures/attested-file.txt`
   (`file_path`; no `attestation_path` or `authorized_signers`, both optional). The check
   passes when `POST /api/evidence/search` finds at least one evidence record with the labels
   the plugin sets for a local file (`provider=file`, `type=attestation`, `source=local`) and
   the agent's `_agent` label (its key's client-id). The fixture has no attestation, so some
   of the plugin's policies are not satisfied; the check only needs the evidence to arrive,
   whatever its status. The plugin's OCI index carries the
   `org.ccf.plugin.protocol.version=2` annotation, so the agent runs it with protocol v2
   without a `protocol_version` in the config; a plugin tag without that annotation would run
   as v1. On a timeout the script prints the last 50 lines of the agent's log before the full
   dump.

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
      plugin-tag: v0.2.0
      policy-tag: v0.2.0
```

| Input | Default | What |
| --- | --- | --- |
| `api-tag` | `0.21.0` | Tag of `ghcr.io/compliance-framework/api`. |
| `ui-tag` | `2.12.1` | Tag of `ghcr.io/compliance-framework/ui`. |
| `agent-tag` | `0.9.0` | Tag of `ghcr.io/compliance-framework/agent`. |
| `plugin-tag` | `v0.2.0` | Tag of `ghcr.io/compliance-framework/plugin-file-attestation`, the plugin the agent runs. Plugin tags keep their `v`. |
| `policy-tag` | `v0.2.0` | Tag of `ghcr.io/compliance-framework/plugin-file-attestation-policies`, its policy bundle. |

The workflow needs only `contents: read`: it checks out this repo at the commit the workflow
was called at (`job.workflow_sha`) and pulls public images. It pushes nothing.

Locally (Docker with compose v2, curl, jq, openssl):

```sh
SMOKE_API_TAG=0.21.0 SMOKE_UI_TAG=2.12.1 SMOKE_AGENT_TAG=0.9.0 \
  SMOKE_PLUGIN_TAG=v0.2.0 SMOKE_POLICY_TAG=v0.2.0 smoke/run.sh
```

| Variable | Default | What |
| --- | --- | --- |
| `SMOKE_API_TAG`, `SMOKE_UI_TAG`, `SMOKE_AGENT_TAG` | (required) | Image tags. |
| `SMOKE_PLUGIN_TAG`, `SMOKE_POLICY_TAG` | (required) | Tags of the plugin and its policy bundle. |
| `SMOKE_API_PORT`, `SMOKE_UI_PORT` | `18080`, `18000` | Host ports, on `127.0.0.1`. |
| `SMOKE_PROJECT` | `ccf-smoke` | Compose project name. |
| `SMOKE_TIMEOUT` | `180` | Seconds each check waits. The plugin runs at the start of each minute, so keep it above 60. |
| `SMOKE_KEEP` | (unset) | `1` leaves the stack up after the run, to debug, and prints the command that tears it down. |

`docker compose up` on `smoke/compose.yaml` alone needs the three tags and
`SMOKE_AGENT_CONFIG` (the rendered agent config, which `smoke/run.sh` writes to a temporary
directory and removes afterwards unless the stack is left up), and its agent runs without a
key. Use `smoke/run.sh`.
