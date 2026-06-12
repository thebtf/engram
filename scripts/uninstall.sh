#!/bin/bash
# engram uninstaller — macOS / Linux / Git-Bash
#
# Removes plugin files, cache, and Claude Code registry entries.
# Optionally preserves the data directory (~/.engram/).
#
# Usage:
#   curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/uninstall.sh | bash
#
# Options:
#   --keep-data    Preserve ~/.engram/ (database, embeddings)
#   --purge        Remove everything including data (default)

set -e

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------
INSTALL_DIR="$HOME/.claude/plugins/marketplaces/engram"
CACHE_DIR="$HOME/.claude/plugins/cache/engram"
DATA_DIR="$HOME/.engram"
PLUGINS_FILE="$HOME/.claude/plugins/installed_plugins.json"
SETTINGS_FILE="$HOME/.claude/settings.json"
MARKETPLACES_FILE="$HOME/.claude/plugins/known_marketplaces.json"
PLUGIN_KEY="engram@engram"

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info()    { echo -e "${BLUE}[INFO]${NC} $1"; }
success() { echo -e "${GREEN}[OK]${NC} $1"; }
warn()    { echo -e "${YELLOW}[WARN]${NC} $1"; }

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
KEEP_DATA=false
for arg in "$@"; do
    case "$arg" in
        --keep-data) KEEP_DATA=true  ;;
        --purge)     KEEP_DATA=false ;;
    esac
done

echo ""
echo "╔═══════════════════════════════════════════════════════════╗"
echo "║         Engram - Uninstallation Script                   ║"
echo "╚═══════════════════════════════════════════════════════════╝"
echo ""

# ---------------------------------------------------------------------------
# Stop any running server process before removing files
# ---------------------------------------------------------------------------
info "Stopping server processes..."
pkill -9 -f 'engram-server' 2>/dev/null || true
pkill -9 -f '\.claude/plugins/.*/engram-server' 2>/dev/null || true

# Port 37777 — use whichever tool is available
if command -v lsof &> /dev/null; then
    lsof -ti :37777 | xargs kill -9 2>/dev/null || true
elif command -v ss &> /dev/null; then
    ss -tlnp 'sport = :37777' 2>/dev/null \
        | awk 'NR>1 {print $6}' \
        | grep -oP 'pid=\K[0-9]+' \
        | xargs -r kill -9 2>/dev/null || true
elif command -v fuser &> /dev/null; then
    fuser -k 37777/tcp 2>/dev/null || true
fi
sleep 1
success "Server processes stopped"

# ---------------------------------------------------------------------------
# Remove plugin and cache directories
# ---------------------------------------------------------------------------
info "Removing plugin directories..."
if [[ -d "$INSTALL_DIR" ]]; then
    rm -rf "$INSTALL_DIR"
    success "Removed $INSTALL_DIR"
else
    info "Plugin directory not found (already removed)"
fi

if [[ -d "$CACHE_DIR" ]]; then
    rm -rf "$CACHE_DIR"
    success "Removed $CACHE_DIR"
fi

# ---------------------------------------------------------------------------
# Clean up Claude Code registry entries
# ---------------------------------------------------------------------------
if command -v jq &> /dev/null; then
    info "Cleaning up Claude Code configuration..."

    if [[ -f "$PLUGINS_FILE" ]]; then
        jq 'del(.plugins["'"$PLUGIN_KEY"'"])' "$PLUGINS_FILE" \
            > "${PLUGINS_FILE}.tmp" && mv "${PLUGINS_FILE}.tmp" "$PLUGINS_FILE"
        success "Removed from installed_plugins.json"
    fi

    if [[ -f "$SETTINGS_FILE" ]]; then
        jq 'del(.enabledPlugins["'"$PLUGIN_KEY"'"]) |
            if .statusLine.command | test("engram") then del(.statusLine) else . end' \
            "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp" \
            && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
        success "Removed from settings.json (including statusline)"
    fi

    if [[ -f "$MARKETPLACES_FILE" ]]; then
        jq 'del(.["engram"])' "$MARKETPLACES_FILE" \
            > "${MARKETPLACES_FILE}.tmp" && mv "${MARKETPLACES_FILE}.tmp" "$MARKETPLACES_FILE"
        success "Removed from known_marketplaces.json"
    fi
else
    warn "jq not found — configuration files not cleaned up"
    warn "Remove engram entries manually from:"
    warn "  - $PLUGINS_FILE"
    warn "  - $SETTINGS_FILE"
    warn "  - $MARKETPLACES_FILE"
fi

# ---------------------------------------------------------------------------
# Handle data directory
# ---------------------------------------------------------------------------
if [[ -d "$DATA_DIR" ]]; then
    if [[ "$KEEP_DATA" == "true" ]]; then
        warn "Keeping data directory: $DATA_DIR"
        warn "To remove it later: rm -rf $DATA_DIR"
    else
        info "Removing data directory..."
        rm -rf "$DATA_DIR"
        success "Removed $DATA_DIR"
    fi
fi

echo ""
echo "╔═══════════════════════════════════════════════════════════╗"
echo "║              Uninstallation Complete!                    ║"
echo "╚═══════════════════════════════════════════════════════════╝"
echo ""

if [[ "$KEEP_DATA" == "true" ]]; then
    echo "  Data preserved at: $DATA_DIR"
    echo "  To reinstall: curl -sSL .../install.sh | bash"
    echo ""
fi

success "Engram has been uninstalled"
