// One typed native task-context operation. The parent never terminates this
// thread: even a cancelled blocking spawn must return so its own child is reaped.
const { parentPort, workerData } = require('node:worker_threads');
const { execFile } = require('node:child_process');
const path = require('node:path');

let child, timer, callbackDone = false, childClosed = false, finished = false;
let output, failed = false;
const data = workerData;
const valid = parentPort && data?.schema === 1 && path.isAbsolute(data.path ?? '') &&
 path.isAbsolute(data.cwd ?? '') && typeof data.input === 'string' && Buffer.byteLength(data.input) <= 20480 &&
 Number.isSafeInteger(data.deadlineAt) && Number.isSafeInteger(data.maxBytes) && data.maxBytes >= 1024 && data.maxBytes <= 32768 &&
 data.cancellation instanceof SharedArrayBuffer && data.cancellation.byteLength === 4;
const cancellation = valid ? new Int32Array(data.cancellation) : null;
const stopped = () => !valid || Atomics.load(cancellation, 0) !== 0 || Date.now() >= data.deadlineAt;

function finish() {
 if (finished || !callbackDone || !childClosed) return;
 finished = true;
 clearTimeout(timer);
 const ok = !failed && !stopped() && Buffer.isBuffer(output) && output.length <= data.maxBytes;
 // Transfer an exact-sized owned buffer, never a pooled backing store containing
 // unrelated bytes. No stderr, environment, locator or credential leaves here.
 const bytes = ok ? Uint8Array.from(output) : null;
 parentPort.postMessage({ type: 'closed', ok, bytes: bytes?.buffer, childPID: child?.pid ?? null }, bytes ? [bytes.buffer] : []);
 parentPort.close();
}

function cancel() {
 failed = true;
 if (child && !childClosed) {
  child.stdin?.destroy();
  child.kill();
 }
}

if (!valid) {
 parentPort?.close();
} else if (stopped()) {
 callbackDone = childClosed = true;
 finish();
} else {
 parentPort.on('message', message => { if (message === 'cancel') cancel(); });
 parentPort.once('close', cancel);
 timer = setTimeout(cancel, Math.max(1, data.deadlineAt - Date.now()));
 try {
  child = execFile(data.path, ['task-context', '--json'], {
   cwd: data.cwd, env: data.env, windowsHide: true, encoding: 'buffer',
   maxBuffer: data.maxBytes, timeout: Math.max(1, data.deadlineAt - Date.now()),
  }, (error, stdout) => {
   failed ||= !!error;
   output = stdout;
   callbackDone = true;
   finish();
  });
  child.once('spawn', () => parentPort.postMessage({ type: 'spawned', childPID: child.pid }));
  child.once('close', () => { childClosed = true; finish(); });
  child.stdin?.once('error', () => { });
  // A synchronous OS spawn cannot observe port messages. The shared cancellation
  // flag and unchanged absolute deadline are checked before sending any request.
  if (stopped()) cancel();
  else child.stdin.end(data.input);
 } catch {
  failed = true;
  callbackDone = childClosed = true;
  finish();
 }
}
