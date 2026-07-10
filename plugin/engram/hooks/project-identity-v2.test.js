const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const lib = require('./lib');

const vectorsPath = path.resolve(__dirname, '../../../.agent/specs/security-project-identity/evidence/project-identity-v2-vectors.json');
const vectors = JSON.parse(fs.readFileSync(vectorsPath, 'utf8'));

test('project identity v2 consumes the repository-wide vectors', () => {
  assert.equal(vectors.identity_version, lib.PROJECT_IDENTITY_VERSION_V2);
  for (const vector of vectors.vectors) {
    const identity = lib.buildProjectIdentityV2(vector);
    assert.equal(identity.version, 2, vector.name);
    assert.equal(identity.legacy_project_id, vector.legacy_project_id, vector.name);
    assert.equal(identity.git_remote, vector.git_remote, vector.name);
    assert.equal(identity.relative_path, vector.relative_path, vector.name);
    assert.equal(identity.non_git_anchor, vector.non_git_anchor, vector.name);
    assert.equal(identity.anchor_shared, vector.anchor_shared, vector.name);
    assert.doesNotThrow(() => lib.validateProjectIdentityV2(identity), vector.name);
  }
});

test('non-git v2 anchor is strict, high-entropy, stable, and concurrent-safe', async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));

  const identities = await Promise.all(Array.from({ length: 16 }, () =>
    lib.resolveProjectIdentityV2(dir)));
  const anchors = new Set(identities.map((identity) => identity.non_git_anchor));
  assert.equal(anchors.size, 1);
  assert.match(identities[0].non_git_anchor, /^[0-9a-f]{32}$/);
  assert.equal(identities[0].anchor_shared, false);

  const otherDir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-other-'));
  t.after(() => fs.rmSync(otherDir, { recursive: true, force: true }));
  const other = lib.resolveProjectIdentityV2(otherDir);
  assert.notEqual(other.non_git_anchor, identities[0].non_git_anchor,
    'independent projects must not receive the same anchor');

  const bad = { ...identities[0], non_git_anchor: 'path-derived' };
  assert.throws(() => lib.validateProjectIdentityV2(bad), /PROJECT_IDENTITY_INVALID/);
});

test('v2 metadata and anchor files reject non-normalized or unknown input', (t) => {
  const malformed = lib.buildProjectIdentityV2({
    legacy_project_id: ' selector ',
    display_name: 'fixture',
    git_remote: 'https://example.invalid/acme/mono.git',
    relative_path: 'packages/core/',
  });
  assert.throws(() => lib.validateProjectIdentityV2(malformed), /PROJECT_IDENTITY_INVALID/);

  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-extra-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  fs.writeFileSync(path.join(dir, '.engram-project-v2.json'), JSON.stringify({
    version: 2,
    anchor: '00112233445566778899aabbccddeeff',
    shared: false,
    unexpected: true,
  }));
  assert.throws(() => lib.resolveProjectIdentityV2(dir), /PROJECT_IDENTITY_INVALID/);
});

test('shared invalid vectors and wrong-type anchor sharing are rejected exactly', () => {
  for (const vector of vectors.invalid_vectors) {
    if (vector.invalid_target !== 'identity') continue;
    const identity = lib.buildProjectIdentityV2(vector);
    assert.throws(() => lib.validateProjectIdentityV2(identity), /PROJECT_IDENTITY_INVALID/, vector.name);
  }
  assert.throws(() => lib.buildProjectIdentityV2({
    legacy_project_id: 'workspace',
    display_name: 'workspace',
    non_git_anchor: '00112233445566778899aabbccddeeff',
    anchor_shared: 'false',
  }), /PROJECT_IDENTITY_INVALID/);
});

test('registration offline fallback distinguishes transport failure from malformed reached-server response', () => {
  const offline = new TypeError('fetch failed', { cause: Object.assign(new Error('connect'), { code: 'ECONNREFUSED' }) });
  assert.equal(lib.isProjectIdentityTransportOffline(offline), true);
  assert.equal(lib.isProjectIdentityTransportOffline(new SyntaxError('Unexpected token')), false);
});

test('registration is synchronous, idempotent, and updates the hook canonical selector', async () => {
  const calls = [];
  const context = {
    Project: 'legacy-selector',
    ProjectIdentityV2: {
      version: 2,
      legacy_project_id: 'legacy-selector',
      display_name: 'fixture',
      git_remote: '',
      relative_path: '',
      non_git_anchor: '00112233445566778899aabbccddeeff',
      anchor_shared: false,
    },
  };
  const requestFn = async (_method, endpoint, body) => {
    calls.push({ endpoint, body });
    return { canonical_project: 'canonical-v2' };
  };

  await lib.registerProjectIdentityV2(context, requestFn);
  await lib.registerProjectIdentityV2(context, requestFn);

  assert.equal(context.Project, 'canonical-v2');
  assert.equal(calls.length, 2);
  assert.equal(calls[0].endpoint, '/api/context/inject');
  assert.equal(calls[0].body.identity_only, true);
});

test('registration rejects shared invalid selectors before transport', async () => {
  for (const vector of vectors.invalid_vectors) {
    if (vector.invalid_target !== 'selector') continue;
    let requests = 0;
    const context = {
      Project: vector.selector,
      ProjectIdentityV2: lib.buildProjectIdentityV2(vector),
    };
    await assert.rejects(
      () => lib.registerProjectIdentityV2(context, async () => {
        requests++;
        return { canonical_project: 'must-not-run' };
      }),
      /PROJECT_IDENTITY_INVALID/,
      vector.name,
    );
    assert.equal(requests, 0, vector.name);
  }
});

test('registration preserves legacy selector characters accepted by the HTTP boundary', async () => {
  const selector = 'legacy:C\\workspace';
  const context = {
    Project: selector,
    ProjectIdentityV2: {
      version: 2,
      legacy_project_id: selector,
      display_name: 'workspace',
      git_remote: 'https://example.invalid/acme/mono.git',
      relative_path: 'packages/core/',
      non_git_anchor: '',
      anchor_shared: null,
    },
  };
  let sentSelector = '';
  await lib.registerProjectIdentityV2(context, async (_method, _endpoint, body) => {
    sentSelector = body.project;
    return { canonical_project: selector };
  });
  assert.equal(sentSelector, selector);
  assert.equal(context.Project, selector);
});

test('registration fails closed on malformed canonical responses without raw fallback', async () => {
  const payloads = [
    {},
    { canonical_project: '' },
    { canonical_project: 42 },
    { canonical_project: ' invalid-canonical ' },
    { canonical_project: '../private' },
  ];
  for (const payload of payloads) {
    const context = {
      Project: 'legacy-selector',
      ProjectIdentityV2: {
        version: 2,
        legacy_project_id: 'legacy-selector',
        display_name: 'fixture',
        git_remote: 'https://example.invalid/acme/mono.git',
        relative_path: 'packages/core/',
        non_git_anchor: '',
        anchor_shared: null,
      },
    };
    let downstream = 0;
    await assert.rejects(async () => {
      await lib.registerProjectIdentityV2(context, async () => payload);
      downstream++;
    }, /PROJECT_IDENTITY_UNAVAILABLE/);
    assert.equal(context.Project, 'legacy-selector');
    assert.equal(downstream, 0);
  }
});
