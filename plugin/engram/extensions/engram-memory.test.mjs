import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import lib from '../hooks/lib.js';
import engramMemory, { ambientMessage, sessionStartMessage } from './engram-memory.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const packagePath = path.resolve(here, '..', 'package.json');
const canonicalManifestPath = path.resolve(here, '..', '.claude-plugin', 'plugin.json');
const extensionPath = path.resolve(here, 'engram-memory.mjs');
const identity = {
  version: 2,
  legacy_project_id: 'engram_123456',
  display_name: 'engram',
  git_remote: 'https://github.com/thebtf/engram.git',
  relative_path: '',
  non_git_anchor: '',
  anchor_shared: null,
};

function installConfiguredStubs(requestPost) {
  const original = Object.fromEntries([
    'getEngramConfig', 'isQuietMode', 'resolveProjectIdentityV2', 'ProjectIDWithName',
    'LegacyProjectID', 'registerProjectIdentityV2', 'requestPost',
  ].map((name) => [name, lib[name]]));
  lib.getEngramConfig = () => ({ serverURL: 'http://127.0.0.1:37777', token: 'worker-secret-token' });
  lib.isQuietMode = () => false;
  lib.resolveProjectIdentityV2 = () => identity;
  lib.ProjectIDWithName = () => 'engram';
  lib.LegacyProjectID = () => 'engram_123456';
  lib.registerProjectIdentityV2 = async (context) => {
    context.Project = 'p2g_canonical';
    return context.Project;
  };
  lib.requestPost = requestPost;
  return () => Object.assign(lib, original);
}

function adapterHarness() {
  const handlers = new Map();
  const sent = [];
  engramMemory({
    on(event, handler) { handlers.set(event, handler); },
    sendMessage(message, options) { sent.push({ message, options }); },
  });
  return { handlers, sent };
}

test('package exposes the native OMP extension at the canonical plugin version', () => {
  const manifest = JSON.parse(fs.readFileSync(packagePath, 'utf8'));
  const canonicalManifest = JSON.parse(fs.readFileSync(canonicalManifestPath, 'utf8'));
  assert.equal(manifest.version, canonicalManifest.version);
  assert.deepEqual(manifest.omp.extensions, ['./extensions/engram-memory.mjs']);
  assert.equal(fs.existsSync(extensionPath), true);
  assert.equal(Object.hasOwn(manifest, 'type'), false);
});
test('exported factory registers the supported OMP event handlers', () => {
  const { handlers } = adapterHarness();
  assert.deepEqual([...handlers.keys()], ['session_start', 'before_agent_start']);
});

test('quiet mode sends no messages and makes no requests', async () => {
  const originalQuiet = lib.isQuietMode;
  const originalConfig = lib.getEngramConfig;
  const originalRequest = lib.requestPost;
  let requested = false;
  lib.isQuietMode = () => true;
  lib.getEngramConfig = () => ({ serverURL: 'http://127.0.0.1:37777', token: 'worker-secret-token' });
  lib.requestPost = async () => { requested = true; };
  try {
    const { handlers, sent } = adapterHarness();
    await handlers.get('session_start')({ cwd: process.cwd(), sessionId: 'quiet-session' }, {});
    assert.equal(await handlers.get('before_agent_start')({ cwd: process.cwd(), sessionId: 'quiet-session', prompt: 'remember this' }, {}), undefined);
    assert.equal(sent.length, 0);
    assert.equal(requested, false);
  } finally {
    Object.assign(lib, { isQuietMode: originalQuiet, getEngramConfig: originalConfig, requestPost: originalRequest });
  }
});

test('session start validates identity then queues one hidden next-turn context without leaking config', async () => {
  const calls = [];
  const registrations = [];
  const restore = installConfiguredStubs(async (endpoint, body, timeoutMs) => {
    calls.push({ endpoint, body, timeoutMs });
    if (endpoint === '/api/context/session-start') {
      return { memories: [{ content: `Use the project memory contract. ${'x'.repeat(20000)}` }], issues: [], rules: [] };
    }
    throw new Error(`unexpected endpoint ${endpoint}`);
  });
  lib.registerProjectIdentityV2 = async (context) => {
    registrations.push({ ...context });
    context.Project = 'p2g_canonical';
    return context.Project;
  };
  try {
    const { handlers, sent } = adapterHarness();
    await handlers.get('session_start')({ type: 'session_start' }, {
      cwd: process.cwd(),
      sessionManager: { getSessionId: () => 'start-session' },
    });
    assert.deepEqual(registrations, [{
      Project: 'engram',
      LegacyProject: 'engram_123456',
      GitRemote: identity.git_remote,
      RelativePath: identity.relative_path,
      ProjectIdentityV2: identity,
    }]);
    assert.deepEqual(calls, [{
      endpoint: '/api/context/session-start',
      body: { project: 'p2g_canonical', session_id: 'start-session' },
      timeoutMs: 5000,
    }]);
    assert.equal(sent.length, 1);
    assert.deepEqual(sent[0].options, { deliverAs: 'nextTurn' });
    assert.equal(sent[0].message.customType, 'engram-memory');
    assert.equal(sent[0].message.display, false);
    assert.equal(sent[0].message.attribution, 'agent');
    assert.match(sent[0].message.content, /Use the project memory contract/);
    assert.ok(sent[0].message.content.length <= 12000);
    assert.doesNotMatch(JSON.stringify(sent), /worker-secret-token|127\.0\.0\.1:37777/);
  } finally {
    restore();
  }
});

test('before-agent-start returns one hidden bounded ambient message under the existing three-hint budget', async () => {
  const calls = [];
  const restore = installConfiguredStubs(async (endpoint, body, timeoutMs) => {
    calls.push({ endpoint, body, timeoutMs });
    if (endpoint === '/api/hooks/ambient-candidates') {
      return {
        hints: [
          { title: 'First hint', reason: 'current task', score: 0.9 },
          { title: 'Second hint', reason: 'same turn', score: 0.8 },
          { title: 'Third hint', reason: 'budget edge', score: 0.7 },
          { title: 'Fourth hint', reason: 'must not render', score: 0.6 },
        ]
      };
    }
    throw new Error(`unexpected endpoint ${endpoint}`);
  });
  try {
    const message = await ambientMessage({ cwd: process.cwd(), sessionId: 'ambient-session', prompt: 'Need memory now' }, {});
    assert.deepEqual(calls, [{
      endpoint: '/api/hooks/ambient-candidates',
      body: { project: 'p2g_canonical', session_id: 'ambient-session', prompt_text: 'Need memory now', limit: 3 },
      timeoutMs: 200,
    }]);
    assert.equal(message.display, false);
    assert.equal(message.attribution, 'agent');
    assert.match(message.content, /First hint/);
    assert.match(message.content, /Third hint/);
    assert.doesNotMatch(message.content, /Fourth hint/);
  } finally {
    restore();
  }
});

test('canonical OMP before-agent events resolve sessionManager and return the message wrapper', async () => {
  const calls = [];
  const restore = installConfiguredStubs(async (endpoint, body, timeoutMs) => {
    calls.push({ endpoint, body, timeoutMs });
    if (endpoint === '/api/hooks/ambient-candidates') {
      return { hints: [{ title: 'Canonical hint', reason: 'current task', score: 0.9 }] };
    }
    throw new Error(`unexpected endpoint ${endpoint}`);
  });
  try {
    const { handlers } = adapterHarness();
    const handler = handlers.get('before_agent_start');
    const result = await handler({
      type: 'before_agent_start',
      prompt: 'Need canonical memory now',
      images: [],
      systemPrompt: ['canonical system prompt'],
    }, {
      cwd: process.cwd(),
      sessionManager: { getSessionId: () => 'canonical-session' },
    });
    assert.deepEqual(calls, [{
      endpoint: '/api/hooks/ambient-candidates',
      body: { project: 'p2g_canonical', session_id: 'canonical-session', prompt_text: 'Need canonical memory now', limit: 3 },
      timeoutMs: 200,
    }]);
    assert.deepEqual(Object.keys(result), ['message']);
    assert.equal(result.message.customType, 'engram-memory');
    assert.equal(result.message.display, false);
    assert.equal(result.message.attribution, 'agent');
    assert.match(result.message.content, /Canonical hint/);
    assert.equal(await handler({ type: 'before_agent_start', prompt: 'No session manager' }, { cwd: process.cwd() }), undefined);
    assert.equal(await handler({ type: 'before_agent_start', prompt: 'Throwing session manager' }, {
      cwd: process.cwd(),
      sessionManager: { getSessionId() { throw new Error('session unavailable'); } },
    }), undefined);
    assert.equal(calls.length, 1);
  } finally {
    restore();
  }
});


test('before-agent-start fails open within 200 ms when identity registration stalls', async () => {
  const restore = installConfiguredStubs(async () => {
    throw new Error('ambient request must not begin after stalled identity registration');
  });
  lib.registerProjectIdentityV2 = async () => new Promise(() => { });
  try {
    const started = performance.now();
    const message = await ambientMessage({ cwd: process.cwd(), sessionId: 'stalled-identity', prompt: 'Need memory now' }, {});
    const elapsedMs = performance.now() - started;
    assert.equal(message, null);
    assert.ok(elapsedMs >= 180 && elapsedMs < 275, `whole before-agent-start path took ${elapsedMs} ms`);
  } finally {
    restore();
  }
});
test('missing configuration, identity failures, and request deadline errors fail open', async () => {
  const originalConfig = lib.getEngramConfig;
  lib.getEngramConfig = () => ({ serverURL: '', token: '' });
  try {
    assert.equal(await sessionStartMessage({ cwd: process.cwd(), sessionId: 'no-config' }, {}), null);
  } finally {
    lib.getEngramConfig = originalConfig;
  }

  const restore = installConfiguredStubs(async (endpoint) => {
    if (endpoint === '/api/hooks/ambient-candidates') {
      const error = new Error('deadline exceeded');
      error.name = 'AbortError';
      throw error;
    }
    throw new Error(`unexpected endpoint ${endpoint}`);
  });
  const originalRegister = lib.registerProjectIdentityV2;
  try {
    assert.equal(await ambientMessage({ cwd: process.cwd(), sessionId: 'timed-out', prompt: 'Need memory now' }, {}), null);
    lib.registerProjectIdentityV2 = async () => { throw new Error('backend unavailable'); };
    assert.equal(await sessionStartMessage({ cwd: process.cwd(), sessionId: 'backend-error' }, {}), null);
  } finally {
    lib.registerProjectIdentityV2 = originalRegister;
    restore();
  }
});
