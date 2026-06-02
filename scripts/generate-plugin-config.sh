#!/bin/bash
# Copy static plugin configuration files to release directory.
# Called from .goreleaser.yaml before hooks. The output goes into a
# .claude-plugin/ and .codex-plugin/ directories that the goreleaser archive
# then bundles into the per-platform tar.gz / zip artefacts consumed by
# scripts/install.sh.

set -e

CLAUDE_OUTPUT_DIR=".claude-plugin"
CODEX_OUTPUT_DIR=".codex-plugin"
mkdir -p "$CLAUDE_OUTPUT_DIR"
mkdir -p "$CODEX_OUTPUT_DIR"

# plugin.json single source of truth lives in plugin/engram/.claude-plugin/.
# Copy it into the release-time OUTPUT_DIR so the goreleaser archive picks
# up the freshly-bumped version (install.sh then unpacks it to
# $INSTALL_DIR/.claude-plugin/plugin.json).
cp "plugin/engram/.claude-plugin/plugin.json" "$CLAUDE_OUTPUT_DIR/plugin.json"
echo "Copied $CLAUDE_OUTPUT_DIR/plugin.json"

# Codex has its own plugin manifest format. Keep it parallel to Claude's
# manifest instead of trying to reuse Claude userConfig fields.
cp "plugin/engram/.codex-plugin/plugin.json" "$CODEX_OUTPUT_DIR/plugin.json"
echo "Copied $CODEX_OUTPUT_DIR/plugin.json"

# History note: an earlier version of this script ran
#   cp plugin/.claude-plugin/marketplace.json $OUTPUT_DIR/marketplace.json
# but the source path was deleted in commit 653fabb (#151) when the
# marketplace metadata was reshaped — the tracked file now lives directly
# at .claude-plugin/marketplace.json (added by 9a9c5a0). Because that
# location IS OUTPUT_DIR, no copy step is needed: goreleaser's
# `archives.files[].src: .claude-plugin/*` glob picks up the tracked
# marketplace.json as-is. Leaving the failing cp in place silently broke
# the Release (GoReleaser) workflow for 8 consecutive releases
# (v5.2.5 → v6.4.0). See TD-008 in TECHNICAL_DEBT.md for the full trail.

echo "Plugin config files copied successfully"
