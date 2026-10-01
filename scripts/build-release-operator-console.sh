#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
if [[ "$(uname -s)/$(uname -m)" != Linux/x86_64 ]]; then
  echo 'release console build requires Linux x86_64' >&2
  exit 1
fi

node_version=22.19.0
node_sha256=c0649af18e6a24f6fe5535a3e86b341dd49a8e71117c8b68bde973ef834f16f2
workdir="$(mktemp -d)"
trap 'rm -r -- "$workdir"' EXIT
archive="${ENGRAM_RELEASE_NODE_ARCHIVE:-$workdir/node.tar.xz}"
if [[ -z "${ENGRAM_RELEASE_NODE_ARCHIVE:-}" ]]; then
  curl --fail --silent --show-error --location \
    "https://nodejs.org/dist/v${node_version}/node-v${node_version}-linux-x64.tar.xz" \
    --output "$archive"
fi
printf '%s  %s\n' "$node_sha256" "$archive" | sha256sum --check --status || {
  echo 'release Node archive checksum mismatch' >&2
  exit 1
}
tar -xJf "$archive" -C "$workdir"
export PATH="$workdir/node-v${node_version}-linux-x64/bin:$PATH"
if [[ "$(node --version)" != "v${node_version}" || "$(node -p 'process.platform + "/" + process.arch')" != linux/x64 ]]; then
  echo 'release Node version or architecture mismatch' >&2
  exit 1
fi

(
  cd apps/operator-console
  npm ci
  npm run parity
  npm run build
  npm run generate
)
bash scripts/stage-release-operator-console.sh
