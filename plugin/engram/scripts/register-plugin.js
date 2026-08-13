#!/usr/bin/env node
"use strict";

const crypto = require("node:crypto");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const DEFAULT_LOCK_TIMEOUT_MS = 5_000;
const LOCK_RETRY_MS = 25;
const LOCK_NAME = ".engram-registry-transaction.lock";
const JOURNAL_NAME = ".engram-registry-transaction.recovery";
const MANIFEST_NAME = "manifest.json";
const RECEIPT_NAME = "receipt.json";
const UUID = /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/;
const DIGEST = /^[0-9a-f]{64}$/;
const RECLAIM_MARKER = new RegExp(`^${LOCK_NAME.replaceAll(".", "\\.")}\\.reclaim-\\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$`);
const CLAIMED_RECLAIM_MARKER = new RegExp(`^${LOCK_NAME.replaceAll(".", "\\.")}\\.reclaiming-([1-9]\\d*)-([0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12})-([0-9a-f]{64})-([1-9]\\d*)-([0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12})$`);
const RELEASED_MARKER = new RegExp(`^${LOCK_NAME.replaceAll(".", "\\.")}\\.released-\\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}-[0-9a-f]{64}$`);
const TERMINAL_MARKER = new RegExp(`^${JOURNAL_NAME.replaceAll(".", "\\.")}\\.terminal-\\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}-[0-9a-f]{64}-[0-9a-f]{64}$`);

function fail(message) { throw new Error(message); }
function usage() { fail("usage: register-plugin.js <installed_plugins.json> <settings.json> <known_marketplaces.json> <plugin-key> <cache-path> <version> <timestamp> <install-dir>"); }
function sleep(milliseconds) { Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, milliseconds); }
function sha256(bytes) { return crypto.createHash("sha256").update(bytes).digest("hex"); }
function sameBytes(left, right) { return left.length === right.length && crypto.timingSafeEqual(left, right); }
function sameOwner(left, right) { return left.hostname === right.hostname && left.pid === right.pid && left.token === right.token; }
function strictKeys(value, keys) { return value && !Array.isArray(value) && typeof value === "object" && Object.keys(value).sort().join("\0") === [...keys].sort().join("\0"); }
function validOwner(value) { return strictKeys(value, ["hostname", "pid", "token"]) && typeof value.hostname === "string" && value.hostname.length > 0 && Number.isSafeInteger(value.pid) && value.pid > 0 && typeof value.token === "string" && UUID.test(value.token); }
function lockTimeout() {
  const value = process.env.ENGRAM_REGISTRY_LOCK_TIMEOUT_MS;
  if (value === undefined || value === "") return DEFAULT_LOCK_TIMEOUT_MS;
  if (!/^\d+$/.test(value)) fail("ENGRAM_REGISTRY_LOCK_TIMEOUT_MS must be a non-negative integer");
  return Number(value);
}

function snapshot(file) {
  let stat;
  try { stat = fs.lstatSync(file); } catch (error) {
    if (error.code === "ENOENT") return { exists: false, bytes: null };
    throw error;
  }
  if (!stat.isFile() || stat.isSymbolicLink()) fail(`${file} must be a regular file`);
  return { exists: true, bytes: fs.readFileSync(file) };
}

function prepareParent(directory) {
  const root = path.parse(directory).root;
  const parts = path.relative(root, directory).split(path.sep).filter(Boolean);
  let current = root;
  for (const part of parts) {
    current = path.join(current, part);
    let stat;
    try {
      stat = fs.lstatSync(current);
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
      fs.mkdirSync(current, { mode: 0o700 });
      stat = fs.lstatSync(current);
    }
    if (!stat.isDirectory() || stat.isSymbolicLink()) fail(`${current} must be a non-symlink directory`);
  }
}

function readLockOwner(directory) {
  let value;
  try { value = JSON.parse(fs.readFileSync(path.join(directory, "owner"), "utf8")); } catch { return null; }
  return validOwner(value) ? value : null;
}
function isDeadLocalOwner(owner) {
  if (!owner || owner.hostname !== os.hostname()) return false;
  try { process.kill(owner.pid, 0); return false; } catch (error) { return error.code === "ESRCH"; }
}
function staleOwner(directory, expected) {
  const owner = readLockOwner(directory);
  return owner && (!expected || sameOwner(owner, expected)) && isDeadLocalOwner(owner) ? owner : null;
}
function claimedReclaimIdentity(name) {
  const match = CLAIMED_RECLAIM_MARKER.exec(name);
  if (!match) return null;
  const [pid, claimantPid] = [Number(match[1]), Number(match[4])];
  return Number.isSafeInteger(pid) && Number.isSafeInteger(claimantPid) ? { pid, token: match[2], hostnameHash: match[3], claimantPid, claimantToken: match[5] } : null;
}
function cleanupClaimedReclaimMarker(marker) {
  const identity = claimedReclaimIdentity(path.basename(marker));
  if (!identity) return false;
  let stat;
  try { stat = fs.lstatSync(marker); } catch (error) { if (error.code === "ENOENT") return true; throw error; }
  if (!stat.isDirectory() || stat.isSymbolicLink()) return false;
  let children;
  try { children = fs.readdirSync(marker); } catch { return false; }
  if (children.length === 0) {
    try { fs.rmdirSync(marker); return true; } catch (error) { if (error.code === "ENOENT") return true; throw error; }
  }
  if (children.length !== 1 || children[0] !== "owner") return false;
  const ownerFile = path.join(marker, "owner");
  let ownerStat;
  try { ownerStat = fs.lstatSync(ownerFile); } catch { return false; }
  const owner = ownerStat.isFile() && !ownerStat.isSymbolicLink() ? readLockOwner(marker) : null;
  if (!owner || owner.pid !== identity.pid || owner.token !== identity.token || sha256(Buffer.from(owner.hostname, "utf8")) !== identity.hostnameHash || !isDeadLocalOwner(owner)) return false;
  try { fs.unlinkSync(ownerFile); fs.rmdirSync(marker); return true; } catch (error) {
    if (error.code === "ENOENT") return false;
    throw error;
  }
}
function releasedIdentity(name) {
  const match = new RegExp(`^${LOCK_NAME.replaceAll(".", "\\.")}\\.released-(\\d+)-([0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12})-([0-9a-f]{64})$`).exec(name);
  return match ? { pid: Number(match[1]), token: match[2], hostnameHash: match[3] } : null;
}
function cleanupReleasedMarker(marker) {
  const identity = releasedIdentity(path.basename(marker));
  if (!identity) return false;
  let stat;
  try { stat = fs.lstatSync(marker); } catch (error) { if (error.code === "ENOENT") return true; throw error; }
  if (!stat.isDirectory() || stat.isSymbolicLink()) return false;
  const children = fs.readdirSync(marker);
  if (children.length === 0) {
    try { fs.rmdirSync(marker); return true; } catch (error) {
      if (error.code === "ENOENT") return true;
      if (error.code === "ENOTEMPTY") return false;
      throw error;
    }
  }
  if (children.length !== 1 || children[0] !== "owner") return false;
  const ownerFile = path.join(marker, "owner");
  let ownerStat;
  try { ownerStat = fs.lstatSync(ownerFile); } catch { return false; }
  const owner = ownerStat.isFile() && !ownerStat.isSymbolicLink() ? readLockOwner(marker) : null;
  if (!owner || owner.hostname !== os.hostname() || owner.pid !== identity.pid || owner.token !== identity.token || sha256(Buffer.from(owner.hostname, "utf8")) !== identity.hostnameHash) return false;
  try { fs.unlinkSync(ownerFile); } catch (error) { if (error.code === "ENOENT") return true; throw error; }
  try { fs.rmdirSync(marker); return true; } catch (error) {
    if (error.code === "ENOENT") return true;
    if (error.code === "ENOTEMPTY") return false;
    throw error;
  }
}
function claimReclaimMarker(marker, expected) {
  let stat;
  try { stat = fs.lstatSync(marker); } catch (error) { if (error.code === "ENOENT") return true; throw error; }
  if (!stat.isDirectory() || stat.isSymbolicLink()) return false;
  let children;
  try { children = fs.readdirSync(marker); } catch { return false; }
  if (children.length === 0) return false;
  if (children.length !== 1 || children[0] !== "owner") return false;
  let ownerStat;
  try { ownerStat = fs.lstatSync(path.join(marker, "owner")); } catch { return false; }
  const owner = ownerStat.isFile() && !ownerStat.isSymbolicLink() ? staleOwner(marker, expected) : null;
  if (!owner) return false;
  const claimed = path.join(path.dirname(marker), `${LOCK_NAME}.reclaiming-${owner.pid}-${owner.token}-${sha256(Buffer.from(owner.hostname, "utf8"))}-${process.pid}-${crypto.randomUUID()}`);
  try { fs.renameSync(marker, claimed); } catch (error) { if (error.code === "ENOENT" || error.code === "EEXIST") return false; throw error; }
  return cleanupClaimedReclaimMarker(claimed);
}
function recoverReclaimMarkers(claudeDirectory) {
  for (const name of fs.readdirSync(claudeDirectory)) {
    const marker = path.join(claudeDirectory, name);
    if (RECLAIM_MARKER.test(name) && !claimReclaimMarker(marker)) return false;
    if (name.startsWith(`${LOCK_NAME}.reclaiming-`) && !cleanupClaimedReclaimMarker(marker)) return false;
  }
  return true;
}
function recoverReleasedMarkers(claudeDirectory) {
  for (const name of fs.readdirSync(claudeDirectory)) if (RELEASED_MARKER.test(name)) cleanupReleasedMarker(path.join(claudeDirectory, name));
}
function quarantineDeadCanonical(directory) {
  const owner = staleOwner(directory);
  if (!owner) return false;
  const marker = `${directory}.reclaim-${process.pid}-${crypto.randomUUID()}`;
  try { fs.renameSync(directory, marker); } catch (error) {
    if (error.code === "ENOENT" || error.code === "EEXIST") return false;
    throw error;
  }
  return claimReclaimMarker(marker, owner);
}
function acquireLock(claudeDirectory) {
  fs.mkdirSync(claudeDirectory, { recursive: true, mode: 0o700 });
  const directory = path.join(claudeDirectory, LOCK_NAME);
  const deadline = Date.now() + lockTimeout();
  const identity = { hostname: os.hostname(), pid: process.pid, token: crypto.randomUUID() };
  for (; ;) {
    recoverReleasedMarkers(claudeDirectory);
    if (recoverReclaimMarkers(claudeDirectory)) {
      try {
        fs.mkdirSync(directory, { mode: 0o700 });
        const owner = path.join(directory, "owner");
        try { fs.writeFileSync(owner, `${JSON.stringify(identity)}\n`, { encoding: "utf8", flag: "wx", mode: 0o600 }); }
        catch (error) { try { fs.rmdirSync(directory); } catch { } throw error; }
        return { directory, identity };
      } catch (error) {
        if (error.code !== "EEXIST") throw error;
        quarantineDeadCanonical(directory);
      }
    }
    const remaining = deadline - Date.now();
    if (remaining <= 0) fail(`timed out waiting for registry transaction lock: ${directory}`);
    sleep(Math.min(LOCK_RETRY_MS, remaining));
  }
}
function verifyLockOwner(lock) {
  const owner = readLockOwner(lock.directory);
  if (!owner || !sameOwner(owner, lock.identity)) fail("registry transaction lock ownership changed");
}
function releaseLock(lock) {
  const reclaim = `${lock.directory}.reclaim-${lock.identity.pid}-${lock.identity.token}`;
  fs.renameSync(lock.directory, reclaim);
  const owner = readLockOwner(reclaim);
  if (!owner || !sameOwner(owner, lock.identity)) fail(`registry transaction lock ownership changed; retained lock: ${reclaim}`);
  const released = `${lock.directory}.released-${lock.identity.pid}-${lock.identity.token}-${sha256(Buffer.from(lock.identity.hostname, "utf8"))}`;
  fs.renameSync(reclaim, released);
  try { cleanupReleasedMarker(released); } catch (error) { fail(`registry transaction lock cleanup failed; retained lock: ${released}: ${error.message}`); }
}

function jsonObject(text, file) {
  const value = JSON.parse(text.startsWith("\uFEFF") ? text.slice(1) : text);
  if (value === null || Array.isArray(value) || typeof value !== "object") fail(`${file} must contain a JSON object`);
  return value;
}
function nestedObject(container, key, file) {
  if (container[key] == null) return (container[key] = {});
  if (Array.isArray(container[key]) || typeof container[key] !== "object") fail(`${file}.${key} must contain a JSON object`);
  return container[key];
}
function entry(target, original, output, identity) {
  return { target, original, output, staged: `${target}.staged-${identity.pid}-${identity.token}.tmp`, backup: `${target}.backup-${identity.pid}-${identity.token}`, stagedIdentity: null };
}
function stage(output) {
  const descriptor = fs.openSync(output.staged, "wx", 0o600);
  try {
    const stat = fs.fstatSync(descriptor);
    if (!stat.isFile()) fail(`registration conflict: ${output.staged} is not a regular file`);
    output.stagedIdentity = { dev: stat.dev, ino: stat.ino };
    fs.writeFileSync(descriptor, output.output);
    fs.fsyncSync(descriptor);
  } finally { fs.closeSync(descriptor); }
}
function removeOwnedStage(output, failures) {
  if (!output.stagedIdentity) return;
  try {
    const stat = fs.lstatSync(output.staged);
    if (stat.isFile() && !stat.isSymbolicLink() && stat.dev === output.stagedIdentity.dev && stat.ino === output.stagedIdentity.ino) fs.unlinkSync(output.staged);
    else failures.push(`retained staged ${output.staged}: ownership changed`);
  } catch (error) { if (error.code !== "ENOENT") failures.push(`remove staged ${output.staged}: ${error.message}`); }
}
function verifyOriginal(output) {
  const current = snapshot(output.target);
  if (current.exists !== output.original.exists || (current.exists && !sameBytes(current.bytes, output.original.bytes))) fail(`registration conflict: ${output.target} changed before commit`);
}
function moveOriginal(output) {
  if (!output.original.exists) return;
  verifyOriginal(output);
  if (fs.existsSync(output.backup)) fail(`registration conflict: backup path already exists: ${output.backup}`);
  fs.renameSync(output.target, output.backup);
  const moved = snapshot(output.backup);
  if (!moved.exists || !sameBytes(moved.bytes, output.original.bytes)) fail(`registration conflict: ${output.target} changed while moving to backup`);
}
function installStage(output) {
  try { fs.linkSync(output.staged, output.target); } catch (error) {
    if (error.code === "EEXIST") fail(`registration conflict: ${output.target} appeared during commit`);
    throw error;
  }
  fs.unlinkSync(output.staged);
}

function canonicalJson(value) { return Buffer.from(`${JSON.stringify(value)}\n`, "utf8"); }
function readStrictJson(file, description) {
  const bytes = snapshot(file);
  if (!bytes.exists) fail(`invalid registry recovery journal: missing ${description}`);
  let value;
  try { value = JSON.parse(bytes.bytes.toString("utf8")); } catch { fail(`invalid registry recovery journal: malformed ${description}`); }
  if (!sameBytes(bytes.bytes, canonicalJson(value))) fail(`invalid registry recovery journal: non-canonical ${description}`);
  return { value, bytes: bytes.bytes };
}
function validEntry(value, target, lock) {
  return strictKeys(value, ["backup", "original", "outputSha256", "staged", "target"]) && value.target === target &&
    value.staged === `${target}.staged-${lock.pid}-${lock.token}.tmp` && value.backup === `${target}.backup-${lock.pid}-${lock.token}` &&
    strictKeys(value.original, ["exists", "sha256"]) && typeof value.original.exists === "boolean" &&
    (value.original.exists ? DIGEST.test(value.original.sha256) : value.original.sha256 === null) && DIGEST.test(value.outputSha256);
}
function readJournalDirectory(directory, targets) {
  let stat;
  try { stat = fs.lstatSync(directory); } catch (error) { if (error.code === "ENOENT") return null; throw error; }
  if (!stat.isDirectory() || stat.isSymbolicLink()) fail("invalid registry recovery journal: journal path is not a regular directory");
  const children = fs.readdirSync(directory).sort();
  if (children.join("\0") !== [MANIFEST_NAME, ...(children.includes(RECEIPT_NAME) ? [RECEIPT_NAME] : [])].sort().join("\0")) fail("invalid registry recovery journal: unexpected directory children");
  const manifest = readStrictJson(path.join(directory, MANIFEST_NAME), "manifest");
  const value = manifest.value;
  if (!strictKeys(value, ["entries", "lock", "version"]) || value.version !== 1 || !validOwner(value.lock) || !Array.isArray(value.entries) || value.entries.length !== targets.length) fail("invalid registry recovery journal: malformed manifest");
  const seen = new Set();
  for (const item of value.entries) {
    if (!validEntry(item, item.target, value.lock) || !targets.includes(item.target) || seen.has(item.target)) fail("invalid registry recovery journal: invalid entry");
    seen.add(item.target);
  }
  if (seen.size !== targets.length) fail("invalid registry recovery journal: registry targets do not match");
  let committed = false;
  if (children.includes(RECEIPT_NAME)) {
    const receipt = readStrictJson(path.join(directory, RECEIPT_NAME), "receipt").value;
    if (!strictKeys(receipt, ["lock", "manifestSha256", "version"]) || receipt.version !== 1 || !validOwner(receipt.lock) || !sameOwner(receipt.lock, value.lock) || receipt.manifestSha256 !== sha256(manifest.bytes)) fail("invalid registry recovery journal: invalid receipt");
    committed = true;
  }
  return { directory, manifest: value, committed };
}
function readJournal(claudeDirectory, targets) { return readJournalDirectory(path.join(claudeDirectory, JOURNAL_NAME), targets); }
function validateUncommitted(journal) {
  for (const output of journal.manifest.entries) {
    const target = snapshot(output.target);
    const backup = snapshot(output.backup);
    const staged = snapshot(output.staged);
    if (staged.exists && sha256(staged.bytes) !== output.outputSha256) fail("registry recovery retained foreign staged file");
    if (output.original.exists) {
      if (backup.exists && sha256(backup.bytes) !== output.original.sha256) fail("registry recovery retained foreign backup");
      if (!backup.exists && (!target.exists || sha256(target.bytes) !== output.original.sha256)) fail("registry recovery cannot restore original");
      if (target.exists && sha256(target.bytes) !== output.original.sha256 && sha256(target.bytes) !== output.outputSha256) fail("registry recovery retained foreign target");
    } else {
      if (backup.exists) fail("registry recovery retained unexpected backup");
      if (target.exists && sha256(target.bytes) !== output.outputSha256) fail("registry recovery retained foreign target");
    }
  }
}
function restoreUncommitted(journal) {
  validateUncommitted(journal);
  for (const output of journal.manifest.entries) {
    const target = snapshot(output.target);
    const backup = snapshot(output.backup);
    if (output.original.exists && backup.exists) {
      if (target.exists && sha256(target.bytes) === output.outputSha256) fs.unlinkSync(output.target);
      if (!snapshot(output.target).exists) fs.renameSync(output.backup, output.target);
      else fs.unlinkSync(output.backup);
    } else if (!output.original.exists && target.exists) fs.unlinkSync(output.target);
  }
  for (const output of journal.manifest.entries) if (snapshot(output.staged).exists) fs.unlinkSync(output.staged);
  for (const output of journal.manifest.entries) {
    const target = snapshot(output.target);
    if (output.original.exists ? !target.exists || sha256(target.bytes) !== output.original.sha256 : target.exists) fail("registry recovery did not reach original state");
  }
}
function validateCommitted(journal) {
  for (const output of journal.manifest.entries) {
    const target = snapshot(output.target);
    const backup = snapshot(output.backup);
    const staged = snapshot(output.staged);
    if (!target.exists || sha256(target.bytes) !== output.outputSha256) fail("registry recovery retained changed committed target");
    if (staged.exists && sha256(staged.bytes) !== output.outputSha256) fail("registry recovery retained foreign staged file");
    if (backup.exists && (!output.original.exists || sha256(backup.bytes) !== output.original.sha256)) fail("registry recovery retained foreign backup");
  }
}
function finishCommitted(journal) {
  validateCommitted(journal);
  const failures = [];
  for (const output of journal.manifest.entries) for (const file of [output.backup, output.staged]) {
    try { if (snapshot(file).exists) fs.unlinkSync(file); } catch (error) { failures.push(`remove ${file}: ${error.message}`); }
  }
  if (failures.length) fail(`registry recovery cleanup failed: ${failures.join("; ")}`);
  validateCommitted(journal);
}
function terminalMarker(directory, identity, manifestDigest) {
  return `${directory}.terminal-${identity.pid}-${identity.token}-${sha256(Buffer.from(identity.hostname, "utf8"))}-${manifestDigest}`;
}
function terminalIdentity(name) {
  const match = new RegExp(`^${JOURNAL_NAME.replaceAll(".", "\\.")}\\.terminal-(\\d+)-([0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12})-([0-9a-f]{64})-([0-9a-f]{64})$`).exec(name);
  return match ? { pid: Number(match[1]), token: match[2], hostnameHash: match[3], manifestDigest: match[4] } : null;
}
function cleanupTerminalMarker(marker, targets) {
  const markerIdentity = terminalIdentity(path.basename(marker));
  if (!markerIdentity) return false;
  let stat;
  try { stat = fs.lstatSync(marker); } catch (error) { if (error.code === "ENOENT") return true; throw error; }
  if (!stat.isDirectory() || stat.isSymbolicLink()) return false;
  const children = fs.readdirSync(marker).sort();
  if (children.length === 0) { fs.rmdirSync(marker); return true; }
  if (children.join("\0") === MANIFEST_NAME) {
    let manifest;
    try { manifest = readStrictJson(path.join(marker, MANIFEST_NAME), "manifest"); } catch { return false; }
    const value = manifest.value;
    if (!strictKeys(value, ["entries", "lock", "version"]) || !validOwner(value.lock) || value.lock.pid !== markerIdentity.pid || value.lock.token !== markerIdentity.token || sha256(Buffer.from(value.lock.hostname, "utf8")) !== markerIdentity.hostnameHash || sha256(manifest.bytes) !== markerIdentity.manifestDigest) return false;
    for (const item of value.entries) if (!validEntry(item, item.target, value.lock) || !targets.includes(item.target)) return false;
    try { validateCommitted({ directory: marker, manifest: value, committed: true }); } catch { return false; }
    fs.unlinkSync(path.join(marker, MANIFEST_NAME));
    fs.rmdirSync(marker);
    return true;
  }
  if (children.join("\0") !== `${MANIFEST_NAME}\0${RECEIPT_NAME}`) return false;
  let journal;
  try { journal = readJournalDirectory(marker, targets); } catch { return false; }
  if (!journal || !journal.committed || journal.manifest.lock.pid !== markerIdentity.pid || journal.manifest.lock.token !== markerIdentity.token || sha256(Buffer.from(journal.manifest.lock.hostname, "utf8")) !== markerIdentity.hostnameHash || sha256(fs.readFileSync(path.join(marker, MANIFEST_NAME))) !== markerIdentity.manifestDigest) return false;
  try { finishCommitted(journal); } catch { return false; }
  fs.unlinkSync(path.join(marker, RECEIPT_NAME));
  fs.unlinkSync(path.join(marker, MANIFEST_NAME));
  fs.rmdirSync(marker);
  return true;
}
function cleanTerminalMarkers(claudeDirectory, targets) {
  for (const name of fs.readdirSync(claudeDirectory)) if (TERMINAL_MARKER.test(name)) cleanupTerminalMarker(path.join(claudeDirectory, name), targets);
}
function removeJournal(journal, targets) {
  const manifest = fs.readFileSync(path.join(journal.directory, MANIFEST_NAME));
  const marker = terminalMarker(journal.directory, journal.manifest.lock, sha256(manifest));
  fs.renameSync(journal.directory, marker);
  cleanupTerminalMarker(marker, targets);
}
function recoverJournal(claudeDirectory, targets) {
  cleanTerminalMarkers(claudeDirectory, targets);
  const journal = readJournal(claudeDirectory, targets);
  if (!journal) return false;
  if (journal.committed) {
    finishCommitted(journal);
    removeJournal(journal, targets);
  } else {
    restoreUncommitted(journal);
    fs.unlinkSync(path.join(journal.directory, MANIFEST_NAME));
    fs.rmdirSync(journal.directory);
  }
  return true;
}
function publishManifest(claudeDirectory, outputs, identity) {
  const directory = path.join(claudeDirectory, JOURNAL_NAME);
  if (fs.existsSync(directory)) fail(`invalid registry recovery journal: ${directory} already exists`);
  const manifest = {
    version: 1,
    lock: identity,
    entries: outputs.map((output) => ({ target: output.target, staged: output.staged, backup: output.backup, original: { exists: output.original.exists, sha256: output.original.exists ? sha256(output.original.bytes) : null }, outputSha256: sha256(output.output) })),
  };
  const pending = `${directory}.pending-${identity.pid}-${identity.token}`;
  fs.mkdirSync(pending, { mode: 0o700 });
  const manifestFile = path.join(pending, MANIFEST_NAME);
  const descriptor = fs.openSync(manifestFile, "wx", 0o600);
  try { fs.writeFileSync(descriptor, canonicalJson(manifest)); fs.fsyncSync(descriptor); } finally { fs.closeSync(descriptor); }
  try { fs.renameSync(pending, directory); } catch (error) { try { fs.unlinkSync(manifestFile); fs.rmdirSync(pending); } catch { } throw error; }
}
function publishReceipt(claudeDirectory, targets, identity) {
  const journal = readJournal(claudeDirectory, targets);
  if (!journal || !sameOwner(journal.manifest.lock, identity)) fail("registry recovery journal identity changed");
  const manifest = fs.readFileSync(path.join(journal.directory, MANIFEST_NAME));
  const receipt = canonicalJson({ version: 1, lock: identity, manifestSha256: sha256(manifest) });
  const pending = `${journal.directory}.receipt-${identity.pid}-${identity.token}.tmp`;
  const descriptor = fs.openSync(pending, "wx", 0o600);
  try { fs.writeFileSync(descriptor, receipt); fs.fsyncSync(descriptor); } finally { fs.closeSync(descriptor); }
  try { fs.renameSync(pending, path.join(journal.directory, RECEIPT_NAME)); } catch (error) { try { fs.unlinkSync(pending); } catch { } throw error; }
}

function register(arguments_) {
  if (arguments_.length !== 8) usage();
  const [pluginsFile, settingsFile, marketplacesFile, pluginKey, cachePath, version, timestamp, installDir] = arguments_;
  const files = [pluginsFile, settingsFile, marketplacesFile].map((file) => path.resolve(file));
  const claudeDirectory = path.dirname(files[1]);
  const defaults = ["{\"version\":2,\"plugins\":{}}", "{}", "{}"];
  for (const file of files) prepareParent(path.dirname(file));
  const lock = acquireLock(claudeDirectory);
  let primaryError;
  try {
    recoverJournal(claudeDirectory, files);
    const originals = files.map(snapshot);
    const [plugins, settings, marketplaces] = originals.map((original, index) => jsonObject(original.exists ? original.bytes.toString("utf8") : defaults[index], files[index]));
    nestedObject(plugins, "plugins", pluginsFile)[pluginKey] = [{ scope: "user", installPath: cachePath, version, installedAt: timestamp, lastUpdated: timestamp, isLocal: true }];
    nestedObject(settings, "enabledPlugins", settingsFile)[pluginKey] = true;
    const separator = installDir.includes("\\") ? "\\" : "/";
    const statuslinePath = `${installDir.replace(/[\\/]+$/, "")}${separator}hooks${separator}statusline.js`;
    settings.statusLine = { type: "command", command: `node "${statuslinePath}"`, padding: 0 };
    marketplaces.engram = { source: { source: "directory", path: installDir }, installLocation: installDir, lastUpdated: timestamp };
    const outputs = [plugins, settings, marketplaces].map((value, index) => {
      const bom = originals[index].exists && originals[index].bytes.subarray(0, 3).equals(Buffer.from([0xef, 0xbb, 0xbf])) ? "\uFEFF" : "";
      return entry(files[index], originals[index], Buffer.from(`${bom}${JSON.stringify(value, null, 2)}\n`, "utf8"), lock.identity);
    });
    try {
      for (const output of outputs) stage(output);
      for (const output of outputs) verifyOriginal(output);
      verifyLockOwner(lock);
      publishManifest(claudeDirectory, outputs, lock.identity);
      for (const output of outputs) { moveOriginal(output); installStage(output); }
      for (const output of outputs) {
        const current = snapshot(output.target);
        if (!current.exists || sha256(current.bytes) !== sha256(output.output)) fail(`registration conflict: ${output.target} failed byte verification`);
      }
      publishReceipt(claudeDirectory, files, lock.identity);
      try { recoverJournal(claudeDirectory, files); } catch (error) {
        if (!/registry recovery cleanup failed/.test(error.message)) throw error;
        console.error(`WARNING: Retained orphan registration backups: ${error.message}`);
      }
    } catch (error) {
      let journal;
      try { journal = readJournal(claudeDirectory, files); } catch (journalError) { error.message += `\nRecovery failed: ${journalError.message}`; throw error; }
      if (journal) {
        try { recoverJournal(claudeDirectory, files); } catch (recoveryError) { error.message += `\nRecovery failed: ${recoveryError.message}`; }
      } else {
        const failures = [];
        for (const output of outputs) removeOwnedStage(output, failures);
        if (failures.length) error.message += `\nStaging cleanup failed: ${failures.join("; ")}`;
      }
      throw error;
    }
  } catch (error) {
    primaryError = error;
  }
  try { releaseLock(lock); } catch (error) {
    if (primaryError) primaryError.message += `\nLock cleanup failed: ${error.message}`;
    else primaryError = error;
  }
  if (primaryError) throw primaryError;
}

register(process.argv.slice(2));
