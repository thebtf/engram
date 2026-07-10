import assert from 'node:assert/strict';
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

test('OpenClaw consumes the same v2 vectors as Go and Claude hooks', () => {
  assert.equal(PROJECT_IDENTITY_VERSION_V2, vectors.identity_version);
  for (const vector of vectors.vectors) {
    const identity = buildProjectIdentityV2(vector);
    assert.equal(identity.version, 2, vector.name);
    assert.equal(identity.legacy_project_id, vector.legacy_project_id, vector.name);
    assert.doesNotThrow(() => validateProjectIdentityV2(identity), vector.name);
  }
});

test('OpenClaw non-git identity has a stable strict anchor, never the agent id', () => {
  const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-identity-v2-'));
  const otherWorkspace = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-identity-v2-other-'));
  try {
    const first = resolveIdentity('agent-secret-a', workspace);
    const second = resolveIdentity('agent-secret-b', workspace);
    assert.ok(first.projectIdentityV2);
    assert.match(first.projectIdentityV2.non_git_anchor, /^[0-9a-f]{32}$/);
    assert.equal(first.projectIdentityV2.non_git_anchor, second.projectIdentityV2.non_git_anchor);
    assert.notEqual(first.projectIdentityV2.non_git_anchor, 'agent-secret-a');
    assert.equal(first.projectIdentityV2.anchor_shared, false);
    const other = resolveIdentity('agent-secret-c', otherWorkspace);
    assert.notEqual(first.projectIdentityV2.non_git_anchor, other.projectIdentityV2.non_git_anchor);
  } finally {
    fs.rmSync(workspace, { recursive: true, force: true });
    fs.rmSync(otherWorkspace, { recursive: true, force: true });
  }
});

test('OpenClaw rejects non-normalized metadata and unknown anchor-file fields', () => {
  const malformed = buildProjectIdentityV2({
    legacy_project_id: ' selector ',
    display_name: 'fixture',
    git_remote: 'https://example.invalid/acme/mono.git',
    relative_path: 'packages/core/',
  });
  assert.throws(() => validateProjectIdentityV2(malformed), /PROJECT_IDENTITY_INVALID/);

  const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-identity-v2-extra-'));
  try {
    fs.writeFileSync(path.join(workspace, '.engram-project-v2.json'), JSON.stringify({
      version: 2,
      anchor: '00112233445566778899aabbccddeeff',
      shared: false,
      unexpected: true,
    }));
    assert.throws(() => resolveIdentity('agent-a', workspace), /PROJECT_IDENTITY_INVALID/);
  } finally {
    fs.rmSync(workspace, { recursive: true, force: true });
  }
});
