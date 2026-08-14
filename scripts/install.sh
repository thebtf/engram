#!/bin/bash
# engram installer — macOS / Linux / Git-Bash
#
# One-shot install from GitHub releases:
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

# The release policy is validated before any release files are installed.
require_node() {
    command -v node &> /dev/null || error "Node.js 18+ is required to validate the release bootstrap policy. Install Node.js 18 or newer and re-run this installer."
    local node_version
    node_version=$(node --version 2>/dev/null)
    if ! [[ "$node_version" =~ ^v?([0-9]+)\. ]] || (( BASH_REMATCH[1] < 18 )); then
        error "Node.js 18+ is required to validate the release bootstrap policy. Found ${node_version:-an invalid version}; install Node.js 18 or newer and re-run this installer."
    fi
}

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
    [[ -n "$version" ]] || error "Could not parse release tag from GitHub API. Response: $response"
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
    # shellcheck disable=SC2064 # Capture this per-install temp directory now.
    trap "rm -rf -- $(printf '%q' "$tmp_dir")" EXIT

    # Windows releases use zip; everything else uses tar.gz
    archive_ext="tar.gz"
    if [[ "$platform" == windows_* ]]; then
        archive_ext="zip"
        command -v unzip &> /dev/null || error "unzip is required for Windows-compatible Bash installs"
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

    [[ -d "$tmp_dir/scripts" ]] \
        || error "Release archive is missing required scripts directory"
    compgen -G "$tmp_dir/scripts/*.js" > /dev/null \
        || error "Release archive is missing required JS scripts"
    [[ -f "$tmp_dir/scripts/register-plugin.js" ]] \
        || error "Release archive is missing required registry transaction helper"
    [[ -f "$tmp_dir/bootstrap-targets.json" ]] \
        || error "Release archive is missing required bootstrap-targets.json"
    [[ -f "$tmp_dir/package.json" ]] \
        || error "Release archive is missing required OMP package.json"
    [[ -f "$tmp_dir/extensions/engram-memory.mjs" ]] \
        || error "Release archive is missing required OMP extension"
    # This validator is part of the trusted installer, not the release archive.
    # A release payload must never be allowed to validate its own policy.
    node - "$tmp_dir/bootstrap-targets.json" "${version#v}" <<'NODE' \
        || error "Release archive has an invalid bootstrap policy"
const fs = require('node:fs');
const [file, version] = process.argv.slice(2);
const assets = { 'win32-x64': 'engram-windows-amd64.exe', 'linux-x64': 'engram-linux-amd64', 'darwin-arm64': 'engram-darwin-arm64' };
const semver = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(?:0|[1-9A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9A-Za-z-][0-9A-Za-z-]*))*)?$/;
const sha256 = /^[0-9a-f]{64}$/;
const exact = (value, fields) => {
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).sort().join('\0') !== [...fields].sort().join('\0')) throw new Error('unknown or missing fields');
};
const assertNoDuplicateProperties = (text) => {
  let cursor = 0;
  const skip = () => { while (/\s/.test(text[cursor])) cursor += 1; };
  const string = () => {
    if (text[cursor] !== '"') throw new Error('bootstrap policy is not valid JSON');
    const start = cursor++;
    while (cursor < text.length) {
      const character = text[cursor++];
      if (character === '"') return JSON.parse(text.slice(start, cursor));
      if (character === '\\') { if (text[cursor++] === 'u') cursor += 4; }
      else if (character < ' ') throw new Error('bootstrap policy is not valid JSON');
    }
    throw new Error('bootstrap policy is not valid JSON');
  };
  const value = () => {
    skip();
    if (text[cursor] === '"') { string(); return; }
    if (text[cursor] === '{') {
      cursor += 1; skip();
      const keys = new Set();
      if (text[cursor] === '}') { cursor += 1; return; }
      for (;;) {
        skip(); const key = string();
        if (keys.has(key)) throw new Error('bootstrap policy has duplicate fields');
        keys.add(key); skip();
        if (text[cursor++] !== ':') throw new Error('bootstrap policy is not valid JSON');
        value(); skip();
        if (text[cursor] === '}') { cursor += 1; return; }
        if (text[cursor++] !== ',') throw new Error('bootstrap policy is not valid JSON');
      }
    }
    if (text[cursor] === '[') {
      cursor += 1; skip();
      if (text[cursor] === ']') { cursor += 1; return; }
      for (;;) {
        value(); skip();
        if (text[cursor] === ']') { cursor += 1; return; }
        if (text[cursor++] !== ',') throw new Error('bootstrap policy is not valid JSON');
      }
    }
    const token = /^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/.exec(text.slice(cursor));
    if (!token) throw new Error('bootstrap policy is not valid JSON');
    cursor += token[0].length;
  };
  value(); skip();
  if (cursor !== text.length) throw new Error('bootstrap policy is not valid JSON');
};
const text = fs.readFileSync(file, 'utf8');
assertNoDuplicateProperties(text);
const policy = JSON.parse(text);
exact(policy, ['schema_version', 'launcher_security_epoch', 'package_version', 'daemon_compat_epoch', 'targets', 'revoked_sha256', 'build_contract']);
if (policy.schema_version !== 1 || policy.launcher_security_epoch !== 1 || policy.daemon_compat_epoch !== 1 || policy.package_version !== version || !semver.test(version)) throw new Error('schema or version mismatch');
exact(policy.build_contract, ['go_version', 'trimpath', 'buildvcs', 'client_cgo', 'daemon_version_ldflag']);
if (policy.build_contract.go_version !== '1.25.12' || policy.build_contract.trimpath !== true || policy.build_contract.buildvcs !== false || policy.build_contract.client_cgo !== false || policy.build_contract.daemon_version_ldflag !== `v${version}`) throw new Error('unsupported build contract');
if (!Array.isArray(policy.revoked_sha256) || policy.revoked_sha256.some((hash) => typeof hash !== 'string' || !sha256.test(hash)) || new Set(policy.revoked_sha256).size !== policy.revoked_sha256.length) throw new Error('invalid revocation list');
exact(policy.targets, Object.keys(assets));
for (const [key, asset] of Object.entries(assets)) {
  const target = policy.targets[key];
  exact(target, ['desired', 'predecessor']);
  if (target.predecessor !== null) throw new Error('predecessor is not allowed');
  exact(target.desired, ['version', 'asset', 'size', 'sha256']);
  if (target.desired.version !== version || target.desired.asset !== asset || !Number.isSafeInteger(target.desired.size) || target.desired.size <= 0 || target.desired.size > 128 * 1024 * 1024 || !sha256.test(target.desired.sha256) || policy.revoked_sha256.includes(target.desired.sha256)) throw new Error(`invalid target ${key}`);
}
NODE

    info "Installing to ${INSTALL_DIR}..."
    mkdir -p "$INSTALL_DIR/hooks" "$INSTALL_DIR/scripts" "$INSTALL_DIR/.claude-plugin" "$INSTALL_DIR/commands" "$INSTALL_DIR/extensions"

    # Server binary is only included in --full installs
    if [[ "$INSTALL_MODE" == "full" ]]; then
        cp "$tmp_dir/engram-server" "$INSTALL_DIR/" 2>/dev/null || true
    fi

    # JS hooks are mandatory — the plugin cannot function without them
    cp "$tmp_dir/hooks/"*.js "$INSTALL_DIR/hooks/" \
        || error "Failed to copy JS hooks from $tmp_dir/hooks/"
    if compgen -G "$tmp_dir/hooks/*.cjs" > /dev/null; then
        cp "$tmp_dir/hooks/"*.cjs "$INSTALL_DIR/hooks/" \
            || error "Failed to copy CJS hooks from $tmp_dir/hooks/"
    fi
    cp "$tmp_dir/hooks/hooks.json" "$INSTALL_DIR/hooks/" \
        || error "Failed to copy hooks.json from $tmp_dir/hooks/"

    cp "$tmp_dir/scripts/"*.js "$INSTALL_DIR/scripts/" \
        || error "Failed to copy JS scripts from $tmp_dir/scripts/"
    cp "$tmp_dir/bootstrap-targets.json" "$INSTALL_DIR/" \
        || error "Failed to copy bootstrap policy from release archive"
    cp "$tmp_dir/package.json" "$INSTALL_DIR/" \
        || error "Failed to copy OMP package manifest from release archive"
    cp "$tmp_dir/extensions/engram-memory.mjs" "$INSTALL_DIR/extensions/" \
        || error "Failed to copy OMP extension from release archive"

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
    local version="$1" timestamp cache_path

    timestamp=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")
    mkdir -p "$HOME/.claude/plugins"

    # Preserve stale cache versions: already-running sessions may have hook
    # commands pointing at their versioned cache path until they restart.
    if [[ -d "$CACHE_DIR" ]]; then
        info "Preserving old cache versions for running-session hook compatibility..."
    fi

    cache_path="${CACHE_DIR}/${version}"
    mkdir -p "$cache_path/.claude-plugin" "$cache_path/hooks" "$cache_path/commands"
    cp -a "$INSTALL_DIR/." "$cache_path/" 2>/dev/null || error "Failed to copy plugin files to versioned cache"

    if ! node "$INSTALL_DIR/scripts/register-plugin.js" \
        "$PLUGINS_FILE" "$SETTINGS_FILE" "$MARKETPLACES_FILE" \
        "$PLUGIN_KEY" "$cache_path" "${version#v}" "$timestamp" "$INSTALL_DIR"; then
        error "Plugin registration failed"
    fi

    success "Plugin registered in installed_plugins.json"
    success "Plugin enabled in settings.json"
    success "Statusline configured in settings.json"
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
    read -r -p "  Server URL [${default_url}]: " ENGRAM_URL
    ENGRAM_URL="${ENGRAM_URL:-$default_url}"

    # Callers sometimes forget the MCP path segment
    if [[ "$ENGRAM_URL" != */mcp ]]; then
        ENGRAM_URL="${ENGRAM_URL%/}/mcp"
        info "Added /mcp suffix: $ENGRAM_URL"
    fi

    read -r -p "  API Token (leave blank for no auth): " ENGRAM_API_TOKEN
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
        sed -i.bak '/^export ENGRAM_TOKEN=/d' "$shell_profile" 2>/dev/null || true
        rm -f "${shell_profile}.bak"
        {
            printf 'export ENGRAM_URL=%q\n' "$ENGRAM_URL"
            printf 'export ENGRAM_API_TOKEN=%q\n' "$ENGRAM_API_TOKEN"
            printf 'export ENGRAM_TOKEN=%q\n' "$ENGRAM_API_TOKEN"
        } >> "$shell_profile"
        success "Environment variables written to $shell_profile"
    else
        warn "Could not detect a shell profile. Set these manually:"
        echo "  export ENGRAM_URL=\"${ENGRAM_URL}\""
        echo "  export ENGRAM_API_TOKEN=\"${ENGRAM_API_TOKEN}\""
    fi

    ENGRAM_TOKEN="$ENGRAM_API_TOKEN"
    export ENGRAM_URL ENGRAM_API_TOKEN ENGRAM_TOKEN
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

    require_node

    local platform
    platform=$(detect_platform)

    if [[ "$platform" == windows_* ]]; then
        command -v unzip &> /dev/null || error "unzip is required for Windows-compatible Bash installs"
    fi

    command -v curl &> /dev/null || error "curl is required but not installed"
    command -v tar  &> /dev/null || error "tar is required but not installed"

    info "Detected platform: $platform"

    if [[ -z "$version" ]]; then
        info "Fetching latest release..."
        version=$(get_latest_version)
    fi
    info "Installing version: $version"

    download_release "$version" "$platform"

    register_plugin "$version"
    success "Plugin registered successfully"

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
    require_node
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
                if (.statusLine.command // "") | test("engram") then del(.statusLine) else . end' \
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

    data_dir="$HOME/.engram"
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
