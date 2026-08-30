"use strict";

const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const { EventEmitter } = require("node:events");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const {
  CALLBACK_MAX_TIME_SECONDS,
  MCP_SEQUENCE,
  ProbeError,
  assertSafeEvidence,
  callbackEnvironment,
  callbackSpec,
  classifyDaemonRuntime,
  createExclusiveDirectory,
  discoverMuxcoreArtifacts,
  inspectInstalledArtifacts,
  parseArgs,
  parseBootstrapPolicy,
  resolveProfileFiles,
  runDaemonMcp,
  runProbe,
  safeRequestProjection,
  sessionTranscriptProjection,
  writeJsonExclusive,
} = require("./omp-capability-probe.js");

function sha(value) {
  return crypto.createHash("sha256").update(value).digest("hex");
}

function temporaryRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "hap01-probe-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

function writeJson(filePath, value) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  fs.writeFileSync(filePath, JSON.stringify(value), "utf8");
}

function fixtureFiles(t) {
  const root = temporaryRoot(t);
  const pluginRoot = path.join(root, "plugin");
  const pluginData = path.join(root, "plugin-data");
  const configuration = path.join(root, "config.json");
  const bytes = Buffer.from("installed daemon fixture bytes");
  const target = {
    version: "6.48.0",
    asset: "engram-windows-amd64.exe",
    size: bytes.length,
    sha256: sha(bytes),
  };
  writeJson(path.join(pluginRoot, "package.json"), {
    name: "engram",
    version: "6.48.0",
    omp: { extensions: ["./extensions/engram-memory.mjs"] },
  });
  writeJson(path.join(pluginRoot, ".omp-plugin", "plugin.json"), {
    name: "engram",
    version: "6.48.0",
    mcpServers: { engram: { type: "stdio", command: "node", args: ["./scripts/run-engram.js"], cwd: ".", timeout: 60000 } },
  });
  fs.mkdirSync(path.join(pluginRoot, "extensions"), { recursive: true });
  fs.writeFileSync(path.join(pluginRoot, "extensions", "engram-memory.mjs"), "export default () => {};", "utf8");
  writeJson(path.join(pluginRoot, "bootstrap-targets.json"), {
    schema_version: 1,
    package_version: "6.48.0",
    targets: { "win32-x64": { desired: target } },
  });
  const object = path.join(pluginData, "bin", "objects", "sha256", target.sha256, target.asset);
  fs.mkdirSync(path.dirname(object), { recursive: true });
  fs.writeFileSync(object, bytes);
  writeJson(configuration, { server_url: "http://fixture.invalid:37777", api_token: "fixture-token" });
  const registry = path.join(root, "installed_plugins.json");
  const lock = path.join(root, "omp-plugins.lock.json");
  writeJson(registry, {
    profiles: {
      fixture: {
        plugins: {
          "engram@engram": [{ version: "6.48.0", installPath: pluginRoot, dataPath: pluginData, configPath: configuration }],
        },
        enabledPlugins: { "engram@engram": true },
      },
    },
  });
  writeJson(lock, {
    profiles: {
      fixture: {
        plugins: { "engram@engram": [{ version: "6.48.0", installPath: pluginRoot }] },
      },
    },
  });
  return { root, pluginRoot, pluginData, configuration, registry, lock, object, target };
}

function profileFiles(files) {
  return {
    registryPath: files.registry,
    lockPath: files.lock,
    registryEntry: { version: "6.48.0", installPath: files.pluginRoot, dataPath: files.pluginData, configPath: files.configuration, enabled: true },
    lockEntry: { version: "6.48.0", installPath: files.pluginRoot, dataPath: "", configPath: "", enabled: true },
  };
}

function baseDeps(overrides = {}) {
  return {
    fs,
    path,
    os,
    platform: "win32",
    arch: "x64",
    env: {},
    now: () => 100,
    randomBytes: () => Buffer.alloc(16, 7),
    ...overrides,
  };
}

test("requires contained evidence paths and exclusive owned directories", (t) => {
  const root = temporaryRoot(t);
  const run = "safe-run";
  const options = parseArgs(["--profile", "fixture", "--run-id", run, "--evidence-dir", `.agent/runs/hap-01/${run}`], root, path);
  assert.equal(options.evidence_dir, path.join(root, ".agent", "runs", "hap-01", run));
  assert.throws(() => parseArgs(["--profile", "fixture", "--run-id", run, "--evidence-dir", "../escape"], root, path), ProbeError);
  const deps = baseDeps();
  createExclusiveDirectory(options.evidence_dir, deps);
  assert.throws(() => createExclusiveDirectory(options.evidence_dir, deps), /owned run directory already exists/);
  writeJsonExclusive(path.join(options.evidence_dir, "receipt.json"), { schema: "fixture" }, deps);
  assert.throws(() => writeJsonExclusive(path.join(options.evidence_dir, "receipt.json"), { schema: "fixture" }, deps), /evidence file already exists/);
});

test("rejects registry and lock version mismatches", (t) => {
  const files = fixtureFiles(t);
  writeJson(files.lock, { profiles: { fixture: { plugins: { "engram@engram": [{ version: "6.47.9", installPath: files.pluginRoot }] } } } });
  assert.throws(
    () => resolveProfileFiles("fixture", baseDeps({ env: { OMP_PROFILE_REGISTRY: files.registry, OMP_PLUGIN_LOCK: files.lock } })),
    (error) => error instanceof ProbeError && error.code === "PROFILE_LOCK_MISMATCH",
  );
});
test("discovers the active OMP profile layout and lock plugin alias", (t) => {
  const files = fixtureFiles(t);
  const profileRoot = path.join(files.root, ".omp", "profiles", "fixture");
  const pluginsRoot = path.join(profileRoot, "plugins");
  const registry = path.join(pluginsRoot, "installed_plugins.json");
  const lock = path.join(pluginsRoot, "omp-plugins.lock.json");
  writeJson(registry, {
    version: 2,
    plugins: { "engram@engram": [{ version: "6.48.0", installPath: files.pluginRoot }] },
  });
  writeJson(lock, { plugins: { engram: { version: "6.48.0", enabled: true } }, settings: {} });
  const resolved = resolveProfileFiles("fixture", baseDeps({
    env: { PI_CODING_AGENT_DIR: path.join(profileRoot, "agent") },
  }));
  assert.equal(resolved.registryPath, registry);
  assert.equal(resolved.lockPath, lock);
  assert.equal(resolved.registryEntry.version, "6.48.0");
  assert.equal(resolved.lockEntry.version, "6.48.0");
});

test("authenticates the policy-selected object rather than trusting its location", (t) => {
  const files = fixtureFiles(t);
  const inspected = inspectInstalledArtifacts(profileFiles(files), baseDeps());
  assert.equal(inspected.artifact.daemon_object_sha256, files.target.sha256);
  fs.writeFileSync(files.object, "tampered installed daemon");
  assert.throws(
    () => inspectInstalledArtifacts(profileFiles(files), baseDeps()),
    (error) => error instanceof ProbeError && error.code === "POLICY_OBJECT_MISMATCH",
  );
  assert.throws(
    () => parseBootstrapPolicy(Buffer.from(JSON.stringify({ schema_version: 1, package_version: "6.48.1", targets: {} })), "6.48.0", "win32", "x64"),
    (error) => error instanceof ProbeError && error.code === "BOOTSTRAP_POLICY_INVALID",
  );
});

test("clears inherited credentials and keeps request projection structural", () => {
  const environment = callbackEnvironment({
    ENGRAM_TOKEN: "inherited-token",
    CLAUDE_PLUGIN_OPTION_API_TOKEN: "another-token",
    NODE_OPTIONS: "--require unsafe.js",
    KEEP: "safe",
  }, "http://127.0.0.1:1234", "synthetic-token");
  assert.deepEqual(environment.env, {
    KEEP: "safe",
    ENGRAM_URL: "http://127.0.0.1:1234",
    ENGRAM_TOKEN: "synthetic-token",
    ENGRAM_QUIET: "0",
  });
  const projection = safeRequestProjection(
    { url: "/api/context/session-start?leak=never", method: "POST", headers: { authorization: "Bearer raw-secret" } },
    JSON.stringify({ prompt: "raw prompt", session_id: "session-1", nested: { source: "private" } }),
    sha("body"),
    1,
    10,
    200,
    baseDeps({ now: () => 35 }),
  );
  assert.deepEqual(projection.body_keys, ["nested", "prompt", "session_id"]);
  assert.deepEqual(projection.field_lengths, { nested: 1, prompt: 10, session_id: 9 });
  assert.equal(projection.authorization_present, true);
  assert.doesNotMatch(JSON.stringify(projection), /raw-secret|raw prompt|private/);
  assert.throws(() => assertSafeEvidence({ token: "never" }), /prohibited raw field/);
});

test("projects transcript metadata without retaining model text", (t) => {
  const root = temporaryRoot(t);
  fs.writeFileSync(path.join(root, "session.jsonl"), `${JSON.stringify({
    type: "custom_message",
    customType: "engram-memory",
    content: "UNTRUSTED_REFERENCE customer text",
  })}\n`, "utf8");
  const projection = sessionTranscriptProjection(root, baseDeps());
  assert.equal(projection.custom_message_count, 1);
  assert.equal(projection.untrusted_reference_present, true);
  assert.equal(projection.tool_control_semantics_present, false);
  assert.doesNotMatch(JSON.stringify(projection), /customer text/);
});

test("correlates muxcore marker and descriptor through hashes only", (t) => {
  const root = temporaryRoot(t);
  writeJson(path.join(root, "muxcore-daemon-registry", "engram-fixture.json"), { engine_name: "engram", pid: 42, daemon_control_path: "private-control" });
  writeJson(path.join(root, "engram-muxd.ctl.sock.marker.json"), {
    schema_version: 2,
    pid: 42,
    daemon_generation: "private-generation",
    exe: "C:/private/engram.exe",
  });
  const projection = discoverMuxcoreArtifacts(baseDeps({ tempDir: root }), true);
  assert.equal(projection.correlated, true);
  assert.equal(projection.descriptor.reachable, true);
  assert.match(projection.marker.marker_binding_sha256, /^[a-f0-9]{64}$/);
  assert.doesNotMatch(JSON.stringify(projection), /private-generation|private-control|C:\//i);
});

test("MCP sends tools/list only after local initialize and requires it for daemon proof", async () => {
  const child = new EventEmitter();
  child.stdout = new EventEmitter();
  child.stderr = new EventEmitter();
  child.kill = () => true;
  const writes = [];
  child.stdin = {
    write(line) {
      const message = JSON.parse(line);
      writes.push(message);
      if (message.id === 1) queueMicrotask(() => child.stdout.emit("data", Buffer.from('{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{}}}\n')));
      if (message.id === 2) queueMicrotask(() => {
        child.stdout.emit("data", Buffer.from('{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}\n'));
        child.emit("close", 0);
      });
    },
    end() { },
  };
  const result = await runDaemonMcp({ command: "installed-daemon", cwd: ".", env: {}, timeout_ms: 1000 }, baseDeps({
    spawn: () => child,
    setTimeout,
    clearTimeout,
  }));
  assert.deepEqual(result.sequence, MCP_SEQUENCE);
  assert.deepEqual(writes.map((message) => message.method), MCP_SEQUENCE);
  assert.equal(classifyDaemonRuntime(result).state, "UNAVAILABLE");
  assert.equal(classifyDaemonRuntime({
    sequence: ["initialize"],
    tools_list: { received: false, error_code: null },
    authenticated_subject_proof_sha256: sha("subject"),
  }).state, "UNAVAILABLE");
});

test("probe preserves evidence while cleaning scratch and leaves profile inputs unchanged", async (t) => {
  const files = fixtureFiles(t);
  const options = {
    cwd: files.root,
    profile: "fixture",
    run_id: "probe-run",
    evidence_dir: path.join(files.root, ".agent", "runs", "hap-01", "probe-run"),
    scratch_dir: path.join(files.root, ".agent", "tmp", "hap-01", "probe-run"),
  };
  createExclusiveDirectory(options.evidence_dir, baseDeps());
  const originalRegistry = fs.readFileSync(files.registry, "utf8");
  const originalLock = fs.readFileSync(files.lock, "utf8");
  const originalConfig = fs.readFileSync(files.configuration, "utf8");
  const fixture = {
    requests: [
      { order: 1, method: "POST", path: "/api/context/inject", authorization_present: true, body_keys: ["project"], field_lengths: { project: 7 }, body_sha256: sha("one"), status: 200, timing_ms: 1 },
      { order: 2, method: "POST", path: "/api/context/session-start", authorization_present: true, body_keys: ["project", "session_id"], field_lengths: { project: 7, session_id: 9 }, body_sha256: sha("two"), status: 200, timing_ms: 2 },
      { order: 3, method: "POST", path: "/api/hooks/ambient-candidates", authorization_present: true, body_keys: ["prompt", "session_id"], field_lengths: { prompt: 20, session_id: 9 }, body_sha256: sha("three"), status: 200, timing_ms: 3 },
    ],
    async start() { return { port: 43123 }; },
    async close() { },
  };
  const result = await runProbe(options, baseDeps({
    env: { ENGRAM_URL: "http://fixture.invalid:37777", ENGRAM_TOKEN: "real-token" },
    resolveProfileFiles: () => profileFiles(files),
    resolveActiveConfig: () => ({ endpoint: "http://fixture.invalid:37777", token: "real-token", configPath: files.configuration, config_digest: sha(originalConfig) }),
    createFixture: () => fixture,
    runChild: async (spec) => {
      assert.deepEqual(spec.args, ["--profile", "fixture", "--session-dir", options.scratch_dir, "--no-tools", "--max-time", String(CALLBACK_MAX_TIME_SECONDS), "-p", "Reply exactly HAP01_CALLBACK_SENTINEL."]);
      assert.equal(spec.env.NODE_OPTIONS, undefined);
      assert.equal(spec.args.includes("--extension"), false);
      assert.equal(spec.args.includes("--no-extensions"), false);
      fs.writeFileSync(path.join(spec.cwd, "transcript.jsonl"), `${JSON.stringify({ type: "custom_message", customType: "engram-memory", content: "HAP01_REFERENCE_DATA" })}\n`, "utf8");
      return { started: true, error: false, exit_code: 0, timed_out: false, close_unconfirmed: false, stdout_bytes: 0, stderr_bytes: 0, sentinel_present: false };
    },
    runDaemon: async () => ({
      sequence: MCP_SEQUENCE,
      initialize: { received: true, error_code: null, result_keys: ["serverInfo"], tools_count: null, content_sha256: sha("initialize") },
      tools_list: { received: true, error_code: null, result_keys: ["tools"], tools_count: 0, content_sha256: sha("tools") },
      authenticated_subject_proof_sha256: null,
      child: { started: true, error: false, exit_code: 0, timed_out: false, close_unconfirmed: false, stderr_bytes: 0 },
    }),
    tempDir: path.join(files.root, "tmp"),
  }));
  assert.equal(result.record.disposition, "UNQUALIFIED");
  assert.equal(result.record.guarantees.direct_credential_disabled.state, "CONTRADICTED");
  assert.equal(result.receipt.effects.profile_registry_unchanged, true);
  assert.equal(result.receipt.effects.configuration_unchanged, true);
  assert.equal(result.receipt.effects.scratch_cleaned, true);
  assert.equal(fs.existsSync(options.scratch_dir), false);
  assert.equal(fs.existsSync(path.join(options.evidence_dir, "receipt.json")), true);
  assert.equal(fs.existsSync(path.join(options.evidence_dir, "record.json")), true);
  assert.equal(fs.readFileSync(files.registry, "utf8"), originalRegistry);
  assert.equal(fs.readFileSync(files.lock, "utf8"), originalLock);
  assert.equal(fs.readFileSync(files.configuration, "utf8"), originalConfig);
  assert.doesNotMatch(JSON.stringify(result.receipt), /real-token|fixture-token|[A-Za-z]:[\\/]/i);
});
