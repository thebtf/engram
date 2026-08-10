#!/usr/bin/env bash
# Check GitHub's post-publication raw client asset metadata against package policy.
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
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] || { echo 'tag must be canonical vSemVer' >&2; exit 2; }
: "${GITHUB_TOKEN:?GITHUB_TOKEN is required}"
release="$(mktemp)"
trap 'rm -f "$release"' EXIT
curl --fail --silent --show-error --location \
  -H "Authorization: Bearer ${GITHUB_TOKEN}" \
  -H 'Accept: application/vnd.github+json' \
  "https://api.github.com/repos/thebtf/engram/releases/tags/${tag}" > "$release"
node - "$policy" "$release" <<'NODE'
const fs = require('node:fs');
const [policyPath, releasePath] = process.argv.slice(2);
const policy = JSON.parse(fs.readFileSync(policyPath, 'utf8'));
const release = JSON.parse(fs.readFileSync(releasePath, 'utf8'));
const expected = Object.values(policy.targets).map(({ desired }) => desired);
if (expected.length !== 3 || !Array.isArray(release.assets)) throw new Error('malformed policy or GitHub response');
for (const item of expected) {
  const matches = release.assets.filter((asset) => asset.name === item.asset);
  if (matches.length !== 1) throw new Error(`expected exactly one published ${item.asset}`);
  const asset = matches[0];
  if (asset.size !== item.size || asset.digest !== `sha256:${item.sha256}`) throw new Error(`published asset mismatch for ${item.asset}`);
}
NODE
