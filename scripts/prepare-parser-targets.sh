#!/usr/bin/env bash
set -euo pipefail
version=""
output="plugin/engram/parser-targets.json"
check=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) version="${2:?}"; shift 2 ;;
    --output) output="${2:?}"; shift 2 ;;
    --check) check=true; shift ;;
    *) echo 'usage: prepare-parser-targets.sh --version X.Y.Z [--output PATH] [--check]' >&2; exit 2 ;;
  esac
done
[[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo 'invalid parser package version' >&2; exit 2; }
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC="${ENGRAM_PARSER_CC:-x86_64-w64-mingw32-gcc}" \
  "${ENGRAM_BOOTSTRAP_GO:-go}" build -trimpath -buildvcs=false -ldflags '-s -w -buildid=' \
  -o "$workdir/uci-parser-windows-amd64.exe" ./tools/uci-parser
node - "$version" "$workdir/uci-parser-windows-amd64.exe" "$workdir/parser-targets.json" <<'NODE'
const crypto = require('node:crypto');
const fs = require('node:fs');
const [version, file, output] = process.argv.slice(2);
const bytes = fs.readFileSync(file);
const policy = {
  schema_version: 1,
  package_version: version,
  targets: {
    'win32-x64': { version, asset: 'uci-parser-windows-amd64.exe', size: bytes.length, sha256: crypto.createHash('sha256').update(bytes).digest('hex') },
    'linux-x64': null,
    'darwin-arm64': null,
  },
};
fs.writeFileSync(output, `${JSON.stringify(policy, null, 2)}\n`);
NODE
if "$check"; then
  cmp "$workdir/parser-targets.json" "$output"
else
  cp "$workdir/parser-targets.json" "$output"
fi
