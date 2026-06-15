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
 * Quiet mode — global injection kill-switch.
 *
 * When ENGRAM_QUIET (or ENGRAM_QUIET_HOOKS) is truthy, every hook routed
 * through RunHook returns an empty `{continue:true}` response and skips its
 * handler entirely: no context injection, no behavioral-rule / memory / issue
 * blocks, and no per-hook server calls. This is the "zero beats noise" escape
 * hatch — useful while a server-side rule set is stale or mis-scoped, or during
 * focused development where injected context is more distracting than helpful.
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
  if (isTruthyFlag(raw)) {
    return true;
  }
  // Fallback to the engram config file (Codex ≥0.139 hook children see no env).
  const cf = readEngramConfigFile(resolveConfigFilePath());
  return !!cf && isTruthyFlag(cf.quiet);
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
    const opts = { cwd, stdio: ['ignore', 'pipe', 'ignore'], timeout: 3000 };
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
  } catch {
    return null;
  }
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

// Claude Code validates hookSpecificOutput as a discriminated union by hookEventName.
// Only PreToolUse, UserPromptSubmit, PostToolUse have defined schemas with hookEventName.
// Other hooks (PostCompact, SessionStart, etc.) must omit hookEventName entirely
// and send only { additionalContext } to pass validation.
const HOOKS_WITH_EVENT_NAME = new Set([
  'PreToolUse',
  'UserPromptSubmit',
  'PostToolUse',
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

async function requestGet(endpoint, timeoutMs = 10000) {
  return request('GET', endpoint, undefined, timeoutMs);
}

async function requestPost(endpoint, body, timeoutMs = 10000) {
  return request('POST', endpoint, body, timeoutMs);
}

async function request(method, endpoint, body, timeoutMs = 10000) {
  const url = resolveRequestURL(endpoint);
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);

  try {
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
  }
}

async function RunHook(hookName, handler) {
  if (isInternalHook()) {
    writeResponse(hookName);
    return;
  }

  // Quiet mode: emit an empty pass-through response and skip the handler.
  // No context injection, no server calls. See isQuietMode() for rationale.
  if (isQuietMode()) {
    writeResponse(hookName);
    return;
  }

  // Hydrate ENGRAM_URL / ENGRAM_TOKEN from the config file for every hook
  // process. Each hook runs in its own Node process so env changes from
  // session-start.js do not carry over. This ensures config-file-only setups
  // (e.g. ~/.engram/config.json for Codex ≥0.139) work in all hook handlers.
  getEngramConfig();

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
  const gitResult = getGitRemoteID(cwd);

  const context = {
    SessionID: typeof input.session_id === 'string' ? input.session_id : '',
    CWD: cwd,
    PermissionMode: typeof input.permission_mode === 'string' ? input.permission_mode : '',
    HookEventName: typeof input.hook_event_name === 'string' ? input.hook_event_name : hookName,
    Project: ProjectIDWithName(cwd),
    LegacyProject: LegacyProjectID(cwd),
    GitRemote: gitResult ? gitResult.gitRemote : '',
    RelativePath: gitResult ? gitResult.relativePath : '',
    RawInput: rawInput,
  };

  try {
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
// Session signal store — persists per-session counters to a temp
// file so post-tool-use.js and stop.js can share state across
// separate process invocations (hooks run as independent procs).
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

  let block = `<open-issues count="${sorted.length}" project="${project}" action-required="true">\n`;
  block += `ACTION REQUIRED: ${sorted.length} active issue(s) assigned to this project (statuses: open, acknowledged, reopened).\n`;
  block += `Before starting new work, you MUST triage these. Run /engram:issue for the full workflow, or at minimum:\n`;
  block += `  1. Read each with issues(action="get", id=N, project="${project}")\n`;
  block += `  2. Treat them as YOUR project's inbox and direct work orders — study, investigate, test, implement, comment, resolve, or reject with evidence\n`;
  block += `  3. acknowledged means delivered and accepted into YOUR active backlog, not done\n`;
  block += `  4. Do NOT close — only the source agent closes after verifying your fix\n`;
  block += `Ignoring this block means real work from another agent is blocked on you.\n\n`;

  for (const issue of sorted) {
    const prio = (issue.priority || 'medium').toUpperCase();
    const from = issue.source_project || 'unknown';
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

    const type = ((issue.type || '').trim().toUpperCase()) || 'TASK';
    block += `#${issue.id} [${type}] [${prio}] [${prefix}]${staleTag} ${issue.title}\n`;

    if (actionDirective) {
      block += actionDirective;
    } else if (issue.comment_count > 0 && issue.updated_at) {
      const ago = _timeAgo(new Date(issue.updated_at));
      block += `  └─ ${issue.comment_count} comment(s), updated ${ago}\n`;
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
  writeEngramConfigFile,
  writeJSONFile,
  ProjectIDWithName,
  LegacyProjectID,
  requestGet,
  requestPost,
  RunHook,
  RunStatuslineHook,
  writeResponse,
  incrementSessionSignals,
  appendSessionFile,
  createPendingMarker,
  getStaleMarkers,
  formatIssuesBlock,
};
