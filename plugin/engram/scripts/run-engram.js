#!/usr/bin/env node
// run-engram.js - Cross-platform wrapper that execs the correct engram binary.
// Used as plugin MCP command to handle Windows .exe suffix and path resolution.

const { spawnSync } = require("child_process");
const path = require("path");
const fs = require("fs");

const pluginRoot =
  process.env.PLUGIN_ROOT ||
  process.env.CLAUDE_PLUGIN_ROOT ||
  path.resolve(__dirname, "..");
const pluginData = process.env.PLUGIN_DATA || process.env.CLAUDE_PLUGIN_DATA;

if (!pluginData) {
  process.stderr.write("[engram] PLUGIN_DATA/CLAUDE_PLUGIN_DATA not set\n");
  process.exit(1);
}

const ext = process.platform === "win32" ? ".exe" : "";
const binaryPath = path.join(pluginData, "bin", `engram${ext}`);

if (!fs.existsSync(binaryPath)) {
  const ensureBinary = path.join(pluginRoot, "scripts", "ensure-binary.js");
  if (fs.existsSync(ensureBinary)) {
    process.stderr.write(`[engram] binary not found at ${binaryPath}; running ensure-binary.js\n`);
    spawnSync(process.execPath, [ensureBinary], {
      stdio: "inherit",
      env: {
        ...process.env,
        PLUGIN_ROOT: pluginRoot,
        PLUGIN_DATA: pluginData,
        CLAUDE_PLUGIN_ROOT: process.env.CLAUDE_PLUGIN_ROOT || pluginRoot,
        CLAUDE_PLUGIN_DATA: process.env.CLAUDE_PLUGIN_DATA || pluginData,
      },
    });
  }
}

if (!fs.existsSync(binaryPath)) {
  process.stderr.write(
    `[engram] binary not found at ${binaryPath}. The plugin could not install the client binary. ` +
    "Check network access to GitHub Releases and reinstall or upgrade the plugin.\n"
  );
  process.exit(1);
}

// Visible diagnostic: fail early if the workstation is not configured. A new
// install should not expose half-working tools with no remote memory backend.
const serverURL = configuredEnvValue(
  "ENGRAM_URL",
  "ENGRAM_SERVER_URL",
  "ENGRAM_CLAUDE_USERCONFIG_URL"
);
if (!serverURL) {
  process.stderr.write(
    "[engram] FATAL: ENGRAM_URL is empty. Configure Engram before first use.\n" +
    "Claude Code: run /engram:setup or set ENGRAM_URL in ~/.claude/settings.json env.\n" +
    "Codex: set ENGRAM_URL in ~/.codex/config.toml under [shell_environment_policy.set].\n"
  );
  process.exit(1);
}
process.env.ENGRAM_URL = serverURL;

// v6 model: ENGRAM_TOKEN is the per-workstation keycard issued via the
// dashboard /tokens page. The operator key (ENGRAM_AUTH_ADMIN_TOKEN) lives
// ONLY on the server host and MUST NOT be set on a workstation.
const token = configuredEnvValue("ENGRAM_TOKEN", "ENGRAM_CLAUDE_USERCONFIG_TOKEN");
if (!token) {
  process.stderr.write(
    `[engram] FATAL: ENGRAM_TOKEN is empty. Open ${serverURL.replace(/\/+$/, "")}/tokens, ` +
    "generate a workstation keycard, then configure ENGRAM_TOKEN.\n" +
    "Claude Code: store it in ~/.claude/settings.json env.\n" +
    "Codex: store it in ~/.codex/config.toml under [shell_environment_policy.set].\n"
  );
  process.exit(1);
}
process.env.ENGRAM_TOKEN = token;

if (process.env.ENGRAM_AUTH_ADMIN_TOKEN) {
  process.stderr.write(
    "[engram] WARN: ENGRAM_AUTH_ADMIN_TOKEN is set on this workstation. v6 forbids " +
    "this — the operator key belongs ONLY on the server host. Remove it from " +
    "your local agent config and use ENGRAM_TOKEN with a dashboard-issued keycard.\n"
  );
}

// Replace this process with the engram binary
try {
  const result = spawnSync(binaryPath, process.argv.slice(2), {
    stdio: "inherit",
    env: process.env,
  });
  process.exit(result.status ?? 1);
} catch (err) {
  process.stderr.write(`[engram] exec failed: ${err.message}\n`);
  process.exit(1);
}

function configuredEnvValue(...keys) {
  for (const key of keys) {
    const value = process.env[key];
    if (isConfiguredValue(value)) {
      return value.trim();
    }
  }
  return "";
}

function isConfiguredValue(value) {
  if (typeof value !== "string") {
    return false;
  }
  const trimmed = value.trim();
  if (!trimmed) {
    return false;
  }
  return !/^\$\{[^}]+\}$/.test(trimmed);
}
