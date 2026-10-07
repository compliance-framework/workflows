#!/usr/bin/env bash
# Stack smoke test (docs/stack-smoke.md): starts postgres, the API, the UI and one agent from
# the published images in smoke/compose.yaml, checks that they work together, and tears the
# stack down. On any failure it prints the container logs before the teardown.
#
#   SMOKE_API_TAG=0.21.0 SMOKE_UI_TAG=2.12.1 SMOKE_AGENT_TAG=0.9.0 smoke/run.sh
#
# Checks:
#   1. api: GET /api/health/ready is 200 (the API listens only after its migrations ran), and
#      the tables the checks below use exist in postgres.
#   2. ui: GET / is 200 and serves the app's index.html.
#   3. agent: with a key created in the API for this run, the agent's instance shows in
#      GET /api/admin/agents/{id}/instances and the API has recorded a heartbeat from it.
#
# Settings (environment):
#   SMOKE_API_TAG, SMOKE_UI_TAG, SMOKE_AGENT_TAG  image tags (required)
#   SMOKE_API_PORT, SMOKE_UI_PORT  host ports, on 127.0.0.1 (default 18080, 18000)
#   SMOKE_PROJECT   compose project name (default ccf-smoke)
#   SMOKE_TIMEOUT   seconds to wait for each check (default 180; the agent heartbeats once a
#                   minute at a random second)
#   SMOKE_KEEP=1    leave the stack running (no teardown), to debug
#
# Needs docker (with compose v2), curl, jq and openssl. Bash 3.2 compatible.
set -euo pipefail

SMOKE_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT="${SMOKE_PROJECT:-ccf-smoke}"
TIMEOUT="${SMOKE_TIMEOUT:-180}"
export SMOKE_API_PORT="${SMOKE_API_PORT:-18080}"
export SMOKE_UI_PORT="${SMOKE_UI_PORT:-18000}"
API="http://127.0.0.1:$SMOKE_API_PORT"
UI="http://127.0.0.1:$SMOKE_UI_PORT"
ADMIN_EMAIL="smoke-admin@example.test"
AGENT_NAME="smoke-agent"

started=""

log() { printf '==> %s\n' "$*"; }

# fail MESSAGE: prints the failure (as a GitHub annotation on Actions) and exits 1; the EXIT
# trap then dumps the logs and tears the stack down.
fail() {
    if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
        echo "::error title=stack smoke::$*"
    fi
    echo "SMOKE FAILED: $*" >&2
    exit 1
}

# mask VALUE: hides a generated secret in the Actions log.
mask() {
    if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
        echo "::add-mask::$1"
    fi
}

compose() {
    docker compose -p "$PROJECT" -f "$SMOKE_DIR/compose.yaml" "$@"
}

cleanup() {
    local status=$?
    trap - EXIT
    if [ -n "$started" ]; then
        if [ "$status" -ne 0 ]; then
            echo "---- containers ----" >&2
            compose ps -a >&2 || true
            echo "---- container logs ----" >&2
            compose logs --no-color --timestamps >&2 || true
            echo "---- end of container logs ----" >&2
        fi
        if [ "${SMOKE_KEEP:-}" = "1" ]; then
            log "SMOKE_KEEP=1: leaving the stack up (docker compose -p $PROJECT -f $SMOKE_DIR/compose.yaml down -v)"
        else
            log "Tearing the stack down"
            # A failed teardown does not change the result, but say what is left behind.
            compose down -v --remove-orphans >/dev/null 2>&1 ||
                echo "WARNING: could not tear the stack down; run: docker compose -p $PROJECT -f $SMOKE_DIR/compose.yaml down -v" >&2
        fi
    fi
    if [ "$status" -eq 0 ]; then
        log "Stack smoke test passed"
    fi
    exit "$status"
}

# wait_for COMMAND...: runs COMMAND every 2 seconds until it succeeds; returns 1 when it has
# not succeeded within TIMEOUT seconds.
wait_for() {
    local start
    start=$(date +%s)
    until "$@"; do
        if [ $(($(date +%s) - start)) -ge "$TIMEOUT" ]; then
            return 1
        fi
        sleep 2
    done
}

# api METHOD PATH [curl args...]: calls the API with the admin token; sets STATUS and BODY.
api() {
    local method="$1" path="$2" out
    shift 2
    out="$(mktemp)"
    STATUS="$(curl -sS -o "$out" -w '%{http_code}' -X "$method" "$API/api$path" \
        -H "Authorization: Bearer $TOKEN" "$@")" || STATUS="000"
    BODY="$(cat "$out")"
    rm -f "$out"
}

check_settings() {
    local tool name tag value
    for tool in docker curl jq openssl; do
        command -v "$tool" >/dev/null 2>&1 || fail "$tool is not installed"
    done
    for name in SMOKE_TIMEOUT SMOKE_API_PORT SMOKE_UI_PORT; do
        case "$name" in SMOKE_TIMEOUT) value="$TIMEOUT" ;; *) value="${!name}" ;; esac
        case "$value" in
            '' | *[!0-9]*) fail "$name '$value' is not a whole number" ;;
        esac
    done
    for name in SMOKE_API_TAG SMOKE_UI_TAG SMOKE_AGENT_TAG; do
        tag="${!name:-}"
        [ -n "$tag" ] || fail "$name is not set"
        # The docker tag grammar; anything else would not be a tag.
        printf '%s' "$tag" | grep -Eq '^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$' ||
            fail "$name '$tag' is not a valid image tag"
    done
}

api_ready() {
    [ "$(curl -s -o /dev/null -w '%{http_code}' "$API/api/health/ready" 2>/dev/null)" = "200" ]
}

ui_ready() {
    [ "$(curl -s -o /dev/null -w '%{http_code}' "$UI/" 2>/dev/null)" = "200" ]
}

check_api() {
    local tables
    log "Waiting for the API (GET /api/health/ready)"
    wait_for api_ready || fail "the API did not become ready at $API/api/health/ready within ${TIMEOUT}s"
    # The API migrates before it listens, so ready means migrated; check the tables anyway.
    tables="$(compose exec -T postgres psql -U postgres -d ccf -tAc \
        "select count(*) from information_schema.tables where table_schema = 'public' and table_name in ('ccf_users', 'ccf_agents', 'ccf_agent_service_account_keys', 'ccf_agent_instances')")" ||
        fail "could not query postgres for the API's tables"
    [ "$(printf '%s' "$tables" | tr -d '[:space:]')" = "4" ] ||
        fail "the API's migrations did not create its tables (found $tables of ccf_users, ccf_agents, ccf_agent_service_account_keys, ccf_agent_instances)"
    log "api: ready, migrations ran"
}

check_ui() {
    local page
    log "Waiting for the UI (GET /)"
    wait_for ui_ready || fail "the UI did not serve $UI/ within ${TIMEOUT}s"
    page="$(curl -sS "$UI/")" || fail "GET $UI/ failed"
    printf '%s' "$page" | grep -q '<div id="app"' ||
        fail "GET $UI/ did not return the UI's index.html"
    log "ui: serves /"
}

# bootstrap_agent: creates an admin user (CLI in the API container), logs in, creates the
# agent and a key, and exports the key for the agent service. Everything is generated per run.
bootstrap_agent() {
    local password agent_id creds out
    password="$(openssl rand -hex 24)"
    mask "$password"
    log "Creating the admin user and the agent's key"
    # `api users add` logs the password it was given: keep its output out of the log unless
    # it fails (the password is generated for this run only).
    out="$(compose exec -T api /api users add -e "$ADMIN_EMAIL" -f Smoke -l Admin -p "$password" 2>&1)" || {
        printf '%s\n' "$out" >&2
        fail "could not create the admin user (/api users add)"
    }
    TOKEN="$(curl -sS -X POST "$API/api/auth/login" -H 'Content-Type: application/json' \
        -d "$(jq -cn --arg e "$ADMIN_EMAIL" --arg p "$password" '{email: $e, password: $p}')" |
        jq -r '.data.auth_token // empty')" || TOKEN=""
    [ -n "$TOKEN" ] || fail "could not log in as $ADMIN_EMAIL (POST /api/auth/login)"
    mask "$TOKEN"

    api POST /admin/agents -H 'Content-Type: application/json' \
        -d "$(jq -cn --arg n "$AGENT_NAME" '{name: $n, description: "stack smoke test", "is-active": true}')"
    case "$STATUS" in 200 | 201) ;; *) fail "POST /api/admin/agents returned $STATUS: $BODY" ;; esac
    agent_id="$(printf '%s' "$BODY" | jq -r '.data.id // empty')"
    [ -n "$agent_id" ] || fail "POST /api/admin/agents returned no id: $BODY"
    AGENT_ID="$agent_id"

    api POST "/admin/agents/$AGENT_ID/keys" -H 'Content-Type: application/json' \
        -d '{"name": "smoke-key", "never-expires": true}'
    # The body holds the key's secret: never print it.
    case "$STATUS" in 200 | 201) ;; *) fail "POST /api/admin/agents/$AGENT_ID/keys returned $STATUS" ;; esac
    creds="$(printf '%s' "$BODY" | jq -r '[.data."client-id" // "", .data."client-secret" // ""] | @tsv')"
    SMOKE_AGENT_CLIENT_ID="$(printf '%s' "$creds" | cut -f1)"
    SMOKE_AGENT_CLIENT_SECRET="$(printf '%s' "$creds" | cut -f2)"
    [ -n "$SMOKE_AGENT_CLIENT_ID" ] && [ -n "$SMOKE_AGENT_CLIENT_SECRET" ] ||
        fail "POST /api/admin/agents/$AGENT_ID/keys returned no credentials"
    mask "$SMOKE_AGENT_CLIENT_SECRET"
    BODY=""
    export SMOKE_AGENT_CLIENT_ID SMOKE_AGENT_CLIENT_SECRET
    log "agent '$AGENT_NAME' (id $AGENT_ID), key client-id $SMOKE_AGENT_CLIENT_ID"
}

# agent_heartbeated: true once the agent's instance is listed and a heartbeat from it was
# recorded (heartbeat-config-revision is set only by a heartbeat).
agent_heartbeated() {
    api GET "/admin/agents/$AGENT_ID/instances"
    [ "$STATUS" = "200" ] || return 1
    INSTANCE="$(printf '%s' "$BODY" | jq -c '[.data[]? | select(."heartbeat-config-revision" != null)][0] // empty')"
    [ -n "$INSTANCE" ]
}

check_agent() {
    log "Starting the agent"
    compose up -d --no-deps agent >/dev/null 2>&1 || fail "could not start the agent"
    log "Waiting for the agent's instance and a heartbeat (GET /api/admin/agents/$AGENT_ID/instances)"
    wait_for agent_heartbeated ||
        fail "no instance of the agent with a heartbeat in GET /api/admin/agents/$AGENT_ID/instances within ${TIMEOUT}s (last response: $STATUS $BODY)"
    log "agent: instance $(printf '%s' "$INSTANCE" | jq -r '"\(."instance-id") (version \(."agent-version" // "?"), mode \(.mode), status \(.status))"') registered and heartbeating"
}

# TODO(W3): plugin check. The W3 plugin step adds a plugin to smoke/agent-config.yml and checks
# here, with the stack still up, that its evidence reached the API.
check_plugin() {
    :
}

main() {
    check_settings
    SMOKE_JWT_SECRET="$(openssl rand -hex 32)"
    mask "$SMOKE_JWT_SECRET"
    export SMOKE_JWT_SECRET SMOKE_API_TAG SMOKE_UI_TAG SMOKE_AGENT_TAG
    TOKEN="" AGENT_ID="" STATUS="" BODY="" INSTANCE=""

    trap cleanup EXIT
    log "Stack: api $SMOKE_API_TAG, ui $SMOKE_UI_TAG, agent $SMOKE_AGENT_TAG (project $PROJECT)"
    log "Pulling the images"
    compose pull --quiet || fail "could not pull the images"
    started=1
    log "Starting postgres, the API and the UI"
    compose up -d postgres api ui || fail "could not start postgres, the API and the UI"

    check_api
    check_ui
    bootstrap_agent
    check_agent
    check_plugin
}

main "$@"
