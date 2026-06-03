const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const {
  daemonVersionForPluginVersion,
  installedBinaryMatches,
} = require("./ensure-binary.js");

test("daemonVersionForPluginVersion normalizes semver for daemon --version", () => {
  assert.equal(daemonVersionForPluginVersion("6.4.7"), "v6.4.7");
  assert.equal(daemonVersionForPluginVersion("v6.4.7"), "v6.4.7");
});

test("installedBinaryMatches rejects stale binary even when marker matches", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-ensure-binary-"));
  const binaryPath = path.join(dir, process.platform === "win32" ? "engram.exe" : "engram");
  const versionFile = path.join(dir, ".version");

  fs.writeFileSync(binaryPath, "fake");
  fs.writeFileSync(versionFile, "6.4.7");

  assert.equal(
    installedBinaryMatches(binaryPath, versionFile, "6.4.7", () => "v6.4.5"),
    false
  );
});

test("installedBinaryMatches accepts matching marker and binary version", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-ensure-binary-"));
  const binaryPath = path.join(dir, process.platform === "win32" ? "engram.exe" : "engram");
  const versionFile = path.join(dir, ".version");

  fs.writeFileSync(binaryPath, "fake");
  fs.writeFileSync(versionFile, "6.4.7");

  assert.equal(
    installedBinaryMatches(binaryPath, versionFile, "6.4.7", () => "v6.4.7"),
    true
  );
});

test("installedBinaryMatches rejects marker mismatch before binary version", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-ensure-binary-"));
  const binaryPath = path.join(dir, process.platform === "win32" ? "engram.exe" : "engram");
  const versionFile = path.join(dir, ".version");

  fs.writeFileSync(binaryPath, "fake");
  fs.writeFileSync(versionFile, "6.4.5");

  assert.equal(
    installedBinaryMatches(binaryPath, versionFile, "6.4.7", () => "v6.4.7"),
    false
  );
});
