#!/usr/bin/env node
'use strict';

const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

function getServerURL() {
  // ENGRAM_URL may include a path (e.g. http://server:37777/mcp for MCP transport).
  // Hooks use REST API endpoints at the server root (/api/...), so we extract just the origin.
  const customURL = process.env.ENGRAM_URL;
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
    const dirName = path.basename(path.resolve(cwd || ''));
    return {
      projectID: dirName + '_' + hash.slice(0, 8),
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
 */
function ProjectIDWithName(cwd) {
  const gitResult = getGitRemoteID(cwd);
  if (gitResult) {
    return gitResult.projectID;
  }
  // Fallback: path-based ID for directories without a git remote.
  return LegacyProjectID(cwd);
}

function buildRequestHeaders(includeJsonBody = false) {
  const headers = {};
  const token = process.env.ENGRAM_API_TOKEN;
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

/**
 * WorkstationID returns a deterministic 8-char hex ID from hostname + machine_id.
 * Matches the server-side sessions.WorkstationID() logic:
 *   - On Linux: reads /etc/machine-id; falls back to hostname if unavailable.
 *   - On other platforms: uses hostname as both components (machine_id = hostname).
 */
function WorkstationID() {
  const os = require('os');
  const fs = require('fs');
  const hostname = os.hostname();

  let machineID = '';
  if (os.platform() === 'linux') {
    try {
      machineID = fs.readFileSync('/etc/machine-id', 'utf8').trim();
    } catch {
      // /etc/machine-id not available; fall back to hostname.
    }
  }
  if (!machineID) {
    machineID = hostname;
  }

  const input = hostname + machineID;
  const hash = crypto.createHash('sha256').update(input).digest('hex');
  return hash.slice(0, 8);
}

/**
 * requestUpload sends raw content (text/ndjson) to the server.
 * Optionally gzip-compresses bodies larger than 500 KB.
 */
async function requestUpload(endpoint, content, timeoutMs = 15000) {
  const zlib = require('zlib');
  const url = resolveRequestURL(endpoint);
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);

  try {
    const headers = buildRequestHeaders(false);
    headers['Content-Type'] = 'application/x-ndjson';

    let body = content;
    if (typeof content === 'string' && content.length > 500 * 1024) {
      body = zlib.gzipSync(Buffer.from(content, 'utf8'));
      headers['Content-Encoding'] = 'gzip';
    }

    const response = await fetch(url, {
      method: 'POST',
      headers,
      body,
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
    for (const [key, delta] of Object.entries(increments)) {
      current[key] = (current[key] || 0) + (Number(delta) || 0);
    }
    fs.writeFileSync(p, JSON.stringify(current), 'utf8');
  } catch {
    // Signal tracking is best-effort; never throw
  }
}

/**
 * Read accumulated signal counters for the given session.
 * Returns an empty object when no signals have been recorded.
 * @param {string} sessionID - Claude session ID
 * @returns {Object}
 */
function getSessionSignals(sessionID) {
  if (!sessionID) return {};
  try {
    const p = _signalPath(sessionID);
    return JSON.parse(fs.readFileSync(p, 'utf8'));
  } catch {
    return {};
  }
}

/**
 * Delete the signal file for the given session (call after stop).
 * @param {string} sessionID - Claude session ID
 */
function clearSessionSignals(sessionID) {
  if (!sessionID) return;
  try {
    fs.unlinkSync(_signalPath(sessionID));
  } catch {
    // File may not exist — ignore
  }
}

// --- Diff-scope auto-tagging (gstack-insights FR-7) ---

const SCOPE_PATTERNS = [
  { pattern: /\.(tsx|jsx|vue|svelte|css|scss|less)$/i, scope: 'scope:frontend' },
  { pattern: /^(internal|cmd|pkg)\//i, scope: 'scope:backend' },
  { pattern: /(prompt|generation)/i, scope: 'scope:prompts' },
  { pattern: /(_test\.go|\.test\.[jt]sx?|_test\.py)$/i, scope: 'scope:tests' },
  { pattern: /(\.md$|^docs\/)/i, scope: 'scope:docs' },
  { pattern: /\.(yaml|yml|toml)$|\.json$/i, scope: 'scope:config' },
  { pattern: /(migration|migrate)/i, scope: 'scope:migrations' },
  { pattern: /(api|handler|route)/i, scope: 'scope:api' },
  { pattern: /(auth|session|jwt|oauth)/i, scope: 'scope:auth' },
];

/**
 * Analyze file paths and return matching scope tags.
 * @param {string[]} filePaths - Array of file paths
 * @returns {string[]} Unique scope tags
 */
function diffScope(filePaths) {
  if (!filePaths || !Array.isArray(filePaths)) return [];
  const scopes = new Set();
  for (const fp of filePaths) {
    if (!fp) continue;
    for (const { pattern, scope } of SCOPE_PATTERNS) {
      if (pattern.test(fp)) scopes.add(scope);
    }
  }
  return [...scopes];
}

// --- Crash-safe session markers (gstack-insights FR-8) ---

const os = require('os');
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
 * Delete the pending session marker.
 * @param {string} sessionId
 */
function deletePendingMarker(sessionId) {
  if (!sessionId) return;
  try {
    fs.unlinkSync(path.join(os.tmpdir(), MARKER_PREFIX + sessionId));
  } catch {
    // File may not exist
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

module.exports = {
  getServerURL,
  ProjectIDWithName,
  LegacyProjectID,
  WorkstationID,
  requestGet,
  requestPost,
  requestUpload,
  RunHook,
  RunStatuslineHook,
  writeResponse,
  incrementSessionSignals,
  getSessionSignals,
  clearSessionSignals,
  diffScope,
  createPendingMarker,
  deletePendingMarker,
  getStaleMarkers,
};
