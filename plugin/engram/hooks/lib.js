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

/**
 * Read accumulated signal counters and file history for the given session.
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
 * Read file history for the given session.
 * Returns an empty array when no files have been recorded.
 * @param {string} sessionID - Claude session ID
 * @returns {string[]}
 */
function getSessionFiles(sessionID) {
  if (!sessionID) return [];
  try {
    const current = getSessionSignals(sessionID);
    const files = Array.isArray(current.files) ? current.files : [];
    return files.filter((entry) => typeof entry === 'string');
  } catch {
    return [];
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
 * Format resolved issues into a <resolved-issues> block for source agent notification.
 * @param {Array} issues - Array of resolved issue objects created by this project
 * @param {string} project - Source project slug (the creator)
 * @returns {string} Formatted XML block, or empty string if no issues
 */
function formatResolvedIssuesBlock(issues, project) {
  if (!issues || !Array.isArray(issues) || issues.length === 0) return '';

  let block = `<resolved-issues from-you count="${issues.length}" project="${project}" action-required="true">\n`;
  block += `ACTION REQUIRED: ${issues.length} issue(s) you filed were RESOLVED by target agents. You must verify.\n`;
  block += `Run /engram:issue for the full workflow, or at minimum for each issue:\n`;
  block += `  1. issues(action="get", id=N) — read the resolution comment and understand what the other project claims to have fixed or added\n`;
  block += `  2. Treat this as YOUR follow-up inbox for cross-project dialogue: inspect status, read comments/reports, test, verify, and judge result quality\n`;
  block += `  3. If it works and you're satisfied: issues(action="close", id=N, project="${project}")\n`;
  block += `  4. If it is incomplete, wrong, misunderstood, or unsatisfactory: issues(action="reopen", id=N, project="${project}", body="<concrete evidence>")\n`;
  block += `  5. If more discussion is needed before reopen/close: comment with precise feedback and what still needs verification\n`;
  block += `Leaving these unverified means false-positive 'fixed' claims stay in the system.\n\n`;

  for (const issue of issues) {
    const target = issue.target_project || 'unknown';
    const resolvedAgo = issue.resolved_at ? _timeAgo(new Date(issue.resolved_at)) : 'recently';
    block += `#${issue.id} [RESOLVED by ${target}] ${issue.title} (${resolvedAgo})\n`;

    // Show latest comment as resolution summary
    if (issue.comment_count > 0 && issue.updated_at) {
      block += `  └─ ${issue.comment_count} comment(s), last updated ${_timeAgo(new Date(issue.updated_at))}\n`;
    }
  }
  block += '</resolved-issues>';
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
  appendSessionFile,
  getSessionSignals,
  getSessionFiles,
  clearSessionSignals,
  diffScope,
  createPendingMarker,
  deletePendingMarker,
  getStaleMarkers,
  formatIssuesBlock,
  formatResolvedIssuesBlock,
};
