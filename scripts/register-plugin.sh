#!/bin/bash
# Register the engram plugin with Claude Code.
#
# Called by `make install` after files have been copied to the marketplace
# directory. Can also be run standalone to repair a broken registration.
#
# Usage:
#   ./scripts/register-plugin.sh [VERSION]
#
# VERSION defaults to the nearest git tag (same logic as the Makefile).

set -e

PLUGINS_FILE="$HOME/.claude/plugins/installed_plugins.json"
SETTINGS_FILE="$HOME/.claude/settings.json"
MARKETPLACES_FILE="$HOME/.claude/plugins/known_marketplaces.json"
PLUGIN_KEY="engram@engram"
MARKETPLACE_NAME="engram"
MARKETPLACE_PATH="$HOME/.claude/plugins/marketplaces/engram"

# Use the provided argument, or fall back to the git-derived tag
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo "dev")}"
CACHE_BASE="$HOME/.claude/plugins/cache/engram/engram"
CACHE_PATH="$CACHE_BASE/$VERSION"
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")

mkdir -p "$HOME/.claude/plugins"

# Preserve stale cache versions: already-running sessions may have hook
# commands pointing at their versioned cache path until they restart.
# installed_plugins.json below points new sessions at CACHE_PATH, so old
# cache slots no longer shadow the new plugin.
if [ -d "$CACHE_BASE" ]; then
    echo "Preserving old cache versions for running-session hook compatibility..."
fi

# Bootstrap JSON files when they do not exist yet
[ ! -f "$PLUGINS_FILE" ]      && echo '{"version": 2, "plugins": {}}' > "$PLUGINS_FILE"
[ ! -f "$SETTINGS_FILE" ]     && echo '{}' > "$SETTINGS_FILE"
[ ! -f "$MARKETPLACES_FILE" ] && echo '{}' > "$MARKETPLACES_FILE"

# jq is required — manual fallback message printed below if absent
if ! command -v jq &> /dev/null; then
    echo "ERROR: jq is required for plugin registration"
    echo "Install jq: brew install jq (macOS) / apt-get install jq (Linux)"
    exit 1
fi

mkdir -p "$CACHE_PATH/.claude-plugin" "$CACHE_PATH/hooks" "$CACHE_PATH/commands"
cp -r "$MARKETPLACE_PATH/"* "$CACHE_PATH/" 2>/dev/null || true

# ---------------------------------------------------------------------------
# installed_plugins.json — record install path, version, and timestamps
# ---------------------------------------------------------------------------
PLUGIN_ENTRY=$(cat <<EOF
[{
    "scope": "user",
    "installPath": "$CACHE_PATH",
    "version": "$VERSION",
    "installedAt": "$TIMESTAMP",
    "lastUpdated": "$TIMESTAMP",
    "isLocal": true
}]
EOF
)

jq --arg key "$PLUGIN_KEY" --argjson entry "$PLUGIN_ENTRY" \
    '.plugins[$key] = $entry' "$PLUGINS_FILE" > "${PLUGINS_FILE}.tmp" \
    && mv "${PLUGINS_FILE}.tmp" "$PLUGINS_FILE"
echo "Plugin registered in installed_plugins.json"

# ---------------------------------------------------------------------------
# settings.json — enable plugin and wire statusline
# ---------------------------------------------------------------------------
STATUSLINE_CMD="node \"$MARKETPLACE_PATH/hooks/statusline.js\""
STATUSLINE_ENTRY=$(cat <<EOF
{
    "type": "command",
    "command": "$STATUSLINE_CMD",
    "padding": 0
}
EOF
)

jq --arg key "$PLUGIN_KEY" --argjson statusline "$STATUSLINE_ENTRY" \
    '.enabledPlugins //= {} | .enabledPlugins[$key] = true | .statusLine = $statusline' \
    "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp" \
    && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
echo "Plugin enabled in settings.json"
echo "Statusline configured in settings.json"

# ---------------------------------------------------------------------------
# known_marketplaces.json — register local directory as the source
# ---------------------------------------------------------------------------
MARKETPLACE_ENTRY=$(cat <<EOF
{
    "source": {
        "source": "directory",
        "path": "$MARKETPLACE_PATH"
    },
    "installLocation": "$MARKETPLACE_PATH",
    "lastUpdated": "$TIMESTAMP"
}
EOF
)

jq --arg key "$MARKETPLACE_NAME" --argjson entry "$MARKETPLACE_ENTRY" \
    '.[$key] = $entry' "$MARKETPLACES_FILE" > "${MARKETPLACES_FILE}.tmp" \
    && mv "${MARKETPLACES_FILE}.tmp" "$MARKETPLACES_FILE"
echo "Marketplace registered in known_marketplaces.json"

# ---------------------------------------------------------------------------
# MCP server registration (optional — only when the binary is present)
# ---------------------------------------------------------------------------
MCP_BINARY="$MARKETPLACE_PATH/mcp-server"
if [ -f "$MCP_BINARY" ]; then
    echo "Registering MCP server in settings.json..."

    # The ${CLAUDE_PROJECT} placeholder is intentionally literal — Claude Code
    # expands it at runtime, not here.
    MCP_ENTRY=$(cat <<'EOF'
{
    "command": "MCP_BINARY_PLACEHOLDER",
    "args": ["--project", "${CLAUDE_PROJECT}"],
    "env": {}
}
EOF
)
    MCP_ENTRY=$(echo "$MCP_ENTRY" | sed "s|MCP_BINARY_PLACEHOLDER|$MCP_BINARY|g")

    if jq --arg key "engram" --argjson entry "$MCP_ENTRY" \
        '.mcpServers //= {} | .mcpServers[$key] = $entry' \
        "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp"; then
        mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
        echo "MCP server registered successfully"
    else
        echo "Warning: Failed to register MCP server (jq error)"
        rm -f "${SETTINGS_FILE}.tmp"
    fi
else
    echo "MCP server binary not found at $MCP_BINARY, skipping MCP registration"
fi

echo "Plugin registered successfully"
