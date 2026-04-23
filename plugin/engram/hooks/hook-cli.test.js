const assert = require('node:assert/strict');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const test = require('node:test');

const hooksDir = __dirname;

function runHook(scriptName, input) {
  const scriptPath = path.join(hooksDir, scriptName);
  const result = spawnSync(process.execPath, [scriptPath], {
    input,
    encoding: 'utf8',
    timeout: 2000,
    killSignal: 'SIGKILL',
    maxBuffer: 1024 * 1024,
    env: {
      ...process.env,
      ENGRAM_INTERNAL: '1',
    },
  });
  assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
  return result;
}

test('pre-compact hook emits JSON continue envelope', () => {
  const result = runHook('pre-compact.js', JSON.stringify({
    session_id: 'test-session',
    cwd: process.cwd(),
  }));

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, '{"continue":true}\n');
});

test('stop hook emits JSON continue envelope', () => {
  const result = runHook('stop.js', JSON.stringify({
    session_id: 'test-session',
    cwd: process.cwd(),
  }));

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, '{"continue":true}\n');
});

test('statusline hook emits static status text through shared wrapper', () => {
  const result = runHook('statusline.js', JSON.stringify({
    session_id: 'test-session',
    cwd: process.cwd(),
  }));

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, '[engram] ○ v5 cleanup in progress\n');
});
