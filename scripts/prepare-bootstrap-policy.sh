#!/usr/bin/env bash
# Generate (or verify) the package-bound client target policy from frozen inputs.
set -euo pipefail

version=""
output="plugin/engram/bootstrap-targets.json"
go_command="${ENGRAM_BOOTSTRAP_GO:-go}"
check=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) version="${2:?--version requires a value}"; shift 2 ;;
    --output) output="${2:?--output requires a value}"; shift 2 ;;
    --check) check=true; shift ;;
    *) printf 'usage: %s --version X.Y.Z [--output PATH] [--check]\n' "$0" >&2; exit 2 ;;
  esac
done
[[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]] || { echo 'version must be canonical SemVer without v' >&2; exit 2; }
for manifest in plugin/engram/.claude-plugin/plugin.json plugin/engram/.codex-plugin/plugin.json plugin/engram/.omp-plugin/plugin.json; do
  manifest_version="$(node -e 'const fs=require("node:fs"); const value=JSON.parse(fs.readFileSync(process.argv[1], "utf8")).version; if (typeof value !== "string") process.exit(1); process.stdout.write(value)' "$manifest")"
  [[ "$manifest_version" == "$version" ]] || { echo "package manifest version differs from policy version: $manifest" >&2; exit 1; }
done
[[ "$("$go_command" version | awk '{print $3}')" == 'go1.25.12' ]] || { echo 'requires Go 1.25.12' >&2; exit 1; }

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
build_target() {
  local goos="$1" goarch="$2" asset="$3"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 "$go_command" build -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/thebtf/engram/internal/version.Daemon=v${version}" \
    -o "$workdir/$asset" ./cmd/engram
}
build_target linux amd64 engram-linux-amd64
build_target darwin arm64 engram-darwin-arm64
build_target windows amd64 engram-windows-amd64.exe

candidate="$workdir/bootstrap-targets.json"
node - "$version" "$workdir" "$candidate" <<'NODE'
const crypto = require('node:crypto');
const fs = require('node:fs');
const { createPolicy } = require('./plugin/engram/scripts/bootstrap-policy.js');
const [version, dir, output] = process.argv.slice(2);
const target = (asset) => {
  const bytes = fs.readFileSync(`${dir}/${asset}`);
  return { version, asset, size: bytes.length, sha256: crypto.createHash('sha256').update(bytes).digest('hex') };
};
const policy = createPolicy(version, {
  'win32-x64': target('engram-windows-amd64.exe'),
  'linux-x64': target('engram-linux-amd64'),
  'darwin-arm64': target('engram-darwin-arm64'),
});
fs.writeFileSync(output, `${JSON.stringify(policy, null, 2)}\n`);
NODE

if "$check"; then
  cmp "$candidate" "$output" || { echo "committed policy differs; run $0 --version $version" >&2; exit 1; }
else
  mkdir -p "$(dirname "$output")"
  cp "$candidate" "$output"
fi
