#!/usr/bin/env bash
# A test stand-in for ui's scripts/sync-agentconfig-conformance.sh, offline: it writes the api ref
# it is given into the fixture, where the real script downloads api's conformance.json at that ref.
#
#   scripts/sync-agentconfig-conformance.sh <ref>    e.g. v1.2.3
set -euo pipefail

ref="${1:-main}"
fixture="src/utils/agent-config/__tests__/fixtures/agentconfig-conformance.json"
cd "$(dirname "$0")/.."
printf '{"api": "%s", "cases": []}\n' "$ref" > "$fixture"
echo "Wrote api@${ref}'s cases to $fixture."
