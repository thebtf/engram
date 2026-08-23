import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

import * as v3 from './project-identity-v3.js';

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
    expected: { outcome: string };
  }>;
};

const helpers = [
  'parseProjectAnchorV3',
  'discoverProjectAnchorV3',
  'normalizeGitRemoteV3',
  'buildProjectIdentityV3',
] as const;

function descriptorInput(anchor: Record<string, unknown>, descriptor: Record<string, unknown>) {
  return {
    anchor,
    version: descriptor.version,
    anchor_project_id: descriptor.anchor_project_id,
    name: descriptor.name,
    scope: descriptor.scope,
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
      const invalidAnchor = vector.expected.outcome === 'PROJECT_ANCHOR_INVALID';
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
    const unsupportedVersion = descriptor.version !== 3;
    const missingClientID = typeof descriptor.client_instance_id !== 'string' || descriptor.client_instance_id === '';
    const mismatchedAnchor = descriptor.name !== anchor.name || descriptor.scope !== anchor.scope ||
      descriptor.anchor_project_id !== anchor.project_id;
    const expectedDescriptorRefusal = vector.expected.outcome === 'PROJECT_DESCRIPTOR_INVALID';
    if (hasClientKey || unsupportedVersion || missingClientID || mismatchedAnchor || expectedDescriptorRefusal) {
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

test('OpenClaw V3 rejects credential-shaped SCP remotes without retaining input', () => {
  assert.deepEqual(
    v3.normalizeGitRemoteV3('git:pa:ss@GIT.EXAMPLE.TEST:Platform/Widget.git'),
    { disposition: 'refused' },
  );
});

test('OpenClaw V3 rejects non-canonical descriptor evidence', () => {
  const anchor = corpus.vectors[0].input.anchor!;
  const base = {
    anchor,
    normalized_git_remotes: [],
    legacy_identifiers: [],
    client_instance_id: 'fixture-install-edge',
  };

  assert.throws(
    () => v3.buildProjectIdentityV3({ ...base, normalized_git_remotes: ['git.example.test//Platform/Widget'] }),
    /PROJECT_DESCRIPTOR_INVALID/,
  );
  for (const value of ['legacy widget alias', 'legacy-widget\u0007alias']) {
    assert.throws(
      () => v3.buildProjectIdentityV3({
        ...base,
        legacy_identifiers: [{ scheme: 'manual_alias', value, provenance: 'operator_import' }],
      }),
      /PROJECT_DESCRIPTOR_INVALID/,
    );
  }
});

test('OpenClaw V3 rejects non-opaque client instance IDs', () => {
  const anchor = corpus.vectors[0].input.anchor!;
  const base = {
    anchor,
    normalized_git_remotes: [],
    legacy_identifiers: [],
  };

  for (const client_instance_id of [
    '/private/operator/path',
    'C:\\private\\operator',
    'install / private',
    'install\u0007private',
    'credential@private',
  ]) {
    assert.throws(
      () => v3.buildProjectIdentityV3({ ...base, client_instance_id }),
      /PROJECT_DESCRIPTOR_INVALID/,
      client_instance_id,
    );
  }
});
