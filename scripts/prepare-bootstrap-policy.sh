#!/usr/bin/env bash
# Generate (or verify) the package-bound client target policy from frozen inputs.
set -euo pipefail

version=""
output="plugin/engram/bootstrap-targets.json"
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
[[ "$(go version | awk '{print $3}')" == 'go1.25.12' ]] || { echo 'requires Go 1.25.12' >&2; exit 1; }

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
build_target() {
  local goos="$1" goarch="$2" asset="$3"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -buildvcs=false \
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
const [version, dir, output] = process.argv.slice(2);
const target = (asset) => {
  const bytes = fs.readFileSync(`${dir}/${asset}`);
  return { version, asset, size: bytes.length, sha256: crypto.createHash('sha256').update(bytes).digest('hex') };
};
const policy = {
  schema_version: 1,
  launcher_security_epoch: 1,
  package_version: version,
  daemon_compat_epoch: 1,
  targets: {
    'win32-x64': { desired: target('engram-windows-amd64.exe'), predecessor: null },
    'linux-x64': { desired: target('engram-linux-amd64'), predecessor: null },
    'darwin-arm64': { desired: target('engram-darwin-arm64'), predecessor: null },
  },
  revoked_sha256: [],
  build_contract: {
    go_version: '1.25.12', trimpath: true, buildvcs: false, client_cgo: false,
    daemon_version_ldflag: `v${version}`,
  },
};
for (const entry of Object.values(policy.targets)) {
  if (!Number.isSafeInteger(entry.desired.size) || entry.desired.size <= 0 || !/^[0-9a-f]{64}$/.test(entry.desired.sha256)) process.exit(1);
}
fs.writeFileSync(output, `${JSON.stringify(policy, null, 2)}\n`);
NODE

if "$check"; then
  cmp "$candidate" "$output" || { echo "committed policy differs; run $0 --version $version" >&2; exit 1; }
else
  mkdir -p "$(dirname "$output")"
  cp "$candidate" "$output"
fi
