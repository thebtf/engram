#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
static=internal/worker/static
public=apps/operator-console/.output/public
if [[ ! -f "$static/placeholder.html" || -L "$static/placeholder.html" ]] ||
   [[ -n "$(find "$static" -mindepth 1 ! -path "$static/placeholder.html" -print -quit)" ]]; then
  echo 'release static directory contains unexpected content; refusing to overwrite it' >&2
  exit 1
fi
if [[ ! -s "$public/index.html" ]]; then
  echo 'generated operator-console index.html is missing' >&2
  exit 1
fi
cp -a "$public"/. "$static"/
