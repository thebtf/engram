#!/usr/bin/env node
"use strict";

const childProcess = require("node:child_process");
const crypto = require("node:crypto");
const fs = require("node:fs");
const net = require("node:net");
const os = require("node:os");
const path = require("node:path");
const { pathToFileURL } = require("node:url");

const {
  GUARANTEES,
  SCENARIO_IDS,
  boundaryFailure,
  buildQualificationRecord,
  canonicalize,
  exitFor,
  sha256,
} = require("./omp-capability-record.js");

const PLUGIN_KEY = "engram@engram";
const PLUGIN_KEYS = new Set([PLUGIN_KEY, "engram"]);
const ARTIFACT_MANIFEST = "hap-01c-artifacts.json";
const ARTIFACT_SCHEMA = "hap-01c-artifact-matrix/1";
const ADAPTER_REVISION = "omp-hap-01b/1";
const RELAY_PROTOCOL = "engram-legacy-relay/1";
const RELAY_LOCATOR_NAME = "engram-hap-01b.locator.json";
const ADAPTER_DIGEST_DOMAIN = Buffer.from("engram-hap-01b-extension-tcb/1\0", "utf8");
const RUN_ID = /^[a-z][a-z0-9_-]{0,63}$/;
const SHA256 = /^[a-f0-9]{64}$/;
const GIT_OBJECT_ID = /^(?:[a-f0-9]{40}|[a-f0-9]{64})$/;
const SEMVER = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(?:0|[1-9]\d*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;
const SAFE_IMAGE = /^[A-Za-z0-9][A-Za-z0-9._:@/+-]{0,255}$/;
const MAX_CONTROL_FILE_BYTES = 8 * 1024 * 1024;
const MAX_TRANSCRIPT_FILE_BYTES = 512 * 1024;
const MAX_TRANSCRIPT_FILES = 64;
const MAX_PROXY_FRAME_BYTES = 64 * 1024;
const MAX_PROCESS_OUTPUT_BYTES = 512 * 1024;
const CAPABILITY_TTL_MS = 15 * 60 * 1000;
const DEFAULT_TIMEOUTS = Object.freeze({
  startup_timeout_ms: 90_000,
  turn_timeout_ms: 90_000,
  relay_timeout_ms: 5_000,
  telemetry_timeout_ms: 10_000,
  expiry_wait_ms: CAPABILITY_TTL_MS + 15_000,
});
const CHILD_SETTLE_GRACE_MS = 1_500;
const MODEL_SENTINEL = "HAP01C_MODEL_SENTINEL";
const MODEL_NAME = "hap-01c-local";
const MODEL_PROVIDER = "hap-01c-local";
const MODEL_SELECTOR = `${MODEL_PROVIDER}/${MODEL_NAME}`;
const OBSERVER_FILE = path.join(__dirname, "hap-01c-observer.mjs");
const CALLBACKS = new Set(["session_start", "before_agent_start"]);
const RELAY_ROUTES = new Set(["IDENTITY_REGISTRATION", "SESSION_START_CONTEXT", "AMBIENT_CANDIDATES"]);
const RELAY_OUTCOMES = new Set(["OK", "NO_DELIVERY", "REJECTED"]);
const SENSITIVE_KEY = /(?:^|_)(?:token|secret|password|dsn|capability|descriptor|request_?id|generation|prompt|pid|path|header|url)(?:$|_)/i;
const BASE_CHILD_ENV_KEYS = new Set(["PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "COMSPEC", "LANG", "LC_ALL", "LC_CTYPE", "TZ"]);
class ProbeError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "ProbeError";
    this.code = code;
  }
}

function fail(code, message) {
  throw new ProbeError(code, message);
}

function safeErrorCode(error, fallback = "PROBE_RUNTIME_FAILURE") {
  return error instanceof ProbeError && /^[A-Z][A-Z0-9_]{0,127}$/.test(error.code) ? error.code : fallback;
}

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype;
}

function defaultDependencies() {
  return {
    arch: process.arch,
    cwd: () => process.cwd(),
    env: process.env,
    fs,
    net,
    os,
    path,
    platform: process.platform,
    randomBytes: crypto.randomBytes,
    randomUUID: crypto.randomUUID,
    now: () => Date.now(),
    sleep: (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)),
    setTimeout,
    clearTimeout,
    spawn: childProcess.spawn,
    processKill: process.kill.bind(process),
    stdout: (value) => process.stdout.write(value),
    stderr: (value) => process.stderr.write(value),
  };
}

function normalizeDependencies(overrides = {}) {
  return { ...defaultDependencies(), ...overrides };
}

function isInside(root, candidate, pathApi = path) {
  const relative = pathApi.relative(pathApi.resolve(root), pathApi.resolve(candidate));
  return relative === "" || (!relative.startsWith(`..${pathApi.sep}`) && relative !== ".." && !pathApi.isAbsolute(relative));
}

function samePath(left, right, pathApi = path) {
  return pathApi.relative(pathApi.resolve(left), pathApi.resolve(right)) === "";
}

function safeRelative(value, label) {
  if (typeof value !== "string" || value.length === 0) fail("ARTIFACT_MANIFEST_INVALID", `${label} is missing`);
  const normalized = value.replace(/^\.\//, "");
  if (!normalized || normalized.includes("\\") || normalized.startsWith("/") || normalized.split("/").some((part) => !part || part === "." || part === "..")) {
    fail("ARTIFACT_MANIFEST_INVALID", `${label} is not a normalized relative path`);
  }
  return normalized;
}

function resolveContained(root, relative, label, pathApi = path) {
  const resolved = pathApi.resolve(root, safeRelative(relative, label));
  if (!isInside(root, resolved, pathApi)) fail("ARTIFACT_MANIFEST_INVALID", `${label} escapes its root`);
  return resolved;
}

function parsePositiveMilliseconds(value, label, minimum, maximum = 24 * 60 * 60 * 1000) {
  if (!/^\d+$/.test(value || "")) fail("CLI_INVALID", `${label} must be a positive integer`);
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed) || parsed < minimum || parsed > maximum) fail("CLI_INVALID", `${label} is out of range`);
  return parsed;
}

function parseArgs(args, cwd = process.cwd(), pathApi = path) {
  if (!Array.isArray(args)) fail("CLI_INVALID", "arguments must be an array");
  const required = new Set([
    "--run-id",
    "--artifact-root",
    "--omp-command",
    "--postgres-image",
    "--baseline-source",
    "--baseline-commit",
    "--fixture-command",
  ]);
  const optional = new Set([
    "--evidence-root",
    "--scratch-root",
    "--startup-timeout-ms",
    "--turn-timeout-ms",
    "--relay-timeout-ms",
    "--telemetry-timeout-ms",
    "--expiry-wait-ms",
  ]);
  const values = {};
  for (let index = 0; index < args.length; index += 1) {
    const flag = args[index];
    if ((!required.has(flag) && !optional.has(flag)) || Object.hasOwn(values, flag)) fail("CLI_INVALID", "unsupported or duplicate option");
    const value = args[++index];
    if (typeof value !== "string" || !value || value.startsWith("--")) fail("CLI_INVALID", "option value is missing");
    values[flag] = value;
  }
  for (const flag of required) {
    if (!Object.hasOwn(values, flag)) fail("CLI_INVALID", "required HAP-01C option is missing");
  }
  if (!RUN_ID.test(values["--run-id"])) fail("CLI_INVALID", "run id is unsafe");
  if (!SAFE_IMAGE.test(values["--postgres-image"])) fail("CLI_INVALID", "postgres image is unsafe");
  if (!GIT_OBJECT_ID.test(values["--baseline-commit"])) fail("CLI_INVALID", "baseline commit is invalid");

  const root = pathApi.resolve(cwd);
  const permittedEvidenceRoot = pathApi.resolve(root, ".agent", "runs", "hap-01c");
  const permittedScratchRoot = pathApi.resolve(root, ".agent", "tmp", "hap-01c");
  const evidenceRoot = pathApi.resolve(root, values["--evidence-root"] || ".agent/runs/hap-01c");
  const scratchRoot = pathApi.resolve(root, values["--scratch-root"] || ".agent/tmp/hap-01c");
  if (!isInside(permittedEvidenceRoot, evidenceRoot, pathApi) || !isInside(permittedScratchRoot, scratchRoot, pathApi)) {
    fail("EVIDENCE_DIR_INVALID", "evidence and scratch roots must stay in their HAP-01C containment roots");
  }
  const evidenceDir = pathApi.resolve(evidenceRoot, values["--run-id"]);
  const scratchDir = pathApi.resolve(scratchRoot, values["--run-id"]);
  if (!isInside(evidenceRoot, evidenceDir, pathApi) || !isInside(scratchRoot, scratchDir, pathApi)) fail("EVIDENCE_DIR_INVALID", "run paths escape their configured roots");

  const commandPath = (name) => {
    const value = values[name];
    if (value.includes("\u0000")) fail("CLI_INVALID", `${name} is unsafe`);
    return pathApi.resolve(root, value);
  };
  const timeouts = {
    startup_timeout_ms: Object.hasOwn(values, "--startup-timeout-ms")
      ? parsePositiveMilliseconds(values["--startup-timeout-ms"], "startup timeout", 1_000)
      : DEFAULT_TIMEOUTS.startup_timeout_ms,
    turn_timeout_ms: Object.hasOwn(values, "--turn-timeout-ms")
      ? parsePositiveMilliseconds(values["--turn-timeout-ms"], "turn timeout", 1_000)
      : DEFAULT_TIMEOUTS.turn_timeout_ms,
    relay_timeout_ms: Object.hasOwn(values, "--relay-timeout-ms")
      ? parsePositiveMilliseconds(values["--relay-timeout-ms"], "relay timeout", 100)
      : DEFAULT_TIMEOUTS.relay_timeout_ms,
    telemetry_timeout_ms: Object.hasOwn(values, "--telemetry-timeout-ms")
      ? parsePositiveMilliseconds(values["--telemetry-timeout-ms"], "telemetry timeout", 100)
      : DEFAULT_TIMEOUTS.telemetry_timeout_ms,
    expiry_wait_ms: Object.hasOwn(values, "--expiry-wait-ms")
      ? parsePositiveMilliseconds(values["--expiry-wait-ms"], "expiry wait", CAPABILITY_TTL_MS + 1)
      : DEFAULT_TIMEOUTS.expiry_wait_ms,
  };

  return Object.freeze({
    cwd: root,
    run_id: values["--run-id"],
    evidence_root: evidenceRoot,
    scratch_root: scratchRoot,
    evidence_dir: evidenceDir,
    scratch_dir: scratchDir,
    artifact_root: commandPath("--artifact-root"),
    omp_command: commandPath("--omp-command"),
    postgres_image: values["--postgres-image"],
    baseline_source: commandPath("--baseline-source"),
    baseline_commit: values["--baseline-commit"],
    fixture_command: commandPath("--fixture-command"),
    timeouts: Object.freeze(timeouts),
    scratch_profile: `hap01c-${sha256(values["--run-id"]).slice(0, 16)}`,
  });
}

function validateExecutionInputs(options, deps) {
  if (!isInside(options.cwd, options.artifact_root, deps.path) || !isInside(options.cwd, options.baseline_source, deps.path) ||
    !isInside(options.artifact_root, options.fixture_command, deps.path)) {
    fail("EXECUTION_INPUT_UNSAFE", "artifact, baseline, or fixture command escapes the project root");
  }
  requireDirectory(options.artifact_root, deps, "EXECUTION_INPUT_UNSAFE");
  requireDirectory(options.baseline_source, deps, "EXECUTION_INPUT_UNSAFE");
  const fixture = digestRegularFile(options.fixture_command, deps, "EXECUTION_INPUT_UNSAFE");
  const omp = digestRegularFile(options.omp_command, deps, "EXECUTION_INPUT_UNSAFE");
  return Object.freeze({ fixture_command_sha256: fixture.sha256, omp_command_sha256: omp.sha256 });
}

function exists(filePath, deps) {
  try {
    deps.fs.lstatSync(filePath);
    return true;
  } catch (error) {
    if (error && error.code === "ENOENT") return false;
    throw error;
  }
}

function assertNoSymlinkComponents(candidate, deps, code = "PATH_UNSAFE") {
  const absolute = deps.path.resolve(candidate);
  const parsed = deps.path.parse(absolute);
  let current = parsed.root;
  const relative = absolute.slice(parsed.root.length);
  for (const part of relative.split(deps.path.sep).filter(Boolean)) {
    current = deps.path.join(current, part);
    let stat;
    try {
      stat = deps.fs.lstatSync(current);
    } catch (error) {
      if (error && error.code === "ENOENT") return absolute;
      fail(code, "path component is unavailable");
    }
    if (stat.isSymbolicLink()) fail(code, "path contains a symbolic link or reparse point");
  }
  return absolute;
}

function ensureDirectory(directory, deps, code = "OWNED_DIRECTORY_INVALID") {
  assertNoSymlinkComponents(directory, deps, code);
  deps.fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  assertNoSymlinkComponents(directory, deps, code);
  return requireDirectory(directory, deps, code);
}

function requireDirectory(directory, deps, code = "FILE_INVALID") {
  assertNoSymlinkComponents(directory, deps, code);
  let stat;
  try {
    stat = deps.fs.lstatSync(directory);
  } catch {
    fail(code, "required directory is unavailable");
  }
  if (!stat.isDirectory() || stat.isSymbolicLink()) fail(code, "required directory is unsafe");
  return stat;
}

function regularFileBytes(filePath, deps, code = "FILE_INVALID", maxBytes = MAX_CONTROL_FILE_BYTES) {
  assertNoSymlinkComponents(filePath, deps, code);
  let stat;
  try {
    stat = deps.fs.lstatSync(filePath);
  } catch {
    fail(code, "required file is unavailable");
  }
  if (!stat.isFile() || stat.isSymbolicLink() || !Number.isSafeInteger(stat.size) || stat.size < 0 || stat.size > maxBytes) {
    fail(code, "required file is not a bounded regular file");
  }
  return Buffer.from(deps.fs.readFileSync(filePath));
}

function parseJson(bytes, code) {
  try {
    const value = JSON.parse(Buffer.from(bytes).toString("utf8"));
    if (!isPlainObject(value)) fail(code, "JSON root is not an object");
    return value;
  } catch (error) {
    if (error instanceof ProbeError) throw error;
    fail(code, "JSON is malformed");
  }
}

function digestRegularFile(filePath, deps, code = "FILE_INVALID") {
  assertNoSymlinkComponents(filePath, deps, code);
  let stat;
  try {
    stat = deps.fs.lstatSync(filePath);
  } catch {
    fail(code, "file is unavailable");
  }
  if (!stat.isFile() || stat.isSymbolicLink()) fail(code, "file is not regular");
  const hash = crypto.createHash("sha256");
  const descriptor = deps.fs.openSync(filePath, "r");
  try {
    const buffer = Buffer.allocUnsafe(64 * 1024);
    for (; ;) {
      const count = deps.fs.readSync(descriptor, buffer, 0, buffer.length, null);
      if (!count) break;
      hash.update(buffer.subarray(0, count));
    }
  } finally {
    deps.fs.closeSync(descriptor);
  }
  return { size: stat.size, sha256: hash.digest("hex") };
}

function createExclusiveDirectory(directory, deps) {
  const parent = deps.path.dirname(directory);
  ensureDirectory(parent, deps, "OWNED_DIRECTORY_INVALID");
  try {
    deps.fs.mkdirSync(directory, { recursive: false, mode: 0o700 });
  } catch (error) {
    if (error && error.code === "EEXIST") fail("OWNED_DIRECTORY_EXISTS", "owned HAP-01C directory already exists");
    throw error;
  }
  assertNoSymlinkComponents(directory, deps, "OWNED_DIRECTORY_INVALID");
  requireDirectory(directory, deps, "OWNED_DIRECTORY_INVALID");
}

function removeOwnedDirectory(directory, boundary, deps) {
  if (!isInside(boundary, directory, deps.path)) return false;
  try {
    assertNoSymlinkComponents(boundary, deps, "OWNED_DIRECTORY_INVALID");
    assertNoSymlinkComponents(directory, deps, "OWNED_DIRECTORY_INVALID");
    const stat = deps.fs.lstatSync(directory);
    if (!stat.isDirectory() || stat.isSymbolicLink()) return false;
    deps.fs.rmSync(directory, { recursive: true, force: true, maxRetries: 2 });
    return !exists(directory, deps);
  } catch {
    return false;
  }
}

function writeExclusive(filePath, data, deps, mode = 0o600) {
  assertNoSymlinkComponents(deps.path.dirname(filePath), deps, "EVIDENCE_PATH_INVALID");
  let descriptor;
  try {
    descriptor = deps.fs.openSync(filePath, "wx", mode);
    deps.fs.writeFileSync(descriptor, data);
    if (typeof deps.fs.fsyncSync === "function") deps.fs.fsyncSync(descriptor);
  } catch (error) {
    if (error && error.code === "EEXIST") fail("EVIDENCE_WRITE_EXISTS", "exclusive output already exists");
    throw error;
  } finally {
    if (descriptor !== undefined) deps.fs.closeSync(descriptor);
  }
}

function writeJsonExclusive(filePath, value, deps) {
  writeExclusive(filePath, `${canonicalize(value)}\n`, deps, 0o600);
}

function writeSecretJson(filePath, value, deps) {
  writeExclusive(filePath, `${JSON.stringify(value)}\n`, deps, 0o600);
}

function writeSecretText(filePath, value, deps) {
  writeExclusive(filePath, String(value), deps, 0o600);
}

function writeAtomicJson(filePath, value, deps) {
  const parent = deps.path.dirname(filePath);
  ensureDirectory(parent, deps, "EVIDENCE_PATH_INVALID");
  const temporary = deps.path.join(parent, `.${deps.path.basename(filePath)}.${sha256(`${deps.now()}:${Math.random()}`).slice(0, 12)}.tmp`);
  try {
    writeExclusive(temporary, `${JSON.stringify(value)}\n`, deps, 0o600);
    deps.fs.renameSync(temporary, filePath);
  } finally {
    try { deps.fs.rmSync(temporary, { force: true }); } catch { /* owned temporary is best effort */ }
  }
}

function copyDirectory(source, destination, deps) {
  requireDirectory(source, deps, "ARTIFACT_MANIFEST_INVALID");
  if (exists(destination, deps)) fail("OWNED_DIRECTORY_EXISTS", "scratch installation already exists");
  if (typeof deps.fs.cpSync !== "function") fail("COPY_UNAVAILABLE", "runtime does not provide copy support");
  deps.fs.cpSync(source, destination, { recursive: true, dereference: false, errorOnExist: true, force: false });
  requireDirectory(destination, deps, "ARTIFACT_MANIFEST_INVALID");
}

function directoryTreeDigest(root, deps) {
  requireDirectory(root, deps, "ARTIFACT_MANIFEST_INVALID");
  const files = [];
  function walk(current) {
    const entries = deps.fs.readdirSync(current, { withFileTypes: true }).sort((left, right) => left.name.localeCompare(right.name));
    for (const entry of entries) {
      const candidate = deps.path.join(current, entry.name);
      if (entry.isSymbolicLink()) fail("ARTIFACT_MANIFEST_INVALID", "artifact tree contains a symbolic link");
      if (entry.isDirectory()) {
        walk(candidate);
      } else if (entry.isFile()) {
        const relative = deps.path.relative(root, candidate).split(deps.path.sep).join("/");
        files.push({ relative, candidate });
      } else {
        fail("ARTIFACT_MANIFEST_INVALID", "artifact tree contains an unsupported entry");
      }
    }
  }
  walk(root);
  const hash = crypto.createHash("sha256");
  hash.update("hap-01c-install-tree/1\0", "utf8");
  for (const item of files) {
    const name = Buffer.from(item.relative, "utf8");
    const digest = digestRegularFile(item.candidate, deps, "ARTIFACT_MANIFEST_INVALID");
    const nameLength = Buffer.allocUnsafe(4);
    const size = Buffer.allocUnsafe(8);
    nameLength.writeUInt32BE(name.length);
    size.writeBigUInt64BE(BigInt(digest.size));
    hash.update(nameLength);
    hash.update(name);
    hash.update(size);
    hash.update(Buffer.from(digest.sha256, "hex"));
  }
  return { file_count: files.length, sha256: hash.digest("hex") };
}

function adapterDigest(entryPath, helperPath, deps) {
  const files = [
    { relative: "extensions/engram-memory.mjs", file: entryPath },
    { relative: "extensions/legacy-relay.mjs", file: helperPath },
  ].sort((left, right) => left.relative.localeCompare(right.relative));
  const hash = crypto.createHash("sha256");
  hash.update(ADAPTER_DIGEST_DOMAIN);
  for (const item of files) {
    const name = Buffer.from(item.relative, "utf8");
    const bytes = regularFileBytes(item.file, deps, "ARTIFACT_MANIFEST_INVALID");
    const nameLength = Buffer.allocUnsafe(4);
    const size = Buffer.allocUnsafe(8);
    nameLength.writeUInt32BE(name.length);
    size.writeBigUInt64BE(BigInt(bytes.length));
    hash.update(nameLength);
    hash.update(name);
    hash.update(size);
    hash.update(bytes);
  }
  return hash.digest("hex");
}

function requireExactKeys(value, keys, label, code = "ARTIFACT_MANIFEST_INVALID") {
  if (!isPlainObject(value)) fail(code, `${label} is not an object`);
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) fail(code, `${label} has an unsupported shape`);
}

function validDigest(value, label) {
  if (typeof value !== "string" || !SHA256.test(value)) fail("ARTIFACT_MANIFEST_INVALID", `${label} is invalid`);
  return value;
}

function validGitObject(value, label) {
  if (typeof value !== "string" || !GIT_OBJECT_ID.test(value)) fail("ARTIFACT_MANIFEST_INVALID", `${label} is invalid`);
  return value;
}

function expectedArchives(version) {
  return [
    { name: `engram_${version}_darwin_arm64.tar.gz`, platform: "darwin", arch: "arm64" },
    { name: `engram_${version}_linux_amd64.tar.gz`, platform: "linux", arch: "amd64" },
    { name: `engram_${version}_windows_amd64.zip`, platform: "win32", arch: "amd64" },
  ];
}

function parseArtifactManifest(root, deps) {
  const manifestPath = deps.path.join(root, ARTIFACT_MANIFEST);
  const manifest = parseJson(regularFileBytes(manifestPath, deps, "ARTIFACT_MANIFEST_INVALID"), "ARTIFACT_MANIFEST_INVALID");
  requireExactKeys(manifest, ["schema", "candidate", "artifact", "baseline"], "artifact manifest");
  if (manifest.schema !== ARTIFACT_SCHEMA) fail("ARTIFACT_MANIFEST_INVALID", "artifact manifest schema is unsupported");
  requireExactKeys(manifest.candidate, [
    "source_path", "source_commit", "source_tree", "design_path", "design_sha256", "adapter_revision", "adapter_sha256",
  ], "artifact manifest.candidate");
  requireExactKeys(manifest.artifact, [
    "version", "package_path", "server_path", "client_path", "fixture_path", "archives_dir", "package_sha256", "extension_entry_sha256",
    "relay_helper_sha256", "bootstrap_targets_sha256", "server_sha256", "client_sha256", "fixture_sha256", "omp_command_sha256", "postgres_image",
    "postgres_image_sha256", "install_tree_sha256",
  ], "artifact manifest.artifact");
  requireExactKeys(manifest.baseline, ["source_commit", "source_tree", "plugin_path", "client_path", "plugin_install_tree_sha256", "client_sha256"], "artifact manifest.baseline");
  if (!SEMVER.test(manifest.artifact.version)) fail("ARTIFACT_MANIFEST_INVALID", "artifact version is invalid");
  if (manifest.candidate.adapter_revision !== ADAPTER_REVISION) fail("ARTIFACT_MANIFEST_INVALID", "artifact adapter revision is unsupported");
  if (!SAFE_IMAGE.test(manifest.artifact.postgres_image)) fail("ARTIFACT_MANIFEST_INVALID", "artifact postgres image is invalid");
  for (const key of ["source_path", "design_path"]) safeRelative(manifest.candidate[key], `candidate.${key}`);
  for (const key of ["package_path", "server_path", "client_path", "fixture_path", "archives_dir"]) safeRelative(manifest.artifact[key], `artifact.${key}`);
  for (const key of ["plugin_path", "client_path"]) safeRelative(manifest.baseline[key], `baseline.${key}`);
  validGitObject(manifest.candidate.source_commit, "candidate.source_commit");
  validGitObject(manifest.candidate.source_tree, "candidate.source_tree");
  validGitObject(manifest.baseline.source_commit, "baseline.source_commit");
  validGitObject(manifest.baseline.source_tree, "baseline.source_tree");
  for (const key of ["design_sha256", "adapter_sha256"]) validDigest(manifest.candidate[key], `candidate.${key}`);
  for (const key of ["package_sha256", "extension_entry_sha256", "relay_helper_sha256", "bootstrap_targets_sha256", "server_sha256", "client_sha256", "fixture_sha256", "omp_command_sha256", "postgres_image_sha256", "install_tree_sha256"]) {
    validDigest(manifest.artifact[key], `artifact.${key}`);
  }
  for (const key of ["plugin_install_tree_sha256", "client_sha256"]) validDigest(manifest.baseline[key], `baseline.${key}`);
  return { manifest, manifest_sha256: digestRegularFile(manifestPath, deps, "ARTIFACT_MANIFEST_INVALID").sha256 };
}

async function runProcess(spec, deps) {
  if (!spec || typeof spec.command !== "string" || !Array.isArray(spec.args) || typeof spec.cwd !== "string" || !isPlainObject(spec.env) || !Number.isSafeInteger(spec.timeout_ms) || spec.timeout_ms <= 0) {
    fail("PROCESS_SPEC_INVALID", "child process specification is invalid");
  }
  return new Promise((resolve) => {
    let child;
    let settled = false;
    let timedOut = false;
    let timeout;
    let grace;
    let stdoutLength = 0;
    let stderrLength = 0;
    let stdout = Buffer.alloc(0);
    let stderr = Buffer.alloc(0);
    const stdoutHash = crypto.createHash("sha256");
    const stderrHash = crypto.createHash("sha256");
    const finish = (value) => {
      if (settled) return;
      settled = true;
      deps.clearTimeout(timeout);
      deps.clearTimeout(grace);
      resolve({
        stdout_bytes: stdoutLength,
        stderr_bytes: stderrLength,
        stdout_sha256: stdoutHash.digest("hex"),
        stderr_sha256: stderrHash.digest("hex"),
        stdout,
        stderr,
        ...value,
      });
    };
    const capture = (stream) => (chunk) => {
      const bytes = Buffer.from(chunk);
      if (stream === "stdout") {
        stdoutLength += bytes.length;
        stdoutHash.update(bytes);
        if (stdout.length < MAX_PROCESS_OUTPUT_BYTES) stdout = Buffer.concat([stdout, bytes.subarray(0, MAX_PROCESS_OUTPUT_BYTES - stdout.length)]);
      } else {
        stderrLength += bytes.length;
        stderrHash.update(bytes);
        if (stderr.length < MAX_PROCESS_OUTPUT_BYTES) stderr = Buffer.concat([stderr, bytes.subarray(0, MAX_PROCESS_OUTPUT_BYTES - stderr.length)]);
      }
    };
    try {
      child = deps.spawn(spec.command, spec.args, {
        cwd: spec.cwd,
        env: spec.env,
        stdio: ["ignore", "pipe", "pipe"],
        windowsHide: true,
      });
    } catch {
      finish({ started: false, exit_code: null, timed_out: false, close_unconfirmed: false, error: true });
      return;
    }
    if (child.stdout && typeof child.stdout.on === "function") child.stdout.on("data", capture("stdout"));
    if (child.stderr && typeof child.stderr.on === "function") child.stderr.on("data", capture("stderr"));
    if (child && typeof child.once === "function") {
      child.once("error", () => finish({ started: true, exit_code: null, timed_out: timedOut, close_unconfirmed: false, error: true }));
      child.once("close", (code, signal) => finish({
        started: true,
        exit_code: Number.isInteger(code) ? code : null,
        exit_signal: typeof signal === "string" ? signal : null,
        timed_out: timedOut,
        close_unconfirmed: false,
        error: false,
      }));
    }
    timeout = deps.setTimeout(() => {
      timedOut = true;
      try { child.kill("SIGTERM"); } catch { /* closing is verified by the grace timeout */ }
      grace = deps.setTimeout(() => finish({ started: true, exit_code: null, timed_out: true, close_unconfirmed: true, error: false }), CHILD_SETTLE_GRACE_MS);
    }, spec.timeout_ms);
  });
}

function requireProcessSuccess(result, code, label) {
  if (!result.started || result.error || result.timed_out || result.close_unconfirmed || result.exit_code !== 0) {
    fail(code, `${label} did not complete successfully`);
  }
  return result;
}

async function commandOutput(command, args, cwd, env, timeout, deps, code, label) {
  const result = requireProcessSuccess(await runProcess({ command, args, cwd, env, timeout_ms: timeout }, deps), code, label);
  if (result.stdout_bytes > MAX_PROCESS_OUTPUT_BYTES) fail(code, `${label} output is too large`);
  return result.stdout.toString("utf8");
}

async function gitIdentity(source, options, deps) {
  if (typeof deps.gitIdentity === "function") return deps.gitIdentity(source, options);
  requireDirectory(source, deps, "ARTIFACT_SOURCE_MISMATCH");
  const environment = childEnvironment(deps.env, {}, deps);
  const status = await commandOutput("git", ["-C", source, "status", "--porcelain=v1", "--untracked-files=all"], options.cwd, environment, options.timeouts.startup_timeout_ms, deps, "ARTIFACT_SOURCE_MISMATCH", "git source status");
  if (status.trim()) fail("ARTIFACT_SOURCE_MISMATCH", "git source contains uncommitted or untracked bytes");
  const commit = (await commandOutput("git", ["-C", source, "rev-parse", "HEAD"], options.cwd, environment, options.timeouts.startup_timeout_ms, deps, "ARTIFACT_SOURCE_MISMATCH", "git source commit")).trim();
  const tree = (await commandOutput("git", ["-C", source, "rev-parse", "HEAD^{tree}"], options.cwd, environment, options.timeouts.startup_timeout_ms, deps, "ARTIFACT_SOURCE_MISMATCH", "git source tree")).trim();
  if (!GIT_OBJECT_ID.test(commit) || !GIT_OBJECT_ID.test(tree)) fail("ARTIFACT_SOURCE_MISMATCH", "git identity output is invalid");
  return { source_commit: commit, source_tree: tree };
}

async function extractArchive(archive, destination, options, deps) {
  if (typeof deps.extractArchive === "function") return deps.extractArchive(archive, destination, options);
  createExclusiveDirectory(destination, deps);
  const extension = deps.path.basename(archive).toLowerCase();
  let spec;
  if (extension.endsWith(".zip") && deps.platform === "win32") {
    spec = {
      command: "powershell.exe",
      args: ["-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop'; Expand-Archive -LiteralPath $env:HAP_01C_ARCHIVE -DestinationPath $env:HAP_01C_DESTINATION -Force"],
      env: childEnvironment(deps.env, { HAP_01C_ARCHIVE: archive, HAP_01C_DESTINATION: destination }, deps),
    };
  } else if (extension.endsWith(".zip")) {
    spec = { command: "unzip", args: ["-qq", archive, "-d", destination], env: childEnvironment(deps.env, {}, deps) };
  } else {
    spec = { command: "tar", args: ["-xzf", archive, "-C", destination], env: childEnvironment(deps.env, {}, deps) };
  }
  requireProcessSuccess(await runProcess({ ...spec, cwd: options.cwd, timeout_ms: options.timeouts.startup_timeout_ms }, deps), "ARCHIVE_EXTRACTION_FAILED", "archive extraction");
}

function packagePayload(root, deps) {
  const packagePath = deps.path.join(root, "package.json");
  const entryPath = deps.path.join(root, "extensions", "engram-memory.mjs");
  const helperPath = deps.path.join(root, "extensions", "legacy-relay.mjs");
  const bootstrapPath = deps.path.join(root, "bootstrap-targets.json");
  const packageBytes = regularFileBytes(packagePath, deps, "ARTIFACT_MANIFEST_INVALID");
  const packageJson = parseJson(packageBytes, "ARTIFACT_MANIFEST_INVALID");
  if (packageJson.name !== "engram" || typeof packageJson.version !== "string" || !SEMVER.test(packageJson.version)) fail("ARTIFACT_MANIFEST_INVALID", "plugin package identity is invalid");
  return {
    version: packageJson.version,
    package_sha256: sha256(packageBytes),
    extension_entry_sha256: digestRegularFile(entryPath, deps, "ARTIFACT_MANIFEST_INVALID").sha256,
    relay_helper_sha256: digestRegularFile(helperPath, deps, "ARTIFACT_MANIFEST_INVALID").sha256,
    bootstrap_targets_sha256: digestRegularFile(bootstrapPath, deps, "ARTIFACT_MANIFEST_INVALID").sha256,
    adapter_sha256: adapterDigest(entryPath, helperPath, deps),
    install_tree_sha256: directoryTreeDigest(root, deps).sha256,
  };
}

function assertPayloadEquals(payload, expected, label) {
  for (const key of ["package_sha256", "extension_entry_sha256", "relay_helper_sha256", "bootstrap_targets_sha256", "adapter_sha256", "install_tree_sha256"]) {
    if (payload[key] !== expected[key]) fail("ARTIFACT_MATRIX_MISMATCH", `${label} does not match the frozen artifact matrix`);
  }
}

async function inspectArtifactMatrix(options, deps, inspectionRoot = null) {
  if (typeof deps.inspectArtifactMatrix === "function") return deps.inspectArtifactMatrix(options, inspectionRoot);
  requireDirectory(options.artifact_root, deps, "ARTIFACT_MANIFEST_INVALID");
  const { manifest, manifest_sha256: manifestDigest } = parseArtifactManifest(options.artifact_root, deps);
  const candidateSource = resolveContained(options.artifact_root, manifest.candidate.source_path, "candidate.source_path", deps.path);
  const candidateIdentity = await gitIdentity(candidateSource, options, deps);
  if (candidateIdentity.source_commit !== manifest.candidate.source_commit || candidateIdentity.source_tree !== manifest.candidate.source_tree) {
    fail("ARTIFACT_SOURCE_MISMATCH", "candidate source identity does not match the frozen manifest");
  }
  const baselineIdentity = await gitIdentity(options.baseline_source, options, deps);
  if (baselineIdentity.source_commit !== options.baseline_commit || manifest.baseline.source_commit !== options.baseline_commit ||
    baselineIdentity.source_tree !== manifest.baseline.source_tree) {
    fail("BASELINE_SOURCE_MISMATCH", "baseline source does not match its pinned commit and tree");
  }
  if (manifest.artifact.postgres_image !== options.postgres_image) fail("ARTIFACT_MATRIX_MISMATCH", "postgres image reference differs from the frozen manifest");

  const designPath = resolveContained(options.artifact_root, manifest.candidate.design_path, "candidate.design_path", deps.path);
  if (digestRegularFile(designPath, deps, "ARTIFACT_MANIFEST_INVALID").sha256 !== manifest.candidate.design_sha256) fail("ARTIFACT_MATRIX_MISMATCH", "candidate design digest differs");
  const pluginRoot = resolveContained(options.artifact_root, manifest.artifact.package_path, "artifact.package_path", deps.path);
  const payload = packagePayload(pluginRoot, deps);
  const expectedPayload = {
    package_sha256: manifest.artifact.package_sha256,
    extension_entry_sha256: manifest.artifact.extension_entry_sha256,
    relay_helper_sha256: manifest.artifact.relay_helper_sha256,
    bootstrap_targets_sha256: manifest.artifact.bootstrap_targets_sha256,
    adapter_sha256: manifest.candidate.adapter_sha256,
    install_tree_sha256: manifest.artifact.install_tree_sha256,
  };
  if (payload.version !== manifest.artifact.version) fail("ARTIFACT_MATRIX_MISMATCH", "candidate package version differs from manifest");
  assertPayloadEquals(payload, expectedPayload, "candidate plugin");
  if (manifest.candidate.adapter_sha256 !== payload.adapter_sha256) fail("ARTIFACT_MATRIX_MISMATCH", "candidate adapter digest differs from installed relay pair");

  const serverPath = resolveContained(options.artifact_root, manifest.artifact.server_path, "artifact.server_path", deps.path);
  const clientPath = resolveContained(options.artifact_root, manifest.artifact.client_path, "artifact.client_path", deps.path);
  const server = digestRegularFile(serverPath, deps, "ARTIFACT_MANIFEST_INVALID");
  const client = digestRegularFile(clientPath, deps, "ARTIFACT_MANIFEST_INVALID");
  if (server.sha256 !== manifest.artifact.server_sha256 || client.sha256 !== manifest.artifact.client_sha256) fail("ARTIFACT_MATRIX_MISMATCH", "candidate server or client object differs from manifest");
  const fixturePath = resolveContained(options.artifact_root, manifest.artifact.fixture_path, "artifact.fixture_path", deps.path);
  const fixture = digestRegularFile(fixturePath, deps, "ARTIFACT_MANIFEST_INVALID");
  if (fixture.sha256 !== manifest.artifact.fixture_sha256 || !samePath(options.fixture_command, fixturePath, deps.path)) {
    fail("ARTIFACT_MATRIX_MISMATCH", "fixture command differs from the frozen artifact matrix");
  }

  const archivesDirectory = resolveContained(options.artifact_root, manifest.artifact.archives_dir, "artifact.archives_dir", deps.path);
  requireDirectory(archivesDirectory, deps, "ARTIFACT_MANIFEST_INVALID");
  const archiveSpecs = expectedArchives(payload.version);
  const expectedNames = new Set(archiveSpecs.map((archive) => archive.name));
  const candidates = deps.fs.readdirSync(archivesDirectory, { withFileTypes: true })
    .filter((entry) => entry.isFile() && (entry.name.endsWith(".tar.gz") || entry.name.endsWith(".zip")));
  if (candidates.length !== archiveSpecs.length || candidates.some((entry) => !expectedNames.has(entry.name))) {
    fail("ARTIFACT_ARCHIVE_MATRIX_MISMATCH", "archive directory does not contain exactly the supported three-platform matrix");
  }
  const archives = archiveSpecs.map((expected) => {
    const archivePath = deps.path.join(archivesDirectory, expected.name);
    const digest = digestRegularFile(archivePath, deps, "ARTIFACT_ARCHIVE_MATRIX_MISMATCH");
    return { ...expected, size: digest.size, sha256: digest.sha256, archive_path: archivePath };
  });
  if (inspectionRoot) {
    for (const archive of archives) {
      const destination = deps.path.join(inspectionRoot, sha256(archive.name).slice(0, 16));
      await extractArchive(archive.archive_path, destination, options, deps);
      const archivePayload = packagePayload(destination, deps);
      assertPayloadEquals(archivePayload, expectedPayload, "server-plugin archive");
      if (archivePayload.version !== payload.version) fail("ARTIFACT_ARCHIVE_MATRIX_MISMATCH", "archive package version differs from candidate");
    }
  }

  const baselinePlugin = resolveContained(options.artifact_root, manifest.baseline.plugin_path, "baseline.plugin_path", deps.path);
  const baselineClient = resolveContained(options.artifact_root, manifest.baseline.client_path, "baseline.client_path", deps.path);
  const baselinePluginTree = directoryTreeDigest(baselinePlugin, deps);
  const baselineClientDigest = digestRegularFile(baselineClient, deps, "ARTIFACT_MANIFEST_INVALID");
  if (baselinePluginTree.sha256 !== manifest.baseline.plugin_install_tree_sha256 || baselineClientDigest.sha256 !== manifest.baseline.client_sha256) {
    fail("BASELINE_SOURCE_MISMATCH", "baseline plugin or client bytes differ from the frozen manifest");
  }

  return Object.freeze({
    candidate: Object.freeze({
      source_commit: manifest.candidate.source_commit,
      source_tree: manifest.candidate.source_tree,
      design_sha256: manifest.candidate.design_sha256,
      adapter_revision: ADAPTER_REVISION,
      adapter_sha256: payload.adapter_sha256,
    }),
    artifact: Object.freeze({
      version: payload.version,
      package_sha256: payload.package_sha256,
      extension_entry_sha256: payload.extension_entry_sha256,
      relay_helper_sha256: payload.relay_helper_sha256,
      adapter_sha256: payload.adapter_sha256,
      daemon_object_sha256: server.sha256,
      client_object_sha256: client.sha256,
      fixture_object_sha256: fixture.sha256,
      omp_command_sha256: manifest.artifact.omp_command_sha256,
      postgres_image_sha256: manifest.artifact.postgres_image_sha256,
      bootstrap_targets_sha256: payload.bootstrap_targets_sha256,
      install_tree_sha256: payload.install_tree_sha256,
      baseline_source_commit: baselineIdentity.source_commit,
      baseline_source_tree: baselineIdentity.source_tree,
      baseline_plugin_install_tree_sha256: baselinePluginTree.sha256,
      baseline_client_object_sha256: baselineClientDigest.sha256,
      archives: archives.map(({ archive_path, ...archive }) => archive),
    }),
    fixture_command_path: fixturePath,
    fixture_object_sha256: fixture.sha256,
    postgres_image: manifest.artifact.postgres_image,
    candidate_plugin_root: pluginRoot,
    candidate_server_path: serverPath,
    candidate_client_path: clientPath,
    selected_archive: archives.find((archive) => archive.platform === options.platform && archive.arch === (options.arch === "x64" ? "amd64" : options.arch)) || null,
    baseline_plugin_root: baselinePlugin,
    baseline_client_path: baselineClient,
    baseline_source_commit: baselineIdentity.source_commit,
    matrix_sha256: sha256(canonicalize({ manifest_sha256: manifestDigest, candidate: manifest.candidate, artifact: manifest.artifact, baseline: manifest.baseline })),
  });
}

function profileRoots(env, pathApi) {
  const roots = [];
  const seen = new Set();
  const add = (candidate) => {
    const resolved = pathApi.resolve(candidate);
    if (!seen.has(resolved)) {
      seen.add(resolved);
      roots.push(resolved);
    }
  };
  for (const key of ["OMP_PROFILE_ROOT", "OMP_PROFILE_DIR", "PI_CODING_AGENT_DIR", "OMP_CONFIG_DIR", "OMP_CONFIG_HOME", "OMP_HOME"]) {
    const value = typeof env[key] === "string" ? env[key].trim() : "";
    if (!value) continue;
    let current = pathApi.resolve(value);
    for (let depth = 0; depth < 5; depth += 1) {
      add(current);
      const parent = pathApi.dirname(current);
      if (parent === current) break;
      current = parent;
    }
  }
  for (const key of ["USERPROFILE", "HOME"]) {
    const value = typeof env[key] === "string" ? env[key].trim() : "";
    if (value) add(pathApi.join(value, ".omp"));
  }
  return roots;
}

function profileDirectories(root, profile, pathApi) {
  return [...new Set([
    root,
    pathApi.join(root, "plugins"),
    pathApi.join(root, profile),
    pathApi.join(root, profile, "plugins"),
    pathApi.join(root, "profiles", profile),
    pathApi.join(root, "profiles", profile, "plugins"),
    pathApi.join(root, "agent"),
    pathApi.join(root, "profiles", profile, "agent"),
  ])];
}

function profileScope(document, profile) {
  if (isPlainObject(document.profiles) && isPlainObject(document.profiles[profile])) return document.profiles[profile];
  if (Array.isArray(document.profiles)) return document.profiles.find((entry) => isPlainObject(entry) && (entry.name === profile || entry.id === profile || entry.profile === profile)) || document;
  if (isPlainObject(document[profile])) return document[profile];
  return document;
}

function flattenEntries(value) {
  if (Array.isArray(value)) return value.flatMap(flattenEntries);
  return isPlainObject(value) ? [value] : [];
}

function pluginEntries(document, profile) {
  const found = [];
  const visited = new Set();
  const visit = (value, depth) => {
    if (!isPlainObject(value) || depth > 8 || visited.has(value)) return;
    visited.add(value);
    for (const [key, child] of Object.entries(value)) {
      if (PLUGIN_KEYS.has(key)) found.push(...flattenEntries(child));
      visit(child, depth + 1);
    }
  };
  visit(profileScope(document, profile), 0);
  return found;
}

function entryString(entry, keys) {
  for (const key of keys) if (typeof entry[key] === "string" && entry[key].trim()) return entry[key].trim();
  return "";
}

function selectedPluginEntry(document, profile, label, requireInstallPath) {
  const entries = pluginEntries(document, profile).map((entry) => ({
    version: entryString(entry, ["version", "pluginVersion", "plugin_version"]),
    installPath: entryString(entry, ["installPath", "install_path", "path", "root", "directory"]),
    dataPath: entryString(entry, ["dataPath", "data_path", "pluginData", "plugin_data"]),
    configPath: entryString(entry, ["configPath", "config_path"]),
    enabled: entry.enabled !== false && entry.isEnabled !== false && entry.is_enabled !== false,
  })).filter((entry) => entry.enabled);
  if (entries.length === 0) fail("PROFILE_PLUGIN_NOT_ENABLED", `${label} has no enabled engram entry`);
  const unique = new Map(entries.map((entry) => [canonicalize(entry), entry]));
  if (unique.size !== 1) fail("PROFILE_REGISTRY_AMBIGUOUS", `${label} has ambiguous engram entries`);
  const selected = [...unique.values()][0];
  if (!SEMVER.test(selected.version) || (requireInstallPath && !selected.installPath)) fail("PROFILE_REGISTRY_INVALID", `${label} has an invalid selected plugin`);
  return selected;
}

function findNamedFiles(directories, names, deps) {
  const matches = [];
  for (const directory of directories) {
    for (const name of names) {
      const candidate = deps.path.join(directory, name);
      if (exists(candidate, deps)) matches.push(candidate);
    }
  }
  return [...new Set(matches)];
}

function resolveProfileFiles(profile, deps, env = deps.env) {
  if (typeof deps.resolveProfileFiles === "function") return deps.resolveProfileFiles(profile, env);
  const explicitRegistry = typeof env.OMP_PROFILE_REGISTRY === "string" && env.OMP_PROFILE_REGISTRY.trim() ? deps.path.resolve(env.OMP_PROFILE_REGISTRY) : "";
  const explicitLock = typeof env.OMP_PLUGIN_LOCK === "string" && env.OMP_PLUGIN_LOCK.trim() ? deps.path.resolve(env.OMP_PLUGIN_LOCK) : "";
  if (Boolean(explicitRegistry) !== Boolean(explicitLock)) fail("PROFILE_REGISTRY_INVALID", "registry and lock overrides must be supplied together");
  const directories = profileRoots(env, deps.path).flatMap((root) => profileDirectories(root, profile, deps.path));
  const registries = explicitRegistry ? [explicitRegistry] : findNamedFiles(directories, ["installed_plugins.json", "plugins.json", "plugin-registry.json", "registry.json"], deps);
  const locks = explicitLock ? [explicitLock] : findNamedFiles(directories, ["omp-plugins.lock.json"], deps);
  if (registries.length === 0 || locks.length === 0) fail("PROFILE_REGISTRY_UNAVAILABLE", "scratch profile registry or lock is unavailable");
  let lastError;
  for (const registryPath of registries) {
    for (const lockPath of locks) {
      try {
        const registry = parseJson(regularFileBytes(registryPath, deps, "PROFILE_REGISTRY_INVALID"), "PROFILE_REGISTRY_INVALID");
        const lock = parseJson(regularFileBytes(lockPath, deps, "PROFILE_LOCK_INVALID"), "PROFILE_LOCK_INVALID");
        const registryEntry = selectedPluginEntry(registry, profile, "profile registry", true);
        const lockEntry = selectedPluginEntry(lock, profile, "plugin lock", false);
        if (registryEntry.version !== lockEntry.version) fail("PROFILE_LOCK_MISMATCH", "registry and lock choose different versions");
        if (lockEntry.installPath && !samePath(registryEntry.installPath, lockEntry.installPath, deps.path)) fail("PROFILE_LOCK_MISMATCH", "registry and lock choose different plugin roots");
        return {
          registryPath,
          lockPath,
          registryEntry,
          lockEntry,
          registry_sha256: digestRegularFile(registryPath, deps, "PROFILE_REGISTRY_INVALID").sha256,
          lock_sha256: digestRegularFile(lockPath, deps, "PROFILE_LOCK_INVALID").sha256,
        };
      } catch (error) {
        if (!(error instanceof ProbeError)) throw error;
        lastError = error;
      }
    }
  }
  throw lastError || new ProbeError("PROFILE_REGISTRY_UNAVAILABLE", "scratch profile registry could not be resolved");
}

function resolveLinkedPluginList(value, profileRoot, expectedVersion, expectedTree, deps) {
  requireExactKeys(value, ["npm", "marketplace"], "OMP plugin list", "OMP_PLUGIN_LIST_INVALID");
  if (!Array.isArray(value.npm) || !Array.isArray(value.marketplace)) fail("OMP_PLUGIN_LIST_INVALID", "OMP plugin list collections are invalid");
  const entries = [...value.npm, ...value.marketplace].filter((entry) => isPlainObject(entry) && entry.name === "engram" && entry.enabled !== false);
  if (entries.length !== 1) fail("OMP_PLUGIN_LIST_INVALID", "OMP plugin list must select exactly one enabled engram plugin");
  const selected = entries[0];
  if (selected.version !== expectedVersion || !SEMVER.test(selected.version) || typeof selected.path !== "string" || !selected.path) {
    fail("OMP_PLUGIN_LIST_INVALID", "OMP plugin list selected an invalid engram identity");
  }
  requireExactKeys(selected.manifest, ["extensions", "version"], "OMP plugin manifest", "OMP_PLUGIN_LIST_INVALID");
  if (selected.manifest.version !== expectedVersion || !Array.isArray(selected.manifest.extensions) ||
    selected.manifest.extensions.length !== 1 || selected.manifest.extensions[0] !== "./extensions/engram-memory.mjs") {
    fail("OMP_PLUGIN_LIST_INVALID", "OMP plugin manifest differs from the installed extension contract");
  }
  const installPath = deps.path.resolve(selected.path);
  if (!isInside(profileRoot, installPath, deps.path)) fail("OMP_PLUGIN_LIST_INVALID", "OMP plugin install path escapes the scratch profile");
  const tree = directoryTreeDigest(installPath, deps);
  if (tree.sha256 !== expectedTree) fail("OMP_PLUGIN_LIST_INVALID", "OMP plugin install tree differs from the frozen source bytes");
  return Object.freeze({ version: selected.version, installPath, dataPath: "", configPath: "", install_tree_sha256: tree.sha256 });
}

function resolvePluginData(pluginRoot, entry, scratchRoot, deps) {
  if (entry.dataPath) return deps.path.resolve(entry.dataPath);
  const conventional = deps.path.join(scratchRoot, "plugins", "data", "engram-engram");
  try {
    const launcher = require(deps.path.join(pluginRoot, "scripts", "run-engram.js"));
    for (const name of ["resolvePluginData", "inferCodexPluginDataDir"]) {
      if (typeof launcher[name] !== "function") continue;
      const candidate = launcher[name](pluginRoot);
      if (typeof candidate === "string" && candidate) return deps.path.resolve(candidate);
    }
  } catch {
    // The artifact is only inspected; its launcher never executes in the runner process.
  }
  return conventional;
}

function activeEnvelopeFiles(env, deps) {
  const profile = typeof env.OMP_PROFILE === "string" && env.OMP_PROFILE.trim()
    ? env.OMP_PROFILE.trim()
    : (typeof env.PI_PROFILE === "string" && env.PI_PROFILE.trim() ? env.PI_PROFILE.trim() : "implicit");
  const directories = profileRoots(env, deps.path).flatMap((root) => profileDirectories(root, profile, deps.path));
  const names = ["installed_plugins.json", "plugins.json", "plugin-registry.json", "registry.json", "omp-plugins.lock.json", "config.json", "config.yml", "config.yaml", "models.yml", "models.yaml"];
  const candidates = findNamedFiles(directories, names, deps).filter((candidate) => {
    try {
      const stat = deps.fs.lstatSync(candidate);
      return stat.isFile() && !stat.isSymbolicLink();
    } catch {
      return false;
    }
  });
  return [...new Set(candidates)].sort();
}

function snapshotActiveProfile(deps) {
  if (typeof deps.snapshotActiveProfile === "function") return deps.snapshotActiveProfile();
  const envEntries = Object.keys(deps.env)
    .filter((key) => /^(?:OMP|PI|ENGRAM|CLAUDE_PLUGIN_OPTION)_/i.test(key))
    .sort()
    .map((key) => [key, sha256(`${key}\0${String(deps.env[key])}`)]);
  const files = activeEnvelopeFiles(deps.env, deps).map((file) => {
    const digest = digestRegularFile(file, deps, "ACTIVE_PROFILE_INPUT_INVALID");
    return { binding_sha256: sha256(deps.path.resolve(file)), size: digest.size, sha256: digest.sha256 };
  });
  return Object.freeze({ sha256: sha256(canonicalize({ env: envEntries, files })), file_count: files.length });
}

function childEnvironment(source, additions) {
  const env = {};
  for (const [key, value] of Object.entries(source || {})) {
    if (BASE_CHILD_ENV_KEYS.has(key.toUpperCase()) && typeof value === "string") env[key] = value;
  }
  return { ...env, ...additions };
}

function scratchEnvironment(root, profile, deps) {
  const home = deps.path.join(root, "home");
  const agent = deps.path.join(home, ".omp", "profiles", profile, "agent");
  const cache = deps.path.join(root, "cache");
  const temp = deps.path.join(root, "tmp");
  const localAppData = deps.path.join(root, "localappdata");
  for (const directory of [home, agent, cache, temp, localAppData]) ensureDirectory(directory, deps, "SCRATCH_ENV_INVALID");
  return childEnvironment(deps.env, {
    HOME: home,
    USERPROFILE: home,
    LOCALAPPDATA: localAppData,
    XDG_CACHE_HOME: cache,
    XDG_CONFIG_HOME: deps.path.join(root, "config"),
    TEMP: temp,
    TMP: temp,
    TMPDIR: temp,
    OMP_PROFILE: profile,
    PI_PROFILE: profile,
    PI_CODING_AGENT_DIR: agent,
    ENGRAM_DATA_DIR: deps.path.join(root, "engram-data"),
  }, deps);
}

function listenServer(server, address) {
  return new Promise((resolve, reject) => {
    const onError = (error) => { cleanup(); reject(error); };
    const onListen = () => {
      cleanup();
      resolve(server.address());
    };
    const cleanup = () => {
      if (typeof server.off === "function") {
        server.off("error", onError);
        server.off("listening", onListen);
      }
    };
    server.once("error", onError);
    server.once("listening", onListen);
    server.listen(address);
  });
}

function closeServer(server) {
  return new Promise((resolve) => {
    try { server.close(() => resolve()); } catch { resolve(); }
  });
}

function dialEndpoint(logicalEndpoint, platform) {
  if (platform !== "win32") return logicalEndpoint;
  const digest = sha256(String(logicalEndpoint).toLowerCase()).slice(0, 32);
  return `\\\\.\\pipe\\mcp-mux-${digest}`;
}

function relayCallback(frame, state) {
  const body = isPlainObject(frame.body) ? frame.body : {};
  const session = typeof body.hostSessionRef === "string" ? sha256(body.hostSessionRef) : "unknown";
  const entry = state.get(session) || { identity_attempts: 0, stale_callback: null };
  let callback;
  if (frame.route === "SESSION_START_CONTEXT") callback = "session_start";
  else if (frame.route === "AMBIENT_CANDIDATES") callback = "before_agent_start";
  else if (entry.stale_callback) {
    callback = entry.stale_callback;
    entry.stale_callback = null;
  } else {
    callback = entry.identity_attempts === 0 ? "session_start" : "before_agent_start";
    entry.identity_attempts += 1;
  }
  state.set(session, entry);
  return { callback, session };
}

function parseRelayFrame(bytes) {
  const newline = bytes.indexOf(0x0a);
  if (newline < 0 || newline === 0 || newline !== bytes.length - 1) return null;
  try {
    const frame = JSON.parse(bytes.subarray(0, newline).toString("utf8"));
    if (!isPlainObject(frame) || frame.protocol !== RELAY_PROTOCOL || !RELAY_ROUTES.has(frame.route) || !Number.isSafeInteger(frame.deadlineUnixMs)) return null;
    return frame;
  } catch {
    return null;
  }
}

function relayOutcome(bytes, expected) {
  const newline = bytes.indexOf(0x0a);
  if (newline < 0) return null;
  try {
    const response = JSON.parse(bytes.subarray(0, newline).toString("utf8"));
    if (!isPlainObject(response) || response.protocol !== RELAY_PROTOCOL || response.route !== expected.route || response.requestId !== expected.requestId || response.daemonGeneration !== expected.daemonGeneration) return null;
    return RELAY_OUTCOMES.has(response.kind) ? response.kind : null;
  } catch {
    return null;
  }
}

function createRelayTap(options, deps) {
  let server = null;
  let mode = "normal";
  let upstream = null;
  let locatorPath = "";
  let currentLocator = null;
  let stalePublished = false;
  const records = [];
  const callbackState = new Map();
  const activeSockets = new Set();
  const activeUpstreams = new Set();
  const deadlineState = new Map();
  let sharedDeadline = true;
  let rediscoveries = 0;
  let staleCallback = null;

  function appendRecord(frame, callback) {
    const previous = deadlineState.get(callback);
    if (previous !== undefined && previous !== frame.deadlineUnixMs) sharedDeadline = false;
    if (previous === undefined) deadlineState.set(callback, frame.deadlineUnixMs);
    const record = {
      callback,
      route: frame.route,
      outcome: "NO_DELIVERY",
      request_sha256: sha256(Buffer.from(JSON.stringify(frame))),
      response_sha256: null,
    };
    records.push(record);
    return record;
  }

  function publish(locator) {
    if (!locatorPath || !locator) fail("RELAY_TAP_INVALID", "relay tap locator is not configured");
    writeAtomicJson(locatorPath, {
      protocol: RELAY_PROTOCOL,
      daemon_generation: locator.daemon_generation,
      endpoint: options.logical_endpoint,
    }, deps);
  }

  function staleResponse(frame) {
    return Buffer.from(`${JSON.stringify({
      protocol: RELAY_PROTOCOL,
      requestId: frame.requestId,
      daemonGeneration: frame.daemonGeneration,
      route: frame.route,
      kind: "NO_DELIVERY",
      reason: "STALE_GENERATION",
    })}\n`, "utf8");
  }

  function handler(socket) {
    let buffered = Buffer.alloc(0);
    let handled = false;
    let upstreamSocket = null;
    activeSockets.add(socket);
    const closeUpstream = () => {
      if (!upstreamSocket) return;
      activeUpstreams.delete(upstreamSocket);
      try { upstreamSocket.destroy(); } catch { /* closed upstream */ }
      upstreamSocket = null;
    };
    const reject = () => {
      closeUpstream();
      try { socket.destroy(); } catch { /* closed peer */ }
    };
    socket.on("data", (chunk) => {
      if (handled) return;
      buffered = Buffer.concat([buffered, Buffer.from(chunk)]);
      if (buffered.length > MAX_PROXY_FRAME_BYTES) {
        reject();
        return;
      }
      if (buffered.indexOf(0x0a) < 0) return;
      handled = true;
      const frame = parseRelayFrame(buffered);
      if (!frame) {
        reject();
        return;
      }
      const selection = relayCallback(frame, callbackState);
      const record = appendRecord(frame, selection.callback);
      if (mode === "outage") {
        try { socket.end(); } catch { /* closed peer */ }
        return;
      }
      if (mode === "stale_generation" && !stalePublished) {
        stalePublished = true;
        staleCallback = selection.callback;
        callbackState.get(selection.session).stale_callback = selection.callback;
        const response = staleResponse(frame);
        record.response_sha256 = sha256(response);
        record.outcome = "NO_DELIVERY";
        publish(currentLocator);
        try { socket.end(response); } catch { /* closed peer */ }
        return;
      }
      if (mode === "stale_generation" && stalePublished && staleCallback === selection.callback && frame.route === "IDENTITY_REGISTRATION") {
        rediscoveries += 1;
        staleCallback = null;
      }
      try {
        upstreamSocket = deps.net.createConnection({ path: dialEndpoint(upstream.endpoint, deps.platform) });
        activeUpstreams.add(upstreamSocket);
      } catch {
        try { socket.end(); } catch { /* closed peer */ }
        return;
      }
      let response = Buffer.alloc(0);
      upstreamSocket.once("error", () => {
        closeUpstream();
        try { socket.end(); } catch { /* closed peer */ }
      });
      upstreamSocket.once("connect", () => {
        try { upstreamSocket.end(buffered); } catch { closeUpstream(); try { socket.end(); } catch { /* closed peer */ } }
      });
      upstreamSocket.on("data", (responseChunk) => {
        const bytes = Buffer.from(responseChunk);
        if (response.length < MAX_PROXY_FRAME_BYTES) response = Buffer.concat([response, bytes.subarray(0, MAX_PROXY_FRAME_BYTES - response.length)]);
        const outcome = relayOutcome(response, frame);
        if (outcome) {
          record.outcome = outcome;
          record.response_sha256 = sha256(response);
        }
        try { socket.write(bytes); } catch { /* peer closed */ }
      });
      upstreamSocket.once("end", () => {
        const outcome = relayOutcome(response, frame);
        record.outcome = outcome || "NO_DELIVERY";
        record.response_sha256 = response.length ? sha256(response) : null;
        try { socket.end(); } catch { /* peer closed */ }
      });
      upstreamSocket.once("close", () => {
        if (upstreamSocket) activeUpstreams.delete(upstreamSocket);
        if (!record.response_sha256) {
          const outcome = relayOutcome(response, frame);
          record.outcome = outcome || "NO_DELIVERY";
          record.response_sha256 = response.length ? sha256(response) : null;
        }
      });
    });
    socket.once("close", () => {
      activeSockets.delete(socket);
      closeUpstream();
    });
    socket.once("error", closeUpstream);
  }

  return Object.freeze({
    async start() {
      if (server) fail("RELAY_TAP_INVALID", "relay tap is already running");
      if (typeof options.logical_endpoint !== "string" || !deps.path.isAbsolute(options.logical_endpoint)) fail("RELAY_TAP_INVALID", "relay tap endpoint is invalid");
      ensureDirectory(deps.path.dirname(options.logical_endpoint), deps, "OWNED_DIRECTORY_INVALID");
      server = deps.net.createServer({ allowHalfOpen: true }, handler);
      await listenServer(server, dialEndpoint(options.logical_endpoint, deps.platform), deps);
      return { endpoint_sha256: sha256(options.logical_endpoint) };
    },
    installLocator(locator, targetPath) {
      if (!isPlainObject(locator) || locator.protocol !== RELAY_PROTOCOL || typeof locator.daemon_generation !== "string" || typeof locator.endpoint !== "string") {
        fail("RELAY_TAP_INVALID", "upstream relay locator is invalid");
      }
      locatorPath = targetPath;
      upstream = { endpoint: locator.endpoint };
      currentLocator = { daemon_generation: locator.daemon_generation };
      publish(currentLocator);
    },
    armStaleGeneration(staleGeneration) {
      if (typeof staleGeneration !== "string" || !staleGeneration) fail("RELAY_TAP_INVALID", "stale generation is invalid");
      if (!currentLocator) fail("RELAY_TAP_INVALID", "relay tap has no current locator");
      stalePublished = false;
      mode = "stale_generation";
      writeAtomicJson(locatorPath, { protocol: RELAY_PROTOCOL, daemon_generation: staleGeneration, endpoint: options.logical_endpoint }, deps);
    },
    setMode(next) {
      if (!new Set(["normal", "outage", "stale_generation"]).has(next)) fail("RELAY_TAP_INVALID", "relay tap mode is invalid");
      mode = next;
    },
    mark() { return records.length; },
    snapshot(start = 0) {
      const selected = records.slice(start).map((record) => ({ ...record }));
      return Object.freeze({
        records: selected,
        shared_deadline: sharedDeadline,
        rediscoveries,
        server_dispatches: selected.filter((record) => record.outcome === "OK").length,
      });
    },
    async close() {
      for (const socket of activeSockets) {
        try { socket.destroy(); } catch { /* closed peer */ }
      }
      for (const connection of activeUpstreams) {
        try { connection.destroy(); } catch { /* closed upstream */ }
      }
      activeSockets.clear();
      activeUpstreams.clear();
      if (server) {
        const active = server;
        server = null;
        await closeServer(active);
      }
      if (locatorPath) {
        try { deps.fs.rmSync(locatorPath, { force: true }); } catch { /* owned locator cleanup is recorded by caller */ }
      }
      callbackState.clear();
      deadlineState.clear();
    },
  });
}

function createServerTap(options, deps) {
  let server = null;
  const records = [];
  const activeSockets = new Set();
  const activeUpstreams = new Set();
  function parseRequest(bytes) {
    const end = bytes.indexOf("\r\n\r\n");
    if (end < 0) return null;
    const text = bytes.subarray(0, end).toString("latin1");
    const [requestLine, ...headers] = text.split("\r\n");
    const match = /^([A-Z]+) ([^ ]+) HTTP\/1\.[01]$/.exec(requestLine);
    if (!match) return null;
    return {
      method: match[1],
      path: match[2].split("?")[0],
      authorization_present: headers.some((line) => /^authorization\s*:/i.test(line)),
      status: null,
    };
  }
  function parseResponse(bytes) {
    const lineEnd = bytes.indexOf("\r\n");
    if (lineEnd < 0) return null;
    const match = /^HTTP\/1\.[01] (\d{3})\b/.exec(bytes.subarray(0, lineEnd).toString("latin1"));
    return match ? Number(match[1]) : null;
  }
  function handler(socket) {
    let request = Buffer.alloc(0);
    let response = Buffer.alloc(0);
    let current = null;
    let upstream;
    activeSockets.add(socket);
    socket.on("data", (chunk) => {
      const bytes = Buffer.from(chunk);
      if (!current && request.length < 32 * 1024) {
        request = Buffer.concat([request, bytes.subarray(0, 32 * 1024 - request.length)]);
        current = parseRequest(request);
        if (current) records.push(current);
      }
      if (upstream) {
        try { upstream.write(bytes); } catch { /* peer cleanup */ }
      }
    });
    try {
      upstream = deps.net.createConnection({ host: options.target_host, port: options.target_port });
      activeUpstreams.add(upstream);
    } catch {
      try { socket.destroy(); } catch { /* closed peer */ }
      return;
    }
    upstream.once("error", () => { try { socket.destroy(); } catch { /* closed peer */ } });
    upstream.on("data", (chunk) => {
      const bytes = Buffer.from(chunk);
      if (current && current.status === null && response.length < 8 * 1024) {
        response = Buffer.concat([response, bytes.subarray(0, 8 * 1024 - response.length)]);
        current.status = parseResponse(response);
      }
      try { socket.write(bytes); } catch { /* closed peer */ }
    });
    upstream.once("end", () => { try { socket.end(); } catch { /* closed peer */ } });
    upstream.once("close", () => { activeUpstreams.delete(upstream); });
    socket.once("end", () => { try { upstream.end(); } catch { /* closed peer */ } });
    socket.once("close", () => {
      activeSockets.delete(socket);
      activeUpstreams.delete(upstream);
      try { upstream.destroy(); } catch { /* closed peer */ }
    });
    socket.once("error", () => { try { upstream.destroy(); } catch { /* closed peer */ } });
  }
  return Object.freeze({
    async start() {
      if (server) fail("SERVER_TAP_INVALID", "server tap is already running");
      server = deps.net.createServer({ allowHalfOpen: true }, handler);
      const address = await listenServer(server, { host: "127.0.0.1", port: 0 }, deps);
      if (!address || typeof address === "string" || !Number.isInteger(address.port)) fail("SERVER_TAP_INVALID", "server tap did not bind a TCP port");
      return Object.freeze({ host: "127.0.0.1", port: address.port, url: `http://127.0.0.1:${address.port}` });
    },
    mark() { return records.length; },
    snapshot(start = 0) {
      const selected = records.slice(start).map((record) => ({ ...record }));
      return Object.freeze({ records: selected, authorization_seen: selected.some((record) => record.authorization_present) });
    },
    async close() {
      for (const socket of activeSockets) {
        try { socket.destroy(); } catch { /* closed peer */ }
      }
      for (const connection of activeUpstreams) {
        try { connection.destroy(); } catch { /* closed upstream */ }
      }
      activeSockets.clear();
      activeUpstreams.clear();
      if (!server) return;
      const active = server;
      server = null;
      await closeServer(active);
    },
  });
}

function observerProjection(observerPath, deps) {
  if (!exists(observerPath, deps)) return { callbacks: [], extension_url_present: false, extension_token_present: false, extension_config_path_present: false, scratch_cwd: false };
  const bytes = regularFileBytes(observerPath, deps, "OBSERVER_INVALID", MAX_TRANSCRIPT_FILE_BYTES);
  const callbacks = [];
  let extensionURL = false;
  let extensionToken = false;
  let extensionConfig = false;
  let scratchCwd = true;
  for (const line of bytes.toString("utf8").split("\n")) {
    if (!line.trim()) continue;
    let observation;
    try { observation = JSON.parse(line); } catch { fail("OBSERVER_INVALID", "observer output is malformed"); }
    requireExactKeys(observation, ["kind", "extension_url_present", "extension_token_present", "extension_config_path_present", "scratch_cwd", "timestamp"], "observer output", "OBSERVER_INVALID");
    if (!CALLBACKS.has(observation.kind) || typeof observation.extension_url_present !== "boolean" || typeof observation.extension_token_present !== "boolean" || typeof observation.extension_config_path_present !== "boolean" || typeof observation.scratch_cwd !== "boolean" || typeof observation.timestamp !== "string") {
      fail("OBSERVER_INVALID", "observer output has an invalid value");
    }
    callbacks.push(observation.kind);
    extensionURL ||= observation.extension_url_present;
    extensionToken ||= observation.extension_token_present;
    extensionConfig ||= observation.extension_config_path_present;
    scratchCwd &&= observation.scratch_cwd;
  }
  return Object.freeze({
    callbacks,
    extension_url_present: extensionURL,
    extension_token_present: extensionToken,
    extension_config_path_present: extensionConfig,
    scratch_cwd: scratchCwd,
  });
}

function readBoundedRegularFile(filePath, maxBytes, deps) {
  const stat = deps.fs.lstatSync(filePath);
  if (!stat.isFile() || stat.isSymbolicLink()) fail("TRANSCRIPT_INVALID", "transcript file is not regular");
  const descriptor = deps.fs.openSync(filePath, "r");
  try {
    const buffer = Buffer.alloc(Math.min(stat.size, maxBytes));
    const count = buffer.length ? deps.fs.readSync(descriptor, buffer, 0, buffer.length, 0) : 0;
    return { bytes: buffer.subarray(0, count), truncated: stat.size > count };
  } finally {
    deps.fs.closeSync(descriptor);
  }
}

function sessionTranscriptProjection(directory, deps) {
  let files = 0;
  let customMessages = 0;
  let complete = true;
  const contentHash = crypto.createHash("sha256");
  function walk(current) {
    let entries;
    try { entries = deps.fs.readdirSync(current, { withFileTypes: true }); } catch { complete = false; return; }
    for (const entry of entries) {
      if (files >= MAX_TRANSCRIPT_FILES) { complete = false; return; }
      const candidate = deps.path.join(current, entry.name);
      if (entry.isDirectory()) { walk(candidate); continue; }
      if (!entry.isFile() || !entry.name.endsWith(".jsonl")) continue;
      let read;
      try { read = readBoundedRegularFile(candidate, MAX_TRANSCRIPT_FILE_BYTES, deps); } catch { complete = false; continue; }
      files += 1;
      if (read.truncated) complete = false;
      for (const line of read.bytes.toString("utf8").split("\n")) {
        if (!line.trim()) continue;
        try {
          const entryValue = JSON.parse(line);
          if (isPlainObject(entryValue) && entryValue.type === "custom_message" && entryValue.customType === "engram-memory") {
            customMessages += 1;
            contentHash.update(typeof entryValue.content === "string" ? entryValue.content : "", "utf8");
          }
        } catch {
          complete = false;
        }
      }
    }
  }
  if (exists(directory, deps)) walk(directory);
  return Object.freeze({ file_count: files, custom_message_count: customMessages, complete, content_sha256: contentHash.digest("hex") });
}

function signalOwnedProcessTree(child, signal, deps) {
  if (deps.platform !== "win32" && Number.isInteger(child?.pid) && child.pid > 0) {
    try { deps.processKill(-child.pid, signal); return true; } catch { /* fall back to the direct child */ }
  }
  try { return Boolean(child && typeof child.kill === "function" && child.kill(signal)); } catch { return false; }
}

async function forceOwnedProcessTree(child, deps, timeoutMs) {
  if (deps.platform === "win32" && Number.isInteger(child?.pid) && child.pid > 0) {
    const result = await runProcess({
      command: "taskkill.exe",
      args: ["/PID", String(child.pid), "/T", "/F"],
      cwd: deps.cwd(),
      env: childEnvironment(deps.env, {}, deps),
      timeout_ms: Math.max(1_000, timeoutMs),
    }, deps);
    return result.started && !result.error && !result.timed_out && !result.close_unconfirmed && result.exit_code === 0;
  }
  return signalOwnedProcessTree(child, "SIGKILL", deps);
}

function stopOwnedChild(child, deps, timeoutMs = 5_000) {
  if (!child || typeof child.kill !== "function" || typeof child.once !== "function") return Promise.resolve(false);
  if (child.exitCode !== null && child.exitCode !== undefined || child.signalCode) return Promise.resolve(true);
  return new Promise((resolve) => {
    let settled = false;
    let terminateTimer;
    let killTimer;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      deps.clearTimeout(terminateTimer);
      deps.clearTimeout(killTimer);
      child.off?.("close", onClose);
      resolve(value);
    };
    const onClose = () => finish(true);
    child.once("close", onClose);
    const force = async () => {
      try { await forceOwnedProcessTree(child, deps, timeoutMs); } catch { /* final timer reports failure */ }
      killTimer = deps.setTimeout(() => finish(false), 1_500);
    };
    if (!signalOwnedProcessTree(child, "SIGTERM", deps)) {
      void force();
      return;
    }
    terminateTimer = deps.setTimeout(() => { void force(); }, Math.max(100, timeoutMs - 1_500));
  });
}

function withCleanupResidue(error, residue) {
  if (error && typeof error === "object" && Number.isSafeInteger(residue) && residue > 0) {
    Object.defineProperty(error, "cleanup_residue", { value: residue, configurable: true });
  }
  return error;
}

function cleanupResidueFrom(error) {
  return Number.isSafeInteger(error?.cleanup_residue) && error.cleanup_residue > 0 ? error.cleanup_residue : 0;
}

async function closeResourceStack(resources) {
  let residue = 0;
  for (const resource of [...resources].reverse()) {
    try {
      if (!resource || typeof resource.close !== "function") { residue += 1; continue; }
      const closed = await resource.close();
      if (Number.isSafeInteger(closed) && closed > 0) residue += closed;
      else if (closed === false) residue += 1;
    } catch {
      residue += 1;
    }
  }
  return residue;
}

async function rethrowAfterCleanup(error, resources) {
  const residue = cleanupResidueFrom(error) + await closeResourceStack(resources);
  throw withCleanupResidue(error, residue);
}

function runBoundedChild(spec, sentinel, deps) {
  return new Promise((resolve) => {
    let child;
    let settled = false;
    let timedOut = false;
    let timer;
    let stdoutBytes = 0;
    let stderrBytes = 0;
    let sentinelPresent = false;
    let sentinelWindow = "";
    const finish = (result) => {
      if (settled) return;
      settled = true;
      deps.clearTimeout(timer);
      resolve({ stdout_bytes: stdoutBytes, stderr_bytes: stderrBytes, sentinel_present: sentinelPresent, ...result });
    };
    try {
      child = deps.spawn(spec.command, spec.args, { cwd: spec.cwd, env: spec.env, stdio: ["ignore", "pipe", "pipe"], windowsHide: true, detached: deps.platform !== "win32" });
    } catch {
      finish({ started: false, error: true, exit_code: null, timed_out: false, close_unconfirmed: false });
      return;
    }
    const capture = (kind) => (chunk) => {
      const bytes = Buffer.from(chunk);
      if (kind === "stdout") {
        stdoutBytes += bytes.length;
        if (!sentinelPresent) {
          sentinelWindow = (sentinelWindow + bytes.toString("utf8")).slice(-Math.max(sentinel.length * 2, 256));
          if (sentinelWindow.includes(sentinel)) sentinelPresent = true;
        }
      } else stderrBytes += bytes.length;
    };
    child.stdout?.on("data", capture("stdout"));
    child.stderr?.on("data", capture("stderr"));
    child.once("error", () => finish({ started: true, error: true, exit_code: null, timed_out: timedOut, close_unconfirmed: false }));
    child.once("close", (code, signal) => finish({ started: true, error: false, exit_code: Number.isInteger(code) ? code : null, exit_signal: typeof signal === "string" ? signal : null, timed_out: timedOut, close_unconfirmed: false }));
    timer = deps.setTimeout(() => {
      timedOut = true;
      void stopOwnedChild(child, deps, CHILD_SETTLE_GRACE_MS).then((stopped) => {
        finish({ started: true, error: false, exit_code: null, timed_out: true, close_unconfirmed: !stopped });
      });
    }, spec.timeout_ms);
  });
}

function turnIsComplete(child, transcript) {
  return Boolean(child?.started && !child.error && child.exit_code === 0 && !child.timed_out &&
    !child.close_unconfirmed && child.sentinel_present && transcript?.complete && transcript.file_count > 0);
}

async function waitForTcpHealth(target, timeoutMs, deps) {
  const deadline = deps.now() + timeoutMs;
  while (deps.now() < deadline) {
    const status = await new Promise((resolve) => {
      let socket;
      let done = false;
      const finish = (value) => { if (!done) { done = true; try { socket?.destroy(); } catch { /* peer closed */ } resolve(value); } };
      try { socket = deps.net.createConnection({ host: target.host, port: target.port }); } catch { resolve(0); return; }
      let response = Buffer.alloc(0);
      socket.once("error", () => finish(0));
      socket.once("connect", () => socket.write("GET /api/ready HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n"));
      socket.on("data", (chunk) => {
        if (response.length < 4096) response = Buffer.concat([response, Buffer.from(chunk).subarray(0, 4096 - response.length)]);
        const match = /^HTTP\/1\.[01] (\d{3})\b/.exec(response.toString("latin1"));
        if (match) finish(Number(match[1]));
      });
      socket.once("end", () => finish(0));
    });
    if (status === 200) return;
    await deps.sleep(200);
  }
  fail("SERVER_STARTUP_TIMEOUT", "candidate server did not become ready on loopback");
}

async function createLocalModelFixture(root, deps) {
  if (typeof deps.createLocalModelFixture === "function") return deps.createLocalModelFixture(root);
  const server = deps.net.createServer((socket) => {
    let input = Buffer.alloc(0);
    let answered = false;
    socket.on("data", (chunk) => {
      if (answered) return;
      if (input.length < 128 * 1024) input = Buffer.concat([input, Buffer.from(chunk).subarray(0, 128 * 1024 - input.length)]);
      const headerEnd = input.indexOf("\r\n\r\n");
      if (headerEnd < 0) return;
      const headersText = input.subarray(0, headerEnd).toString("latin1");
      const lengthMatch = /(?:^|\r\n)content-length:\s*(\d+)\s*(?:\r\n|$)/i.exec(headersText);
      const contentLength = lengthMatch ? Number(lengthMatch[1]) : 0;
      if (!Number.isSafeInteger(contentLength) || contentLength < 0 || contentLength > 120 * 1024 || input.length < headerEnd + 4 + contentLength) return;
      answered = true;
      const body = input.subarray(headerEnd + 4, headerEnd + 4 + contentLength).toString("utf8");
      const streaming = /"stream"\s*:\s*true/.test(body);
      const payload = streaming
        ? `data: ${JSON.stringify({ id: "hap01c", object: "chat.completion.chunk", choices: [{ index: 0, delta: { content: MODEL_SENTINEL }, finish_reason: null }] })}\n\ndata: ${JSON.stringify({ id: "hap01c", object: "chat.completion.chunk", choices: [{ index: 0, delta: {}, finish_reason: "stop" }] })}\n\ndata: [DONE]\n\n`
        : JSON.stringify({ id: "hap01c", object: "chat.completion", choices: [{ index: 0, message: { role: "assistant", content: MODEL_SENTINEL }, finish_reason: "stop" }] });
      const headers = streaming
        ? `HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: ${Buffer.byteLength(payload)}\r\nConnection: close\r\n\r\n`
        : `HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: ${Buffer.byteLength(payload)}\r\nConnection: close\r\n\r\n`;
      socket.end(`${headers}${payload}`);
    });
    socket.once("error", () => { });
  });
  const address = await listenServer(server, { host: "127.0.0.1", port: 0 }, deps);
  if (!address || typeof address === "string" || !Number.isInteger(address.port)) fail("MODEL_FIXTURE_INVALID", "local model did not bind loopback");
  return Object.freeze({
    url: `http://127.0.0.1:${address.port}/v1`,
    async close() { await closeServer(server); },
  });
}

function writeModelConfig(agentDir, modelURL, deps) {
  const modelConfig = [
    "providers:",
    `  ${MODEL_PROVIDER}:`,
    `    baseUrl: ${modelURL}`,
    "    api: openai-completions",
    "    auth: none",
    "    models:",
    `      - id: ${MODEL_NAME}`,
    "        name: HAP-01C Local",
    "        contextWindow: 4096",
    "        maxTokens: 64",
    "        supportsTools: false",
    "",
  ].join("\n");
  ensureDirectory(agentDir, deps, "MODEL_CONFIG_INVALID");
  writeSecretText(deps.path.join(agentDir, "models.yml"), modelConfig, deps);
}

function fixtureRoot(options, deps) {
  return deps.path.join(options.scratch_dir, "fixture", `hap01c-${options.run_id}`);
}

function fixtureRequest(options, deps) {
  const uuid = deps.randomUUID;
  const legacyWorkspace = deps.path.resolve(options.scratch_dir, "scenarios", "old_plugin_new_daemon", "workspace");
  return Object.freeze({
    run_id: options.run_id,
    anchor_project_id: uuid(),
    canonical_project_key: uuid(),
    legacy_project_id: sha256(legacyWorkspace).slice(0, 6),
  });
}

function relativeFixturePath(options, filePath, deps) {
  const relative = deps.path.relative(options.cwd, filePath);
  if (!relative || relative === ".." || relative.startsWith(`..${deps.path.sep}`) || deps.path.isAbsolute(relative)) fail("FIXTURE_PATH_INVALID", "fixture path is not project-contained");
  return relative;
}

async function invokeFixture(action, args, options, deps) {
  const result = requireProcessSuccess(await runProcess({
    command: options.fixture_command,
    args: [action, ...args],
    cwd: options.cwd,
    env: childEnvironment(deps.env, {}, deps),
    timeout_ms: options.timeouts.startup_timeout_ms,
  }, deps), "FIXTURE_COMMAND_FAILED", `fixture ${action}`);
  return Object.freeze({ stdout_sha256: result.stdout_sha256, stderr_sha256: result.stderr_sha256 });
}

function readFixtureSecrets(filePath, options, deps) {
  const value = parseJson(regularFileBytes(filePath, deps, "FIXTURE_SECRETS_INVALID"), "FIXTURE_SECRETS_INVALID");
  requireExactKeys(value, [
    "schema", "run_id", "ordinary_token", "registration_token", "project_token", "legacy_direct_token",
    "ordinary_token_id", "registration_token_id", "project_token_id", "legacy_direct_token_id",
  ], "fixture secrets", "FIXTURE_SECRETS_INVALID");
  if (value.schema !== "hap-01c-fixture-secrets/1" || value.run_id !== options.run_id) fail("FIXTURE_SECRETS_INVALID", "fixture secrets are not bound to this run");
  for (const key of ["ordinary_token", "registration_token", "project_token", "legacy_direct_token"]) {
    if (typeof value[key] !== "string" || !/^engram_[0-9a-f]{32}$/i.test(value[key])) fail("FIXTURE_SECRETS_INVALID", "fixture secret has an invalid synthetic keycard shape");
  }
  return value;
}

function parseFixtureSnapshot(filePath, options, deps) {
  const value = parseJson(regularFileBytes(filePath, deps, "FIXTURE_SNAPSHOT_INVALID"), "FIXTURE_SNAPSHOT_INVALID");
  requireExactKeys(value, [
    "schema", "run_id_sha256", "resolution_attempts", "registration_attempts", "session_start_attempts", "target_memory_injection_count",
    "memory_rows", "rule_rows", "active_project_token_count", "revoked_project_token_count", "ambient_delivery_available", "ambient_delivery_unavailable_reason",
  ], "fixture snapshot", "FIXTURE_SNAPSHOT_INVALID");
  if (value.schema !== "hap-01c-fixture-snapshot/1" || value.run_id_sha256 !== sha256(options.run_id) || value.ambient_delivery_available !== false || value.ambient_delivery_unavailable_reason !== "NO_DURABLE_AMBIENT_ATTEMPT_COUNTER") {
    fail("FIXTURE_SNAPSHOT_INVALID", "fixture snapshot is not the fixed HAP-01C shape");
  }
  for (const key of ["resolution_attempts", "registration_attempts", "session_start_attempts", "target_memory_injection_count", "memory_rows", "rule_rows", "active_project_token_count", "revoked_project_token_count"]) {
    if (!Number.isSafeInteger(value[key]) || value[key] < 0) fail("FIXTURE_SNAPSHOT_INVALID", "fixture snapshot counter is invalid");
  }
  return Object.freeze({
    resolution_attempts: value.resolution_attempts,
    registration_attempts: value.registration_attempts,
    session_start_attempts: value.session_start_attempts,
    target_memory_injection_count: value.target_memory_injection_count,
    active_project_token_count: value.active_project_token_count,
    revoked_project_token_count: value.revoked_project_token_count,
    ambient_delivery_available: false,
    ambient_delivery_reason_sha256: sha256(value.ambient_delivery_unavailable_reason),
  });
}

async function snapshotFixture(runtime, label, deps) {
  const out = runtime.deps.path.join(runtime.fixture_root, "snapshots", `${label}.json`);
  ensureDirectory(runtime.deps.path.dirname(out), runtime.deps, "FIXTURE_PATH_INVALID");
  await invokeFixture("snapshot", [
    "--run-id", runtime.options.run_id,
    "--dsn-file", relativeFixturePath(runtime.options, runtime.dsn_file, runtime.deps),
    "--request-file", relativeFixturePath(runtime.options, runtime.seed_request_file, runtime.deps),
    "--out", relativeFixturePath(runtime.options, out, runtime.deps),
  ], runtime.options, deps);
  const snapshot = parseFixtureSnapshot(out, runtime.options, deps);
  try { deps.fs.rmSync(out, { force: true }); } catch { /* raw control snapshot has already been projected */ }
  return snapshot;
}

function stablePostgresProbeCount(current, result) {
  const succeeded = Boolean(result?.started && !result.error && !result.timed_out && !result.close_unconfirmed && result.exit_code === 0 &&
    Buffer.from(result.stdout || "").toString("utf8").trim() === "1");
  return succeeded ? current + 1 : 0;
}

async function startPostgres(options, matrix, root, deps) {
  if (typeof deps.startPostgres === "function") return deps.startPostgres(options, matrix, root);
  const container = `hap01c-${options.run_id}`;
  const postgresRoot = deps.path.join(root, "postgres");
  createExclusiveDirectory(postgresRoot, deps);
  const password = deps.randomBytes(24).toString("base64url");
  const envFile = deps.path.join(postgresRoot, "postgres.env");
  const database = `hap01c_${options.run_id}`;
  writeSecretText(envFile, `POSTGRES_USER=hap01c\nPOSTGRES_PASSWORD=${password}\nPOSTGRES_DB=${database}\n`, deps);
  let started = false;
  const removeContainer = async () => requireProcessSuccess(await runProcess({
    command: "docker", args: ["rm", "--force", container], cwd: options.cwd,
    env: childEnvironment(deps.env, {}), timeout_ms: options.timeouts.startup_timeout_ms,
  }, deps), "POSTGRES_CLEANUP_FAILED", "scratch postgres removal");
  try {
    const imageID = (await commandOutput("docker", ["image", "inspect", "--format", "{{.Id}}", options.postgres_image], options.cwd, childEnvironment(deps.env, {}), options.timeouts.startup_timeout_ms, deps, "POSTGRES_IMAGE_UNAVAILABLE", "local postgres image inspection")).trim();
    const imageMatch = /^sha256:([a-f0-9]{64})$/.exec(imageID);
    if (!imageMatch) fail("POSTGRES_IMAGE_UNAVAILABLE", "local postgres image identity is invalid");
    if (imageMatch[1] !== matrix.artifact.postgres_image_sha256 || options.postgres_image !== matrix.postgres_image) {
      fail("POSTGRES_IMAGE_MISMATCH", "local postgres image differs from the frozen artifact matrix");
    }
    requireProcessSuccess(await runProcess({
      command: "docker",
      args: ["run", "--detach", "--name", container, "--label", `hap-01c-run=${options.run_id}`, "--env-file", envFile, "--volume", `${deps.path.join(postgresRoot, "data")}:/var/lib/postgresql/data`, "--publish", "127.0.0.1::5432", imageID],
      cwd: options.cwd, env: childEnvironment(deps.env, {}), timeout_ms: options.timeouts.startup_timeout_ms,
    }, deps), "POSTGRES_START_FAILED", "scratch postgres start");
    started = true;
    const portText = await commandOutput("docker", ["port", container, "5432/tcp"], options.cwd, childEnvironment(deps.env, {}), options.timeouts.startup_timeout_ms, deps, "POSTGRES_START_FAILED", "scratch postgres port discovery");
    const match = /127\.0\.0\.1:(\d+)/.exec(portText);
    if (!match) fail("POSTGRES_START_FAILED", "scratch postgres did not expose a loopback port");
    const port = Number(match[1]);
    const dsn = `postgres://hap01c:${password}@127.0.0.1:${port}/${database}?sslmode=disable`;
    const deadline = deps.now() + options.timeouts.startup_timeout_ms;
    let stableProbes = 0;
    while (deps.now() < deadline && stableProbes < 2) {
      const result = await runProcess({
        command: "docker",
        args: ["exec", container, "psql", "--username", "hap01c", "--dbname", database, "--tuples-only", "--no-align", "--command", "SELECT 1"],
        cwd: options.cwd,
        env: childEnvironment(deps.env, {}),
        timeout_ms: 5_000,
      }, deps);
      stableProbes = stablePostgresProbeCount(stableProbes, result);
      if (stableProbes < 2) await deps.sleep(stableProbes === 1 ? 1_500 : 250);
    }
    if (stableProbes < 2) fail("POSTGRES_START_TIMEOUT", "scratch postgres did not remain query-ready");
    return Object.freeze({ dsn, image_sha256: imageMatch[1], async close() { await removeContainer(); return true; } });
  } catch (error) {
    let residue = 0;
    if (started) {
      try { await removeContainer(); } catch { residue = 1; }
    }
    throw withCleanupResidue(error, residue);
  }
}

function randomLoopbackPort(deps) {
  return new Promise((resolve, reject) => {
    const server = deps.net.createServer();
    server.once("error", reject);
    server.listen({ host: "127.0.0.1", port: 0 }, () => {
      const address = server.address();
      server.close(() => {
        if (!address || typeof address === "string" || !Number.isInteger(address.port)) reject(new Error("loopback allocation failed"));
        else resolve(address.port);
      });
    });
  });
}

function drainChildOutput(child, { stdout = true, stderr = true } = {}) {
  if (stdout && typeof child?.stdout?.resume === "function") child.stdout.resume();
  if (stderr && typeof child?.stderr?.resume === "function") child.stderr.resume();
}

function scratchServerEnvironment(serverRoot, port, dsn, adminToken, deps) {
  return childEnvironment(deps.env, {
    HOME: serverRoot,
    USERPROFILE: serverRoot,
    LOCALAPPDATA: deps.path.join(serverRoot, "localappdata"),
    TEMP: deps.path.join(serverRoot, "tmp"),
    TMP: deps.path.join(serverRoot, "tmp"),
    TMPDIR: deps.path.join(serverRoot, "tmp"),
    ENGRAM_DATA_DIR: deps.path.join(serverRoot, "data"),
    ENGRAM_WORKER_HOST: "127.0.0.1",
    ENGRAM_WORKER_PORT: String(port),
    DATABASE_DSN: dsn,
    ENGRAM_AUTH_ADMIN_TOKEN: adminToken,
    ENGRAM_HAP_01B_RELAY_ENABLED: "true",
    ENGRAM_HAP_01B_RELAY_REVISION: ADAPTER_REVISION,
    ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT: "true",
    ENGRAM_V7_PLUG_ENABLED: "true",
    ENGRAM_V7_S3_AMBIENT: "true",
  });
}

async function startServer(options, matrix, dsn, root, deps) {
  if (typeof deps.startServer === "function") return deps.startServer(options, matrix, dsn, root);
  const port = await randomLoopbackPort(deps);
  const serverRoot = deps.path.join(root, "server");
  createExclusiveDirectory(serverRoot, deps);
  const adminToken = `engram_${deps.randomBytes(16).toString("hex")}`;
  const env = scratchServerEnvironment(serverRoot, port, dsn, adminToken, deps);
  const child = deps.spawn(matrix.candidate_server_path, [], { cwd: options.cwd, env, stdio: ["ignore", "pipe", "pipe"], windowsHide: true, detached: deps.platform !== "win32" });
  drainChildOutput(child);
  try {
    await waitForTcpHealth({ host: "127.0.0.1", port }, options.timeouts.startup_timeout_ms, deps);
  } catch (error) {
    const stopped = await stopOwnedChild(child, deps, options.timeouts.startup_timeout_ms);
    throw withCleanupResidue(error, stopped ? 0 : 1);
  }
  return Object.freeze({
    host: "127.0.0.1",
    port,
    async close() {
      if (!await stopOwnedChild(child, deps, options.timeouts.startup_timeout_ms)) fail("SERVER_CLEANUP_FAILED", "scratch server did not stop");
      return true;
    },
  });
}

function parseLocator(locatorPath, deps) {
  const value = parseJson(regularFileBytes(locatorPath, deps, "RELAY_LOCATOR_INVALID", 4096), "RELAY_LOCATOR_INVALID");
  requireExactKeys(value, ["protocol", "daemon_generation", "endpoint"], "relay locator", "RELAY_LOCATOR_INVALID");
  if (value.protocol !== RELAY_PROTOCOL || typeof value.daemon_generation !== "string" || !value.daemon_generation || typeof value.endpoint !== "string" || !value.endpoint) {
    fail("RELAY_LOCATOR_INVALID", "daemon relay locator is invalid");
  }
  return value;
}

async function waitForLocator(locatorPath, options, deps) {
  const deadline = deps.now() + options.timeouts.startup_timeout_ms;
  while (deps.now() < deadline) {
    if (exists(locatorPath, deps)) {
      try { return parseLocator(locatorPath, deps); } catch (error) { if (!(error instanceof ProbeError)) throw error; }
    }
    await deps.sleep(100);
  }
  fail("RELAY_LOCATOR_TIMEOUT", "candidate daemon did not publish its owned relay locator");
}

async function startDaemon(runtime, scenarioRoot, variant, deps) {
  const candidate = variant === "candidate";
  const binary = candidate ? runtime.matrix.candidate_client_path : runtime.matrix.baseline_client_path;
  const daemonRoot = deps.path.join(scenarioRoot, "daemon");
  createExclusiveDirectory(daemonRoot, deps);
  const env = scratchEnvironment(daemonRoot, runtime.options.scratch_profile, deps);
  const additions = {
    ENGRAM_URL: runtime.server_tap.url,
    ENGRAM_TOKEN: runtime.secrets.ordinary_token,
    ENGRAM_CLIENT_INSTANCE_ID: `hap01c-${sha256(`${runtime.options.run_id}:${scenarioRoot}`).slice(0, 20)}`,
  };
  if (candidate) {
    additions.ENGRAM_HAP_01B_RELAY_ENABLED = "true";
    additions.ENGRAM_HAP_01B_RELAY_REVISION = ADAPTER_REVISION;
    additions.ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT = "true";
    additions.ENGRAM_HAP_01B_ADAPTER_SHA256 = runtime.matrix.candidate.adapter_sha256;
  }
  const child = deps.spawn(binary, ["--muxcore-daemon"], { cwd: runtime.options.cwd, env: { ...env, ...additions }, stdio: ["ignore", "pipe", "pipe"], windowsHide: true, detached: deps.platform !== "win32" });
  drainChildOutput(child);
  const locatorPath = deps.path.join(env.LOCALAPPDATA, "engram", "run", "hap-01b", RELAY_LOCATOR_NAME);
  try {
    const locator = candidate ? await waitForLocator(locatorPath, runtime.options, deps) : null;
    return Object.freeze({
      child, env, locator, locator_path: locatorPath, async close() {
        if (!await stopOwnedChild(child, deps, runtime.options.timeouts.startup_timeout_ms)) fail("DAEMON_CLEANUP_FAILED", "scratch daemon did not stop");
        return true;
      }
    });
  } catch (error) {
    const stopped = await stopOwnedChild(child, deps, runtime.options.timeouts.startup_timeout_ms);
    throw withCleanupResidue(error, stopped ? 0 : 1);
  }
}

function scratchPluginConfig(runtime, mode) {
  return mode === "new"
    ? {
      server_url: runtime.server_tap.url,
      api_token: runtime.secrets.ordinary_token,
      hap_01b: {
        relay_enabled: true,
        relay_revision: ADAPTER_REVISION,
        legacy_direct_enforcement: true,
        adapter_sha256: runtime.matrix.candidate.adapter_sha256,
        registration_token: runtime.secrets.registration_token,
        project_tokens: { [runtime.seed.canonical_project_key]: runtime.secrets.project_token },
      },
    }
    : { server_url: runtime.server_tap.url, api_token: runtime.secrets.legacy_direct_token };
}

function replaceScratchPluginConfig(runtime, plugin, deps) {
  writeAtomicJson(plugin.config_path, scratchPluginConfig(runtime, plugin.mode), deps);
}

async function linkScratchPlugin(runtime, scenarioRoot, pluginRoot, mode, deps) {
  const profileRoot = deps.path.join(scenarioRoot, "omp");
  const env = scratchEnvironment(profileRoot, runtime.options.scratch_profile, deps);
  const installRoot = deps.path.join(scenarioRoot, "plugin");
  copyDirectory(pluginRoot, installRoot, deps);
  requireProcessSuccess(await runProcess({
    command: runtime.options.omp_command,
    args: ["--profile", runtime.options.scratch_profile, "plugin", "link", installRoot],
    cwd: runtime.options.cwd,
    env,
    timeout_ms: runtime.options.timeouts.startup_timeout_ms,
  }, deps), "OMP_PLUGIN_LINK_FAILED", "scratch OMP plugin link");
  const listOutput = await commandOutput(runtime.options.omp_command, ["--profile", runtime.options.scratch_profile, "plugin", "list", "--json"], runtime.options.cwd, env, runtime.options.timeouts.startup_timeout_ms, deps, "OMP_PLUGIN_LIST_INVALID", "scratch OMP plugin list");
  const expectedVersion = packagePayload(pluginRoot, deps).version;
  const expectedTree = mode === "new" ? runtime.matrix.artifact.install_tree_sha256 : runtime.matrix.artifact.baseline_plugin_install_tree_sha256;
  const installed = resolveLinkedPluginList(parseJson(Buffer.from(listOutput), "OMP_PLUGIN_LIST_INVALID"), profileRoot, expectedVersion, expectedTree, deps);
  const pluginData = resolvePluginData(installed.installPath, installed, deps.path.join(profileRoot, "home", ".omp"), deps);
  if (!isInside(profileRoot, pluginData, deps.path)) fail("PLUGIN_DATA_UNSAFE", "scratch plugin data root escapes the scenario profile");
  ensureDirectory(pluginData, deps, "PLUGIN_DATA_UNSAFE");
  const configPath = deps.path.join(pluginData, "config.json");
  writeSecretJson(configPath, scratchPluginConfig(runtime, mode), deps);
  return Object.freeze({ env, profile_root: profileRoot, plugin_root: installed.installPath, plugin_data: pluginData, config_path: configPath, mode });
}

async function runOmpTurn(runtime, scenarioRoot, plugin, directCredentials, deps) {
  const observerPath = deps.path.join(scenarioRoot, "observer.ndjson");
  const sessionDirectory = deps.path.join(scenarioRoot, "session");
  createExclusiveDirectory(sessionDirectory, deps);
  const workspace = deps.path.join(scenarioRoot, "workspace");
  createExclusiveDirectory(workspace, deps);
  writeSecretJson(deps.path.join(workspace, ".engram-project"), {
    version: 3,
    project_id: runtime.seed.anchor_project_id,
    name: "hap-01c",
    scope: "directory",
  }, deps);
  writeModelConfig(plugin.env.PI_CODING_AGENT_DIR, runtime.model.url, deps);
  const env = {
    ...plugin.env,
    GIT_CEILING_DIRECTORIES: workspace,
    HAP_01C_OBSERVER_FILE: observerPath,
    HAP_01C_OBSERVER_SCRATCH_CWD: workspace,
  };
  if (directCredentials) {
    env.ENGRAM_URL = runtime.server_tap.url;
    env.ENGRAM_TOKEN = runtime.secrets.legacy_direct_token;
  }
  const args = [
    "--profile", runtime.options.scratch_profile,
    "--session-dir", sessionDirectory,
    "--no-tools",
    "--no-lsp",
    "--no-title",
    "--model", MODEL_SELECTOR,
    "--extension", OBSERVER_FILE,
    "-p", `Reply exactly ${MODEL_SENTINEL}.`,
  ];
  const child = await runBoundedChild({ command: runtime.options.omp_command, args, cwd: workspace, env, timeout_ms: runtime.options.timeouts.turn_timeout_ms }, MODEL_SENTINEL, deps);
  const observer = observerProjection(observerPath, deps);
  const transcript = sessionTranscriptProjection(sessionDirectory, deps);
  if (!turnIsComplete(child, transcript)) {
    throw withCleanupResidue(new ProbeError("OMP_TURN_INCOMPLETE", "scratch OMP turn did not complete cleanly"), child.close_unconfirmed ? 1 : 0);
  }
  return Object.freeze({ child, observer, transcript });
}

function makeDescriptor(runtime, scenarioID) {
  return Object.freeze({
    version: 3,
    anchor_project_id: runtime.seed.anchor_project_id,
    name: "hap-01c",
    scope: "directory",
    normalized_git_remotes: [],
    legacy_identifiers: [],
    client_instance_id: `hap01c-${sha256(`${runtime.options.run_id}:${scenarioID}`).slice(0, 24)}`,
  });
}
function sessionReference(runtime, label) {
  return `hap01c-${runtime.options.run_id}-${sha256(label).slice(0, 20)}`;
}

function waitForMcpResponse(child, id, timeoutMs, deps) {
  return new Promise((resolve, reject) => {
    let buffer = Buffer.alloc(0);
    let timer;
    const finish = (value, error) => {
      deps.clearTimeout(timer);
      child.stdout?.off?.("data", onData);
      if (error) reject(error); else resolve(value);
    };
    const onData = (chunk) => {
      buffer = Buffer.concat([buffer, Buffer.from(chunk)]);
      for (; ;) {
        const index = buffer.indexOf(0x0a);
        if (index < 0) break;
        const line = buffer.subarray(0, index);
        buffer = buffer.subarray(index + 1);
        try {
          const response = JSON.parse(line.toString("utf8"));
          if (response.id === id) { finish(response); return; }
        } catch { /* unrelated daemon output cannot satisfy a response */ }
      }
    };
    child.stdout?.on?.("data", onData);
    timer = deps.setTimeout(() => finish(null, new ProbeError("MCP_SHIM_TIMEOUT", "MCP shim did not answer its bootstrap request")), timeoutMs);
  });
}

async function openMcpShim(runtime, plugin, scenarioID, deps) {
  if (typeof deps.openMcpShim === "function") return deps.openMcpShim(runtime, plugin, scenarioID);
  const env = {
    ...plugin.env,
    PLUGIN_ROOT: plugin.plugin_root,
    PLUGIN_DATA: plugin.plugin_data,
    ENGRAM_CLIENT_INSTANCE_ID: makeDescriptor(runtime, scenarioID, deps).client_instance_id,
  };
  const child = deps.spawn("node", [deps.path.join(plugin.plugin_root, "scripts", "run-engram.js")], { cwd: plugin.plugin_root, env, stdio: ["pipe", "pipe", "pipe"], windowsHide: true, detached: deps.platform !== "win32" });
  drainChildOutput(child, { stdout: false, stderr: true });
  try {
    const send = (message) => child.stdin.write(`${JSON.stringify(message)}\n`);
    const initializePending = waitForMcpResponse(child, 1, runtime.options.timeouts.startup_timeout_ms, deps);
    send({ jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2024-11-05", capabilities: {}, clientInfo: { name: "hap01c", version: "1" } } });
    const initialize = await initializePending;
    if (!isPlainObject(initialize) || isPlainObject(initialize.error)) fail("MCP_SHIM_INVALID", "MCP shim initialize failed");
    send({ jsonrpc: "2.0", method: "notifications/initialized", params: {} });
    const toolsPending = waitForMcpResponse(child, 2, runtime.options.timeouts.startup_timeout_ms, deps);
    send({ jsonrpc: "2.0", id: 2, method: "tools/list", params: {} });
    const tools = await toolsPending;
    if (!isPlainObject(tools) || isPlainObject(tools.error)) fail("MCP_SHIM_INVALID", "MCP shim tools/list failed");
    return Object.freeze({ child, async close() { try { child.stdin.end(); } catch { /* closed child */ } return stopOwnedChild(child, deps, runtime.options.timeouts.startup_timeout_ms); } });
  } catch (error) {
    try { child.stdin.end(); } catch { /* closed child */ }
    const stopped = await stopOwnedChild(child, deps, runtime.options.timeouts.startup_timeout_ms);
    throw withCleanupResidue(error, stopped ? 0 : 1);
  }
}

async function createInstalledRelay(pluginRoot, clientEnv, deps) {
  if (typeof deps.createInstalledRelay === "function") return deps.createInstalledRelay(pluginRoot, clientEnv);
  const moduleURL = pathToFileURL(deps.path.join(pluginRoot, "extensions", "legacy-relay.mjs")).href;
  const relayModule = await import(moduleURL);
  const artifactFiles = [
    { relativePath: "extensions/engram-memory.mjs", filePath: deps.path.join(pluginRoot, "extensions", "engram-memory.mjs") },
    { relativePath: "extensions/legacy-relay.mjs", filePath: deps.path.join(pluginRoot, "extensions", "legacy-relay.mjs") },
  ];
  return relayModule.createLegacyRelay({ env: clientEnv, platform: deps.platform, artifactFiles });
}

function routeToken(record) {
  return `${record.callback}:${record.route}:${record.outcome}`;
}

function custodyFromTurn(turn, authorizationSeen, childHapConfig) {
  return Object.freeze({
    extension_url_present: turn.observer.extension_url_present,
    extension_token_present: turn.observer.extension_token_present,
    extension_config_path_present: turn.observer.extension_config_path_present,
    authorization_seen: authorizationSeen,
    child_hap_config_present: childHapConfig,
  });
}

function counts(values = {}) {
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
    ...values,
  };
}

function scenarioObservation(id, { expected, actual, routes, shared_deadline, custody, subcases = [] }) {
  const mismatch = Object.entries(expected).some(([key, value]) => actual[key] !== value);
  const subcaseFailure = subcases.some((subcase) => !subcase.passed);
  const passed = !mismatch && !subcaseFailure && Boolean(shared_deadline);
  return Object.freeze({
    id,
    state: passed ? "OBSERVED" : "CONTRADICTED",
    passed,
    counts: actual,
    route_sequence: routes,
    shared_deadline: Boolean(shared_deadline),
    custody,
    subcases,
    reason_codes: passed ? [] : ["SCENARIO_OBSERVATION_MISMATCH"],
  });
}

async function openRelayScenario(runtime, scenarioID, daemonVariant, pluginVariant, relayMode, deps) {
  const scenarioRoot = deps.path.join(runtime.options.scratch_dir, "scenarios", scenarioID);
  ensureDirectory(deps.path.dirname(scenarioRoot), deps, "OWNED_DIRECTORY_INVALID");
  createExclusiveDirectory(scenarioRoot, deps);
  let daemon = null;
  let relayTap = null;
  try {
    daemon = await startDaemon(runtime, scenarioRoot, daemonVariant, deps);
    const pluginRoot = pluginVariant === "candidate" ? runtime.matrix.candidate_plugin_root : runtime.matrix.baseline_plugin_root;
    const plugin = await linkScratchPlugin(runtime, scenarioRoot, pluginRoot, pluginVariant === "candidate" ? "new" : "old", deps);
    if (daemon.locator) {
      const logicalEndpoint = deps.path.join(scenarioRoot, "taps", "relay-tap.sock");
      const clientLocator = deps.path.join(plugin.env.LOCALAPPDATA, "engram", "run", "hap-01b", RELAY_LOCATOR_NAME);
      relayTap = createRelayTap({ logical_endpoint: logicalEndpoint }, deps);
      await relayTap.start();
      relayTap.installLocator(daemon.locator, clientLocator);
      relayTap.setMode(relayMode);
      if (relayMode === "stale_generation") relayTap.armStaleGeneration(`stale-${sha256(`${runtime.options.run_id}:${scenarioID}`).slice(0, 24)}`);
    }
    return Object.freeze({
      root: scenarioRoot,
      daemon,
      plugin,
      relay_tap: relayTap,
      async close() {
        let residue = 0;
        if (relayTap) { try { await relayTap.close(); } catch { residue += 1; } }
        try { await daemon.close(); } catch { residue += 1; }
        if (!removeOwnedDirectory(scenarioRoot, runtime.options.scratch_dir, deps)) residue += 1;
        return residue;
      },
    });
  } catch (error) {
    let residue = 0;
    if (relayTap) { try { await relayTap.close(); } catch { residue += 1; } }
    if (daemon) { try { await daemon.close(); } catch { residue += 1; } }
    if (exists(scenarioRoot, deps) && !removeOwnedDirectory(scenarioRoot, runtime.options.scratch_dir, deps)) residue += 1;
    throw withCleanupResidue(error, residue);
  }
}

function telemetryDelta(before, after) {
  return Math.max(0, after.target_memory_injection_count - before.target_memory_injection_count);
}

async function executeNewRelayTurn(runtime, id, mode, deps) {
  let scenario = await openRelayScenario(runtime, id, "candidate", "candidate", mode, deps);
  try {
    const before = await snapshotFixture(runtime, `${id}-before`, deps);
    const mark = scenario.relay_tap.mark();
    const serverMark = runtime.server_tap.mark();
    const turn = await runOmpTurn(runtime, scenario.root, scenario.plugin, false, deps);
    const after = await snapshotFixture(runtime, `${id}-after`, deps);
    const tap = scenario.relay_tap.snapshot(mark);
    const server = runtime.server_tap.snapshot(serverMark);
    const cleanupResidue = await scenario.close();
    scenario = null;
    return Object.freeze({ turn, tap, server, telemetry_attempts: telemetryDelta(before, after), cleanup_residue: cleanupResidue });
  } catch (error) {
    if (scenario) await rethrowAfterCleanup(error, [scenario]);
    throw error;
  }
}

async function runHappyScenario(runtime, deps) {
  const observed = await executeNewRelayTurn(runtime, "new_new_happy", "normal", deps);
  const actual = counts({
    callbacks_expected: 2,
    callbacks_observed: observed.turn.observer.callbacks.length,
    route_attempts_expected: 4,
    route_attempts_observed: observed.tap.records.length,
    deliveries_expected: 2,
    deliveries_observed: observed.turn.transcript.custom_message_count,
    direct_fallback_attempts: observed.server.records.length,
    rediscoveries: observed.tap.rediscoveries,
    server_dispatches: observed.tap.server_dispatches,
    telemetry_attempts: observed.telemetry_attempts,
    cleanup_residue: observed.cleanup_residue,
  });
  const expected = { callbacks_observed: 2, route_attempts_observed: 4, deliveries_observed: 2, direct_fallback_attempts: 0, rediscoveries: 0, server_dispatches: 4, telemetry_attempts: 2, cleanup_residue: 0 };
  return scenarioObservation("new_new_happy", {
    expected,
    actual,
    routes: observed.tap.records.map(routeToken),
    shared_deadline: observed.tap.shared_deadline && observed.turn.observer.scratch_cwd,
    custody: custodyFromTurn(observed.turn, observed.server.authorization_seen, true),
  });
}

async function runNewPluginOldDaemonScenario(runtime, deps) {
  let scenario = await openRelayScenario(runtime, "new_plugin_old_daemon", "baseline", "candidate", "normal", deps);
  try {
    const serverMark = runtime.server_tap.mark();
    const turn = await runOmpTurn(runtime, scenario.root, scenario.plugin, false, deps);
    const server = runtime.server_tap.snapshot(serverMark);
    const cleanupResidue = await scenario.close();
    scenario = null;
    const actual = counts({ callbacks_expected: 2, callbacks_observed: turn.observer.callbacks.length, route_attempts_expected: 0, route_attempts_observed: 0, deliveries_expected: 0, deliveries_observed: turn.transcript.custom_message_count, direct_fallback_attempts: server.records.length, server_dispatches: server.records.length, cleanup_residue: cleanupResidue });
    return scenarioObservation("new_plugin_old_daemon", {
      expected: { callbacks_observed: 2, route_attempts_observed: 0, deliveries_observed: 0, direct_fallback_attempts: 0, rediscoveries: 0, server_dispatches: 0, telemetry_attempts: 0, cleanup_residue: 0 },
      actual,
      routes: [],
      shared_deadline: turn.observer.scratch_cwd,
      custody: custodyFromTurn(turn, server.authorization_seen, true),
    });
  } catch (error) {
    if (scenario) await rethrowAfterCleanup(error, [scenario]);
    throw error;
  }
}

async function runOldPluginNewDaemonScenario(runtime, deps) {
  let scenario = await openRelayScenario(runtime, "old_plugin_new_daemon", "candidate", "baseline", "normal", deps);
  try {
    const serverMark = runtime.server_tap.mark();
    const turn = await runOmpTurn(runtime, scenario.root, scenario.plugin, true, deps);
    const server = runtime.server_tap.snapshot(serverMark);
    const cleanupResidue = await scenario.close();
    scenario = null;
    const actual = counts({
      callbacks_expected: 2,
      callbacks_observed: turn.observer.callbacks.length,
      route_attempts_expected: 0,
      route_attempts_observed: 0,
      deliveries_expected: 2,
      deliveries_observed: turn.transcript.custom_message_count,
      server_dispatches: server.records.length,
      cleanup_residue: cleanupResidue,
    });
    return scenarioObservation("old_plugin_new_daemon", {
      expected: { callbacks_observed: 2, route_attempts_observed: 0, deliveries_observed: 2, cleanup_residue: 0 },
      actual,
      routes: [],
      shared_deadline: turn.observer.scratch_cwd,
      custody: custodyFromTurn(turn, server.authorization_seen, false),
    });
  } catch (error) {
    if (scenario) await rethrowAfterCleanup(error, [scenario]);
    throw error;
  }
}

async function runOutageScenario(runtime, deps) {
  const observed = await executeNewRelayTurn(runtime, "relay_outage", "outage", deps);
  const actual = counts({
    callbacks_expected: 2,
    callbacks_observed: observed.turn.observer.callbacks.length,
    route_attempts_expected: 2,
    route_attempts_observed: observed.tap.records.length,
    deliveries_expected: 0,
    deliveries_observed: observed.turn.transcript.custom_message_count,
    direct_fallback_attempts: observed.server.records.length,
    rediscoveries: observed.tap.rediscoveries,
    server_dispatches: observed.tap.server_dispatches,
    telemetry_attempts: observed.telemetry_attempts,
    cleanup_residue: observed.cleanup_residue,
  });
  return scenarioObservation("relay_outage", {
    expected: { callbacks_observed: 2, route_attempts_observed: 2, deliveries_observed: 0, direct_fallback_attempts: 0, rediscoveries: 0, server_dispatches: 0, telemetry_attempts: 0, cleanup_residue: 0 },
    actual,
    routes: observed.tap.records.map(routeToken),
    shared_deadline: observed.tap.shared_deadline && observed.turn.observer.scratch_cwd,
    custody: custodyFromTurn(observed.turn, observed.server.authorization_seen, true),
  });
}

async function runStaleScenario(runtime, deps) {
  const observed = await executeNewRelayTurn(runtime, "stale_generation", "stale_generation", deps);
  const actual = counts({
    callbacks_expected: 2,
    callbacks_observed: observed.turn.observer.callbacks.length,
    route_attempts_expected: 5,
    route_attempts_observed: observed.tap.records.length,
    deliveries_expected: 2,
    deliveries_observed: observed.turn.transcript.custom_message_count,
    direct_fallback_attempts: observed.server.records.length,
    rediscoveries: observed.tap.rediscoveries,
    server_dispatches: observed.tap.server_dispatches,
    telemetry_attempts: observed.telemetry_attempts,
    cleanup_residue: observed.cleanup_residue,
  });
  return scenarioObservation("stale_generation", {
    expected: { callbacks_observed: 2, route_attempts_observed: 5, deliveries_observed: 2, direct_fallback_attempts: 0, rediscoveries: 1, server_dispatches: 4, telemetry_attempts: 2, cleanup_residue: 0 },
    actual,
    routes: observed.tap.records.map(routeToken),
    shared_deadline: observed.tap.shared_deadline && observed.turn.observer.scratch_cwd,
    custody: custodyFromTurn(observed.turn, observed.server.authorization_seen, true),
  });
}

async function rotateProjectKeycard(runtime, deps) {
  await invokeFixture("rotate-project-keycard", [
    "--run-id", runtime.options.run_id,
    "--dsn-file", relativeFixturePath(runtime.options, runtime.dsn_file, deps),
    "--secrets-file", relativeFixturePath(runtime.options, runtime.secrets_file, deps),
  ], runtime.options, deps);
  runtime.secrets = readFixtureSecrets(runtime.secrets_file, runtime.options, deps);
}

async function prepareRelayCapability(runtime, subcase, deps) {
  let scenario = null;
  let shim = null;
  try {
    scenario = await openRelayScenario(runtime, `capability-${subcase}`, "candidate", "candidate", "normal", deps);
    shim = await openMcpShim(runtime, scenario.plugin, `capability-${subcase}`, deps);
    const relay = await createInstalledRelay(scenario.plugin.plugin_root, scenario.plugin.env, deps);
    const descriptor = makeDescriptor(runtime, `capability-${subcase}`, deps);
    const deadline = deps.now() + runtime.options.timeouts.relay_timeout_ms;
    const identity = await relay.call("IDENTITY_REGISTRATION", { hostSessionRef: sessionReference(runtime, subcase), projectIdentityV3: descriptor }, deadline);
    if (!identity || identity.kind !== "OK" || typeof identity.sessionCapability !== "string" || identity.sessionCapability.length === 0) fail("CAPABILITY_SETUP_FAILED", "candidate relay did not issue a capability");
    return Object.freeze({ scenario, shim, relay, descriptor, capability: identity.sessionCapability, deadline });
  } catch (error) {
    await rethrowAfterCleanup(error, [shim, scenario].filter(Boolean));
  }
}

async function closeRelayCapability(session) {
  if (!session) return 0;
  return closeResourceStack([session.scenario, session.shim]);
}

async function runInvalidationSubcase(runtime, id, deps) {
  if (typeof deps.runInvalidationSubcase === "function") return deps.runInvalidationSubcase(runtime, id);
  let session = null;
  let replacementShim = null;
  let recovery = null;
  let mark = null;
  let extraCleanup = 0;
  try {
    session = await prepareRelayCapability(runtime, id, deps);
    mark = session.scenario.relay_tap.mark();
    let result;
    let freshRecovery = false;
    if (id === "adapter_mismatch") {
      const moduleURL = pathToFileURL(deps.path.join(session.scenario.plugin.plugin_root, "extensions", "legacy-relay.mjs")).href;
      const relayModule = await import(moduleURL);
      const invalidRelay = relayModule.createLegacyRelay({
        env: session.scenario.plugin.env,
        platform: deps.platform,
        artifactDigest: "0".repeat(64),
      });
      result = await invalidRelay.call("SESSION_START_CONTEXT", { hostSessionRef: sessionReference(runtime, id), sessionCapability: session.capability }, session.deadline);
    } else if (id === "expiry") {
      await deps.sleep(runtime.options.timeouts.expiry_wait_ms);
      result = await session.relay.call("SESSION_START_CONTEXT", { hostSessionRef: sessionReference(runtime, id), sessionCapability: session.capability }, deps.now() + runtime.options.timeouts.relay_timeout_ms);
    } else if (id === "process_exit") {
      if (!await session.shim.close()) extraCleanup += 1;
      result = await session.relay.call("SESSION_START_CONTEXT", { hostSessionRef: sessionReference(runtime, id), sessionCapability: session.capability }, deps.now() + runtime.options.timeouts.relay_timeout_ms);
    } else if (id === "project_keycard_rotation") {
      await rotateProjectKeycard(runtime, deps);
      replaceScratchPluginConfig(runtime, session.scenario.plugin, deps);
      replacementShim = await openMcpShim(runtime, session.scenario.plugin, `${id}-replacement`, deps);
      result = await session.relay.call("SESSION_START_CONTEXT", { hostSessionRef: sessionReference(runtime, id), sessionCapability: session.capability }, deps.now() + runtime.options.timeouts.relay_timeout_ms);
      recovery = await prepareRelayCapability(runtime, `${id}-fresh`, deps);
      freshRecovery = typeof recovery.capability === "string" && recovery.capability.length > 0;
      extraCleanup += await closeRelayCapability(recovery);
      recovery = null;
    } else {
      fail("INVALIDATION_SUBCASE_INVALID", "unknown invalidation subcase");
    }
    const tap = session.scenario.relay_tap.snapshot(mark);
    const routes = tap.records.map(routeToken);
    const failedClosed = result && result.kind !== "OK";
    if (replacementShim && !await replacementShim.close()) extraCleanup += 1;
    replacementShim = null;
    extraCleanup += await closeRelayCapability(session);
    session = null;
    return Object.freeze({
      id,
      passed: Boolean(failedClosed && (id !== "project_keycard_rotation" || freshRecovery) && routes.length >= 1 && tap.server_dispatches === 0 && extraCleanup === 0),
      route_attempts: routes.length,
      routes,
      server_dispatches: tap.server_dispatches,
      proof_digest: sha256(canonicalize({ id, records: tap.records, fresh_recovery: freshRecovery })),
      cleanup_residue: extraCleanup,
    });
  } catch (error) {
    if (recovery) extraCleanup += await closeRelayCapability(recovery);
    if (replacementShim) {
      try { if (!await replacementShim.close()) extraCleanup += 1; } catch { extraCleanup += 1; }
    }
    if (session) extraCleanup += await closeRelayCapability(session);
    return Object.freeze({ id, passed: false, route_attempts: 0, routes: [], server_dispatches: 0, proof_digest: sha256(`${id}:${safeErrorCode(error)}`), cleanup_residue: extraCleanup + cleanupResidueFrom(error) });
  }
}

async function runCapabilityInvalidationScenario(runtime, deps) {
  const ids = ["adapter_mismatch", "expiry", "process_exit", "project_keycard_rotation"];
  const outcomes = [];
  for (const id of ids) outcomes.push(await runInvalidationSubcase(runtime, id, deps));
  const subcases = outcomes.map((outcome) => ({
    id: outcome.id,
    passed: outcome.passed,
    route_attempts: outcome.route_attempts,
    server_dispatches: outcome.server_dispatches,
    reason_codes: outcome.passed ? [] : ["INVALIDATION_SUBCASE_FAILED"],
  }));
  const routeSequence = outcomes.flatMap((outcome) => outcome.routes);
  const cleanupResidue = outcomes.reduce((sum, outcome) => sum + outcome.cleanup_residue, 0);
  const serverDispatches = outcomes.reduce((sum, outcome) => sum + outcome.server_dispatches, 0);
  const actual = counts({ route_attempts_expected: ids.length, route_attempts_observed: routeSequence.length, server_dispatches: serverDispatches, cleanup_residue: cleanupResidue });
  const passed = subcases.every((subcase) => subcase.passed && subcase.route_attempts >= 1 && subcase.server_dispatches === 0) && routeSequence.length === ids.length && cleanupResidue === 0;
  return Object.freeze({
    id: "capability_invalidation",
    state: passed ? "OBSERVED" : "CONTRADICTED",
    passed,
    counts: actual,
    route_sequence: routeSequence,
    shared_deadline: true,
    custody: { extension_url_present: false, extension_token_present: false, extension_config_path_present: false, authorization_seen: false, child_hap_config_present: true },
    subcases,
    reason_codes: passed ? [] : ["INVALIDATION_SUBCASE_FAILED"],
  });
}

async function runRollbackScenario(runtime, deps) {
  const outage = await executeNewRelayTurn(runtime, "rollback_future_turn_outage", "outage", deps);
  const healthy = await executeNewRelayTurn(runtime, "rollback_future_turn_healthy", "normal", deps);
  const routes = [...outage.tap.records, ...healthy.tap.records].map(routeToken);
  const actual = counts({
    callbacks_expected: 4,
    callbacks_observed: outage.turn.observer.callbacks.length + healthy.turn.observer.callbacks.length,
    route_attempts_expected: 6,
    route_attempts_observed: routes.length,
    deliveries_expected: 2,
    deliveries_observed: outage.turn.transcript.custom_message_count + healthy.turn.transcript.custom_message_count,
    direct_fallback_attempts: outage.server.records.length + healthy.server.records.length,
    rediscoveries: outage.tap.rediscoveries + healthy.tap.rediscoveries,
    server_dispatches: outage.tap.server_dispatches + healthy.tap.server_dispatches,
    telemetry_attempts: outage.telemetry_attempts + healthy.telemetry_attempts,
    cleanup_residue: outage.cleanup_residue + healthy.cleanup_residue,
  });
  return scenarioObservation("rollback_future_turn", {
    expected: { callbacks_observed: 4, route_attempts_observed: 6, deliveries_observed: 2, direct_fallback_attempts: 0, rediscoveries: 0, server_dispatches: 4, telemetry_attempts: 2, cleanup_residue: 0 },
    actual,
    routes,
    shared_deadline: outage.tap.shared_deadline && healthy.tap.shared_deadline && outage.turn.observer.scratch_cwd && healthy.turn.observer.scratch_cwd,
    custody: custodyFromTurn(healthy.turn, healthy.server.authorization_seen, true),
  });
}
async function runScenario(id, runtime, deps) {
  switch (id) {
    case "new_new_happy": return runHappyScenario(runtime, deps);
    case "new_plugin_old_daemon": return runNewPluginOldDaemonScenario(runtime, deps);
    case "old_plugin_new_daemon": return runOldPluginNewDaemonScenario(runtime, deps);
    case "relay_outage": return runOutageScenario(runtime, deps);
    case "stale_generation": return runStaleScenario(runtime, deps);
    case "capability_invalidation": return runCapabilityInvalidationScenario(runtime, deps);
    case "rollback_future_turn": return runRollbackScenario(runtime, deps);
    default: fail("SCENARIO_INVALID", "unsupported HAP-01C scenario");
  }
}

async function createRuntime(options, matrix, deps) {
  if (typeof deps.createRuntime === "function") return deps.createRuntime(options, matrix);
  const resources = [];
  try {
    const model = await createLocalModelFixture(options.scratch_dir, deps);
    resources.push(model);
    const postgres = await startPostgres(options, matrix, options.scratch_dir, deps);
    resources.push(postgres);
    const server = await startServer(options, matrix, postgres.dsn, options.scratch_dir, deps);
    resources.push(server);
    const serverTap = createServerTap({ target_host: server.host, target_port: server.port }, deps);
    const tapAddress = await serverTap.start();
    resources.push(serverTap);
    const root = fixtureRoot(options, deps);
    ensureDirectory(root, deps, "FIXTURE_PATH_INVALID");
    const dsnFile = deps.path.join(root, "dsn.txt");
    const seedRequestFile = deps.path.join(root, "seed-request.json");
    const secretsFile = deps.path.join(root, "keycards.json");
    const seed = fixtureRequest(options, deps);
    writeSecretText(dsnFile, postgres.dsn, deps);
    writeSecretJson(seedRequestFile, seed, deps);
    await invokeFixture("seed", [
      "--run-id", options.run_id,
      "--dsn-file", relativeFixturePath(options, dsnFile, deps),
      "--request-file", relativeFixturePath(options, seedRequestFile, deps),
      "--secrets-out", relativeFixturePath(options, secretsFile, deps),
    ], options, deps);
    const secrets = readFixtureSecrets(secretsFile, options, deps);
    return {
      options, matrix, deps, model, postgres, server, server_tap: tapAddress, serverTap,
      fixture_root: root, dsn_file: dsnFile, seed_request_file: seedRequestFile, secrets_file: secretsFile,
      seed, secrets,
      async close() { return closeResourceStack(resources); },
    };
  } catch (error) {
    const residue = await closeResourceStack(resources);
    throw withCleanupResidue(error, residue);
  }
}

function guarantee(state, references, reasons = []) {
  return { state, proof_refs: Array.isArray(references) ? references : [references], reason_codes: reasons };
}

function redactedScenarioObservation(scenario) {
  return {
    id: scenario.id,
    state: scenario.state,
    passed: scenario.passed,
    counts: scenario.counts,
    route_sequence: scenario.route_sequence,
    shared_deadline: scenario.shared_deadline,
    custody: scenario.custody,
    subcases: scenario.subcases.map((subcase) => ({ id: subcase.id, passed: subcase.passed, route_attempts: subcase.route_attempts, server_dispatches: subcase.server_dispatches })),
    reason_codes: scenario.reason_codes,
  };
}

function assertSafeEvidence(value, label = "evidence") {
  if (Array.isArray(value)) {
    value.forEach((item, index) => assertSafeEvidence(item, `${label}[${index}]`));
    return;
  }
  if (!isPlainObject(value)) return;
  for (const [key, child] of Object.entries(value)) {
    if (SENSITIVE_KEY.test(key) && !/(?:sha256|present|available|attempts|path$)/i.test(key)) fail("EVIDENCE_REDACTION_FAILED", `${label}.${key} would retain a prohibited raw value`);
    assertSafeEvidence(child, `${label}.${key}`);
  }
}

function evidenceReference(id, kind, value) {
  return Object.freeze({ id, class: kind, sha256: sha256(canonicalize(value)) });
}

function buildGuarantees(scenarios, evidence) {
  const byID = new Map(scenarios.map((scenario) => [scenario.id, scenario]));
  const passed = (id) => byID.get(id)?.state === "OBSERVED" && byID.get(id)?.passed === true;
  const custodyClean = ["new_new_happy", "new_plugin_old_daemon", "relay_outage", "stale_generation", "rollback_future_turn"].every((id) => {
    const custody = byID.get(id)?.custody;
    return custody && !custody.extension_url_present && !custody.extension_token_present && !custody.extension_config_path_present && custody.child_hap_config_present;
  });
  const observed = {
    relay_reachable: passed("new_new_happy"),
    adapter_attestation_bound: passed("capability_invalidation"),
    credential_custody: custodyClean && passed("old_plugin_new_daemon"),
    route_parity: passed("new_new_happy") && passed("stale_generation"),
    callback_order: scenarios.every((scenario) => scenario.counts.callbacks_expected === scenario.counts.callbacks_observed),
    callback_deadline: scenarios.every((scenario) => scenario.shared_deadline),
    capability_lifecycle: passed("capability_invalidation"),
    partial_rollout: passed("new_plugin_old_daemon") && passed("old_plugin_new_daemon"),
    outage_no_fallback: passed("relay_outage"),
    attempt_telemetry: passed("new_new_happy") && passed("stale_generation") && passed("rollback_future_turn"),
    rollback_future_turn: passed("rollback_future_turn"),
    cleanup: scenarios.every((scenario) => scenario.counts.cleanup_residue === 0),
  };
  const proofScenarios = {
    relay_reachable: ["new_new_happy"],
    adapter_attestation_bound: ["capability_invalidation"],
    credential_custody: ["new_new_happy", "old_plugin_new_daemon"],
    route_parity: ["new_new_happy", "stale_generation"],
    callback_order: ["new_new_happy"],
    callback_deadline: ["stale_generation"],
    capability_lifecycle: ["capability_invalidation"],
    partial_rollout: ["new_plugin_old_daemon", "old_plugin_new_daemon"],
    outage_no_fallback: ["relay_outage"],
    attempt_telemetry: ["new_new_happy", "stale_generation", "rollback_future_turn"],
    rollback_future_turn: ["rollback_future_turn"],
    cleanup: ["rollback_future_turn"],
  };
  return Object.fromEntries(GUARANTEES.map((key) => {
    const references = proofScenarios[key].map((id) => evidence.get(`runtime-${id}`));
    return [key, guarantee(observed[key] ? "OBSERVED" : "CONTRADICTED", references, observed[key] ? [] : ["GUARANTEE_NOT_OBSERVED"])];
  }));
}

function withProofReferences(scenarios, evidence) {
  return scenarios.map((scenario) => {
    const reference = evidence.get(`runtime-${scenario.id}`);
    const subcases = scenario.subcases.map((subcase) => ({
      ...subcase,
      proof_refs: [evidence.get(`runtime-${scenario.id}-${subcase.id}`)],
    }));
    return { ...scenario, subcases, proof_refs: [reference] };
  });
}

function buildQualificationInput(options, matrix, scenarios, before, after, cleanupResidue) {
  const observations = scenarios.map(redactedScenarioObservation);
  assertSafeEvidence(observations);
  const evidence = new Map();
  evidence.set("artifact-matrix", evidenceReference("artifact-matrix", "BUILT_ARTIFACT", matrix.artifact));
  for (const scenario of observations) {
    evidence.set(`runtime-${scenario.id}`, evidenceReference(`runtime-${scenario.id}`, "INSTALLED_RUNTIME", scenario));
    for (const subcase of scenario.subcases) evidence.set(`runtime-${scenario.id}-${subcase.id}`, evidenceReference(`runtime-${scenario.id}-${subcase.id}`, "INSTALLED_RUNTIME", subcase));
  }
  const projectedScenarios = withProofReferences(scenarios, evidence);
  const guarantees = buildGuarantees(projectedScenarios, evidence);
  return {
    run_id: options.run_id,
    generated_at: new Date().toISOString(),
    candidate: matrix.candidate,
    host: { platform: options.platform, arch: options.arch, profile: options.scratch_profile, scratch: true },
    artifact: matrix.artifact,
    scenarios: projectedScenarios,
    guarantees,
    effects: {
      active_profile_before_sha256: before.sha256,
      active_profile_after_sha256: after.sha256,
      production_mutation: false,
      real_credentials_used: false,
      cleanup_residue: cleanupResidue,
    },
    evidence_refs: [...evidence.values()].filter(Boolean).sort((left, right) => left.id.localeCompare(right.id)),
  };
}

async function runProbe(options, overrides = {}) {
  const deps = normalizeDependencies(overrides);
  const before = snapshotActiveProfile(deps);
  let runtime = null;
  let cleanupResidue = 0;
  let scratchCleaned = false;
  let after = before;
  try {
    const executionInputs = validateExecutionInputs(options, deps);
    createExclusiveDirectory(options.evidence_dir, deps);
    createExclusiveDirectory(options.scratch_dir, deps);
    const inspectionRoot = deps.path.join(options.scratch_dir, "artifact-inspection");
    createExclusiveDirectory(inspectionRoot, deps);
    const matrix = await inspectArtifactMatrix({ ...options, platform: deps.platform, arch: deps.arch }, deps, inspectionRoot);
    if (executionInputs.fixture_command_sha256 !== matrix.artifact.fixture_object_sha256 || executionInputs.omp_command_sha256 !== matrix.artifact.omp_command_sha256) {
      fail("EXECUTION_INPUT_MISMATCH", "fixture or OMP command differs from the frozen artifact matrix");
    }
    runtime = await createRuntime({ ...options, platform: deps.platform, arch: deps.arch }, matrix, deps);
    const scenarios = [];
    for (const id of SCENARIO_IDS) {
      const scenario = typeof deps.runScenario === "function" ? await deps.runScenario(id, runtime) : await runScenario(id, runtime, deps);
      scenarios.push(scenario);
    }
    try { deps.fs.rmSync(runtime.secrets_file, { force: true }); } catch { cleanupResidue += 1; }
    try { deps.fs.rmSync(runtime.dsn_file, { force: true }); } catch { cleanupResidue += 1; }
    try { deps.fs.rmSync(runtime.seed_request_file, { force: true }); } catch { cleanupResidue += 1; }
    const postgresImageSha256 = runtime.postgres.image_sha256;
    const closingRuntime = runtime;
    runtime = null;
    cleanupResidue += await closingRuntime.close();
    scratchCleaned = removeOwnedDirectory(options.scratch_dir, options.scratch_root, deps);
    if (!scratchCleaned) cleanupResidue += 1;
    after = snapshotActiveProfile(deps);
    const qualificationInput = buildQualificationInput({ ...options, platform: deps.platform, arch: deps.arch }, matrix, scenarios, before, after, cleanupResidue);
    const record = buildQualificationRecord(qualificationInput);
    const observations = scenarios.map(redactedScenarioObservation);
    const receipt = {
      schema: "hap-01c-run-receipt/1",
      run_id: options.run_id,
      artifact_matrix_sha256: matrix.matrix_sha256,
      active_profile_before_sha256: before.sha256,
      active_profile_after_sha256: after.sha256,
      active_profile_file_count: before.file_count,
      scratch_cleaned: scratchCleaned,
      cleanup_residue: cleanupResidue,
      production_mutation: false,
      real_credentials_used: false,
      observations_sha256: sha256(canonicalize(observations)),
      omp_command_sha256: executionInputs.omp_command_sha256,
      fixture_command_sha256: matrix.fixture_object_sha256,
      postgres_image_sha256: postgresImageSha256,
    };
    assertSafeEvidence(receipt);
    writeJsonExclusive(deps.path.join(options.evidence_dir, "redacted-observations.json"), observations, deps);
    writeJsonExclusive(deps.path.join(options.evidence_dir, "record.json"), record, deps);
    writeJsonExclusive(deps.path.join(options.evidence_dir, "receipt.json"), receipt, deps);
    return Object.freeze({ record, receipt, observations });
  } catch (error) {
    cleanupResidue += cleanupResidueFrom(error);
    if (runtime) {
      try { cleanupResidue += await runtime.close(); } catch { cleanupResidue += 1; }
    }
    if (exists(options.scratch_dir, deps)) scratchCleaned = removeOwnedDirectory(options.scratch_dir, options.scratch_root, deps);
    if (!scratchCleaned && exists(options.scratch_dir, deps)) cleanupResidue += 1;
    try { after = snapshotActiveProfile(deps); } catch { /* boundary receipt keeps the pre-run digest only when inspection itself failed */ }
    const failure = boundaryFailure(safeErrorCode(error), error?.message || "HAP-01C runner boundary failure");
    const receipt = {
      schema: "hap-01c-boundary-receipt/1",
      run_id: options.run_id,
      failure,
      active_profile_before_sha256: before.sha256,
      active_profile_after_sha256: after.sha256,
      scratch_cleaned: scratchCleaned,
      cleanup_residue: cleanupResidue,
    };
    assertSafeEvidence(receipt);
    if (exists(options.evidence_dir, deps)) {
      try { writeJsonExclusive(deps.path.join(options.evidence_dir, "boundary-failure.json"), receipt, deps); } catch { /* original boundary remains authoritative */ }
    }
    return Object.freeze({ failure, receipt });
  }
}

async function main(args = process.argv.slice(2), overrides = {}) {
  const deps = normalizeDependencies(overrides);
  try {
    const options = parseArgs(args, deps.cwd(), deps.path);
    const result = await runProbe(options, deps);
    if (result.failure) {
      deps.stdout(`${canonicalize(result.failure)}\n`);
      return exitFor(result.failure);
    }
    deps.stdout(`${canonicalize(result.record)}\n`);
    return exitFor(result.record);
  } catch (error) {
    const failure = boundaryFailure(safeErrorCode(error, "CLI_ORCHESTRATION_FAILURE"), error?.message || "HAP-01C orchestration failure");
    deps.stdout(`${canonicalize(failure)}\n`);
    return exitFor(failure);
  }
}

if (require.main === module) {
  main().then((code) => { process.exitCode = code; });
}

module.exports = {
  ADAPTER_REVISION,
  ARTIFACT_MANIFEST,
  CAPABILITY_TTL_MS,
  DEFAULT_TIMEOUTS,
  MODEL_SELECTOR,
  ProbeError,
  adapterDigest,
  assertSafeEvidence,
  buildQualificationInput,
  childEnvironment,
  createExclusiveDirectory,
  createRelayTap,
  createServerTap,
  drainChildOutput,
  directoryTreeDigest,
  inspectArtifactMatrix,
  main,
  observerProjection,
  scenarioObservation,
  parseArgs,
  parseFixtureSnapshot,
  readFixtureSecrets,
  resolveProfileFiles,
  runBoundedChild,
  runCapabilityInvalidationScenario,
  runInvalidationSubcase,
  runProbe,
  runScenario,
  turnIsComplete,
  sessionTranscriptProjection,
  snapshotActiveProfile,
  scratchServerEnvironment,
  writeJsonExclusive,
  stablePostgresProbeCount,
  extractArchive,
  resolveLinkedPluginList,
};
