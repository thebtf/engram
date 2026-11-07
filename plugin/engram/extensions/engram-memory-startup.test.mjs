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
  let cleanupRegisters = 0, cleanupUnregisters = 0, cleanupInvocations = 0, readiness = 'pending', factoryState = 'pending', preparationCalls = 0;
  let preparation, installing, preparedSignal, failure, held, afterPreparation, settled, afterTamper, afterRecovery, afterDisposal;
  const gate = deferred(), entered = deferred();
  const terminalMode = mode.startsWith('terminal-noise');
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
  const { BootstrapError, createPolicy } = require('../scripts/bootstrap-policy.js');
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
    preparationCalls++;
    if (terminalMode && preparationCalls > 1) {
      return realPrepare({ ...options, pluginRoot: root, pluginData: root, resolve: async () => { throw new BootstrapError('controlled recovery secret_fixture\n\x1b[31m'); } });
    }
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
          if (terminalMode) throw new BootstrapError('controlled startup secret_fixture https://private.invalid/?token=token_fixture\n\x1b[31m');
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
    assert.notEqual(readiness, 'pending', 'pending preparation must not resolve a project');
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
    }).then(context => { readiness = 'trusted'; return context; });
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
  const disposeRegistered = async () => {
    for (const [name, cleanup] of [...registrations]) {
      await cleanup();
      registrations.delete(name);
      cleanupInvocations++;
    }
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
    if (mode === 'cancelled') await disposeRegistered();
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
      assert.deepEqual(settled.effects, { ...held.effects, projectResolutions: mode === 'cancelled' ? 0 : 2 },
        'failed/corrupt cache may be checked read-only but must not execute, fetch or deliver; disposal must not even resolve');
      assert.equal(result, undefined);
      assert.equal(sent.length, 0);
      if (terminalMode) {
        await turn();
        assert.equal(preparationCalls, 2, 'missing trusted cache must trigger one bounded background recovery');
      }
      if (mode !== 'cancelled') {
        fs.writeFileSync(object, bytes);
        const recovered = await callbacks('after-verified-cache');
        afterRecovery = snapshot();
        assert.equal(afterRecovery.readiness, 'trusted', 'only successful real trusted resolution may admit enrichment');
        assert.deepEqual(afterRecovery.effects, { ...held.effects, projectResolutions: 4, projectExecutions: 2, backendRequests: 2 });
        assert.equal(sent.length, 1);
        assert.match(sent[0].message.content, /trusted startup fixture memory/);
        assert.deepEqual(sent[0].options, { deliverAs: 'nextTurn' });
        assert.equal(recovered.message.content, 'trusted startup fixture ambient');
        assert.deepEqual(executions.map((item, index) => item.timeout > 0 && item.timeout <= (index === 0 ? 5000 : 500)), [true, true]);
      }
    }
    assert.equal(cleanupRegisters, 1, 'actual default factory must register with the guaranteed-root host singleton');
    assert.equal(cleanupUnregisters, 0, 'settled readiness must not cancel its actual teardown registration');
    assert.equal(registrations.size, mode === 'cancelled' ? 0 : 1, 'retain cleanup until actual host teardown consumes it');
    const beforeDisposal = snapshot();
    await disposeRegistered();
    assert.equal(await callbacks('after-disposal'), undefined);
    afterDisposal = snapshot();
    assert.deepEqual(afterDisposal.effects, beforeDisposal.effects, 'actual teardown must block every later resolve/exec/backend request');
    assert.equal(afterDisposal.sent, beforeDisposal.sent, 'actual teardown must not deliver context');
    assert.equal(registrations.size, 0, 'actual host teardown must consume its registration');
    assert.equal(cleanupInvocations, 1);
    assert.equal(preparedSignal.aborted, true);
    assert.equal(unhandled.length, 0, 'background rejection must be handled');
    if (terminalMode && mode !== 'terminal-noise-unwritable') {
      const log = fs.readFileSync(path.join(root, 'logs', 'startup-env.log'), 'utf8');
      const diagnostics = log.split('\n').filter(line => line.includes('Trusted hook client readiness unavailable'));
      assert.equal(diagnostics.length, 2, 'retain bounded preparation and disposal diagnostics');
      assert.ok(diagnostics.every(line => line.includes(`stage=prepare code=BOOTSTRAP_ERROR plugin=${require('../package.json').version}`)));
      assert.doesNotMatch(log, /secret_fixture|token_fixture|private\.invalid|\x1b/);
      assert.ok(Buffer.byteLength(log) <= 2 * 128 * 1024, 'existing startup log retention bound applies');
    }
    // 7.0.8 replaces callback-admission blocking, never the trusted-execution guard.
    assert.equal(held.factoryState, 'fulfilled', 'default factory must finish while cold preparation is held');
    assert.deepEqual(held.callbacks, ['session_start', 'before_agent_start'], 'register before readiness, execute only after trust');
  } catch (error) { failure = error; }
  finally {
    gate.resolve();
    await preparation?.catch(() => { });
    await installing;
    await disposeRegistered();
    await turn();
    delete globalThis[Symbol.for(key)];
    process.removeListener('unhandledRejection', onUnhandled);
    console.log(`STARTUP_CAUSAL_RESULT ${JSON.stringify({
      mode, entry: fileURLToPath(entry), entrySHA256: crypto.createHash('sha256').update(fs.readFileSync(entry)).digest('hex'),
      held, afterPreparation, settled, afterTamper, afterRecovery, afterDisposal, executions, requests, effects, preparationCalls, cleanupRegisters, cleanupUnregisters, cleanupInvocations,
      cleanupActive: registrations.size, loaderLifetime: 'isolated-worker', preparationSettled: readiness !== 'pending',
      preparedSignalAborted: preparedSignal?.aborted ?? null, unhandled,
      failure: failure ? { name: failure.name, message: failure.message, code: failure.code ?? null } : null,
    })}`);
  }
  if (failure) throw failure;
}

async function exercisePackageLifetime(mode) {
  const require = createRequire(import.meta.url);
  const fixture = process.env.ENGRAM_STARTUP_FIXTURE_ROOT;
  const packageRoot = path.join(fixture, 'old-package');
  const pluginData = path.join(fixture, 'shared-data');
  const sourceRoot = fileURLToPath(new URL('../', import.meta.url));
  const files = ['package.json', '.omp-plugin/plugin.json', 'extensions/engram-memory.mjs',
    'hooks/lib.js', 'hooks/session-start.js', 'hooks/user-prompt.js',
    'scripts/run-engram.js', 'scripts/ensure-binary.js', 'scripts/bootstrap-policy.js', 'scripts/client-instance.js'];
  for (const name of files) {
    const destination = path.join(packageRoot, name);
    fs.mkdirSync(path.dirname(destination), { recursive: true });
    fs.copyFileSync(path.join(sourceRoot, name), destination);
  }
  fs.mkdirSync(pluginData, { recursive: true });
  process.env.PLUGIN_DATA = pluginData;
  const fixtureRequire = createRequire(path.join(packageRoot, 'package.json'));
  const { createPolicy, TARGET_ASSETS } = fixtureRequire('./scripts/bootstrap-policy.js');
  const { objectRoots, objectPath, hashFile, parsePolicy } = fixtureRequire('./scripts/ensure-binary.js');
  const bytes = Buffer.from('controlled trusted startup client, never executable');
  const sha256 = crypto.createHash('sha256').update(bytes).digest('hex');
  const version = fixtureRequire('./package.json').version;
  const targets = Object.fromEntries(Object.entries(TARGET_ASSETS).map(([key, asset]) => [key, { version, asset, size: bytes.length, sha256 }]));
  const rawPolicy = createPolicy(version, targets);
  const policyPath = path.join(packageRoot, 'bootstrap-targets.json');
  fs.writeFileSync(policyPath, JSON.stringify(rawPolicy));
  const policy = parsePolicy(JSON.stringify(rawPolicy), version);
  const roots = objectRoots(pluginData), object = objectPath(roots, policy.target.desired);
  fs.writeFileSync(object, bytes);
  const entryPath = path.join(packageRoot, 'extensions/engram-memory.mjs');
  const entrySHA256 = crypto.createHash('sha256').update(fs.readFileSync(entryPath)).digest('hex');
  const launcherSHA256 = crypto.createHash('sha256').update(fs.readFileSync(path.join(packageRoot, 'scripts/run-engram.js'))).digest('hex');
  const effects = { executions: 0, requests: 0, network: 0, unexpectedProcesses: 0 };
  const project = '11111111-1111-4111-8111-111111111111';
  for (const name of ['exec', 'execSync', 'execFileSync', 'spawn', 'spawnSync', 'fork']) {
    cp[name] = () => { effects.unexpectedProcesses++; throw new Error('unexpected native process'); };
  }
  cp.execFile = (file, args, options, callback) => {
    assert.equal(file, object);
    assert.equal(hashFile(file, policy.target.desired, roots.objects), true);
    assert.deepEqual(args, ['project', 'resolve', '--root', fixture, '--json']);
    effects.executions++;
    callback(null, JSON.stringify({ project_id: project }));
  };
  for (const [module, names] of [['node:http', ['request', 'get']], ['node:https', ['request', 'get']], ['node:net', ['connect', 'createConnection']], ['node:tls', ['connect']]]) {
    for (const name of names) require(module)[name] = () => { effects.network++; throw new Error('unexpected network'); };
  }
  globalThis.fetch = async (url, options) => {
    assert.equal(hashFile(object, policy.target.desired, roots.objects), true);
    assert.equal(JSON.parse(options.body).project, project);
    effects.requests++;
    return new Response(JSON.stringify(new URL(url).pathname === '/api/context/session-start'
      ? { memories: [{ content: 'trusted startup fixture memory' }] }
      : { additional_context: 'trusted startup fixture ambient' }));
  };
  const lib = fixtureRequire('./hooks/lib.js');
  const launcher = fixtureRequire('./scripts/run-engram.js');
  const realPrepare = launcher.prepareHookClient;
  const prepareEntered = deferred();
  let preparation;
  launcher.prepareHookClient = options => {
    preparation = realPrepare(options);
    prepareEntered.resolve();
    return preparation;
  };
  const realResolve = lib.resolveHookProjectContext;
  let verifyOnlyCalls = 0;
  lib.resolveHookProjectContext = (cwd, options) => {
    assert.equal(options.acquire, false, 'foreground project resolution must never acquire');
    verifyOnlyCalls++;
    return realResolve(cwd, options);
  };
  const realConfig = lib.resolveEngramRuntimeConfig;
  const gate = deferred(), entered = deferred();
  let configCalls = 0;
  lib.resolveEngramRuntimeConfig = async options => {
    if (++configCalls === 1) { entered.resolve(); await gate.promise; }
    return realConfig(options);
  };
  const handlers = new Map(), sent = [], registrations = new Map();
  const singleton = { register(name, callback) { registrations.set(name, callback); return () => registrations.delete(name); } };
  const key = 'engram.startup.fixture.lifetime';
  globalThis[Symbol.for(key)] = singleton;
  const hostURL = `data:text/javascript,${encodeURIComponent(`export const postmortem = globalThis[Symbol.for(${JSON.stringify(key)})];`)}`;
  register(`data:text/javascript,${encodeURIComponent(`export function resolve(specifier, context, nextResolve) {
    if (specifier === '@oh-my-pi/pi-utils') return { url: ${JSON.stringify(hostURL)}, shortCircuit: true };
    if (specifier.startsWith('@oh-my-pi/pi-utils/')) throw new Error('unregistered host subpath');
    return nextResolve(specifier, context);
  }`)}`);
  try {
    const actual = await import(new URL(`file:///${entryPath.replaceAll('\\', '/')}`).href);
    await actual.default({ on(name, callback) { handlers.set(name, callback); }, sendMessage(message) { sent.push(message); } });
    await entered.promise;
    assert.deepEqual([...handlers.keys()], ['session_start', 'before_agent_start']);
    assert.equal(hashFile(object, policy.target.desired, roots.objects), true);
    if (mode === 'retired') fs.rmSync(packageRoot, { recursive: true });
    gate.resolve();
    await prepareEntered.promise;
    const preparationResult = await preparation.then(() => 'ready', error => error.name);
    await turn();
    if (mode === 'corrupt') fs.writeFileSync(object, Buffer.alloc(bytes.length));
    if (mode === 'revoked') {
      rawPolicy.revoked_sha256.push(sha256);
      fs.writeFileSync(policyPath, JSON.stringify(rawPolicy));
    }
    await handlers.get('session_start')({ cwd: fixture, sessionId: 'lifetime-session' }, {});
    const ambient = await handlers.get('before_agent_start')({ cwd: fixture, sessionId: 'lifetime-session', prompt: 'fixture prompt' }, {});
    for (let i = 0; i < 4; i++) await turn();
    const logPath = path.join(pluginData, 'logs', 'startup-env.log');
    const log = fs.existsSync(logPath) ? fs.readFileSync(logPath, 'utf8') : '';
    console.log(`LIFETIME_RESULT ${JSON.stringify({
      mode, entrySHA256, launcherSHA256, clientSHA256: sha256,
      packageRetired: !fs.existsSync(packageRoot), trustedCAS: hashFile(object, policy.target.desired, roots.objects),
      effects, sent: sent.map(message => message.content), ambient: ambient?.message?.content ?? null,
      preparationResult, verifyOnlyCalls, bootstrapDiagnostic: log.includes('stage=prepare code=BOOTSTRAP_ERROR'), registrations: registrations.size
    })}`);
  } finally {
    gate.resolve();
    for (const cleanup of registrations.values()) await cleanup();
    delete globalThis[Symbol.for(key)];
  }
}

if (process.argv[2] === '--lifetime-worker') {
  try { await exercisePackageLifetime(process.argv[3]); }
  catch (error) { console.error(error.stack); process.exitCode = 1; }
} else
  if (process.argv[2] === '--startup-worker') {
    try { await exerciseDefaultFactory(process.argv[3]); }
    catch (error) { console.error(error.stack); process.exitCode = 1; }
  } else {
    test('loaded package lifetime retains trusted context after fixture retirement', t => {
      const buildRoot = process.env.ENGRAM_STARTUP_TEST_OUTPUT_ROOT || os.tmpdir();
      const root = fs.mkdtempSync(path.join(buildRoot, 'hook-lifetime-01a15ebb-'));
      const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => ['SYSTEMROOT', 'WINDIR', 'COMSPEC', 'PATH', 'PATHEXT', 'SYSTEMDRIVE'].includes(key.toUpperCase())));
      for (const key of ['HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'TMP', 'TEMP']) env[key] = root;
      Object.assign(env, { ENGRAM_URL: 'http://startup-fixture.invalid', ENGRAM_CONFIG_FILE: path.join(root, 'absent-config.json') });
      const results = {};
      try {
        for (const mode of ['intact', 'corrupt', 'revoked', 'retired']) {
          const fixture = path.join(root, mode);
          fs.mkdirSync(fixture);
          const result = cp.spawnSync(process.execPath, [fileURLToPath(import.meta.url), '--lifetime-worker', mode], {
            cwd: fileURLToPath(new URL('../../../', import.meta.url)), env: { ...env, ENGRAM_STARTUP_FIXTURE_ROOT: fixture },
            encoding: 'utf8', windowsHide: true, timeout: 10000, maxBuffer: 1024 * 1024,
          });
          assert.ifError(result.error);
          assert.equal(result.status, 0, result.stderr || result.stdout);
          assert.equal(result.stderr, '', 'no raw terminal output');
          assert.match(result.stdout, /^LIFETIME_RESULT [^\n]+\n$/);
          results[mode] = JSON.parse(result.stdout.slice('LIFETIME_RESULT '.length));
          t.diagnostic(JSON.stringify(results[mode]));
        }
      } finally {
        fs.rmSync(root, { recursive: true, force: true });
        t.diagnostic(`fixtureRemoved=${!fs.existsSync(root)}`);
      }
      for (const mode of ['intact', 'retired']) {
        assert.equal(results[mode].trustedCAS, true);
        assert.deepEqual(results[mode].effects, { executions: 2, requests: 2, network: 0, unexpectedProcesses: 0 });
        assert.match(results[mode].sent[0], /trusted startup fixture memory/, `${mode} must deliver useful same-instance context`);
        assert.equal(results[mode].ambient, 'trusted startup fixture ambient');
      }
      for (const mode of ['corrupt', 'revoked']) {
        assert.equal(results[mode].verifyOnlyCalls, 2);
        assert.equal(results[mode].effects.executions, 0);
        assert.equal(results[mode].effects.requests, 0);
        assert.equal(results[mode].effects.unexpectedProcesses, 0);
        assert.deepEqual(results[mode].sent, []);
        assert.equal(results[mode].ambient, null);
      }
      assert.equal(results.retired.packageRetired, true);
      assert.equal(results.intact.packageRetired, false);
    });
    for (const mode of ['trusted', 'warm', 'rejected', 'corrupt', 'cancelled', 'terminal-noise', 'terminal-noise-unwritable']) {
      test(mode.startsWith('terminal-noise') ? `OMP default factory emits no raw terminal output during failed readiness and recovery (${mode})` : `OMP default factory registers during held ${mode === 'warm' ? 'warm' : 'cold'} readiness and preserves ${mode} execution guards`, t => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'HOOK-TERMINAL-startup-'));
        const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => ['SYSTEMROOT', 'WINDIR', 'COMSPEC', 'PATH', 'PATHEXT', 'SYSTEMDRIVE'].includes(key.toUpperCase())));
        for (const key of ['HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'TMP', 'TEMP']) env[key] = root;
        Object.assign(env, { ENGRAM_STARTUP_FIXTURE_ROOT: root, ENGRAM_URL: 'http://startup-fixture.invalid', ENGRAM_CONFIG_FILE: path.join(root, 'absent-config.json'), ENGRAM_DATA_DIR: root });
        env.PLUGIN_DATA = root;
        if (mode === 'terminal-noise-unwritable') {
          env.PLUGIN_DATA = path.join(root, 'not-a-directory');
          fs.writeFileSync(env.PLUGIN_DATA, 'controlled diagnostic write failure');
        } else if (mode === 'terminal-noise') {
          fs.mkdirSync(path.join(root, 'logs'));
          fs.writeFileSync(path.join(root, 'logs', 'startup-env.log'), 'x\n'.repeat(128 * 1024));
        }
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
        if (mode.startsWith('terminal-noise')) {
          const lines = result.stdout.trimEnd().split('\n');
          assert.equal(lines.length, 1, 'only the worker result protocol may appear on stdout');
          assert.ok(lines[0].startsWith('STARTUP_CAUSAL_RESULT '));
          t.diagnostic(`worker stderr: ${JSON.stringify(result.stderr)}`);
          assert.equal(result.stderr, '', 'background readiness, recovery and disposal must not write raw terminal output');
        }
      });
    }
  }
