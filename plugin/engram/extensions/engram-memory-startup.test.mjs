import assert from 'node:assert/strict';
import test from 'node:test';
import cp from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import { createRequire, register } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const entry = new URL('./engram-memory.mjs', import.meta.url);
const turn = () => new Promise(resolve => setImmediate(resolve));
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

async function exerciseDefaultFactory(mode) {
  const require = createRequire(import.meta.url);
  const root = process.env.ENGRAM_STARTUP_FIXTURE_ROOT;
  assert.ok(root, 'worker requires its isolated fixture root');
  const effects = { projectResolutions: 0, projectExecutions: 0, backendRequests: 0, nativeProcesses: 0, network: 0, downloads: 0 };
  const registrations = new Map(), handlers = new Map(), sent = [], requests = [], executions = [], unhandled = [];
  let cleanupRegisters = 0, cleanupUnregisters = 0, readiness = 'pending', factoryState = 'pending';
  let preparation, installing, preparedSignal, failure, held, afterPreparation, settled, afterTamper;
  const gate = deferred(), entered = deferred();
  const onUnhandled = error => unhandled.push(String(error));
  process.on('unhandledRejection', onUnhandled);
  // Fence real process/network effects before loading the production CJS helpers.
  for (const name of ['exec', 'execFile', 'execSync', 'execFileSync', 'spawn', 'spawnSync', 'fork']) {
    cp[name] = () => { effects.nativeProcesses++; throw new Error(`unexpected native process: ${name}`); };
  }
  for (const [module, names] of [['node:http', ['request', 'get']], ['node:https', ['request', 'get']], ['node:net', ['connect', 'createConnection']], ['node:tls', ['connect']]]) {
    for (const name of names) require(module)[name] = () => { effects.network++; throw new Error('unexpected external network'); };
  }
  const lib = require('../hooks/lib.js');
  const launcher = require('../scripts/run-engram.js');
  const { hashFile, objectPath, objectRoots, parsePolicy, resolveForLaunch, TARGET_ASSETS } = require('../scripts/ensure-binary.js');
  const { createPolicy } = require('../scripts/bootstrap-policy.js');
  const bytes = Buffer.from('controlled trusted startup client, never executable');
  const sha256 = crypto.createHash('sha256').update(bytes).digest('hex');
  const version = '7.0.7';
  const targets = Object.fromEntries(Object.entries(TARGET_ASSETS).map(([key, asset]) => [key, { version, asset, size: bytes.length, sha256 }]));
  const policy = parsePolicy(JSON.stringify(createPolicy(version, targets)), version);
  const roots = objectRoots(root), object = objectPath(roots, policy.target.desired);
  if (mode === 'warm') fs.writeFileSync(object, bytes);
  const project = '11111111-1111-4111-8111-111111111111';
  const verify = options => resolveForLaunch({
    ...options, pluginRoot: root, pluginData: root, policy, parserTarget: null, acquire: false,
    request() { effects.downloads++; throw new Error('fixture must never download'); },
  });
  const realPrepare = launcher.prepareHookClient, realResolve = lib.resolveHookProjectContext;
  launcher.prepareHookClient = options => {
    assert.equal(preparation, undefined, 'one preparation per binding');
    assert.ok(options.timeoutMs > 0 && options.timeoutMs <= 360000);
    preparedSignal = options.signal;
    preparation = realPrepare({
      ...options, pluginRoot: root, pluginData: root,
      resolve: async forwarded => {
        const abort = () => gate.resolve();
        forwarded.signal.addEventListener('abort', abort, { once: true });
        entered.resolve();
        try {
          await gate.promise;
          if (mode === 'rejected') throw new Error('controlled offline readiness failure');
          if (forwarded.signal.aborted) throw new Error('controlled readiness abort');
          if (mode !== 'warm') fs.writeFileSync(object, mode === 'corrupt' ? Buffer.alloc(bytes.length) : bytes);
          return await verify(forwarded);
        } finally { forwarded.signal.removeEventListener('abort', abort); }
      },
    });
    preparation.then(() => { readiness = 'trusted'; }, () => { readiness = 'failed'; });
    return preparation;
  };
  lib.resolveHookProjectContext = (cwd, options) => {
    effects.projectResolutions++;
    assert.equal(readiness, 'trusted', 'pending or failed preparation must not resolve a project');
    assert.equal(options.acquire, false, 'short callbacks must remain verify-only');
    return realResolve(cwd, {
      ...options, pluginRoot: root, pluginData: root, resolve: verify,
      execFile(file, args, execOptions, callback) {
        effects.projectExecutions++;
        assert.equal(file, object);
        assert.equal(hashFile(file, policy.target.desired, roots.objects), true, 'only verified bytes may reach execution');
        assert.deepEqual(args, ['project', 'resolve', '--root', root, '--json']);
        executions.push({ args, timeout: execOptions.timeout });
        callback(null, JSON.stringify({ project_id: project }));
      },
    });
  };
  globalThis.fetch = async (url, options) => {
    url = new URL(url);
    effects.backendRequests++;
    assert.equal(readiness, 'trusted', 'pending or failed preparation must not fetch enrichment');
    assert.equal(hashFile(object, policy.target.desired, roots.objects), true);
    assert.equal(url.origin, 'http://startup-fixture.invalid');
    assert.equal(options.method, 'POST');
    const body = JSON.parse(options.body);
    assert.equal(body.project, project);
    assert.ok(['/api/context/session-start', '/api/hooks/ambient-candidates'].includes(url.pathname));
    requests.push({ route: url.pathname, body });
    return new Response(JSON.stringify(url.pathname === '/api/context/session-start'
      ? { memories: [{ content: 'trusted startup fixture memory' }] }
      : { additional_context: 'trusted startup fixture ambient' }));
  };
  const singleton = Object.freeze({
    register(name, callback) {
      assert.equal(name, 'engram-client-readiness');
      assert.equal(typeof callback, 'function');
      assert.equal(registrations.has(name), false);
      cleanupRegisters++;
      registrations.set(name, callback);
      return () => { assert.equal(registrations.delete(name), true); cleanupUnregisters++; };
    },
  });
  const key = 'engram.startup.fixture.postmortem';
  globalThis[Symbol.for(key)] = singleton;
  const hostURL = `data:text/javascript,${encodeURIComponent(`export const postmortem = globalThis[Symbol.for(${JSON.stringify(key)})];`)}`;
  // Node20-compatible root-only host loader lives only for this isolated worker.
  register(`data:text/javascript,${encodeURIComponent(`
    export function resolve(specifier, context, nextResolve) {
      if (specifier === '@oh-my-pi/pi-utils') return { url: ${JSON.stringify(hostURL)}, shortCircuit: true };
      if (specifier.startsWith('@oh-my-pi/pi-utils/')) throw new Error('unregistered host subpath; no filesystem fallback');
      return nextResolve(specifier, context);
    }
  `)}`);
  const snapshot = () => ({ factoryState, readiness, callbacks: [...handlers.keys()], trustedCAS: hashFile(object, policy.target.desired, roots.objects), effects: { ...effects }, sent: sent.length });
  const callbacks = async sessionId => {
    await handlers.get('session_start')?.({ cwd: root, sessionId }, {});
    return handlers.get('before_agent_start')?.({ cwd: root, sessionId, prompt: 'fixture prompt' }, {});
  };
  try {
    const host = await import('@oh-my-pi/pi-utils');
    assert.equal(host.postmortem, singleton);
    const actual = await import(entry.href);
    installing = actual.default({
      on(name, callback) { assert.equal(handlers.has(name), false); handlers.set(name, callback); },
      sendMessage(message, options) { sent.push({ message, options }); },
    }).then(() => { factoryState = 'fulfilled'; }, error => { factoryState = 'rejected'; return error; });
    await entered.promise;
    await turn();
    assert.equal(fs.existsSync(object), mode === 'warm', 'only the warm fixture starts with cached client bytes');
    assert.equal(await callbacks('while-held'), undefined);
    held = snapshot();
    assert.deepEqual(held.effects, { projectResolutions: 0, projectExecutions: 0, backendRequests: 0, nativeProcesses: 0, network: 0, downloads: 0 });
    assert.equal(held.sent, 0);
    if (mode === 'cancelled') await registrations.get('engram-client-readiness')();
    else gate.resolve();
    await preparation.catch(() => { });
    await installing;
    await turn();
    afterPreparation = snapshot();
    assert.deepEqual(afterPreparation.effects, held.effects, 'preparation completion must not replay a missed callback');
    assert.equal(afterPreparation.sent, 0, 'preparation completion must not inject hidden context');
    const result = await callbacks('after-settlement');
    settled = snapshot();
    if (mode === 'trusted' || mode === 'warm') {
      assert.equal(settled.readiness, 'trusted');
      assert.equal(settled.effects.projectResolutions, 2);
      assert.equal(settled.effects.projectExecutions, 2);
      assert.equal(settled.effects.backendRequests, 2);
      assert.equal(sent.length, 1);
      assert.match(sent[0].message.content, /trusted startup fixture memory/);
      assert.deepEqual(sent[0].options, { deliverAs: 'nextTurn' });
      assert.equal(result.message.content, 'trusted startup fixture ambient');
      assert.deepEqual(executions.map((item, index) => item.timeout > 0 && item.timeout <= (index === 0 ? 5000 : 500)), [true, true]);
      fs.writeFileSync(object, Buffer.alloc(bytes.length));
      assert.equal(await callbacks('after-tamper'), undefined);
      afterTamper = snapshot();
      assert.equal(afterTamper.effects.projectExecutions, 2, 'tampered bytes must not execute');
      assert.equal(afterTamper.effects.backendRequests, 2, 'tampering must not request more context');
      assert.equal(afterTamper.sent, 1, 'tampering must not replay enrichment');
    } else {
      assert.equal(settled.readiness, 'failed');
      assert.deepEqual(settled.effects, held.effects, 'failed/corrupt readiness must not enter the consumer path');
      assert.equal(result, undefined);
      assert.equal(sent.length, 0);
    }
    assert.equal(cleanupRegisters, 1, 'actual default factory must register with the guaranteed-root host singleton');
    assert.equal(cleanupUnregisters, 1, 'settled readiness must unregister its owned cleanup');
    assert.equal(registrations.size, 0, 'settled readiness must leave no owned cleanup');
    assert.equal(unhandled.length, 0, 'background rejection must be handled');
    // 7.0.8 replaces callback-admission blocking, never the trusted-execution guard.
    assert.equal(held.factoryState, 'fulfilled', 'default factory must finish while cold preparation is held');
    assert.deepEqual(held.callbacks, ['session_start', 'before_agent_start'], 'register before readiness, execute only after trust');
  } catch (error) { failure = error; }
  finally {
    gate.resolve();
    await preparation?.catch(() => { });
    await installing;
    for (const cleanup of [...registrations.values()]) await cleanup();
    await turn();
    delete globalThis[Symbol.for(key)];
    process.removeListener('unhandledRejection', onUnhandled);
    console.log(`STARTUP_CAUSAL_RESULT ${JSON.stringify({
      mode, entry: fileURLToPath(entry), entrySHA256: crypto.createHash('sha256').update(fs.readFileSync(entry)).digest('hex'),
      held, afterPreparation, settled, afterTamper, executions, requests, effects, cleanupRegisters, cleanupUnregisters,
      cleanupActive: registrations.size, loaderLifetime: 'isolated-worker', preparationSettled: readiness !== 'pending',
      preparedSignalAborted: preparedSignal?.aborted ?? null, unhandled,
      failure: failure ? { name: failure.name, message: failure.message, code: failure.code ?? null } : null,
    })}`);
  }
  if (failure) throw failure;
}

if (process.argv[2] === '--startup-worker') {
  try { await exerciseDefaultFactory(process.argv[3]); }
  catch (error) { console.error(error.stack); process.exitCode = 1; }
} else {
  for (const mode of ['trusted', 'warm', 'rejected', 'corrupt', 'cancelled']) {
    test(`OMP default factory registers during held ${mode === 'warm' ? 'warm' : 'cold'} readiness and preserves ${mode} execution guards`, t => {
      const root = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-default-startup-'));
      const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => ['SYSTEMROOT', 'WINDIR', 'COMSPEC', 'PATH', 'PATHEXT', 'SYSTEMDRIVE'].includes(key.toUpperCase())));
      for (const key of ['HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'TMP', 'TEMP']) env[key] = root;
      Object.assign(env, { ENGRAM_STARTUP_FIXTURE_ROOT: root, ENGRAM_URL: 'http://startup-fixture.invalid', ENGRAM_CONFIG_FILE: path.join(root, 'absent-config.json'), ENGRAM_DATA_DIR: root });
      let result;
      try {
        result = cp.spawnSync(process.execPath, [fileURLToPath(import.meta.url), '--startup-worker', mode], {
          cwd: fileURLToPath(new URL('../../../', import.meta.url)), env, encoding: 'utf8', windowsHide: true, timeout: 10000, maxBuffer: 1024 * 1024,
        });
      } finally { fs.rmSync(root, { recursive: true, force: true }); }
      t.diagnostic(JSON.stringify({ mode, nativeExit: result.status, signal: result.signal, error: result.error?.message ?? null, fixtureRemoved: !fs.existsSync(root) }));
      t.diagnostic(result.stdout);
      assert.ifError(result.error);
      assert.equal(result.status, 0, result.stderr || result.stdout);
    });
  }
}
