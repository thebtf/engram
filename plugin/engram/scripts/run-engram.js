#!/usr/bin/env node
// run-engram.js - Cross-platform wrapper that execs the correct engram binary.
// Used as plugin MCP command to handle Windows .exe suffix and path resolution.

const { spawnSync } = require("child_process");
const path = require("path");
const fs = require("fs");

const STARTUP_DIAGNOSTIC_LOG_MAX_BYTES = 128 * 1024;

function main() {
  const pluginRoot = resolvePluginRoot();
  const pluginData = resolvePluginData(pluginRoot);

  const ext = process.platform === "win32" ? ".exe" : "";
  const binaryPath = path.join(pluginData, "bin", `engram${ext}`);
  const ensureBinary = path.join(pluginRoot, "scripts", "ensure-binary.js");

  emitStartupDiagnostic(pluginData);

  if (fs.existsSync(ensureBinary)) {
    // ensure-binary owns freshness: it compares plugin.json with both the
    // marker file and the binary's own --version output.
    const ensureStatus = checkedSpawnSync(process.execPath, [ensureBinary], {
      stdio: "inherit",
      env: {
        ...process.env,
        PLUGIN_ROOT: pluginRoot,
        PLUGIN_DATA: pluginData,
        CLAUDE_PLUGIN_ROOT: process.env.CLAUDE_PLUGIN_ROOT || pluginRoot,
        CLAUDE_PLUGIN_DATA: process.env.CLAUDE_PLUGIN_DATA || pluginData,
      },
    }, "ensure-binary");
    if (ensureStatus !== 0) {
      process.exit(ensureStatus);
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
    // Claude Code exports plugin userConfig values to plugin subprocesses as
    // CLAUDE_PLUGIN_OPTION_<KEY>. Interpolating ${user_config.*} inside the
    // .mcp.json env block is NOT used: it silently prevents the MCP server
    // from spawning (anthropics/claude-code#51573).
    "CLAUDE_PLUGIN_OPTION_server_url",
    "CLAUDE_PLUGIN_OPTION_SERVER_URL",
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
  const token = configuredEnvValue(
    "ENGRAM_TOKEN",
    "CLAUDE_PLUGIN_OPTION_api_token",
    "CLAUDE_PLUGIN_OPTION_API_TOKEN",
    "ENGRAM_CLAUDE_USERCONFIG_TOKEN"
  );
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
  const status = checkedSpawnSync(binaryPath, process.argv.slice(2), {
    stdio: "inherit",
    env: process.env,
  }, "engram exec");
  process.exit(status);
}

function resolvePluginRoot() {
  return (
    process.env.PLUGIN_ROOT ||
    process.env.CLAUDE_PLUGIN_ROOT ||
    path.resolve(__dirname, "..")
  );
}

function resolvePluginData(pluginRoot) {
  const configured = configuredEnvValue("PLUGIN_DATA", "CLAUDE_PLUGIN_DATA");
  if (configured) {
    return configured;
  }

  const codexData = inferCodexPluginDataDir(pluginRoot);
  if (codexData) {
    return codexData;
  }

  return path.join(pluginRoot, ".data");
}

function inferCodexPluginDataDir(pluginRoot) {
  const resolved = path.resolve(pluginRoot);
  const parsed = path.parse(resolved);
  const relative = resolved.slice(parsed.root.length);
  const parts = relative.split(path.sep).filter(Boolean);
  const cacheIndex = parts.lastIndexOf("cache");

  if (
    cacheIndex < 1 ||
    parts[cacheIndex - 1] !== "plugins" ||
    parts.length < cacheIndex + 4
  ) {
    return "";
  }

  const marketplace = parts[cacheIndex + 1];
  const pluginName = parts[cacheIndex + 2];
  const pluginDataRoot = path.join(parsed.root, ...parts.slice(0, cacheIndex), "data");
  return path.join(pluginDataRoot, `${marketplace}-${pluginName}`);
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

function emitStartupDiagnostic(pluginData) {
  const line = formatStartupDiagnostic();
  process.stderr.write(`${line}\n`);
  appendStartupDiagnosticLog(pluginData, line);
}

function formatStartupDiagnostic(env = process.env) {
  const keys = [
    ["ENGRAM_URL", false],
    ["ENGRAM_TOKEN", true],
    ["ENGRAM_SERVER_URL", false],
    ["CLAUDE_PLUGIN_OPTION_server_url", false],
    ["CLAUDE_PLUGIN_OPTION_api_token", true],
    ["ENGRAM_CLAUDE_USERCONFIG_URL", false],
    ["ENGRAM_CLAUDE_USERCONFIG_TOKEN", true],
    ["PLUGIN_ROOT", false],
    ["CLAUDE_PLUGIN_ROOT", false],
    ["PLUGIN_DATA", false],
    ["CLAUDE_PLUGIN_DATA", false],
  ];
  return `[engram] startup env: ${keys.map(([key, sensitive]) => describeEnvValue(key, env, sensitive)).join("; ")}`;
}

function describeEnvValue(key, env = process.env, sensitive = false) {
  const raw = env[key];
  if (typeof raw !== "string") {
    return `${key}=missing`;
  }

  const value = raw.trim();
  if (!value) {
    return `${key}=empty`;
  }
  if (/^\$\{[^}]+\}$/.test(value)) {
    return `${key}=placeholder`;
  }

  const kind = sensitive ? "redacted" : "present";
  return `${key}=${kind}(len=${value.length})`;
}

function appendStartupDiagnosticLog(pluginData, line, now = new Date()) {
  try {
    const logsDir = path.join(pluginData, "logs");
    fs.mkdirSync(logsDir, { recursive: true });
    const logPath = path.join(logsDir, "startup-env.log");
    fs.appendFileSync(logPath, `${now.toISOString()} pid=${process.pid} ${line}\n`, "utf8");
    const stat = fs.statSync(logPath);
    if (stat.size > 2 * STARTUP_DIAGNOSTIC_LOG_MAX_BYTES) {
      trimStartupDiagnosticLog(logPath);
    }
  } catch {
    // Diagnostics must never prevent MCP startup.
  }
}

function trimStartupDiagnosticLog(logPath, maxBytes = STARTUP_DIAGNOSTIC_LOG_MAX_BYTES) {
  try {
    const stat = fs.statSync(logPath);
    if (stat.size <= maxBytes) {
      return;
    }

    const content = fs.readFileSync(logPath, "utf8");
    let trimmed = content.slice(-Math.floor(maxBytes / 2));
    const firstNewline = trimmed.indexOf("\n");
    if (firstNewline !== -1) {
      trimmed = trimmed.slice(firstNewline + 1);
    }
    fs.writeFileSync(logPath, trimmed, "utf8");
  } catch {
    // Best-effort only.
  }
}

function checkedSpawnSync(command, args, options, label) {
  const result = spawnSync(command, args, options);
  const failure = spawnFailureMessage(result, label);
  if (failure) {
    process.stderr.write(failure);
    process.exit(1);
  }
  return result.status ?? 0;
}

function spawnFailureMessage(result, label) {
  const prefix = `[engram] ${label}`;
  if (result && result.error) {
    return `${prefix} failed: ${result.error.message}\n`;
  }
  if (result && result.status === null) {
    return `${prefix} terminated by signal ${result.signal || "unknown"}\n`;
  }
  return "";
}

if (require.main === module) {
  main();
}

module.exports = {
  main,
  configuredEnvValue,
  appendStartupDiagnosticLog,
  describeEnvValue,
  emitStartupDiagnostic,
  formatStartupDiagnostic,
  inferCodexPluginDataDir,
  isConfiguredValue,
  resolvePluginData,
  resolvePluginRoot,
  spawnFailureMessage,
  trimStartupDiagnosticLog,
};
