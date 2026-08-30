"use strict";

const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const net = require("node:net");
const os = require("node:os");
const path = require("node:path");
const { spawn, spawnSync } = require("node:child_process");
const test = require("node:test");
const { EventEmitter } = require("node:events");
const { PassThrough } = require("node:stream");

const { buildQualificationRecord } = require("./omp-capability-record.js");
const {
  ADAPTER_REVISION,
  ARTIFACT_MANIFEST,
  bindPluginDaemonNamespace,
  ProbeError,
  adapterDigest,
  buildQualificationInput,
  childEnvironment,
  createExclusiveDirectory,
  createRelayTap,
  extractArchive,
  createServerTap,
  drainChildOutput,
  directoryTreeDigest,
  initializeScratchWorktree,
  trackScratchRepositoryAnchor,
  muxcoreProjectID,
  ompTurnArguments,
  inspectArtifactMatrix,
  parseArgs,
  parseFixtureSnapshot,
  resolveLinkedPluginList,
  runBoundedChild,
  scratchServerEnvironment,
  scratchDaemonEnvironment,
  seedInstalledClientObject,
  scratchOmpTurnEnvironment,
  scratchPluginDataEnvironment,
  stablePostgresProbeCount,
  telemetryDelta,
  scenarioObservation,
  writeScenarioProjectAnchor,
  runProbe,
  snapshotActiveProfile,
  turnIsComplete,
} = require("./omp-capability-probe.js");

const digest = (value) => crypto.createHash("sha256").update(value).digest("hex");
const gitObject = (character) => character.repeat(40);

function temporaryRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "hap01c-probe-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

function writeJson(filePath, value) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  fs.writeFileSync(filePath, `${JSON.stringify(value)}\n`, { mode: 0o600 });
}

function baseDeps(overrides = {}) {
  return {
    fs,
    net,
    path,
    env: {},
    platform: process.platform,
    arch: "x64",
    now: () => Date.now(),
    sleep: async () => { },
    randomBytes: (length) => Buffer.alloc(length, 7),
    randomUUID: crypto.randomUUID,
    cwd: () => process.cwd(),
    setTimeout,
    clearTimeout,
    ...overrides,
  };
}

function recordArtifact() {
  return {
    version: "6.49.0-rc.1",
    package_sha256: "1".repeat(64),
    extension_entry_sha256: "2".repeat(64),
    relay_helper_sha256: "3".repeat(64),
    adapter_sha256: "4".repeat(64),
    daemon_object_sha256: "5".repeat(64),
    fixture_object_sha256: digest("fixture bytes"),
    omp_command_sha256: digest("omp bytes"),
    postgres_image_sha256: "d".repeat(64),
    client_object_sha256: "6".repeat(64),
    bootstrap_targets_sha256: "7".repeat(64),
    baseline_source_commit: gitObject("b"),
    baseline_source_tree: gitObject("e"),
    baseline_plugin_install_tree_sha256: "c".repeat(64),
    baseline_client_object_sha256: "f".repeat(64),
    install_tree_sha256: "8".repeat(64),
    archives: [
      { name: "engram_6.49.0-rc.1_darwin_arm64.tar.gz", platform: "darwin", arch: "arm64", size: 1, sha256: "9".repeat(64) },
      { name: "engram_6.49.0-rc.1_linux_amd64.tar.gz", platform: "linux", arch: "amd64", size: 2, sha256: "a".repeat(64) },
      { name: "engram_6.49.0-rc.1_windows_amd64.zip", platform: "win32", arch: "amd64", size: 3, sha256: "b".repeat(64) },
    ],
  };
}

function matrixFixture() {
  const artifact = recordArtifact();
  return {
    candidate: {
      source_commit: gitObject("c"),
      source_tree: gitObject("d"),
      design_sha256: "e".repeat(64),
      adapter_revision: ADAPTER_REVISION,
      adapter_sha256: artifact.adapter_sha256,
    },
    fixture_object_sha256: digest("fixture bytes"),
    artifact,
    matrix_sha256: "f".repeat(64),
  };
}

function cleanCustody() {
  return {
    extension_url_present: false,
    extension_token_present: false,
    extension_config_path_present: false,
    authorization_seen: false,
    child_hap_config_present: true,
  };
}

function counts(overrides = {}) {
  return {
    callbacks_expected: 0,
    callbacks_observed: 0,
    route_attempts_expected: 0,
    route_attempts_observed: 0,
    deliveries_expected: 0,
    deliveries_observed: 0,
    direct_fallback_attempts: 0,
    rediscoveries: 0,
    server_dispatches: 0,
    telemetry_attempts: 0,
    cleanup_residue: 0,
    ...overrides,
  };
}

function scenarioFixture(id) {
  const common = { id, state: "OBSERVED", passed: true, shared_deadline: true, custody: cleanCustody(), subcases: [], reason_codes: [] };
  switch (id) {
    case "new_new_happy":
      return {
        ...common,
        counts: counts({ callbacks_expected: 2, callbacks_observed: 2, route_attempts_expected: 4, route_attempts_observed: 4, deliveries_expected: 2, deliveries_observed: 2, server_dispatches: 4, telemetry_attempts: 2 }),
        route_sequence: [
          "session_start:IDENTITY_REGISTRATION:OK",
          "session_start:SESSION_START_CONTEXT:OK",
          "before_agent_start:IDENTITY_REGISTRATION:OK",
          "before_agent_start:AMBIENT_CANDIDATES:OK",
        ],
      };
    case "new_plugin_old_daemon":
      return { ...common, counts: counts({ callbacks_expected: 2, callbacks_observed: 2 }), route_sequence: [] };
    case "old_plugin_new_daemon":
      return {
        ...common,
        counts: counts({ callbacks_expected: 2, callbacks_observed: 2, deliveries_expected: 2, deliveries_observed: 2, server_dispatches: 2 }),
        route_sequence: [],
        custody: { extension_url_present: true, extension_token_present: true, extension_config_path_present: false, authorization_seen: true, child_hap_config_present: false },
      };
    case "relay_outage":
      return {
        ...common,
        counts: counts({ callbacks_expected: 2, callbacks_observed: 2, route_attempts_expected: 2, route_attempts_observed: 2 }),
        route_sequence: ["session_start:IDENTITY_REGISTRATION:NO_DELIVERY", "before_agent_start:IDENTITY_REGISTRATION:NO_DELIVERY"],
      };
    case "stale_generation":
      return {
        ...common,
        counts: counts({ callbacks_expected: 2, callbacks_observed: 2, route_attempts_expected: 5, route_attempts_observed: 5, deliveries_expected: 2, deliveries_observed: 2, rediscoveries: 1, server_dispatches: 4, telemetry_attempts: 2 }),
        route_sequence: [
          "session_start:IDENTITY_REGISTRATION:NO_DELIVERY",
          "session_start:IDENTITY_REGISTRATION:OK",
          "session_start:SESSION_START_CONTEXT:OK",
          "before_agent_start:IDENTITY_REGISTRATION:OK",
          "before_agent_start:AMBIENT_CANDIDATES:OK",
        ],
      };
    case "capability_invalidation":
      return {
        ...common,
        counts: counts({ route_attempts_expected: 4, route_attempts_observed: 4 }),
        route_sequence: Array.from({ length: 4 }, () => "session_start:SESSION_START_CONTEXT:NO_DELIVERY"),
        subcases: ["adapter_mismatch", "expiry", "process_exit", "project_keycard_rotation"].map((subcase) => ({
          id: subcase,
          passed: true,
          route_attempts: 1,
          server_dispatches: 0,
          reason_codes: [],
        })),
      };
    case "rollback_future_turn":
      return {
        ...common,
        counts: counts({ callbacks_expected: 4, callbacks_observed: 4, route_attempts_expected: 6, route_attempts_observed: 6, deliveries_expected: 2, deliveries_observed: 2, server_dispatches: 4, telemetry_attempts: 2 }),
        route_sequence: [
          "session_start:IDENTITY_REGISTRATION:NO_DELIVERY",
          "before_agent_start:IDENTITY_REGISTRATION:NO_DELIVERY",
          "session_start:IDENTITY_REGISTRATION:OK",
          "session_start:SESSION_START_CONTEXT:OK",
          "before_agent_start:IDENTITY_REGISTRATION:OK",
          "before_agent_start:AMBIENT_CANDIDATES:OK",
        ],
      };
    default:
      throw new Error(`unknown fixture scenario ${id}`);
  }
}

function allScenarios() {
  return [
    "new_new_happy",
    "new_plugin_old_daemon",
    "old_plugin_new_daemon",
    "relay_outage",
    "stale_generation",
    "capability_invalidation",
    "rollback_future_turn",
  ].map(scenarioFixture);
}

function qualificationRecord(before = "a".repeat(64), after = before) {
  const matrix = matrixFixture();
  return buildQualificationRecord(buildQualificationInput(
    { run_id: "hap01c-run", platform: "win32", arch: "x64", scratch_profile: "hap01c-test" },
    matrix,
    allScenarios(),
    { sha256: before, file_count: 3 },
    { sha256: after, file_count: 3 },
    0,
  ));
}

function physicalEndpoint(logical) {
  if (process.platform !== "win32") return logical;
  return `\\\\.\\pipe\\mcp-mux-${digest(logical.toLowerCase()).slice(0, 32)}`;
}

function listen(server, endpoint) {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(endpoint, () => resolve());
  });
}

function close(server) {
  return new Promise((resolve) => server.close(resolve));
}

function socketRequest(endpoint, bytes) {
  return new Promise((resolve, reject) => {
    const socket = net.createConnection({ path: endpoint });
    let response = Buffer.alloc(0);
    socket.once("error", reject);
    socket.once("connect", () => socket.write(bytes));
    socket.on("data", (chunk) => { response = Buffer.concat([response, Buffer.from(chunk)]); });
    socket.once("end", () => resolve(response));
  });
}

function relayFrame(generation, route = "IDENTITY_REGISTRATION") {
  return Buffer.from(`${JSON.stringify({
    protocol: "engram-legacy-relay/1",
    requestId: "raw-request-id-never-retained",
    daemonGeneration: generation,
    adapter: { revision: ADAPTER_REVISION, installedArtifactSha256: "a".repeat(64) },
    route,
    deadlineUnixMs: Date.now() + 5_000,
    body: {
      hostSessionRef: "raw-host-session-never-retained",
      projectIdentityV3: {
        version: 3,
        anchor_project_id: "11111111-1111-4111-8111-111111111111",
        name: "fixture",
        scope: "directory",
        normalized_git_remotes: [],
        legacy_identifiers: [],
        client_instance_id: "fixture-client",
      },
    },
  })}\n`);
}

function relayResponse(request) {
  const frame = JSON.parse(request.subarray(0, request.length - 1).toString("utf8"));
  return Buffer.from(`${JSON.stringify({
    protocol: frame.protocol,
    requestId: frame.requestId,
    daemonGeneration: frame.daemonGeneration,
    route: frame.route,
    kind: "OK",
    sessionCapability: Buffer.alloc(32, 9).toString("base64url"),
    canonicalProjectRef: "canonical-project",
  })}\n`);
}

test("bounded OMP child recognizes a model sentinel split across stdout chunks", async () => {
  const child = new EventEmitter();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  child.kill = () => true;
  const pending = runBoundedChild({ command: "omp", args: [], cwd: process.cwd(), env: {}, timeout_ms: 1_000 }, "HAP_01C_MODEL_OK", baseDeps({
    spawn: () => child,
  }));
  child.stdout.write("HAP_01C_");
  child.stdout.write("MODEL_OK\n");
  child.emit("close", 0, null);
  const result = await pending;
  assert.equal(result.sentinel_present, true);
  assert.equal(result.exit_code, 0);
  assert.equal(result.timed_out, false);
});

test("parses only contained HAP-01C roots and enforces the real capability TTL floor", () => {
  const root = path.join(os.tmpdir(), "hap01c-parse-root");
  const args = [
    "--run-id", "safe-run",
    "--artifact-root", "artifacts",
    "--omp-command", "bin/omp.exe",
    "--postgres-image", "postgres:17-pgvector",
    "--baseline-source", "baseline",
    "--baseline-commit", "a".repeat(40),
    "--fixture-command", "bin/hap-01c-fixture.exe",
  ];
  const parsed = parseArgs(args, root, path);
  assert.equal(parsed.evidence_dir, path.join(root, ".agent", "runs", "hap-01c", "safe-run"));
  assert.equal(parsed.scratch_dir, path.join(root, ".agent", "tmp", "hap-01c", "safe-run"));
  assert.throws(() => parseArgs([...args, "--evidence-root", "../escape"], root, path), ProbeError);
  assert.throws(() => parseArgs([...args, "--expiry-wait-ms", "1"], root, path), ProbeError);
});

test("uses a canonical active-profile envelope and makes a changed envelope unqualified", (t) => {
  const root = temporaryRoot(t);
  const profileRoot = path.join(root, "home", ".omp", "profiles", "active", "plugins");
  writeJson(path.join(profileRoot, "installed_plugins.json"), { plugins: {} });
  writeJson(path.join(profileRoot, "omp-plugins.lock.json"), { plugins: {} });
  const deps = baseDeps({ env: { HOME: path.join(root, "home"), OMP_PROFILE: "active", ENGRAM_URL: "never-retained" } });
  const first = snapshotActiveProfile(deps);
  writeJson(path.join(profileRoot, "omp-plugins.lock.json"), { plugins: { changed: true } });
  const second = snapshotActiveProfile(deps);
  assert.notEqual(first.sha256, second.sha256);
  const record = qualificationRecord(first.sha256, second.sha256);
  assert.equal(record.disposition, "UNQUALIFIED");
});

test("installed-runtime proof requires a clean child exit, sentinel, and complete transcript", () => {
  const completeChild = { started: true, error: false, exit_code: 0, timed_out: false, close_unconfirmed: false, sentinel_present: true };
  const completeTranscript = { complete: true, file_count: 1 };
  assert.equal(turnIsComplete(completeChild, completeTranscript), true);
  for (const [child, transcript] of [
    [{ ...completeChild, exit_code: 1 }, completeTranscript],
    [{ ...completeChild, timed_out: true }, completeTranscript],
    [{ ...completeChild, close_unconfirmed: true }, completeTranscript],
    [{ ...completeChild, sentinel_present: false }, completeTranscript],
    [completeChild, { ...completeTranscript, complete: false }],
    [completeChild, { ...completeTranscript, file_count: 0 }],
  ]) assert.equal(turnIsComplete(child, transcript), false);
});

test("scratch children inherit only the explicit neutral environment allowlist", () => {
  const child = childEnvironment({
    PATH: "safe-path",
    SystemRoot: "C:\\Windows",
    PI_CONFIG_DIR: "active-profile",
    PI_CONFIG_FILES: "active-settings.json",
    COPILOT_GITHUB_TOKEN: "real-copilot-token",
    CURSOR_ACCESS_TOKEN: "real-cursor-token",
    OMP_PROFILE: "active",
    ENGRAM_TOKEN: "real-engram-token",
  }, { HOME: "scratch-home", OMP_PROFILE: "scratch" });
  assert.deepEqual(child, { PATH: "safe-path", SystemRoot: "C:\\Windows", HOME: "scratch-home", OMP_PROFILE: "scratch" });
});

test("scenario projection contradicts any measured candidate direct fallback", () => {
  const observation = scenarioObservation("relay_outage", {
    expected: { direct_fallback_attempts: 0 },
    actual: counts({ direct_fallback_attempts: 1 }),
    routes: [],
    shared_deadline: true,
    custody: cleanCustody(),
  });
  assert.equal(observation.state, "CONTRADICTED");
  assert.equal(observation.passed, false);
  assert.equal(observation.counts.direct_fallback_attempts, 1);
});

test("scratch server uses live worker host and port variables", () => {
  const root = path.join("scratch", "server");
  const environment = scratchServerEnvironment(root, 45678, "postgres://fixture", "engram_fixture", baseDeps({
    env: { ENGRAM_LISTEN_ADDR: "production:37777", GITHUB_TOKEN: "never-inherited", PATH: "safe" },
  }));
  assert.equal(environment.ENGRAM_WORKER_HOST, "127.0.0.1");
  assert.equal(environment.ENGRAM_WORKER_PORT, "45678");
  assert.equal(environment.ENGRAM_V7_PLUG_ENABLED, "true");
  assert.equal(environment.ENGRAM_V7_S2_METAMEM, "true");
  assert.equal(environment.ENGRAM_V7_S3_AMBIENT, "true");
  assert.equal(environment.ENGRAM_LISTEN_ADDR, undefined);

  assert.equal(environment.GITHUB_TOKEN, undefined);
  assert.equal(environment.DATABASE_DSN, "postgres://fixture");
});
test("scratch daemon uses the server tap address rather than its observer handle", () => {
  const runtime = {
    options: { run_id: "daemon-run" },
    server_tap_address: { url: "http://127.0.0.1:45678" },
    server_tap: { mark() { throw new Error("not an address"); } },
    secrets: { ordinary_token: "engram_fixture" },
    matrix: { candidate: { adapter_sha256: "a".repeat(64) } },
  };
  const environment = scratchDaemonEnvironment(runtime, "scenario", { PATH: "safe" }, true);
  assert.equal(environment.ENGRAM_URL, runtime.server_tap_address.url);
  assert.equal(environment.ENGRAM_HAP_01B_RELAY_ENABLED, "true");
  assert.equal(environment.ENGRAM_HAP_01B_ADAPTER_SHA256, "a".repeat(64));
  const turnEnvironment = scratchOmpTurnEnvironment(runtime, "scenario", { env: { OMP_PROFILE: "scratch" } }, "observer.ndjson", "workspace");
  assert.equal(turnEnvironment.ENGRAM_CLIENT_INSTANCE_ID, environment.ENGRAM_CLIENT_INSTANCE_ID);
  assert.equal(turnEnvironment.OMP_PROFILE, "scratch");
});

test("long-lived child logs are drained without consuming MCP stdout", () => {
  let stdoutResumes = 0;
  let stderrResumes = 0;
  const child = {
    stdout: { resume: () => { stdoutResumes += 1; } },
    stderr: { resume: () => { stderrResumes += 1; } },
  };
  drainChildOutput(child);
  assert.equal(stdoutResumes, 1);
  assert.equal(stderrResumes, 1);
  drainChildOutput(child, { stdout: false, stderr: true });
  assert.equal(stdoutResumes, 1);
  assert.equal(stderrResumes, 2);
});

test("PostgreSQL readiness requires consecutive successful SQL probes", () => {
  const success = { started: true, error: false, timed_out: false, close_unconfirmed: false, exit_code: 0, stdout: Buffer.from("1\n") };
  const failure = { started: true, error: false, timed_out: false, close_unconfirmed: false, exit_code: 1, stdout: Buffer.alloc(0) };
  assert.equal(stablePostgresProbeCount(0, success), 1);
  assert.equal(stablePostgresProbeCount(1, success), 2);
  assert.equal(stablePostgresProbeCount(1, failure), 0);
});

test("scratch directory creation rejects an intermediate symbolic link or junction", (t) => {
  const root = temporaryRoot(t);
  const target = path.join(root, "foreign");
  const link = path.join(root, "linked");
  fs.mkdirSync(target);
  try {
    fs.symlinkSync(target, link, process.platform === "win32" ? "junction" : "dir");
  } catch (error) {
    if (error && (error.code === "EPERM" || error.code === "EACCES")) {
      t.skip("host does not permit scratch symlink creation");
      return;
    }
    throw error;
  }
  assert.throws(() => createExclusiveDirectory(path.join(link, "escaped"), baseDeps()), ProbeError);
  assert.equal(fs.existsSync(path.join(target, "escaped")), false);
});

test("resolves the exact installed OMP plugin tree from plugin list JSON", (t) => {
  const root = temporaryRoot(t);
  const profileRoot = path.join(root, "profile");
  const installed = path.join(profileRoot, "home", ".omp", "profiles", "scratch", "plugins", "node_modules", "engram");
  fs.mkdirSync(path.join(installed, "extensions"), { recursive: true });
  writeJson(path.join(installed, "package.json"), { name: "engram", version: "6.48.0", omp: { extensions: ["./extensions/engram-memory.mjs"] } });
  fs.writeFileSync(path.join(installed, "extensions", "engram-memory.mjs"), "entry");
  const expectedTree = directoryTreeDigest(installed, baseDeps()).sha256;
  const listed = {
    npm: [{ name: "engram", version: "6.48.0", path: installed, manifest: { extensions: ["./extensions/engram-memory.mjs"], version: "6.48.0" }, enabledFeatures: null, enabled: true }],
    marketplace: [],
  };
  const resolved = resolveLinkedPluginList(listed, profileRoot, "6.48.0", expectedTree, installed, baseDeps());
  assert.equal(resolved.installPath, installed);
  const outside = structuredClone(listed);
  outside.npm[0].path = path.join(root, "outside");
  assert.throws(() => resolveLinkedPluginList(outside, profileRoot, "6.48.0", expectedTree, installed, baseDeps()), ProbeError);
});

test("accepts only an OMP plugin link to the owned staging tree", (t) => {
  const root = temporaryRoot(t);
  const profileRoot = path.join(root, "profile");
  const staging = path.join(root, "owned-staging");
  const link = path.join(profileRoot, "plugins", "node_modules", "engram");
  fs.mkdirSync(path.join(staging, "extensions"), { recursive: true });
  fs.mkdirSync(path.dirname(link), { recursive: true });
  writeJson(path.join(staging, "package.json"), { name: "engram", version: "6.48.0" });
  fs.writeFileSync(path.join(staging, "extensions", "engram-memory.mjs"), "entry");
  try {
    fs.symlinkSync(staging, link, process.platform === "win32" ? "junction" : "dir");
  } catch (error) {
    if (error && (error.code === "EPERM" || error.code === "EACCES")) {
      t.skip("host does not permit plugin link creation");
      return;
    }
    throw error;
  }

  t.after(() => {
    try { fs.unlinkSync(link); } catch { try { fs.rmdirSync(link); } catch { /* temporaryRoot owns final cleanup */ } }
  });
  const expectedTree = directoryTreeDigest(staging, baseDeps()).sha256;
  const listed = { npm: [{ name: "engram", version: "6.48.0", path: link, manifest: { extensions: ["./extensions/engram-memory.mjs"], version: "6.48.0" }, enabled: true }], marketplace: [] };
  assert.equal(resolveLinkedPluginList(listed, profileRoot, "6.48.0", expectedTree, staging, baseDeps()).installPath, link);
  assert.throws(() => resolveLinkedPluginList(listed, profileRoot, "6.48.0", expectedTree, path.join(root, "foreign"), baseDeps()), ProbeError);
});
test("OMP plugin children share the elected scratch daemon namespace", () => {
  const plugin = { env: { HOME: "profile-home", TEMP: "plugin-temp", PLUGIN_DATA: "plugin-data" }, mode: "new" };
  const daemonEnv = { TEMP: "daemon-temp", TMP: "daemon-tmp", TMPDIR: "daemon-tmpdir", ENGRAM_DATA_DIR: "daemon-data" };
  const bound = bindPluginDaemonNamespace(plugin, daemonEnv);
  assert.equal(bound.env.HOME, "profile-home");
  assert.equal(bound.env.PLUGIN_DATA, "plugin-data");
  assert.equal(bound.env.TEMP, "daemon-temp");
  assert.equal(bound.env.TMP, "daemon-tmp");
  assert.equal(bound.env.TMPDIR, "daemon-tmpdir");
  assert.equal(bound.env.ENGRAM_DATA_DIR, "daemon-data");
  assert.equal(Object.isFrozen(bound.env), true);
});

test("OMP turn keeps plugin MCP startup enabled", () => {
  const args = ompTurnArguments({ options: { scratch_profile: "scratch" } }, "session-dir");
  assert.equal(args.includes("--no-tools"), false);
  assert.equal(args.includes("--no-lsp"), true);
  assert.equal(args.includes("--extension"), true);
  assert.equal(args.includes("-p"), true);
  assert.equal(args[args.indexOf("-p") + 1], "HAP-01C qualification fixture memory.");
});

test("scratch plugin data stays outside the installed plugin link", (t) => {
  const root = temporaryRoot(t);
  const profileRoot = path.join(root, "profile");
  const binding = scratchPluginDataEnvironment(profileRoot, { OMP_PROFILE: "scratch" }, baseDeps());
  assert.equal(binding.pluginData, path.join(profileRoot, "plugin-data", "engram"));
  assert.equal(binding.env.PLUGIN_DATA, binding.pluginData);
  assert.equal(binding.env.OMP_PROFILE, "scratch");
  assert.equal(fs.lstatSync(binding.pluginData).isDirectory(), true);
});

test("seeds the exact client through the repository bootstrap object store", (t) => {
  const assets = {
    "win32-x64": "engram-windows-amd64.exe",
    "linux-x64": "engram-linux-amd64",
    "darwin-arm64": "engram-darwin-arm64",
  };
  const platformKey = `${process.platform}-${process.arch}`;
  if (!Object.hasOwn(assets, platformKey)) {
    t.skip(`unsupported bootstrap test platform ${platformKey}`);
    return;
  }
  const root = temporaryRoot(t);
  const pluginRoot = path.join(root, "plugin");
  const pluginData = path.join(root, "plugin-data");
  const scripts = path.join(pluginRoot, "scripts");
  const manifestRoot = path.join(pluginRoot, ".omp-plugin");
  fs.mkdirSync(scripts, { recursive: true });
  fs.mkdirSync(manifestRoot, { recursive: true });
  const repositoryRoot = path.resolve(__dirname, "..", "..");
  fs.copyFileSync(path.join(repositoryRoot, "plugin", "engram", "scripts", "ensure-binary.js"), path.join(scripts, "ensure-binary.js"));
  fs.copyFileSync(path.join(repositoryRoot, "plugin", "engram", "scripts", "bootstrap-policy.js"), path.join(scripts, "bootstrap-policy.js"));
  writeJson(path.join(manifestRoot, "plugin.json"), { version: "6.48.0" });
  const clientPath = path.join(root, process.platform === "win32" ? "candidate.exe" : "candidate");
  fs.writeFileSync(clientPath, "exact candidate client bytes");
  const clientSha256 = digest(fs.readFileSync(clientPath));
  const policyModule = require(path.join(scripts, "bootstrap-policy.js"));
  const targets = Object.fromEntries(Object.entries(assets).map(([key, asset]) => [key, { version: "6.48.0", asset, size: 1, sha256: digest(key) }]));
  targets[platformKey] = { version: "6.48.0", asset: assets[platformKey], size: fs.statSync(clientPath).size, sha256: clientSha256 };
  fs.writeFileSync(path.join(pluginRoot, "bootstrap-targets.json"), `${JSON.stringify(policyModule.createPolicy("6.48.0", targets), null, 2)}\n`);
  const runtime = {
    matrix: {
      candidate_client_path: clientPath,
      baseline_client_path: clientPath,
      artifact: { client_object_sha256: clientSha256, baseline_client_object_sha256: clientSha256 },
    },
  };
  const installed = seedInstalledClientObject(runtime, pluginRoot, pluginData, "new", baseDeps({ platform: process.platform, arch: process.arch }));
  assert.equal(digest(fs.readFileSync(installed)), clientSha256);
  assert.equal(installed.includes(path.join("objects", "sha256", clientSha256)), true);
});

test("scratch qualification admits one explicit V3 anchor scope", (t) => {
  const root = temporaryRoot(t);
  const repositoryRoot = path.join(root, "repository");
  const workspace = path.join(repositoryRoot, "workspace");
  fs.mkdirSync(workspace, { recursive: true });
  const runtime = { seed: { anchor_project_id: "11111111-1111-4111-8111-111111111111" } };
  const repositoryAnchor = writeScenarioProjectAnchor(runtime, repositoryRoot, baseDeps(), "repository");
  const common = { version: 3, project_id: runtime.seed.anchor_project_id, name: "hap-01c" };
  assert.deepEqual(JSON.parse(fs.readFileSync(repositoryAnchor, "utf8")), { ...common, scope: "repository" });
  assert.equal(fs.existsSync(path.join(workspace, ".engram-project")), false);
  assert.throws(() => writeScenarioProjectAnchor(runtime, workspace, baseDeps()), ProbeError);
  assert.throws(() => writeScenarioProjectAnchor(runtime, root, baseDeps(), "global"), ProbeError);
});

test("scratch worktree produces the muxcore project identifier", async (t) => {
  const root = temporaryRoot(t);
  const scratch = path.join(root, "scratch-project");
  fs.mkdirSync(scratch);
  const options = { cwd: root, scratch_dir: scratch, timeouts: { startup_timeout_ms: 10_000 } };
  const deps = baseDeps({ env: process.env, platform: process.platform, spawn });
  const projectID = await initializeScratchWorktree(options, deps);
  const canonical = (process.platform === "win32" ? fs.realpathSync(scratch).toLowerCase() : fs.realpathSync(scratch));
  assert.equal(projectID, digest(canonical).slice(0, 16));
  assert.equal(projectID, muxcoreProjectID(scratch, deps));
  assert.equal(fs.lstatSync(path.join(scratch, ".git")).isDirectory(), true);
  const runtime = { seed: { anchor_project_id: "11111111-1111-4111-8111-111111111111" } };
  writeScenarioProjectAnchor(runtime, scratch, deps, "repository");
  await trackScratchRepositoryAnchor(options, deps);
  const tracked = spawnSync("git", ["-C", scratch, "ls-files", "--error-unmatch", "--", ".engram-project"], { encoding: "utf8", windowsHide: true });
  assert.equal(tracked.status, 0, tracked.stderr);
});


test("extracts a Windows ZIP through environment-bound literal paths", async (t) => {
  if (process.platform !== "win32") {
    t.skip("Windows Expand-Archive contract");
    return;
  }
  const root = temporaryRoot(t);
  const source = path.join(root, "payload with spaces.txt");
  const archive = path.join(root, "archive with spaces.zip");
  const destination = path.join(root, "expanded with spaces");
  fs.writeFileSync(source, "zip payload");
  const created = spawnSync("powershell.exe", [
    "-NoProfile", "-NonInteractive", "-Command",
    "$ErrorActionPreference='Stop'; Compress-Archive -LiteralPath $env:HAP_TEST_SOURCE -DestinationPath $env:HAP_TEST_ARCHIVE",
  ], { env: { ...process.env, HAP_TEST_SOURCE: source, HAP_TEST_ARCHIVE: archive }, encoding: "utf8", windowsHide: true });
  assert.equal(created.status, 0, created.stderr);
  await extractArchive(archive, destination, { cwd: root, timeouts: { startup_timeout_ms: 10_000 } }, baseDeps({ env: process.env, platform: "win32", spawn }));
  assert.equal(fs.readFileSync(path.join(destination, path.basename(source)), "utf8"), "zip payload");
});
test("inspects exact candidate bytes and rejects an extra hand-labeled archive", async (t) => {
  const root = temporaryRoot(t);
  const candidate = path.join(root, "candidate");
  const source = path.join(candidate, "source");
  const plugin = path.join(candidate, "plugin");
  const dist = path.join(candidate, "dist");
  const baseline = path.join(root, "baseline");
  fs.mkdirSync(source, { recursive: true });
  fs.mkdirSync(path.join(plugin, "extensions"), { recursive: true });
  fs.mkdirSync(dist, { recursive: true });
  fs.mkdirSync(path.join(baseline, "plugin"), { recursive: true });
  fs.writeFileSync(path.join(candidate, "design.json"), "design bytes");
  writeJson(path.join(plugin, "package.json"), { name: "engram", version: "6.49.0" });
  fs.writeFileSync(path.join(plugin, "extensions", "engram-memory.mjs"), "entry bytes");
  fs.writeFileSync(path.join(plugin, "extensions", "legacy-relay.mjs"), "helper bytes");
  writeJson(path.join(plugin, "bootstrap-targets.json"), { schema_version: 1 });
  fs.writeFileSync(path.join(candidate, "engram-server.exe"), "server bytes");
  fs.writeFileSync(path.join(candidate, "engram-client.exe"), "client bytes");
  fs.writeFileSync(path.join(baseline, "client.exe"), "baseline client");
  fs.writeFileSync(path.join(candidate, "hap-01c-fixture.exe"), "fixture bytes");
  const artifact = {
    version: "6.49.0",
    package_sha256: digest(fs.readFileSync(path.join(plugin, "package.json"))),
    extension_entry_sha256: digest(fs.readFileSync(path.join(plugin, "extensions", "engram-memory.mjs"))),
    relay_helper_sha256: digest(fs.readFileSync(path.join(plugin, "extensions", "legacy-relay.mjs"))),
    bootstrap_targets_sha256: digest(fs.readFileSync(path.join(plugin, "bootstrap-targets.json"))),
    server_sha256: digest("server bytes"),
    client_sha256: digest("client bytes"),
    fixture_sha256: digest("fixture bytes"),
    omp_command_sha256: digest("omp bytes"),
    postgres_image: "pgvector/pgvector:pg17",
    postgres_image_sha256: digest("postgres image"),
    install_tree_sha256: directoryTreeDigest(plugin, baseDeps()).sha256,
  };
  const adapter = adapterDigest(path.join(plugin, "extensions", "engram-memory.mjs"), path.join(plugin, "extensions", "legacy-relay.mjs"), baseDeps());
  for (const name of ["engram_6.49.0_darwin_arm64.tar.gz", "engram_6.49.0_linux_amd64.tar.gz", "engram_6.49.0_windows_amd64.zip"]) fs.writeFileSync(path.join(dist, name), name);
  writeJson(path.join(root, ARTIFACT_MANIFEST), {
    schema: "hap-01c-artifact-matrix/1",
    candidate: {
      source_path: "candidate/source",
      source_commit: "1".repeat(40),
      source_tree: "2".repeat(40),
      design_path: "candidate/design.json",
      design_sha256: digest("design bytes"),
      adapter_revision: ADAPTER_REVISION,
      adapter_sha256: adapter,
    },
    artifact: {
      version: artifact.version,
      package_path: "candidate/plugin",
      server_path: "candidate/engram-server.exe",
      client_path: "candidate/engram-client.exe",
      archives_dir: "candidate/dist",
      fixture_path: "candidate/hap-01c-fixture.exe",
      ...artifact,
    },
    baseline: {
      source_commit: "3".repeat(40),
      source_tree: "4".repeat(40),
      plugin_path: "baseline/plugin",
      client_path: "baseline/client.exe",
      plugin_install_tree_sha256: directoryTreeDigest(path.join(baseline, "plugin"), baseDeps()).sha256,
      client_sha256: digest("baseline client"),
    },
  });
  const options = {
    artifact_root: root,
    baseline_source: baseline,
    baseline_commit: "3".repeat(40),
    postgres_image: "pgvector/pgvector:pg17",
    cwd: root,
    fixture_command: path.join(candidate, "hap-01c-fixture.exe"),
    platform: "win32",
    arch: "x64",
    timeouts: { startup_timeout_ms: 1_000 },
  };
  const deps = baseDeps({ gitIdentity: async (sourcePath) => sourcePath === source ? { source_commit: "1".repeat(40), source_tree: "2".repeat(40) } : { source_commit: "3".repeat(40), source_tree: "4".repeat(40) } });
  const foreign = path.join(root, "foreign-baseline-entry");
  fs.writeFileSync(foreign, "foreign");
  const linked = path.join(baseline, "plugin", "linked-entry");
  try {
    fs.symlinkSync(foreign, linked, "file");
    await assert.rejects(() => inspectArtifactMatrix(options, deps), ProbeError);
    fs.rmSync(linked, { force: true });
  } catch (error) {
    if (!error || (error.code !== "EPERM" && error.code !== "EACCES")) throw error;
  }
  const matrix = await inspectArtifactMatrix(options, deps);
  assert.equal(matrix.artifact.adapter_sha256, adapter);
  assert.equal(matrix.artifact.archives.length, 3);
  fs.writeFileSync(path.join(dist, "hand-labeled-extra.zip"), "not admitted");
  await assert.rejects(() => inspectArtifactMatrix(options, deps), ProbeError);
});

test("RelayTap forwards normal bytes unchanged, redacts records, fails closed on outage, and rediscoveries once", async (t) => {
  const root = temporaryRoot(t);
  const upstreamLogical = path.join(root, "upstream.sock");
  const upstreamPhysical = physicalEndpoint(upstreamLogical);
  let upstreamCalls = 0;
  const upstream = net.createServer((socket) => {
    let input = Buffer.alloc(0);
    let answered = false;
    socket.on("data", (chunk) => {
      if (answered) return;
      input = Buffer.concat([input, Buffer.from(chunk)]);
      if (input.indexOf(0x0a) < 0) return;
      answered = true;
      upstreamCalls += 1;
      setTimeout(() => socket.end(relayResponse(input)), 200);
    });
  });
  await listen(upstream, upstreamPhysical);
  t.after(() => close(upstream));

  const logical = path.join(root, "nested", "tap.sock");
  const locator = path.join(root, "locator.json");
  const tap = createRelayTap({ logical_endpoint: logical }, baseDeps({ platform: process.platform }));
  await tap.start();
  t.after(() => tap.close());
  tap.installLocator({ protocol: "engram-legacy-relay/1", daemon_generation: "current-generation", endpoint: upstreamLogical }, locator);

  const normal = relayFrame("current-generation");
  const normalResponse = await socketRequest(physicalEndpoint(logical), normal);
  assert.deepEqual(JSON.parse(normalResponse.toString("utf8")).kind, "OK");
  const normalObservation = tap.snapshot();
  assert.equal(upstreamCalls, 1);
  assert.equal(normalObservation.records[0].outcome, "OK");
  assert.doesNotMatch(JSON.stringify(normalObservation), /raw-request-id|raw-host-session|fixture-client|current-generation/);

  const staleMark = tap.mark();
  tap.armStaleGeneration("stale-generation");
  const staleResponse = await socketRequest(physicalEndpoint(logical), relayFrame("stale-generation"));
  assert.equal(JSON.parse(staleResponse.toString("utf8")).reason, "STALE_GENERATION");
  await socketRequest(physicalEndpoint(logical), relayFrame("current-generation"));
  const staleObservation = tap.snapshot(staleMark);
  assert.deepEqual(staleObservation.records.map((item) => item.outcome), ["NO_DELIVERY", "OK"]);
  assert.equal(staleObservation.rediscoveries, 1);

  const outageMark = tap.mark();
  tap.setMode("outage");
  await socketRequest(physicalEndpoint(logical), relayFrame("current-generation"));
  const outage = tap.snapshot(outageMark);
  assert.equal(outage.records.length, 1);
  assert.equal(outage.server_dispatches, 0);
});

test("ServerTap retains only observable HTTP method/path/status and authorization presence", async (t) => {
  const target = net.createServer((socket) => {
    socket.on("data", () => socket.end("HTTP/1.1 204 No Content\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"));
  });
  await listen(target, { host: "127.0.0.1", port: 0 });
  t.after(() => close(target));
  const targetAddress = target.address();
  const tap = createServerTap({ target_host: "127.0.0.1", target_port: targetAddress.port }, baseDeps());
  const endpoint = await tap.start();
  t.after(() => tap.close());
  const response = await new Promise((resolve, reject) => {
    const socket = net.createConnection({ host: endpoint.host, port: endpoint.port });
    let bytes = Buffer.alloc(0);
    socket.once("error", reject);
    socket.once("connect", () => socket.end("GET /durable HTTP/1.1\r\nHost: loopback\r\nAuthorization: Bearer raw-secret\r\nConnection: close\r\n\r\n"));
    socket.on("data", (chunk) => { bytes = Buffer.concat([bytes, Buffer.from(chunk)]); });
    socket.once("end", () => resolve(bytes));
  });
  assert.match(response.toString("latin1"), /^HTTP\/1\.1 204/);
  const observation = tap.snapshot();
  assert.deepEqual(observation.records, [{ method: "GET", path: "/durable", authorization_present: true, status: 204 }]);
  assert.doesNotMatch(JSON.stringify(observation), /raw-secret|Host:/);
});

test("record assembly binds exact seven scenario denominators, custody, and invalidation subcases", () => {
  const record = qualificationRecord();
  assert.equal(record.disposition, "QUALIFIED");
  assert.deepEqual(record.scenarios.map((scenario) => scenario.id), [
    "new_new_happy",
    "new_plugin_old_daemon",
    "old_plugin_new_daemon",
    "relay_outage",
    "stale_generation",
    "capability_invalidation",
    "rollback_future_turn",
  ]);
  const oldPlugin = record.scenarios.find((scenario) => scenario.id === "old_plugin_new_daemon");
  assert.equal(oldPlugin.custody.extension_url_present, true);
  assert.equal(oldPlugin.custody.authorization_seen, true);
  assert.equal(oldPlugin.custody.child_hap_config_present, false);
  const invalidation = record.scenarios.find((scenario) => scenario.id === "capability_invalidation");
  assert.deepEqual(invalidation.subcases.map((subcase) => [subcase.id, subcase.route_attempts, subcase.server_dispatches]), [
    ["adapter_mismatch", 1, 0],
    ["expiry", 1, 0],
    ["process_exit", 1, 0],
    ["project_keycard_rotation", 1, 0],
  ]);
});

test("runProbe cleans only its owned scratch root and writes a record from injected runtime evidence", async (t) => {
  const root = temporaryRoot(t);
  fs.mkdirSync(path.join(root, "artifact-root"));
  fs.mkdirSync(path.join(root, "baseline"));
  fs.mkdirSync(path.join(root, "bin"));
  fs.writeFileSync(path.join(root, "artifact-root", "hap-01c-fixture.exe"), "fixture bytes");
  fs.writeFileSync(path.join(root, "bin", "omp.exe"), "omp bytes");
  const options = parseArgs([
    "--run-id", "runner-cleanup",
    "--artifact-root", "artifact-root",
    "--omp-command", "bin/omp.exe",
    "--postgres-image", "postgres:17-pgvector",
    "--baseline-source", "baseline",
    "--baseline-commit", "a".repeat(40),
    "--fixture-command", "artifact-root/hap-01c-fixture.exe",
  ], root, path);
  let closed = false;
  const result = await runProbe(options, baseDeps({
    inspectArtifactMatrix: async () => matrixFixture(),
    snapshotActiveProfile: () => ({ sha256: "c".repeat(64), file_count: 0 }),
    createRuntime: async (receivedOptions) => {
      for (const name of ["keycards.json", "dsn.txt", "seed-request.json"]) fs.writeFileSync(path.join(receivedOptions.scratch_dir, name), "secret", { mode: 0o600 });
      return {
        options: receivedOptions,
        secrets_file: path.join(receivedOptions.scratch_dir, "keycards.json"),
        dsn_file: path.join(receivedOptions.scratch_dir, "dsn.txt"),
        seed_request_file: path.join(receivedOptions.scratch_dir, "seed-request.json"),
        postgres: { image_sha256: "d".repeat(64) },
        async close() { closed = true; return 0; },
      };
    },
    runScenario: async (id) => scenarioFixture(id),
  }));
  assert.equal(result.failure, undefined, JSON.stringify(result.failure));
  assert.equal(result.record.disposition, "QUALIFIED");
  assert.equal(result.receipt.omp_command_sha256, digest("omp bytes"));
  assert.equal(result.receipt.fixture_command_sha256, digest("fixture bytes"));
  assert.equal(result.receipt.postgres_image_sha256, "d".repeat(64));
  assert.equal(closed, true);
  assert.equal(fs.existsSync(options.scratch_dir), false);
  assert.equal(fs.existsSync(path.join(options.evidence_dir, "record.json")), true);
  assert.equal(fs.existsSync(path.join(options.evidence_dir, "redacted-observations.json")), true);
});

test("telemetry delta counts both durable session-start writes without inventing ambient persistence", () => {
  const before = { session_start_attempts: 3, target_memory_injection_count: 7 };
  const after = { session_start_attempts: 4, target_memory_injection_count: 8 };
  assert.equal(telemetryDelta(before, after), 2);
  assert.equal(telemetryDelta(after, before), 0);
});

test("fixture snapshots are closed and probe source contains no direct REST HAP fallback", (t) => {
  const root = temporaryRoot(t);
  const snapshot = path.join(root, "snapshot.json");
  writeJson(snapshot, {
    schema: "hap-01c-fixture-snapshot/1",
    run_id_sha256: digest("run\0fixture-run"),
    resolution_attempts: 0,
    registration_attempts: 0,
    session_start_attempts: 0,
    target_memory_injection_count: 0,
    memory_rows: 0,
    rule_rows: 0,
    active_project_token_count: 1,
    revoked_project_token_count: 0,
    ambient_delivery_available: false,
    ambient_delivery_unavailable_reason: "NO_DURABLE_AMBIENT_ATTEMPT_COUNTER",
  });
  assert.equal(parseFixtureSnapshot(snapshot, { run_id: "fixture-run" }, baseDeps()).ambient_delivery_available, false);
  const rawHash = JSON.parse(fs.readFileSync(snapshot, "utf8"));
  rawHash.run_id_sha256 = digest("fixture-run");
  writeJson(snapshot, rawHash);
  assert.throws(() => parseFixtureSnapshot(snapshot, { run_id: "fixture-run" }, baseDeps()), ProbeError);
  const source = fs.readFileSync(path.join(__dirname, "omp-capability-probe.js"), "utf8");
  assert.doesNotMatch(source, /createLoopbackFixture|safeRequestProjection|\/api\/context\//);
});
