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

// Visible diagnostic: warn to stderr if ENGRAM_URL is empty so the user has a signal,
// not a silent gRPC dial failure on every tool call.
if (!process.env.ENGRAM_URL) {
  process.stderr.write(
    "[engram] WARN: ENGRAM_URL is empty. Run /engram:setup to configure server URL, " +
    "or set the ENGRAM_URL env var in your shell.\n"
  );
}
if (!process.env.ENGRAM_AUTH_ADMIN_TOKEN) {
  process.stderr.write(
    "[engram] WARN: ENGRAM_AUTH_ADMIN_TOKEN is empty. If your server requires authentication, " +
    "set it via /config or follow /engram:setup to add it to ~/.claude/settings.json " +
    "(recommended — survives CC credential store races, see engram issue #83).\n"
  );
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