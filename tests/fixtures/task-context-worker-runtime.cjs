'use strict';
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { Worker } = require('node:worker_threads');
const { requestTaskContext } = require('../../plugin/engram/scripts/run-engram.js');

async function main() {
 const binary = fs.realpathSync.native(process.execPath);
 const digest = crypto.createHash('sha256');
 for await (const bytes of fs.createReadStream(binary)) digest.update(bytes);
 const target = { sha256: digest.digest('hex'), size: fs.statSync(binary).size };
 const runtime = process.versions.bun ? 'bun' : 'node';
 const cases = ['normal', 'blocking-launch', 'expired-worker-start', 'cancel-during-launch', 'late-output', 'stdin-failure'];
 const selected = process.argv[2] ? [process.argv[2]] : cases;
 assert.ok(selected.every(mode => cases.includes(mode)), 'unknown standalone fixture case');
 for (const mode of selected) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-worker-runtime-'));
  const pidPath = path.join(root, 'child.pid');
  const productPath = path.join(root, 'product-started');
  const emittedPath = path.join(root, 'emitted');
  const controller = new AbortController();
  const response = JSON.stringify({ schema: 1, status: 'not_ready', reason_code: 'DAEMON_NOT_READY' }) + '\n';
  fs.writeFileSync(path.join(root, 'task-context'), `const fs=require('node:fs');fs.writeFileSync(${JSON.stringify(pidPath)},String(process.pid));let input='';process.stdin.on('data',b=>input+=b);process.stdin.on('end',()=>{if(!input)return;const r=JSON.parse(input);if(process.argv.at(-1)!=='--json'||r.schema!==1||!input.endsWith('\\n'))process.exit(2);if(Date.now()>=r.deadline_unix_ms)return;fs.writeFileSync(${JSON.stringify(productPath)},'started');const emit=()=>{fs.writeFileSync(${JSON.stringify(emittedPath)},'emitted');process.stdout.write(${JSON.stringify(response)});};${mode === 'normal' ? 'emit();' : 'setTimeout(emit,1200);'}});setTimeout(()=>process.exit(3),2000).unref();`);
  const wrapper = path.join(root, 'worker.cjs');
  let workerClosed, closedMessage, workerCode, children = 0, factoryCalls = 0;
  let beats = 0, maxGap = 0, previousBeat = performance.now();
  const heartbeat = setInterval(() => { const now = performance.now(); maxGap = Math.max(maxGap, now - previousBeat); previousBeat = now; beats++; }, 5);
  const watcher = mode === 'cancel-during-launch' ? fs.watch(root, (_event, file) => { if (String(file) === 'child.pid') controller.abort(); }) : null;
  const started = performance.now();
  try {
   const outcome = await requestTaskContext({ cwd: root, session: 'runtime-fixture', prompt: 'Inspect bounded native context' }, {
    signal: controller.signal, deadlineAt: Date.now() + 500, maxBytes: 8000,
    resolve: async () => ({ path: binary, target }),
    roots: () => ({ objects: path.dirname(binary) }),
    env: { PATH: process.env.PATH, SYSTEMROOT: process.env.SystemRoot || process.env.SYSTEMROOT, TEMP: os.tmpdir(), TMP: os.tmpdir() },
    createTaskContextWorker(entry, options) {
     factoryCalls++;
     assert.deepEqual(options.env, {});
     assert.equal(options.workerData.path, binary);
     assert.equal(options.workerData.cwd, root);
     let file = entry;
     if (mode === 'expired-worker-start') {
      fs.writeFileSync(wrapper, `Atomics.wait(new Int32Array(new SharedArrayBuffer(4)),0,0,650);require(${JSON.stringify(entry)});`);
      file = wrapper;
     } else if (mode === 'blocking-launch' || mode === 'cancel-during-launch') {
      fs.writeFileSync(wrapper, `const cp=require('node:child_process');const original=cp.execFile;cp.execFile=(...args)=>{const child=original(...args);Atomics.wait(new Int32Array(new SharedArrayBuffer(4)),0,0,650);return child;};require(${JSON.stringify(entry)});`);
      file = wrapper;
     } else if (mode === 'stdin-failure') {
      fs.writeFileSync(wrapper, `const cp=require('node:child_process');const original=cp.execFile;cp.execFile=(...args)=>{const child=original(...args);child.stdin.end=()=>{throw new Error('owned fixture pipe failure');};return child;};require(${JSON.stringify(entry)});`);
      file = wrapper;
     }
     const worker = new Worker(file, options);
     workerClosed = new Promise(resolve => worker.once('exit', code => { workerCode = code; resolve(); }));
     worker.on('message', message => { if (message.type === 'spawned') children++; if (message.type === 'closed') closedMessage = message; });
     return worker;
    },
   });
   const callbackMS = performance.now() - started;
   clearInterval(heartbeat);
   watcher?.close();
   assert.ok(callbackMS <= 500, `${mode} callback exceeded unchanged500ms: ${callbackMS}`);
   assert.equal(factoryCalls, 1, `${mode}: ${JSON.stringify(outcome)}`);
   if (mode === 'normal') assert.deepEqual(outcome, { schema: 1, status: 'not_ready', reason_code: 'DAEMON_NOT_READY' });
   else if (mode === 'cancel-during-launch') assert.deepEqual(outcome, { schema: 1, status: 'unavailable', reason_code: 'CANCELLED' });
   else if (mode === 'stdin-failure') assert.deepEqual(outcome, { schema: 1, status: 'unavailable', reason_code: 'CLIENT_UNAVAILABLE' });
   else assert.deepEqual(outcome, { schema: 1, status: 'deadline_exceeded', reason_code: 'DEADLINE_EXCEEDED' });
   if (mode !== 'normal') assert.ok(beats >= 5, `${mode} blocked the parent callback thread`);
   await workerClosed;
   assert.equal(workerCode, 0);
   assert.equal(closedMessage?.ok, mode === 'normal');
   assert.equal(children, mode === 'expired-worker-start' ? 0 : 1);
   if (mode === 'blocking-launch' || mode === 'expired-worker-start') assert.equal(fs.existsSync(productPath), false, 'expired/cancelled spawn sent native work');
   if (mode !== 'normal') assert.equal(fs.existsSync(emittedPath), false, 'late response producer survived cancellation');
   if (closedMessage.childPID) assert.throws(() => process.kill(closedMessage.childPID, 0), error => error.code === 'ESRCH', 'worker exited with a live owned child');
   console.log(JSON.stringify({ runtime, version: process.versions.bun || process.versions.node, mode, callback_ms: callbackMS, cleanup_ms: performance.now() - started, heartbeat_count: beats, max_heartbeat_gap_ms: maxGap, native_children: children, worker_exit: workerCode, real_hashFile: true, native_child_live_after_cleanup: false, not_loaded_OMP: true }));
  } finally {
   clearInterval(heartbeat);
   watcher?.close();
   controller.abort();
   await workerClosed;
   fs.rmSync(root, { recursive: true, force: true });
  }
 }
}
main().catch(error => { console.error(error.message); process.exitCode = 1; });
