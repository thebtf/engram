#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 || -z "${DATABASE_DSN:-}" ]]; then
  echo 'usage: DATABASE_DSN=<isolated-test-db> check-release-server-workspace.sh <server-binary> <generated-static-dir> <unused-local-port>' >&2
  exit 2
fi
binary="$1"
static="$2"
port="$3"
workdir="$(mktemp -d)"
server_pid=''
cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf "$workdir"
}
trap cleanup EXIT

ENGRAM_AUTH_DISABLED=true ENGRAM_AUTH_ADMIN_TOKEN= ENGRAM_OPERATOR_CONSOLE_URL= \
  ENGRAM_WORKER_HOST=127.0.0.1 ENGRAM_WORKER_PORT="$port" \
  "$binary" > "$workdir/server.log" 2>&1 &
server_pid=$!
url="http://127.0.0.1:$port"
for _ in $(seq 1 80); do
  if curl --fail --silent --show-error --max-time 1 \
    -H 'Accept: text/html' "$url/code" -o "$workdir/index.html" 2> "$workdir/curl.log"; then
    break
  fi
  if ! kill -0 "$server_pid" 2>/dev/null; then
    echo 'standalone release server exited before serving /code' >&2
    exit 1
  fi
  sleep 0.5
done
if ! cmp -s "$static/index.html" "$workdir/index.html"; then
  echo '/code did not serve generated Vue Workspace HTML' >&2
  exit 1
fi
if ! grep -q 'id="app"' "$workdir/index.html"; then
  echo '/code is not a Vue Workspace page' >&2
  exit 1
fi
js="$(sed -nE 's/.*<script type="module"[^>]*src="(\/_nuxt\/[^" ]+\.js)".*/\1/p' "$workdir/index.html")"
css="$(sed -nE 's/.*<link rel="stylesheet"[^>]*href="(\/_nuxt\/[^" ]+\.css)".*/\1/p' "$workdir/index.html")"
for asset in "$js" "$css"; do
  if [[ -z "$asset" || ! -f "$static${asset}" ]]; then
    echo 'generated Workspace JS/CSS reference missing' >&2
    exit 1
  fi
  curl --fail --silent --show-error --max-time 5 "$url$asset" -o "$workdir/asset"
  if ! cmp -s "$static${asset}" "$workdir/asset"; then
    echo "server asset differs from generated source: $asset" >&2
    exit 1
  fi
  printf 'served %s (%s bytes)\n' "$asset" "$(wc -c < "$workdir/asset")"
done
printf 'served /code as canonical Vue Workspace HTML with byte-exact JS/CSS\n'
