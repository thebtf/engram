const assert = require('node:assert/strict');
const test = require('node:test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

test('loaded task worker survives retirement of its own package', async () => {
 const root = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-worker-retired-'));
 const packageRoot = path.join(root, 'package');
 fs.mkdirSync(packageRoot);
 try {
  for (const file of ['run-engram.js', 'ensure-binary.js', 'bootstrap-policy.js', 'client-instance.js', 'task-context-worker.js']) fs.copyFileSync(path.join(__dirname, file), path.join(packageRoot, file));
  const launcher = require(path.join(packageRoot, 'run-engram.js'));
  fs.writeFileSync(path.join(root, 'task-context'), 'process.stdin.resume();process.stdin.on("end",()=>process.stdout.write(JSON.stringify({schema:1,status:"not_ready",reason_code:"DAEMON_NOT_READY"})+"\\n"))');
  fs.rmSync(packageRoot, { recursive: true });
  let verified = 0;
  const result = await launcher.requestTaskContext({ cwd: root, session: 'retired', prompt: 'Inspect callback' }, {
   deadlineAt: Date.now() + 500, env: { SystemRoot: process.env.SystemRoot, ENGRAM_CLIENT_INSTANCE_ID: 'fixture-client' },
   resolve: async () => ({ path: process.execPath, target: {} }), roots: () => ({ objects: root }), hash: () => { verified++; return true; },
  });
  assert.deepEqual(result, { schema: 1, status: 'not_ready', reason_code: 'DAEMON_NOT_READY' });
  assert.equal(verified, 1);
 } finally { fs.rmSync(root, { recursive: true, force: true }); }
});
