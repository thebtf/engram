import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import { EngramRestClient, resolveAndRegisterProject } from '../dist/client.js';
import { handleSessionStart } from '../dist/hooks/session-start.js';
import pluginModule from '../dist/index.js';
import { parseConfig } from '../dist/config.js';
import { createFileWatcherService } from '../dist/services/file-watcher.js';

const plugin = pluginModule.default ?? pluginModule;

const here = path.dirname(fileURLToPath(import.meta.url));
const vectors = JSON.parse(fs.readFileSync(
  path.resolve(here, '../../../.agent/specs/security-project-identity/evidence/project-identity-v2-vectors.json'),
  'utf8',
));

function gitIdentity() {
  return {
    projectId: 'legacy-selector',
    agentId: 'agent-a',
    gitRemote: 'https://example.invalid/acme/mono.git',
    relativePath: 'packages/core/',
    projectIdentityV2: {
      version: 2,
      legacy_project_id: 'workspace_a1b2c3',
      display_name: 'core',
      git_remote: 'https://example.invalid/acme/mono.git',
      relative_path: 'packages/core/',
      non_git_anchor: '',
      anchor_shared: null,
    },
  };
}

function clientConfig(token = 'test-token') {
  return { url: 'http://engram.test:37777', token, timeoutMs: 1000 };
}

test('registration sends full v2 metadata first, substitutes canonical, and deduplicates concurrent and late calls', async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => { globalThis.fetch = originalFetch; });
  const requests = [];
  globalThis.fetch = async (url, init) => {
    requests.push({ url: String(url), init, body: JSON.parse(String(init.body)) });
    await new Promise((resolve) => setTimeout(resolve, 5));
    return new Response(JSON.stringify({ canonical_project: 'canonical-project' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  };

  const client = new EngramRestClient(clientConfig());
  const identity = gitIdentity();
  const results = await Promise.all(Array.from({ length: 12 }, () =>
    client.registerAndResolveProject(identity, 'configured-selector')));
  const late = await client.registerAndResolveProject(identity, 'configured-selector');

  assert.equal(requests.length, 1, 'one in-flight and one completed registration must be reused');
  assert.equal(requests[0].url, 'http://engram.test:37777/api/context/inject');
  assert.equal(requests[0].body.project, 'configured-selector');
  assert.equal(requests[0].body.identity_only, true);
  assert.deepEqual(requests[0].body.project_identity, identity.projectIdentityV2);
  for (const result of [...results, late]) {
    assert.deepEqual(result, { ok: true, canonicalProject: 'canonical-project' });
  }
});

test('configured project override registers a selector-only shared scope', async () => {
  const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-configured-project-'));
  try {
    fs.writeFileSync(path.join(workspace, '.engram-project-v2.json'), '{malformed');
    const calls = [];
    const client = {
      registerAndResolveProject: async (identity, selector) => {
        calls.push({ identity, selector });
        return { ok: true, canonicalProject: selector };
      },
    };

    const result = await resolveAndRegisterProject(client, 'agent-a', workspace, 'team-memory');

    assert.deepEqual(result, { ok: true, canonicalProject: 'team-memory' });
    assert.deepEqual(calls, [{
      identity: { projectId: 'team-memory', agentId: 'agent-a' },
      selector: 'team-memory',
    }]);
  } finally {
    fs.rmSync(workspace, { recursive: true, force: true });
  }
});

test('stable registration error preserves code/action and permits zero downstream requests', async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => { globalThis.fetch = originalFetch; });
  const requests = [];
  globalThis.fetch = async (url, init) => {
    requests.push({ url: String(url), init });
    return new Response(JSON.stringify({
      error: {
        code: 'PROJECT_IDENTITY_AMBIGUOUS',
        message: 'legacy selector maps to multiple canonical projects',
        upgrade_action: 'send_project_identity_v2',
      },
    }), { status: 409, statusText: 'Conflict' });
  };

  const client = new EngramRestClient(clientConfig());
  const result = await client.registerAndResolveProject({ projectId: 'legacy', agentId: 'agent' }, 'legacy');
  const cached = await client.registerAndResolveProject({ projectId: 'legacy', agentId: 'agent' }, 'legacy');
  if (result.ok) {
    await client.searchContext({ project: result.canonicalProject, query: 'must not run' });
  }

  assert.deepEqual(result, {
    ok: false,
    error: {
      code: 'PROJECT_IDENTITY_AMBIGUOUS',
      message: 'legacy selector maps to multiple canonical projects',
      upgradeAction: 'send_project_identity_v2',
      httpStatus: 409,
    },
  });
  assert.deepEqual(cached, result);
  assert.equal(requests.length, 1, 'permanent registration failure must be cached and short-circuit data access');
});

test('session-start awaits registration before first write and honors config.project as outer selector', async () => {
  const sequence = [];
  const identity = gitIdentity();
  const fakeClient = {
    isAvailable: () => true,
    registerAndResolveProject: async (receivedIdentity, selector) => {
      sequence.push(['register', receivedIdentity, selector]);
      return { ok: true, canonicalProject: 'canonical-from-server' };
    },
    initSession: async (body) => {
      sequence.push(['data', body]);
      return { sessionDbId: 1, promptNumber: 1 };
    },
  };

  await handleSessionStart(
    { initialPrompt: 'hello' },
    { agentId: 'agent-a', sessionId: 'session-a', workspaceDir: undefined },
    fakeClient,
    { project: 'configured-selector' },
  );

  assert.equal(sequence[0][0], 'register');
  assert.equal(sequence[0][2], 'configured-selector');
  assert.equal(sequence[1][0], 'data');
  assert.equal(sequence[1][1].project, 'canonical-from-server');
});

test('session-start stable registration failure sends no session write', async () => {
  let writes = 0;
  const fakeClient = {
    isAvailable: () => true,
    registerAndResolveProject: async () => ({
      ok: false,
      error: {
        code: 'PROJECT_IDENTITY_AMBIGUOUS',
        message: 'ambiguous',
        upgradeAction: 'send_project_identity_v2',
        httpStatus: 409,
      },
    }),
    initSession: async () => { writes++; },
  };

  await handleSessionStart(
    { initialPrompt: 'hello' },
    { agentId: 'agent-a', sessionId: 'session-a' },
    fakeClient,
    {},
  );
  assert.equal(writes, 0);
});

test('invalid bearer plus a known selector never reaches private data access', async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => { globalThis.fetch = originalFetch; });
  const requests = [];
  globalThis.fetch = async (url, init) => {
    requests.push({ url: String(url), authorization: init.headers.Authorization });
    return new Response(JSON.stringify({ error: 'unauthorized' }), { status: 401, statusText: 'Unauthorized' });
  };
  const client = new EngramRestClient(clientConfig('invalid-bearer'));
  const result = await client.registerAndResolveProject(gitIdentity(), 'known-private-selector');
  if (result.ok) {
    await client.searchContext({ project: result.canonicalProject, query: 'private' });
  }
  assert.equal(result.ok, false);
  assert.equal(requests.length, 1);
  assert.equal(requests[0].authorization, 'Bearer invalid-bearer');
});

test('registration rejects shared invalid selectors before fetch and never trims them', async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => { globalThis.fetch = originalFetch; });
  for (const vector of vectors.invalid_vectors) {
    if (vector.invalid_target !== 'selector') continue;
    let requests = 0;
    globalThis.fetch = async () => {
      requests++;
      return new Response(JSON.stringify({ canonical_project: 'must-not-run' }), { status: 200 });
    };
    const client = new EngramRestClient(clientConfig());
    const result = await client.registerAndResolveProject(gitIdentity(), vector.selector);
    assert.deepEqual(result, {
      ok: false,
      error: {
        code: 'PROJECT_IDENTITY_INVALID',
        message: 'project selector is empty or malformed',
        upgradeAction: 'regenerate_project_identity_v2',
        httpStatus: 400,
      },
    }, vector.name);
    assert.equal(requests, 0, vector.name);
  }
});

test('registration preserves legacy selector characters accepted by the HTTP boundary', async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => { globalThis.fetch = originalFetch; });
  const selector = 'legacy:C\\workspace';
  let sentSelector = '';
  globalThis.fetch = async (_url, init) => {
    sentSelector = JSON.parse(String(init?.body)).project;
    return new Response(JSON.stringify({ canonical_project: selector }), { status: 200 });
  };
  const client = new EngramRestClient(clientConfig());
  const result = await client.registerAndResolveProject(gitIdentity(), selector);
  assert.deepEqual(result, { ok: true, canonicalProject: selector });
  assert.equal(sentSelector, selector);
});

test('2xx malformed canonical response is not cached and cannot reach downstream data', async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => { globalThis.fetch = originalFetch; });
  const payloads = [
    {},
    { canonical_project: '' },
    { canonical_project: 42 },
    { canonical_project: ' invalid-canonical ' },
    { canonical_project: '../private' },
  ];
  for (const payload of payloads) {
    const paths = [];
    globalThis.fetch = async (url) => {
      paths.push(new URL(String(url)).pathname);
      return new Response(JSON.stringify(payload), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    };
    const client = new EngramRestClient(clientConfig());
    const first = await client.registerAndResolveProject(gitIdentity(), 'legacy-selector');
    if (first.ok) {
      await client.searchContext({ project: first.canonicalProject, query: 'must-not-run' });
    }
    const second = await client.registerAndResolveProject(gitIdentity(), 'legacy-selector');
    const expected = {
      ok: false,
      error: {
        code: 'PROJECT_IDENTITY_UNAVAILABLE',
        message: 'project identity registration response is malformed',
        upgradeAction: 'retry_project_identity_registration',
        httpStatus: 503,
      },
    };
    assert.deepEqual(first, expected);
    assert.deepEqual(second, expected);
    assert.deepEqual(paths, ['/api/context/inject', '/api/context/inject']);
  }
});

test('async hook registration returns the handler promises to the host', async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => { globalThis.fetch = originalFetch; });
  const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-hook-promises-'));
  t.after(() => fs.rmSync(workspace, { recursive: true, force: true }));
  globalThis.fetch = async () => new Response(JSON.stringify({ canonical_project: 'canonical-project' }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
  const callbacks = new Map();
  const logger = { debug() { }, info() { }, warn() { }, error() { } };
  plugin.register({
    pluginConfig: {
      url: 'http://engram.test:37777',
      token: 'test-token',
      autoExtract: false,
      workspaceDir: workspace,
    },
    logger,
    on(name, callback) { callbacks.set(name, callback); },
    registerTool() { },
    registerCommand() { },
    registerCli() { },
    registerService() { },
  });

  const cases = [
    ['after_tool_call', { toolName: 'noop' }],
    ['before_compaction', { messages: [] }],
    ['session_end', { messages: [] }],
  ];
  for (const [name, event] of cases) {
    const result = callbacks.get(name)(event, { agentId: 'agent-a', sessionId: 'session-a', workspaceDir: workspace });
    assert.equal(typeof result?.then, 'function', `${name} must return a Promise`);
    await result;
  }
});

test('file watcher startup rejects when canonical registration fails', async () => {
  const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-file-watcher-registration-'));
  try {
    const warnings = [];
    let registrationCall;
    const logger = {
      debug() { },
      info() { },
      warn(message) { warnings.push(message); },
      error() { },
    };
    const client = {
      registerAndResolveProject: async (identity, selector) => {
        registrationCall = { identity, selector };
        return {
          ok: false,
          error: { code: 'PROJECT_IDENTITY_UNAVAILABLE' },
        };
      },
    };
    const service = createFileWatcherService(workspace, client, parseConfig({
      url: 'http://engram.test:37777',
      token: 'test-token',
      project: 'team-memory',
    }), logger);

    await assert.rejects(service.start({ logger }), /PROJECT_IDENTITY_UNAVAILABLE/);
    assert.deepEqual(warnings, ['[file-watcher] project registration failed: PROJECT_IDENTITY_UNAVAILABLE']);
    assert.deepEqual(registrationCall, {
      identity: { projectId: 'team-memory', agentId: 'file-watcher' },
      selector: 'team-memory',
    });
  } finally {
    fs.rmSync(workspace, { recursive: true, force: true });
  }
});
