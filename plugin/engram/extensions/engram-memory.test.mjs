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

test('OMP install awaits trusted cold client before the first short context callback', async t => {
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
  let response, transferTimer, requests = 0, installState = 'pending', installError;
  let installing = Promise.resolve();
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
    prepareHookClient: async options => {
      const prepared = await resolve(options);
      assert.equal(hashFile(prepared.path, prepared.target, roots.objects), true);
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
  assert.equal(installState, 'pending', 'extension install completed while the exact trusted client CAS was absent');
  assert.equal(handlers.size, 0, 'context callbacks must not be registered before trusted readiness');
  t.mock.timers.tick(5001);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(installState, 'pending', 'readiness must outlive the short callback budget without admitting callbacks');
  assert.equal(fs.existsSync(object), false);
  assert.equal(executions.length, 0);
  assert.equal(contextRequests.length, 0);
  assert.equal(sent.length, 0);
  t.mock.timers.tick(999);
  await installing;
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
});
