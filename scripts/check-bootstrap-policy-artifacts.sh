#!/usr/bin/env bash
# Refuse GoReleaser merge/upload unless split raw clients equal package policy.
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
node - "$policy" "$dist" <<'NODE'
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const [policyPath, dist] = process.argv.slice(2);
const policy = JSON.parse(fs.readFileSync(policyPath, 'utf8'));
const expected = Object.values(policy.targets).map(({ desired }) => desired);
if (expected.length !== 3 || new Set(expected.map(({ asset }) => asset)).size !== 3) throw new Error('policy target matrix is not exact');
const files = [];
const walk = (dir) => { for (const entry of fs.readdirSync(dir, { withFileTypes: true })) { const file = path.join(dir, entry.name); entry.isDirectory() ? walk(file) : files.push(file); } };
walk(dist);
for (const item of expected) {
  const matches = files.filter((file) => path.basename(file) === item.asset);
  if (matches.length !== 1) throw new Error(`expected exactly one split raw artifact named ${item.asset}`);
  const bytes = fs.readFileSync(matches[0]);
  const digest = crypto.createHash('sha256').update(bytes).digest('hex');
  if (bytes.length !== item.size || digest !== item.sha256) throw new Error(`policy mismatch for ${item.asset}`);
}
NODE
