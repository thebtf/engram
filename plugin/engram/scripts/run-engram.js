#!/usr/bin/env node
// run-engram.js - Cross-platform wrapper that execs the correct engram binary.
// Used as plugin MCP command to handle Windows .exe suffix and path resolution.

const { spawnSync } = require("child_process");
const path = require("path");
const fs = require("fs");
const os = require("os");
const { hashFile, objectRoots, resolveForLaunch } = require("./ensure-binary.js");
const { installationClientInstanceID } = require("./client-instance.js");

const STARTUP_DIAGNOSTIC_LOG_MAX_BYTES = 128 * 1024;
const HAP_01B_REVISION = "omp-hap-01b/1";
const HAP_01B_FIELDS = Object.freeze([
 "adapter_sha256",
 "legacy_direct_enforcement",
 "project_tokens",
 "registration_token",
 "relay_enabled",
 "relay_revision",
]);
const HAP_01B_INVALID = Symbol("hap_01b_invalid");
const HAP_01B_NORMALIZED = Symbol("hap_01b_normalized");
const HAP_01B_SHA256 = /^[0-9a-f]{64}$/;
const HAP_01B_KEYCARD = /^engram_[0-9a-fA-F]{32}$/;
const HAP_01B_PROJECT_KEY = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

async function main() {
 const pluginRoot = resolvePluginRoot();
 const pluginData = resolvePluginData(pluginRoot);

 const configFilePath = resolveConfigFilePath(pluginData);
 const configFile = readEngramConfigFile(configFilePath);

 emitStartupDiagnostic(pluginData, configFilePath, configFile);

 if (isInvalidHap01bConfig(configFile)) {
  process.stderr.write(`[engram] FATAL: invalid HAP-01B configuration in ${configFilePath}\n`);
  process.exitCode = 1;
  return;
 }

 // Visible diagnostic: fail early if the workstation is not configured. A new
 // install should not expose half-working tools with no remote memory backend.
 const serverURL =
  configuredEnvValue(
   "ENGRAM_URL",
   "ENGRAM_SERVER_URL",
   "CLAUDE_PLUGIN_OPTION_server_url",
   "CLAUDE_PLUGIN_OPTION_SERVER_URL",
   "ENGRAM_CLAUDE_USERCONFIG_URL"
  ) ||
  (configFile && isConfiguredValue(configFile.server_url) ? configFile.server_url : "");
 if (!serverURL) {
  process.stderr.write(
   "[engram] FATAL: ENGRAM_URL is empty. Configure Engram before first use.\n" +
   "Universal (all harnesses): create ~/.engram/config.json with {\"server_url\":\"http://...\",\"api_token\":\"engram_...\"}\n" +
   "  or set ENGRAM_CONFIG_FILE to a custom path.\n" +
   "Claude Code: run /engram:setup or set ENGRAM_URL in ~/.claude/settings.json env.\n" +
   `Config file checked: ${configFilePath}\n`
  );
  process.exitCode = 1;
  return;
 }
 process.env.ENGRAM_URL = serverURL;

 const token =
  configuredEnvValue(
   "ENGRAM_TOKEN",
   "CLAUDE_PLUGIN_OPTION_api_token",
   "CLAUDE_PLUGIN_OPTION_API_TOKEN",
   "ENGRAM_CLAUDE_USERCONFIG_TOKEN"
  ) ||
  (configFile && isConfiguredValue(configFile.api_token) ? configFile.api_token : "");
 if (!token) {
  process.stderr.write(
   `[engram] FATAL: ENGRAM_TOKEN is empty. Open ${serverURL.replace(/\/+$/, "")}/tokens, ` +
   "generate a workstation keycard, then configure ENGRAM_TOKEN.\n" +
   "Universal (all harnesses): add \"api_token\":\"engram_...\" to the config file.\n" +
   `Config file checked: ${configFilePath}\n`
  );
  process.exitCode = 1;
  return;
 }
 process.env.ENGRAM_CLIENT_INSTANCE_ID = configuredEnvValue(
  "ENGRAM_CLIENT_INSTANCE_ID",
  "CLAUDE_PLUGIN_OPTION_client_instance_id",
  "CLAUDE_PLUGIN_OPTION_CLIENT_INSTANCE_ID",
  "ENGRAM_CLAUDE_USERCONFIG_CLIENT_INSTANCE_ID"
 ) || configFile?.client_instance_id || "";
 process.env.ENGRAM_TOKEN = token;
 const childEnv = childEnvForEngram(process.env, configFile?.hap_01b);

 if (process.env.ENGRAM_AUTH_ADMIN_TOKEN) {
  process.stderr.write(
   "[engram] WARN: ENGRAM_AUTH_ADMIN_TOKEN is set on this workstation. v6 forbids " +
   "this — the operator key belongs ONLY on the server host. Remove it from " +
   "your local agent config and use ENGRAM_TOKEN with a dashboard-issued keycard.\n"
  );
 }

 try {
  const status = await resolveAndSpawn({ pluginRoot, pluginData, args: process.argv.slice(2), env: childEnv });
  process.exitCode = status;
 } catch (error) {
  process.stderr.write(`[engram] FATAL: trusted client launch failed: ${error.message}\n`);
  process.exitCode = 1;
 }
}

async function resolveAndSpawn(options) {
 const resolver = options.resolve || resolveForLaunch;
 const hash = options.hash || hashFile;
 const roots = options.roots || objectRoots;
 const resolved = await resolver({ pluginRoot: options.pluginRoot, pluginData: options.pluginData });
 // Rehash the policy-derived object in this process immediately before spawn.
 if (!hash(resolved.path, resolved.target, roots(options.pluginData).objects)) {
  throw new Error("resolved client failed final integrity verification");
 }
 const env = { ...options.env };
 env.ENGRAM_CLIENT_INSTANCE_ID = env.ENGRAM_CLIENT_INSTANCE_ID || installationClientInstanceID(options.pluginData);
 delete env.ENGRAM_UCI_PARSER_EXECUTABLE;
 delete env.ENGRAM_UCI_PARSER_BUNDLE_DIGEST;
 if (resolved.parserTarget) {
  if (!hash(resolved.parserPath, resolved.parserTarget, roots(options.pluginData).objects)) {
   throw new Error("resolved parser failed final integrity verification");
  }
  const parserEnv = Object.fromEntries(["SYSTEMROOT", "WINDIR", "COMSPEC"]
   .filter((name) => env[name]).map((name) => [name, env[name]]));
  const probe = (options.spawnSync || spawnSync)(resolved.parserPath, ["--bundle-digest"], {
   encoding: "utf8", env: parserEnv, timeout: 10000, maxBuffer: 256,
  });
  const failure = spawnFailureMessage(probe, "parser identity probe");
  if (failure || !/^sha256:[0-9a-f]{64}\r?\n$/.test(probe.stdout || "")) {
   throw new Error(failure || "installed parser returned an invalid bundle identity");
  }
  env.ENGRAM_UCI_PARSER_EXECUTABLE = resolved.parserPath;
  env.ENGRAM_UCI_PARSER_BUNDLE_DIGEST = probe.stdout.trim();
 }
 const result = (options.spawnSync || spawnSync)(resolved.path, options.args || [], {
  stdio: "inherit",
  env,
 });
 const failure = spawnFailureMessage(result, "engram exec");
 if (failure) throw new Error(failure.trim());
 return result.status ?? 0;
}

function childEnvForEngram(env = process.env, hapConfig) {
 const childEnv = { ...env };
 for (const key of Object.keys(childEnv)) {
  const canonical = key.toUpperCase();
  if (canonical === "ENGRAM_AUTH_ADMIN_TOKEN" || canonical.startsWith("ENGRAM_HAP_01B_")) {
   delete childEnv[key];
  }
 }
 if (isNormalizedHap01bConfig(hapConfig)) {
  childEnv.ENGRAM_HAP_01B_RELAY_ENABLED = "true";
  childEnv.ENGRAM_HAP_01B_RELAY_REVISION = hapConfig.relay_revision;
  childEnv.ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT = String(hapConfig.legacy_direct_enforcement);
  childEnv.ENGRAM_HAP_01B_ADAPTER_SHA256 = hapConfig.adapter_sha256;
  childEnv.ENGRAM_HAP_01B_REGISTRATION_TOKEN = hapConfig.registration_token;
  childEnv.ENGRAM_HAP_01B_PROJECT_TOKENS_JSON = JSON.stringify(hapConfig.project_tokens);
 }
 return childEnv;
}

function resolvePluginRoot() {
 return (
  configuredEnvValue("PLUGIN_ROOT", "CLAUDE_PLUGIN_ROOT") ||
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

 if (cacheIndex < 1 || parts[cacheIndex - 1] !== "plugins") {
  return "";
 }

 // OMP stores marketplace plugins in cache/plugins/<marketplace>___<plugin>___<version>.
 // Keep mutable data outside the versioned cache so upgrades reuse the binary and config.
 const ompCacheSlot = (parts[cacheIndex + 1] === "plugins" ? parts[cacheIndex + 2] : "") || "";
 const ompMatch = ompCacheSlot.match(/^(.+?)___(.+?)___(.+)$/);
 if (ompMatch && parts.length === cacheIndex + 3) {
  const pluginDataRoot = path.join(parsed.root, ...parts.slice(0, cacheIndex), "data");
  return path.join(pluginDataRoot, `${ompMatch[1]}-${ompMatch[2]}`);
 }

 if (parts.length < cacheIndex + 4) {
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

/**
 * Resolve the engram config file path, in priority order:
 *   1. $ENGRAM_CONFIG_FILE if set and non-empty
 *   2. <pluginData>/config.json if that file exists
 *   3. ~/.engram/config.json (home-directory universal fallback)
 * Returns the resolved path string (file may or may not exist).
 *
 * When pluginData is set but <pluginData>/config.json does not exist,
 * we fall through to the home-directory path so users who create only
 * ~/.engram/config.json (the documented Codex setup path) are found.
 */
function resolveConfigFilePath(pluginData) {
 const explicit = process.env.ENGRAM_CONFIG_FILE;
 if (isConfiguredValue(explicit)) {
  return explicit.trim();
 }
 if (pluginData && typeof pluginData === "string" && pluginData.trim()) {
  const candidate = path.join(pluginData.trim(), "config.json");
  if (fs.existsSync(candidate)) {
   return candidate;
  }
 }
 return path.join(os.homedir(), ".engram", "config.json");
}

/**
 * Read and parse the engram config file.
 * Returns normalized base fields, plus a normalized hap_01b block when present and valid.
 * An invalid present hap_01b block is represented only by an internal marker.
 * Returns null on missing or malformed root file; never throws.
 */
function readEngramConfigFile(configFilePath) {
 try {
  if (!configFilePath || !fs.existsSync(configFilePath)) {
   return null;
  }
  const raw = fs.readFileSync(configFilePath, "utf8");
  const parsed = JSON.parse(raw);
  if (!isPlainObject(parsed)) {
   return null;
  }
  const config = {
   server_url: typeof parsed.server_url === "string" ? parsed.server_url.trim() : "",
   client_instance_id: typeof parsed.client_instance_id === "string" ? parsed.client_instance_id : "",
   api_token: typeof parsed.api_token === "string" ? parsed.api_token.trim() : "",
  };
  if (!Object.hasOwn(parsed, "hap_01b")) {
   return config;
  }
  const hapConfig = parseHap01bConfig(parsed.hap_01b);
  if (!hapConfig) {
   Object.defineProperty(config, HAP_01B_INVALID, { value: true });
   return config;
  }
  config.hap_01b = hapConfig;
  return config;
 } catch {
  // Missing file, permission error, or malformed JSON — skip silently.
  return null;
 }
}

function parseHap01bConfig(value) {
 if (!isPlainObject(value) || !hasExactHap01bFields(value)) {
  return null;
 }
 if (
  value.relay_enabled !== true ||
  value.relay_revision !== HAP_01B_REVISION ||
  typeof value.legacy_direct_enforcement !== "boolean" ||
  typeof value.adapter_sha256 !== "string" ||
  !HAP_01B_SHA256.test(value.adapter_sha256) ||
  typeof value.registration_token !== "string" ||
  !HAP_01B_KEYCARD.test(value.registration_token)
 ) {
  return null;
 }
 const projectTokens = normalizeProjectTokens(value.project_tokens);
 if (!projectTokens) {
  return null;
 }
 const normalized = {
  relay_enabled: true,
  relay_revision: HAP_01B_REVISION,
  legacy_direct_enforcement: value.legacy_direct_enforcement,
  adapter_sha256: value.adapter_sha256,
  registration_token: value.registration_token,
  project_tokens: projectTokens,
 };
 Object.defineProperty(normalized, HAP_01B_NORMALIZED, { value: true });
 return Object.freeze(normalized);
}

function normalizeProjectTokens(value) {
 if (!isPlainObject(value)) {
  return null;
 }
 const entries = Object.entries(value);
 if (entries.length === 0 || entries.length > 256) {
  return null;
 }
 for (const [projectKey, token] of entries) {
  if (!HAP_01B_PROJECT_KEY.test(projectKey) || typeof token !== "string" || !HAP_01B_KEYCARD.test(token)) {
   return null;
  }
 }
 return Object.freeze(Object.fromEntries(entries.sort(([left], [right]) => (left < right ? -1 : left > right ? 1 : 0))));
}

function hasExactHap01bFields(value) {
 const keys = Object.keys(value).sort();
 return keys.length === HAP_01B_FIELDS.length && keys.every((key, index) => key === HAP_01B_FIELDS[index]);
}

function isPlainObject(value) {
 return value !== null && typeof value === "object" && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype;
}

function isInvalidHap01bConfig(configFile) {
 return Boolean(configFile?.[HAP_01B_INVALID]);
}

function isNormalizedHap01bConfig(value) {
 return Boolean(value?.[HAP_01B_NORMALIZED]);
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

function emitStartupDiagnostic(pluginData, configFilePath, configFile) {
 const line = formatStartupDiagnostic(process.env, configFilePath, configFile);
 process.stderr.write(`${line}\n`);
 appendStartupDiagnosticLog(pluginData, line);
}

function formatStartupDiagnostic(env = process.env, configFilePath, configFile) {
 const keys = [
  ["ENGRAM_URL", false],
  ["ENGRAM_TOKEN", true],
  ["ENGRAM_SERVER_URL", false],
  ["CLAUDE_PLUGIN_OPTION_server_url", false],
  ["CLAUDE_PLUGIN_OPTION_SERVER_URL", false],
  ["CLAUDE_PLUGIN_OPTION_api_token", true],
  ["CLAUDE_PLUGIN_OPTION_API_TOKEN", true],
  ["ENGRAM_CLAUDE_USERCONFIG_URL", false],
  ["ENGRAM_CLAUDE_USERCONFIG_TOKEN", true],
  ["ENGRAM_CONFIG_FILE", false],
  ["PLUGIN_ROOT", false],
  ["CLAUDE_PLUGIN_ROOT", false],
  ["PLUGIN_DATA", false],
  ["CLAUDE_PLUGIN_DATA", false],
 ];
 const envParts = keys.map(([key, sensitive]) => describeEnvValue(key, env, sensitive)).join("; ");
 const cfPart = describeConfigFile(configFilePath, configFile);
 const hapPart = formatHap01bDiagnostic(configFile);
 return `[engram] startup env: ${envParts}; ${cfPart}; ${hapPart}`;
}

function describeConfigFile(configFilePath, configFile) {
 if (!configFilePath) {
  return "config_file=unresolved";
 }
 if (!fs.existsSync(configFilePath)) {
  return `config_file=missing(${configFilePath})`;
 }
 if (configFile === null || configFile === undefined) {
  return `config_file=malformed(${configFilePath})`;
 }
 return `config_file=present(${configFilePath})`;
}

function formatHap01bDiagnostic(configFile) {
 if (isInvalidHap01bConfig(configFile)) {
  return "hap_01b=invalid";
 }
 const hapConfig = configFile?.hap_01b;
 if (!isNormalizedHap01bConfig(hapConfig)) {
  return "hap_01b=absent";
 }
 return "hap_01b=present(" +
  `relay_enabled=${hapConfig.relay_enabled},` +
  `relay_revision_len=${hapConfig.relay_revision.length},` +
  `legacy_direct_enforcement=${hapConfig.legacy_direct_enforcement},` +
  `adapter_sha256_len=${hapConfig.adapter_sha256.length},` +
  `registration_token_len=${hapConfig.registration_token.length},` +
  `project_token_count=${Object.keys(hapConfig.project_tokens).length})`;
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
 main().catch((error) => {
  process.stderr.write(`[engram] FATAL: trusted client launch failed: ${error.message}\n`);
  process.exitCode = 1;
 });
}

module.exports = {
 main,
 childEnvForEngram,
 configuredEnvValue,
 appendStartupDiagnosticLog,
 describeConfigFile,
 describeEnvValue,
 emitStartupDiagnostic,
 formatHap01bDiagnostic,
 formatStartupDiagnostic,
 inferCodexPluginDataDir,
 isConfiguredValue,
 isInvalidHap01bConfig,
 parseHap01bConfig,
 readEngramConfigFile,
 resolveConfigFilePath,
 resolvePluginData,
 resolvePluginRoot,
 resolveAndSpawn,
 spawnFailureMessage,
 trimStartupDiagnosticLog,
};
