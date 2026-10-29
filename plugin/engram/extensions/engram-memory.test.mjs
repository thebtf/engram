import assert from 'node:assert/strict';
import test from 'node:test';
import { createEngramMemoryExtension } from './engram-memory.mjs';
import crypto from 'node:crypto';
import { EventEmitter } from 'node:events';
import fs from 'node:fs';
import { createRequire } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { Readable } from 'node:stream';

const require = createRequire(import.meta.url);
const { hashFile, objectPath, objectRoots, parsePolicy, resolveForLaunch, TARGET_ASSETS } = require('../scripts/ensure-binary.js');
const { resolveHookProjectContext } = require('../hooks/lib.js');
const { prepareHookClient } = require('../scripts/run-engram.js');
const { createPolicy } = require('../scripts/bootstrap-policy.js');

const projects = { '/session/a': '11111111-1111-4111-8111-111111111111', '/session/b': '22222222-2222-4222-8222-222222222222' };
function extension(overrides = {}) {
  const calls = [];
  const adapter = createEngramMemoryExtension({
    isQuiet: () => false,
    prepareHookClient: async () => { },
    resolveEngramRuntimeConfig: async () => ({ serverURL: 'http://fixture', token: '', quiet: false }),
    resolveHookProjectContext: async cwd => ({ Project: projects[cwd] }),
    requestPost: async (route, body) => {
      calls.push({ route, body });
      return route === '/api/context/session-start'
        ? { memories: [{ content: `known memory for ${body.project}` }] }
        : { additional_context: `ambient for ${body.project}` };
    },
    ...overrides,
  });
  return { adapter, calls };
}

test('OMP automatic callbacks use current root and one UUID without identity registration', async () => {
  const { adapter, calls } = extension();
  const handlers = new Map();
  const sent = [];
  await adapter.install({ on: (name, fn) => handlers.set(name, fn), sendMessage: (message, options) => sent.push({ message, options }) });
  await new Promise(resolve => setImmediate(resolve));
  await Promise.all([
    handlers.get('session_start')({ cwd: '/session/a', sessionId: 'a' }, {}),
    handlers.get('session_start')({ cwd: '/session/b', sessionId: 'b' }, {}),
  ]);
  assert.equal(sent.length, 2);
  assert.match(sent[0].message.content, /known memory for 11111111/);
  assert.deepEqual(sent[0].options, { deliverAs: 'nextTurn' });
  const result = await handlers.get('before_agent_start')({ cwd: '/session/b', sessionId: 'a', prompt: 'new root' }, {});
  assert.equal(result.message.content, `ambient for ${projects['/session/b']}`);
  assert.deepEqual(calls.map(call => call.body.project), [projects['/session/a'], projects['/session/b'], projects['/session/b']]);
  assert.deepEqual(calls.map(call => call.route), ['/api/context/session-start', '/api/context/session-start', '/api/hooks/ambient-candidates']);
  assert.equal(calls.some(call => 'project_descriptor' in call.body || 'sessionCapability' in call.body), false);
});

test('OMP binding shares one background preparation across repeated installation and early callbacks', async t => {
  let finishPreparation, preparations = 0, configurations = 0, registrations = 0;
  const preparation = new Promise(resolve => { finishPreparation = resolve; });
  t.after(finishPreparation);
  const handlers = new Map(), sent = [];
  const { adapter, calls } = extension({
    resolveEngramRuntimeConfig: async () => { configurations++; return { serverURL: 'http://fixture', token: '', quiet: false }; },
    prepareHookClient: () => { preparations++; return preparation; },
  });
  const pi = { on: (name, handler) => { registrations++; handlers.set(name, handler); }, sendMessage: message => sent.push(message) };
  await Promise.all([adapter.install(pi), adapter.install(pi)]);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(preparations, 1);
  assert.equal(registrations, 2);
  await handlers.get('session_start')({ cwd: '/session/a', sessionId: 'early' }, {});
  assert.equal(await handlers.get('before_agent_start')({ cwd: '/session/a', sessionId: 'early', prompt: 'fixture' }, {}), undefined);
  assert.equal(configurations, 1, 'pending callbacks must not start their own configuration work');
  assert.equal(calls.length, 0);
  assert.equal(sent.length, 0);
  finishPreparation();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(sent.length, 0, 'readiness must not replay the early event');
  await handlers.get('session_start')({ cwd: '/session/a', sessionId: 'later' }, {});
  assert.equal(preparations, 1);
  assert.equal(calls.length, 1);
  assert.equal(sent.length, 1);
});

test('OMP missing context, quiet and resolution failure do not make scoped requests', async () => {
  const { adapter, calls } = extension();
  assert.equal(await adapter.sessionStartMessage({ sessionId: 'no-root' }, {}), null);
  assert.equal(await adapter.ambientMessage({ cwd: '/session/a', sessionId: 'a' }, {}), null);
  assert.equal(calls.length, 0);
  const quiet = extension({ isQuiet: () => true });
  assert.equal(await quiet.adapter.sessionStartMessage({ cwd: '/session/a', sessionId: 'a' }, {}), null);
  const failed = extension({ resolveHookProjectContext: async () => { throw new Error('PROJECT_IDENTITY_CONFLICT'); } });
  assert.equal(await failed.adapter.sessionStartMessage({ cwd: '/session/a', sessionId: 'a' }, {}), null);
  assert.equal(failed.calls.length, 0);
});

test('OMP install registers before cold readiness without executing the first short callback', async t => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 1000 });
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-readiness-'));
  const bytes = Buffer.from('trusted readiness fixture client body');
  const sha256 = crypto.createHash('sha256').update(bytes).digest('hex');
  const version = '7.0.6';
  const policy = parsePolicy(JSON.stringify({
    schema_version: 1, launcher_security_epoch: 1, package_version: version, daemon_compat_epoch: 1,
    targets: Object.fromEntries(Object.entries(TARGET_ASSETS).map(([key, asset]) => [key, {
      desired: { version, asset, size: bytes.length, sha256 }, predecessor: null,
    }])),
    revoked_sha256: [],
    build_contract: { go_version: '1.26.10', trimpath: true, buildvcs: false, client_cgo: false, daemon_version_ldflag: `v${version}` },
  }), version);
  const roots = objectRoots(root);
  const object = objectPath(roots, policy.target.desired);
  const handlers = new Map(), sent = [], executions = [], contextRequests = [];
  const diagnostics = [];
  let response, transferTimer, requests = 0, installState = 'pending', installError;
  let installing = Promise.resolve(), finishReadiness;
  const readinessSettled = new Promise(resolve => { finishReadiness = resolve; });
  t.after(async () => {
    clearTimeout(transferTimer);
    response?.destroy(new Error('owned readiness fixture closed'));
    await installing;
    fs.rmSync(root, { recursive: true, force: true });
  });
  const request = (url, _options, callback) => {
    assert.equal(url.href, `https://github.com/thebtf/engram/releases/download/v${version}/${policy.target.desired.asset}`);
    requests++;
    const req = new EventEmitter();
    req.end = () => {
      response = Object.assign(new Readable({ read() { } }), { statusCode: 200, headers: { 'content-length': String(bytes.length) } });
      response.once('close', () => req.emit('close'));
      callback(response);
      response.push(bytes.subarray(0, 4));
      transferTimer = setTimeout(() => { response.push(bytes.subarray(4)); response.push(null); }, 6000);
    };
    return req;
  };
  const resolve = options => resolveForLaunch({ ...options, pluginRoot: root, pluginData: root, policy, parserTarget: null, request });
  const { adapter } = extension({
    onDiagnostic: message => diagnostics.push(message),
    prepareHookClient: async options => {
      try {
        const prepared = await resolve(options);
        assert.equal(hashFile(prepared.path, prepared.target, roots.objects), true);
      } finally { finishReadiness(); }
    },
    resolveHookProjectContext: (cwd, options) => resolveHookProjectContext(cwd, {
      ...options, pluginRoot: root, pluginData: root, resolve,
      execFile(file, args, options, callback) {
        executions.push({ file, args, timeout: options.timeout });
        assert.equal(file, object);
        assert.equal(hashFile(file, policy.target.desired, roots.objects), true);
        callback(null, JSON.stringify({ project_id: projects[cwd] }));
      },
    }),
    requestPost: async (route, body, timeout) => {
      contextRequests.push({ route, body, timeout });
      return { memories: [{ content: `known memory for ${body.project}/${body.session_id}` }] };
    },
  });
  installing = Promise.resolve(adapter.install({
    on: (name, handler) => handlers.set(name, handler),
    sendMessage: (message, options) => sent.push({ message, options }),
  })).then(() => { installState = 'ready'; }, error => { installState = 'rejected'; installError = error; });
  await new Promise(resolve => setImmediate(resolve));
  t.diagnostic(JSON.stringify({ installState, trustedCAS: fs.existsSync(object), requests, executions: executions.length, contextRequests: contextRequests.length }));
  assert.equal(fs.existsSync(object), false);
  assert.equal(installState, 'ready', 'cold acquisition must not block foreground installation');
  assert.deepEqual([...handlers.keys()], ['session_start', 'before_agent_start']);
  await handlers.get('session_start')({ cwd: '/session/a', sessionId: 'early-skipped' }, {});
  t.mock.timers.tick(5001);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(installState, 'ready', 'background readiness must outlive the short callback without blocking startup');
  assert.equal(fs.existsSync(object), false);
  assert.equal(executions.length, 0);
  assert.equal(contextRequests.length, 0);
  assert.equal(sent.length, 0);
  t.mock.timers.tick(999);
  await installing;
  await readinessSettled;
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(sent.length, 0, 'readiness completion must not replay the skipped early event');
  if (installError) throw installError;
  assert.equal(hashFile(object, policy.target.desired, roots.objects), true);
  assert.equal(requests, 1, 'fixture body was acquired and verified once outside the short callback');
  await handlers.get('session_start')({ cwd: '/session/a', sessionId: 'cold-first' }, {});
  assert.deepEqual(contextRequests, [{ route: '/api/context/session-start', body: { project: projects['/session/a'], session_id: 'cold-first' }, timeout: 5000 }]);
  assert.deepEqual(executions, [{ file: object, args: ['project', 'resolve', '--root', '/session/a', '--json'], timeout: 5000 }]);
  assert.equal(requests, 1, 'first short callback must use verified CAS, not start another acquisition');
  assert.equal(sent.length, 1);
  assert.ok(sent[0].message.content.includes(`known memory for ${projects['/session/a']}/cold-first`));
  assert.deepEqual(sent[0].options, { deliverAs: 'nextTurn' });
  fs.writeFileSync(object, Buffer.alloc(bytes.length));
  await handlers.get('session_start')({ cwd: '/session/b', sessionId: 'after-tamper' }, {});
  assert.equal(requests, 1, 'a lost trusted client must not trigger a hook download');
  assert.equal(executions.length, 1, 'tampered bytes must not execute');
  assert.equal(contextRequests.length, 1, 'failed readiness must not fetch another project context');
  assert.equal(sent.length, 1, 'failed readiness must not replay old memory');
  assert.match(diagnostics.join('\n'), /readiness unavailable/);
});

test('OMP background readiness handles offline, hash and cooperative abort failures without enrichment', async () => {
  const bytes = Buffer.from('trusted readiness failure fixture'), version = '7.0.6';
  const sha256 = crypto.createHash('sha256').update(bytes).digest('hex');
  const targets = Object.fromEntries(Object.entries(TARGET_ASSETS).map(([key, asset]) => [key, { version, asset, size: bytes.length, sha256 }]));
  const policy = parsePolicy(JSON.stringify(createPolicy(version, targets)), version);
  for (const mode of ['offline', 'hash', 'abort-headers', 'abort-body']) {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-readiness-failure-'));
    const roots = objectRoots(root), handlers = new Map(), sent = [];
    let cleanup, unregistered = false, requestClosed = false, response, finishReadiness, installError;
    const readinessSettled = new Promise(resolve => { finishReadiness = resolve; });
    const diagnostics = [];
    const request = (_url, options, callback) => {
      if (mode === 'offline') throw new Error('offline signed=query_fixture_secret');
      const req = new EventEmitter();
      req.destroy = error => {
        if (requestClosed) return;
        requestClosed = true;
        response?.destroy(error);
        req.emit('error', error);
        req.emit('close');
      };
      const onAbort = () => req.destroy(new Error('fixture acquisition aborted'));
      options.signal?.addEventListener('abort', onAbort, { once: true });
      req.once('close', () => { requestClosed = true; options.signal?.removeEventListener('abort', onAbort); });
      req.end = () => {
        if (mode === 'abort-headers') return;
        response = Object.assign(mode === 'hash' ? Readable.from([Buffer.alloc(bytes.length)]) : new Readable({ read() { } }), {
          statusCode: 200, headers: { 'content-length': String(bytes.length) },
        });
        response.once('close', () => req.emit('close'));
        callback(response);
        if (mode === 'abort-body') response.push(bytes.subarray(0, 4));
      };
      return req;
    };
    const { adapter, calls } = extension({
      registerCleanup: (_name, callback) => { cleanup = callback; return () => { unregistered = true; }; },
      onDiagnostic: message => diagnostics.push(message),
      prepareHookClient: options => {
        const preparation = prepareHookClient({
          pluginRoot: root, pluginData: root, ...options,
          resolve: forwarded => resolveForLaunch({ ...forwarded, policy, request }),
        });
        preparation.then(finishReadiness, finishReadiness);
        return preparation;
      },
      resolveHookProjectContext: (cwd, options) => resolveHookProjectContext(cwd, {
        ...options, pluginRoot: root, pluginData: root,
        resolve: forwarded => resolveForLaunch({ ...forwarded, policy, parserTarget: null, request }),
        execFile() { assert.fail('unavailable or corrupt readiness fixture must never execute a client'); },
      }),
    });
    const installing = adapter.install({ on: (name, callback) => handlers.set(name, callback), sendMessage: message => sent.push(message) })
      .catch(error => { installError = error; });
    try {
      await new Promise(resolve => setImmediate(resolve));
      if (mode.startsWith('abort-')) await cleanup();
      await readinessSettled;
      await installing;
      await new Promise(resolve => setImmediate(resolve));
      assert.equal(installError, undefined, 'background failure must not reject foreground registration');
      assert.deepEqual([...handlers.keys()], ['session_start', 'before_agent_start']);
      await handlers.get('session_start')({ cwd: '/session/a', sessionId: mode }, {});
      assert.equal(await handlers.get('before_agent_start')({ cwd: '/session/a', sessionId: mode, prompt: 'fixture' }, {}), undefined);
      assert.match(diagnostics.join('\n'), /readiness unavailable/, 'genuine failed/corrupt/cancelled readiness must remain visible');
      assert.doesNotMatch(diagnostics.join('\n'), /query_fixture_secret|signed=/);
      assert.equal(calls.length, 0, mode);
      assert.equal(sent.length, 0, mode);
      assert.equal(unregistered, true, mode);
      if (mode.startsWith('abort-')) assert.equal(requestClosed, true, mode);
      assert.equal(fs.existsSync(objectPath(roots, policy.target.desired)), false);
      assert.deepEqual(fs.readdirSync(roots.staging), []);
    } finally { response?.destroy(); await installing; fs.rmSync(root, { recursive: true, force: true }); }
  }
});

test('OMP failed startup readiness recovers through verified local cache without another acquisition', async t => {
  for (const [eventName, budget] of [['session_start', 5000], ['before_agent_start', 500]]) {
    await t.test(eventName, async t => {
      t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 1000 });
      const root = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-readiness-recovery-'));
      t.after(() => fs.rmSync(root, { recursive: true, force: true }));
      const bytes = Buffer.from('verified local client after failed preparation');
      const version = require('../package.json').version;
      const sha256 = crypto.createHash('sha256').update(bytes).digest('hex');
      const targets = Object.fromEntries(Object.entries(TARGET_ASSETS).map(([key, asset]) => [key, { version, asset, size: bytes.length, sha256 }]));
      const policy = parsePolicy(JSON.stringify(createPolicy(version, targets)), version);
      const roots = objectRoots(root), object = objectPath(roots, policy.target.desired);
      const handlers = new Map(), sent = [], resolutions = [], executions = [], contextRequests = [];
      let preparations = 0, acquisitions = 0, quiet = false, finishSettlement;
      const settled = new Promise(resolve => { finishSettlement = resolve; });
      const resolve = options => resolveForLaunch({
        ...options, pluginRoot: root, pluginData: root, policy, parserTarget: null,
        request() { acquisitions++; throw new Error('synthetic acquisition unavailable'); },
      });
      const { adapter } = extension({
        isQuiet: () => quiet,
        onDiagnostic() { },
        registerCleanup: () => () => finishSettlement(),
        prepareHookClient: options => {
          preparations++;
          return prepareHookClient({ ...options, pluginRoot: root, pluginData: root, resolve });
        },
        resolveHookProjectContext: (cwd, options) => {
          resolutions.push({ cwd, acquire: options.acquire, timeoutMs: options.timeoutMs, deadlineAt: options.deadlineAt });
          assert.equal(options.acquire, false, 'callback recovery must remain read-only and never acquire');
          assert.equal(options.timeoutMs, budget);
          assert.equal(options.deadlineAt, Date.now() + budget);
          return resolveHookProjectContext(cwd, {
            ...options, pluginRoot: root, pluginData: root, resolve,
            execFile(file, args, options, callback) {
              executions.push({ file, args, timeout: options.timeout });
              assert.equal(file, object);
              assert.equal(hashFile(file, policy.target.desired, roots.objects), true);
              assert.equal(options.timeout, budget);
              callback(null, JSON.stringify({ project_id: projects[cwd] }));
            },
          });
        },
        requestPost: async (route, body, timeout, options) => {
          contextRequests.push({ route, body, timeout });
          assert.equal(timeout, budget);
          assert.equal(options.signal.aborted, false);
          return route === '/api/context/session-start'
            ? { memories: [{ content: `recovered memory for ${body.project}` }] }
            : { additional_context: `recovered memory for ${body.project}` };
        },
      });
      await adapter.install({ on: (name, handler) => handlers.set(name, handler), sendMessage: (message, options) => sent.push({ message, options }) });
      await settled;
      assert.equal(preparations, 1);
      assert.equal(acquisitions, 1, 'one failed startup acquisition is the baseline');
      const fire = (sessionId, cwd = '/session/a') => handlers.get(eventName)({ cwd, sessionId, prompt: 'restore backups' }, {});
      await fire('cache-missing');
      fs.writeFileSync(object, Buffer.alloc(bytes.length));
      await fire('cache-hash-invalid');
      assert.equal(executions.length, 0, 'missing or hash-invalid objects must not execute');
      assert.equal(contextRequests.length, 0, 'missing or hash-invalid cache must not reach backend HTTP');
      assert.equal(sent.length, 0, 'untrusted cache must not deliver hidden context');
      fs.writeFileSync(object, bytes);
      quiet = true;
      const resolutionsBeforeQuiet = resolutions.length;
      await fire('quiet-valid-cache');
      assert.equal(resolutions.length, resolutionsBeforeQuiet, 'quiet callbacks must not attempt recovery');
      assert.equal(contextRequests.length, 0);
      quiet = false;
      const result = await fire('verified-cache-now-present');
      const delivered = eventName === 'session_start' ? sent.at(-1)?.message : result?.message;
      assert.ok(delivered, 'same failed binding must deliver on the next qualified callback once the local cache verifies');
      assert.equal(delivered.customType, 'engram-memory');
      assert.equal(delivered.display, false);
      assert.ok(delivered.content.includes(`recovered memory for ${projects['/session/a']}`));
      assert.equal(executions.length, 1);
      assert.ok(resolutions.length >= 3, 'missing, invalid and valid callbacks must each use the trusted resolver');
      assert.equal(contextRequests.length, 1);
      if (eventName === 'session_start') assert.deepEqual(sent[0].options, { deliverAs: 'nextTurn' });
      fs.writeFileSync(object, Buffer.alloc(bytes.length));
      const afterTamper = await fire('invalid-after-recovery', '/session/b');
      assert.equal(afterTamper, undefined);
      assert.equal(executions.length, 1, 'recovery must not bypass later final integrity verification');
      assert.equal(contextRequests.length, 1, 'tampered cache must not fetch another project context');
      assert.equal(preparations, 1, 'no new background readiness job or foreground prepare is allowed');
      assert.equal(acquisitions, 1, 'callback recovery must not initiate a network download');
    });
  }
});

test('OMP first readiness failure diagnostic retains safe stage code and plugin version', async () => {
  const version = require('../package.json').version;
  const diagnostics = [], handlers = new Map();
  let finishSettlement;
  const settled = new Promise(resolve => { finishSettlement = resolve; });
  const rawFailure = Object.assign(new Error('private_path_fixture https://private-fixture.invalid/?token=token_fixture secret_fixture'), { code: 'HOOK_CLIENT_UNAVAILABLE' });
  const { adapter, calls } = extension({
    onDiagnostic: message => diagnostics.push(message),
    registerCleanup: () => () => finishSettlement(),
    prepareHookClient: async () => { throw rawFailure; },
    resolveHookProjectContext: async () => { throw new Error('later_resolver_error_fixture'); },
  });
  await adapter.install({ on: (name, handler) => handlers.set(name, handler), sendMessage() { throw new Error('failed readiness must not send'); } });
  await settled;
  assert.ok(diagnostics.length > 0, 'the first startup failure must remain diagnosable');
  const first = diagnostics.join('\n');
  await handlers.get('session_start')({ cwd: '/session/a', sessionId: 'diagnostic-later' }, {});
  const later = diagnostics.join('\n');
  for (const message of [first, later]) {
    assert.match(message, /(?:stage[=: ]+prepare|prepareHookClient)/, 'diagnostic must identify the failed preparation stage');
    assert.ok(message.includes('HOOK_CLIENT_UNAVAILABLE'), 'retain a stable safe code, not the discarded exception text');
    assert.ok(message.includes(version), 'diagnostic must identify the loaded plugin package version');
    assert.doesNotMatch(message, /private_path_fixture|private-fixture|token_fixture|secret_fixture|later_resolver_error_fixture|https?:\/\//);
  }
  assert.equal(calls.length, 0);
  for (let i = 0; i < 20; i++) {
    await handlers.get('session_start')({ cwd: '/session/a', sessionId: `diagnostic-repeat-${i}` }, {});
  }
  assert.ok(diagnostics.length <= 3, 'retained failure must not produce an unbounded callback warning stream');
});

test('OMP configuration failure is diagnosed safely and can recover without preparation', async () => {
  let failConfig = true, preparations = 0, finishSettlement;
  const settled = new Promise(resolve => { finishSettlement = resolve; });
  const handlers = new Map(), diagnostics = [], sent = [];
  const { adapter, calls } = extension({
    resolveEngramRuntimeConfig: async () => {
      if (failConfig) throw Object.assign(new Error('config_private_fixture https://config-fixture.invalid/?token=secret_fixture'), { code: 'https://untrusted-code.invalid' });
      return { serverURL: 'http://fixture', token: '', quiet: false };
    },
    prepareHookClient: async () => { preparations++; },
    registerCleanup: () => () => finishSettlement(),
    onDiagnostic: message => diagnostics.push(message),
  });
  await adapter.install({ on: (name, handler) => handlers.set(name, handler), sendMessage: message => sent.push(message) });
  await settled;
  assert.match(diagnostics.join('\n'), /stage[=: ]+config/);
  assert.match(diagnostics.join('\n'), /code[=: ]+UNKNOWN/);
  assert.doesNotMatch(diagnostics.join('\n'), /config_private_fixture|config-fixture|secret_fixture|untrusted-code|https?:\/\//);
  failConfig = false;
  await handlers.get('session_start')({ cwd: '/session/a', sessionId: 'config-recovered' }, {});
  assert.equal(sent.length, 1, 'a recovered configuration must reach the existing trusted read-only resolver');
  assert.equal(calls.length, 1);
  assert.equal(preparations, 0, 'callback recovery must not invoke prepare even when startup config failed');
});

test('OMP disposed background readiness cannot recover through a later resolvable context', async () => {
  let cleanup, finishSettlement, resolutions = 0;
  const settled = new Promise(resolve => { finishSettlement = resolve; });
  const handlers = new Map(), sent = [];
  const { adapter, calls } = extension({
    prepareHookClient: options => new Promise((_, reject) => {
      options.signal.addEventListener('abort', () => reject(Object.assign(new Error('fixture cancelled'), { code: 'ABORT_ERR' })), { once: true });
    }),
    registerCleanup: (_name, callback) => { cleanup = callback; return () => finishSettlement(); },
    resolveHookProjectContext: async () => { resolutions++; return { Project: projects['/session/a'] }; },
    onDiagnostic() { },
  });
  await adapter.install({ on: (name, handler) => handlers.set(name, handler), sendMessage: message => sent.push(message) });
  await new Promise(resolve => setImmediate(resolve));
  await cleanup();
  await settled;
  await handlers.get('session_start')({ cwd: '/session/a', sessionId: 'disposed-later' }, {});
  assert.equal(await handlers.get('before_agent_start')({ cwd: '/session/a', sessionId: 'disposed-later', prompt: 'fixture' }, {}), undefined);
  assert.equal(resolutions, 0, 'cancellation/disposal is not a recoverable acquisition failure');
  assert.equal(calls.length, 0);
  assert.equal(sent.length, 0);
});

test('OMP cleanup captured before settlement still closes failed readiness when invoked late', async () => {
  let cleanup, finishSettlement, resolutions = 0;
  const settled = new Promise(resolve => { finishSettlement = resolve; });
  const handlers = new Map();
  const { adapter, calls } = extension({
    prepareHookClient: async () => { throw new Error('synthetic startup failure'); },
    registerCleanup: (_name, callback) => { cleanup = callback; return () => finishSettlement(); },
    resolveHookProjectContext: async () => { resolutions++; return { Project: projects['/session/a'] }; },
    onDiagnostic() { },
  });
  await adapter.install({ on: (name, handler) => handlers.set(name, handler), sendMessage() { } });
  await settled;
  await cleanup();
  await handlers.get('session_start')({ cwd: '/session/a', sessionId: 'late-cleanup' }, {});
  assert.equal(resolutions, 0, 'late cleanup must close admission even after the preparation abort listener was removed');
  assert.equal(calls.length, 0);
});

for (const mode of ['unconfigured', 'config-quiet']) {
  test(`OMP skipped ${mode} callbacks stay silent until eligibility is enabled later`, async t => {
    let enabled = false, finishSettlement, acquisitions = 0, resolutions = 0, backendRequests = 0, messages = 0, unregistered = false;
    const settled = new Promise(resolve => { finishSettlement = resolve; });
    const handlers = new Map(), diagnostics = [];
    const { adapter } = extension({
      resolveEngramRuntimeConfig: async () => ({ serverURL: !enabled && mode === 'unconfigured' ? '' : 'http://fixture', quiet: !enabled && mode === 'config-quiet', token: '' }),
      prepareHookClient: async () => { acquisitions++; throw new Error('skipped binding must not acquire'); },
      resolveHookProjectContext: async () => { resolutions++; throw new Error('skipped binding must not resolve a project'); },
      requestPost: async () => { backendRequests++; throw new Error('skipped binding must not request backend context'); },
      registerCleanup: () => () => { unregistered = true; finishSettlement(); },
      onDiagnostic: message => diagnostics.push(message),
    });
    await adapter.install({ on: (name, callback) => handlers.set(name, callback), sendMessage() { messages++; } });
    await settled;
    assert.deepEqual([...handlers.keys()], ['session_start', 'before_agent_start']);
    assert.equal(await handlers.get('session_start')({ cwd: '/session/a', sessionId: mode }, {}), undefined);
    assert.equal(await handlers.get('before_agent_start')({ cwd: '/session/a', sessionId: mode, prompt: 'fixture' }, {}), undefined);
    const ineligibleDiagnostics = [...diagnostics];
    enabled = true;
    assert.equal(await handlers.get('session_start')({ cwd: '/session/a', sessionId: `${mode}-enabled` }, {}), undefined);
    assert.equal(await handlers.get('before_agent_start')({ cwd: '/session/a', sessionId: `${mode}-enabled`, prompt: 'fixture' }, {}), undefined);
    const enabledDiagnostics = diagnostics.slice(ineligibleDiagnostics.length);
    t.diagnostic(JSON.stringify({ mode, ineligibleDiagnostics, enabledDiagnostics, acquisitions, resolutions, backendRequests, messages, unregistered }));
    assert.equal(acquisitions, 0);
    assert.equal(resolutions, 0);
    assert.equal(backendRequests, 0);
    assert.equal(messages, 0);
    assert.equal(unregistered, true, 'background readiness has settled and released its cleanup registration');
    assert.equal(enabledDiagnostics.length, 2, 'each later eligible callback must explain the fresh-binding requirement');
    assert.ok(enabledDiagnostics.every(message => /fresh ordinary session/.test(message)));
    assert.deepEqual(ineligibleDiagnostics, [], 'deliberately skipped callbacks must not report a trusted-client failure');
  });
}

test('OMP quiet or unconfigured installation skips acquisition and requires a fresh session when enabled later', async () => {
  for (const mode of ['environment-quiet', 'config-quiet', 'unconfigured']) {
    let configured = false, acquisitions = 0;
    const diagnostics = [];
    const { adapter, calls } = extension({
      isQuiet: () => !configured && mode === 'environment-quiet',
      resolveEngramRuntimeConfig: async () => ({ serverURL: !configured && mode === 'unconfigured' ? '' : 'http://fixture', quiet: !configured && mode === 'config-quiet', token: '' }),
      prepareHookClient: async () => { acquisitions++; throw new Error('ineligible startup must not acquire'); },
      onDiagnostic: message => diagnostics.push(message),
    });
    await adapter.install({ on() { }, sendMessage() { throw new Error('ineligible startup must not send context'); } });
    assert.equal(acquisitions, 0, mode);
    configured = true;
    assert.equal(await adapter.sessionStartMessage({ cwd: '/session/a', sessionId: mode }, {}), null);
    assert.equal(calls.length, 0, mode);
    assert.equal(acquisitions, 0, mode);
    assert.match(diagnostics.join('\n'), /fresh ordinary session/);
  }
});

test('OMP background readiness budget includes config and never trusts late preparation', async t => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 1000 });
  let finishConfig, finishPreparation, preparedSignal;
  const config = new Promise(resolve => { finishConfig = resolve; });
  const preparation = new Promise(resolve => { finishPreparation = resolve; });
  const handlers = new Map();
  const { adapter, calls } = extension({
    resolveEngramRuntimeConfig: () => config,
    prepareHookClient: options => { preparedSignal = options.signal; return preparation; },
  });
  const installing = adapter.install({ on: (name, callback) => handlers.set(name, callback), sendMessage() { throw new Error('late readiness must not send'); } });
  let installState = 'pending';
  const installed = installing.then(() => { installState = 'ready'; }, () => { installState = 'rejected'; });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(installState, 'ready', 'held configuration must not block callback registration');
  t.mock.timers.tick(300000);
  finishConfig({ serverURL: 'http://fixture', token: '', quiet: false });
  await new Promise(resolve => setImmediate(resolve));
  t.mock.timers.tick(59999);
  assert.equal(preparedSignal.aborted, false);
  assert.deepEqual([...handlers.keys()], ['session_start', 'before_agent_start']);
  t.mock.timers.tick(1);
  await installed;
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(preparedSignal.aborted, true);
  finishPreparation();
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual([...handlers.keys()], ['session_start', 'before_agent_start']);
  assert.equal(await handlers.get('session_start')({ cwd: '/session/a', sessionId: 'expired' }, {}), undefined);
  assert.equal(await handlers.get('before_agent_start')({ cwd: '/session/a', sessionId: 'expired', prompt: 'fixture' }, {}), undefined);
  assert.equal(calls.length, 0);
});

test('OMP expired background configuration cannot start late acquisition or replay context', async t => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 1000 });
  let finishConfig, preparations = 0, unregistered = false;
  const config = new Promise(resolve => { finishConfig = resolve; });
  const diagnostics = [], handlers = new Map();
  const { adapter, calls } = extension({
    resolveEngramRuntimeConfig: () => config,
    prepareHookClient: async () => { preparations++; },
    registerCleanup: () => () => { unregistered = true; },
    onDiagnostic: message => diagnostics.push(message),
  });
  await adapter.install({ on: (name, callback) => handlers.set(name, callback), sendMessage() { throw new Error('expired config must not send'); } });
  t.mock.timers.tick(360000);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(unregistered, true);
  finishConfig({ serverURL: 'http://fixture', token: '', quiet: false });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(await handlers.get('session_start')({ cwd: '/session/a', sessionId: 'late-config' }, {}), undefined);
  assert.equal(await handlers.get('before_agent_start')({ cwd: '/session/a', sessionId: 'late-config', prompt: 'fixture' }, {}), undefined);
  assert.equal(preparations, 0);
  assert.equal(calls.length, 0);
  assert.match(diagnostics.join('\n'), /readiness unavailable/);
});

test('OMP context keeps its absolute deadline when wall clock advances without timer delivery', async t => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 1000 });
  const bytes = Buffer.from('trusted deadline edge client'), version = '7.0.6';
  const sha256 = crypto.createHash('sha256').update(bytes).digest('hex');
  const targets = Object.fromEntries(Object.entries(TARGET_ASSETS).map(([key, asset]) => [key, { version, asset, size: bytes.length, sha256 }]));
  const policy = parsePolicy(JSON.stringify(createPolicy(version, targets)), version);
  for (const [eventName, budget] of [['session_start', 5000], ['before_agent_start', 500]]) {
    for (const edge of ['config-return', 'resolver-entry']) {
      const root = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-deadline-edge-'));
      const roots = objectRoots(root), object = objectPath(roots, policy.target.desired);
      fs.writeFileSync(object, bytes);
      const handlers = new Map(), sent = [], resolutions = [], executions = [];
      let callbackPhase = false, deadline;
      const { adapter, calls } = extension({
        resolveEngramRuntimeConfig: async () => {
          if (callbackPhase && edge === 'config-return') t.mock.timers.setTime(deadline + 1);
          return { serverURL: 'http://fixture', quiet: false, token: '' };
        },
        resolveHookProjectContext: (cwd, options) => {
          resolutions.push({ timeoutMs: options.timeoutMs, deadlineAt: options.deadlineAt ?? null, signalAborted: options.signal.aborted });
          if (edge === 'resolver-entry') t.mock.timers.setTime(deadline + 1);
          return resolveHookProjectContext(cwd, {
            ...options, pluginRoot: root, pluginData: root,
            resolve: forwarded => resolveForLaunch({ ...forwarded, policy, request() { throw new Error('deadline fixture must not download'); } }),
            execFile(_file, _args, options, callback) {
              executions.push({ at: Date.now(), timeout: options.timeout, signalAborted: options.signal.aborted });
              callback(null, JSON.stringify({ project_id: projects[cwd] }));
            },
          });
        },
      });
      try {
        await adapter.install({ on: (name, handler) => handlers.set(name, handler), sendMessage: message => sent.push(message) });
        await new Promise(resolve => setImmediate(resolve));
        callbackPhase = true;
        deadline = Date.now() + budget;
        await handlers.get(eventName)({ cwd: '/session/a', sessionId: 'wall-clock-edge', prompt: 'current prompt' }, {});
        t.diagnostic(JSON.stringify({ eventName, edge, deadline, now: Date.now(), resolutions, executions, contextRequests: calls.length }));
        assert.equal(executions.length, 0, 'expired context must not enter native project exec even before the abort timer is delivered');
        assert.equal(calls.length, 0);
        assert.equal(sent.length, 0);
      } finally { fs.rmSync(root, { recursive: true, force: true }); }
    }
  }
});
