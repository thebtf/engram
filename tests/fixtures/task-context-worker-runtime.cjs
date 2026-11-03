'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { Worker } = require('node:worker_threads');

async function main() {
 const root = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-worker-runtime-'));
 const entry = path.resolve(__dirname, '../../plugin/engram/scripts/task-context-worker.js');
 const cancellation = new SharedArrayBuffer(4);
 const input = JSON.stringify({ schema: 1, host_session_id: 'runtime-fixture', query: 'Inspect bounded native context', deadline_unix_ms: Date.now() + 470, max_bytes: 8000 }) + '\n';
 const deadlineAt = JSON.parse(input).deadline_unix_ms;
 fs.writeFileSync(path.join(root, 'task-context'), `let input='';process.stdin.on('data',b=>input+=b);process.stdin.on('end',()=>{const r=JSON.parse(input);if(process.argv.at(-1)!=='--json'||r.schema!==1||!input.endsWith('\\n'))process.exit(2);process.stdout.write(JSON.stringify({schema:1,status:'not_ready',reason_code:'DAEMON_NOT_READY'})+'\\n');});setTimeout(()=>process.exit(3),2000).unref();`);
 let worker, closed, spawned = 0;
 try {
  const started = performance.now();
  worker = new Worker(entry, { env: {}, execArgv: [], stdout: true, stderr: true, workerData: { schema: 1, path: process.execPath, cwd: root, env: { ...process.env }, input, deadlineAt, maxBytes: 8000, cancellation } });
  worker.stdout.resume();
  worker.stderr.resume();
  worker.on('message', message => {
   if (message.type === 'spawned') spawned++;
   if (message.type === 'closed') closed = message;
  });
  const timer = setTimeout(() => { Atomics.store(new Int32Array(cancellation), 0, 1); worker.postMessage('cancel'); }, Math.max(1, deadlineAt - Date.now()));
  const code = await new Promise((resolve, reject) => { worker.once('error', reject); worker.once('exit', resolve); });
  clearTimeout(timer);
  assert.equal(code, 0);
  assert.equal(spawned, 1);
  assert.equal(closed?.ok, true);
  assert.ok(closed.bytes instanceof ArrayBuffer);
  const bytes = Buffer.from(closed.bytes);
  const expected = { schema: 1, status: 'not_ready', reason_code: 'DAEMON_NOT_READY' };
  assert.deepEqual(bytes, Buffer.from(JSON.stringify(expected) + '\n'));
  assert.deepEqual(JSON.parse(bytes.toString('utf8')), expected);
  console.log(JSON.stringify({ runtime: process.versions.bun ? 'bun' : 'node', version: process.versions.bun || process.versions.node, worker_exit: code, native_children: spawned, bytes: bytes.length, elapsed_ms: performance.now() - started, primitive: 'node:worker_threads+child_process.execFile+SAB+transfer+exit', not_loaded_OMP: true }));
 } finally {
  // A successful exit above proves the actual native child closed before this
  // thread. No terminate/unref shortcuts are used in the compatibility probe.
  fs.rmSync(root, { recursive: true, force: true });
 }
}
main().catch(error => { console.error(error.message); process.exitCode = 1; });
