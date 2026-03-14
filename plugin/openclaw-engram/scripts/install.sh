#!/usr/bin/env bash
# Install openclaw-engram plugin into OpenClaw extensions directory.
# Usage: npx openclaw-engram-install
#   or:  bash <(curl -sL https://raw.githubusercontent.com/thebtf/engram/main/plugin/openclaw-engram/scripts/install.sh)

set -euo pipefail

EXTENSIONS_DIR="${OPENCLAW_EXTENSIONS_DIR:-${HOME}/.openclaw/extensions}"
PLUGIN_DIR="${EXTENSIONS_DIR}/engram"

echo "Installing openclaw-engram to ${PLUGIN_DIR}..."

mkdir -p "${PLUGIN_DIR}"
cd "${PLUGIN_DIR}"

# Create minimal package.json wrapper if not exists
if [ ! -f package.json ]; then
  cat > package.json << 'WRAP'
{
  "name": "openclaw-ext-engram",
  "version": "1.0.0",
  "private": true,
  "description": "OpenClaw extension wrapper for openclaw-engram plugin",
  "main": "node_modules/openclaw-engram/dist/index.js"
}
WRAP
fi

# Install the npm package (brings all runtime deps)
npm install openclaw-engram@latest --save 2>&1 | tail -3

# Symlink plugin manifest and dist to extension root (OpenClaw expects them here)
ln -sf node_modules/openclaw-engram/openclaw.plugin.json openclaw.plugin.json
ln -sf node_modules/openclaw-engram/dist dist

echo ""
echo "Installed openclaw-engram@$(node -p "require('openclaw-engram/package.json').version")"
echo "Restart gateway: openclaw gateway restart"
