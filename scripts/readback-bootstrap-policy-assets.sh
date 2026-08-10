#!/usr/bin/env bash
# Check GitHub's uploaded release assets (including a private draft) against package policy.
set -euo pipefail
policy="plugin/engram/bootstrap-targets.json"
tag=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --policy) policy="${2:?}"; shift 2 ;;
    --tag) tag="${2:?}"; shift 2 ;;
    *) echo "usage: $0 --tag vX.Y.Z [--policy PATH]" >&2; exit 2 ;;
  esac
done
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]] || { echo 'tag must be canonical vSemVer' >&2; exit 2; }
: "${GITHUB_TOKEN:?GITHUB_TOKEN is required}"
max_attempts=5
request_timeout_seconds=5
retry_delay_seconds=10
release="$(mktemp)"
trap 'rm -f "$release"' EXIT
for ((attempt = 1; attempt <= max_attempts; attempt++)); do
  curl --fail --silent --show-error --location \
    --max-time "$request_timeout_seconds" \
    -H "Authorization: Bearer ${GITHUB_TOKEN}" \
    -H 'Accept: application/vnd.github+json' \
    "https://api.github.com/repos/thebtf/engram/releases?per_page=100" > "$release"
  if verifier_error="$(node "$(dirname "${BASH_SOURCE[0]}")/verify-bootstrap-release-assets.js" "$policy" "$release" "$tag" 2>&1 >/dev/null)"; then
    exit 0
  fi
  if (( attempt == max_attempts )); then
    printf '%s\n' "$verifier_error" >&2
    exit 1
  fi
  sleep "$retry_delay_seconds"
done
