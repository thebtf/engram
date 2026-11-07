const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

test("hook policy binding retains only the last valid policy for its own retired package", () => {
 const { createHookPolicyBinding } = require("./run-engram.js");
 const { createPolicy, TARGET_ASSETS } = require("./bootstrap-policy.js");
 const base = fs.mkdtempSync(path.join(os.tmpdir(), "engram-hook-policy-"));
 const root = path.join(base, "package"), manifest = path.join(root, ".omp-plugin", "plugin.json");
 const version = "7.0.27", first = "1".repeat(64), second = "2".repeat(64);
 const makePolicy = hash => createPolicy(version, Object.fromEntries(Object.entries(TARGET_ASSETS)
  .map(([key, asset]) => [key, { version, asset, size: 1, sha256: hash }])));
 const writePackage = policy => {
  fs.mkdirSync(path.dirname(manifest), { recursive: true });
  fs.writeFileSync(manifest, JSON.stringify({ version }));
  fs.writeFileSync(path.join(root, "bootstrap-targets.json"), JSON.stringify(policy));
 };
 try {
  writePackage(makePolicy(first));
  const binding = createHookPolicyBinding(root, version);
  assert.equal(binding(root).target.desired.sha256, first);
  assert.throws(() => binding(path.join(base, "different")), /root changed/);
  writePackage(makePolicy(second));
  assert.equal(binding(root).target.desired.sha256, second, "present policy changes take effect");
  fs.rmSync(root, { recursive: true });
  assert.equal(binding(root).target.desired.sha256, second, "retirement retains only the last validated policy");

  writePackage(makePolicy(first));
  const invalidated = createHookPolicyBinding(root, version);
  fs.writeFileSync(path.join(root, "bootstrap-targets.json"), "not json");
  assert.throws(() => invalidated(root), /JSON/);
  fs.rmSync(root, { recursive: true });
  assert.throws(() => invalidated(root), /manifest is missing/, "invalid current policy clears retained authority");

  writePackage(makePolicy(first));
  const revoked = createHookPolicyBinding(root, version);
  fs.writeFileSync(path.join(root, "bootstrap-targets.json"), JSON.stringify({ ...makePolicy(first), revoked_sha256: [first] }));
  assert.throws(() => revoked(root), /revoked/);
  fs.rmSync(root, { recursive: true });
  assert.throws(() => revoked(root), /manifest is missing/, "revoked current policy clears retained authority");

  writePackage(makePolicy(first));
  const mismatch = createHookPolicyBinding(root, "7.0.26");
  assert.throws(() => mismatch(root), /version changed/);
  fs.rmSync(root, { recursive: true });
  assert.throws(() => mismatch(root), /manifest is missing/, "wrong-version policy is never retained");

  writePackage(makePolicy(first));
  const missing = createHookPolicyBinding(root, version);
  fs.unlinkSync(path.join(root, "bootstrap-targets.json"));
  assert.throws(() => missing(root), /bootstrap policy/, "missing policy in a present package is not retirement");
  fs.rmSync(root, { recursive: true });
  assert.throws(() => missing(root), /manifest is missing/);
 } finally { fs.rmSync(base, { recursive: true, force: true }); }
});

test("two hook policy bindings retain and invalidate independently", () => {
 const { createHookPolicyBinding } = require("./run-engram.js");
 const { createPolicy, TARGET_ASSETS } = require("./bootstrap-policy.js");
 const base = fs.mkdtempSync(path.join(os.tmpdir(), "engram-hook-policy-pair-"));
 const version = "7.0.27", roots = [path.join(base, "first"), path.join(base, "second")];
 const hashes = ["1".repeat(64), "2".repeat(64)];
 try {
  for (const [index, root] of roots.entries()) {
   const targets = Object.fromEntries(Object.entries(TARGET_ASSETS)
    .map(([key, asset]) => [key, { version, asset, size: 1, sha256: hashes[index] }]));
   fs.mkdirSync(path.join(root, ".omp-plugin"), { recursive: true });
   fs.writeFileSync(path.join(root, ".omp-plugin", "plugin.json"), JSON.stringify({ version }));
   fs.writeFileSync(path.join(root, "bootstrap-targets.json"), JSON.stringify(createPolicy(version, targets)));
  }
  const [first, second] = roots.map(root => createHookPolicyBinding(root, version));
  assert.equal(first(roots[0]).target.desired.sha256, hashes[0]);
  assert.equal(second(roots[1]).target.desired.sha256, hashes[1]);
  fs.rmSync(roots[0], { recursive: true });
  assert.equal(first(roots[0]).target.desired.sha256, hashes[0]);
  assert.equal(second(roots[1]).target.desired.sha256, hashes[1], "retiring the first package must not change the second policy");
  fs.writeFileSync(path.join(roots[1], "bootstrap-targets.json"), "not json");
  assert.throws(() => second(roots[1]), /JSON/);
  assert.equal(first(roots[0]).target.desired.sha256, hashes[0], "invalidating the second policy must not clear the first binding");
  fs.rmSync(roots[1], { recursive: true });
  assert.throws(() => second(roots[1]), /manifest is missing/, "the second binding must not borrow the first retained policy");
  assert.equal(first(roots[0]).target.desired.sha256, hashes[0]);
 } finally { fs.rmSync(base, { recursive: true, force: true }); }
});
