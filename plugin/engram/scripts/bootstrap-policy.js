"use strict";

const TARGET_ASSETS = Object.freeze({
  "win32-x64": "engram-windows-amd64.exe",
  "linux-x64": "engram-linux-amd64",
  "darwin-arm64": "engram-darwin-arm64",
});
const TOP_LEVEL_FIELDS = ["schema_version", "launcher_security_epoch", "package_version", "daemon_compat_epoch", "targets", "revoked_sha256", "build_contract"];
const BUILD_CONTRACT_FIELDS = ["go_version", "trimpath", "buildvcs", "client_cgo", "daemon_version_ldflag"];
const TARGET_FIELDS = ["desired", "predecessor"];
const OBJECT_FIELDS = ["version", "asset", "size", "sha256"];
const SEMVER = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(?:0|[1-9A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9A-Za-z-][0-9A-Za-z-]*))*)?$/;
const SHA256 = /^[0-9a-f]{64}$/;
const MAX_OBJECT_BYTES = 128 * 1024 * 1024;

class BootstrapError extends Error {
  constructor(message) { super(message); this.name = "BootstrapError"; }
}

function fail(message) { throw new BootstrapError(message); }

function assertNoDuplicateProperties(text) {
  let cursor = 0;
  const skip = () => { while (/\s/.test(text[cursor])) cursor += 1; };
  const string = () => {
    if (text[cursor] !== '"') fail("bootstrap policy is not valid JSON");
    const start = cursor++;
    while (cursor < text.length) {
      const character = text[cursor++];
      if (character === '"') return JSON.parse(text.slice(start, cursor));
      if (character === "\\") { if (text[cursor++] === "u") cursor += 4; }
      else if (character < " ") fail("bootstrap policy is not valid JSON");
    }
    fail("bootstrap policy is not valid JSON");
  };
  const value = () => {
    skip();
    if (text[cursor] === '"') { string(); return; }
    if (text[cursor] === "{") {
      cursor += 1; skip();
      const keys = new Set();
      if (text[cursor] === "}") { cursor += 1; return; }
      for (;;) {
        skip(); const key = string();
        if (keys.has(key)) fail("bootstrap policy has duplicate fields");
        keys.add(key); skip();
        if (text[cursor++] !== ":") fail("bootstrap policy is not valid JSON");
        value(); skip();
        if (text[cursor] === "}") { cursor += 1; return; }
        if (text[cursor++] !== ",") fail("bootstrap policy is not valid JSON");
      }
    }
    if (text[cursor] === "[") {
      cursor += 1; skip();
      if (text[cursor] === "]") { cursor += 1; return; }
      for (;;) {
        value(); skip();
        if (text[cursor] === "]") { cursor += 1; return; }
        if (text[cursor++] !== ",") fail("bootstrap policy is not valid JSON");
      }
    }
    const token = /^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/.exec(text.slice(cursor));
    if (!token) fail("bootstrap policy is not valid JSON");
    cursor += token[0].length;
  };
  value(); skip();
  if (cursor !== text.length) fail("bootstrap policy is not valid JSON");
}

function exactFields(value, fields, label) {
  if (!value || typeof value !== "object" || Array.isArray(value)) fail(`${label} must be an object`);
  const actual = Object.keys(value);
  if (actual.length !== fields.length || actual.some((field) => !fields.includes(field))) fail(`${label} has unknown or missing fields`);
}

function canonicalVersion(value, label) {
  if (typeof value !== "string" || !SEMVER.test(value)) fail(`${label} must be canonical SemVer`);
  return value;
}

function compareVersions(left, right) {
  const parse = (value) => value.split(/[+-]/, 1)[0].split(".").map(Number);
  const a = parse(left);
  const b = parse(right);
  for (let i = 0; i < 3; i += 1) if (a[i] !== b[i]) return a[i] - b[i];
  return left === right ? 0 : left.includes("-") ? -1 : 1;
}

function validateObject(target, key, label, revoked) {
  exactFields(target, OBJECT_FIELDS, label);
  const version = canonicalVersion(target.version, `${label}.version`);
  if (typeof target.asset !== "string" || target.asset !== TARGET_ASSETS[key] || /[\\/:]/.test(target.asset)) fail(`${label}.asset is not the canonical ${key} asset`);
  if (!Number.isSafeInteger(target.size) || target.size <= 0 || target.size > MAX_OBJECT_BYTES) fail(`${label}.size is unsafe`);
  if (typeof target.sha256 !== "string" || !SHA256.test(target.sha256)) fail(`${label}.sha256 is invalid`);
  if (revoked.has(target.sha256)) fail(`${label}.sha256 is revoked`);
  return Object.freeze({ version, asset: target.asset, size: target.size, sha256: target.sha256 });
}

function validatePolicy(raw, packageVersion) {
  exactFields(raw, TOP_LEVEL_FIELDS, "bootstrap policy");
  if (raw.schema_version !== 1) fail("unsupported bootstrap policy schema");
  if (raw.launcher_security_epoch !== 1) fail("unsupported launcher security epoch");
  if (raw.daemon_compat_epoch !== 1) fail("unsupported daemon compatibility epoch");
  const version = canonicalVersion(raw.package_version, "bootstrap policy package_version");
  if (packageVersion !== undefined && version !== canonicalVersion(packageVersion, "active package version")) fail("bootstrap policy package version differs from active package");
  exactFields(raw.build_contract, BUILD_CONTRACT_FIELDS, "bootstrap policy build_contract");
  if (raw.build_contract.go_version !== "1.26.6" || raw.build_contract.trimpath !== true || raw.build_contract.buildvcs !== false || raw.build_contract.client_cgo !== false || raw.build_contract.daemon_version_ldflag !== `v${version}`) fail("bootstrap policy build_contract is unsupported");
  if (!Array.isArray(raw.revoked_sha256) || raw.revoked_sha256.some((hash) => typeof hash !== "string" || !SHA256.test(hash))) fail("bootstrap policy revoked_sha256 is invalid");
  if (new Set(raw.revoked_sha256).size !== raw.revoked_sha256.length) fail("bootstrap policy revoked_sha256 has duplicates");
  exactFields(raw.targets, Object.keys(TARGET_ASSETS), "bootstrap policy targets");
  const revoked = new Set(raw.revoked_sha256);
  const targets = {};
  for (const key of Object.keys(TARGET_ASSETS)) {
    const target = raw.targets[key];
    exactFields(target, TARGET_FIELDS, `target ${key}`);
    const desired = validateObject(target.desired, key, `target ${key}.desired`, revoked);
    if (desired.version !== version) fail(`target ${key}.desired version differs from package version`);
    if (target.predecessor !== null) {
      const predecessor = validateObject(target.predecessor, key, `target ${key}.predecessor`, revoked);
      if (compareVersions(predecessor.version, desired.version) >= 0) fail(`target ${key}.predecessor is not older than desired`);
      fail("security epoch 1 must not authorize a predecessor");
    }
    targets[key] = Object.freeze({ desired, predecessor: null });
  }
  return Object.freeze({ ...raw, package_version: version, targets: Object.freeze(targets) });
}

function parsePolicy(text, packageVersion) {
  let raw;
  try { assertNoDuplicateProperties(text); raw = JSON.parse(text); } catch (error) { if (error instanceof BootstrapError) throw error; fail("bootstrap policy is not valid JSON"); }
  return validatePolicy(raw, packageVersion);
}

function createPolicy(version, targets) {
  return validatePolicy({
    schema_version: 1,
    launcher_security_epoch: 1,
    package_version: version,
    daemon_compat_epoch: 1,
    targets: Object.fromEntries(Object.entries(TARGET_ASSETS).map(([key, asset]) => [key, { desired: targets[key], predecessor: null }])),
    revoked_sha256: [],
    build_contract: { go_version: "1.26.6", trimpath: true, buildvcs: false, client_cgo: false, daemon_version_ldflag: `v${version}` },
  }, version);
}

function main(argv = process.argv.slice(2)) {
  const [command, file, version] = argv;
  if (command !== "validate" || !file || !version || argv.length !== 3) throw new Error("usage: bootstrap-policy.js validate POLICY VERSION");
  parsePolicy(require("node:fs").readFileSync(file, "utf8"), version);
}

if (require.main === module) {
  try { main(); } catch (error) { process.stderr.write(`${error.message}\n`); process.exitCode = 1; }
}

module.exports = { BootstrapError, MAX_OBJECT_BYTES, SHA256, TARGET_ASSETS, canonicalVersion, createPolicy, parsePolicy, validatePolicy };
