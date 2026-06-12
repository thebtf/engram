#!/bin/bash
# Prepare plugin manifests for inclusion in GoReleaser archives.
#
# Invoked as a GoReleaser before-hook (see .goreleaser.yaml). Copies the
# canonical plugin.json files from plugin/engram/ into the OUTPUT_DIR
# directories that the archive glob patterns reference, ensuring the
# freshly-bumped version is bundled into every per-platform tarball/zip.
#
# After unpacking a release, install.sh reads the manifest from:
#   $INSTALL_DIR/.claude-plugin/plugin.json

set -e

CLAUDE_OUTPUT_DIR=".claude-plugin"
CODEX_OUTPUT_DIR=".codex-plugin"
CLAUDE_MANIFEST="plugin/engram/.claude-plugin/plugin.json"
CODEX_MANIFEST="plugin/engram/.codex-plugin/plugin.json"

# Read the version field from a JSON manifest using Node.
# Node is installed as a separate step (actions/setup-node@v4) in release.yaml;
# guard against it being absent to produce a clear error rather than a cryptic one.
read_manifest_version() {
    if ! command -v node >/dev/null 2>&1; then
        echo "Error: node is required but not found in PATH" >&2
        exit 1
    fi
    node -e "
        const fs = require('fs');
        const m  = JSON.parse(fs.readFileSync(process.argv[1], 'utf8'));
        if (!m.version) { process.stderr.write('Error: version field missing in ' + process.argv[1] + '\n'); process.exit(1); }
        process.stdout.write(m.version);
    " "$1"
}

CLAUDE_VERSION="$(read_manifest_version "$CLAUDE_MANIFEST")"
CODEX_VERSION="$(read_manifest_version "$CODEX_MANIFEST")"

# The two manifests must stay in sync — a mismatch means someone bumped
# one without bumping the other.
if [ "$CLAUDE_VERSION" != "$CODEX_VERSION" ]; then
    echo "Plugin manifest version mismatch:" \
         "$CLAUDE_MANIFEST=$CLAUDE_VERSION," \
         "$CODEX_MANIFEST=$CODEX_VERSION" >&2
    exit 1
fi

mkdir -p "$CLAUDE_OUTPUT_DIR"
mkdir -p "$CODEX_OUTPUT_DIR"

# The plugin.json single source of truth lives in plugin/engram/.claude-plugin/.
# Copying it into the release-time OUTPUT_DIR lets the GoReleaser archive
# pick up the freshly-bumped version; install.sh then unpacks it to
# $INSTALL_DIR/.claude-plugin/plugin.json.
cp "$CLAUDE_MANIFEST" "$CLAUDE_OUTPUT_DIR/plugin.json"
echo "Copied $CLAUDE_OUTPUT_DIR/plugin.json"

# Codex has its own manifest format — keep it parallel to Claude's rather
# than trying to reuse Claude userConfig fields.
cp "$CODEX_MANIFEST" "$CODEX_OUTPUT_DIR/plugin.json"
echo "Copied $CODEX_OUTPUT_DIR/plugin.json"

# NOTE: an earlier version of this script copied marketplace.json from a
# now-deleted path (plugin/.claude-plugin/marketplace.json, removed in
# commit 653fabb / issue #151). The tracked file now lives directly at
# .claude-plugin/marketplace.json so GoReleaser's
# `archives.files[].src: .claude-plugin/*` glob picks it up as-is —
# no copy step needed. See TD-008 in TECHNICAL_DEBT.md.

echo "Plugin config files prepared successfully"
