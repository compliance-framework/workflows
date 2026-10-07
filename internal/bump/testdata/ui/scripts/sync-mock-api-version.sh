#!/usr/bin/env bash
# Writes the mock-api release tag the UI targets into src/api-version.json.
# Stands in for ui's scripts/sync-agentconfig-conformance.sh so ccf-bump's ui updater has
# something to run against the mocks. Offline: it does not check the tag exists.
#
#   scripts/sync-mock-api-version.sh <tag>    e.g. v1.2.3 or v1.2.3-rc1
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <tag>   (vX.Y.Z or vX.Y.Z-rcN)" >&2
  exit 2
fi
tag="$1"
if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-rc[0-9]+)?$ ]]; then
  echo "error: '$tag' is not a release tag (vX.Y.Z or vX.Y.Z-rcN)" >&2
  exit 1
fi

cd "$(dirname "$0")/.."
file="src/api-version.json"
printf '{\n  "mockApi": "%s"\n}\n' "$tag" > "$file"
echo "Wrote mock-api ${tag} to ${file}."
