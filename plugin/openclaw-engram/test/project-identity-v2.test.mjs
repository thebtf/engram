import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  PROJECT_IDENTITY_VERSION_V2,
  buildProjectIdentityV2,
  resolveIdentity,
  validateProjectIdentityV2,
} from '../dist/identity.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const vectorsPath = path.resolve(here, '../../../.agent/specs/security-project-identity/evidence/project-identity-v2-vectors.json');
const vectors = JSON.parse(fs.readFileSync(vectorsPath, 'utf8'));
const identityModulePath = path.resolve(here, '../dist/identity.js');

const openClawIdentityChild = String.raw`
const fs = require('node:fs');
const path = require('node:path');
const [modulePath, workspace, barrier, id] = process.argv.slice(1);
fs.writeFileSync(path.join(barrier, 'ready-' + id), '');
const wait = new Int32Array(new SharedArrayBuffer(4));
while (!fs.existsSync(path.join(barrier, 'go'))) Atomics.wait(wait, 0, 0, 5);
try {
  const { resolveIdentity } = require(modulePath);
  process.stdout.write(JSON.stringify({ ok: true, value: resolveIdentity('agent-' + id, workspace).projectIdentityV2 }));
} catch (error) {
  process.stdout.write(JSON.stringify({ ok: false, error: String(error && error.message || error) }));
}
`;

async function resolveOpenClawIdentityInChildProcesses(workspace, count) {
  const barrier = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-identity-v2-barrier-'));
  const children = [];
  try {
    for (let id = 0; id < count; id++) {
      const child = spawn(process.execPath, ['-e', openClawIdentityChild, identityModulePath, workspace, barrier, String(id)], {
        stdio: ['ignore', 'pipe', 'pipe'],
        windowsHide: true,
      });
      let stdout = '';
      let stderr = '';
      const result = new Promise((resolve, reject) => {
        child.stdout.setEncoding('utf8');
        child.stderr.setEncoding('utf8');
        child.stdout.on('data', (chunk) => { stdout += chunk; });
        child.stderr.on('data', (chunk) => { stderr += chunk; });
        child.once('error', reject);
        child.once('close', (code) => {
          if (code !== 0) {
            reject(new Error(`identity child ${id} exited ${code}: ${stderr}`));
            return;
          }
          try { resolve(JSON.parse(stdout)); } catch (error) {
            reject(new Error(`identity child ${id} returned invalid JSON: ${stdout}\n${stderr}`, { cause: error }));
          }
        });
      });
      children.push({ child, result });
    }

    const deadline = Date.now() + 15000;
    while (fs.readdirSync(barrier).filter((name) => name.startsWith('ready-')).length !== count) {
      if (Date.now() >= deadline) throw new Error('identity children did not reach the concurrency barrier');
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    fs.writeFileSync(path.join(barrier, 'go'), '');
    return await Promise.all(children.map(({ result }) => result));
  } finally {
    for (const { child } of children) {
      if (child.exitCode === null) child.kill();
    }
    fs.rmSync(barrier, { recursive: true, force: true });
  }
}

function assertCompleteAnchorPublication(workspace, expectedAnchor) {
  const anchorPath = path.join(workspace, '.engram-project-v2.json');
  const parsed = JSON.parse(fs.readFileSync(anchorPath, 'utf8'));
  assert.deepEqual(Object.keys(parsed).sort(), ['anchor', 'shared', 'version']);
  assert.equal(parsed.version, 2);
  assert.equal(parsed.anchor, expectedAnchor);
  assert.equal(parsed.shared, false);
  if (process.platform !== 'win32') {
    assert.equal(fs.statSync(anchorPath).mode & 0o777, 0o600);
  }
  assert.deepEqual(fs.readdirSync(workspace).filter((name) => name.startsWith('.engram-project-v2.json.tmp-')), []);
}

test('OpenClaw consumes the same v2 vectors as Go and Claude hooks', () => {
  assert.equal(PROJECT_IDENTITY_VERSION_V2, vectors.identity_version);
  for (const vector of vectors.vectors) {
    const identity = buildProjectIdentityV2(vector);
    assert.equal(identity.version, 2, vector.name);
    assert.equal(identity.legacy_project_id, vector.legacy_project_id, vector.name);
    assert.doesNotThrow(() => validateProjectIdentityV2(identity), vector.name);
  }
});

test('OpenClaw non-git identity is stable and child-process concurrent-safe, never the agent id', async () => {
  const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-identity-v2-'));
  const otherWorkspace = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-identity-v2-other-'));
  try {
    const firstRun = await resolveOpenClawIdentityInChildProcesses(workspace, 16);
    assert.ok(firstRun.every((result) => result.ok), JSON.stringify(firstRun));
    const identities = firstRun.map((result) => result.value);
    assert.match(identities[0].non_git_anchor, /^[0-9a-f]{32}$/);
    assert.ok(identities.every((identity) => identity.non_git_anchor === identities[0].non_git_anchor));
    assert.notEqual(identities[0].non_git_anchor, 'agent-secret-a');
    assert.equal(identities[0].anchor_shared, false);
    assertCompleteAnchorPublication(workspace, identities[0].non_git_anchor);

    const anchorPath = path.join(workspace, '.engram-project-v2.json');
    const originalBytes = fs.readFileSync(anchorPath);
    const secondRun = await resolveOpenClawIdentityInChildProcesses(workspace, 8);
    assert.ok(secondRun.every((result) => result.ok), JSON.stringify(secondRun));
    assert.ok(secondRun.every((result) => result.value.non_git_anchor === identities[0].non_git_anchor));
    assert.deepEqual(fs.readFileSync(anchorPath), originalBytes, 'an existing anchor must remain byte-identical');
    assertCompleteAnchorPublication(workspace, identities[0].non_git_anchor);

    const other = resolveIdentity('agent-secret-c', otherWorkspace);
    assert.notEqual(identities[0].non_git_anchor, other.projectIdentityV2.non_git_anchor);
  } finally {
    fs.rmSync(workspace, { recursive: true, force: true });
    fs.rmSync(otherWorkspace, { recursive: true, force: true });
  }
});

test('OpenClaw rejects non-normalized metadata and unknown anchor-file fields without replacement', async () => {
  const malformed = buildProjectIdentityV2({
    legacy_project_id: ' selector ',
    display_name: 'fixture',
    git_remote: 'https://example.invalid/acme/mono.git',
    relative_path: 'packages/core/',
  });
  assert.throws(() => validateProjectIdentityV2(malformed), /PROJECT_IDENTITY_INVALID/);

  const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-identity-v2-extra-'));
  try {
    const anchorPath = path.join(workspace, '.engram-project-v2.json');
    const malformedBytes = Buffer.from(JSON.stringify({
      version: 2,
      anchor: '00112233445566778899aabbccddeeff',
      shared: false,
      unexpected: true,
    }));
    fs.writeFileSync(anchorPath, malformedBytes, { mode: 0o600 });
    assert.throws(() => resolveIdentity('agent-a', workspace), /PROJECT_IDENTITY_INVALID/);
    const concurrent = await resolveOpenClawIdentityInChildProcesses(workspace, 8);
    assert.ok(concurrent.every((result) => !result.ok && /PROJECT_IDENTITY_INVALID/.test(result.error)), JSON.stringify(concurrent));
    assert.deepEqual(fs.readFileSync(anchorPath), malformedBytes, 'malformed existing bytes must never be regenerated');
    assert.deepEqual(fs.readdirSync(workspace).filter((name) => name.startsWith('.engram-project-v2.json.tmp-')), []);
  } finally {
    fs.rmSync(workspace, { recursive: true, force: true });
  }
});

test('OpenClaw rejects every shared invalid metadata vector', () => {
  for (const vector of vectors.invalid_vectors) {
    if (vector.invalid_target !== 'identity') continue;
    const identity = buildProjectIdentityV2(vector);
    assert.throws(() => validateProjectIdentityV2(identity), /PROJECT_IDENTITY_INVALID/, vector.name);
  }
  const wrongBoolean = buildProjectIdentityV2({
    legacy_project_id: 'workspace',
    display_name: 'workspace',
    non_git_anchor: '00112233445566778899aabbccddeeff',
    anchor_shared: 'false',
  });
  assert.throws(() => validateProjectIdentityV2(wrongBoolean), /PROJECT_IDENTITY_INVALID/);
});
