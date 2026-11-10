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
      build_contract: { go_version: "1.26.10", trimpath: true, buildvcs: false, client_cgo: false, daemon_version_ldflag: `v${version}` },
    },
  };
}
function tempRoot() { return fs.mkdtempSync(path.join(os.tmpdir(), "engram-bootstrap-")); }

const selected = () => parsePolicy(JSON.stringify(fixture().policy), "6.47.0");

function enableMockTimeouts(t) {
  try { t.mock.timers.enable({ apis: ["setTimeout"] }); }
  catch (error) {
    if (error.code !== "ERR_INVALID_ARG_TYPE") throw error;
    t.mock.timers.enable(["setTimeout"]);
  }
}

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

test("attestation revocations feed generated policy without authorizing a predecessor", () => {
  const { createPolicy, parseMarkerlessAttestation } = require("./bootstrap-policy.js");
  const { policy } = fixture();
  const target = policy.targets["win32-x64"].desired;
  const raw = JSON.stringify({ schema_version: 1, revoked_sha256: [target.sha256], windows_amd64: [{ version: "v6.46.9", daemon_compat_epoch: 1, size: target.size, sha256: target.sha256 }] });
  const source = parseMarkerlessAttestation(raw);
  const targets = Object.fromEntries(Object.entries(policy.targets).map(([key, value]) => [key, value.desired]));
  assert.throws(() => createPolicy("6.47.0", targets, source.revoked_sha256), /revoked/);
  assert.throws(() => parseMarkerlessAttestation(raw.replace('"schema_version":1', '"schema_version":1,"schema_version":1')), /duplicate/i);
  assert.equal(createPolicy("6.47.0", targets, []).targets["win32-x64"].predecessor, null);
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

test("object verification rejects poisoned, oversized, truncated, directory, and reparse objects", (t) => {
  const root = tempRoot();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const roots = objectRoots(root);
  const target = selected().target.desired;
  const object = objectPath(roots, target);
  fs.writeFileSync(object, "wrong");
  assert.equal(verifyObject(roots, target), "");
  assert.equal(fs.existsSync(object), false, "wrong object is quarantined rather than overwritten");
  const tampered = Buffer.from(fixture().bytes);
  tampered[0] ^= 1;
  fs.writeFileSync(object, tampered);
  assert.equal(verifyObject(roots, target), "", "same-length bytes with a different digest must be rejected");
  assert.equal(fs.existsSync(object), false, "same-length poisoned object is quarantined");
  fs.writeFileSync(object, Buffer.concat([fixture().bytes, Buffer.from("x")]));
  assert.equal(verifyObject(roots, target), "");
  fs.mkdirSync(object);
  assert.throws(() => verifyObject(roots, target), BootstrapError);
  fs.symlinkSync(path.join(root, "outside"), object, process.platform === "win32" ? "file" : undefined);
  assert.throws(() => verifyObject(roots, target), BootstrapError);
});

test("legacy canonical bytes are imported only after exact hashing and are never authority", (t) => {
  const root = tempRoot();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const roots = objectRoots(root);
  const target = selected().target.desired;
  const legacy = path.join(roots.bin, process.platform === "win32" ? "engram.exe" : "engram");
  const tampered = Buffer.from(fixture().bytes);
  tampered[0] ^= 1;
  fs.writeFileSync(legacy, tampered);
  assert.equal(importLegacy(roots, target), "", "same-length legacy bytes with a different digest must be rejected");
  assert.equal(fs.existsSync(objectPath(roots, target)), false, "invalid legacy bytes must not be published");
  assert.deepEqual(fs.readdirSync(roots.staging), []);
  assert.deepEqual(fs.readFileSync(legacy), tampered, "invalid legacy bytes must remain unchanged");
  fs.writeFileSync(legacy, fixture().bytes);
  fs.chmodSync(legacy, 0o444); // Models a legacy file the importer must only read.
  const imported = importLegacy(roots, target);
  assert.equal(hashFile(imported, target, roots.objects), true);
  assert.equal(fs.readFileSync(legacy, "utf8"), "trusted client bytes");
});

test("sequential publication of identical stages returns the same hash-verified object", () => {
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

test("direct and release API requests use the configured HTTPS global agent", async () => {
  const https = require("node:https");
  const originalAgent = https.globalAgent;
  const originalAddRequest = Object.getOwnPropertyDescriptor(https.Agent.prototype, "addRequest");
  const configuredAgent = new https.Agent();
  const hosts = [];
  let freshAgentCalls = 0;
  configuredAgent.addRequest = (req, options) => {
    hosts.push(options.hostname);
    process.nextTick(() => req.emit("error", new Error("configured agent sentinel")));
  };
  https.Agent.prototype.addRequest = (req) => {
    freshAgentCalls++;
    process.nextTick(() => req.emit("error", new Error("unexpected fresh agent sentinel")));
  };
  https.globalAgent = configuredAgent;
  try {
    const target = selected().target.desired;
    for (const [url, releaseApi] of [
      [`https://github.com/thebtf/engram/releases/download/v${target.version}/${target.asset}`, undefined],
      [`https://api.github.com/repos/thebtf/engram/releases/tags/v${target.version}`, "metadata"],
      ["https://api.github.com/repos/thebtf/engram/releases/assets/12345", "asset"],
    ]) {
      await assert.rejects(requestStream(url, target, https.request, 0, { releaseApi }), /download request failed/);
    }
    assert.deepEqual(hosts, ["github.com", "api.github.com", "api.github.com"]);
    assert.equal(freshAgentCalls, 0);
  } finally {
    https.globalAgent = originalAgent;
    if (originalAddRequest) Object.defineProperty(https.Agent.prototype, "addRequest", originalAddRequest);
    else delete https.Agent.prototype.addRequest;
    configuredAgent.destroy();
  }
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

function releaseApiFixture(target, options = {}) {
  const direct = `https://github.com/thebtf/engram/releases/download/v${target.version}/${target.asset}`;
  const metadataUrl = `https://api.github.com/repos/thebtf/engram/releases/tags/v${target.version}`;
  const assetUrl = "https://api.github.com/repos/thebtf/engram/releases/assets/12345";
  const cdnUrl = "https://release-assets.githubusercontent.com/fixture?signature=fixture-secret";
  const metadata = {
    tag_name: `v${target.version}`,
    assets: [{ id: 12345, name: target.asset, size: target.size, digest: null, url: assetUrl }],
  };
  options.mutateMetadata?.(metadata);
  const metadataBytes = options.metadataBytes ?? Buffer.from(JSON.stringify(metadata));
  const payload = options.payload ?? fixture().bytes;
  const calls = [];
  const request = (url, requestOptions, callback) => {
    const href = url.href;
    calls.push(href);
    assert.equal(url.username, "");
    assert.equal(url.password, "");
    assert.equal(requestOptions.auth, undefined);
    const headers = Object.fromEntries(Object.entries(requestOptions.headers || {}).map(([key, value]) => [key.toLowerCase(), value]));
    assert.equal(headers.authorization, undefined);
    assert.equal(headers.cookie, undefined);
    assert.equal(headers["proxy-authorization"], undefined);
    const req = new EventEmitter();
    req.end = () => {
      let statusCode;
      let headers = {};
      let bytes = Buffer.alloc(0);
      if (href === direct) {
        statusCode = options.directStatus ?? 504;
      } else if (href === metadataUrl) {
        statusCode = 200;
        bytes = metadataBytes;
        headers = { "content-length": String(bytes.length), "content-type": "application/json" };
      } else if (href === assetUrl) {
        assert.match(String(requestOptions.headers?.Accept ?? requestOptions.headers?.accept ?? ""), /application\/octet-stream/i);
        statusCode = options.assetRedirect === undefined ? 200 : 302;
        if (statusCode === 302) headers = { location: options.assetRedirect };
        else { bytes = payload; headers = { "content-length": String(target.size) }; }
      } else if (href === cdnUrl) {
        statusCode = 200;
        bytes = payload;
        headers = { "content-length": String(target.size) };
      } else {
        throw new Error("unexpected request destination");
      }
      callback(Object.assign(Readable.from([bytes]), { statusCode, headers }));
    };
    return req;
  };
  return { request, calls, direct, metadataUrl, assetUrl, cdnUrl };
}

test("release API fallback publishes policy-verified bytes after gateway 504", async (t) => {
  for (const transport of ["asset 200", "asset 302", "asset 200 with digest"]) {
    await t.test(transport, async () => {
      const roots = objectRoots(tempRoot());
      const target = selected().target.desired;
      const options = transport === "asset 302" ? { assetRedirect: "https://release-assets.githubusercontent.com/fixture?signature=fixture-secret" } : {};
      if (transport === "asset 200 with digest") options.mutateMetadata = (data) => { data.assets[0].digest = `sha256:${target.sha256}`; };
      const api = releaseApiFixture(target, options);
      const published = await downloadObject(roots, target, { request: api.request });
      assert.equal(published, objectPath(roots, target));
      assert.deepEqual(fs.readFileSync(published), fixture().bytes);
      assert.equal(hashFile(published, target, roots.objects), true);
      assert.deepEqual(fs.readdirSync(roots.staging), []);
      assert.deepEqual(api.calls, transport === "asset 302" ? [api.direct, api.metadataUrl, api.assetUrl, api.cdnUrl] : [api.direct, api.metadataUrl, api.assetUrl]);
    });
  }
});

test("release API fallback fails closed on invalid metadata, bytes and redirects", async (t) => {
  const tampered = Buffer.from(fixture().bytes);
  tampered[0] ^= 1;
  const cases = [
    ["wrong tag", { mutateMetadata: (data) => { data.tag_name = "v6.47.1"; } }, false],
    ["wrong asset name", { mutateMetadata: (data) => { data.assets[0].name = "other"; } }, false],
    ["wrong asset size", { mutateMetadata: (data) => { data.assets[0].size++; } }, false],
    ["contradictory digest", { mutateMetadata: (data) => { data.assets[0].digest = `sha256:${"0".repeat(64)}`; } }, false],
    ["malformed metadata", { metadataBytes: Buffer.from('{"url":"https://release-assets.githubusercontent.com/file?signature=fixture-secret"') }, false],
    ["oversized metadata", { mutateMetadata: (data) => { data.padding = "x".repeat(8 * 1024 * 1024); } }, false],
    ["tampered bytes with null digest", { payload: tampered }, true],
    ["untrusted redirect", { assetRedirect: "https://evil.invalid/file?signature=fixture-secret" }, true],
    ["malformed redirect", { assetRedirect: "http://[invalid?signature=fixture-secret" }, true],
  ];
  for (const [name, options, reachesAsset] of cases) {
    await t.test(name, async () => {
      const roots = objectRoots(tempRoot());
      const target = selected().target.desired;
      const api = releaseApiFixture(target, options);
      let failure;
      try { await downloadObject(roots, target, { request: api.request }); }
      catch (error) { failure = error; }
      assert.ok(failure, "acquisition must fail");
      assert.notEqual(failure.code, "ERR_ASSERTION", "fixture assertions must not be mistaken for safe refusal");
      assert.doesNotMatch(String(failure.message), /unexpected request destination/);
      assert.doesNotMatch(String(failure.stack || failure), /fixture-secret/);
      assert.equal(fs.existsSync(objectPath(roots, target)), false);
      assert.deepEqual(fs.readdirSync(roots.staging), []);
      assert.equal(api.calls[0], api.direct);
      assert.ok(api.calls.includes(api.metadataUrl), "metadata must be reached before refusal");
      assert.equal(api.calls.includes(api.assetUrl), reachesAsset);
      assert.equal(api.calls.length, reachesAsset ? 3 : 2);
    });
  }
});

test("release API fallback does not retry non-retryable status or cancelled input", async () => {
  for (const status of [400, 401, 403, 404]) {
    const roots = objectRoots(tempRoot());
    const target = selected().target.desired;
    const api = releaseApiFixture(target, { directStatus: status });
    await assert.rejects(downloadObject(roots, target, { request: api.request }), BootstrapError);
    assert.deepEqual(api.calls, [api.direct]);
    assert.equal(fs.existsSync(objectPath(roots, target)), false);
    assert.deepEqual(fs.readdirSync(roots.staging), []);
  }
  const roots = objectRoots(tempRoot());
  const target = selected().target.desired;
  const api = releaseApiFixture(target);
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(downloadObject(roots, target, { request: api.request, signal: controller.signal }), BootstrapError);
  assert.deepEqual(api.calls, []);
  assert.equal(fs.existsSync(objectPath(roots, target)), false);
  assert.deepEqual(fs.readdirSync(roots.staging), []);
});
test("cold-cache stream outlives the old limit but not the bounded bootstrap deadline", async (t) => {
  enableMockTimeouts(t);
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
  assert.throws(() => loadParserTarget(pluginRoot, "0.0.0", "win32-x64"), /does not match/);
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

async function observeCancelledAcquisition(t, mode, launch) {
  const http = require("node:http");
  const wallTimeout = setTimeout;
  const deadlineStartedAt = Date.now();
  let deadlineNow = deadlineStartedAt;
  if (mode === "deadline") {
    enableMockTimeouts(t);
    t.mock.method(Date, "now", () => deadlineNow);
  }
  const root = tempRoot();
  const roots = objectRoots(root);
  const policy = selected();
  const target = policy.target.desired;
  const controller = new AbortController();
  let requestSeen;
  const started = new Promise((resolve) => { requestSeen = resolve; });
  let responseClosed = false;
  let responseDidClose;
  const closed = new Promise((resolve) => { responseDidClose = resolve; });
  let clientRequest;
  let requests = 0;
  let headersReceived = false;
  const server = http.createServer((_request, response) => {
    response.once("close", () => { responseClosed = true; responseDidClose(); });
    if (mode !== "deadline") {
      response.writeHead(200, { "Content-Length": target.size });
      response.write(fixture().bytes.subarray(0, 4));
    }
    if (mode === "deadline") requestSeen();
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const request = (_releaseURL, options, callback) => {
    requests++;
    // Only the transport is redirected; policy, streaming, hashing and publication stay real.
    clientRequest = http.request(`http://127.0.0.1:${server.address().port}`, options, (response) => {
      headersReceived = true;
      callback(response);
      if (mode !== "deadline") requestSeen();
    });
    return clientRequest;
  };
  let outcome = "pending";
  let errorMessage = "";
  const pending = launch({
    pluginRoot: root, pluginData: root, policy, parserTarget: null,
    request, signal: controller.signal, timeoutMs: mode === "deadline" ? 50 : 1000
  })
    .then((value) => { outcome = value === null ? "null" : "resolved"; },
      (error) => { outcome = "rejected"; errorMessage = error.message; });
  let snapshot;
  try {
    const began = await Promise.race([started.then(() => true), new Promise((resolve) => wallTimeout(() => resolve(false), 1000))]);
    assert.equal(began, true, "fixture did not receive the actual acquisition request; not cancellation RED");
    if (mode === "deadline") {
      assert.equal(outcome, "pending", "the acquisition must be in flight before advancing its deadline");
      assert.equal(headersReceived, false, "deadline coverage must precede response headers");
      assert.equal(Date.now(), deadlineStartedAt, "cold preflight must not consume the frozen deadline");
      deadlineNow += 50;
      t.mock.timers.tick(50);
    } else controller.abort();
    await Promise.race([pending, new Promise((resolve) => wallTimeout(resolve, 300))]);
    // A rejection alone is insufficient: the owned HTTP request must actually close.
    if (outcome !== "pending" && !responseClosed) {
      await Promise.race([closed, new Promise((resolve) => wallTimeout(resolve, 300))]);
    }
    snapshot = {
      outcome, requestDestroyed: clientRequest.destroyed, responseClosed,
      requests, published: fs.existsSync(objectPath(roots, target)), staging: fs.readdirSync(roots.staging).length
    };
    t.diagnostic(JSON.stringify({ mode, ...snapshot, errorMessage }));
  } finally {
    clientRequest?.destroy(new Error("owned lifecycle fixture closed"));
    server.closeAllConnections();
    await pending;
    await new Promise((resolve) => server.close(resolve));
    assert.equal(fs.existsSync(objectPath(roots, target)), false, "cancelled acquisition published an object");
    assert.deepEqual(fs.readdirSync(roots.staging), [], "owned staging survives request closure");
    fs.rmSync(root, { recursive: true, force: true });
  }
  assert.deepEqual(snapshot, {
    outcome: mode === "callback" ? "null" : "rejected",
    requestDestroyed: true, responseClosed: true, requests: 1, published: false, staging: 0
  },
    "supplied cancellation/deadline must settle acquisition and close its HTTP request before fixture cleanup");
  if (mode !== "callback") assert.match(errorMessage, /abort|cancel|deadline|timeout/i);
}

test("plugin lifecycle: supplied abort closes trusted acquisition during the body", async (t) => {
  await observeCancelledAcquisition(t, "abort", resolveForLaunch);
});

test("plugin lifecycle: supplied deadline closes trusted acquisition before response headers", async (t) => {
  await observeCancelledAcquisition(t, "deadline", resolveForLaunch);
});

test("public hook resolver cancels cold acquisition without late project execution", async (t) => {
  const { resolveHookProjectContext } = require("../hooks/lib.js");
  let executions = 0;
  await observeCancelledAcquisition(t, "callback", (fixtureOptions) =>
    resolveHookProjectContext(fixtureOptions.pluginRoot, {
      ...fixtureOptions,
      resolve: (forwarded) => resolveForLaunch({ ...fixtureOptions, ...forwarded, policy: fixtureOptions.policy, parserTarget: null }),
      execFile() { executions++; throw new Error("cancelled resolver must not execute a client"); },
    }).catch(() => null));
  assert.equal(executions, 0);
});

test("plugin lifecycle: cache hits retain hashing and already-aborted or expired acquisition starts no request", async (t) => {
  const root = tempRoot();
  const roots = objectRoots(root);
  const policy = selected();
  const target = policy.target.desired;
  const object = objectPath(roots, target);
  let requests = 0;
  const options = {
    pluginRoot: root, pluginData: root, policy, parserTarget: null,
    request() { requests++; throw new Error("owned fixture forbids network"); }
  };
  const controller = new AbortController();
  controller.abort();
  try {
    fs.writeFileSync(object, fixture().bytes);
    assert.equal((await resolveForLaunch(options)).path, object);
    assert.equal(hashFile(object, target, roots.objects), true);
    const cached = await resolveForLaunch({ ...options, signal: controller.signal }).then(() => "resolved", () => "rejected");
    assert.equal(hashFile(object, target, roots.objects), true, "cancellation must not damage a verified cache hit");
    fs.writeFileSync(object, "poisoned");
    const poisoned = await resolveForLaunch({ ...options, signal: controller.signal }).then(() => "resolved", () => "rejected");
    const expired = await resolveForLaunch({ ...options, deadlineAt: Date.now() - 1 }).then(() => "resolved", () => "rejected");
    t.diagnostic(JSON.stringify({ cached, poisoned, expired, requests }));
    assert.deepEqual({ cached, poisoned, expired, requests }, { cached: "rejected", poisoned: "rejected", expired: "rejected", requests: 0 });
    assert.deepEqual(fs.readdirSync(roots.staging), []);
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});

test("verify-only resolution accepts exact desired CAS but never imports or downloads missing or tampered bytes", async () => {
  for (const mode of ["missing", "tampered", "trusted"]) {
    const root = tempRoot();
    const roots = objectRoots(root), policy = selected(), target = policy.target.desired;
    const object = objectPath(roots, target);
    let requests = 0;
    try {
      fs.writeFileSync(path.join(roots.bin, process.platform === "win32" ? "engram.exe" : "engram"), fixture().bytes);
      if (mode !== "missing") {
        const bytes = Buffer.from(fixture().bytes);
        if (mode === "tampered") bytes[0] ^= 1;
        fs.writeFileSync(object, bytes);
      }
      const pending = resolveForLaunch({
        pluginRoot: root, pluginData: root, policy, parserTarget: null, acquire: false,
        request() { requests++; throw new Error("verify-only must not open a release request"); },
      });
      if (mode === "trusted") {
        assert.equal((await pending).path, object);
        assert.equal(hashFile(object, target, roots.objects), true);
      } else {
        await assert.rejects(pending, /HOOK_CLIENT_UNAVAILABLE/);
        assert.equal(hashFile(object, target, roots.objects), false);
      }
      assert.equal(requests, 0, mode);
      assert.deepEqual(fs.readdirSync(roots.staging), []);
      assert.deepEqual(fs.readdirSync(roots.quarantine), [], "short verify-only callbacks leave invalid bytes for readiness diagnosis");
    } finally { fs.rmSync(root, { recursive: true, force: true }); }
  }
});

test('read-only project inspection never creates missing cache directories or imports a client', async () => {
  const root = tempRoot();
  try {
    await assert.rejects(resolveForLaunch({
      pluginRoot: root, pluginData: root, policy: selected(), parserTarget: null, acquire: false, check: true,
      request() { assert.fail('read-only inspection cannot download'); },
    }), /HOOK_CLIENT_UNAVAILABLE/);
    assert.equal(fs.existsSync(path.join(root, 'bin')), false);
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});
