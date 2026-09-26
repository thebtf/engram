const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const { EventEmitter } = require("node:events");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { Readable } = require("node:stream");
const test = require("node:test");

const {
  BootstrapError, downloadObject, hashFile, importLegacy, loadPolicy, loadParserTarget, installParser,
  objectPath, objectRoots, parsePolicy, publishStage, requestStream, resolveForLaunch, verifyObject,
} = require("./ensure-binary.js");

function digest(bytes) { return crypto.createHash("sha256").update(bytes).digest("hex"); }
function fixture(version = "6.47.0", bytes = Buffer.from("trusted client bytes")) {
  const hash = digest(bytes);
  const tuple = (asset) => ({ version, asset, size: bytes.length, sha256: hash });
  return {
    bytes,
    policy: {
      schema_version: 1,
      launcher_security_epoch: 1,
      package_version: version,
      daemon_compat_epoch: 1,
      targets: {
        "win32-x64": { desired: tuple("engram-windows-amd64.exe"), predecessor: null },
        "linux-x64": { desired: tuple("engram-linux-amd64"), predecessor: null },
        "darwin-arm64": { desired: tuple("engram-darwin-arm64"), predecessor: null },
      },
      revoked_sha256: [],
      build_contract: { go_version: "1.26.6", trimpath: true, buildvcs: false, client_cgo: false, daemon_version_ldflag: `v${version}` },
    },
  };
}
function tempRoot() { return fs.mkdtempSync(path.join(os.tmpdir(), "engram-bootstrap-")); }

const selected = () => parsePolicy(JSON.stringify(fixture().policy), "6.47.0");

test("strict policy rejects unknown fields, package skew, revoked hashes, and epoch-1 predecessor", () => {
  for (const mutate of [
    (policy) => { policy.extra = true; },
    (policy) => { policy.package_version = "6.47.1"; },
    (policy) => { policy.revoked_sha256.push(policy.targets["win32-x64"].desired.sha256); },
    (policy) => { policy.targets["win32-x64"].predecessor = { ...policy.targets["win32-x64"].desired, version: "6.46.9" }; },
    (policy) => { policy.targets["win32-x64"].desired.asset = "../engram"; },
  ]) {
    const { policy } = fixture();
    mutate(policy);
    assert.throws(() => parsePolicy(JSON.stringify(policy), "6.47.0", "win32-x64"), BootstrapError);
  }
});

test("policy selection only accepts the exact host platform tuple", () => {
  const policy = selected();
  assert.equal(policy.target.desired.asset, policy.targets[policy.platform].desired.asset);
  assert.throws(() => parsePolicy(JSON.stringify(fixture().policy), "6.47.0", "linux-arm64"), /no target/);
});

test("OMP manifest wins over a skewed Codex fallback while loading strict policy", () => {
  const root = tempRoot();
  fs.mkdirSync(path.join(root, ".omp-plugin"));
  fs.mkdirSync(path.join(root, ".codex-plugin"));
  fs.writeFileSync(path.join(root, ".omp-plugin", "plugin.json"), '{"version":"6.47.0"}');
  fs.writeFileSync(path.join(root, ".codex-plugin", "plugin.json"), '{"version":"6.46.9"}');
  fs.writeFileSync(path.join(root, "bootstrap-targets.json"), JSON.stringify(fixture().policy));

  assert.equal(loadPolicy(root, { platformKey: "win32-x64" }).package_version, "6.47.0");
});

test("object verification rejects poisoned, oversized, truncated, directory, and reparse objects", () => {
  const root = tempRoot();
  const roots = objectRoots(root);
  const target = selected().target.desired;
  const object = objectPath(roots, target);
  fs.writeFileSync(object, "wrong");
  assert.equal(verifyObject(roots, target), "");
  assert.equal(fs.existsSync(object), false, "wrong object is quarantined rather than overwritten");
  fs.writeFileSync(object, Buffer.concat([fixture().bytes, Buffer.from("x")]));
  assert.equal(verifyObject(roots, target), "");
  fs.mkdirSync(object);
  assert.throws(() => verifyObject(roots, target), BootstrapError);
  fs.symlinkSync(path.join(root, "outside"), object, process.platform === "win32" ? "file" : undefined);
  assert.throws(() => verifyObject(roots, target), BootstrapError);
});

test("legacy canonical bytes are imported only after exact hashing and are never authority", () => {
  const root = tempRoot();
  const roots = objectRoots(root);
  const target = selected().target.desired;
  const legacy = path.join(roots.bin, process.platform === "win32" ? "engram.exe" : "engram");
  fs.writeFileSync(legacy, fixture().bytes);
  fs.chmodSync(legacy, 0o444); // Models a legacy file the importer must only read.
  const imported = importLegacy(roots, target);
  assert.equal(hashFile(imported, target, roots.objects), true);
  assert.equal(fs.readFileSync(legacy, "utf8"), "trusted client bytes");
});

test("concurrent no-overwrite publication accepts only an independently verified winner", () => {
  const root = tempRoot();
  const roots = objectRoots(root);
  const target = selected().target.desired;
  const stages = [path.join(roots.staging, "one.part"), path.join(roots.staging, "two.part")];
  fs.writeFileSync(stages[0], fixture().bytes);
  fs.writeFileSync(stages[1], fixture().bytes);
  const first = publishStage(stages[0], roots, target);
  const second = publishStage(stages[1], roots, target);
  assert.equal(first, second);
  assert.equal(hashFile(second, target, roots.objects), true);
});

test("redirect escape and loop fail before bytes are accepted", async () => {
  const target = selected().target.desired;
  const fakeRequest = (url, _options, callback) => {
    const request = new EventEmitter();
    request.end = () => callback(Object.assign(Readable.from([]), { statusCode: 302, headers: { location: "http://evil.invalid/a" }, resume() { } }));
    return request;
  };
  await assert.rejects(requestStream("https://github.com/thebtf/engram/releases/download/v6.47.0/x", target, fakeRequest), BootstrapError);
  const loopRequest = (_url, _options, callback) => {
    const request = new EventEmitter();
    request.end = () => callback(Object.assign(Readable.from([]), { statusCode: 302, headers: { location: "https://github.com/again" }, resume() { } }));
    return request;
  };
  await assert.rejects(requestStream("https://github.com/thebtf/engram/releases/download/v6.47.0/x", target, loopRequest), /redirect limit/);
});


test("bounded acquisition rejects contradictory, truncated, and one-byte oversize streams without leaving staging", async () => {
  const root = tempRoot();
  const roots = objectRoots(root);
  const target = selected().target.desired;
  const makeRequest = (bytes, length = target.size) => (_url, _options, callback) => {
    const request = new EventEmitter();
    request.end = () => callback(Object.assign(Readable.from([bytes]), { statusCode: 200, headers: { "content-length": String(length) } }));
    return request;
  };
  await assert.rejects(downloadObject(roots, target, { request: makeRequest(fixture().bytes, target.size + 1) }), BootstrapError);
  await assert.rejects(downloadObject(roots, target, { request: makeRequest(fixture().bytes.subarray(0, -1)) }), BootstrapError);
  await assert.rejects(downloadObject(roots, target, { request: makeRequest(Buffer.concat([fixture().bytes, Buffer.from("x")])) }), BootstrapError);
  const tampered = Buffer.from(fixture().bytes);
  tampered[0] ^= 1;
  await assert.rejects(downloadObject(roots, target, { request: makeRequest(tampered) }), /digest differs/);
  assert.deepEqual(fs.readdirSync(roots.staging), []);
});
test("cold-cache stream outlives the old limit but not the bounded bootstrap deadline", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const { bytes } = fixture();
  const target = selected().target.desired;
  let response;
  const request = (_url, _options, callback) => {
    const req = new EventEmitter();
    req.end = () => {
      response = Object.assign(new Readable({ read() { } }), {
        statusCode: 200, headers: { "content-length": String(target.size) },
      });
      callback(response);
      response.push(bytes.subarray(0, 4));
    };
    return req;
  };

  const roots = objectRoots(tempRoot());
  const pending = downloadObject(roots, target, { request });
  void pending.catch(() => { });
  await new Promise((resolve) => setImmediate(resolve));
  t.mock.timers.tick(60_000);
  response.push(bytes.subarray(4, 8));
  t.mock.timers.tick(61_000);
  assert.equal(response.destroyed, false, "a progressing stream must survive past two minutes");
  response.push(bytes.subarray(8));
  response.push(null);
  const published = await pending;
  assert.equal(hashFile(published, target, roots.objects), true);
  assert.deepEqual(fs.readdirSync(roots.staging), []);

  const stalledRoots = objectRoots(tempRoot());
  const stalled = downloadObject(stalledRoots, target, { request });
  await new Promise((resolve) => setImmediate(resolve));
  t.mock.timers.tick(360_000);
  await assert.rejects(stalled, /deadline exceeded/);
  assert.equal(fs.existsSync(objectPath(stalledRoots, target)), false);
  assert.deepEqual(fs.readdirSync(stalledRoots.staging), []);
});

test("offline resolution uses a verified desired object and rejects bytes changed after final verification", async () => {
  const root = tempRoot();
  const roots = objectRoots(root);
  const policy = selected();
  const object = objectPath(roots, policy.target.desired);
  fs.writeFileSync(object, fixture().bytes);
  const resolved = await resolveForLaunch({ pluginRoot: root, pluginData: root, policy, parserTarget: null, request: () => { throw new Error("network must not run"); } });
  assert.equal(resolved.path, object);
  fs.writeFileSync(object, "poisoned");
  assert.equal(hashFile(resolved.path, resolved.target, roots.objects), false, "wrapper final rehash detects post-resolution mutation");
});
test("parser policy is tied to the active plugin version and verified sibling bytes", async () => {
  const pluginRoot = path.resolve(__dirname, "..");
  const current = loadPolicy(pluginRoot);
  const parser = loadParserTarget(pluginRoot, current.package_version, "win32-x64");
  assert.equal(parser.asset, "uci-parser-windows-amd64.exe");
  assert.throws(() => loadParserTarget(pluginRoot, "6.50.1", "win32-x64"), /does not match/);
  const root = tempRoot();
  const roots = objectRoots(root);
  const client = objectPath(roots, selected().target.desired);
  fs.writeFileSync(client, fixture().bytes);
  const bytes = Buffer.from("verified parser bytes");
  const target = { version: "6.47.0", asset: parser.asset, size: bytes.length, sha256: digest(bytes) };
  fs.writeFileSync(objectPath(roots, target), bytes);
  const installed = await installParser(roots, client, target, { request: () => { throw new Error("network must not run"); } });
  assert.equal(installed, path.join(path.dirname(client), "parser", `parser${path.extname(client)}`));
  assert.equal(hashFile(installed, target, roots.objects), true);
  fs.unlinkSync(installed);
  fs.writeFileSync(installed, "poisoned");
  await assert.rejects(installParser(roots, client, target, {}), /verification/);
});
