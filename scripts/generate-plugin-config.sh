#!/bin/bash
# Copy static plugin configuration files to release directory.
# Called from .goreleaser.yaml before hooks.

set -e

OUTPUT_DIR=".claude-plugin"
mkdir -p "$OUTPUT_DIR"

# plugin.json lives in plugin/engram/.claude-plugin/
cp "plugin/engram/.claude-plugin/plugin.json" "$OUTPUT_DIR/plugin.json"
echo "Copied $OUTPUT_DIR/plugin.json"

# marketplace.json lives in plugin/.claude-plugin/
cp "plugin/.claude-plugin/marketplace.json" "$OUTPUT_DIR/marketplace.json"
echo "Copied $OUTPUT_DIR/marketplace.json"

echo "Plugin config files copied successfully"
