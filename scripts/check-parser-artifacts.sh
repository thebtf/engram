#!/usr/bin/env bash
set -euo pipefail
dist="${1:-dist}"
parser_policy="${ENGRAM_PARSER_POLICY:-plugin/engram/parser-targets.json}"
version="${2:?parser package version is required}"
node - "$dist" "$parser_policy" "$version" <<'NODE'
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const [dist, parserPolicy, version] = process.argv.slice(2);
const policy = require('./plugin/engram/scripts/ensure-binary.js').loadParserTarget(path.dirname(parserPolicy), version, 'win32-x64');
const found = [];
const walk = dir => { for (const entry of fs.readdirSync(dir, { withFileTypes: true })) { const file = path.join(dir, entry.name); entry.isDirectory() ? walk(file) : (entry.name === policy.asset && found.push(file)); } };
walk(dist);
if (found.length !== 1) throw new Error('expected exactly one parser release asset');
const bytes = fs.readFileSync(found[0]);
if (bytes.length !== policy.size || crypto.createHash('sha256').update(bytes).digest('hex') !== policy.sha256) throw new Error('parser release asset differs from policy');
NODE
found_archive=false
while IFS= read -r -d '' archive; do
  found_archive=true
  entry=""
  count=0
  if [[ "$archive" == *.tar.gz ]]; then
    while IFS= read -r name; do
      [[ "${name##*/}" == "parser-targets.json" ]] || continue
      entry="$name"; ((count += 1))
    done < <(tar -tzf "$archive")
    [[ "$count" == 1 ]] && tar -xOzf "$archive" "$entry" | cmp -s "$parser_policy" -
  else
    while IFS= read -r name; do
      [[ "${name##*/}" == "parser-targets.json" ]] || continue
      entry="$name"; ((count += 1))
    done < <(unzip -Z1 "$archive")
    [[ "$count" == 1 ]] && unzip -p "$archive" "$entry" | cmp -s "$parser_policy" -
  fi || { echo "parser policy missing or changed in $archive" >&2; exit 1; }
done < <(find "$dist" -type f \( -name '*.tar.gz' -o -name '*.zip' \) -print0)
"$found_archive" || { echo 'no parser package archives were found' >&2; exit 1; }
