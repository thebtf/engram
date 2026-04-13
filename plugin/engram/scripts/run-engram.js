#!/usr/bin/env node
// run-engram.js — Cross-platform wrapper that execs the correct engram binary.
// Used as .mcp.json command to handle Windows .exe suffix and path resolution.

const { execFileSync } = require("child_process");
const path = require("path");
const fs = require("fs");

const pluginData = process.env.CLAUDE_PLUGIN_DATA;
if (!pluginData) {
  process.stderr.write("[engram] CLAUDE_PLUGIN_DATA not set\n");
  process.exit(1);
}

const ext = process.platform === "win32" ? ".exe" : "";
const binaryPath = path.join(pluginData, "bin", `engram${ext}`);

if (!fs.existsSync(binaryPath)) {
  process.stderr.write(
    `[engram] binary not found at ${binaryPath} — run ensure-binary.js first\n`
  );
  process.exit(1);
}

// Replace this process with the engram binary
try {
  const { spawnSync } = require("child_process");
  const result = spawnSync(binaryPath, process.argv.slice(2), {
    stdio: "inherit",
    env: process.env,
  });
  process.exit(result.status ?? 1);
} catch (err) {
  process.stderr.write(`[engram] exec failed: ${err.message}\n`);
  process.exit(1);
}
