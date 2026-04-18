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

// Fallback: when CC plugin update doesn't migrate user_config, ENGRAM_URL is empty.
// Pick up the legacy system env value if present.
if (!process.env.ENGRAM_URL && process.env.ENGRAM_URL_LEGACY) {
  process.env.ENGRAM_URL = process.env.ENGRAM_URL_LEGACY;
}

// Same pattern for the API token: CC's shared credential-store is prone to races that
// silently wipe pluginSecrets (see anthropics/claude-code#45551). A user who sets
// ENGRAM_API_TOKEN directly in ~/.claude/settings.json survives those wipes.
if (!process.env.ENGRAM_API_TOKEN && process.env.ENGRAM_API_TOKEN_LEGACY) {
  process.env.ENGRAM_API_TOKEN = process.env.ENGRAM_API_TOKEN_LEGACY;
}

// Visible diagnostic: warn to stderr if both ended up empty so the user has a signal,
// not a silent gRPC dial failure on every tool call.
if (!process.env.ENGRAM_URL) {
  process.stderr.write(
    "[engram] WARN: ENGRAM_URL is empty. Run /engram:setup to configure server URL, " +
    "or set the ENGRAM_URL env var in your shell.\n"
  );
}
if (!process.env.ENGRAM_API_TOKEN) {
  process.stderr.write(
    "[engram] WARN: ENGRAM_API_TOKEN is empty. If your server requires authentication, " +
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