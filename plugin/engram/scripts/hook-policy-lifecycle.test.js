const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const { createHookPolicyBinding } = require('./run-engram.js');
const { createPolicy, TARGET_ASSETS } = require('./bootstrap-policy.js');

const version = require('../package.json').version;
const policyFor = hash => createPolicy(version, Object.fromEntries(Object.entries(TARGET_ASSETS)
  .map(([key, asset]) => [key, { version, asset, size: 1, sha256: hash }])));
const writePackage = (root, policy) => {
  fs.mkdirSync(path.join(root, '.omp-plugin'), { recursive: true });
  fs.writeFileSync(path.join(root, '.omp-plugin', 'plugin.json'), JSON.stringify({ version }));
  fs.writeFileSync(path.join(root, 'bootstrap-targets.json'), JSON.stringify(policy));
};

test('retirement cannot resurrect an explicitly revoked retained policy', () => {
  const base = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-policy-revoked-'));
  const root = path.join(base, 'package'), hash = '1'.repeat(64);
  try {
    writePackage(root, policyFor(hash));
    const binding = createHookPolicyBinding(root, version);
    assert.equal(binding(root).target.desired.sha256, hash);
    writePackage(root, { ...policyFor(hash), revoked_sha256: [hash] });
    assert.throws(() => binding(root), /revoked/);
    fs.rmSync(root, { recursive: true });
    assert.throws(() => binding(root), /manifest is missing/);
  } finally { fs.rmSync(base, { recursive: true, force: true }); }
});

test('independent bindings survive retirement and invalidation of the other root', () => {
  const base = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-policy-pair-'));
  const roots = [path.join(base, 'first'), path.join(base, 'second')];
  const hashes = ['1'.repeat(64), '2'.repeat(64)];
  try {
    roots.forEach((root, index) => writePackage(root, policyFor(hashes[index])));
    const [first, second] = roots.map(root => createHookPolicyBinding(root, version));
    assert.equal(first(roots[0]).target.desired.sha256, hashes[0]);
    assert.equal(second(roots[1]).target.desired.sha256, hashes[1]);
    fs.rmSync(roots[0], { recursive: true });
    assert.equal(first(roots[0]).target.desired.sha256, hashes[0]);
    assert.equal(second(roots[1]).target.desired.sha256, hashes[1]);
    fs.writeFileSync(path.join(roots[1], 'bootstrap-targets.json'), 'not json');
    assert.throws(() => second(roots[1]), /JSON/);
    assert.equal(first(roots[0]).target.desired.sha256, hashes[0]);
    fs.rmSync(roots[1], { recursive: true });
    assert.throws(() => second(roots[1]), /manifest is missing/);
    assert.equal(first(roots[0]).target.desired.sha256, hashes[0]);
  } finally { fs.rmSync(base, { recursive: true, force: true }); }
});
