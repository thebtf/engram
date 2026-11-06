#!/usr/bin/env node
// run-engram.js - Cross-platform wrapper that execs the correct engram binary.
// Used as plugin MCP command to handle Windows .exe suffix and path resolution.

const { execFile, spawnSync } = require("child_process");
const { Worker } = require('node:worker_threads');
const path = require("path");
const fs = require("fs");
const os = require("os");
const crypto = require('node:crypto');
const { DOWNLOAD_DEADLINE_MS, hashFile, loadPolicy, objectRoots, resolveForLaunch } = require("./ensure-binary.js");
const { installationClientInstanceID } = require("./client-instance.js");

const STARTUP_DIAGNOSTIC_LOG_MAX_BYTES = 128 * 1024;
const TASK_CONTEXT_COMPLETION_RESERVE_MS = 30;
const TASK_CONTEXT_STATUSES = new Set(['ok', 'empty', 'not_ready', 'stale', 'context_required', 'forbidden', 'unavailable', 'deadline_exceeded']);
const TASK_CONTEXT_REASONS = new Set([
 'OK', 'NO_MATCH', 'INSTALLATION_NOT_READY', 'DAEMON_NOT_READY', 'OPERATION_NOT_READY',
 'CONTEXT_REQUIRED', 'CONTEXT_MISMATCH', 'PROJECT_CONTEXT_REQUIRED', 'PROJECT_CONTEXT_MISMATCH',
 'PERMISSION_DENIED', 'SOURCE_UNAVAILABLE', 'VIEW_RETIRED', 'CHECKOUT_OFFLINE', 'INDEX_CATCHING_UP',
 'REGISTRATION_PROFILE_UNBOUND', 'SERVER_UNAVAILABLE', 'TRANSPORT_UNAVAILABLE', 'EXPOSURE_UNAVAILABLE',
 'BUDGET_EXCEEDED', 'INVALID_REQUEST', 'INVALID_RESPONSE', 'DEADLINE_EXCEEDED',
]);

function validTaskContextResponse(response) {
 if (!response || typeof response !== 'object' || Array.isArray(response) || response.schema !== 1 ||
  !TASK_CONTEXT_STATUSES.has(response.status) || !TASK_CONTEXT_REASONS.has(response.reason_code) ||
  Object.keys(response).some(key => !['schema', 'status', 'reason_code', 'project', 'packet', 'knowledge', 'code_status', 'code_reason'].includes(key))) return false;
 if (response.code_status !== undefined && !TASK_CONTEXT_STATUSES.has(response.code_status)) return false;
 if (response.code_reason !== undefined && !TASK_CONTEXT_REASONS.has(response.code_reason)) return false;
 const knowledge = response.knowledge;
 if (knowledge !== undefined) {
  if (!knowledge || typeof knowledge !== 'object' || Array.isArray(knowledge) || Object.keys(knowledge).some(k => !['memory_status', 'note_status', 'rule_status', 'items'].includes(k)) || !Array.isArray(knowledge.items) || knowledge.items.length > 9) return false;
  const counts = { memory: 0, note: 0, rule: 0 };
  for (const item of knowledge.items) {
   if (!item || typeof item !== 'object' || Array.isArray(item) || Object.keys(item).some(k => !['kind', 'id', 'version', 'content_hash', 'source_hash', 'excerpt', 'path', 'content'].includes(k)) || !Object.hasOwn(counts, item.kind) || !Number.isSafeInteger(item.id) || item.id < 1 || (item.version !== undefined && (!Number.isSafeInteger(item.version) || item.version < 1)) || (item.kind === 'note' && (!item.version || !item.path)) || typeof item.content !== 'string' || !item.content.trim() || Buffer.byteLength(item.content) > 4000 || Buffer.from(item.content).toString('utf8') !== item.content || (item.path !== undefined && (typeof item.path !== 'string' || Buffer.byteLength(item.path) > 4096 || Buffer.from(item.path).toString('utf8') !== item.path)) || typeof item.excerpt !== 'boolean' || !/^[0-9a-f]{64}$/.test(item.content_hash ?? '') || !/^[0-9a-f]{64}$/.test(item.source_hash ?? '')) return false;
   if (crypto.createHash('sha256').update(item.content, 'utf8').digest('hex') !== item.content_hash || (!item.excerpt && item.source_hash !== item.content_hash)) return false;
   counts[item.kind]++;
  }
  for (const kind of Object.keys(counts)) if (!['ok', 'empty', 'unavailable', 'deadline_exceeded'].includes(knowledge[`${kind}_status`]) || (knowledge[`${kind}_status`] === 'ok') !== (counts[kind] > 0)) return false;
 }
 if (response.status === 'ok' && !response.packet && !knowledge?.items.length) return false;
 return true;
}

async function main() {
 if (process.argv[2] === 'codex-config') {
  process.stdout.write(codexMcpConfig());
  return;
 }
 const pluginRoot = resolvePluginRoot();
 const pluginData = resolvePluginData(pluginRoot);

 const configFilePath = resolveConfigFilePath(pluginData);
 const configFile = readEngramConfigFile(configFilePath);

 const localProjectCommand = process.argv[2] === 'project';
 if (!localProjectCommand) emitStartupDiagnostic(pluginData, configFilePath, configFile);


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
 if (!serverURL && !localProjectCommand) {
  process.stderr.write(
   "[engram] FATAL: ENGRAM_URL is empty. Configure Engram before first use.\n" +
   "Universal (all harnesses): create ~/.engram/config.json with {\"server_url\":\"http://...\"} (add api_token when server auth is enabled).\n" +
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
 process.env.ENGRAM_CLIENT_INSTANCE_ID = configuredEnvValue(
  "ENGRAM_CLIENT_INSTANCE_ID",
  "CLAUDE_PLUGIN_OPTION_client_instance_id",
  "CLAUDE_PLUGIN_OPTION_CLIENT_INSTANCE_ID",
  "ENGRAM_CLAUDE_USERCONFIG_CLIENT_INSTANCE_ID"
 ) || configFile?.client_instance_id || "";
 process.env.ENGRAM_TOKEN = token;
 const childEnv = childEnvForEngram(process.env);

 if (process.env.ENGRAM_AUTH_ADMIN_TOKEN) {
  process.stderr.write(
   "[engram] WARN: ENGRAM_AUTH_ADMIN_TOKEN is set on this workstation. v6 forbids " +
   "this — the operator key belongs ONLY on the server host. Remove it from " +
   "your local agent config; when server auth is enabled use ENGRAM_TOKEN with a dashboard-issued keycard.\n"
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

async function resolveClient(options, projectOnly = false) {
 let policy;
 if (!options.resolve) {
  policy = loadPolicy(options.pluginRoot);
  // A predecessor may not implement this package's project resolve interface.
  policy = { ...policy, target: { ...policy.target, predecessor: null } };
 }
 const resolved = await (options.resolve || resolveForLaunch)({
  pluginRoot: options.pluginRoot, pluginData: options.pluginData, policy,
  signal: options.signal, timeoutMs: options.timeoutMs, deadlineAt: options.deadlineAt,
  acquire: options.acquire, check: options.check,
  ...(projectOnly ? { parserTarget: null } : {}),
 });
 if (!(options.hash || hashFile)(resolved.path, resolved.target, (options.roots || objectRoots)(options.pluginData, options).objects, options)) {
  throw new Error("resolved client failed final integrity verification");
 }
 return resolved;
}

async function prepareHookClient(options = {}) {
 const now = Date.now();
 const pluginRoot = options.pluginRoot || resolvePluginRoot();
 const pluginData = options.pluginData || resolvePluginData(pluginRoot);
 const deadlineAt = Math.min(options.deadlineAt ?? now + DOWNLOAD_DEADLINE_MS,
  now + (options.timeoutMs ?? DOWNLOAD_DEADLINE_MS), now + DOWNLOAD_DEADLINE_MS);
 await resolveClient({ ...options, pluginRoot, pluginData, deadlineAt }, true);
}

function resolveHostRoot(env = process.env, cwd = process.cwd()) {
 return path.resolve(env.CLAUDE_PROJECT_DIR || cwd);
}

async function resolveLogicalProject(root, options = {}) {
 if (typeof root !== 'string' || !root.trim()) throw new Error('PROJECT_CONTEXT_REQUIRED');
 if (options.signal?.aborted) throw new Error('PROJECT_RESOLUTION_ABORTED');
 options = { ...options, deadlineAt: options.deadlineAt ?? (options.timeoutMs === undefined ? undefined : Date.now() + options.timeoutMs) };
 const pluginRoot = options.pluginRoot || resolvePluginRoot();
 const pluginData = options.pluginData || resolvePluginData(pluginRoot);
 const resolved = await resolveClient({ ...options, pluginRoot, pluginData }, true);
 const remaining = options.deadlineAt === undefined ? 15000 : options.deadlineAt - Date.now();
 if (options.signal?.aborted) throw new Error('PROJECT_RESOLUTION_ABORTED');
 if (remaining <= 0) throw new Error('PROJECT_RESOLUTION_DEADLINE_EXCEEDED');
 const raw = await new Promise((resolve, reject) => {
  (options.execFile || execFile)(resolved.path, ['project', 'resolve', '--root', root, '--json', ...(options.check === true ? ['--check'] : [])], {
   env: childEnvForEngram(options.env || process.env), encoding: 'utf8', windowsHide: true,
   timeout: remaining, maxBuffer: 4096, signal: options.signal,
  }, (error, stdout) => {
   if (error) {
    let code = 'PROJECT_RESOLUTION_UNAVAILABLE';
    let guidance = '';
    try {
     const result = JSON.parse(stdout);
     if (typeof result.code === 'string' && /^PROJECT_[A-Z_]+$/.test(result.code)) code = result.code;
     if (typeof result.guidance === 'string' && result.guidance.length <= 3072) guidance = result.guidance;
    } catch { }
    reject(Object.assign(new Error(guidance ? `${code}: ${guidance}` : code), { code, guidance }));
   } else resolve(stdout);
  });
 });
 if (options.signal?.aborted) throw new Error('PROJECT_RESOLUTION_ABORTED');
 if (options.deadlineAt !== undefined && Date.now() >= options.deadlineAt) throw new Error('PROJECT_RESOLUTION_DEADLINE_EXCEEDED');
 let project;
 try { project = JSON.parse(raw); } catch { throw new Error('PROJECT_CLIENT_UPGRADE_REQUIRED'); }
 if (options.check === true) {
  project = { project_id: projectFromInspection(project) };
 }
 if (!project || !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(project.project_id) ||
  project.project_id === '00000000-0000-0000-0000-000000000000' ||
  (project.name !== undefined && typeof project.name !== 'string')) throw new Error('PROJECT_IDENTITY_INVALID');
 return { project_id: project.project_id, ...(project.name !== undefined ? { name: project.name } : {}) };
}

function projectFromInspection(inspection) {
 if (inspection?.status !== 'resolved') throw new Error(inspection?.status === 'migration_required' ? 'PROJECT_MIGRATION_REQUIRED' : 'PROJECT_CONTEXT_REQUIRED');
 const binding = inspection.binding;
 if (!binding || binding.removed === true ||
  !['manual_alias', 'git_remote', 'portable_marker', 'common_dir_cache'].includes(binding.match) ||
  (binding.identity_status != null && binding.identity_status !== 'active') ||
  (binding.project_key != null && binding.project_key !== binding.id) ||
  !/^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(binding.id ?? '') ||
  binding.id === '00000000-0000-0000-0000-000000000000') throw new Error('PROJECT_IDENTITY_INVALID');
 return binding.id;
}

function executeTaskContextProcess(data, { signal, createTaskContextWorker } = {}) {
 return new Promise((resolve, reject) => {
  if (signal?.aborted || Date.now() >= data.deadlineAt) return reject(new Error('TASK_CONTEXT_STOPPED'));
  const cancellation = new SharedArrayBuffer(4);
  let worker, result, settled = false;
  const fail = () => { if (!settled) { settled = true; reject(new Error('TASK_CONTEXT_STOPPED')); } };
  const cancel = () => {
   Atomics.store(new Int32Array(cancellation), 0, 1);
   try { worker?.postMessage('cancel'); } catch { }
   fail();
  };
  signal?.addEventListener('abort', cancel, { once: true });
  try {
   worker = (createTaskContextWorker || ((file, options) => new Worker(file, options)))(path.join(__dirname, 'task-context-worker.js'), {
    env: {}, execArgv: [], stdout: true, stderr: true,
    workerData: { schema: 1, ...data, cancellation },
   });
  } catch {
   signal?.removeEventListener('abort', cancel);
   fail();
   return;
  }
  worker.stdout.resume();
  worker.stderr.resume();
  worker.on('message', message => { if (message?.type === 'closed') result = message; });
  worker.once('error', cancel);
  worker.once('exit', code => {
   signal?.removeEventListener('abort', cancel);
   if (settled) return;
   if (signal?.aborted || Date.now() >= data.deadlineAt || code !== 0 || result?.ok !== true ||
    !(result.bytes instanceof ArrayBuffer) || result.bytes.byteLength > data.maxBytes) return fail();
   settled = true;
   resolve(Buffer.from(result.bytes));
  });
  // Do not terminate/unref the worker when the callback expires. Its listener
  // owns cancellation until even a blocked spawn returns and its child closes.
  if (signal?.aborted || Date.now() >= data.deadlineAt) cancel();
 });
}

async function requestTaskContext(facts = {}, options = {}) {
 const closed = (status, reason_code) => ({ schema: 1, status, reason_code });
 const now = Date.now();
 const deadlineAt = Math.min(Number.isSafeInteger(options.deadlineAt) ? options.deadlineAt : now + 500, now + 500) - TASK_CONTEXT_COMPLETION_RESERVE_MS;
 if (deadlineAt <= now) return closed('deadline_exceeded', 'DEADLINE_EXCEEDED');
 if (typeof facts.cwd !== 'string' || !path.isAbsolute(facts.cwd) || typeof facts.prompt !== 'string' || !facts.prompt.trim() ||
  Buffer.byteLength(facts.prompt) > 16384 || Buffer.from(facts.prompt).toString('utf8') !== facts.prompt ||
  typeof facts.session !== 'string' || !/^[^\s\p{Cc}/\\@]{1,256}$/u.test(facts.session)) return closed('unavailable', 'INVALID_REQUEST');
 const maxBytes = Math.max(1024, Math.min(32768, Number.isSafeInteger(options.maxBytes) ? options.maxBytes : 16384));
 const input = JSON.stringify({ schema: 1, host_session_id: facts.session, query: facts.prompt, deadline_unix_ms: deadlineAt, max_bytes: maxBytes }) + '\n';
 if (Buffer.byteLength(input) > 20480) return closed('unavailable', 'BUDGET_EXCEEDED');
 const controller = new AbortController();
 const abort = () => controller.abort();
 let onAbort, deadlineExpired = false;
 const stopped = new Promise(resolve => {
  onAbort = () => resolve(closed(deadlineExpired || Date.now() >= deadlineAt ? 'deadline_exceeded' : 'unavailable', deadlineExpired || Date.now() >= deadlineAt ? 'DEADLINE_EXCEEDED' : 'CANCELLED'));
  controller.signal.addEventListener('abort', onAbort, { once: true });
 });
 const timer = setTimeout(() => { deadlineExpired = true; abort(); }, Math.max(1, deadlineAt - Date.now()));
 options.signal?.addEventListener('abort', abort, { once: true });
 if (options.signal?.aborted) abort();
 const remaining = () => {
  if (controller.signal.aborted || Date.now() >= deadlineAt) throw new Error('TASK_CONTEXT_STOPPED');
  return Math.max(1, deadlineAt - Date.now());
 };
 const operation = (async () => {
  const pluginRoot = options.pluginRoot || resolvePluginRoot();
  const pluginData = options.pluginData || resolvePluginData(pluginRoot);
  remaining();
  const resolved = await resolveClient({ ...options, pluginRoot, pluginData, signal: controller.signal, deadlineAt, timeoutMs: remaining(), acquire: false, check: true }, true);
  remaining();
  if (!path.isAbsolute(resolved.path)) throw new Error('TASK_CONTEXT_STOPPED');
  const stdout = await executeTaskContextProcess({
   path: resolved.path, cwd: facts.cwd,
   env: childEnvForEngram(options.env || process.env), input, deadlineAt, maxBytes,
  }, { signal: controller.signal, createTaskContextWorker: options.createTaskContextWorker });
  remaining();
  if (Buffer.byteLength(stdout) > maxBytes) return closed('unavailable', 'BUDGET_EXCEEDED');
  const text = new TextDecoder('utf-8', { fatal: true }).decode(stdout);
  const response = JSON.parse(text);
  if (!text.endsWith('\n') || !validTaskContextResponse(response)) return closed('unavailable', 'INVALID_RESPONSE');
  if (response.project?.status === 'resolved') projectFromInspection(response.project);
  remaining();
  return response;
 })().catch(error => {
  const timedOut = deadlineExpired || Date.now() >= deadlineAt || (error?.killed === true && error.code === null && error.signal === 'SIGTERM');
  return closed(timedOut ? 'deadline_exceeded' : 'unavailable', timedOut ? 'DEADLINE_EXCEEDED' : 'CLIENT_UNAVAILABLE');
 });
 try { return await Promise.race([operation, stopped]); }
 finally {
  clearTimeout(timer);
  options.signal?.removeEventListener('abort', abort);
  controller.signal.removeEventListener('abort', onAbort);
  controller.abort();
 }
}

async function resolveAndSpawn(options) {
 const projectOnly = options.args?.[0] === 'project';
 const resolved = await resolveClient(options, projectOnly);
 const hash = options.hash || hashFile;
 const roots = options.roots || objectRoots;
 const env = { ...options.env };
 if (!projectOnly) env.ENGRAM_CLIENT_INSTANCE_ID = env.ENGRAM_CLIENT_INSTANCE_ID || installationClientInstanceID(options.pluginData);
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
  stdio: "inherit", env, cwd: options.cwd || resolveHostRoot(env),
 });
 const failure = spawnFailureMessage(result, "engram exec");
 if (failure) throw new Error(failure.trim());
 return result.status ?? 0;
}

function codexMcpConfig() {
 return '[mcp_servers.engram]\ncommand = "node"\nargs = ' + JSON.stringify([path.resolve(__filename)]) + '\nstartup_timeout_sec = 60\n';
}

function childEnvForEngram(env = process.env) {
 const childEnv = { ...env };
 for (const key of Object.keys(childEnv)) {
  const canonical = key.toUpperCase();
  if (canonical === "ENGRAM_AUTH_ADMIN_TOKEN" || canonical.startsWith("ENGRAM_HAP_01B_")) {
   delete childEnv[key];
  }
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
 * Returns normalized URL, token and installation reference only.
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
  return {
   server_url: typeof parsed.server_url === "string" ? parsed.server_url.trim() : "",
   client_instance_id: typeof parsed.client_instance_id === "string" ? parsed.client_instance_id : "",
   api_token: typeof parsed.api_token === "string" ? parsed.api_token.trim() : "",
  };
 } catch {
  // Missing file, permission error, or malformed JSON — skip silently.
  return null;
 }
}


function isPlainObject(value) {
 return value !== null && typeof value === "object" && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype;
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
 return `[engram] startup env: ${envParts}; ${cfPart}`;
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
 TASK_CONTEXT_COMPLETION_RESERVE_MS,
 main,
 childEnvForEngram,
 configuredEnvValue,
 appendStartupDiagnosticLog,
 describeConfigFile,
 describeEnvValue,
 emitStartupDiagnostic,
 formatStartupDiagnostic,
 inferCodexPluginDataDir,
 isConfiguredValue,
 readEngramConfigFile,
 resolveConfigFilePath,
 resolvePluginData,
 resolvePluginRoot,
 resolveAndSpawn,
 resolveLogicalProject,
 projectFromInspection,
 validTaskContextResponse,
 requestTaskContext,
 prepareHookClient,
 resolveHostRoot,
 codexMcpConfig,
 spawnFailureMessage,
 trimStartupDiagnosticLog,
};
