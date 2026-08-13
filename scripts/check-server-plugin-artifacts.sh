#!/usr/bin/env bash
# Refuse publication unless every built server-plugin archive preserves the OMP payload.
set -euo pipefail

source_manifest="plugin/engram/package.json"
source_extension="plugin/engram/extensions/engram-memory.mjs"
dist="dist"
version=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dist) dist="${2:?}"; shift 2 ;;
    --version) version="${2:?}"; shift 2 ;;
    *) echo "usage: $0 --version VERSION [--dist PATH]" >&2; exit 2 ;;
  esac
done

[[ -n "$version" && -f "$source_manifest" && -f "$source_extension" && -d "$dist" ]] \
  || { echo 'release version, source OMP payload, or split dist is missing' >&2; exit 1; }

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

validate_manifest() {
  local manifest="$1"
  node - "$manifest" "$version" <<'NODE'
const fs = require('node:fs');
const [manifestPath, version] = process.argv.slice(2);
let manifest;
try { manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8')); } catch { throw new Error('OMP package manifest is not valid JSON'); }
if (!manifest || typeof manifest !== 'object' || Array.isArray(manifest) || manifest.version !== version || !manifest.omp || typeof manifest.omp !== 'object' || Array.isArray(manifest.omp) || Object.keys(manifest.omp).length !== 1 || !Array.isArray(manifest.omp.extensions) || manifest.omp.extensions.length !== 1 || manifest.omp.extensions[0] !== './extensions/engram-memory.mjs') {
  throw new Error('OMP package manifest does not match the release contract');
}
NODE
}

validate_manifest "$source_manifest"

expected_archives=(
  "engram_${version}_linux_amd64.tar.gz"
  "engram_${version}_darwin_arm64.tar.gz"
  "engram_${version}_windows_amd64.zip"
)
mapfile -t archives < <(find "$dist" -type f \( -name "engram_${version}_*.tar.gz" -o -name "engram_${version}_*.zip" \) ! -name '*_checksums.txt*' | sort)
if [[ "${#archives[@]}" -ne "${#expected_archives[@]}" ]]; then
  echo "server-plugin archive matrix mismatch: expected ${#expected_archives[@]}, found ${#archives[@]}" >&2
  exit 1
fi
for expected in "${expected_archives[@]}"; do
  matches=0
  for archive in "${archives[@]}"; do
    if [[ "${archive##*/}" == "$expected" ]]; then
      ((matches += 1))
    fi
  done
  [[ "$matches" -eq 1 ]] || { echo "server-plugin archive matrix is missing or duplicates $expected" >&2; exit 1; }
done

archive_entries() {
  local archive="$1"
  if [[ "$archive" == *.tar.gz ]]; then
    tar -tzf "$archive"
  else
    unzip -Z1 "$archive"
  fi
}

extract_entry() {
  local archive="$1" entry="$2" output="$3"
  if [[ "$archive" == *.tar.gz ]]; then
    tar -xOzf "$archive" "$entry" > "$output"
  else
    unzip -p "$archive" "$entry" > "$output"
  fi
}

for archive in "${archives[@]}"; do
  mapfile -t entries < <(archive_entries "$archive")
  for entry in package.json extensions/engram-memory.mjs; do
    matches=0
    for candidate in "${entries[@]}"; do
      if [[ "$candidate" == "$entry" ]]; then
        ((matches += 1))
      fi
    done
    [[ "$matches" -eq 1 ]] || { echo "archive must contain exactly one canonical $entry: $archive" >&2; exit 1; }
  done

  manifest="$work_dir/package.json"
  extension="$work_dir/engram-memory.mjs"
  extract_entry "$archive" package.json "$manifest"
  extract_entry "$archive" extensions/engram-memory.mjs "$extension"
  validate_manifest "$manifest"
  cmp -s "$source_manifest" "$manifest" || { echo "archive OMP package manifest differs from tagged source: $archive" >&2; exit 1; }
  cmp -s "$source_extension" "$extension" || { echo "archive OMP extension differs from tagged source: $archive" >&2; exit 1; }
done

printf 'verified OMP payload in %d server-plugin archive(s)\n' "${#archives[@]}"
