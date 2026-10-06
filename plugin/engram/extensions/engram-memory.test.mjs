import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import http from 'node:http';
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import engramMemory, {
  createEngramMemoryExtension,
  hiddenMessage,
} from './engram-memory.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const packagePath = path.resolve(here, '..', 'package.json');
const extensionPath = path.resolve(here, 'engram-memory.mjs');
const relayPath = path.resolve(here, 'legacy-relay.mjs');
const clientInstanceID = 'omp-extension-test-client';
const sessionCapability = Buffer.alloc(32, 7).toString('base64url');
const projectIdentityV3 = Object.freeze({
  version: 3,
  anchor_project_id: '33333333-3333-4333-8333-333333333333',
  name: 'engram',
  scope: 'repository',
  normalized_git_remotes: ['github.com/thebtf/engram'],
  legacy_identifiers: [],
  client_instance_id: clientInstanceID,
});

function withClientInstance(t, value = clientInstanceID) {
  const previous = process.env.ENGRAM_CLIENT_INSTANCE_ID;
  process.env.ENGRAM_CLIENT_INSTANCE_ID = value;
  t.after(() => {
    if (previous === undefined) delete process.env.ENGRAM_CLIENT_INSTANCE_ID;
    else process.env.ENGRAM_CLIENT_INSTANCE_ID = previous;
  });
}

const runtimeConfigEnvironmentKeys = Object.freeze([
  'ENGRAM_CONFIG_FILE', 'ENGRAM_DATA_DIR', 'CLAUDE_PLUGIN_DATA', 'PLUGIN_DATA',
  'ENGRAM_URL', 'ENGRAM_SERVER_URL', 'CLAUDE_PLUGIN_OPTION_server_url',
  'CLAUDE_PLUGIN_OPTION_SERVER_URL', 'ENGRAM_CLAUDE_USERCONFIG_URL',
  'ENGRAM_TOKEN', 'CLAUDE_PLUGIN_OPTION_api_token', 'CLAUDE_PLUGIN_OPTION_API_TOKEN',
  'ENGRAM_CLAUDE_USERCONFIG_TOKEN',
  'ENGRAM_CLIENT_INSTANCE_ID', 'CLAUDE_PLUGIN_OPTION_client_instance_id',
  'CLAUDE_PLUGIN_OPTION_CLIENT_INSTANCE_ID', 'ENGRAM_CLAUDE_USERCONFIG_CLIENT_INSTANCE_ID',
  'ENGRAM_QUIET', 'ENGRAM_QUIET_HOOKS', 'CLAUDE_PLUGIN_OPTION_ENGRAM_QUIET',
  'CLAUDE_PLUGIN_OPTION_engram_quiet', 'CLAUDE_PLUGIN_OPTION_QUIET',
  'CLAUDE_PLUGIN_OPTION_quiet',
]);
const clientIdentityEnvironmentKeys = Object.freeze([
  'ENGRAM_CLIENT_INSTANCE_ID', 'CLAUDE_PLUGIN_OPTION_client_instance_id',
  'CLAUDE_PLUGIN_OPTION_CLIENT_INSTANCE_ID', 'ENGRAM_CLAUDE_USERCONFIG_CLIENT_INSTANCE_ID',
]);

function withRuntimeConfig(t, config) {
  const previous = new Map(runtimeConfigEnvironmentKeys.map((key) => [key, process.env[key]]));
  for (const key of runtimeConfigEnvironmentKeys) delete process.env[key];
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-extension-config-'));
  process.env.ENGRAM_CONFIG_FILE = path.join(directory, 'config.json');
  fs.writeFileSync(process.env.ENGRAM_CONFIG_FILE, JSON.stringify(config));
  t.after(() => {
    fs.rmSync(directory, { recursive: true, force: true });
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  });
  return (values = {}) => {
    for (const key of clientIdentityEnvironmentKeys) delete process.env[key];
    Object.assign(process.env, values);
  };
}

function runtimeConfigExtension(options = {}) {
  return createEngramMemoryExtension({
    now: () => 1_000,
    resolveHookProjectContext(cwd, clientID) {
      return { ProjectDescriptorV3: { ...projectIdentityV3, client_instance_id: clientID } };
    },
    ...options,
  });
}

function relayResponse(route, extra = {}) {
  if (route === 'IDENTITY_REGISTRATION') {
    return {
      kind: 'OK',
      route,
      sessionCapability,
      canonicalProjectRef: 'canonical-project-ref',
      ...extra,
    };
  }
  if (route === 'SESSION_START_CONTEXT') {
    return {
      kind: 'OK',
      route,
      payload: { issues: [], rules: [], memories: [{ content: 'memory delivered through relay' }] },
      ...extra,
    };
  }
  return {
    kind: 'OK',
    route,
    additionalContext: '<engram-ambient>relay ambient context</engram-ambient>',
    ...extra,
  };
}

function scriptedRelay(responses, calls = []) {
  return {
    async call(route, body, deadlineUnixMs) {
      calls.push({ route, body, deadlineUnixMs });
      const next = responses.shift();
      if (next instanceof Error) throw next;
      return typeof next === 'function' ? next(route, body, deadlineUnixMs) : next;
    },
  };
}

function createExtension(options = {}) {
  return createEngramMemoryExtension({
    now: () => 1_000,
    isQuiet: () => false,
    resolveHookProjectContext: () => ({ ProjectDescriptorV3: projectIdentityV3 }),
    ...options,
  });
}

function adapterHarness(adapter = createExtension()) {
  const handlers = new Map();
  const sent = [];
  adapter.install({
    on(event, handler) { handlers.set(event, handler); },
    sendMessage(message, options) { sent.push({ message, options }); },
  });
  return { handlers, sent };
}

test('package ships the private relay helper without declaring it as an OMP entry', () => {
  const manifest = JSON.parse(fs.readFileSync(packagePath, 'utf8'));
  assert.deepEqual(manifest.omp.extensions, ['./extensions/engram-memory.mjs']);
  assert.equal(fs.existsSync(extensionPath), true);
  assert.equal(fs.existsSync(relayPath), true);
});

test('factory installs only the supported OMP wrappers', () => {
  const { handlers } = adapterHarness();
  assert.deepEqual([...handlers.keys()], ['session_start', 'before_agent_start']);
  assert.equal(typeof engramMemory, 'function');
});

test('standard config without hap_01b delivers the OMP session-start relay', async (t) => {
  withRuntimeConfig(t, {
    server_url: 'https://engram.example.test',
    api_token: 'engram_test_keycard',
    client_instance_id: 'config-install-alpha',
  });
  const calls = [];
  const extension = runtimeConfigExtension({
    relay: scriptedRelay([
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('SESSION_START_CONTEXT'),
    ], calls),
  });

  assert.ok(await extension.sessionStartMessage({ cwd: process.cwd(), sessionId: 'config-session' }, {}));
  assert.equal(calls[0].body.projectIdentityV3.client_instance_id, 'config-install-alpha');
  assert.deepEqual(calls.map(({ route }) => route), ['IDENTITY_REGISTRATION', 'SESSION_START_CONTEXT']);
});

test('tokenless config resolves a stable client identity and delivers server-selected memory', async (t) => {
  withRuntimeConfig(t, { server_url: 'http://127.0.0.1:37777' });
  const directory = path.dirname(process.env.ENGRAM_CONFIG_FILE);
  process.env.PLUGIN_DATA = directory;
  const calls = [];
  const extension = runtimeConfigExtension({
    relay: scriptedRelay([
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('SESSION_START_CONTEXT'),
    ], calls),
  });
  const message = await extension.sessionStartMessage({ cwd: process.cwd(), sessionId: 'tokenless-session' }, {});
  assert.match(message.content, /memory delivered through relay/);
  assert.deepEqual(calls.map(({ route }) => route), ['IDENTITY_REGISTRATION', 'SESSION_START_CONTEXT']);
  assert.equal(calls[0].body.projectIdentityV3.client_instance_id,
    fs.readFileSync(path.join(directory, 'client-instance-id'), 'utf8').trim());
});

test('OMP client identity follows canonical environment, option, then config precedence', async (t) => {
  const setClientIdentity = withRuntimeConfig(t, {
    server_url: 'https://engram.example.test',
    api_token: 'engram_test_keycard',
    client_instance_id: 'config-install-alpha',
  });

  for (const [values, expected] of [
    [{ ENGRAM_CLIENT_INSTANCE_ID: 'env-install-alpha', CLAUDE_PLUGIN_OPTION_client_instance_id: 'option-install-alpha' }, 'env-install-alpha'],
    [{ CLAUDE_PLUGIN_OPTION_client_instance_id: 'option-install-alpha' }, 'option-install-alpha'],
    [{}, 'config-install-alpha'],
  ]) {
    setClientIdentity(values);
    const calls = [];
    const extension = runtimeConfigExtension({
      relay: scriptedRelay([
        relayResponse('IDENTITY_REGISTRATION'),
        relayResponse('SESSION_START_CONTEXT'),
      ], calls),
    });
    assert.ok(await extension.sessionStartMessage({ cwd: process.cwd(), sessionId: `identity-${expected}` }, {}));
    assert.equal(calls[0].body.projectIdentityV3.client_instance_id, expected);
  }
});

test('quiet config suppresses OMP relay injection without env forwarding', async (t) => {
  const setClientIdentity = withRuntimeConfig(t, {
    server_url: 'https://engram.example.test',
    api_token: 'engram_test_keycard',
    client_instance_id: 'config-install-alpha',
    quiet: true,
  });
  setClientIdentity({ ENGRAM_CLIENT_INSTANCE_ID: 'env-install-alpha' });
  let calls = 0;
  const extension = runtimeConfigExtension({
    relay: { async call() { calls += 1; return relayResponse('IDENTITY_REGISTRATION'); } },
  });

  assert.equal(await extension.sessionStartMessage({ cwd: process.cwd(), sessionId: 'quiet-config' }, {}), null);
  assert.equal(await extension.ambientMessage({ cwd: process.cwd(), sessionId: 'quiet-config', prompt: 'prompt' }, {}), null);
  assert.equal(calls, 0);
});

test('session start performs relay identity then structured context with one absolute deadline', async (t) => {
  withClientInstance(t);
  const calls = [];
  const resolverCalls = [];
  const extension = createExtension({
    relay: scriptedRelay([
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('SESSION_START_CONTEXT'),
    ], calls),
    resolveHookProjectContext(cwd, clientID) {
      resolverCalls.push({ cwd, clientID });
      return { ProjectDescriptorV3: projectIdentityV3 };
    },
  });

  const message = await extension.sessionStartMessage({ cwd: process.cwd(), sessionId: 'session-1' }, {});
  assert.deepEqual(resolverCalls, [{ cwd: process.cwd(), clientID: clientInstanceID }]);
  assert.deepEqual(calls.map(({ route }) => route), ['IDENTITY_REGISTRATION', 'SESSION_START_CONTEXT']);
  assert.equal(calls[0].deadlineUnixMs, 6_000);
  assert.equal(calls[1].deadlineUnixMs, 6_000);
  assert.deepEqual(calls[0].body, {
    hostSessionRef: 'session-1',
    projectIdentityV3,
  });
  assert.deepEqual(calls[1].body, {
    hostSessionRef: 'session-1',
    sessionCapability,
  });
  assert.equal(message.customType, 'engram-memory');
  assert.equal(message.display, false);
  assert.equal(message.attribution, 'agent');
  assert.match(message.content, /memory delivered through relay/);
  assert.ok(message.content.length <= 12_000);
});

test('session start preserves the exact next-turn wrapper', async (t) => {
  withClientInstance(t);
  const adapter = createExtension({
    relay: scriptedRelay([
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('SESSION_START_CONTEXT'),
    ]),
  });
  const { handlers, sent } = adapterHarness(adapter);
  const result = await handlers.get('session_start')({ cwd: process.cwd(), sessionId: 'session-2' }, {});
  assert.equal(result, undefined);
  assert.deepEqual(sent, [{
    message: hiddenMessage(sent[0].message.content),
    options: { deliverAs: 'nextTurn' },
  }]);
});

test('ambient uses the session descriptor cache and refreshes identity without descriptor discovery', async (t) => {
  withClientInstance(t);
  const calls = [];
  let resolverCalls = 0;
  const extension = createExtension({
    relay: scriptedRelay([
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('SESSION_START_CONTEXT'),
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('AMBIENT_CANDIDATES'),
    ], calls),
    resolveHookProjectContext() {
      resolverCalls += 1;
      return { ProjectDescriptorV3: projectIdentityV3 };
    },
  });

  assert.ok(await extension.sessionStartMessage({ cwd: process.cwd(), sessionId: 'session-3' }, {}));
  const ambient = await extension.ambientMessage({ cwd: process.cwd(), sessionId: 'session-3', prompt: 'Need relay context' }, {});
  assert.equal(resolverCalls, 1);
  assert.deepEqual(calls.map(({ route }) => route), [
    'IDENTITY_REGISTRATION',
    'SESSION_START_CONTEXT',
    'IDENTITY_REGISTRATION',
    'AMBIENT_CANDIDATES',
  ]);
  assert.equal(calls[2].deadlineUnixMs, 1_500);
  assert.equal(calls[3].deadlineUnixMs, 1_500);
  assert.deepEqual(calls[3].body, {
    hostSessionRef: 'session-3',
    sessionCapability,
    queryText: 'Need relay context',
  });
  assert.deepEqual(ambient, hiddenMessage('<engram-ambient>relay ambient context</engram-ambient>'));
});

test('before-agent wrapper returns one message or undefined', async (t) => {
  withClientInstance(t);
  const extension = createExtension({
    relay: scriptedRelay([
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('SESSION_START_CONTEXT'),
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('AMBIENT_CANDIDATES'),
    ]),
  });
  const { handlers } = adapterHarness(extension);
  await handlers.get('session_start')({ cwd: process.cwd(), sessionId: 'session-4' }, {});
  const result = await handlers.get('before_agent_start')({ sessionId: 'session-4', prompt: 'prompt' }, { cwd: process.cwd() });
  assert.deepEqual(Object.keys(result), ['message']);
  assert.deepEqual(result.message, hiddenMessage('<engram-ambient>relay ambient context</engram-ambient>'));
  assert.equal(await handlers.get('before_agent_start')({ sessionId: 'not-cached', prompt: 'prompt' }, { cwd: process.cwd() }), undefined);
});

test('ambient safely omits when no valid cached descriptor exists', async (t) => {
  withClientInstance(t);
  let relayCalls = 0;
  let resolverCalls = 0;
  const extension = createExtension({
    relay: { async call() { relayCalls += 1; return relayResponse('IDENTITY_REGISTRATION'); } },
    resolveHookProjectContext() { resolverCalls += 1; return { ProjectDescriptorV3: projectIdentityV3 }; },
  });
  assert.equal(await extension.ambientMessage({ cwd: process.cwd(), sessionId: 'missing-session', prompt: 'prompt' }, {}), null);
  assert.equal(relayCalls, 0);
  assert.equal(resolverCalls, 0);
});

test('descriptor cache is bounded and evicts old sessions without synchronous rediscovery', async (t) => {
  withClientInstance(t);
  const calls = [];
  const extension = createExtension({
    descriptorCacheLimit: 1,
    relay: scriptedRelay([
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('SESSION_START_CONTEXT'),
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('SESSION_START_CONTEXT'),
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('AMBIENT_CANDIDATES'),
    ], calls),
  });
  assert.ok(await extension.sessionStartMessage({ cwd: process.cwd(), sessionId: 'old-session' }, {}));
  assert.ok(await extension.sessionStartMessage({ cwd: process.cwd(), sessionId: 'new-session' }, {}));
  assert.equal(await extension.ambientMessage({ cwd: process.cwd(), sessionId: 'old-session', prompt: 'prompt' }, {}), null);
  assert.ok(await extension.ambientMessage({ cwd: process.cwd(), sessionId: 'new-session', prompt: 'prompt' }, {}));
  assert.equal(calls.length, 6);
});

test('ambient invalidates a cached descriptor when the session workspace changes', async (t) => {
  withClientInstance(t);
  let relayCalls = 0;
  const extension = createExtension({
    relay: {
      async call(route) {
        relayCalls += 1;
        return relayResponse(route);
      },
    },
  });
  assert.ok(await extension.sessionStartMessage({ cwd: process.cwd(), sessionId: 'moved-session' }, {}));
  const moved = path.resolve(process.cwd(), 'different-workspace');
  assert.equal(await extension.ambientMessage({ cwd: moved, sessionId: 'moved-session', prompt: 'prompt' }, {}), null);
  assert.equal(relayCalls, 2, 'workspace mismatch must fail before identity refresh');
});

test('each callback omits relay failures, malformed non-OK results, and late responses', async (t) => {
  withClientInstance(t);
  const unavailable = createExtension({
    relay: scriptedRelay([{ kind: 'NO_DELIVERY', route: 'IDENTITY_REGISTRATION', reason: 'DIAL_FAILED' }]),
  });
  assert.equal(await unavailable.sessionStartMessage({ cwd: process.cwd(), sessionId: 'unavailable' }, {}), null);

  let now = 1_000;
  const late = createExtension({
    now: () => now,
    relay: {
      async call() {
        now = 1_201;
        return relayResponse('IDENTITY_REGISTRATION');
      },
    },
  });
  assert.equal(await late.sessionStartMessage({ cwd: process.cwd(), sessionId: 'late' }, {}, 100), null);

  const malformed = createExtension({
    relay: scriptedRelay([{ kind: 'OK', route: 'IDENTITY_REGISTRATION' }]),
  });
  assert.equal(await malformed.sessionStartMessage({ cwd: process.cwd(), sessionId: 'malformed' }, {}), null);
});

test('descriptor resolution that crosses the callback deadline leaves no ambient cache', async (t) => {
  withClientInstance(t);
  let now = 1_000;
  let relayCalls = 0;
  const extension = createExtension({
    now: () => now,
    resolveHookProjectContext() {
      now = 6_001;
      return { ProjectDescriptorV3: projectIdentityV3 };
    },
    relay: { async call() { relayCalls += 1; return relayResponse('IDENTITY_REGISTRATION'); } },
  });
  assert.equal(await extension.sessionStartMessage({ cwd: process.cwd(), sessionId: 'late-descriptor' }, {}), null);
  assert.equal(await extension.ambientMessage({ cwd: process.cwd(), sessionId: 'late-descriptor', prompt: 'prompt' }, {}), null);
  assert.equal(relayCalls, 0);
});

test('quiet, missing client identity, invalid descriptors, and bounded inputs safely omit', async (t) => {
  withClientInstance(t);
  let calls = 0;
  const quiet = createExtension({
    isQuiet: () => true,
    relay: { async call() { calls += 1; } },
  });
  assert.equal(await quiet.sessionStartMessage({ cwd: process.cwd(), sessionId: 'quiet' }, {}), null);
  assert.equal(calls, 0);

  const invalidDescriptor = createExtension({
    resolveHookProjectContext() { return { project: 'not-v3' }; },
    relay: { async call() { calls += 1; } },
  });
  assert.equal(await invalidDescriptor.sessionStartMessage({ cwd: process.cwd(), sessionId: 'invalid' }, {}), null);
  assert.equal(calls, 0);

  const oversize = createExtension({
    relay: scriptedRelay([
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('SESSION_START_CONTEXT'),
    ]),
  });
  assert.ok(await oversize.sessionStartMessage({ cwd: process.cwd(), sessionId: 'oversize' }, {}));
  assert.equal(await oversize.ambientMessage({
    cwd: process.cwd(),
    sessionId: 'oversize',
    prompt: 'x'.repeat(12_001),
  }, {}), null);
});

test('ambient applies the relay additional context directly and preserves the 12 KiB limit', async (t) => {
  withClientInstance(t);
  const accepted = 'a'.repeat(12_000);
  const extension = createExtension({
    relay: scriptedRelay([
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('SESSION_START_CONTEXT'),
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('AMBIENT_CANDIDATES', { additionalContext: accepted }),
    ]),
  });
  assert.ok(await extension.sessionStartMessage({ cwd: process.cwd(), sessionId: 'ambient-limit' }, {}));
  assert.deepEqual(await extension.ambientMessage({ cwd: process.cwd(), sessionId: 'ambient-limit', prompt: 'prompt' }, {}), hiddenMessage(accepted));

  const oversized = createExtension({
    relay: scriptedRelay([
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('SESSION_START_CONTEXT'),
      relayResponse('IDENTITY_REGISTRATION'),
      relayResponse('AMBIENT_CANDIDATES', { additionalContext: 'b'.repeat(12_001) }),
    ]),
  });
  assert.ok(await oversized.sessionStartMessage({ cwd: process.cwd(), sessionId: 'ambient-oversize' }, {}));
  assert.equal(await oversized.ambientMessage({ cwd: process.cwd(), sessionId: 'ambient-oversize', prompt: 'prompt' }, {}), null);
});

test('legacy OMP uses real hook transport, canonical V2 scope and no V3 fallback', async (t) => {
  const repo = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-omp-name-only-'));
  t.after(() => fs.rmSync(repo, { recursive: true, force: true }));
  execFileSync('git', ['init', '--quiet', repo]);
  execFileSync('git', ['-C', repo, 'remote', 'add', 'origin', 'https://github.com/thebtf/engram.git']);
  const marker = path.join(repo, '.engram-project');
  fs.writeFileSync(marker, '{"name":"engram"}\n');
  execFileSync('git', ['-C', repo, 'add', '.engram-project']);
  const calls = [];
  const canonical = 'p2g_00112233445566778899aabbccddeeff';
  let registrationStatus = 200;
  let malformedCanonical = false;
  const server = http.createServer(async (request, response) => {
    let raw = '';
    for await (const chunk of request) raw += chunk;
    calls.push({ endpoint: request.url, body: JSON.parse(raw) });
    response.setHeader('Content-Type', 'application/json');
    if (request.url === '/api/context/inject') {
      response.statusCode = registrationStatus;
      response.end(JSON.stringify({ canonical_project: malformedCanonical ? null : canonical }));
    } else if (request.url === '/api/context/session-start') {
      response.end(JSON.stringify({ memories: [{ content: 'existing scope memory' }], api_token: 'must-not-deliver', server_url: 'must-not-deliver' }));
    } else if (request.url === '/api/hooks/ambient-candidates') {
      response.end(JSON.stringify({ additional_context: 'existing scope ambient', api_token: 'must-not-deliver' }));
    } else {
      response.statusCode = 404;
      response.end('{}');
    }
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  withRuntimeConfig(t, { server_url: `http://127.0.0.1:${server.address().port}`, client_instance_id: clientInstanceID });
  const childProcess = createRequire(import.meta.url)('node:child_process');
  let gitCalls = 0;
  for (const key of ['execFile', 'execFileSync', 'execSync']) {
    const original = childProcess[key];
    childProcess[key] = function(...args) {
      if (args[0] === 'git' || /^git\s/.test(args[0])) gitCalls += 1;
      return original.apply(this, args);
    };
    t.after(() => { childProcess[key] = original; });
  }
  let relayCalls = 0;
  const extension = createEngramMemoryExtension({
    isQuiet: () => false,
    relay: { async call(route) { relayCalls += 1; return relayResponse(route); } },
  });
  const event = { cwd: repo, sessionId: 'legacy-omp-fixture' };
  const message = await extension.sessionStartMessage(event, {}, 15000);
  assert.match(message?.content || '', /existing scope memory/);
  assert.doesNotMatch(message.content, /api_token|server_url|must-not-deliver/);
  assert.equal(message.customType, 'engram-memory');
  assert.equal(relayCalls, 0);
  assert.equal(calls[0].body.project, '67e398f8');
  assert.equal(calls[0].body.project_identity.version, 2);
  assert.equal(calls[0].body.project_identity.relative_path, '');
  assert.equal(calls[0].body.identity_only, true);
  assert.equal(calls[0].body.project_descriptor, undefined);
  assert.equal(calls[1].body.project, canonical);
  assert.equal(calls[1].body.session_id, event.sessionId);
  const ambient = await extension.ambientMessage({ ...event, prompt: 'memory query' }, {});
  assert.equal(ambient?.content, 'existing scope ambient');
  assert.equal(calls[3].body.project, canonical);
  assert.equal(relayCalls, 0);
  execFileSync('git', ['-C', repo, 'remote', 'set-url', 'origin', 'https://fixture-user:fixture-password@github.com/thebtf/engram.git']);
  const privacyStart = calls.length;
  assert.ok(await extension.sessionStartMessage(event, {}, 15000));
  assert.equal(calls[privacyStart].body.project, '67e398f8', 'userinfo must be stripped before the V2 hash');
  assert.equal(calls[privacyStart].body.git_remote, 'https://github.com/thebtf/engram.git');
  assert.equal(calls[privacyStart].body.project_identity.git_remote, 'https://github.com/thebtf/engram.git');
  assert.doesNotMatch(JSON.stringify(calls.slice(privacyStart)), /fixture-user|fixture-password/);
  fs.writeFileSync(marker, Buffer.concat([Buffer.from('{"name":"'), Buffer.from([0xff]), Buffer.from('"}')]));
  const invalidStart = calls.length;
  assert.equal(await extension.sessionStartMessage(event, {}, 15000), null);
  assert.equal(calls.length, invalidStart, 'original invalid UTF-8 must fail before transport');
  fs.writeFileSync(marker, '{"name":"engram"}\n');
  execFileSync('git', ['-C', repo, 'remote', 'set-url', 'origin', 'https://github.com/thebtf/engram.git']);
  gitCalls = 0;
  assert.ok(await extension.sessionStartMessage(event, {}, 15000));
  assert.ok(gitCalls <= 9, `cold legacy classification repeated Git evidence: ${gitCalls}`);
  gitCalls = 0;
  const warmStarted = performance.now();
  assert.equal((await extension.ambientMessage({ ...event, prompt: 'warm memory query' }, {}))?.content, 'existing scope ambient');
  assert.equal(gitCalls, 0, 'warm legacy ambient must not perform Git round trips');
  assert.ok(performance.now() - warmStarted < 500, 'warm ambient exceeded its actual callback budget');
  for (const status of [401, 403, 503]) {
    registrationStatus = status;
    const before = calls.length;
    assert.equal(await extension.sessionStartMessage(event, {}, 15000), null);
    assert.equal(calls.length, before + 1, `HTTP ${status} must not fetch context or fall back`);
  }
  registrationStatus = 200;
  malformedCanonical = true;
  assert.equal(await extension.sessionStartMessage(event, {}, 15000), null);
  malformedCanonical = false;
  for (const raw of ['{}', '{"name":"engram","version":3}', '{"name":"engram","extra":true}', '{"version":3,"project_id":"bad","name":"engram","scope":"repository"}']) {
    fs.writeFileSync(marker, raw);
    const before = calls.length;
    assert.equal(await extension.sessionStartMessage(event, {}, 15000), null);
    assert.equal(calls.length, before);
  }
  fs.writeFileSync(marker, JSON.stringify({ version: 3, project_id: projectIdentityV3.anchor_project_id, name: 'engram', scope: 'repository' }));
  const beforeV3 = calls.length;
  assert.ok(await extension.sessionStartMessage(event, {}, 15000));
  assert.equal(relayCalls, 2);
  assert.equal(calls.length, beforeV3, 'V3 must never use normal hook credential transport');
  const refusing = createEngramMemoryExtension({ isQuiet: () => false, relay: { async call() { return { kind: 'NO_DELIVERY', reason: 'SERVER_UNAVAILABLE' }; } } });
  assert.equal(await refusing.sessionStartMessage(event, {}, 15000), null);
  assert.equal(calls.length, beforeV3);
  assert.equal(fs.existsSync(path.join(repo, '.engram-project-v2.json')), false);
});
