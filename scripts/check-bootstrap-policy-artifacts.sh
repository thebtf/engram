#!/usr/bin/env bash
# Refuse public release publication unless GoReleaser raw clients equal package policy.
set -euo pipefail
policy="plugin/engram/bootstrap-targets.json"
dist="dist"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --policy) policy="${2:?}"; shift 2 ;;
    --dist) dist="${2:?}"; shift 2 ;;
    *) echo "usage: $0 [--policy PATH] [--dist PATH]" >&2; exit 2 ;;
  esac
done
[[ -f "$policy" && -d "$dist" ]] || { echo 'policy or split dist is missing' >&2; exit 1; }
version="$(node - "$policy" "$dist" <<'NODE'
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const [policyPath, dist] = process.argv.slice(2);
const policy = require('./plugin/engram/scripts/bootstrap-policy.js').parsePolicy(fs.readFileSync(policyPath, 'utf8'));
const expected = Object.values(policy.targets).map(({ desired }) => desired);
if (expected.length !== 3 || new Set(expected.map(({ asset }) => asset)).size !== 3) throw new Error('policy target matrix is not exact');
const files = [];
const walk = (dir) => { for (const entry of fs.readdirSync(dir, { withFileTypes: true })) { const file = path.join(dir, entry.name); entry.isDirectory() ? walk(file) : files.push(file); } };
walk(dist);
for (const item of expected) {
  const matches = files.filter((file) => path.basename(file) === item.asset);
  if (matches.length !== 1) throw new Error(`expected exactly one raw artifact named ${item.asset}`);
  const bytes = fs.readFileSync(matches[0]);
  const digest = crypto.createHash('sha256').update(bytes).digest('hex');
  if (bytes.length !== item.size || digest !== item.sha256) throw new Error(`policy mismatch for ${item.asset}: expected size=${item.size} sha256=${item.sha256}, got size=${bytes.length} sha256=${digest}`);
}
console.log(policy.package_version);
NODE
)"

archive_policy_entry() {
  local archive="$1" name entry="" count=0
  local -a list_command
  if [[ "$archive" == *.tar.gz ]]; then
    list_command=(tar -tzf "$archive")
  else
    list_command=(unzip -Z1 "$archive")
  fi
  while IFS= read -r name; do
    [[ "${name##*/}" == "bootstrap-targets.json" ]] || continue
    entry="$name"
    ((count += 1))
  done < <("${list_command[@]}")
  [[ "$count" -eq 1 ]] || { echo "archive must contain exactly one bootstrap-targets.json: $archive" >&2; return 1; }
  if [[ "$archive" == *.tar.gz ]]; then
    tar -xOzf "$archive" "$entry" | cmp -s "$policy" -
  else
    unzip -p "$archive" "$entry" | cmp -s "$policy" -
  fi || { echo "archive policy differs from committed policy: $archive" >&2; return 1; }
}

mapfile -t archives < <(find "$dist" -type f \( -name '*.tar.gz' -o -name '*.zip' \))
((${#archives[@]})) || { echo 'no release archives found for policy verification' >&2; exit 1; }
for archive in "${archives[@]}"; do archive_policy_entry "$archive"; done
bash "$(dirname "$0")/check-server-plugin-artifacts.sh" --version "$version" --dist "$dist"
