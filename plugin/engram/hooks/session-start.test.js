const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const lib = require('./lib');
const {
  handleSessionStart,
  buildCachedSessionStartPayload,
} = require('./session-start');

test('handleSessionStart caches live static payload and renders issues, rules, and memories', async () => {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-session-start-live-'));
  const originalRequestGet = lib.requestGet;
  const originalRequestPost = lib.requestPost;
  const originalEngramDataDir = process.env.ENGRAM_DATA_DIR;
  const originalEngramURL = process.env.ENGRAM_URL;

  process.env.ENGRAM_DATA_DIR = tmpDir;
  process.env.ENGRAM_URL = 'http://example.test/mcp';

  const getCalls = [];
  const postCalls = [];
  lib.requestGet = async (endpoint) => {
    getCalls.push(endpoint);
    return buildCachedSessionStartPayload({
      issues: [
        {
          id: 11,
          title: 'Investigate failing startup path',
          status: 'open',
          priority: 'high',
          type: 'bug',
          source_project: 'orchestrator',
          target_project: 'engram',
          source_agent: 'agent-x',
          labels: ['bug'],
          comment_count: 0,
          created_at: '2026-04-22T12:00:00Z',
          updated_at: '2026-04-22T12:00:00Z',
        },
      ],
      rules: [
        { id: 21, content: 'Always validate API responses before use.', project: 'engram' },
      ],
      memories: [
        { id: 31, content: 'Session-start payload is static-only in v5.' },
      ],
      generated_at: '2026-04-22T12:34:56Z',
    });
  };
  lib.requestPost = async (endpoint, body) => {
    postCalls.push({ endpoint, body });
    return {};
  };

  try {
    const result = await handleSessionStart({ Project: 'engram', SessionID: 'sess-live' }, {});
    assert.match(result, /<open-issues/);
    assert.match(result, /Investigate failing startup path/);
    assert.match(result, /<user-behavior-rules>/);
    assert.match(result, /Always validate API responses before use\./);
    assert.match(result, /<engram-static-memories>/);
    assert.match(result, /Session-start payload is static-only in v5\./);
    assert.ok(getCalls.some((endpoint) => endpoint.includes('/api/context/session-start?project=engram')));
    assert.ok(postCalls.some((call) => call.endpoint === '/api/issues/acknowledge'));

    const cachePath = lib.getSessionStartCachePath('engram');
    assert.ok(fs.existsSync(cachePath), 'expected cache file to be written');
    const cached = JSON.parse(fs.readFileSync(cachePath, 'utf8'));
    assert.equal(cached.generated_at, '2026-04-22T12:34:56Z');
  } finally {
    lib.requestGet = originalRequestGet;
    lib.requestPost = originalRequestPost;
    if (originalEngramDataDir === undefined) {
      delete process.env.ENGRAM_DATA_DIR;
    } else {
      process.env.ENGRAM_DATA_DIR = originalEngramDataDir;
    }
    if (originalEngramURL === undefined) {
      delete process.env.ENGRAM_URL;
    } else {
      process.env.ENGRAM_URL = originalEngramURL;
    }
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});

test('handleSessionStart falls back to cached payload with stale banner on transport failure', async () => {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-session-start-cache-'));
  const originalRequestGet = lib.requestGet;
  const originalRequestPost = lib.requestPost;
  const originalEngramDataDir = process.env.ENGRAM_DATA_DIR;
  const originalEngramURL = process.env.ENGRAM_URL;

  process.env.ENGRAM_DATA_DIR = tmpDir;
  process.env.ENGRAM_URL = 'http://example.test/mcp';

  const cachePath = path.join(tmpDir, 'cache', 'session-start-engram.json');
  fs.mkdirSync(path.dirname(cachePath), { recursive: true });
  fs.writeFileSync(cachePath, JSON.stringify(buildCachedSessionStartPayload({
    issues: [
      {
        id: 41,
        title: 'Cached issue',
        status: 'acknowledged',
        priority: 'medium',
        type: 'task',
        source_project: 'orchestrator',
        target_project: 'engram',
        source_agent: 'agent-y',
        labels: [],
        comment_count: 1,
        created_at: '2026-04-22T11:00:00Z',
        updated_at: '2026-04-22T11:30:00Z',
      },
    ],
    rules: [
      { id: 51, content: 'Cached rule content.' },
    ],
    memories: [
      { id: 61, content: 'Cached memory content.' },
    ],
    generated_at: '2026-04-22T11:59:59Z',
  }), null, 2), 'utf8');

  lib.requestGet = async () => {
    throw new Error('connect ETIMEDOUT');
  };
  lib.requestPost = async () => ({});

  try {
    const result = await handleSessionStart({ Project: 'engram', SessionID: 'sess-cache' }, {});
    assert.match(result, /<engram-session-start-stale>/);
    assert.match(result, /Cached payload generated at 2026-04-22T11:59:59Z/);
    assert.match(result, /Cached issue/);
    assert.match(result, /Cached rule content\./);
    assert.match(result, /Cached memory content\./);
  } finally {
    lib.requestGet = originalRequestGet;
    lib.requestPost = originalRequestPost;
    if (originalEngramDataDir === undefined) {
      delete process.env.ENGRAM_DATA_DIR;
    } else {
      process.env.ENGRAM_DATA_DIR = originalEngramDataDir;
    }
    if (originalEngramURL === undefined) {
      delete process.env.ENGRAM_URL;
    } else {
      process.env.ENGRAM_URL = originalEngramURL;
    }
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});

test('handleSessionStart returns no-cache banner when live fetch fails and cache is absent', async () => {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-session-start-empty-'));
  const originalRequestGet = lib.requestGet;
  const originalRequestPost = lib.requestPost;
  const originalEngramDataDir = process.env.ENGRAM_DATA_DIR;
  const originalEngramURL = process.env.ENGRAM_URL;

  process.env.ENGRAM_DATA_DIR = tmpDir;
  process.env.ENGRAM_URL = 'http://example.test/mcp';

  lib.requestGet = async () => {
    throw new Error('network down');
  };
  lib.requestPost = async () => ({});

  try {
    const result = await handleSessionStart({ Project: 'engram', SessionID: 'sess-empty' }, {});
    assert.match(result, /<engram-session-start-unavailable>/);
    assert.match(result, /no cache is present/i);
  } finally {
    lib.requestGet = originalRequestGet;
    lib.requestPost = originalRequestPost;
    if (originalEngramDataDir === undefined) {
      delete process.env.ENGRAM_DATA_DIR;
    } else {
      process.env.ENGRAM_DATA_DIR = originalEngramDataDir;
    }
    if (originalEngramURL === undefined) {
      delete process.env.ENGRAM_URL;
    } else {
      process.env.ENGRAM_URL = originalEngramURL;
    }
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});
