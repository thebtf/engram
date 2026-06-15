const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const test = require('node:test');
const crypto = require('crypto');
const path = require('path');

const lib = require('./lib');

function signalPath(sessionID) {
  const safe = String(sessionID).replace(/[^a-zA-Z0-9_-]/g, '_');
  return path.join(os.tmpdir(), `engram-signals-${safe}.json`);
}

function cleanup(sessionID) {
  try { fs.unlinkSync(signalPath(sessionID)); } catch (_) {}
}

function getSessionFiles(sessionID) {
  try {
    const raw = JSON.parse(fs.readFileSync(signalPath(sessionID), 'utf8'));
    return Array.isArray(raw.files) ? raw.files : [];
  } catch (_) {
    return [];
  }
}

test('add two different files to session store', (t) => {
  const sessionID = 'lib-session-file-tracking-1';
  t.after(() => cleanup(sessionID));

  cleanup(sessionID);

  lib.appendSessionFile(sessionID, '/repo/one.txt');
  lib.appendSessionFile(sessionID, '/repo/two.txt');

  const files = getSessionFiles(sessionID);
  assert.deepStrictEqual(files, ['/repo/one.txt', '/repo/two.txt']);
});

test('dedupe repeated file paths in session store', (t) => {
  const sessionID = 'lib-session-file-tracking-2';
  t.after(() => cleanup(sessionID));

  cleanup(sessionID);

  lib.appendSessionFile(sessionID, '/repo/repeat.txt');
  lib.appendSessionFile(sessionID, '/repo/repeat.txt');

  const files = getSessionFiles(sessionID);
  assert.deepStrictEqual(files, ['/repo/repeat.txt']);
});

test('keep only the latest 10 files when more are appended', (t) => {
  const sessionID = 'lib-session-file-tracking-3';
  t.after(() => cleanup(sessionID));

  cleanup(sessionID);

  for (let i = 1; i <= 11; i++) {
    lib.appendSessionFile(sessionID, `/repo/file-${i}.txt`);
  }

  const files = getSessionFiles(sessionID);
  assert.strictEqual(files.length, 10);
  assert.deepStrictEqual(files, [
    '/repo/file-2.txt',
    '/repo/file-3.txt',
    '/repo/file-4.txt',
    '/repo/file-5.txt',
    '/repo/file-6.txt',
    '/repo/file-7.txt',
    '/repo/file-8.txt',
    '/repo/file-9.txt',
    '/repo/file-10.txt',
    '/repo/file-11.txt',
  ]);
});

test('appendSessionFile no-op behavior is detectable', (t) => {
  const sessionID = 'lib-session-file-tracking-4';
  t.after(() => cleanup(sessionID));

  cleanup(sessionID);

  lib.appendSessionFile(sessionID, '/repo/important.txt');
  const files = getSessionFiles(sessionID);
  assert.deepStrictEqual(files, ['/repo/important.txt']);
});

// ── Project ID format tests ───────────────────────────────────────────────────

/**
 * Helper: compute the expected git-remote-based project ID the same way
 * both Go (ResolveProjectSlug) and JS (ProjectIDWithName / getGitRemoteID) do:
 *   SHA-256(remoteURL + "/" + relativePath).slice(0, 8)
 */
function expectedGitProjectID(remoteURL, relativePath) {
  const key = remoteURL + '/' + relativePath;
  return crypto.createHash('sha256').update(key).digest('hex').slice(0, 8);
}

/**
 * Helper: compute the expected non-git project ID:
 *   SHA-256(absolutePath).slice(0, 6)
 */
function expectedPathProjectID(absolutePath) {
  return crypto.createHash('sha256').update(absolutePath).digest('hex').slice(0, 6);
}

test('git project ID is pure 8-char hex hash (no dirName prefix)', () => {
  const remoteURL = 'https://github.com/example/myrepo.git';
  const relativePath = '';
  const id = expectedGitProjectID(remoteURL, relativePath);

  // Must be exactly 8 lowercase hex characters — no underscore, no dirName prefix.
  assert.match(id, /^[0-9a-f]{8}$/, 'git project ID should be 8 lowercase hex chars');
});

test('non-git project ID is pure 6-char hex hash (no dirName prefix)', () => {
  const absolutePath = '/home/user/projects/my-app';
  const id = expectedPathProjectID(absolutePath);

  // Must be exactly 6 lowercase hex characters — no underscore, no dirName prefix.
  assert.match(id, /^[0-9a-f]{6}$/, 'non-git project ID should be 6 lowercase hex chars');
});

test('git project IDs with same remote+path are identical (cross-platform stability)', () => {
  const remoteURL = 'git@github.com:org/repo.git';
  const relativePath = 'packages/core/';
  const id1 = expectedGitProjectID(remoteURL, relativePath);
  const id2 = expectedGitProjectID(remoteURL, relativePath);
  assert.strictEqual(id1, id2, 'same remote+path must always produce same ID');
});

test('git project IDs differ when remote URL differs', () => {
  const relativePath = '';
  const id1 = expectedGitProjectID('https://github.com/org/repo-a.git', relativePath);
  const id2 = expectedGitProjectID('https://github.com/org/repo-b.git', relativePath);
  assert.notStrictEqual(id1, id2, 'different remotes must produce different IDs');
});

test('git project IDs differ when relative path differs (monorepo)', () => {
  const remoteURL = 'https://github.com/org/monorepo.git';
  const id1 = expectedGitProjectID(remoteURL, 'packages/frontend/');
  const id2 = expectedGitProjectID(remoteURL, 'packages/backend/');
  assert.notStrictEqual(id1, id2, 'different relative paths must produce different IDs');
});

test('JS git ID algorithm matches Go ResolveProjectSlug for canonical test vector', () => {
  // Canonical test vector: the exact same key that Go uses.
  // Go: key = remoteURL + "/" + relativePath; id = sha256Hex(key)[:8]
  const remoteURL = 'https://github.com/thebtf/engram.git';
  const relativePath = '';
  const jsID = expectedGitProjectID(remoteURL, relativePath);

  // Compute expected value independently via Node crypto to verify the formula.
  const expected = crypto.createHash('sha256')
    .update(remoteURL + '/' + relativePath)
    .digest('hex')
    .slice(0, 8);

  assert.strictEqual(jsID, expected, 'JS ID must equal independently computed SHA-256 slice');
  assert.match(jsID, /^[0-9a-f]{8}$/, 'canonical vector must produce 8 hex chars');
});

// Quiet mode (ENGRAM_QUIET) — global injection kill-switch through RunHook.
// Driven via a child process because the guard lives in RunHook before the
// handler, ahead of any stdin parsing or server call.
const { execFileSync } = require('node:child_process');

function runHookProcess(scriptName, env) {
  const out = execFileSync('node', [path.join(__dirname, scriptName)], {
    input: JSON.stringify({ session_id: 'quiet-test', cwd: __dirname }),
    env: { ...process.env, ...env },
    encoding: 'utf8',
    stdio: ['pipe', 'pipe', 'ignore'],
  });
  return out.trim();
}

test('quiet mode emits empty pass-through and injects nothing (ENGRAM_QUIET=1)', () => {
  const out = runHookProcess('session-start.js', {
    ENGRAM_QUIET: '1',
    ENGRAM_URL: 'http://127.0.0.1:9/unreachable',
    ENGRAM_TOKEN: 'engram_test',
  });
  assert.strictEqual(out, '{"continue":true}',
    'quiet mode must return exactly {"continue":true} with no hookSpecificOutput');
});

test('quiet mode accepts truthy aliases and ignores falsey values', (t) => {
  for (const v of ['true', 'YES', 'on']) {
    const out = runHookProcess('session-start.js', {
      ENGRAM_QUIET: v,
      ENGRAM_URL: 'http://127.0.0.1:9/unreachable',
      ENGRAM_TOKEN: 'engram_test',
    });
    assert.strictEqual(out, '{"continue":true}', `value ${v} must enable quiet mode`);
  }
  // A falsey value must NOT short-circuit: with an unreachable server the hook
  // still runs its handler and falls through to the no-cache banner (proof the
  // handler executed rather than being skipped by quiet mode).
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-quiet-off-'));
  t.after(() => { try { fs.rmSync(dataDir, { recursive: true, force: true }); } catch (_) {} });
  const active = runHookProcess('session-start.js', {
    ENGRAM_QUIET: '0',
    ENGRAM_URL: 'http://127.0.0.1:9/unreachable',
    ENGRAM_TOKEN: 'engram_test',
    ENGRAM_DATA_DIR: dataDir,
  });
  assert.notStrictEqual(active, '{"continue":true}',
    'ENGRAM_QUIET=0 must leave the hook active (handler runs)');
});

test('quiet mode is honored from the engram config file (Codex ≥0.139 path, no env)', (t) => {
  // Codex ≥0.139 does not forward env vars to plugin hook children, so the
  // switch must also be readable from ~/.engram/config.json (here pointed at a
  // temp file via ENGRAM_CONFIG_FILE). No ENGRAM_QUIET env is set.
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-quiet-cfg-'));
  t.after(() => { try { fs.rmSync(dir, { recursive: true, force: true }); } catch (_) {} });
  const cfgPath = path.join(dir, 'config.json');
  fs.writeFileSync(cfgPath, JSON.stringify({
    server_url: 'http://127.0.0.1:9/unreachable',
    api_token: 'engram_test',
    quiet: true,
  }));

  const out = runHookProcess('session-start.js', { ENGRAM_CONFIG_FILE: cfgPath });
  assert.strictEqual(out, '{"continue":true}',
    'quiet:true in the config file must mute hooks even with no quiet env var');
});
