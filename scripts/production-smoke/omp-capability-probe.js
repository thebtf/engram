#!/usr/bin/env node
"use strict";

const childProcess = require("node:child_process");
const crypto = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");

const {
  EXIT_CODES,
  boundaryFailure,
  buildCapabilityRecord,
  canonicalize,
  exitFor,
  sha256,
} = require("./omp-capability-record.js");

const PLUGIN_KEY = "engram@engram";
const PLUGIN_KEYS = new Set([PLUGIN_KEY, "engram"]);
const PROFILE_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const RUN_ID = /^[a-z][a-z0-9_-]{0,63}$/;
const SHA256 = /^[a-f0-9]{64}$/;
const SAFE_VERSION = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;
const SAFE_ASSET = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const CALLBACK_MAX_TIME_SECONDS = 75;
const CALLBACK_CHILD_TIMEOUT_MS = 80_000;
const DAEMON_TIMEOUT_MS = 30_000;
const CHILD_SETTLE_GRACE_MS = 1_000;
const MAX_REQUEST_BODY_BYTES = 64 * 1024;
const MAX_CONTROL_FILE_BYTES = 2 * 1024 * 1024;
const MAX_TRANSCRIPT_FILE_BYTES = 512 * 1024;
const MAX_TRANSCRIPT_FILES = 64;
const CALLBACK_PROMPT = "Reply exactly HAP01_CALLBACK_SENTINEL.";
const CALLBACK_SENTINEL = "HAP01_CALLBACK_SENTINEL";
const MCP_SEQUENCE = Object.freeze(["initialize", "notifications/initialized", "tools/list"]);

class ProbeError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "ProbeError";
    this.code = code;
  }
}

function fail(code, message) {
  throw new ProbeError(code, message);
}

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype;
}

function safeErrorCode(error, fallback = "PROBE_RUNTIME_FAILURE") {
  return error instanceof ProbeError && /^[A-Z][A-Z0-9_]{0,127}$/.test(error.code) ? error.code : fallback;
}

function defaultDependencies() {
  return {
    arch: process.arch,
    cwd: () => process.cwd(),
    env: process.env,
    fs,
    http,
    now: () => Date.now(),
    os,
    path,
    platform: process.platform,
    randomBytes: crypto.randomBytes,
    requireModule: require,
    setTimeout,
    clearTimeout,
    spawn: childProcess.spawn,
    stderr: (text) => process.stderr.write(text),
    stdout: (text) => process.stdout.write(text),
  };
}

function normalizeDependencies(overrides = {}) {
  return { ...defaultDependencies(), ...overrides };
}

function isInside(root, candidate, pathApi = path) {
  const relative = pathApi.relative(pathApi.resolve(root), pathApi.resolve(candidate));
  return relative === "" || (!relative.startsWith(`..${pathApi.sep}`) && relative !== ".." && !pathApi.isAbsolute(relative));
}

function samePath(left, right, pathApi = path) {
  return pathApi.relative(pathApi.resolve(left), pathApi.resolve(right)) === "";
}

function safeRelative(value, label) {
  if (typeof value !== "string" || value.length === 0) fail("ARTIFACT_MANIFEST_INVALID", `${label} is missing`);
  const normalized = value.replace(/^\.\//, "");
  if (!normalized || normalized.includes("\\") || normalized.startsWith("/") || normalized.split("/").some((part) => part === "." || part === ".." || part === "")) {
    fail("ARTIFACT_MANIFEST_INVALID", `${label} is not a normalized relative path`);
  }
  return normalized;
}

function parseArgs(args, cwd = process.cwd(), pathApi = path) {
  if (!Array.isArray(args)) fail("CLI_INVALID", "arguments must be an array");
  const values = {};
  for (let index = 0; index < args.length; index += 1) {
    const flag = args[index];
    if (!new Set(["--profile", "--run-id", "--evidence-dir"]).has(flag) || Object.hasOwn(values, flag)) {
      fail("CLI_INVALID", "unsupported or duplicate option");
    }
    const value = args[++index];
    if (typeof value !== "string" || value.length === 0 || value.startsWith("--")) fail("CLI_INVALID", "option value is missing");
    values[flag] = value;
  }
  if (Object.keys(values).length !== 3) fail("CLI_INVALID", "profile, run id, and evidence directory are required");
  if (!PROFILE_NAME.test(values["--profile"]) || !RUN_ID.test(values["--run-id"])) {
    fail("CLI_INVALID", "profile or run id is unsafe");
  }
  const root = pathApi.resolve(cwd);
  const evidenceDir = pathApi.resolve(root, values["--evidence-dir"]);
  const expectedEvidenceDir = pathApi.resolve(root, ".agent", "runs", "hap-01", values["--run-id"]);
  if (!samePath(evidenceDir, expectedEvidenceDir, pathApi)) {
    fail("EVIDENCE_DIR_INVALID", "evidence directory is not the owned HAP-01 run directory");
  }
  return Object.freeze({
    cwd: root,
    profile: values["--profile"],
    run_id: values["--run-id"],
    evidence_dir: expectedEvidenceDir,
    scratch_dir: pathApi.resolve(root, ".agent", "tmp", "hap-01", values["--run-id"]),
  });
}

function exists(filePath, deps) {
  try {
    deps.fs.lstatSync(filePath);
    return true;
  } catch (error) {
    if (error && error.code === "ENOENT") return false;
    throw error;
  }
}

function regularFileBytes(filePath, deps, code = "FILE_INVALID") {
  let stat;
  try {
    stat = deps.fs.lstatSync(filePath);
  } catch {
    fail(code, "required file is unavailable");
  }
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > MAX_CONTROL_FILE_BYTES) fail(code, "required file is not a bounded regular file");
  return Buffer.from(deps.fs.readFileSync(filePath));
}
function readBoundedRegularFile(filePath, maxBytes, deps) {
  let stat;
  try {
    stat = deps.fs.lstatSync(filePath);
  } catch {
    fail("TRANSCRIPT_INVALID", "transcript file is unavailable");
  }
  if (!stat.isFile() || stat.isSymbolicLink()) fail("TRANSCRIPT_INVALID", "transcript file is not regular");
  const descriptor = deps.fs.openSync(filePath, "r");
  try {
    const buffer = Buffer.alloc(Math.min(stat.size, maxBytes));
    const count = buffer.length === 0 ? 0 : deps.fs.readSync(descriptor, buffer, 0, buffer.length, 0);
    return { bytes: buffer.subarray(0, count), truncated: stat.size > count };
  } finally {
    deps.fs.closeSync(descriptor);
  }
}

function parseJson(bytes, code) {
  try {
    const value = JSON.parse(Buffer.from(bytes).toString("utf8"));
    if (!isPlainObject(value)) fail(code, "JSON root is not an object");
    return value;
  } catch (error) {
    if (error instanceof ProbeError) throw error;
    fail(code, "JSON is malformed");
  }
}

function digestRegularFile(filePath, deps, code = "FILE_INVALID") {
  let stat;
  try {
    stat = deps.fs.lstatSync(filePath);
  } catch {
    fail(code, "file is unavailable");
  }
  if (!stat.isFile() || stat.isSymbolicLink()) fail(code, "file is not a regular file");
  const hash = crypto.createHash("sha256");
  const descriptor = deps.fs.openSync(filePath, "r");
  try {
    const buffer = Buffer.allocUnsafe(64 * 1024);
    for (; ;) {
      const count = deps.fs.readSync(descriptor, buffer, 0, buffer.length, null);
      if (!count) break;
      hash.update(buffer.subarray(0, count));
    }
  } finally {
    deps.fs.closeSync(descriptor);
  }
  return { size: stat.size, sha256: hash.digest("hex") };
}

function snapshotDigest(filePath, deps) {
  return exists(filePath, deps) ? digestRegularFile(filePath, deps).sha256 : sha256("missing");
}

function createExclusiveDirectory(directory, deps) {
  const parent = deps.path.dirname(directory);
  deps.fs.mkdirSync(parent, { recursive: true, mode: 0o700 });
  try {
    deps.fs.mkdirSync(directory, { recursive: false, mode: 0o700 });
  } catch (error) {
    if (error && error.code === "EEXIST") fail("OWNED_DIRECTORY_EXISTS", "owned run directory already exists");
    throw error;
  }
  const stat = deps.fs.lstatSync(directory);
  if (!stat.isDirectory() || stat.isSymbolicLink()) fail("OWNED_DIRECTORY_INVALID", "owned run directory is unsafe");
}

function removeOwnedDirectory(directory, deps) {
  try {
    deps.fs.rmSync(directory, { recursive: true, force: true, maxRetries: 2 });
    return !exists(directory, deps);
  } catch {
    return false;
  }
}

function writeJsonExclusive(filePath, value, deps) {
  let descriptor;
  try {
    descriptor = deps.fs.openSync(filePath, "wx", 0o600);
    deps.fs.writeFileSync(descriptor, `${canonicalize(value)}\n`, "utf8");
    if (typeof deps.fs.fsyncSync === "function") deps.fs.fsyncSync(descriptor);
  } catch (error) {
    if (error && error.code === "EEXIST") fail("EVIDENCE_WRITE_EXISTS", "evidence file already exists");
    throw error;
  } finally {
    if (descriptor !== undefined) deps.fs.closeSync(descriptor);
  }
}

function configuredPathValues(env) {
  return ["OMP_PROFILE_REGISTRY", "OMP_PLUGIN_LOCK"].map((key) => [key, typeof env[key] === "string" ? env[key].trim() : ""]);
}

function profileRoots(env, pathApi) {
  const roots = [];
  const seen = new Set();
  const add = (candidate) => {
    const resolved = pathApi.resolve(candidate);
    if (!seen.has(resolved)) {
      seen.add(resolved);
      roots.push(resolved);
    }
  };
  for (const key of ["OMP_PROFILE_ROOT", "OMP_PROFILE_DIR", "PI_CODING_AGENT_DIR", "OMP_CONFIG_DIR", "OMP_CONFIG_HOME", "OMP_HOME"]) {
    const value = typeof env[key] === "string" ? env[key].trim() : "";
    if (!value) continue;
    let current = pathApi.resolve(value);
    for (let depth = 0; depth < 5; depth += 1) {
      add(current);
      const parent = pathApi.dirname(current);
      if (parent === current) break;
      current = parent;
    }
  }
  for (const key of ["USERPROFILE", "HOME"]) {
    const value = typeof env[key] === "string" ? env[key].trim() : "";
    if (value) add(pathApi.join(value, ".omp"));
  }
  return roots;
}

function profileDirectories(root, profile, pathApi) {
  return [...new Set([
    root,
    pathApi.join(root, "plugins"),
    pathApi.join(root, profile),
    pathApi.join(root, profile, "plugins"),
    pathApi.join(root, "profiles", profile),
    pathApi.join(root, "profiles", profile, "plugins"),
    pathApi.join(root, "agent"),
    pathApi.join(root, "profiles", profile, "agent"),
  ])];
}

function namedFiles(directories, names, deps) {
  const files = [];
  for (const directory of directories) {
    for (const name of names) {
      const candidate = deps.path.join(directory, name);
      if (exists(candidate, deps)) files.push(candidate);
    }
  }
  return [...new Set(files)];
}

function profileScope(document, profile) {
  if (isPlainObject(document.profiles) && isPlainObject(document.profiles[profile])) return document.profiles[profile];
  if (Array.isArray(document.profiles)) {
    const matched = document.profiles.find((entry) => isPlainObject(entry) && (entry.name === profile || entry.id === profile || entry.profile === profile));
    if (matched) return matched;
  }
  if (isPlainObject(document[profile])) return document[profile];
  return document;
}

function profilePluginEnabled(document, profile) {
  const scope = profileScope(document, profile);
  for (const name of ["enabledPlugins", "enabled_plugins"]) {
    if (!isPlainObject(scope[name])) continue;
    for (const pluginName of PLUGIN_KEYS) {
      if (Object.hasOwn(scope[name], pluginName)) return scope[name][pluginName] === true;
    }
  }
  return true;
}

function flattenEntry(value) {
  if (Array.isArray(value)) return value.flatMap(flattenEntry);
  return isPlainObject(value) ? [value] : [];
}

function pluginEntryCandidates(document, profile) {
  const result = [];
  const visited = new Set();
  function visit(value, depth) {
    if (!isPlainObject(value) || depth > 8 || visited.has(value)) return;
    visited.add(value);
    for (const [key, child] of Object.entries(value)) {
      if (PLUGIN_KEYS.has(key)) result.push(...flattenEntry(child));
      visit(child, depth + 1);
    }
  }
  visit(profileScope(document, profile), 0);
  return result;
}

function fieldString(entry, names) {
  for (const name of names) {
    if (typeof entry[name] === "string" && entry[name].trim()) return entry[name].trim();
  }
  return "";
}

function normalizePluginEntry(entry, label) {
  const version = fieldString(entry, ["version", "pluginVersion", "plugin_version"]);
  const installPath = fieldString(entry, ["installPath", "install_path", "path", "root", "directory"]);
  const dataPath = fieldString(entry, ["dataPath", "data_path", "pluginData", "plugin_data"]);
  const configPath = fieldString(entry, ["configPath", "config_path"]);
  const enabled = entry.enabled !== false && entry.isEnabled !== false && entry.is_enabled !== false;
  if (!version || !SAFE_VERSION.test(version)) fail("PROFILE_REGISTRY_INVALID", `${label} has no canonical version`);
  return { version, installPath, dataPath, configPath, enabled };
}

function selectPluginEntry(document, profile, label, requireInstallPath) {
  if (!profilePluginEnabled(document, profile)) fail("PROFILE_PLUGIN_NOT_ENABLED", `${label} does not enable ${PLUGIN_KEY}`);
  const entries = pluginEntryCandidates(document, profile).map((entry) => normalizePluginEntry(entry, label));
  const enabled = entries.filter((entry) => entry.enabled);
  if (enabled.length === 0) fail("PROFILE_PLUGIN_NOT_ENABLED", `${label} does not enable ${PLUGIN_KEY}`);
  const unique = new Map(enabled.map((entry) => [canonicalize(entry), entry]));
  if (unique.size !== 1) fail("PROFILE_REGISTRY_AMBIGUOUS", `${label} has ambiguous ${PLUGIN_KEY} entries`);
  const selected = [...unique.values()][0];
  if (requireInstallPath && !selected.installPath) fail("PROFILE_REGISTRY_INVALID", `${label} omits installed plugin root`);
  return selected;
}

function resolveProfileFiles(profile, deps) {
  if (typeof deps.resolveProfileFiles === "function") return deps.resolveProfileFiles(profile);
  const explicit = Object.fromEntries(configuredPathValues(deps.env));
  let registryFiles = explicit.OMP_PROFILE_REGISTRY ? [deps.path.resolve(explicit.OMP_PROFILE_REGISTRY)] : [];
  let lockFiles = explicit.OMP_PLUGIN_LOCK ? [deps.path.resolve(explicit.OMP_PLUGIN_LOCK)] : [];
  if ((registryFiles.length === 0) !== (lockFiles.length === 0)) {
    fail("PROFILE_REGISTRY_INVALID", "registry and lock overrides must be supplied together");
  }
  if (registryFiles.length === 0) {
    const directories = profileRoots(deps.env, deps.path).flatMap((root) => profileDirectories(root, profile, deps.path));
    registryFiles = namedFiles(directories, ["installed_plugins.json", "plugins.json", "plugin-registry.json", "registry.json"], deps);
    lockFiles = namedFiles(directories, ["omp-plugins.lock.json"], deps);
  }
  if (registryFiles.length === 0 || lockFiles.length === 0) fail("PROFILE_REGISTRY_UNAVAILABLE", "profile registry or lock is unavailable");
  let lastSelectionError = null;

  for (const registryPath of registryFiles) {
    for (const lockPath of lockFiles) {
      try {
        const registryBytes = regularFileBytes(registryPath, deps, "PROFILE_REGISTRY_INVALID");
        const lockBytes = regularFileBytes(lockPath, deps, "PROFILE_LOCK_INVALID");
        const registry = parseJson(registryBytes, "PROFILE_REGISTRY_INVALID");
        const lock = parseJson(lockBytes, "PROFILE_LOCK_INVALID");
        const registryEntry = selectPluginEntry(registry, profile, "profile registry", true);
        const lockEntry = selectPluginEntry(lock, profile, "plugin lock", false);
        if (registryEntry.version !== lockEntry.version) fail("PROFILE_LOCK_MISMATCH", "registry and lock select different plugin versions");
        if (lockEntry.installPath && !samePath(registryEntry.installPath, lockEntry.installPath, deps.path)) {
          fail("PROFILE_LOCK_MISMATCH", "registry and lock select different install roots");
        }
        return {
          registryPath,
          lockPath,
          registryEntry,
          lockEntry,
          registry_digest: sha256(registryBytes),
          lock_digest: sha256(lockBytes),
        };
      } catch (error) {
        if (!(error instanceof ProbeError)) throw error;
        lastSelectionError = error;
      }
    }
  }
  if (lastSelectionError) throw lastSelectionError;
  fail("PROFILE_LOCK_MISMATCH", "no registry and lock pair selected the enabled plugin");
}

function parseBootstrapPolicy(bytes, packageVersion, platform, arch) {
  const policy = parseJson(bytes, "BOOTSTRAP_POLICY_INVALID");
  const key = `${platform}-${arch}`;
  if (policy.schema_version !== 1 || policy.package_version !== packageVersion || !isPlainObject(policy.targets)) {
    fail("BOOTSTRAP_POLICY_INVALID", "policy does not match installed package");
  }
  const selected = policy.targets[key];
  if (!isPlainObject(selected) || !isPlainObject(selected.desired)) fail("BOOTSTRAP_POLICY_INVALID", "policy does not select this host");
  const target = selected.desired;
  if (!SAFE_VERSION.test(target.version) || target.version !== packageVersion || !SAFE_ASSET.test(target.asset) || !Number.isSafeInteger(target.size) || target.size <= 0 || !SHA256.test(target.sha256)) {
    fail("BOOTSTRAP_POLICY_INVALID", "selected policy object is invalid");
  }
  return { version: target.version, asset: target.asset, size: target.size, sha256: target.sha256 };
}

function resolvePluginData(pluginRoot, entry, deps) {
  if (entry.dataPath) return deps.path.resolve(entry.dataPath);
  if (typeof deps.resolvePluginData === "function") {
    const dataPath = deps.resolvePluginData(pluginRoot, entry);
    if (typeof dataPath === "string" && dataPath) return deps.path.resolve(dataPath);
  }
  try {
    const launcher = deps.requireModule(deps.path.join(pluginRoot, "scripts", "run-engram.js"));
    for (const resolverName of ["resolvePluginData", "inferOmpPluginDataDir", "inferCodexPluginDataDir"]) {
      const resolver = launcher[resolverName];
      if (typeof resolver !== "function") continue;
      const dataPath = resolver(pluginRoot);
      if (typeof dataPath === "string" && dataPath) return deps.path.resolve(dataPath);
    }
  } catch {
    // The installed launcher is an artifact under inspection. It is never executed here.
  }
  fail("PLUGIN_DATA_UNAVAILABLE", "installed plugin data root is unavailable");
}

function inspectInstalledArtifacts(profileFiles, deps) {
  const pluginRoot = deps.path.resolve(profileFiles.registryEntry.installPath);
  const packageBytes = regularFileBytes(deps.path.join(pluginRoot, "package.json"), deps, "ARTIFACT_MANIFEST_INVALID");
  const packageManifest = parseJson(packageBytes, "ARTIFACT_MANIFEST_INVALID");
  if (packageManifest.name !== "engram" || !SAFE_VERSION.test(packageManifest.version) || packageManifest.version !== profileFiles.registryEntry.version) {
    fail("ARTIFACT_MANIFEST_INVALID", "installed package identity does not match registry");
  }
  const ompBytes = regularFileBytes(deps.path.join(pluginRoot, ".omp-plugin", "plugin.json"), deps, "ARTIFACT_MANIFEST_INVALID");
  const ompManifest = parseJson(ompBytes, "ARTIFACT_MANIFEST_INVALID");
  const server = isPlainObject(ompManifest.mcpServers) ? ompManifest.mcpServers.engram : null;
  const extensions = packageManifest.omp && Array.isArray(packageManifest.omp.extensions) ? packageManifest.omp.extensions : [];
  if (ompManifest.name !== "engram" || ompManifest.version !== packageManifest.version || !isPlainObject(server)
    || server.type !== "stdio" || server.command !== "node" || server.cwd !== "." || server.timeout !== 60000
    || !Array.isArray(server.args) || server.args.length !== 1 || extensions.length !== 1) {
    fail("ARTIFACT_MANIFEST_INVALID", "OMP manifest does not identify the installed stdio and extension entrypoints");
  }
  const relativeEntrypoint = safeRelative(server.args[0], "OMP entrypoint");
  const extension = safeRelative(extensions[0], "OMP extension");
  const extensionBytes = regularFileBytes(deps.path.join(pluginRoot, ...extension.split("/")), deps, "ARTIFACT_EXTENSION_INVALID");
  const policyBytes = regularFileBytes(deps.path.join(pluginRoot, "bootstrap-targets.json"), deps, "BOOTSTRAP_POLICY_INVALID");
  const target = parseBootstrapPolicy(policyBytes, packageManifest.version, deps.platform, deps.arch);
  const pluginData = resolvePluginData(pluginRoot, profileFiles.registryEntry, deps);
  const objectsRoot = deps.path.resolve(pluginData, "bin", "objects", "sha256");
  const objectPath = deps.path.resolve(objectsRoot, target.sha256, target.asset);
  if (!isInside(objectsRoot, objectPath, deps.path)) fail("POLICY_OBJECT_MISMATCH", "selected object escapes installed object store");
  const object = digestRegularFile(objectPath, deps, "POLICY_OBJECT_MISMATCH");
  if (object.size !== target.size || object.sha256 !== target.sha256) fail("POLICY_OBJECT_MISMATCH", "selected object does not match policy");
  return {
    pluginRoot,
    pluginData,
    artifact: {
      package_version: packageManifest.version,
      package_manifest_sha256: sha256(packageBytes),
      omp_manifest_sha256: sha256(ompBytes),
      extension_sha256: sha256(extensionBytes),
      bootstrap_policy_sha256: sha256(policyBytes),
      daemon_object_sha256: object.sha256,
      daemon_object_size: object.size,
      relative_entrypoint: relativeEntrypoint,
      daemon_asset: target.asset,
    },
    daemonPath: objectPath,
  };
}

function configuredValue(env, keys) {
  for (const key of keys) {
    const value = typeof env[key] === "string" ? env[key].trim() : "";
    if (value && !/^\$\{[^}]+\}$/.test(value)) return value;
  }
  return "";
}

function readConfigFile(configPath, deps) {
  if (!configPath || !exists(configPath, deps)) return { value: null, digest: sha256("missing") };
  const bytes = regularFileBytes(configPath, deps, "CONFIG_INVALID");
  const value = parseJson(bytes, "CONFIG_INVALID");
  return { value, digest: sha256(bytes) };
}

function resolveConfigPath(installed, deps) {
  const explicit = typeof deps.env.ENGRAM_CONFIG_FILE === "string" ? deps.env.ENGRAM_CONFIG_FILE.trim() : "";
  if (explicit) return deps.path.resolve(explicit);
  if (installed.configPath) return deps.path.resolve(installed.configPath);
  const dataConfig = deps.path.join(installed.pluginData, "config.json");
  if (exists(dataConfig, deps)) return dataConfig;
  try {
    const launcher = deps.requireModule(deps.path.join(installed.pluginRoot, "scripts", "run-engram.js"));
    if (typeof launcher.resolveConfigFilePath === "function") return launcher.resolveConfigFilePath(installed.pluginData);
  } catch {
    // The only fallback is the installed launcher's own configuration resolver.
  }
  return "";
}

function resolveActiveConfig(installed, deps) {
  if (typeof deps.resolveActiveConfig === "function") return deps.resolveActiveConfig(installed);
  const configPath = resolveConfigPath(installed, deps);
  const config = readConfigFile(configPath, deps);
  const serverURL = configuredValue(deps.env, [
    "ENGRAM_URL",
    "ENGRAM_SERVER_URL",
    "CLAUDE_PLUGIN_OPTION_server_url",
    "CLAUDE_PLUGIN_OPTION_SERVER_URL",
    "ENGRAM_CLAUDE_USERCONFIG_URL",
  ]) || (config.value && typeof config.value.server_url === "string" ? config.value.server_url.trim() : "");
  const token = configuredValue(deps.env, [
    "ENGRAM_TOKEN",
    "CLAUDE_PLUGIN_OPTION_api_token",
    "CLAUDE_PLUGIN_OPTION_API_TOKEN",
    "ENGRAM_CLAUDE_USERCONFIG_TOKEN",
  ]) || (config.value && typeof config.value.api_token === "string" ? config.value.api_token.trim() : "");
  let endpoint;
  try {
    endpoint = new URL(serverURL);
  } catch {
    fail("CONFIG_INVALID", "configured endpoint is invalid");
  }
  if (!token || !["http:", "https:"].includes(endpoint.protocol) || !endpoint.host || endpoint.username || endpoint.password || endpoint.search || endpoint.hash) {
    fail("CONFIG_INVALID", "configured daemon credentials are unavailable");
  }
  return { endpoint: endpoint.toString(), token, configPath, config_digest: config.digest };
}

function inputSnapshots(profileFiles, activeConfig, deps) {
  return {
    registry_sha256: snapshotDigest(profileFiles.registryPath, deps),
    lock_sha256: snapshotDigest(profileFiles.lockPath, deps),
    configuration_sha256: activeConfig.configPath ? snapshotDigest(activeConfig.configPath, deps) : sha256("environment-only"),
  };
}

function clearCredentialEnvironment(env) {
  const clean = { ...env };
  let cleared = 0;
  for (const key of Object.keys(clean)) {
    const upper = key.toUpperCase();
    if (upper.startsWith("ENGRAM_") || upper.startsWith("CLAUDE_PLUGIN_OPTION_") || upper === "NODE_OPTIONS") {
      delete clean[key];
      cleared += 1;
    }
  }
  return { env: clean, cleared };
}

function callbackEnvironment(env, loopbackURL, token) {
  const cleared = clearCredentialEnvironment(env);
  return {
    env: { ...cleared.env, ENGRAM_URL: loopbackURL, ENGRAM_TOKEN: token, ENGRAM_QUIET: "0" },
    cleared: cleared.cleared,
  };
}

function daemonEnvironment(env, activeConfig) {
  const cleared = clearCredentialEnvironment(env);
  return { ...cleared.env, ENGRAM_URL: activeConfig.endpoint, ENGRAM_TOKEN: activeConfig.token };
}

function callbackSpec(options, loopbackURL, syntheticToken, deps) {
  const environment = callbackEnvironment(deps.env, loopbackURL, syntheticToken);
  return {
    command: "omp",
    args: ["--profile", options.profile, "--session-dir", options.scratch_dir, "--no-tools", "--max-time", String(CALLBACK_MAX_TIME_SECONDS), "-p", CALLBACK_PROMPT],
    cwd: options.cwd,
    env: environment.env,
    timeout_ms: CALLBACK_CHILD_TIMEOUT_MS,
    cleared_credential_environment: environment.cleared,
  };
}

function fieldLength(value) {
  if (typeof value === "string") return value.length;
  if (Array.isArray(value)) return value.length;
  if (isPlainObject(value)) return Object.keys(value).length;
  return null;
}

function safeRequestProjection(request, body, bodyDigest, order, startedAt, responseStatus, deps) {
  let payload = null;
  try { payload = JSON.parse(body); } catch { /* malformed payload is intentionally represented by no keys */ }
  const fields = isPlainObject(payload)
    ? Object.fromEntries(Object.keys(payload).sort().slice(0, 32).map((key) => [key, fieldLength(payload[key])]))
    : {};
  let pathname = "/";
  try { pathname = new URL(request.url || "/", "http://127.0.0.1").pathname; } catch { }
  const authorizationPresent = Object.entries(request.headers || {}).some(([name, value]) =>
    /(?:authorization|token|api[-_]?key)/i.test(name) && (typeof value === "string" ? value.length > 0 : Array.isArray(value) && value.some((item) => typeof item === "string" && item.length > 0)),
  );
  return {
    order,
    method: typeof request.method === "string" ? request.method : "",
    path: pathname,
    authorization_present: authorizationPresent,
    body_keys: Object.keys(fields),
    field_lengths: fields,
    body_sha256: bodyDigest,
    status: responseStatus,
    timing_ms: Math.max(0, Math.trunc(deps.now() - startedAt)),
  };
}

function fixtureResponse(pathname) {
  if (pathname === "/api/context/inject") return { status: 200, body: { canonical_project: "hap01_fixture_project" } };
  if (pathname === "/api/context/session-start") {
    return { status: 200, body: { issues: [], rules: [], memories: [{ content: "HAP01_REFERENCE_DATA" }] } };
  }
  if (pathname === "/api/hooks/ambient-candidates") {
    return { status: 200, body: { hints: [{ title: "HAP01_REFERENCE_DATA", reason: "reference", score: 1 }] } };
  }
  return { status: 404, body: {} };
}

function createLoopbackFixture(deps) {
  const requests = [];
  const server = deps.http.createServer((request, response) => {
    const startedAt = deps.now();
    const chunks = [];
    let length = 0;
    const hash = crypto.createHash("sha256");
    request.on("data", (chunk) => {
      const bytes = Buffer.from(chunk);
      hash.update(bytes);
      if (length < MAX_REQUEST_BODY_BYTES) chunks.push(bytes.subarray(0, Math.min(bytes.length, MAX_REQUEST_BODY_BYTES - length)));
      length += bytes.length;
    });
    request.once("end", () => {
      let pathname = "/";
      try { pathname = new URL(request.url || "/", "http://127.0.0.1").pathname; } catch { }
      const fixture = fixtureResponse(pathname);
      const projection = safeRequestProjection(
        request,
        Buffer.concat(chunks).toString("utf8"),
        hash.digest("hex"),
        requests.length + 1,
        startedAt,
        fixture.status,
        deps,
      );
      requests.push(projection);
      response.writeHead(fixture.status, { "content-type": "application/json" });
      response.end(JSON.stringify(fixture.body));
    });
    request.once("error", () => response.destroy());
  });
  return {
    requests,
    start: () => new Promise((resolve, reject) => {
      const onError = (error) => reject(error);
      server.once("error", onError);
      server.listen(0, "127.0.0.1", () => {
        server.off("error", onError);
        const address = server.address();
        resolve({ port: address && typeof address === "object" ? address.port : 0 });
      });
    }),
    close: () => new Promise((resolve) => server.close(() => resolve())),
  };
}

function attach(stream, event, handler) {
  if (stream && typeof stream.on === "function") stream.on(event, handler);
}

function once(stream, event, handler) {
  if (!stream) return;
  if (typeof stream.once === "function") stream.once(event, handler);
  else if (typeof stream.on === "function") stream.on(event, handler);
}

function stopChild(child) {
  try {
    if (child && typeof child.kill === "function") return Boolean(child.kill("SIGTERM"));
  } catch {
    return false;
  }
  return false;
}

function runBoundedChild(spec, sentinel, deps) {
  return new Promise((resolve) => {
    let child;
    let settled = false;
    let timedOut = false;
    let stoppedAfterSentinel = false;
    let timeout;
    let grace;
    let sentinelStop;
    let stdoutBytes = 0;
    let stderrBytes = 0;
    let sentinelPresent = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      deps.clearTimeout(timeout);
      deps.clearTimeout(grace);
      deps.clearTimeout(sentinelStop);
      resolve({ stdout_bytes: stdoutBytes, stderr_bytes: stderrBytes, sentinel_present: sentinelPresent, stopped_after_sentinel: stoppedAfterSentinel, ...value });
    };
    try {
      child = deps.spawn(spec.command, spec.args, {
        cwd: spec.cwd,
        env: spec.env,
        stdio: ["ignore", "pipe", "pipe"],
        windowsHide: true,
      });
    } catch {
      finish({ started: false, error: true, exit_code: null, timed_out: false, close_unconfirmed: false });
      return;
    }
    const capture = (stream) => (chunk) => {
      const bytes = Buffer.from(chunk);
      if (stream === "stdout") {
        stdoutBytes += bytes.length;
        if (!sentinelPresent && bytes.toString("utf8").includes(sentinel)) {
          sentinelPresent = true;
          sentinelStop = deps.setTimeout(() => {
            stoppedAfterSentinel = stopChild(child);
          }, CHILD_SETTLE_GRACE_MS);
        }
      } else {
        stderrBytes += bytes.length;
      }
    };
    attach(child.stdout, "data", capture("stdout"));
    attach(child.stderr, "data", capture("stderr"));
    once(child, "error", () => finish({ started: true, error: true, exit_code: null, timed_out: timedOut, close_unconfirmed: false }));
    once(child, "close", (code, signal) => finish({
      started: true,
      error: false,
      exit_code: Number.isInteger(code) ? code : null,
      exit_signal: typeof signal === "string" ? signal : null,
      timed_out: timedOut,
      close_unconfirmed: false,
    }));
    timeout = deps.setTimeout(() => {
      timedOut = true;
      stopChild(child);
      grace = deps.setTimeout(() => finish({ started: true, error: false, exit_code: null, timed_out: true, close_unconfirmed: true }), CHILD_SETTLE_GRACE_MS);
    }, spec.timeout_ms);
  });
}

function sessionTranscriptProjection(directory, deps) {
  const hash = crypto.createHash("sha256");
  let files = 0;
  let bytes = 0;
  let customMessages = 0;
  let untrustedReference = false;
  let toolControl = false;
  let complete = true;
  function walk(current) {
    let entries;
    try {
      entries = deps.fs.readdirSync(current, { withFileTypes: true });
    } catch {
      complete = false;
      return;
    }
    for (const entry of entries) {
      if (files >= MAX_TRANSCRIPT_FILES) {
        complete = false;
        return;
      }
      const candidate = deps.path.join(current, entry.name);
      if (entry.isDirectory()) {
        walk(candidate);
        continue;
      }
      if (!entry.isFile() || !entry.name.endsWith(".jsonl")) continue;
      let bounded;
      try {
        bounded = readBoundedRegularFile(candidate, MAX_TRANSCRIPT_FILE_BYTES, deps);
      } catch {
        complete = false;
        continue;
      }
      const limited = bounded.bytes;
      files += 1;
      bytes += limited.length;
      if (bounded.truncated) complete = false;
      for (const line of limited.toString("utf8").split("\n")) {
        if (!line.trim()) continue;
        let record;
        try {
          record = JSON.parse(line);
        } catch {
          complete = false;
          continue;
        }
        if (!isPlainObject(record) || record.type !== "custom_message" || record.customType !== "engram-memory") continue;
        const content = typeof record.content === "string" ? record.content : "";
        customMessages += 1;
        hash.update(content, "utf8");
        untrustedReference ||= /\bUNTRUSTED_REFERENCE\b/.test(content);
        toolControl ||= Object.keys(record).some((key) => /tool(?:Call|_call|Name|_name)/.test(key)) || /(?:<tool|tool[_ -]?(?:call|use))/i.test(content);
      }
    }
  }
  if (exists(directory, deps)) walk(directory);
  return {
    file_count: files,
    byte_count: bytes,
    custom_message_count: customMessages,
    untrusted_reference_present: untrustedReference,
    tool_control_semantics_present: toolControl,
    content_sha256: hash.digest("hex"),
    complete,
  };
}

async function runCallbackProbe(options, deps) {
  const fixture = typeof deps.createFixture === "function" ? deps.createFixture() : createLoopbackFixture(deps);
  let child = { started: false, error: true, exit_code: null, timed_out: false, close_unconfirmed: false, stdout_bytes: 0, stderr_bytes: 0, sentinel_present: false };
  let error_class = null;
  try {
    const address = await fixture.start();
    if (!address || !Number.isInteger(address.port) || address.port <= 0) fail("FIXTURE_UNAVAILABLE", "loopback fixture did not receive an ephemeral port");
    const syntheticToken = `hap01-${deps.randomBytes(16).toString("hex")}`;
    const spec = callbackSpec(options, `http://127.0.0.1:${address.port}`, syntheticToken, deps);
    child = typeof deps.runChild === "function" ? await deps.runChild(spec, CALLBACK_SENTINEL) : await runBoundedChild(spec, CALLBACK_SENTINEL, deps);
  } catch (error) {
    error_class = safeErrorCode(error, "CALLBACK_RUNTIME_FAILURE");
  } finally {
    try { await fixture.close(); } catch { error_class ||= "FIXTURE_CLOSE_FAILURE"; }
  }
  const transcript = sessionTranscriptProjection(options.scratch_dir, deps);
  return {
    request_count: Array.isArray(fixture.requests) ? fixture.requests.length : 0,
    requests: Array.isArray(fixture.requests) ? fixture.requests : [],
    child,
    transcript,
    error_class,
  };
}

function safeMcpResponse(raw) {
  let message;
  try { message = JSON.parse(raw); } catch { return null; }
  if (!isPlainObject(message)) return null;
  const result = isPlainObject(message.result) ? message.result : {};
  const meta = isPlainObject(result._meta) ? result._meta : isPlainObject(result.meta) ? result.meta : {};
  let subjectProof = "";
  for (const candidate of [meta.engram_authenticated_subject_sha256, meta.engram_auth_subject_sha256, meta.engram_authenticated_subject && meta.engram_authenticated_subject.sha256]) {
    if (typeof candidate === "string" && SHA256.test(candidate)) subjectProof = candidate;
  }
  return {
    id: Number.isInteger(message.id) ? message.id : null,
    projection: {
      received: true,
      error_code: isPlainObject(message.error) && Number.isInteger(message.error.code) ? message.error.code : null,
      result_keys: Object.keys(result).sort(),
      tools_count: Array.isArray(result.tools) ? result.tools.length : null,
      content_sha256: sha256(raw),
    },
    subjectProof,
  };
}

function mcpRequest(id, method, params) {
  const request = { jsonrpc: "2.0", method };
  if (id !== null) request.id = id;
  if (params !== undefined) request.params = params;
  return request;
}

function runDaemonMcp(spec, deps) {
  return new Promise((resolve) => {
    let child;
    let buffer = "";
    let settled = false;
    let timedOut = false;
    let timeout;
    let grace;
    let stderrBytes = 0;
    const result = {
      sequence: [],
      initialize: { received: false, error_code: null, result_keys: [], tools_count: null, content_sha256: sha256("missing") },
      tools_list: { received: false, error_code: null, result_keys: [], tools_count: null, content_sha256: sha256("missing") },
      authenticated_subject_proof_sha256: null,
      child: { started: false, error: true, exit_code: null, timed_out: false, close_unconfirmed: false, stderr_bytes: 0 },
    };
    const finish = () => {
      if (settled) return;
      settled = true;
      deps.clearTimeout(timeout);
      deps.clearTimeout(grace);
      result.child.stderr_bytes = stderrBytes;
      resolve(result);
    };
    const send = (message) => {
      if (!child?.stdin || typeof child.stdin.write !== "function") return false;
      try {
        child.stdin.write(`${JSON.stringify(message)}\n`);
        result.sequence.push(message.method);
        return true;
      } catch {
        return false;
      }
    };
    const stopAfterTools = () => {
      try { child.stdin?.end?.(); } catch { /* process cleanup remains bounded */ }
      stopChild(child);
      grace = deps.setTimeout(finish, CHILD_SETTLE_GRACE_MS);
    };
    try {
      child = deps.spawn(spec.command, [], {
        cwd: spec.cwd,
        env: spec.env,
        stdio: ["pipe", "pipe", "pipe"],
        windowsHide: true,
      });
      result.child.started = true;
    } catch {
      finish();
      return;
    }
    attach(child.stderr, "data", (chunk) => { stderrBytes += Buffer.byteLength(Buffer.from(chunk)); });
    attach(child.stdout, "data", (chunk) => {
      buffer += Buffer.from(chunk).toString("utf8");
      let newline;
      while ((newline = buffer.indexOf("\n")) >= 0) {
        const line = buffer.slice(0, newline);
        buffer = buffer.slice(newline + 1);
        const response = safeMcpResponse(line);
        if (!response) continue;
        if (response.id === 1) {
          result.initialize = response.projection;
          if (response.projection.error_code === null && result.sequence.length === 1) {
            send(mcpRequest(null, "notifications/initialized", {}));
            send(mcpRequest(2, "tools/list", {}));
          }
        } else if (response.id === 2) {
          result.tools_list = response.projection;
          if (response.subjectProof) result.authenticated_subject_proof_sha256 = response.subjectProof;
          stopAfterTools();
        }
      }
    });
    once(child, "error", () => {
      result.child.error = true;
      result.child.timed_out = timedOut;
      finish();
    });
    once(child, "close", (code) => {
      result.child.error = false;
      result.child.exit_code = Number.isInteger(code) ? code : null;
      result.child.timed_out = timedOut;
      finish();
    });
    if (!send(mcpRequest(1, "initialize", {
      protocolVersion: "2024-11-05",
      capabilities: {},
      clientInfo: { name: "omp-advisor-probe", version: "1" },
    }))) {
      result.child.error = true;
      finish();
      return;
    }
    timeout = deps.setTimeout(() => {
      timedOut = true;
      result.child.timed_out = true;
      stopChild(child);
      grace = deps.setTimeout(() => {
        result.child.close_unconfirmed = true;
        finish();
      }, CHILD_SETTLE_GRACE_MS);
    }, spec.timeout_ms);
  });
}

function muxcoreCandidateFiles(root, deps) {
  const files = [];
  const registryRoot = deps.path.join(root, "muxcore-daemon-registry");
  let entries = [];
  try {
    entries = deps.fs.readdirSync(registryRoot, { withFileTypes: true });
  } catch {
    entries = [];
  }
  for (const entry of entries) {
    if (entry.isFile() && entry.name.endsWith(".json")) files.push(deps.path.join(registryRoot, entry.name));
  }
  const marker = deps.path.join(root, "engram-muxd.ctl.sock.marker.json");
  if (exists(marker, deps)) files.push(marker);
  return files;
}

function muxIdentity(value, rawDigest) {
  const read = (names) => names.map((name) => value[name]).find((item) => typeof item === "string" && item) || "";
  const pid = value.pid ?? value.PID;
  const generation = read(["daemon_generation", "daemonGeneration", "DaemonGeneration"]);
  const executable = read(["exe", "executable", "Exe"]);
  const executableName = executable.replaceAll("\\", "/").split("/").pop().toLowerCase();
  const engine = read(["engine_name", "engineName", "product_name", "productName", "namespace", "Namespace"]);
  const marker = value.schema_version === 2 && Number.isInteger(pid) && pid > 0 && generation && executableName.startsWith("engram");
  const descriptor = engine === "engram" && Number.isInteger(pid) && pid > 0;
  if (!marker && !descriptor) return null;
  return {
    kind: marker ? "marker" : "descriptor",
    sha256: rawDigest,
    process_binding_sha256: sha256(String(pid)),
    marker_binding_sha256: marker ? sha256(`${pid}\u0000${generation}\u0000${executable}`) : null,
  };
}

function discoverMuxcoreArtifacts(deps, reachable = false) {
  const root = typeof deps.tempDir === "string" ? deps.tempDir : deps.os.tmpdir();
  const descriptors = [];
  const markers = [];
  for (const file of muxcoreCandidateFiles(root, deps)) {
    let bytes;
    try { bytes = regularFileBytes(file, deps, "MUXCORE_DISCOVERY_INVALID"); } catch { continue; }
    let value;
    try { value = JSON.parse(bytes.toString("utf8")); } catch { continue; }
    if (!isPlainObject(value)) continue;
    const identity = muxIdentity(value, sha256(bytes));
    if (!identity) continue;
    if (identity.kind === "marker") markers.push(identity);
    else descriptors.push(identity);
  }
  const descriptor = descriptors.length === 1 ? descriptors[0] : null;
  const marker = markers.length === 1 ? markers[0] : null;
  return {
    descriptor: descriptor && { sha256: descriptor.sha256, peer_kind: "muxcore", reachable: Boolean(reachable), process_binding_sha256: descriptor.process_binding_sha256 },
    marker: marker && { sha256: marker.sha256, peer_kind: "muxcore", process_binding_sha256: marker.process_binding_sha256, marker_binding_sha256: marker.marker_binding_sha256 },
    correlated: Boolean(descriptor && marker && descriptor.process_binding_sha256 === marker.process_binding_sha256),
    ambiguous: descriptors.length > 1 || markers.length > 1,
  };
}

function evidenceReference(id, proofClass, value) {
  return { id, class: proofClass, sha256: sha256(canonicalize(value)) };
}

function guarantee(state, reference, reason = []) {
  return { state, proof_refs: [reference], reason_codes: [...reason].sort() };
}

function findRequest(requests, pathname) {
  return requests.find((request) => request.path === pathname) || null;
}

function classifyExtensionRuntime(runtime) {
  const reference = evidenceReference("extension-runtime", "INSTALLED_RUNTIME", runtime);
  const identity = findRequest(runtime.requests, "/api/context/inject");
  const sessionStart = findRequest(runtime.requests, "/api/context/session-start");
  const ambient = findRequest(runtime.requests, "/api/hooks/ambient-candidates");
  const orderedSessionStart = Boolean(identity && sessionStart && identity.order < sessionStart.order && (!ambient || sessionStart.order < ambient.order));
  const hasAnchor = Boolean(ambient && ambient.body_keys.includes("session_id") && ambient.body_keys.some((key) => /(?:leaf|turn|anchor)/i.test(key)));
  const extensionObserved = runtime.requests.length > 0;
  const directAuthorization = runtime.requests.some((request) => request.authorization_present);
  return {
    session_start_prewarm: orderedSessionStart
      ? guarantee("OBSERVED", reference)
      : guarantee(extensionObserved ? "MISSING" : "UNAVAILABLE", reference, ["SESSION_START_PREWARM_NOT_OBSERVED"]),
    before_agent_start_anchor: hasAnchor
      ? guarantee("OBSERVED", reference)
      : guarantee(ambient ? "MISSING" : "UNAVAILABLE", reference, ["BEFORE_AGENT_START_ANCHOR_NOT_OBSERVED"]),
    callback_budget: guarantee("UNAVAILABLE", reference, ["CALLBACK_BUDGET_NOT_EXPOSED"]),
    renderer_untrusted_reference: runtime.transcript.untrusted_reference_present && !runtime.transcript.tool_control_semantics_present
      ? guarantee("OBSERVED", reference)
      : guarantee(runtime.transcript.custom_message_count > 0 ? "MISSING" : "UNAVAILABLE", reference, ["UNTRUSTED_REFERENCE_FRAME_NOT_OBSERVED"]),
    direct_credential_disabled: directAuthorization
      ? guarantee("CONTRADICTED", reference, ["DIRECT_REST_AUTHORIZATION_OBSERVED"])
      : guarantee(extensionObserved ? "OBSERVED" : "UNAVAILABLE", reference, extensionObserved ? [] : ["DIRECT_CREDENTIAL_PATH_NOT_EXPOSED"]),
  };
}

function exactMcpSequence(sequence) {
  return Array.isArray(sequence) && sequence.length === MCP_SEQUENCE.length && sequence.every((method, index) => method === MCP_SEQUENCE[index]);
}

function classifyDaemonRuntime(runtime) {
  const reference = evidenceReference("daemon-runtime", "INSTALLED_RUNTIME", runtime);
  const toolsListReached = exactMcpSequence(runtime.sequence) && runtime.tools_list.received && runtime.tools_list.error_code === null;
  const authenticated = toolsListReached && typeof runtime.authenticated_subject_proof_sha256 === "string" && SHA256.test(runtime.authenticated_subject_proof_sha256);
  return authenticated
    ? guarantee("OBSERVED", reference)
    : guarantee("UNAVAILABLE", reference, [toolsListReached ? "AUTH_SUBJECT_NOT_EXPOSED" : "TOOLS_LIST_INITIALIZE_NOT_OBSERVED"]);
}

function capabilityGuarantees(artifact, extensionRuntime, daemonRuntime) {
  const artifactReference = evidenceReference("installed-artifact", "INSTALLED_ARTIFACT", artifact);
  return {
    installed_artifact: guarantee("OBSERVED", artifactReference),
    ...classifyExtensionRuntime(extensionRuntime),
    daemon_authenticated_initialize: classifyDaemonRuntime(daemonRuntime),
  };
}

function assertSafeEvidence(value) {
  const serialized = canonicalize(value);
  if (/([A-Za-z]:[\\/]|(?:^|["'])\/(?:home|Users|private|tmp)(?:[\\/]|["']))/i.test(serialized)) {
    fail("EVIDENCE_REDACTION_FAILURE", "evidence contains an absolute filesystem path");
  }
  const prohibited = new Set(["token", "secret", "password", "prompt", "callback", "pid", "daemon_generation"]);
  function visit(node, parent) {
    if (Array.isArray(node)) {
      for (const item of node) visit(item, parent);
      return;
    }
    if (!isPlainObject(node)) return;
    for (const [key, child] of Object.entries(node)) {
      if (prohibited.has(key) && parent !== "field_lengths") {
        fail("EVIDENCE_REDACTION_FAILURE", "evidence contains a prohibited raw field");
      }
      if (parent === "field_lengths" && child !== null && (!Number.isSafeInteger(child) || child < 0)) {
        fail("EVIDENCE_REDACTION_FAILURE", "evidence field length is invalid");
      }
      visit(child, key);
    }
  }
  visit(value, "");
}

async function runProbe(options, overrides = {}) {
  const deps = normalizeDependencies(overrides);
  const profileFiles = resolveProfileFiles(options.profile, deps);
  const inspected = inspectInstalledArtifacts(profileFiles, deps);
  const activeConfig = resolveActiveConfig({ ...inspected, configPath: profileFiles.registryEntry.configPath }, deps);
  const before = inputSnapshots(profileFiles, activeConfig, deps);
  createExclusiveDirectory(options.scratch_dir, deps);
  let extensionRuntime;
  let daemonRuntime;
  let scratchCleaned = false;
  try {
    const muxBefore = discoverMuxcoreArtifacts(deps);
    extensionRuntime = await runCallbackProbe(options, deps);
    const daemonSpec = {
      command: inspected.daemonPath,
      cwd: options.scratch_dir,
      env: daemonEnvironment(deps.env, activeConfig),
      timeout_ms: DAEMON_TIMEOUT_MS,
    };
    try {
      daemonRuntime = typeof deps.runDaemon === "function" ? await deps.runDaemon(daemonSpec) : await runDaemonMcp(daemonSpec, deps);
    } catch (error) {
      daemonRuntime = {
        sequence: [],
        initialize: { received: false, error_code: null, result_keys: [], tools_count: null, content_sha256: sha256("missing") },
        tools_list: { received: false, error_code: null, result_keys: [], tools_count: null, content_sha256: sha256("missing") },
        authenticated_subject_proof_sha256: null,
        child: { started: false, error: true, exit_code: null, timed_out: false, close_unconfirmed: false, stderr_bytes: 0 },
        error_class: safeErrorCode(error, "DAEMON_RUNTIME_FAILURE"),
      };
    }
    const muxAfter = discoverMuxcoreArtifacts(deps, Boolean(daemonRuntime.tools_list?.received));
    const after = inputSnapshots(profileFiles, activeConfig, deps);
    scratchCleaned = removeOwnedDirectory(options.scratch_dir, deps);
    const effects = {
      profile_registry_unchanged: before.registry_sha256 === after.registry_sha256 && before.lock_sha256 === after.lock_sha256,
      configuration_unchanged: before.configuration_sha256 === after.configuration_sha256,
      probe_writes_confined: true,
      scratch_cleaned: scratchCleaned,
    };
    const receipt = {
      schema: "omp-advisor-1-receipt",
      receipt_id: `hap-01-receipt-${options.run_id}`,
      run_id: options.run_id,
      input_digests: { before, after },
      artifact: inspected.artifact,
      extension_runtime: extensionRuntime,
      daemon_runtime: daemonRuntime,
      muxcore: { before: muxBefore, after: muxAfter },
      effects,
    };
    assertSafeEvidence(receipt);
    const evidenceDigest = sha256(canonicalize(receipt));
    const record = buildCapabilityRecord({
      host: { platform: deps.platform, arch: deps.arch },
      artifact: inspected.artifact,
      probe: { receipt_id: receipt.receipt_id, run_id: options.run_id, evidence_digest: evidenceDigest },
      guarantees: capabilityGuarantees(inspected.artifact, extensionRuntime, daemonRuntime),
      effects,
    });
    writeJsonExclusive(deps.path.join(options.evidence_dir, "receipt.json"), receipt, deps);
    writeJsonExclusive(deps.path.join(options.evidence_dir, "record.json"), record, deps);
    return { record, receipt };
  } finally {
    if (!scratchCleaned && exists(options.scratch_dir, deps)) removeOwnedDirectory(options.scratch_dir, deps);
  }
}

async function main(args = process.argv.slice(2), overrides = {}) {
  const deps = normalizeDependencies(overrides);
  try {
    const options = parseArgs(args, deps.cwd(), deps.path);
    createExclusiveDirectory(options.evidence_dir, deps);
    const { record } = await runProbe(options, deps);
    deps.stdout(`${canonicalize(record)}\n`);
    return exitFor(record);
  } catch (error) {
    const failure = boundaryFailure(safeErrorCode(error, "PROBE_BOUNDARY_FAILURE"), error && error.message ? error.message : "probe failed");
    deps.stderr(`${canonicalize(failure)}\n`);
    return EXIT_CODES.BOUNDARY_FAILURE;
  }
}

if (require.main === module) {
  main().then((code) => { process.exitCode = code; });
}

module.exports = {
  CALLBACK_MAX_TIME_SECONDS,
  CALLBACK_PROMPT,
  CALLBACK_SENTINEL,
  MCP_SEQUENCE,
  PLUGIN_KEY,
  ProbeError,
  assertSafeEvidence,
  callbackEnvironment,
  callbackSpec,
  capabilityGuarantees,
  classifyDaemonRuntime,
  classifyExtensionRuntime,
  clearCredentialEnvironment,
  createExclusiveDirectory,
  createLoopbackFixture,
  daemonEnvironment,
  discoverMuxcoreArtifacts,
  exactMcpSequence,
  inputSnapshots,
  inspectInstalledArtifacts,
  main,
  parseArgs,
  parseBootstrapPolicy,
  resolveActiveConfig,
  resolveProfileFiles,
  runBoundedChild,
  runCallbackProbe,
  runDaemonMcp,
  runProbe,
  safeMcpResponse,
  safeRequestProjection,
  sessionTranscriptProjection,
  writeJsonExclusive,
};
