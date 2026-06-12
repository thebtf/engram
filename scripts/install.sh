#!/bin/bash
# engram installer — macOS / Linux / Git-Bash
#
# One-shot install from GitHub releases:
#   curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash
#
# Pin a specific release tag:
#   curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash -s -- v1.0.0
#
# Flags (order-independent):
#   --full           install server binary in addition to plugin files
#   --client-only    install plugin files only (default)
#   --register-only  re-run plugin registration for an already-downloaded release
#   --uninstall      remove plugin and (unless --keep-data) data directory
#   --keep-data      paired with --uninstall: preserve ~/.engram

set -e

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
INSTALL_MODE="client-only"
VERSION_ARG=""
FLAG_REGISTER_ONLY=false
FLAG_UNINSTALL=false
FLAG_KEEP_DATA=false

for arg in "$@"; do
    case "$arg" in
        --full)          INSTALL_MODE="full" ;;
        --client-only)   INSTALL_MODE="client-only" ;;
        --register-only) FLAG_REGISTER_ONLY=true ;;
        --uninstall)     FLAG_UNINSTALL=true ;;
        --keep-data)     FLAG_KEEP_DATA=true ;;
        --*)             ;;
        *)               VERSION_ARG="${VERSION_ARG:-$arg}" ;;
    esac
done

# ---------------------------------------------------------------------------
# Paths — all derived from $HOME so they survive sudo-less installs
# ---------------------------------------------------------------------------
GITHUB_REPO="thebtf/engram"
INSTALL_DIR="$HOME/.claude/plugins/marketplaces/engram"
CACHE_DIR="$HOME/.claude/plugins/cache/engram/engram"
PLUGINS_FILE="$HOME/.claude/plugins/installed_plugins.json"
SETTINGS_FILE="$HOME/.claude/settings.json"
MARKETPLACES_FILE="$HOME/.claude/plugins/known_marketplaces.json"
PLUGIN_KEY="engram@engram"

# ---------------------------------------------------------------------------
# Terminal output helpers
# ---------------------------------------------------------------------------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info()    { echo -e "${BLUE}[INFO]${NC} $1"; }
success() { echo -e "${GREEN}[OK]${NC} $1"; }
warn()    { echo -e "${YELLOW}[WARN]${NC} $1"; }
error()   { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# ---------------------------------------------------------------------------
# Platform detection
# ---------------------------------------------------------------------------
detect_platform() {
    local os arch

    case "$(uname -s)" in
        Darwin)             os="darwin" ;;
        Linux)              os="linux" ;;
        MINGW*|MSYS*|CYGWIN*) os="windows" ;;
        *)                  error "Unsupported operating system: $(uname -s)" ;;
    esac

    case "$(uname -m)" in
        x86_64|amd64)  arch="amd64" ;;
        arm64|aarch64) arch="arm64" ;;
        *)             error "Unsupported architecture: $(uname -m)" ;;
    esac

    # CGO cross-compilation is not available for linux/arm64
    if [[ "$os" == "linux" && "$arch" == "arm64" ]]; then
        error "Linux ARM64 is not currently supported due to CGO cross-compilation limitations"
    fi

    echo "${os}_${arch}"
}

# ---------------------------------------------------------------------------
# Fetch latest release tag from GitHub API
# ---------------------------------------------------------------------------
get_latest_version() {
    local curl_opts response version

    curl_opts=(-sS)
    # Higher rate limit when a token is present
    if [[ -n "${GITHUB_TOKEN:-}" ]]; then
        curl_opts+=(-H "Authorization: token ${GITHUB_TOKEN}")
    fi

    response=$(curl "${curl_opts[@]}" "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>&1)

    if echo "$response" | grep -q "API rate limit exceeded"; then
        error "GitHub API rate limit exceeded.

Options:
  1. Wait ~1 hour for the limit to reset
  2. Specify a version explicitly:
       curl -sSL https://raw.githubusercontent.com/${GITHUB_REPO}/main/scripts/install.sh | bash -s -- v0.6.1
  3. Export GITHUB_TOKEN to raise the limit
  4. Clone and build from source:
       git clone https://github.com/${GITHUB_REPO}.git && cd engram && make build && make install"
    fi

    if echo "$response" | grep -q '"message":'; then
        local msg
        msg=$(echo "$response" | grep '"message":' | sed -E 's/.*"message": *"([^"]+)".*/\1/')
        error "GitHub API error: $msg"
    fi

    version=$(echo "$response" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

    if [[ -z "$version" ]]; then
        error "Could not parse release tag from GitHub API. Response: $response"
    fi

    echo "$version"
}

# ---------------------------------------------------------------------------
# Download release archive and lay out files into INSTALL_DIR
# ---------------------------------------------------------------------------
download_release() {
    local version="$1"
    local platform="$2"
    local tmp_dir archive_ext archive_name download_url

    tmp_dir=$(mktemp -d)
    trap "rm -rf $tmp_dir" EXIT

    # Windows releases use zip; everything else uses tar.gz
    archive_ext="tar.gz"
    if [[ "$platform" == windows_* ]]; then
        archive_ext="zip"
    fi

    archive_name="engram_${version#v}_${platform}.${archive_ext}"
    download_url="https://github.com/${GITHUB_REPO}/releases/download/${version}/${archive_name}"

    info "Downloading ${archive_name}..."
    if ! curl -sSL -o "$tmp_dir/release.${archive_ext}" "$download_url"; then
        error "Download failed: $download_url"
    fi

    info "Extracting archive..."
    if [[ "$archive_ext" == "zip" ]]; then
        unzip -q "$tmp_dir/release.zip" -d "$tmp_dir" || error "Failed to extract zip"
    else
        tar -xzf "$tmp_dir/release.tar.gz" -C "$tmp_dir" || error "Failed to extract tar.gz"
    fi

    info "Installing to ${INSTALL_DIR}..."
    mkdir -p "$INSTALL_DIR/hooks" "$INSTALL_DIR/.claude-plugin" "$INSTALL_DIR/commands"

    # Server binary is only included in --full installs
    if [[ "$INSTALL_MODE" == "full" ]]; then
        cp "$tmp_dir/engram-server" "$INSTALL_DIR/" 2>/dev/null || true
    fi

    # JS hooks are mandatory — the plugin cannot function without them
    cp "$tmp_dir/hooks/"*.js "$INSTALL_DIR/hooks/" \
        || error "Failed to copy JS hooks from $tmp_dir/hooks/"
    cp "$tmp_dir/hooks/hooks.json" "$INSTALL_DIR/hooks/" \
        || error "Failed to copy hooks.json from $tmp_dir/hooks/"

    cp "$tmp_dir/.claude-plugin/"* "$INSTALL_DIR/.claude-plugin/"

    if [[ -d "$tmp_dir/commands" ]]; then
        cp -r "$tmp_dir/commands/"* "$INSTALL_DIR/commands/" 2>/dev/null || true
    fi

    if [[ -d "$tmp_dir/skills" ]]; then
        mkdir -p "$INSTALL_DIR/skills"
        cp -r "$tmp_dir/skills/"* "$INSTALL_DIR/skills/" 2>/dev/null || true
    fi

    if [[ -f "$tmp_dir/.mcp.json" ]]; then
        cp "$tmp_dir/.mcp.json" "$INSTALL_DIR/.mcp.json"
    fi

    # Claude-specific transport config (referenced by .claude-plugin/plugin.json)
    if [[ -f "$tmp_dir/claude/.mcp.json" ]]; then
        mkdir -p "$INSTALL_DIR/claude"
        cp "$tmp_dir/claude/.mcp.json" "$INSTALL_DIR/claude/.mcp.json"
    fi

    if [[ "$INSTALL_MODE" == "full" ]]; then
        chmod +x "$INSTALL_DIR/engram-server" 2>/dev/null || true
    fi

    success "Files installed to ${INSTALL_DIR}"
}

# ---------------------------------------------------------------------------
# Write plugin metadata into Claude Code's JSON registry files
# ---------------------------------------------------------------------------
register_plugin() {
    local version="$1"
    local timestamp cache_base cache_path plugin_entry statusline_cmd statusline_entry marketplace_entry

    timestamp=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")

    mkdir -p "$HOME/.claude/plugins"

    # Remove stale cache versions so old binaries cannot shadow the new one
    cache_base=$(dirname "$CACHE_DIR")
    if [[ -d "$cache_base" ]]; then
        info "Removing old cache versions..."
        find "$cache_base" -mindepth 1 -maxdepth 1 -type d \
            ! -name "${version#v}" -exec rm -rf {} \; 2>/dev/null || true
    fi

    cache_path="${CACHE_DIR}/${version}"
    mkdir -p "${cache_path}"

    # Bootstrap JSON files when they do not exist yet
    [[ ! -f "$PLUGINS_FILE" ]]      && echo '{"version": 2, "plugins": {}}' > "$PLUGINS_FILE"
    [[ ! -f "$SETTINGS_FILE" ]]     && echo '{}' > "$SETTINGS_FILE"
    [[ ! -f "$MARKETPLACES_FILE" ]] && echo '{}' > "$MARKETPLACES_FILE"

    if ! command -v jq &> /dev/null; then
        warn "jq is not installed — plugin registration requires jq."
        warn "Install jq: brew install jq (macOS) / apt-get install jq (Linux)"
        warn "Then re-run: $0 --register-only"
        return 1
    fi

    mkdir -p "$cache_path/.claude-plugin" "$cache_path/hooks" "$cache_path/commands"
    cp -r "$INSTALL_DIR/"* "$cache_path/" 2>/dev/null || true

    # installed_plugins.json — record install path, version, and timestamp
    plugin_entry=$(cat <<EOF
[{
    "scope": "user",
    "installPath": "$cache_path",
    "version": "${version#v}",
    "installedAt": "$timestamp",
    "lastUpdated": "$timestamp",
    "isLocal": true
}]
EOF
)
    jq --arg key "$PLUGIN_KEY" --argjson entry "$plugin_entry" \
        '.plugins[$key] = $entry' "$PLUGINS_FILE" > "${PLUGINS_FILE}.tmp" \
        && mv "${PLUGINS_FILE}.tmp" "$PLUGINS_FILE"
    success "Plugin registered in installed_plugins.json"

    # settings.json — enable plugin and wire the statusline command
    statusline_cmd="node \"$INSTALL_DIR/hooks/statusline.js\""
    statusline_entry=$(cat <<EOF
{
    "type": "command",
    "command": "$statusline_cmd",
    "padding": 0
}
EOF
)
    jq --arg key "$PLUGIN_KEY" --argjson statusline "$statusline_entry" \
        '.enabledPlugins //= {} | .enabledPlugins[$key] = true | .statusLine = $statusline' \
        "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp" \
        && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
    success "Plugin enabled in settings.json"
    success "Statusline configured in settings.json"

    # known_marketplaces.json — register local directory as the source
    marketplace_entry=$(cat <<EOF
{
    "source": {
        "source": "directory",
        "path": "$INSTALL_DIR"
    },
    "installLocation": "$INSTALL_DIR",
    "lastUpdated": "$timestamp"
}
EOF
)
    jq --arg key "engram" --argjson entry "$marketplace_entry" \
        '.[$key] = $entry' "$MARKETPLACES_FILE" > "${MARKETPLACES_FILE}.tmp" \
        && mv "${MARKETPLACES_FILE}.tmp" "$MARKETPLACES_FILE"
    success "Marketplace registered in known_marketplaces.json"

    # MCP transport is declared in the plugin's .mcp.json — no settings.json edit needed
}

# ---------------------------------------------------------------------------
# Interactive prompt for server URL and API token
# ---------------------------------------------------------------------------
setup_connection() {
    echo ""
    info "Engram stores memories on a remote server. Configure the connection:"
    echo ""

    local default_url="http://localhost:37777/mcp"
    read -p "  Server URL [${default_url}]: " ENGRAM_URL
    ENGRAM_URL="${ENGRAM_URL:-$default_url}"

    # Callers sometimes forget the MCP path segment
    if [[ "$ENGRAM_URL" != */mcp ]]; then
        ENGRAM_URL="${ENGRAM_URL%/}/mcp"
        info "Added /mcp suffix: $ENGRAM_URL"
    fi

    read -p "  API Token (leave blank for no auth): " ENGRAM_API_TOKEN
    ENGRAM_API_TOKEN="${ENGRAM_API_TOKEN:-}"

    # Persist to whichever shell profile is present
    local shell_profile=""
    if [[ -f "$HOME/.zshrc" ]]; then
        shell_profile="$HOME/.zshrc"
    elif [[ -f "$HOME/.bashrc" ]]; then
        shell_profile="$HOME/.bashrc"
    elif [[ -f "$HOME/.bash_profile" ]]; then
        shell_profile="$HOME/.bash_profile"
    fi

    if [[ -n "$shell_profile" ]]; then
        sed -i.bak '/^export ENGRAM_URL=/d' "$shell_profile" 2>/dev/null || true
        sed -i.bak '/^export ENGRAM_API_TOKEN=/d' "$shell_profile" 2>/dev/null || true
        rm -f "${shell_profile}.bak"
        echo "export ENGRAM_URL=\"${ENGRAM_URL}\""      >> "$shell_profile"
        echo "export ENGRAM_API_TOKEN=\"${ENGRAM_API_TOKEN}\"" >> "$shell_profile"
        success "Environment variables written to $shell_profile"
    else
        warn "Could not detect a shell profile. Set these manually:"
        echo "  export ENGRAM_URL=\"${ENGRAM_URL}\""
        echo "  export ENGRAM_API_TOKEN=\"${ENGRAM_API_TOKEN}\""
    fi

    export ENGRAM_URL ENGRAM_API_TOKEN
}

# ---------------------------------------------------------------------------
# Sanity-check that the server is reachable
# ---------------------------------------------------------------------------
verify_health() {
    local health_url="${ENGRAM_URL%/mcp}/health"
    info "Checking server health at ${health_url}..."

    if curl -sS --connect-timeout 5 "$health_url" > /dev/null 2>&1; then
        success "Server is reachable"
    else
        warn "Could not reach server at ${health_url}"
        warn "Ensure your Engram server is running. See docs/DEPLOYMENT.md for setup."
    fi
}

# ---------------------------------------------------------------------------
# Main installation sequence
# ---------------------------------------------------------------------------
main() {
    local version="${1:-}"

    echo ""
    echo "╔═══════════════════════════════════════════════════════════╗"
    echo "║           Engram - Installation Script                   ║"
    echo "║     Persistent Memory System for Claude Code CLI         ║"
    echo "╚═══════════════════════════════════════════════════════════╝"
    echo ""

    command -v curl &> /dev/null || error "curl is required but not installed"
    command -v tar  &> /dev/null || error "tar is required but not installed"

    local platform
    platform=$(detect_platform)
    info "Detected platform: $platform"

    if [[ -z "$version" ]]; then
        info "Fetching latest release..."
        version=$(get_latest_version)
    fi
    info "Installing version: $version"

    download_release "$version" "$platform"

    if register_plugin "$version"; then
        success "Plugin registered successfully"
    else
        warn "Plugin registration incomplete — install jq and re-run with --register-only"
    fi

    setup_connection
    verify_health

    echo ""
    echo "╔═══════════════════════════════════════════════════════════╗"
    echo "║                  Installation Complete!                  ║"
    echo "╠═══════════════════════════════════════════════════════════╣"
    echo "║  Restart Claude Code to activate the engram plugin.      ║"
    echo "║  Then run /engram:doctor to verify the connection.       ║"
    echo "║                                                          ║"
    echo "║  Server setup: docs/DEPLOYMENT.md                        ║"
    echo "╚═══════════════════════════════════════════════════════════╝"
    echo ""
}

# ---------------------------------------------------------------------------
# Entry points for non-default modes
# ---------------------------------------------------------------------------
if [[ "$FLAG_REGISTER_ONLY" == "true" ]]; then
    version=$(grep '"version"' "$INSTALL_DIR/.claude-plugin/plugin.json" 2>/dev/null \
              | sed -E 's/.*"([^"]+)".*/\1/' || echo "1.0.0")
    register_plugin "v$version"
    exit 0
fi

if [[ "$FLAG_UNINSTALL" == "true" ]]; then
    local_keep=false
    [[ "$FLAG_KEEP_DATA" == "true" ]] && local_keep=true

    echo ""
    echo "╔═══════════════════════════════════════════════════════════╗"
    echo "║         Engram - Uninstallation                          ║"
    echo "╚═══════════════════════════════════════════════════════════╝"
    echo ""

    info "Removing plugin directories..."
    rm -rf "$INSTALL_DIR"
    rm -rf "$CACHE_DIR"
    success "Plugin directories removed"

    if command -v jq &> /dev/null; then
        info "Cleaning up Claude Code configuration..."
        if [[ -f "$PLUGINS_FILE" ]]; then
            jq 'del(.plugins["'"$PLUGIN_KEY"'"])' "$PLUGINS_FILE" \
                > "${PLUGINS_FILE}.tmp" && mv "${PLUGINS_FILE}.tmp" "$PLUGINS_FILE"
        fi
        if [[ -f "$SETTINGS_FILE" ]]; then
            jq 'del(.enabledPlugins["'"$PLUGIN_KEY"'"]) |
                if .statusLine.command | test("engram") then del(.statusLine) else . end' \
                "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp" \
                && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
        fi
        if [[ -f "$MARKETPLACES_FILE" ]]; then
            jq 'del(.["engram"])' "$MARKETPLACES_FILE" \
                > "${MARKETPLACES_FILE}.tmp" && mv "${MARKETPLACES_FILE}.tmp" "$MARKETPLACES_FILE"
        fi
        success "Configuration cleaned up"
    else
        warn "jq not found — configuration files not cleaned up"
    fi

    local data_dir="$HOME/.engram"
    if [[ -d "$data_dir" ]]; then
        if [[ "$local_keep" == "true" ]]; then
            warn "Keeping data directory: $data_dir"
        else
            info "Removing data directory..."
            rm -rf "$data_dir"
            success "Data directory removed"
        fi
    fi

    echo ""
    success "Engram uninstalled successfully"
    exit 0
fi

main "$VERSION_ARG"
