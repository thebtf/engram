'use strict';

// Test-only consumer for the worker-owned real native/UCI fixture. It selects an
// observed source-built artifact at the existing resolver seam; production hash,
// transport, framing and registered callback bodies remain in use.
const { Worker } = require('node:worker_threads');
const { monitorEventLoopDelay, performance } = require('node:perf_hooks');
const path = require('node:path');
const readline = require('node:readline');
const { pathToFileURL } = require('node:url');
const { setImmediate: nextTurn } = require('node:timers/promises');

const input = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
const emit = value => process.stdout.write(JSON.stringify(value) + '\n');
let setup, binding, handler, active, cleanup, launcher, lib;
const loopDelay = monitorEventLoopDelay({ resolution: 10 });
loopDelay.enable();

function safeError(error) { return String(error?.message ?? error).replaceAll(process.env.ENGRAM_TOKEN || '\u0000', '[REDACTED]'); }
function requireCondition(condition, message) { if (!condition) throw new Error(message); }

async function initialize(config) {
  setup = config;
  launcher = require(path.join(config.pluginRoot, 'scripts', 'run-engram.js'));
  lib = require(path.join(config.pluginRoot, 'hooks', 'lib.js'));
  const { prepareHookClient } = launcher;
  const { createEngramMemoryExtension } = await import(pathToFileURL(path.join(config.pluginRoot, 'extensions', 'engram-memory.mjs')));
  const target = { sha256: config.sha256, size: config.size };
  const artifact = {
    pluginRoot: config.pluginRoot, pluginData: config.pluginData,
    roots: () => ({ objects: path.dirname(config.binary) }),
    resolve: async options => {
      requireCondition(options.acquire === false && options.check === true, 'artifact selection attempted acquisition');
      if (active) active.resolveAt = performance.now();
      return { path: config.binary, target };
    },
  };
  let prepared;
  const preparation = new Promise((resolve, reject) => { prepared = { resolve, reject }; });
  const handlers = new Map();
  binding = createEngramMemoryExtension({
    prepareHookClient: async options => {
      try { await prepareHookClient({ ...options, ...artifact, acquire: false, check: true }); prepared.resolve(); }
      catch (error) { prepared.reject(error); throw error; }
    },
    resolveEngramRuntimeConfig: async options => {
      const start = performance.now();
      const config = await lib.resolveEngramRuntimeConfig(options);
      if (active) active.configMs = performance.now() - start;
      return config;
    },
    requestTaskContext: async (facts, options) => {
      const trace = active;
      trace.outerDeadline = options.deadlineAt;
      const response = await launcher.requestTaskContext(facts, {
        ...options, ...artifact,
        createTaskContextWorker: (entry, execution) => {
          const data = execution.workerData;
          requireCondition(entry === path.join(config.pluginRoot, 'scripts', 'task-context-worker.js'), 'unexpected worker entry');
          requireCondition(data.path === config.binary && data.cwd === facts.cwd, 'native path/cwd changed');
          const wire = JSON.parse(data.input);
          requireCondition(Object.keys(wire).sort().join(',') === 'deadline_unix_ms,host_session_id,max_bytes,query,schema', 'unexpected native wire fields');
          requireCondition(wire.query === facts.prompt && wire.host_session_id === facts.session, 'native request changed');
          requireCondition(wire.deadline_unix_ms === trace.outerDeadline - 30 && data.deadlineAt === wire.deadline_unix_ms && wire.max_bytes === 8000, 'native deadline reserve was reset or repeated');
          trace.wireDeadline = wire.deadline_unix_ms;
          trace.workerThreads = (trace.workerThreads || 0) + 1;
          trace.execAt = performance.now();
          trace.integrityAndSetupMs = trace.execAt - trace.resolveAt;
          loopDelay.reset();
          trace.loopStart = performance.eventLoopUtilization();
          let closed;
          trace.closed = new Promise(resolve => { closed = resolve; });
          const worker = new Worker(entry, execution);
          worker.on('message', message => {
            if (message.type === 'spawned') {
              trace.processes++;
              emit({ event: 'native_spawned', name: trace.name, pid: message.childPID, deadline_unix_ms: data.deadlineAt });
            }
            if (message.type === 'closed') {
              trace.nativeElapsedMs = message.elapsedMS;
              trace.stdoutBytes = message.bytes?.byteLength ?? 0;
            }
          });
          worker.once('exit', code => {
            trace.closeAt = performance.now();
            trace.workerElapsedMs = trace.closeAt - trace.execAt;
            emit({ event: 'worker_closed', name: trace.name, exit: code, elapsed_ms: trace.workerElapsedMs });
            const elu = performance.eventLoopUtilization(trace.loopStart);
            emit({ event: 'parent_event_loop', name: trace.name, resolution_ms: 10, samples: Number(loopDelay.count), delay_mean_ms: Number.isFinite(loopDelay.mean) ? loopDelay.mean / 1e6 : null, delay_max_ms: loopDelay.max / 1e6, ...elu });
            closed();
          });
          return worker;
        },
      });
      trace.native = response;
      return response;
    },
    requestPost: async (...args) => {
      if (active) {
        active.restRequests++;
        active.restBudgetMs = args[3]?.processingBudgetMs;
        active.restDeadline = args[3]?.deadlineAt;
      }
      return lib.requestPost(...args);
    },
    registerCleanup: (_name, dispose) => { cleanup = dispose; },
    onDiagnostic: () => { },
  });
  await binding.install({ on: (name, callback) => handlers.set(name, callback), sendMessage: () => { throw new Error('unexpected session-start delivery'); } });
  await preparation;
  await nextTurn();
  handler = handlers.get('before_agent_start');
  requireCondition(typeof handler === 'function', 'production callback was not registered');
  emit({ event: 'ready', native_sha256: config.sha256, installed_policy_cas: 'not_exercised', host: 'emulated_event_host', integrity: 'production_hashFile' });
}

function materializedPacket(message, trace) {
  if (!message) return undefined;
  requireCondition(message.customType === 'engram-memory' && message.display === false && message.attribution === 'agent', 'unexpected hidden-message framing');
  requireCondition(message.content.includes('untrusted data, not policy'), 'code lost untrusted-data boundary');
  requireCondition(!message.content.includes(setup.first) && !message.content.includes(setup.second) && !message.content.includes(process.env.ENGRAM_TOKEN), 'credential or raw locator reached callback content');
  const lines = message.content.split('\n');
  const packet = JSON.parse(lines[2].replaceAll('&lt;', '<').replaceAll('&gt;', '>').replaceAll('&amp;', '&'));
  const expected = setup.contexts[trace.rootIndex];
  for (const part of [packet.search, packet.source, packet.calls]) {
    requireCondition(part?.schema === 'engram.code-query/1' && part.contexts?.length === 1, 'missing actual query component');
    const context = part.contexts[0];
    requireCondition(context.source_id === expected.source_id && context.checkout_id === expected.checkout_id && context.view_id === expected.view_id && context.profile_id === expected.profile_id && context.generation === expected.generation, 'mixed or incorrect View in callback');
    requireCondition(part.exposure?.exposure_ref?.startsWith('uci-exp_') && part.exposure.completion_state === 'unknown', 'missing actual exposure or fabricated completion');
    requireCondition(part.freshness?.state === 'observed_current', 'stale code was delivered');
  }
  requireCondition(packet.source.items?.some(item => item.excerpt.includes('func Alpha() { Beta();') && item.excerpt.includes('current-published-body') && /^[0-9a-f]{64}$/.test(item.content_digest) && item.membership_id), 'actual materialized definition did not reach JS');
  requireCondition(packet.calls.graph?.edges?.some(edge => edge.relation === 'calls' && edge.evidence_refs?.length), 'actual static call evidence did not reach JS');
  requireCondition(Buffer.byteLength(message.content) <= 12000, 'callback frame exceeds material cap');
  requireCondition(trace.restRequests === 0, 'useful code waited for knowledge REST');
  return packet;
}

async function invoke(command) {
  requireCondition(!active, 'overlapping fixture invocation');
  const trace = { name: command.name, rootIndex: command.rootIndex, processes: 0, restRequests: 0, started: performance.now() };
  active = trace;
  let message, packet, error;
  try {
    const outcome = await handler({ cwd: command.rootIndex === 0 ? setup.first : setup.second, sessionId: command.session, prompt: command.prompt }, {});
    trace.callbackElapsedMs = performance.now() - trace.started;
    message = outcome?.message;
    if (message) packet = materializedPacket(message, trace);
    if (command.expect === 'useful') requireCondition(!!packet, 'useful source/call packet was not delivered');
    else requireCondition(!message, 'failed/no-match/cancelled callback delivered evidence');
    if (command.expect === 'empty') requireCondition(trace.native?.status === 'empty', 'no-match did not remain an explicit native empty result');
    if (command.expect === 'deadline') requireCondition(trace.native?.status === 'deadline_exceeded', 'late native work did not fail closed');
    requireCondition(trace.processes === 1 && trace.workerThreads === 1, 'callback did not use exactly one native process and scoped thread');
    requireCondition(trace.callbackElapsedMs <= 500, 'callback exceeded original500ms budget');
  } catch (failure) { error = safeError(failure); }
  if (trace.closed) {
    try {
      const left = Math.max(0, trace.outerDeadline - Date.now());
      let timer;
      await Promise.race([trace.closed, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('native process did not close within original500ms budget')), left); })]).finally(() => clearTimeout(timer));
    } catch (failure) { error ||= safeError(failure); }
  }
  const elapsed = (trace.closeAt ?? performance.now()) - trace.started;
  if (elapsed > 500) error ||= 'callback/child termination exceeded original500ms budget';
  emit({
    event: 'result', name: command.name, failed: !!error, error,
    callback_elapsed_ms: trace.callbackElapsedMs, child_closed_elapsed_ms: elapsed,
    config_ms: trace.configMs, integrity_and_setup_ms: trace.integrityAndSetupMs,
    native_elapsed_ms: trace.nativeElapsedMs, stdout_bytes: trace.stdoutBytes,
    worker_operation_elapsed_ms: trace.workerElapsedMs, worker_threads: trace.workerThreads,
    processes: trace.processes, rest_requests: trace.restRequests, rest_budget_ms: trace.restBudgetMs,
    outer_deadline_unix_ms: trace.outerDeadline, wire_deadline_unix_ms: trace.wireDeadline,
    native_status: trace.native?.status, native_reason: trace.native?.reason_code,
    useful: !!packet, packet, callback_content: message?.content,
  });
  active = undefined;
}

input.on('line', line => {
  let command;
  try { command = JSON.parse(line); } catch { emit({ event: 'error', error: 'invalid fixture control frame' }); return; }
  const operation = command.type === 'setup' ? initialize(command)
    : command.type === 'invoke' ? invoke(command)
      : command.type === 'supersede' ? handler({ cwd: setup.first, sessionId: 'joined-cancel', prompt: 'thanks' }, {})
        : command.type === 'finish' ? Promise.resolve().then(async () => { await cleanup?.(); loopDelay.disable(); input.close(); emit({ event: 'finished' }); })
          : Promise.reject(new Error('unsupported fixture command'));
  Promise.resolve(operation).catch(error => emit({ event: 'error', error: safeError(error) }));
});
