#!/bin/bash
# Engram - Remote Installation Script
# Usage: curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash
#
# Or with a specific version:
# curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash -s -- v1.0.0

set -e

# Configuration
GITHUB_REPO="thebtf/engram"
INSTALL_DIR="$HOME/.claude/plugins/marketplaces/engram"
CACHE_DIR="$HOME/.claude/plugins/cache/engram/engram"
PLUGINS_FILE="$HOME/.claude/plugins/installed_plugins.json"
SETTINGS_FILE="$HOME/.claude/settings.json"
MARKETPLACES_FILE="$HOME/.claude/plugins/known_marketplaces.json"
PLUGIN_KEY="engram@engram"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

success() {
    echo -e "${GREEN}[OK]${NC} $1"
}

warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1"
    exit 1
}

# Detect OS and architecture
detect_platform() {
    local os arch

    case "$(uname -s)" in
        Darwin)
            os="darwin"
            ;;
        Linux)
            os="linux"
            ;;
        MINGW*|MSYS*|CYGWIN*)
            os="windows"
            ;;
        *)
            error "Unsupported operating system: $(uname -s)"
            ;;
    esac

    case "$(uname -m)" in
        x86_64|amd64)
            arch="amd64"
            ;;
        arm64|aarch64)
            arch="arm64"
            ;;
        *)
            error "Unsupported architecture: $(uname -m)"
            ;;
    esac

    # Check for unsupported combinations
    if [[ "$os" == "linux" && "$arch" == "arm64" ]]; then
        error "Linux ARM64 is not currently supported due to CGO cross-compilation limitations"
    fi

    echo "${os}_${arch}"
}

# Get the latest release version from GitHub
get_latest_version() {
    local response version curl_opts

    # Use GitHub token if available (higher rate limit)
    curl_opts=(-sS)
    if [[ -n "${GITHUB_TOKEN:-}" ]]; then
        curl_opts+=(-H "Authorization: token ${GITHUB_TOKEN}")
    fi

    # Fetch with error handling
    response=$(curl "${curl_opts[@]}" "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>&1)

    # Check for rate limiting
    if echo "$response" | grep -q "API rate limit exceeded"; then
        echo ""
        error "GitHub API rate limit exceeded.

You have a few options:
  1. Wait ~1 hour for the rate limit to reset
  2. Specify a version manually:
     curl -sSL https://raw.githubusercontent.com/${GITHUB_REPO}/main/scripts/install.sh | bash -s -- v0.6.1
  3. Use a GitHub token (set GITHUB_TOKEN environment variable)
  4. Clone and build from source:
     git clone https://github.com/${GITHUB_REPO}.git
     cd engram && make build && make install"
    fi

    # Check for other API errors
    if echo "$response" | grep -q '"message":'; then
        local msg
        msg=$(echo "$response" | grep '"message":' | sed -E 's/.*"message": *"([^"]+)".*/\1/')
        error "GitHub API error: $msg"
    fi

    # Extract version
    version=$(echo "$response" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

    if [[ -z "$version" ]]; then
        error "Failed to fetch latest version from GitHub. Response: $response"
    fi

    echo "$version"
}

# Download and extract the release
download_release() {
    local version="$1"
    local platform="$2"
    local tmp_dir

    tmp_dir=$(mktemp -d)
    trap "rm -rf $tmp_dir" EXIT

    # Construct download URL (use .zip for Windows, .tar.gz for others)
    local archive_ext="tar.gz"
    if [[ "$platform" == windows_* ]]; then
        archive_ext="zip"
    fi
    local archive_name="engram_${version#v}_${platform}.${archive_ext}"
    local download_url="https://github.com/${GITHUB_REPO}/releases/download/${version}/${archive_name}"

    info "Downloading ${archive_name}..."

    if ! curl -sSL -o "$tmp_dir/release.${archive_ext}" "$download_url"; then
        error "Failed to download release from: $download_url"
    fi

    info "Extracting archive..."
    if [[ "$archive_ext" == "zip" ]]; then
        if ! unzip -q "$tmp_dir/release.zip" -d "$tmp_dir"; then
            error "Failed to extract archive"
        fi
    else
        if ! tar -xzf "$tmp_dir/release.tar.gz" -C "$tmp_dir"; then
            error "Failed to extract archive"
        fi
    fi

    # Create installation directories
    info "Installing to ${INSTALL_DIR}..."
    mkdir -p "$INSTALL_DIR/hooks"
    mkdir -p "$INSTALL_DIR/.claude-plugin"
    mkdir -p "$INSTALL_DIR/commands"

    # Copy binaries
    cp "$tmp_dir/engram-server" "$INSTALL_DIR/" 2>/dev/null || true
    cp "$tmp_dir/engram-mcp" "$INSTALL_DIR/" 2>/dev/null || true
    cp "$tmp_dir/engram-mcp-stdio-proxy" "$INSTALL_DIR/" 2>/dev/null || true

    # Copy JS hooks (required for plugin — fail loudly if missing)
    if ! cp "$tmp_dir/hooks/"*.js "$INSTALL_DIR/hooks/" 2>/dev/null; then
        error "Failed to copy JS hooks from $tmp_dir/hooks/ to $INSTALL_DIR/hooks/"
    fi
    if ! cp "$tmp_dir/hooks/hooks.json" "$INSTALL_DIR/hooks/" 2>/dev/null; then
        error "Failed to copy hooks.json from $tmp_dir/hooks/ to $INSTALL_DIR/hooks/"
    fi

    # Copy plugin configuration
    cp "$tmp_dir/.claude-plugin/"* "$INSTALL_DIR/.claude-plugin/"

    # Copy slash commands if they exist in the release
    if [[ -d "$tmp_dir/commands" ]]; then
        cp -r "$tmp_dir/commands/"* "$INSTALL_DIR/commands/" 2>/dev/null || true
    fi

    # Copy skills if they exist in the release
    if [[ -d "$tmp_dir/skills" ]]; then
        mkdir -p "$INSTALL_DIR/skills"
        cp -r "$tmp_dir/skills/"* "$INSTALL_DIR/skills/" 2>/dev/null || true
    fi

    # Copy MCP config if it exists in the release
    if [[ -f "$tmp_dir/.mcp.json" ]]; then
        cp "$tmp_dir/.mcp.json" "$INSTALL_DIR/.mcp.json"
    fi

    # Make binaries executable
    chmod +x "$INSTALL_DIR/engram-server" 2>/dev/null || true
    chmod +x "$INSTALL_DIR/engram-mcp" 2>/dev/null || true
    chmod +x "$INSTALL_DIR/engram-mcp-stdio-proxy" 2>/dev/null || true

    success "Binaries installed to ${INSTALL_DIR}"
}

# Register the plugin with Claude Code
register_plugin() {
    local version="$1"
    local timestamp
    timestamp=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")

    # Ensure directories exist
    mkdir -p "$HOME/.claude/plugins"

    # Clean up old cache versions to prevent stale binaries
    local cache_base
    cache_base=$(dirname "$CACHE_DIR")
    if [[ -d "$cache_base" ]]; then
        info "Cleaning up old cache versions..."
        find "$cache_base" -mindepth 1 -maxdepth 1 -type d ! -name "${version#v}" -exec rm -rf {} \; 2>/dev/null || true
    fi

    mkdir -p "${CACHE_DIR}/${version}"

    # Create JSON files if they don't exist
    [[ ! -f "$PLUGINS_FILE" ]] && echo '{"version": 2, "plugins": {}}' > "$PLUGINS_FILE"
    [[ ! -f "$SETTINGS_FILE" ]] && echo '{}' > "$SETTINGS_FILE"
    [[ ! -f "$MARKETPLACES_FILE" ]] && echo '{}' > "$MARKETPLACES_FILE"

    # Check for jq
    if ! command -v jq &> /dev/null; then
        warn "jq is not installed. Plugin registration requires jq."
        warn "Please install jq: brew install jq (macOS) or apt-get install jq (Linux)"
        warn "Then run: $0 --register-only"
        return 1
    fi

    local cache_path="${CACHE_DIR}/${version}"

    # Copy files to cache directory
    mkdir -p "$cache_path/.claude-plugin"
    mkdir -p "$cache_path/hooks"
    mkdir -p "$cache_path/commands"
    cp -r "$INSTALL_DIR/"* "$cache_path/" 2>/dev/null || true

    # Register in installed_plugins.json
    local plugin_entry
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

    # Enable in settings.json and configure statusline
    local statusline_cmd="node \"$INSTALL_DIR/hooks/statusline.js\""
    local statusline_entry
    statusline_entry=$(cat <<EOF
{
    "type": "command",
    "command": "$statusline_cmd",
    "padding": 0
}
EOF
)

    jq --arg key "$PLUGIN_KEY" --argjson statusline "$statusline_entry" \
        '.enabledPlugins //= {} | .enabledPlugins[$key] = true | .statusLine = $statusline' "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp" \
        && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"

    success "Plugin enabled in settings.json"
    success "Statusline configured in settings.json"

    # Register marketplace
    local marketplace_entry
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

    # Note: MCP server registration is handled by the plugin's .mcp.json file.
    # No need to modify settings.json for MCP — the plugin handles this automatically.
}

# Prompt for server connection settings
setup_connection() {
    echo ""
    info "Engram uses a remote server for storage. Configure your connection:"
    echo ""

    # Prompt for server URL
    local default_url="http://localhost:37777/mcp"
    read -p "  Server URL [${default_url}]: " ENGRAM_URL
    ENGRAM_URL="${ENGRAM_URL:-$default_url}"

    # Ensure URL ends with /mcp
    if [[ "$ENGRAM_URL" != */mcp ]]; then
        ENGRAM_URL="${ENGRAM_URL%/}/mcp"
        info "Added /mcp suffix: $ENGRAM_URL"
    fi

    # Prompt for API token
    read -p "  API Token (empty for no auth): " ENGRAM_API_TOKEN
    ENGRAM_API_TOKEN="${ENGRAM_API_TOKEN:-}"

    # Detect shell profile
    local shell_profile=""
    if [[ -f "$HOME/.zshrc" ]]; then
        shell_profile="$HOME/.zshrc"
    elif [[ -f "$HOME/.bashrc" ]]; then
        shell_profile="$HOME/.bashrc"
    elif [[ -f "$HOME/.bash_profile" ]]; then
        shell_profile="$HOME/.bash_profile"
    fi

    if [[ -n "$shell_profile" ]]; then
        # Remove old entries if present
        sed -i.bak '/^export ENGRAM_URL=/d' "$shell_profile" 2>/dev/null || true
        sed -i.bak '/^export ENGRAM_API_TOKEN=/d' "$shell_profile" 2>/dev/null || true
        rm -f "${shell_profile}.bak"

        # Append new entries
        echo "export ENGRAM_URL=\"${ENGRAM_URL}\"" >> "$shell_profile"
        echo "export ENGRAM_API_TOKEN=\"${ENGRAM_API_TOKEN}\"" >> "$shell_profile"
        success "Environment variables written to $shell_profile"
    else
        warn "Could not detect shell profile. Set these manually:"
        echo "  export ENGRAM_URL=\"${ENGRAM_URL}\""
        echo "  export ENGRAM_API_TOKEN=\"${ENGRAM_API_TOKEN}\""
    fi

    # Export for current session
    export ENGRAM_URL
    export ENGRAM_API_TOKEN
}

# Verify server connectivity
verify_health() {
    local health_url="${ENGRAM_URL%/mcp}/health"
    info "Checking server health at ${health_url}..."

    if curl -sS --connect-timeout 5 "$health_url" > /dev/null 2>&1; then
        success "Server is reachable"
    else
        warn "Could not reach server at ${health_url}"
        warn "Make sure your Engram server is running. See docs/DEPLOYMENT.md for setup."
    fi
}

# Main installation flow
main() {
    local version="${1:-}"

    echo ""
    echo "╔═══════════════════════════════════════════════════════════╗"
    echo "║           Engram - Installation Script           ║"
    echo "║     Persistent Memory System for Claude Code CLI          ║"
    echo "╚═══════════════════════════════════════════════════════════╝"
    echo ""

    # Check required dependencies
    if ! command -v curl &> /dev/null; then
        error "curl is required but not installed"
    fi

    if ! command -v tar &> /dev/null; then
        error "tar is required but not installed"
    fi

    # Detect platform
    local platform
    platform=$(detect_platform)
    info "Detected platform: $platform"

    # Get version
    if [[ -z "$version" ]]; then
        info "Fetching latest release..."
        version=$(get_latest_version)
    fi
    info "Installing version: $version"

    # Download and install
    download_release "$version" "$platform"

    # Register plugin
    if register_plugin "$version"; then
        success "Plugin registered successfully"
    else
        warn "Plugin registration incomplete - please install jq and run again"
    fi

    # Configure server connection
    setup_connection

    # Verify server health
    verify_health

    echo ""
    echo "╔═══════════════════════════════════════════════════════════╗"
    echo "║                  Installation Complete!                   ║"
    echo "╠═══════════════════════════════════════════════════════════╣"
    echo "║  Restart Claude Code to activate the engram plugin.       ║"
    echo "║  Then run /engram:doctor to verify the connection.        ║"
    echo "║                                                           ║"
    echo "║  Server setup: docs/DEPLOYMENT.md                         ║"
    echo "╚═══════════════════════════════════════════════════════════╝"
    echo ""
}

# Handle --register-only flag
if [[ "${1:-}" == "--register-only" ]]; then
    version=$(cat "$INSTALL_DIR/.claude-plugin/plugin.json" 2>/dev/null | grep '"version"' | sed -E 's/.*"([^"]+)".*/\1/' || echo "1.0.0")
    register_plugin "v$version"
    exit 0
fi

# Handle --uninstall flag
if [[ "${1:-}" == "--uninstall" ]]; then
    KEEP_DATA=false
    [[ "${2:-}" == "--keep-data" ]] && KEEP_DATA=true

    echo ""
    echo "╔═══════════════════════════════════════════════════════════╗"
    echo "║         Engram - Uninstallation                  ║"
    echo "╚═══════════════════════════════════════════════════════════╝"
    echo ""

    info "Removing plugin directories..."
    rm -rf "$INSTALL_DIR"
    rm -rf "$CACHE_DIR"
    success "Plugin directories removed"

    # Remove from JSON files (if jq is available)
    if command -v jq &> /dev/null; then
        info "Cleaning up Claude Code configuration..."
        if [[ -f "$PLUGINS_FILE" ]]; then
            jq 'del(.plugins["'"$PLUGIN_KEY"'"])' "$PLUGINS_FILE" > "${PLUGINS_FILE}.tmp" && mv "${PLUGINS_FILE}.tmp" "$PLUGINS_FILE"
        fi
        if [[ -f "$SETTINGS_FILE" ]]; then
            # Remove plugin from enabled plugins, remove statusline if it's ours
            jq 'del(.enabledPlugins["'"$PLUGIN_KEY"'"]) |
                if .statusLine.command | test("engram") then del(.statusLine) else . end' "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp" && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
        fi
        if [[ -f "$MARKETPLACES_FILE" ]]; then
            jq 'del(.["engram"])' "$MARKETPLACES_FILE" > "${MARKETPLACES_FILE}.tmp" && mv "${MARKETPLACES_FILE}.tmp" "$MARKETPLACES_FILE"
        fi
        success "Configuration cleaned up"
    else
        warn "jq not found - configuration files not cleaned up"
    fi

    # Handle data directory
    DATA_DIR="$HOME/.engram"
    if [[ -d "$DATA_DIR" ]]; then
        if [[ "$KEEP_DATA" == "true" ]]; then
            warn "Keeping data directory: $DATA_DIR"
        else
            info "Removing data directory..."
            rm -rf "$DATA_DIR"
            success "Data directory removed"
        fi
    fi

    echo ""
    success "Engram uninstalled successfully"
    exit 0
fi

main "$@"
