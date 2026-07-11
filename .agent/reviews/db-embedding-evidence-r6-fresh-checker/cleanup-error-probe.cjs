#!/usr/bin/env node
'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const repoRoot = path.resolve(
  spawnSync('git', ['rev-parse', '--show-toplevel'], {
    cwd: process.cwd(),
    encoding: 'utf8',
    windowsHide: true,
  }).stdout.trim(),
);
const replayPath = path.join(
  repoRoot,
  '.agent',
  'reports',
  'evidence',
  'production-ready',
  'db-embedding-stats-evidence-transport-r6',
  'verify-final-commit-replay.cjs',
);
const tempBase = path.resolve(os.tmpdir());

function cloneResidue() {
  return fs.readdirSync(tempBase, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && entry.name.startsWith('engram-r6c-'))
    .map((entry) => entry.name)
    .sort();
}

const source = fs.readFileSync(replayPath, 'utf8');
const marker = '    return execute(cloneRoot);';
assert.ok(source.includes(marker), 'canonical clone callback marker is missing');
const injected = source.replace(
  marker,
  "    throw new Error('checker-forced-canonical-execution-error');",
);
const tempScript = path.join(
  tempBase,
  `engram-r6-cleanup-${crypto.randomBytes(8).toString('hex')}.cjs`,
);
const before = cloneResidue();
let result;
try {
  fs.writeFileSync(tempScript, injected, 'utf8');
  result = spawnSync(process.execPath, [tempScript], {
    cwd: repoRoot,
    encoding: 'utf8',
    windowsHide: true,
    maxBuffer: 64 * 1024 * 1024,
  });
} finally {
  const resolved = path.resolve(tempScript);
  assert.ok(resolved.startsWith(`${tempBase}${path.sep}engram-r6-cleanup-`));
  fs.rmSync(resolved, { force: true });
}
const after = cloneResidue();
assert.notEqual(result.status, 0, 'forced execution error must fail');
assert.match(result.stderr, /checker-forced-canonical-execution-error/);
assert.deepEqual(after, before, 'canonical clone residue changed after forced error');

process.stdout.write(`${JSON.stringify({
  status: 'PASS',
  forced_child_exit_code: result.status,
  forced_error_observed: true,
  clone_residue_before: before,
  clone_residue_after: after,
  cleanup_on_error: true,
}, null, 2)}\n`);
