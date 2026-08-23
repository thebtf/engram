import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

import * as identity from './identity.js';

const here = __dirname;
const vectorsPath = path.resolve(here, '../../../contracts/testdata/project_identity_v3_vectors.json');
const corpus = JSON.parse(fs.readFileSync(vectorsPath, 'utf8')) as {
  contract: string;
  identity_version: number;
  vector_counts: { accepted: number; refused: number };
  vectors: Array<{
    id: string;
    input: {
      anchor?: Record<string, unknown> | null;
      descriptor?: Record<string, unknown> | null;
      remote_observations?: Array<Record<string, unknown>>;
    };
  }>;
};

type V3Helpers = Record<string, (value: unknown) => unknown>;
const v3 = identity as unknown as V3Helpers;
const helpers = [
  'parseProjectAnchorV3',
  'discoverProjectAnchorV3',
  'normalizeGitRemoteV3',
  'buildProjectIdentityV3',
];

function descriptorInput(anchor: Record<string, unknown>, descriptor: Record<string, unknown>) {
  return {
    anchor,
    normalized_git_remotes: descriptor.normalized_git_remotes,
    legacy_identifiers: descriptor.legacy_identifiers,
    client_instance_id: descriptor.client_instance_id,
    ...(Object.hasOwn(descriptor, 'project_key') ? { project_key: descriptor.project_key } : {}),
  };
}

for (const helper of helpers) {
  test(`OpenClaw V3 descriptor seam exports ${helper}`, () => {
    assert.equal(typeof v3[helper], 'function', `${helper} is required by the frozen V3 vectors`);
  });
}

test('OpenClaw V3 descriptor helpers consume the frozen shared vectors', () => {
  assert.equal(corpus.contract, 'project_identity_v3');
  assert.equal(corpus.identity_version, 3);
  assert.equal(corpus.vectors.length, corpus.vector_counts.accepted + corpus.vector_counts.refused);

  for (const vector of corpus.vectors) {
    const anchor = vector.input.anchor;
    const descriptor = vector.input.descriptor;
    const remotes = vector.input.remote_observations ?? [];
    if (anchor) {
      const invalidAnchor = anchor.version !== 3 || typeof anchor.project_id !== 'string' ||
        typeof anchor.name !== 'string' || !['repository', 'directory'].includes(anchor.scope as string) ||
        Object.keys(anchor).some((key) => !['version', 'project_id', 'name', 'scope'].includes(key));
      if (invalidAnchor) {
        assert.throws(() => v3.parseProjectAnchorV3(anchor), /PROJECT_ANCHOR_INVALID/, vector.id);
      } else {
        assert.deepEqual(v3.parseProjectAnchorV3(anchor), anchor, vector.id);
      }
    }

    for (const remote of remotes) {
      const normalized = v3.normalizeGitRemoteV3(remote) as Record<string, unknown>;
      const disposition = remote.disposition ?? 'normalized';
      assert.equal(normalized.disposition, disposition, vector.id);
      if (disposition === 'normalized') assert.equal(normalized.value, remote.normalized, vector.id);
      if (disposition !== 'normalized') {
        assert.equal(Object.hasOwn(normalized, 'raw_value'), false, vector.id);
        assert.equal(Object.hasOwn(normalized, 'source'), false, vector.id);
      }
    }

    if (!descriptor || !anchor || anchor.version !== 3) continue;
    const hasClientKey = Object.hasOwn(descriptor, 'project_key');
    const missingClientID = typeof descriptor.client_instance_id !== 'string' || descriptor.client_instance_id === '';
    const mismatchedAnchor = descriptor.name !== anchor.name || descriptor.scope !== anchor.scope ||
      descriptor.anchor_project_id !== anchor.project_id;
    if (hasClientKey || missingClientID || mismatchedAnchor) {
      assert.throws(() => v3.buildProjectIdentityV3(descriptorInput(anchor, descriptor)), /PROJECT_(?:KEY_CLIENT_ASSERTION_FORBIDDEN|DESCRIPTOR_INVALID|SCOPE_MISMATCH)/, vector.id);
    } else {
      assert.deepEqual(v3.buildProjectIdentityV3(descriptorInput(anchor, descriptor)), descriptor, vector.id);
    }
  }
});

test('OpenClaw V3 directory discovery never searches upward from the selected root', (t) => {
  const parent = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-v3-discovery-'));
  const selectedRoot = path.join(parent, 'selected');
  fs.mkdirSync(selectedRoot);
  fs.writeFileSync(path.join(parent, '.engram-project'), JSON.stringify(corpus.vectors[0].input.anchor));
  t.after(() => fs.rmSync(parent, { recursive: true, force: true }));

  assert.equal(v3.discoverProjectAnchorV3(selectedRoot), null);
});
