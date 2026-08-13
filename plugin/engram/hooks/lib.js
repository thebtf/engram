#!/usr/bin/env node
'use strict';

const crypto = require('crypto');
const fs = require('fs');
const os = require('os');
const path = require('path');

function configuredPluginEnv(...keys) {
 // Claude Code exports plugin userConfig values to plugin subprocesses as
 // CLAUDE_PLUGIN_OPTION_<KEY>; explicit ENGRAM_* env always wins.
 for (const key of keys) {
  const value = process.env[key];
  if (typeof value === 'string' && value.trim() !== '' && !/^\$\{[^}]+\}$/.test(value.trim())) {
   return value.trim();
  }
 }
 return '';
}

/**
 * Resolve the engram config file path, in priority order:
 *   1. $ENGRAM_CONFIG_FILE if set, non-empty, and not a bare placeholder
 *   2. <pluginData>/config.json if that file exists
 *   3. ~/.engram/config.json (home-directory universal fallback)
 * Returns the resolved path string (file may or may not exist).
 *
 * When pluginData is set but <pluginData>/config.json does not exist,
 * we fall through to the home-directory path so users who create only
 * ~/.engram/config.json (the documented Codex setup path) are found.
 */
function resolveConfigFilePath() {
 const explicit = configuredPluginEnv('ENGRAM_CONFIG_FILE');
 if (explicit) {
  return explicit;
 }
 const pluginData = (process.env.ENGRAM_DATA_DIR || process.env.CLAUDE_PLUGIN_DATA || '').trim();
 if (pluginData) {
  const candidate = path.join(pluginData, 'config.json');
  if (fs.existsSync(candidate)) {
   return candidate;
  }
 }
 return path.join(os.homedir(), '.engram', 'config.json');
}

/**
 * Read and parse the engram config file.
 * Returns { server_url, api_token, quiet } on success (server_url/api_token
 * trimmed strings, may be empty; quiet is the raw value — boolean or string —
 * or undefined when absent). Returns null on missing or malformed file —
 * callers must treat null as "not configured here". Never throws.
 */
function readEngramConfigFile(configFilePath) {
 try {
  if (!configFilePath || !fs.existsSync(configFilePath)) {
   return null;
  }
  const raw = fs.readFileSync(configFilePath, 'utf8');
  const parsed = JSON.parse(raw);
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
   return null;
  }
  return {
   server_url: typeof parsed.server_url === 'string' ? parsed.server_url.trim() : '',
   api_token: typeof parsed.api_token === 'string' ? parsed.api_token.trim() : '',
   quiet: parsed.quiet,
  };
 } catch {
  // Missing file, permission error, or malformed JSON — skip silently.
  return null;
 }
}

function safePromptScalar(value) {
 return String(value ?? '')
  .replace(/\s+/g, ' ')
  .trim()
  .replace(/&/g, '&amp;')
  .replace(/</g, '&lt;')
  .replace(/>/g, '&gt;');
}

function quotedPromptScalar(value) {
 return JSON.stringify(safePromptScalar(value));
}

function safePromptPayload(value) {
 return String(value ?? '')
  .replace(/&/g, '&amp;')
  .replace(/</g, '&lt;')
  .replace(/>/g, '&gt;');
}

function quotedPromptPayload(value) {
 return JSON.stringify(safePromptPayload(value));
}

/**
 * Write the engram config file with restrictive permissions.
 * On POSIX: chmod 0600 (owner read/write only).
 * On Windows: the file is created in the user profile directory; NTFS ACLs
 *   inherited from the parent directory (e.g. ~/.engram/) already restrict
 *   access to the owner account — no explicit icacls call is made.
 * Never logs the token value.
 * @param {string} configFilePath - Absolute path to write
 * @param {string} serverURL - Engram server URL
 * @param {string} apiToken - Worker keycard token (never logged)
 * @returns {boolean} true on success, false on failure
 */
function writeEngramConfigFile(configFilePath, serverURL, apiToken) {
 try {
  const dir = path.dirname(configFilePath);
  fs.mkdirSync(dir, { recursive: true });
  const content = JSON.stringify({ server_url: serverURL, api_token: apiToken }, null, 2);
  fs.writeFileSync(configFilePath, content, { encoding: 'utf8', mode: 0o600 });
  // On POSIX, enforce mode explicitly in case umask was permissive.
  if (process.platform !== 'win32') {
   try { fs.chmodSync(configFilePath, 0o600); } catch { /* best-effort */ }
  }
  return true;
 } catch {
  return false;
 }
}

/**
 * Resolve ENGRAM_URL and ENGRAM_TOKEN using the full credential chain:
 *   1. Explicit env vars (ENGRAM_URL / ENGRAM_TOKEN)
 *   2. Claude Code plugin option env (CLAUDE_PLUGIN_OPTION_*)
 *   3. Legacy userConfig aliases (ENGRAM_CLAUDE_USERCONFIG_*)
 *   4. Config file fallback (ENGRAM_CONFIG_FILE / <pluginData>/config.json /
 *      ~/.engram/config.json) — added v6.4.15 for Codex ≥0.139 which stopped
 *      forwarding shell_environment_policy.set values to plugin MCP children
 *      (openai/codex#24401).
 *
 * Sets process.env.ENGRAM_URL and process.env.ENGRAM_TOKEN so child processes
 * and subsequent code see the resolved values.
 *
 * Returns { serverURL, token } — empty strings when unconfigured.
 */
function getEngramConfig() {
 let serverURL = configuredPluginEnv(
  'ENGRAM_URL',
  'ENGRAM_SERVER_URL',
  'CLAUDE_PLUGIN_OPTION_server_url',
  'CLAUDE_PLUGIN_OPTION_SERVER_URL',
  'ENGRAM_CLAUDE_USERCONFIG_URL'
 );

 let token = configuredPluginEnv(
  'ENGRAM_TOKEN',
  'CLAUDE_PLUGIN_OPTION_api_token',
  'CLAUDE_PLUGIN_OPTION_API_TOKEN',
  'ENGRAM_CLAUDE_USERCONFIG_TOKEN'
 );

 // Read config file at most once — only when at least one credential is missing.
 if (!serverURL || !token) {
  const cf = readEngramConfigFile(resolveConfigFilePath());
  if (cf) {
   if (!serverURL && cf.server_url) {
    serverURL = cf.server_url;
   }
   if (!token && cf.api_token) {
    token = cf.api_token;
   }
  }
 }

 if (serverURL) {
  process.env.ENGRAM_URL = serverURL;
 }
 if (token) {
  process.env.ENGRAM_TOKEN = token;
 }

 return { serverURL, token };
}

function getServerURL() {
 // ENGRAM_URL may include a path (e.g. http://server:37777/mcp for MCP transport).
 // Hooks use REST API endpoints at the server root (/api/...), so we extract just the origin.
 const customURL = configuredPluginEnv(
  'ENGRAM_URL',
  'ENGRAM_SERVER_URL',
  'CLAUDE_PLUGIN_OPTION_server_url',
  'CLAUDE_PLUGIN_OPTION_SERVER_URL',
  'ENGRAM_CLAUDE_USERCONFIG_URL'
 );
 if (customURL && customURL.trim() !== '') {
  try {
   const parsed = new URL(customURL.trim());
   return `${parsed.protocol}//${parsed.host}`;
  } catch {
   // If URL parsing fails, use as-is but strip trailing path
   return customURL.trim().replace(/\/[^/]*$/, '');
  }
 }

 const host = process.env.ENGRAM_WORKER_HOST || '127.0.0.1';
 const port = process.env.ENGRAM_WORKER_PORT || '37777';
 return `http://${host}:${port}`;
}

function isInternalHook() {
 return process.env.ENGRAM_INTERNAL === '1';
}

/**
 * Quiet mode — automatic-injection kill-switch (tacit, not mute).
 *
 * When ENGRAM_QUIET (or ENGRAM_QUIET_HOOKS) is truthy, the hooks that PUSH
 * context into the prompt return an empty `{continue:true}` response and skip
 * their handler entirely: no context injection, no behavioral-rule / memory /
 * issue blocks, no per-hook server calls. This is the "zero beats noise" escape
 * hatch — useful while a server-side rule set is stale or mis-scoped, or during
 * focused development where injected context is more distracting than helpful.
 *
 * SCOPE — quiet silences AUTOMATIC INJECTION only, not the whole plugin. Two
 * things keep working so the memory stays a learning loop while the prompt is
 * quiet:
 *   1. The MCP daemon and its tools (store/recall/vault/issues) are untouched —
 *      quiet never gated those; the SessionStart binary bootstrap
 *      (scripts/ensure-binary.js, which downloads/updates the daemon only when
 *      missing or version-stale) runs regardless. It injects no context.
 *   2. The CAPTURE / LEARNING hooks (UserPromptSubmit, Stop, SessionEnd,
 *      SubagentStop) still run their handlers under quiet. They emit no prompt
 *      context (writeResponse only renders injection for a non-empty handler
 *      result, and these return ''), but they DO record correction / segment
 *      signals, crystallize lessons from the transcript, and propagate session
 *      outcomes. So engram keeps learning; it just stops talking.
 * Only the INJECTION_HOOKS set below is gated. To stop ALL MCP activity, disable
 * the plugin instead of using quiet mode.
 *
 * Truthy values: "1", "true", "yes", "on" (case-insensitive). Anything else
 * (including unset/empty) leaves hooks fully active. Honored for both Claude
 * Code and Codex because both consume these same hooks.
 *
 * Claude Code forwards plugin userConfig options to subprocesses as
 * CLAUDE_PLUGIN_OPTION_<KEY> (case follows the manifest), so those aliases are
 * checked too — letting the switch be flipped via the plugin config UI, not
 * only a raw env var. Explicit ENGRAM_* env always wins (checked first).
 *
 * Config-file fallback: Codex ≥0.139 no longer forwards env vars to plugin hook
 * children (openai/codex#24401 — the same reason credentials moved to
 * ~/.engram/config.json), so an env-only switch would silently fail to mute
 * Codex — exactly the client this exists for. So a `"quiet"` key in the engram
 * config file is honored too, sitting alongside server_url/api_token. Accepts
 * boolean true or a truthy string ("1"/"true"/"yes"/"on").
 *
 * Precedence: an explicit env/option ALWAYS wins over the config file, including
 * a FALSEY one. If any quiet env var is present (non-empty), its value decides
 * outright (truthy → mute, falsey → active) and the config file is NOT consulted
 * — this lets a user temporarily re-enable injection with ENGRAM_QUIET=0 without
 * editing ~/.engram/config.json. The config file is read only when no quiet env
 * var is set at all (the Codex ≥0.139 case).
 */
function isTruthyFlag(value) {
 if (value === true) return true;
 return typeof value === 'string' && /^(1|true|yes|on)$/i.test(value.trim());
}

function isQuietMode() {
 const raw = configuredPluginEnv(
  'ENGRAM_QUIET',
  'ENGRAM_QUIET_HOOKS',
  'CLAUDE_PLUGIN_OPTION_ENGRAM_QUIET',
  'CLAUDE_PLUGIN_OPTION_engram_quiet',
  'CLAUDE_PLUGIN_OPTION_QUIET',
  'CLAUDE_PLUGIN_OPTION_quiet'
 );
 // An explicit env/option present (non-empty) decides outright — even falsey,
 // so it overrides a config-file quiet:true. configuredPluginEnv returns '' for
 // both "absent" and "empty/placeholder", which we treat the same: fall through.
 if (raw !== '') {
  return isTruthyFlag(raw);
 }
 // No quiet env var at all → consult the engram config file (Codex ≥0.139 hook
 // children receive no env, so this is their only path).
 const cf = readEngramConfigFile(resolveConfigFilePath());
 return !!cf && isTruthyFlag(cf.quiet);
}

// Hooks that PUSH context into the prompt — the only ones quiet mode gates.
// SessionStart renders <user-behavior-rules>/<engram-static-memories>;
// PreToolUse injects per-file warnings + context observations; PreCompact writes
// .engram/reinjection.md (read by the agent via @-import on the next turn).
// Every other hook (UserPromptSubmit, Stop, SessionEnd, SubagentStop) is
// capture/learning: it records signals and crystallizes lessons but returns no
// prompt context, so it stays active under quiet.
const INJECTION_HOOKS = new Set(['SessionStart', 'PreToolUse', 'PreCompact']);

function isInjectionHook(hookName) {
 return INJECTION_HOOKS.has(hookName);
}

/**
 * getGitRemoteID attempts to compute a stable, cross-platform project ID
 * from the git remote origin URL and the relative path within the repo.
 * Returns an object with projectID, gitRemote, and relativePath on success.
 * Returns null if the directory is not a git repository or has no remote.
 */
function getGitRemoteID(cwd) {
 try {
  const execSync = require('child_process').execSync;
  const opts = {
   cwd,
   stdio: ['ignore', 'pipe', 'pipe'],
   timeout: 3000,
   env: { ...process.env, LC_ALL: 'C', LANG: 'C' },
  };
  const remoteURL = execSync('git remote get-url origin', opts).toString().trim();
  if (!remoteURL) return null;
  const relativePath = execSync('git rev-parse --show-prefix', opts).toString().trim();
  const key = remoteURL + '/' + relativePath;
  const hash = crypto.createHash('sha256').update(key).digest('hex');
  return {
   projectID: hash.slice(0, 8),
   gitRemote: remoteURL,
   relativePath: relativePath,
  };
 } catch (error) {
  if (isMissingGitIdentityError(error)) return null;
  throw new Error('PROJECT_IDENTITY_UNAVAILABLE: git identity resolution failed', { cause: error });
 }
}

function abortError() {
 const error = new Error('The operation was aborted');
 error.name = 'AbortError';
 return error;
}

function throwIfAborted(signal) {
 if (signal?.aborted) throw abortError();
}

function execGitFile(args, cwd, options = {}) {
 throwIfAborted(options.signal);
 const { execFile } = require('node:child_process');
 return new Promise((resolve, reject) => {
  execFile('git', args, {
   cwd,
   timeout: options.timeoutMs ?? 3000,
   windowsHide: true,
   env: { ...process.env, LC_ALL: 'C', LANG: 'C' },
   signal: options.signal,
  }, (error, stdout, stderr) => {
   if (error) {
    if (error.stderr == null) error.stderr = stderr;
    reject(error);
    return;
   }
   resolve(String(stdout).trim());
  });
 });
}

async function getGitRemoteIDAsync(cwd, options = {}) {
 try {
  const remoteURL = await execGitFile(['remote', 'get-url', 'origin'], cwd, options);
  throwIfAborted(options.signal);
  if (!remoteURL) return null;
  const relativePath = await execGitFile(['rev-parse', '--show-prefix'], cwd, options);
  throwIfAborted(options.signal);
  const hash = crypto.createHash('sha256').update(`${remoteURL}/${relativePath}`).digest('hex');
  return { projectID: hash.slice(0, 8), gitRemote: remoteURL, relativePath };
 } catch (error) {
  if (error?.name === 'AbortError' || options.signal?.aborted) throw abortError();
  if (isMissingGitIdentityError(error)) return null;
  throw new Error('PROJECT_IDENTITY_UNAVAILABLE: git identity resolution failed', { cause: error });
 }
}

function isMissingGitIdentityError(error) {
 if (!error || typeof error !== 'object') return false;
 if (error.code === 'ENOENT') return true;
 const stderr = error.stderr == null ? '' : String(error.stderr);
 return /not a git repository|no such remote/i.test(stderr);
}

/**
 * LegacyProjectID always returns the OLD path-based project ID (6-char hash).
 * Used during migration to send both old and new IDs to the server,
 * allowing the server to re-associate existing observations.
 */
function LegacyProjectID(cwd) {
 const resolvedPath = path.resolve(cwd || '');
 const dirName = path.basename(resolvedPath);
 const hash = crypto.createHash('sha256').update(resolvedPath).digest('hex');
 return dirName + '_' + hash.slice(0, 6);
}

/**
 * ProjectIDWithName returns the canonical project ID for the given working directory.
 * Prefers a stable git-remote-based ID (cross-platform, cross-OS-path).
 * Falls back to a path-based ID for non-git directories.
 *
 * Algorithm mirrors internal/proxy/identity.go:ResolveProjectSlug exactly:
 *   - git repo with remote: SHA-256(remoteURL + "/" + relativePath), first 8 hex chars
 *   - non-git fallback: SHA-256(absolutePath), first 6 hex chars
 */
function ProjectIDWithName(cwd) {
 const gitResult = getGitRemoteID(cwd);
 if (gitResult) {
  return gitResult.projectID;
 }
 // Fallback: pure path-based hash for directories without a git remote.
 const resolvedPath = path.resolve(cwd || '');
 const hash = crypto.createHash('sha256').update(resolvedPath).digest('hex');
 return hash.slice(0, 6);
}

const PROJECT_IDENTITY_VERSION_V2 = 2;
const PROJECT_IDENTITY_V2_FILE = '.engram-project-v2.json';
const STRICT_ANCHOR_V2 = /^[0-9a-f]{32}$/;
const PROJECT_IDENTITY_CONTROL = /\p{Cc}/u;
const PROJECT_SELECTOR_V2 = /^[A-Za-z0-9_.\/:\\-]+$/;
const RESERVED_PROJECT_BINDING_V2 = /^p2[gn]_[0-9a-f]{32}$/;
const PROJECT_ANCHOR_V2_KEYS = ['anchor', 'shared', 'version'];

function projectIdentityInvalid(reason) {
 return new Error(`PROJECT_IDENTITY_INVALID: ${reason}`);
}

function validateProjectSelectorSyntaxV2(selector) {
 if (typeof selector !== 'string' || selector === '' || selector.length > 256 ||
  selector.trim() !== selector || selector.includes('..') ||
  PROJECT_IDENTITY_CONTROL.test(selector) || !PROJECT_SELECTOR_V2.test(selector)) {
  throw projectIdentityInvalid('project selector is empty or malformed');
 }
 return selector;
}

function validateProjectSelectorV2(selector) {
 const validated = validateProjectSelectorSyntaxV2(selector);
 if (RESERVED_PROJECT_BINDING_V2.test(validated)) {
  throw projectIdentityInvalid('project selector uses the reserved identity binding namespace');
 }
 return validated;
}

function validateCanonicalProjectV2(selector) {
 return validateProjectSelectorSyntaxV2(selector);
}

function buildProjectIdentityV2(value) {
 if (!value || typeof value !== 'object' || Array.isArray(value)) {
  throw projectIdentityInvalid('identity metadata must be an object');
 }
 for (const field of ['legacy_project_id', 'display_name', 'git_remote', 'relative_path', 'non_git_anchor']) {
  if (value[field] != null && typeof value[field] !== 'string') {
   throw projectIdentityInvalid(`${field} must be a string`);
  }
 }
 if (value.anchor_shared != null && typeof value.anchor_shared !== 'boolean') {
  throw projectIdentityInvalid('anchor_shared must be a JSON boolean or null');
 }
 return {
  version: PROJECT_IDENTITY_VERSION_V2,
  legacy_project_id: value.legacy_project_id || '',
  display_name: value.display_name || '',
  git_remote: value.git_remote || '',
  relative_path: value.relative_path || '',
  non_git_anchor: value.non_git_anchor || '',
  anchor_shared: value.anchor_shared == null ? null : value.anchor_shared,
 };
}

function validateProjectIdentityV2(identity) {
 const invalid = (reason) => {
  throw new Error(`PROJECT_IDENTITY_INVALID: ${reason}`);
 };
 if (!identity || identity.version !== PROJECT_IDENTITY_VERSION_V2) {
  invalid('unsupported version');
 }
 for (const field of ['legacy_project_id', 'display_name', 'git_remote', 'relative_path', 'non_git_anchor']) {
  if (typeof identity[field] !== 'string') invalid(`${field} must be a string`);
 }
 if (identity.anchor_shared !== null && typeof identity.anchor_shared !== 'boolean') {
  invalid('anchor_shared must be a JSON boolean or null');
 }
 if (identity.legacy_project_id.length > 256 || identity.display_name.length > 256 ||
  identity.legacy_project_id.trim() !== identity.legacy_project_id ||
  identity.display_name.trim() !== identity.display_name ||
  PROJECT_IDENTITY_CONTROL.test(identity.legacy_project_id) || PROJECT_IDENTITY_CONTROL.test(identity.display_name) ||
  RESERVED_PROJECT_BINDING_V2.test(identity.legacy_project_id)) {
  invalid('selector or display name is malformed');
 }
 const hasGit = identity.git_remote !== '' || identity.relative_path !== '';
 const hasAnchor = identity.non_git_anchor !== '' || identity.anchor_shared !== null;
 if (hasGit === hasAnchor) invalid('exactly one identity source is required');
 if (hasGit) {
  if (!identity.git_remote || identity.git_remote.length > 2048 || identity.git_remote.trim() !== identity.git_remote || PROJECT_IDENTITY_CONTROL.test(identity.git_remote)) {
   invalid('git_remote is missing or malformed');
  }
  if (!normalizedProjectRelativePathV2(identity.relative_path)) {
   invalid('relative_path is not normalized');
  }
 } else {
  if (!STRICT_ANCHOR_V2.test(identity.non_git_anchor) || typeof identity.anchor_shared !== 'boolean') {
   invalid('non-git anchor must be 128-bit lowercase hex with explicit sharing');
  }
 }
 return identity;
}

function normalizedProjectRelativePathV2(value) {
 if (value === '') return true;
 if (value.length > 4096 || value.trim() !== value || value.startsWith('/') ||
  !value.endsWith('/') || value.includes('\\') || PROJECT_IDENTITY_CONTROL.test(value)) return false;
 return value.slice(0, -1).split('/').every((part) =>
  part !== '' && part !== '.' && part !== '..' && part.trim() === part);
}

function readOrCreateProjectAnchorV2(cwd) {
 const anchorPath = path.join(path.resolve(cwd || ''), PROJECT_IDENTITY_V2_FILE);
 for (; ;) {
  try {
   return decodeProjectAnchorV2(fs.readFileSync(anchorPath, 'utf8'));
  } catch (error) {
   if (error && error.code !== 'ENOENT') throw error;
  }

  const anchor = {
   version: PROJECT_IDENTITY_VERSION_V2,
   anchor: crypto.randomBytes(16).toString('hex'),
   shared: false,
  };
  const payload = `${JSON.stringify(anchor, null, 2)}\n`;
  decodeProjectAnchorV2(payload);
  if (publishProjectAnchorV2(anchorPath, payload)) {
   return anchor;
  }
 }
}

function decodeProjectAnchorV2(data) {
 let parsed;
 try {
  parsed = JSON.parse(data);
 } catch (error) {
  throw projectIdentityInvalid(`decode ${PROJECT_IDENTITY_V2_FILE}: ${error.message}`);
 }
 const keys = parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? Object.keys(parsed).sort() : [];
 if (keys.length !== PROJECT_ANCHOR_V2_KEYS.length || keys.some((key, index) => key !== PROJECT_ANCHOR_V2_KEYS[index]) ||
  parsed.version !== PROJECT_IDENTITY_VERSION_V2 || !STRICT_ANCHOR_V2.test(parsed.anchor) || typeof parsed.shared !== 'boolean') {
  throw projectIdentityInvalid(`malformed ${PROJECT_IDENTITY_V2_FILE}`);
 }
 return parsed;
}

function publishProjectAnchorV2(anchorPath, payload) {
 const tempPath = `${anchorPath}.tmp-${process.pid}-${crypto.randomBytes(16).toString('hex')}`;
 let fd;
 let phase = 'create';
 let primaryError;
 try {
  fd = fs.openSync(tempPath, 'wx', 0o600);
  phase = 'write';
  fs.writeFileSync(fd, payload, 'utf8');
  phase = 'sync';
  fs.fsyncSync(fd);
  phase = 'close';
  fs.closeSync(fd);
  fd = undefined;
  phase = 'publish';
  // Hard-link publication is atomic and refuses to replace an existing name.
  fs.linkSync(tempPath, anchorPath);
 } catch (error) {
  primaryError = error;
 }

 if (fd === undefined && phase === 'create' && primaryError) {
  throw primaryError;
 }
 let closeError;
 if (fd !== undefined) {
  try { fs.closeSync(fd); } catch (error) { closeError = error; }
 }
 let cleanupError;
 try { fs.unlinkSync(tempPath); } catch (error) {
  if (!error || error.code !== 'ENOENT') cleanupError = error;
 }
 if (cleanupError) throw projectAnchorPublicationError(primaryError, closeError, cleanupError);
 if (primaryError) {
  if (phase === 'publish' && primaryError.code === 'EEXIST' && !closeError) return false;
  throw projectAnchorPublicationError(primaryError, closeError);
 }
 if (closeError) throw projectAnchorPublicationError(closeError);
 return true;
}

function projectAnchorPublicationError(...errors) {
 const present = errors.filter(Boolean);
 if (present.length === 1) return present[0];
 return new Error(present.map((error) => error.message || String(error)).join('; '));
}

function resolveProjectIdentityV2(cwd) {
 const resolved = path.resolve(cwd || '');
 const git = getGitRemoteID(resolved);
 const base = {
  legacy_project_id: LegacyProjectID(resolved),
  display_name: path.basename(resolved),
  git_remote: git ? git.gitRemote : '',
  relative_path: git ? git.relativePath.replace(/\\/g, '/') : '',
  non_git_anchor: '',
  anchor_shared: null,
 };
 if (!git) {
  const anchor = readOrCreateProjectAnchorV2(resolved);
  base.non_git_anchor = anchor.anchor;
  base.anchor_shared = anchor.shared;
 }
 return validateProjectIdentityV2(buildProjectIdentityV2(base));
}

async function resolveHookProjectIdentityV2(cwd, options = {}) {
 const resolved = path.resolve(cwd || '');
 const git = await getGitRemoteIDAsync(resolved, options);
 throwIfAborted(options.signal);
 const base = {
  legacy_project_id: LegacyProjectID(resolved),
  display_name: path.basename(resolved),
  git_remote: git ? git.gitRemote : '',
  relative_path: git ? git.relativePath.replace(/\\/g, '/') : '',
  non_git_anchor: '',
  anchor_shared: null,
 };
 if (!git) {
  throwIfAborted(options.signal);
  const anchor = readOrCreateProjectAnchorV2(resolved);
  base.non_git_anchor = anchor.anchor;
  base.anchor_shared = anchor.shared;
 }
 throwIfAborted(options.signal);
 return validateProjectIdentityV2(buildProjectIdentityV2(base));
}

async function registerProjectIdentityV2(context, requestFn = request, requestOptions = {}) {
 if (!context || !context.ProjectIdentityV2) {
  throw new Error('PROJECT_IDENTITY_INVALID: hook context has no v2 identity');
 }
 const selector = validateProjectSelectorV2(context.Project);
 validateProjectIdentityV2(context.ProjectIdentityV2);
 const timeoutMs = Number.isFinite(requestOptions.timeoutMs) && requestOptions.timeoutMs > 0
  ? requestOptions.timeoutMs
  : 10000;
 const response = await requestFn('POST', '/api/context/inject', {
  project: selector,
  legacy_project: context.LegacyProject,
  git_remote: context.GitRemote,
  relative_path: context.RelativePath,
  project_identity: context.ProjectIdentityV2,
  identity_only: true,
 }, timeoutMs, requestOptions);
 if (requestOptions.signal?.aborted) throw abortError();
 let canonical;
 try {
  canonical = validateCanonicalProjectV2(response && response.canonical_project);
 } catch {
  throw new Error('PROJECT_IDENTITY_UNAVAILABLE: project identity registration response is malformed');
 }
 context.Project = canonical;
 return context.Project;
}

function isProjectIdentityTransportOffline(error) {
 if (!error || typeof error !== 'object') return false;
 const cause = error.cause && typeof error.cause === 'object' ? error.cause : null;
 const code = error.code || (cause && cause.code);
 const name = error.name || (cause && cause.name);
 if (cause && cause.message === 'bad port') return true;
 if (name === 'AbortError') return true;
 return ['ECONNREFUSED', 'ENOTFOUND', 'EAI_AGAIN', 'EHOSTUNREACH', 'ENETUNREACH', 'ETIMEDOUT', 'UND_ERR_CONNECT_TIMEOUT'].includes(code);
}

function buildRequestHeaders(includeJsonBody = false) {
 const headers = {};
 const token = configuredPluginEnv(
  'ENGRAM_TOKEN',
  'CLAUDE_PLUGIN_OPTION_api_token',
  'CLAUDE_PLUGIN_OPTION_API_TOKEN',
  'ENGRAM_CLAUDE_USERCONFIG_TOKEN'
 );
 if (token) {
  headers.Authorization = `Bearer ${token}`;
 }

 if (includeJsonBody) {
  headers['Content-Type'] = 'application/json';
 }

 return headers;
}

function resolveRequestURL(endpoint) {
 const base = getServerURL().replace(/\/+$/, '');
 if (!endpoint) {
  return base;
 }
 if (endpoint.startsWith('http://') || endpoint.startsWith('https://')) {
  return endpoint;
 }
 const normalizedEndpoint = endpoint.startsWith('/') ? endpoint : `/${endpoint}`;
 return `${base}${normalizedEndpoint}`;
}

function getPluginDataDir() {
 const fromEngram = process.env.ENGRAM_DATA_DIR;
 if (typeof fromEngram === 'string' && fromEngram.trim() !== '') {
  return fromEngram.trim();
 }
 const fromClaude = process.env.CLAUDE_PLUGIN_DATA;
 if (typeof fromClaude === 'string' && fromClaude.trim() !== '') {
  return fromClaude.trim();
 }
 return '';
}

function getSessionStartCachePath(projectSlug) {
 const baseDir = getPluginDataDir();
 if (!baseDir || !projectSlug) {
  return '';
 }
 const safeProjectSlug = String(projectSlug).replace(/[^a-zA-Z0-9._-]/g, '_');
 return path.join(baseDir, 'cache', `session-start-${safeProjectSlug}.json`);
}

function readJSONFile(filePath) {
 if (!filePath) {
  return null;
 }
 try {
  if (!fs.existsSync(filePath)) {
   return null;
  }
  return JSON.parse(fs.readFileSync(filePath, 'utf8'));
 } catch {
  return null;
 }
}

function writeJSONFile(filePath, value) {
 if (!filePath) {
  return;
 }
 try {
  const parentDir = path.dirname(filePath);
  fs.mkdirSync(parentDir, { recursive: true });
  fs.writeFileSync(filePath, JSON.stringify(value, null, 2), { encoding: 'utf8', mode: 0o600 });
 } catch {
  // Cache persistence is best-effort; never throw from hook helpers.
 }
}

function readAllStdin() {
 return new Promise((resolve) => {
  let data = '';
  process.stdin.setEncoding('utf8');
  process.stdin.on('data', (chunk) => {
   data += chunk;
  });
  process.stdin.on('end', () => {
   resolve(data);
  });
 });
}

// drainStdin consumes and discards the hook's stdin without parsing it.
// The host writes the hook JSON into the child's stdin pipe. A hook can return
// early while the writer is still sending a large payload. Returning before the
// pipe is drained can give the writer EPIPE, surfacing a quiet or internal
// no-op path as a hook FAILURE. Draining first keeps the early exit silent.
// No parsing, no server calls — just empty the pipe. Never rejects.
function drainStdin() {
 return new Promise((resolve) => {
  try {
   process.stdin.on('data', () => { });
   process.stdin.on('end', resolve);
   process.stdin.on('error', resolve);
   process.stdin.resume();
  } catch {
   resolve();
  }
 });
}

// clearReinjectionFile removes <cwd>/.engram/reinjection.md if present.
// pre-compact.js writes that file (the agent reads it via @.engram/reinjection.md
// on the next turn) and is the only path that deletes it when stale. Under quiet
// mode the PreCompact handler is skipped, so a previously-written file would keep
// replaying old hints — defeating the "zero hints" promise. The quiet path clears
// it directly. Best-effort: never throws, no server calls. `rawInput` is the hook
// JSON already read from stdin (so the pipe is drained); cwd is parsed from it.
function clearReinjectionFile(rawInput) {
 try {
  if (!rawInput || !rawInput.trim()) return;
  const parsed = JSON.parse(rawInput);
  const cwd = typeof parsed.cwd === 'string' ? parsed.cwd : '';
  if (!cwd) return;
  const reinjectionFile = path.join(cwd, '.engram', 'reinjection.md');
  if (fs.existsSync(reinjectionFile)) {
   fs.unlinkSync(reinjectionFile);
  }
 } catch {
  // Malformed JSON, missing cwd, permission error — non-fatal.
 }
}

// Claude Code validates hookSpecificOutput as a discriminated union by hookEventName.
// Only PreToolUse, UserPromptSubmit, and SessionStart have defined schemas with
// hookEventName. Other hooks must omit hookEventName and send only
// { additionalContext } to pass validation.
const HOOKS_WITH_EVENT_NAME = new Set([
 'PreToolUse',
 'UserPromptSubmit',
 'SessionStart',
]);

function writeResponse(hookName, additionalContext) {
 try {
  const response = { continue: true };
  if (typeof additionalContext === 'string' && additionalContext !== '') {
   if (HOOKS_WITH_EVENT_NAME.has(hookName)) {
    response.hookSpecificOutput = {
     hookEventName: hookName,
     additionalContext,
    };
   }
   // Non-union hooks (PostCompact, PreCompact, Stop, etc.):
   // hookSpecificOutput is NOT valid — CC rejects any object that
   // doesn't match the discriminated union.  Context must be
   // delivered through an alternative channel (e.g. session signals
   // consumed by UserPromptSubmit on the next turn).
  }

  process.stdout.write(`${JSON.stringify(response)}\n`);
 } catch (error) {
  // Never throw during response output.
 }
}

async function requestGet(endpoint, timeoutMs = 10000, options = {}) {
 return request('GET', endpoint, undefined, timeoutMs, options);
}

async function requestPost(endpoint, body, timeoutMs = 10000, options = {}) {
 return request('POST', endpoint, body, timeoutMs, options);
}

async function request(method, endpoint, body, timeoutMs = 10000, options = {}) {
 const url = resolveRequestURL(endpoint);
 const controller = new AbortController();
 const externalSignal = options && options.signal;
 const abort = () => controller.abort();
 const timer = setTimeout(abort, timeoutMs);

 try {
  if (externalSignal && externalSignal.aborted) {
   controller.abort();
   const error = new Error('The operation was aborted');
   error.name = 'AbortError';
   throw error;
  }
  if (externalSignal) externalSignal.addEventListener('abort', abort, { once: true });

  const headers = buildRequestHeaders(body !== undefined);
  const response = await fetch(url, {
   method,
   headers,
   body: body === undefined ? undefined : JSON.stringify(body),
   signal: controller.signal,
  });

  const text = await response.text();
  if (!response.ok) {
   throw new Error(`HTTP ${response.status} ${response.statusText}: ${text}`);
  }

  if (!text) {
   return {};
  }

  return JSON.parse(text);
 } finally {
  clearTimeout(timer);
  if (externalSignal) externalSignal.removeEventListener('abort', abort);
 }
}

async function RunHook(hookName, handler) {
 if (isInternalHook()) {
  // Drain stdin before the early exit so a large hook payload mid-write does
  // not give the host EPIPE (see drainStdin).
  await drainStdin();
  writeResponse(hookName);
  return;
 }

 // Quiet mode: silence AUTOMATIC INJECTION only. An injection hook (SessionStart
 // / PreToolUse / PreCompact) emits an empty pass-through response and skips its
 // handler — no context injection, no server calls. Capture/learning hooks fall
 // through and run normally, so engram keeps recording signals and crystallizing
 // lessons while the prompt stays quiet (see isQuietMode() + INJECTION_HOOKS).
 // readAllStdin() fully drains the pipe (so a large payload mid-write does not
 // give the host EPIPE) AND yields the payload, from which we clear any stale
 // .engram/reinjection.md — the one hint channel the agent reads directly
 // (@-import), out of band from hooks, so skipping PreCompact alone would leave
 // it replaying. Clearing it keeps the "zero hints" promise. Best-effort.
 if (isInjectionHook(hookName) && isQuietMode()) {
  const rawInput = await readAllStdin();
  clearReinjectionFile(rawInput);
  writeResponse(hookName);
  return;
 }

 // Hydrate ENGRAM_URL / ENGRAM_TOKEN from the config file for every hook
 // process. Each hook runs in its own Node process so env changes from
 // session-start.js do not carry over. This ensures config-file-only setups
 // (e.g. ~/.engram/config.json for Codex ≥0.139) work in all hook handlers.
 const runtimeEnv = getEngramConfig();

 let rawInput = '';
 let input = {};

 try {
  rawInput = await readAllStdin();
  if (rawInput && rawInput.trim()) {
   input = JSON.parse(rawInput);
  }
 } catch (error) {
  console.error(`[engram] Failed to parse hook input JSON: ${error.message}`);
 }

 const cwd = typeof input.cwd === 'string' ? input.cwd : '';

 try {
  const gitResult = getGitRemoteID(cwd);
  const projectSelector = ProjectIDWithName(cwd);
  const context = {
   SessionID: typeof input.session_id === 'string' ? input.session_id : '',
   CWD: cwd,
   PermissionMode: typeof input.permission_mode === 'string' ? input.permission_mode : '',
   HookEventName: typeof input.hook_event_name === 'string' ? input.hook_event_name : hookName,
   Project: projectSelector,
   ProjectSelector: projectSelector,
   LegacyProject: LegacyProjectID(cwd),
   GitRemote: gitResult ? gitResult.gitRemote : '',
   RelativePath: gitResult ? gitResult.relativePath : '',
   ProjectIdentityV2: resolveProjectIdentityV2(cwd),
   RawInput: rawInput,
  };
  if (hookName !== 'SessionStart' || (runtimeEnv.serverURL && runtimeEnv.token)) {
   try {
    await registerProjectIdentityV2(context);
   } catch (error) {
    if (!isProjectIdentityTransportOffline(error)) {
     throw error;
    }
    // Capture/learning hooks may have local cleanup to perform even while the
    // server is offline. Injection hooks still fail closed, except SessionStart
    // which owns an explicit stale-cache fallback.
    if (hookName !== 'SessionStart' && isInjectionHook(hookName)) {
     throw error;
    }
    context.ProjectIdentityRegistrationOffline = true;
    const fallback = hookName === 'SessionStart'
     ? 'using cache fallback'
     : 'continuing local capture/cleanup';
    console.error(`[engram] ${hookName} project registration offline; ${fallback}: ${error.message}`);
   }
  }
  const additionalContext =
   typeof handler === 'function' ? await handler(context, input) : '';
  writeResponse(hookName, additionalContext);
 } catch (error) {
  console.error(`[engram] ${hookName} hook failed: ${error.message}`);
  writeResponse(hookName);
 }
}

async function RunStatuslineHook(handler, offlineRenderer) {
 try {
  const rawInput = await readAllStdin();
  let input = null;

  if (rawInput && rawInput.trim()) {
   try {
    input = JSON.parse(rawInput);
   } catch (error) {
    console.error(`[engram] Failed to parse statusline input JSON: ${error.message}`);
   }
  }

  const output = await handler(input);
  console.log(typeof output === 'undefined' ? '' : output);
 } catch (error) {
  console.error(`[engram] statusline hook failed: ${error.message}`);
  const offline =
   typeof offlineRenderer === 'function'
    ? offlineRenderer()
    : '[engram] offline';
  console.log(offline);
 }
}

// ──────────────────────────────────────────────────────────────
// Session signal store — persists per-session counters to a temp file so
// hooks can share state across separate process invocations.
// ──────────────────────────────────────────────────────────────

function _signalPath(sessionID) {
 const safe = String(sessionID).replace(/[^a-zA-Z0-9_-]/g, '_');
 const tmpDir = require('os').tmpdir();
 return path.join(tmpDir, `engram-signals-${safe}.json`);
}

/**
 * Increment one or more signal counters for the given session.
 * @param {string} sessionID - Claude session ID
 * @param {Object} increments - e.g. { commits: 1 }
 */
function incrementSessionSignals(sessionID, increments) {
 if (!sessionID || !increments) return;
 try {
  const p = _signalPath(sessionID);
  let current = {};
  try {
   current = JSON.parse(fs.readFileSync(p, 'utf8'));
  } catch {
   // File doesn't exist yet — start fresh
  }

  const next = { ...current };
  for (const [key, delta] of Object.entries(increments)) {
   next[key] = (next[key] || 0) + (Number(delta) || 0);
  }
  fs.writeFileSync(p, JSON.stringify(next), 'utf8');
 } catch {
  // Signal tracking is best-effort; never throw
 }
}

/**
 * Track up to 10 recently touched files for the given session, with dedupe.
 * Keeps insertion order and evicts the oldest file when exceeding the limit.
 * Stores data under the `files` key, alongside numeric counters.
 * @param {string} sessionID - Claude session ID
 * @param {string} filePath - Absolute or relative file path
 */
function appendSessionFile(sessionID, filePath) {
 if (!sessionID || !filePath) return;
 try {
  const p = _signalPath(sessionID);
  let current = {};
  try {
   current = JSON.parse(fs.readFileSync(p, 'utf8'));
  } catch {
   // File doesn't exist yet — start fresh
  }

  const file = String(filePath);
  const priorFiles = Array.isArray(current.files)
   ? current.files.filter((entry) => typeof entry === 'string')
   : [];
  const nextFiles = priorFiles.filter((entry) => entry !== file);
  nextFiles.push(file);

  if (nextFiles.length > 10) {
   nextFiles.splice(0, nextFiles.length - 10);
  }

  const next = { ...current, files: nextFiles };
  fs.writeFileSync(p, JSON.stringify(next), 'utf8');
 } catch {
  // Signal tracking is best-effort; never throw
 }
}

// --- Crash-safe session markers (gstack-insights FR-8) ---

const MARKER_PREFIX = '.engram-pending-';

/**
 * Create a pending session marker in the OS temp directory.
 * @param {string} sessionId
 */
function createPendingMarker(sessionId) {
 if (!sessionId) return;
 try {
  const markerPath = path.join(os.tmpdir(), MARKER_PREFIX + sessionId);
  fs.writeFileSync(markerPath, String(Date.now()), { mode: 0o600 });
 } catch {
  // Non-blocking — marker failure is not critical
 }
}

/**
 * Find stale pending markers (older than maxAgeMs).
 * @param {number} maxAgeMs - Maximum age in milliseconds (default: 2 hours)
 * @returns {{sessionId: string, timestamp: number}[]}
 */
function getStaleMarkers(maxAgeMs = 2 * 60 * 60 * 1000) {
 const stale = [];
 try {
  const tmpDir = os.tmpdir();
  const files = fs.readdirSync(tmpDir);
  const now = Date.now();
  for (const f of files) {
   if (!f.startsWith(MARKER_PREFIX)) continue;
   const sessionId = f.slice(MARKER_PREFIX.length);
   try {
    const content = fs.readFileSync(path.join(tmpDir, f), 'utf8');
    const timestamp = parseInt(content, 10);
    if (!isNaN(timestamp) && (now - timestamp) > maxAgeMs) {
     stale.push({ sessionId, timestamp });
     // Clean up the stale marker
     fs.unlinkSync(path.join(tmpDir, f));
    }
   } catch {
    // Skip unreadable markers
   }
  }
 } catch {
  // tmpdir read failure — non-critical
 }
 return stale;
}

// --- Issue injection formatting (agent-issues FR-5) ---

const PRIORITY_ORDER = { critical: 1, high: 2, medium: 3, low: 4 };

function escapeInjectedScalar(value) {
 return String(value ?? '')
  .replace(/\s+/g, ' ')
  .trim()
  .replace(/&/g, '&amp;')
  .replace(/</g, '&lt;')
  .replace(/>/g, '&gt;')
  .replace(/"/g, '&quot;')
  .replace(/'/g, '&#39;');
}

/**
 * Format issues into an <open-issues> XML block for context injection.
 * @param {Array} issues - Array of issue objects from /api/issues
 * @param {string} project - Current project slug
 * @returns {string} Formatted XML block, or empty string if no issues
 */
function formatIssuesBlock(issues, project) {
 if (!issues || !Array.isArray(issues) || issues.length === 0) return '';

 // Sort: priority (critical first), then newest first
 const sorted = [...issues].sort((a, b) => {
  const pa = PRIORITY_ORDER[a.priority] || 4;
  const pb = PRIORITY_ORDER[b.priority] || 4;
  if (pa !== pb) return pa - pb;
  return new Date(b.created_at) - new Date(a.created_at);
 });

 const staleDays = parseInt(process.env.ENGRAM_ISSUE_STALE_DAYS || '3', 10);
 const nowMs = Date.now();
 const projectText = escapeInjectedScalar(project);

 let block = `<open-issues count="${sorted.length}" project="${projectText}" action-required="true">\n`;
 block += `ACTION REQUIRED: ${sorted.length} active issue(s) assigned to this project (statuses: open, acknowledged, reopened).\n`;
 block += `Before starting new work, you MUST triage these. Run /engram:issue for the full workflow, or at minimum:\n`;
 block += `  1. Read each with issues(action="get", id=N, project="${projectText}")\n`;
 block += `  2. Treat them as YOUR project's inbox and direct work orders — study, investigate, test, implement, comment, resolve, or reject with evidence\n`;
 block += `  3. acknowledged means delivered and accepted into YOUR active backlog, not done\n`;
 block += `  4. Do NOT close — only the source agent closes after verifying your fix\n`;
 block += `Ignoring this block means real work from another agent is blocked on you.\n\n`;

 for (const issue of sorted) {
  const prio = escapeInjectedScalar(issue.priority || 'medium').toUpperCase();
  const from = escapeInjectedScalar(issue.source_project || 'unknown');
  const prefix = issue.status === 'reopened' ? `reopened by: ${from}` : `from: ${from}`;

  // Staleness calculation
  let staleTag = '';
  let actionDirective = '';
  if (issue.acknowledged_at) {
   const ackMs = new Date(issue.acknowledged_at).getTime();
   const daysSinceAck = Math.floor((nowMs - ackMs) / 86400000);
   if (daysSinceAck >= staleDays * 2) {
    staleTag = ` [OVERDUE ${daysSinceAck}d]`;
    actionDirective = `  └─ ACTION: OVERDUE — this issue requires immediate attention. Resolve or explain blocker.\n`;
   } else if (daysSinceAck >= staleDays) {
    staleTag = ` [STALE ${daysSinceAck}d]`;
    actionDirective = `  └─ ACTION: This issue has been open for ${daysSinceAck} days. Resolve or comment with progress.\n`;
   }
  }

  const type = escapeInjectedScalar(((issue.type || '').trim().toUpperCase()) || 'TASK');
  const title = escapeInjectedScalar(issue.title || '');
  const issueID = escapeInjectedScalar(issue.id ?? '');
  block += `#${issueID} [${type}] [${prio}] [${prefix}]${staleTag} title="${title}"\n`;

  if (actionDirective) {
   block += actionDirective;
  } else if (issue.comment_count > 0 && issue.updated_at) {
   const ago = _timeAgo(new Date(issue.updated_at));
   const commentCount = escapeInjectedScalar(issue.comment_count);
   block += `  └─ ${commentCount} comment(s), updated ${escapeInjectedScalar(ago)}\n`;
  }
 }
 block += '</open-issues>';
 return block;
}

/**
 * Simple time-ago formatter.
 * @param {Date} date
 * @returns {string}
 */
function _timeAgo(date) {
 const seconds = Math.floor((Date.now() - date.getTime()) / 1000);
 if (seconds < 60) return 'just now';
 const minutes = Math.floor(seconds / 60);
 if (minutes < 60) return `${minutes}m ago`;
 const hours = Math.floor(minutes / 60);
 if (hours < 24) return `${hours}h ago`;
 const days = Math.floor(hours / 24);
 return `${days}d ago`;
}

module.exports = {
 configuredPluginEnv,
 getEngramConfig,
 getServerURL,
 getPluginDataDir,
 getSessionStartCachePath,
 readEngramConfigFile,
 readJSONFile,
 resolveConfigFilePath,
 safePromptScalar,
 quotedPromptScalar,
 safePromptPayload,
 quotedPromptPayload,
 writeEngramConfigFile,
 writeJSONFile,
 ProjectIDWithName,
 LegacyProjectID,
 PROJECT_IDENTITY_VERSION_V2,
 buildProjectIdentityV2,
 validateProjectIdentityV2,
 validateProjectSelectorV2,
 resolveProjectIdentityV2,
 registerProjectIdentityV2,
 getGitRemoteIDAsync,
 resolveHookProjectIdentityV2,
 isProjectIdentityTransportOffline,
 requestGet,
 requestPost,
 RunHook,
 RunStatuslineHook,
 isInjectionHook,
 isQuietMode,
 writeResponse,
 incrementSessionSignals,
 appendSessionFile,
 createPendingMarker,
 getStaleMarkers,
 formatIssuesBlock,
};
