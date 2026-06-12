#!/bin/bash
# Unregister the engram plugin from Claude Code.
#
# Called by `make uninstall` and by uninstall.sh. Safe to run standalone
# when only the registry entries need removing without touching the file
# system layout (e.g. after a manual directory removal).
#
# Requires jq. Prints clear instructions when jq is absent.

set -e

PLUGINS_FILE="$HOME/.claude/plugins/installed_plugins.json"
SETTINGS_FILE="$HOME/.claude/settings.json"
MARKETPLACES_FILE="$HOME/.claude/plugins/known_marketplaces.json"
CACHE_DIR="$HOME/.claude/plugins/cache/engram"
PLUGIN_KEY="engram@engram"
MARKETPLACE_NAME="engram"

# jq is required for safe JSON mutation
if ! command -v jq &> /dev/null; then
    echo "Warning: jq not found. Remove plugin entries manually from:"
    echo "  - $PLUGINS_FILE  (key: $PLUGIN_KEY)"
    echo "  - $SETTINGS_FILE (keys: enabledPlugins.$PLUGIN_KEY, statusLine)"
    echo "  - $MARKETPLACES_FILE (key: $MARKETPLACE_NAME)"
    echo "  - $CACHE_DIR (directory)"
    echo "  - $HOME/.engram (data directory)"
    exit 1
fi

# ---------------------------------------------------------------------------
# installed_plugins.json
# ---------------------------------------------------------------------------
if [ -f "$PLUGINS_FILE" ]; then
    jq --arg key "$PLUGIN_KEY" 'del(.plugins[$key])' "$PLUGINS_FILE" \
        > "${PLUGINS_FILE}.tmp" && mv "${PLUGINS_FILE}.tmp" "$PLUGINS_FILE"
    echo "Plugin removed from installed_plugins.json"
else
    echo "No plugins file found, skipping"
fi

# ---------------------------------------------------------------------------
# settings.json — remove enabledPlugins entry and statusLine if ours
# ---------------------------------------------------------------------------
if [ -f "$SETTINGS_FILE" ]; then
    jq --arg key "$PLUGIN_KEY" '
        del(.enabledPlugins[$key]) |
        if .statusLine.command and (.statusLine.command | contains("engram")) then
            del(.statusLine)
        else
            .
        end
    ' "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp" \
        && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
    echo "Plugin removed from settings.json"
fi

# ---------------------------------------------------------------------------
# known_marketplaces.json
# ---------------------------------------------------------------------------
if [ -f "$MARKETPLACES_FILE" ]; then
    jq --arg key "$MARKETPLACE_NAME" 'del(.[$key])' "$MARKETPLACES_FILE" \
        > "${MARKETPLACES_FILE}.tmp" && mv "${MARKETPLACES_FILE}.tmp" "$MARKETPLACES_FILE"
    echo "Marketplace removed from known_marketplaces.json"
fi

# ---------------------------------------------------------------------------
# Cache directory and data directory
# ---------------------------------------------------------------------------
if [ -d "$CACHE_DIR" ]; then
    rm -rf "$CACHE_DIR"
    echo "Cache directory removed"
fi

DATA_DIR="$HOME/.engram"
if [ -d "$DATA_DIR" ]; then
    rm -rf "$DATA_DIR"
    echo "Data directory removed ($DATA_DIR)"
fi

echo "Plugin unregistered successfully"
