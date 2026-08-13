import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
import lib from '../hooks/lib.js';
import engramMemory, { ambientMessage, sessionStartMessage } from './engram-memory.mjs';


const require = createRequire(import.meta.url);

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
    'resolveEngramRuntimeConfig', 'resolveHookProjectIdentityV2',
    'resolveProjectIdentityV2', 'ProjectIDWithName', 'LegacyProjectID',
    'registerProjectIdentityV2', 'requestPost',
  ].map((name) => [name, lib[name]]));
  lib.resolveEngramRuntimeConfig = async () => ({
    serverURL: 'http://127.0.0.1:37777', token: 'worker-secret-token', quiet: false,
  });
  lib.resolveProjectIdentityV2 = () => identity;
  lib.resolveHookProjectIdentityV2 = async () => identity;
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
  assert.equal(manifest.engines.node, '>=18');
  assert.deepEqual(manifest.omp.extensions, ['./extensions/engram-memory.mjs']);
  assert.equal(fs.existsSync(extensionPath), true);
  assert.equal(Object.hasOwn(manifest, 'type'), false);
});
test('exported factory registers the supported OMP event handlers', () => {
  const { handlers } = adapterHarness();
  assert.deepEqual([...handlers.keys()], ['session_start', 'before_agent_start']);
});

test('OMP documentation distinguishes native injection from Claude hooks and MCP', () => {
  const readme = fs.readFileSync(path.resolve(here, '..', '..', '..', 'README.md'), 'utf8');
  const setup = fs.readFileSync(path.resolve(here, '..', 'commands', 'setup.md'), 'utf8');
  assert.match(readme, /OMP 17\.x does not execute Claude `hooks\.json`/);
  assert.match(readme, /the bundled native Engram extension instead injects Engram context on `session_start` and\n`before_agent_start`/);
  assert.match(setup, /OMP it suppresses the native `session_start` and `before_agent_start` injection\npaths/);
  assert.match(setup, /it never disables MCP tools/);
});

test('quiet mode sends no messages and makes no requests', async () => {
  const originalConfig = lib.resolveEngramRuntimeConfig;
  const originalRequest = lib.requestPost;
  let requested = false;
  lib.resolveEngramRuntimeConfig = async () => ({
    serverURL: 'http://127.0.0.1:37777', token: 'worker-secret-token', quiet: true,
  });
  lib.requestPost = async () => { requested = true; };
  try {
    const { handlers, sent } = adapterHarness();
    await handlers.get('session_start')({ cwd: process.cwd(), sessionId: 'quiet-session' }, {});
    assert.equal(await handlers.get('before_agent_start')({ cwd: process.cwd(), sessionId: 'quiet-session', prompt: 'remember this' }, {}), undefined);
    assert.equal(sent.length, 0);
    assert.equal(requested, false);
  } finally {
    Object.assign(lib, { resolveEngramRuntimeConfig: originalConfig, requestPost: originalRequest });
  }
});

test('session start validates identity then queues one hidden next-turn context without leaking config', async () => {
  const calls = [];
  const registrations = [];
  const restore = installConfiguredStubs(async (endpoint, body, timeoutMs, options) => {
    calls.push({ endpoint, body, timeoutMs, options });
    if (endpoint === '/api/context/session-start') {
      return { memories: [{ content: `Use the project memory contract. ${'x'.repeat(20000)}` }], issues: [], rules: [] };
    }
    throw new Error(`unexpected endpoint ${endpoint}`);
  });
  lib.registerProjectIdentityV2 = async (context, _request, options) => {
    registrations.push({ ...context, options });
    context.Project = 'p2g_canonical';
    return context.Project;
  };
  try {
    const { handlers, sent } = adapterHarness();
    await handlers.get('session_start')({ type: 'session_start' }, {
      cwd: process.cwd(),
      sessionManager: { getSessionId: () => 'start-session' },
    });
    assert.equal(registrations.length, 1);
    assert.match(registrations[0].Project, /^[0-9a-f]{8}$/);
    assert.equal(registrations[0].LegacyProject, 'engram_123456');
    assert.equal(registrations[0].GitRemote, identity.git_remote);
    assert.equal(registrations[0].RelativePath, identity.relative_path);
    assert.strictEqual(registrations[0].ProjectIdentityV2, identity);
    assert.strictEqual(registrations[0].options.signal, calls[0].options.signal);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].endpoint, '/api/context/session-start');
    assert.deepEqual(calls[0].body, { project: 'p2g_canonical', session_id: 'start-session' });
    assert.ok(calls[0].timeoutMs > 0 && calls[0].timeoutMs <= 5000);
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

test('config-file credential rotation reaches OMP requests without env mutation', async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-runtime-rotation-'));
  const configFile = path.join(dir, 'config.json');
  const envKeys = [
    'ENGRAM_CONFIG_FILE', 'ENGRAM_DATA_DIR', 'CLAUDE_PLUGIN_DATA',
    'ENGRAM_URL', 'ENGRAM_SERVER_URL', 'CLAUDE_PLUGIN_OPTION_server_url',
    'CLAUDE_PLUGIN_OPTION_SERVER_URL', 'ENGRAM_CLAUDE_USERCONFIG_URL',
    'ENGRAM_TOKEN', 'CLAUDE_PLUGIN_OPTION_api_token', 'CLAUDE_PLUGIN_OPTION_API_TOKEN',
    'ENGRAM_CLAUDE_USERCONFIG_TOKEN',
  ];
  const previousEnv = Object.fromEntries(envKeys.map((key) => [key, process.env[key]]));
  for (const key of envKeys) delete process.env[key];
  process.env.ENGRAM_CONFIG_FILE = configFile;

  const originalIdentity = lib.resolveHookProjectIdentityV2;
  const originalFetch = globalThis.fetch;
  lib.resolveHookProjectIdentityV2 = async () => identity;
  const requests = [];
  globalThis.fetch = async (url, options) => {
    requests.push({ url: String(url), authorization: options.headers.Authorization });
    const body = String(url).endsWith('/api/context/inject')
      ? { canonical_project: 'p2g_canonical' }
      : { issues: [], rules: [], memories: [{ content: 'rotated config context' }] };
    return { ok: true, text: async () => JSON.stringify(body) };
  };

  t.after(() => {
    lib.resolveHookProjectIdentityV2 = originalIdentity;
    globalThis.fetch = originalFetch;
    for (const key of envKeys) {
      if (previousEnv[key] === undefined) delete process.env[key];
      else process.env[key] = previousEnv[key];
    }
    fs.rmSync(dir, { recursive: true, force: true });
  });

  fs.writeFileSync(configFile, JSON.stringify({
    server_url: 'http://first.example.test/mcp', api_token: 'first-token',
  }));
  assert.match((await sessionStartMessage({ cwd: process.cwd(), sessionId: 'first-config' }, {})).content, /rotated config context/);

  fs.writeFileSync(configFile, JSON.stringify({
    server_url: 'http://second.example.test/mcp', api_token: 'second-token',
  }));
  assert.match((await sessionStartMessage({ cwd: process.cwd(), sessionId: 'second-config' }, {})).content, /rotated config context/);

  assert.deepEqual(requests, [
    { url: 'http://first.example.test/api/context/inject', authorization: 'Bearer first-token' },
    { url: 'http://first.example.test/api/context/session-start', authorization: 'Bearer first-token' },
    { url: 'http://second.example.test/api/context/inject', authorization: 'Bearer second-token' },
    { url: 'http://second.example.test/api/context/session-start', authorization: 'Bearer second-token' },
  ]);
  assert.equal(process.env.ENGRAM_URL, undefined);
  assert.equal(process.env.ENGRAM_TOKEN, undefined);
});

test('session start passes only the remaining registration budget to context fetch', async () => {
  const calls = [];
  const restore = installConfiguredStubs(async (endpoint, body, timeoutMs) => {
    calls.push({ endpoint, body, timeoutMs });
    return { issues: [], rules: [], memories: [{ content: 'ordinary session context' }] };
  });
  lib.registerProjectIdentityV2 = async (context) => {
    await new Promise((resolve) => setTimeout(resolve, 15));
    context.Project = 'p2g_canonical';
  };
  try {
    const message = await sessionStartMessage({ cwd: process.cwd(), sessionId: 'remaining-budget' }, {}, 80);
    assert.match(message.content, /ordinary session context/);
    assert.equal(calls.length, 1);
    assert.ok(calls[0].timeoutMs > 0 && calls[0].timeoutMs < 80);
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
    assert.equal(calls.length, 1);
    assert.equal(calls[0].endpoint, '/api/hooks/ambient-candidates');
    assert.deepEqual(calls[0].body, { project: 'p2g_canonical', session_id: 'ambient-session', prompt_text: 'Need memory now', limit: 3 });
    assert.ok(calls[0].timeoutMs > 0 && calls[0].timeoutMs <= 200);
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
    assert.equal(calls.length, 1);
    assert.equal(calls[0].endpoint, '/api/hooks/ambient-candidates');
    assert.deepEqual(calls[0].body, { project: 'p2g_canonical', session_id: 'canonical-session', prompt_text: 'Need canonical memory now', limit: 3 });
    assert.ok(calls[0].timeoutMs > 0 && calls[0].timeoutMs <= 200);
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

test('ambient shares one signal across identity, registration, and request and suppresses late context', async () => {
  const calls = [];
  const restore = installConfiguredStubs(async (_endpoint, _body, _timeoutMs, options) => {
    calls.push(options.signal);
    return new Promise((resolve) => setTimeout(() => resolve({ hints: [{ title: 'late', reason: 'late', score: 1 }] }), 250));
  });
  let configSignal;
  let identitySignal;
  let registrationSignal;
  lib.resolveEngramRuntimeConfig = async ({ signal }) => {
    configSignal = signal;
    return { serverURL: 'http://127.0.0.1:37777', token: 'worker-secret-token', quiet: false };
  };
  lib.resolveHookProjectIdentityV2 = async (_cwd, options) => {
    identitySignal = options.signal;
    return identity;
  };
  lib.registerProjectIdentityV2 = async (_context, _request, options) => {
    registrationSignal = options.signal;
    await new Promise((resolve) => setTimeout(resolve, 10));
  };
  try {
    assert.equal(await ambientMessage({ cwd: process.cwd(), sessionId: 'late-ambient', prompt: 'No late result' }, {}), null);
    assert.strictEqual(configSignal, identitySignal);
    assert.strictEqual(identitySignal, registrationSignal);
    assert.strictEqual(registrationSignal, calls[0]);
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

test('session start aborts a pending real identity registration without late context effects', async () => {
  let contextRequests = 0;
  const originalRegister = lib.registerProjectIdentityV2;
  const restore = installConfiguredStubs(async () => {
    contextRequests += 1;
    throw new Error('session context fetch must not begin after registration deadline');
  });
  lib.registerProjectIdentityV2 = originalRegister;
  const originalFetch = globalThis.fetch;
  const originalURL = process.env.ENGRAM_URL;
  const originalToken = process.env.ENGRAM_TOKEN;
  let aborts = 0;
  let resolveRegistration;
  let registrationStarted;
  const started = new Promise((resolve) => { registrationStarted = resolve; });
  process.env.ENGRAM_URL = 'http://127.0.0.1:37777';
  process.env.ENGRAM_TOKEN = 'worker-secret-token';
  globalThis.fetch = (url, options) => {
    registrationStarted();
    assert.match(String(url), /\/api\/context\/inject$/);
    assert.equal(options.method, 'POST');
    assert.ok(options.signal);
    return new Promise((resolve) => {
      resolveRegistration = resolve;
      options.signal.addEventListener('abort', () => { aborts += 1; }, { once: true });
    });
  };
  try {
    const pendingMessage = sessionStartMessage({ cwd: process.cwd(), sessionId: 'stalled-registration' }, {}, 100);
    await started;
    const message = await pendingMessage;
    assert.equal(message, null);
    assert.equal(aborts, 1);
    assert.equal(contextRequests, 0);

    resolveRegistration({
      ok: true,
      status: 200,
      statusText: 'OK',
      text: async () => JSON.stringify({ canonical_project: 'p2g_late' }),
    });
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(contextRequests, 0);
  } finally {
    globalThis.fetch = originalFetch;
    if (originalURL === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalURL;
    if (originalToken === undefined) delete process.env.ENGRAM_TOKEN;
    else process.env.ENGRAM_TOKEN = originalToken;
    restore();
  }
});
test('session start does not begin context after a late custom registration settles', async () => {
  let contextRequests = 0;
  let resolveRegistration;
  let registrationStarted;
  let registrationAborted;
  const started = new Promise((resolve) => { registrationStarted = resolve; });
  const aborted = new Promise((resolve) => { registrationAborted = resolve; });
  const restore = installConfiguredStubs(async () => {
    contextRequests += 1;
    throw new Error('context fetch must not begin after the session deadline');
  });
  lib.registerProjectIdentityV2 = (context, _request, options) => new Promise((resolve) => {
    registrationStarted(options.signal);
    options.signal.addEventListener('abort', registrationAborted, { once: true });
    resolveRegistration = () => {
      context.Project = 'p2g_late';
      resolve(context.Project);
    };
  });
  try {
    const pendingMessage = sessionStartMessage({ cwd: process.cwd(), sessionId: 'late-custom-registration' }, {}, 40);
    const registrationSignal = await started;
    await aborted;
    assert.equal(registrationSignal.aborted, true);
    resolveRegistration();
    const message = await pendingMessage;
    assert.equal(message, null);
    assert.equal(contextRequests, 0);
  } finally {
    restore();
  }
});

test('session start rejects a context whose synchronous renderer passes the shared deadline', async () => {
  const budgetMs = 25;
  const startedAt = performance.now();
  let requestStartedAt;
  let requestResolvedAt;
  let contentReads = 0;
  const restore = installConfiguredStubs(async (endpoint) => {
    assert.equal(endpoint, '/api/context/session-start');
    requestStartedAt = performance.now() - startedAt;
    requestResolvedAt = performance.now() - startedAt;
    return {
      issues: [],
      rules: [],
      memories: [{
        get content() {
          contentReads += 1;
          if (contentReads === 1) {
            const blockStartedAt = performance.now();
            while (performance.now() - blockStartedAt <= budgetMs) { }
          }
          return 'context rendered after deadline';
        },
      }],
    };
  });
  try {
    const message = await sessionStartMessage({ cwd: process.cwd(), sessionId: 'render-deadline' }, {}, budgetMs);
    assert.ok(requestStartedAt >= 0 && requestStartedAt < budgetMs);
    assert.ok(requestResolvedAt >= 0 && requestResolvedAt < budgetMs);
    assert.ok(contentReads > 0);
    assert.equal(message, null);
  } finally {
    restore();
  }
});

test('session start aborts the real pending context request through the shared deadline', async () => {
  const originalRegister = lib.registerProjectIdentityV2;
  const originalRequestPost = lib.requestPost;
  const originalFetch = globalThis.fetch;
  const originalURL = process.env.ENGRAM_URL;
  const originalToken = process.env.ENGRAM_TOKEN;
  const originalAbortController = globalThis.AbortController;
  const controllers = [];
  let contextFetchSignal;
  let resolveContextFetch;
  let contextFetchStarted;
  const started = new Promise((resolve) => { contextFetchStarted = resolve; });
  const restore = installConfiguredStubs(async () => {
    throw new Error('the frozen requestPost transport must handle this request');
  });
  lib.registerProjectIdentityV2 = originalRegister;
  lib.requestPost = originalRequestPost;
  globalThis.AbortController = class TrackingAbortController extends originalAbortController {
    constructor() {
      super();
      controllers.push(this);
    }
  };
  process.env.ENGRAM_URL = 'http://127.0.0.1:37777';
  process.env.ENGRAM_TOKEN = 'worker-secret-token';
  globalThis.fetch = (url, options) => {
    assert.equal(options.method, 'POST');
    if (String(url).endsWith('/api/context/inject')) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: 'OK',
        text: async () => JSON.stringify({ canonical_project: 'p2g_canonical' }),
      });
    }
    assert.match(String(url), /\/api\/context\/session-start$/);
    assert.ok(options.signal);
    contextFetchSignal = options.signal;
    const sessionDeadlineSignal = controllers[0]?.signal;
    assert.ok(sessionDeadlineSignal);
    const aborted = new Promise((resolve) => {
      contextFetchSignal.addEventListener('abort', resolve, { once: true });
    });
    const deadlineAborted = new Promise((resolve) => {
      sessionDeadlineSignal.onabort = resolve;
    });
    contextFetchStarted({ aborted, deadlineAborted });
    return new Promise((resolve) => { resolveContextFetch = resolve; });
  };
  try {
    const pendingMessage = sessionStartMessage({ cwd: process.cwd(), sessionId: 'pending-context' }, {}, 80);
    const { aborted, deadlineAborted } = await started;
    await Promise.all([aborted, deadlineAborted]);
    const message = await pendingMessage;
    assert.equal(message, null);
    assert.equal(contextFetchSignal.aborted, true);

    resolveContextFetch({
      ok: true,
      status: 200,
      statusText: 'OK',
      text: async () => JSON.stringify({ issues: [], rules: [], memories: [{ content: 'late context must not be returned' }] }),
    });
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(message, null);
  } finally {
    globalThis.fetch = originalFetch;
    globalThis.AbortController = originalAbortController;
    if (originalURL === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalURL;
    if (originalToken === undefined) delete process.env.ENGRAM_TOKEN;
    else process.env.ENGRAM_TOKEN = originalToken;
    restore();
  }
});

test('session start fails open when context fetch throws after registration', async () => {
  let contextRequests = 0;
  const restore = installConfiguredStubs(async (endpoint) => {
    assert.equal(endpoint, '/api/context/session-start');
    contextRequests += 1;
    throw new Error('context backend unavailable');
  });
  try {
    assert.equal(await sessionStartMessage({ cwd: process.cwd(), sessionId: 'context-throws' }, {}), null);
    assert.equal(contextRequests, 1);
  } finally {
    restore();
  }
});

test('session start clears its deadline without aborting successful context transport', async () => {
  const budgetMs = 80;
  const originalRegister = lib.registerProjectIdentityV2;
  const originalRequestPost = lib.requestPost;
  const originalFetch = globalThis.fetch;
  const originalURL = process.env.ENGRAM_URL;
  const originalToken = process.env.ENGRAM_TOKEN;
  const originalAbortController = globalThis.AbortController;
  const controllers = [];
  let contextFetchSignal;
  const restore = installConfiguredStubs(async () => {
    throw new Error('the frozen requestPost transport must handle this request');
  });
  lib.registerProjectIdentityV2 = originalRegister;
  lib.requestPost = originalRequestPost;
  globalThis.AbortController = class TrackingAbortController extends originalAbortController {
    constructor() {
      super();
      controllers.push(this);
    }
  };
  process.env.ENGRAM_URL = 'http://127.0.0.1:37777';
  process.env.ENGRAM_TOKEN = 'worker-secret-token';
  globalThis.fetch = (url, options) => {
    assert.equal(options.method, 'POST');
    if (String(url).endsWith('/api/context/inject')) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: 'OK',
        text: async () => JSON.stringify({ canonical_project: 'p2g_canonical' }),
      });
    }
    assert.match(String(url), /\/api\/context\/session-start$/);
    contextFetchSignal = options.signal;
    return Promise.resolve({
      ok: true,
      status: 200,
      statusText: 'OK',
      text: async () => JSON.stringify({ issues: [], rules: [], memories: [{ content: 'successful session context' }] }),
    });
  };
  try {
    const message = await sessionStartMessage({ cwd: process.cwd(), sessionId: 'successful-context' }, {}, budgetMs);
    assert.ok(contextFetchSignal);
    assert.equal(contextFetchSignal.aborted, false);
    assert.match(message.content, /successful session context/);

    await new Promise((resolve) => setTimeout(resolve, budgetMs + 20));
    assert.equal(controllers[0].signal.aborted, false);
    assert.equal(contextFetchSignal.aborted, false);
    assert.match(message.content, /successful session context/);
  } finally {
    globalThis.fetch = originalFetch;
    globalThis.AbortController = originalAbortController;
    if (originalURL === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalURL;
    if (originalToken === undefined) delete process.env.ENGRAM_TOKEN;
    else process.env.ENGRAM_TOKEN = originalToken;
    restore();
  }
});

test('missing configuration, identity failures, and request deadline errors fail open', async () => {
  const originalConfig = lib.resolveEngramRuntimeConfig;
  lib.resolveEngramRuntimeConfig = async () => ({ serverURL: '', token: '', quiet: false });
  try {
    assert.equal(await sessionStartMessage({ cwd: process.cwd(), sessionId: 'no-config' }, {}), null);
  } finally {
    lib.resolveEngramRuntimeConfig = originalConfig;
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

test('ambientMessage fails open within its shared budget when async config reading stalls', async (t) => {
  const configEnvKeys = [
    'ENGRAM_CONFIG_FILE', 'ENGRAM_DATA_DIR', 'CLAUDE_PLUGIN_DATA',
    'ENGRAM_URL', 'ENGRAM_SERVER_URL', 'CLAUDE_PLUGIN_OPTION_server_url',
    'CLAUDE_PLUGIN_OPTION_SERVER_URL', 'ENGRAM_CLAUDE_USERCONFIG_URL',
    'ENGRAM_TOKEN', 'CLAUDE_PLUGIN_OPTION_api_token', 'CLAUDE_PLUGIN_OPTION_API_TOKEN',
    'ENGRAM_CLAUDE_USERCONFIG_TOKEN', 'ENGRAM_QUIET', 'ENGRAM_QUIET_HOOKS',
    'CLAUDE_PLUGIN_OPTION_ENGRAM_QUIET', 'CLAUDE_PLUGIN_OPTION_engram_quiet',
    'CLAUDE_PLUGIN_OPTION_QUIET', 'CLAUDE_PLUGIN_OPTION_quiet',
  ];
  const previousEnv = Object.fromEntries(configEnvKeys.map((key) => [key, process.env[key]]));
  for (const key of configEnvKeys) delete process.env[key];
  const configFile = path.join(here, 'stalled-config.json');
  process.env.ENGRAM_CONFIG_FILE = configFile;
  t.after(() => {
    for (const key of configEnvKeys) {
      if (previousEnv[key] === undefined) delete process.env[key];
      else process.env[key] = previousEnv[key];
    }
  });

  const fsPromises = require('node:fs/promises');
  const originalReadFile = fsPromises.readFile;
  const resolveRuntimeConfig = lib.resolveEngramRuntimeConfig;
  const originalReadFileSync = fs.readFileSync;
  const originalExistsSync = fs.existsSync;
  fs.readFileSync = () => { throw new Error('ambient must not synchronously read config'); };
  fs.existsSync = () => { throw new Error('ambient must not synchronously stat config'); };
  t.after(() => {
    fs.readFileSync = originalReadFileSync;
    fs.existsSync = originalExistsSync;
  });
  let configReadSignal;
  let identityStarts = 0;
  let registrationStarts = 0;
  let requestStarts = 0;
  let readStarted;
  const startedRead = new Promise((resolve) => { readStarted = resolve; });
  fsPromises.readFile = (filePath, options) => {
    assert.equal(filePath, configFile);
    configReadSignal = options.signal;
    readStarted();
    return new Promise(() => { });
  };
  t.after(() => { fsPromises.readFile = originalReadFile; });

  const restore = installConfiguredStubs(async () => {
    requestStarts += 1;
    throw new Error('ambient request must not start after a stalled config read');
  });
  lib.resolveEngramRuntimeConfig = resolveRuntimeConfig;
  lib.resolveHookProjectIdentityV2 = async () => {
    identityStarts += 1;
    return identity;
  };
  lib.registerProjectIdentityV2 = async () => { registrationStarts += 1; };
  try {
    const startedAt = performance.now();
    const pending = ambientMessage({ cwd: process.cwd(), sessionId: 'stalled-config', prompt: 'Need memory now' }, {});
    await startedRead;
    const message = await pending;
    const elapsedMs = performance.now() - startedAt;
    assert.equal(message, null);
    assert.strictEqual(configReadSignal.aborted, true);
    assert.ok(elapsedMs >= 180 && elapsedMs < 275, `whole before-agent-start path took ${elapsedMs} ms`);
    assert.equal(identityStarts, 0);
    assert.equal(registrationStarts, 0);
    assert.equal(requestStarts, 0);
  } finally {
    restore();
  }
});
