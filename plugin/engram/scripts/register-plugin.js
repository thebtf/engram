#!/usr/bin/env node
"use strict";

const crypto = require("node:crypto");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const DEFAULT_LOCK_TIMEOUT_MS = 5_000;
const LOCK_RETRY_MS = 25;

function fail(message) {
  throw new Error(message);
}

function usage() {
  fail("usage: register-plugin.js <installed_plugins.json> <settings.json> <known_marketplaces.json> <plugin-key> <cache-path> <version> <timestamp> <install-dir>");
}

function lockTimeout() {
  const value = process.env.ENGRAM_REGISTRY_LOCK_TIMEOUT_MS;
  if (value === undefined || value === "") return DEFAULT_LOCK_TIMEOUT_MS;
  if (!/^\d+$/.test(value)) fail("ENGRAM_REGISTRY_LOCK_TIMEOUT_MS must be a non-negative integer");
  return Number(value);
}

function sleep(milliseconds) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, milliseconds);
}

const LOCK_NAME = ".engram-registry-transaction.lock";
const RECLAIM_MARKER = new RegExp(`^${LOCK_NAME.replaceAll(".", "\\.")}\\.reclaim-\\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$`);
const UUID = /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/;

function sameOwner(left, right) {
  return left.hostname === right.hostname && left.pid === right.pid && left.token === right.token;
}

function readLockOwner(directory) {
  let value;
  try {
    value = JSON.parse(fs.readFileSync(path.join(directory, "owner"), "utf8"));
  } catch {
    return null;
  }
  if (!value || Array.isArray(value) || typeof value !== "object" || Object.keys(value).sort().join("\0") !== "hostname\0pid\0token" ||
    typeof value.hostname !== "string" || value.hostname.length === 0 || !Number.isSafeInteger(value.pid) || value.pid < 1 || typeof value.token !== "string" || !UUID.test(value.token)) return null;
  return value;
}

function isDeadLocalOwner(owner) {
  if (!owner || owner.hostname !== os.hostname()) return false;
  try {
    process.kill(owner.pid, 0);
    return false;
  } catch (error) {
    return error.code === "ESRCH";
  }
}

function staleOwner(directory, expected) {
  const owner = readLockOwner(directory);
  return owner && (!expected || sameOwner(owner, expected)) && isDeadLocalOwner(owner) ? owner : null;
}

function removeDeadLock(directory, expected) {
  if (!staleOwner(directory, expected)) return false;
  try {
    fs.unlinkSync(path.join(directory, "owner"));
    fs.rmdirSync(directory);
    return true;
  } catch (error) {
    if (error.code === "ENOENT") return false;
    throw error;
  }
}

function recoverReclaimMarkers(claudeDirectory) {
  for (const name of fs.readdirSync(claudeDirectory)) {
    if (!RECLAIM_MARKER.test(name)) continue;
    const marker = path.join(claudeDirectory, name);
    if (!removeDeadLock(marker)) return false;
  }
  return true;
}

function quarantineDeadCanonical(directory) {
  const owner = staleOwner(directory);
  if (!owner) return false;
  const marker = `${directory}.reclaim-${process.pid}-${crypto.randomUUID()}`;
  try {
    fs.renameSync(directory, marker);
  } catch (error) {
    if (error.code === "ENOENT" || error.code === "EEXIST") return false;
    throw error;
  }
  return removeDeadLock(marker, owner);
}

function acquireLock(claudeDirectory) {
  fs.mkdirSync(claudeDirectory, { recursive: true, mode: 0o700 });
  const directory = path.join(claudeDirectory, LOCK_NAME);
  const deadline = Date.now() + lockTimeout();
  const identity = { hostname: os.hostname(), pid: process.pid, token: crypto.randomUUID() };
  for (; ;) {
    if (recoverReclaimMarkers(claudeDirectory)) {
      try {
        fs.mkdirSync(directory, { mode: 0o700 });
        const owner = path.join(directory, "owner");
        try {
          fs.writeFileSync(owner, `${JSON.stringify(identity)}\n`, { encoding: "utf8", flag: "wx", mode: 0o600 });
        } catch (error) {
          try { fs.rmdirSync(directory); } catch { }
          throw error;
        }
        return { directory, owner, identity };
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

function releaseLock(lock) {
  const marker = `${lock.directory}.reclaim-${process.pid}-${crypto.randomUUID()}`;
  fs.renameSync(lock.directory, marker);
  const owner = readLockOwner(marker);
  if (!owner || !sameOwner(owner, lock.identity)) {
    fail(`registry transaction lock ownership changed; retained lock: ${marker}`);
  }
  fs.unlinkSync(path.join(marker, "owner"));
  fs.rmdirSync(marker);
}

function snapshot(file) {
  let stat;
  try {
    stat = fs.lstatSync(file);
  } catch (error) {
    if (error.code === "ENOENT") return { exists: false, bytes: null };
    throw error;
  }
  if (!stat.isFile() || stat.isSymbolicLink()) fail(`${file} must be a regular file`);
  return { exists: true, bytes: fs.readFileSync(file) };
}

function sameBytes(left, right) {
  return left.length === right.length && crypto.timingSafeEqual(left, right);
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

function entry(target, original, output) {
  return {
    target,
    original,
    output,
    staged: `${target}.staged-${process.pid}-${crypto.randomUUID()}.tmp`,
    backup: `${target}.backup-${process.pid}-${crypto.randomUUID()}`,
    stagedCreated: false,
    originalMoved: false,
    installed: false,
    restored: false,
  };
}

function stage(output) {
  const descriptor = fs.openSync(output.staged, "wx", 0o600);
  output.stagedCreated = true;
  try {
    fs.writeFileSync(descriptor, output.output);
    fs.fsyncSync(descriptor);
  } finally {
    fs.closeSync(descriptor);
  }
}

function removeStage(output, failures) {
  if (!output.stagedCreated) return;
  try {
    fs.unlinkSync(output.staged);
  } catch (error) {
    if (error.code !== "ENOENT") failures.push(`remove staged ${output.staged}: ${error.message}`);
  }
}

function retainBackup(output, failures, reason) {
  failures.push(`retained backup ${output.backup}: ${reason}`);
}

function verifyOriginal(output) {
  const current = snapshot(output.target);
  if (current.exists !== output.original.exists || (current.exists && !sameBytes(current.bytes, output.original.bytes))) {
    fail(`registration conflict: ${output.target} changed before commit`);
  }
}

function moveOriginal(output) {
  if (!output.original.exists) return;
  verifyOriginal(output);
  if (fs.existsSync(output.backup)) fail(`registration conflict: backup path already exists: ${output.backup}`);
  fs.renameSync(output.target, output.backup);
  output.originalMoved = true;
  const moved = snapshot(output.backup);
  if (!moved.exists || !sameBytes(moved.bytes, output.original.bytes)) {
    fail(`registration conflict: ${output.target} changed while moving to backup`);
  }
}

function installStage(output) {
  try {
    fs.linkSync(output.staged, output.target);
  } catch (error) {
    if (error.code === "EEXIST") fail(`registration conflict: ${output.target} appeared during commit`);
    throw error;
  }
  output.installed = true;
  fs.unlinkSync(output.staged);
}

function rollback(outputs) {
  const failures = [];
  for (const output of [...outputs].reverse()) {
    if (output.installed && !output.originalMoved) {
      try {
        const current = snapshot(output.target);
        if (current.exists && sameBytes(current.bytes, output.output)) fs.unlinkSync(output.target);
        else if (current.exists) retainBackup(output, failures, `foreign replacement remains at ${output.target}`);
      } catch (error) {
        failures.push(`remove installed ${output.target}: ${error.message}`);
      }
    }
    if (output.originalMoved) {
      try {
        const current = snapshot(output.target);
        if (!current.exists) {
          fs.renameSync(output.backup, output.target);
          output.restored = true;
        } else if (!output.installed || !sameBytes(current.bytes, output.output)) {
          retainBackup(output, failures, `foreign replacement remains at ${output.target}`);
        } else {
          fs.unlinkSync(output.target);
          fs.renameSync(output.backup, output.target);
          output.restored = true;
        }
      } catch (error) {
        failures.push(`restore ${output.target}: ${error.message}`);
      }
    }
    removeStage(output, failures);
  }
  return failures;
}

function cleanupBackups(outputs) {
  const failures = [];
  for (const output of outputs) {
    if (!output.originalMoved || output.restored) continue;
    try {
      const current = snapshot(output.target);
      if (!current.exists || !sameBytes(current.bytes, output.output)) {
        retainBackup(output, failures, `committed target changed: ${output.target}`);
        continue;
      }
      const backup = snapshot(output.backup);
      if (backup.exists && sameBytes(backup.bytes, output.original.bytes)) {
        fs.unlinkSync(output.backup);
      } else if (backup.exists) {
        retainBackup(output, failures, "contents changed after commit");
      }
    } catch (error) {
      failures.push(`remove backup ${output.backup}: ${error.message}`);
    }
  }
  return failures;
}

function register(arguments_) {
  if (arguments_.length !== 8) usage();
  const [pluginsFile, settingsFile, marketplacesFile, pluginKey, cachePath, version, timestamp, installDir] = arguments_;
  const files = [pluginsFile, settingsFile, marketplacesFile];
  const defaults = ["{\"version\":2,\"plugins\":{}}", "{}", "{}"];
  const lock = acquireLock(path.dirname(settingsFile));
  let primaryError;
  try {
    const originals = files.map(snapshot);
    const [plugins, settings, marketplaces] = originals.map((original, index) => jsonObject(original.exists ? original.bytes.toString("utf8") : defaults[index], files[index]));

    nestedObject(plugins, "plugins", pluginsFile)[pluginKey] = [{
      scope: "user",
      installPath: cachePath,
      version,
      installedAt: timestamp,
      lastUpdated: timestamp,
      isLocal: true,
    }];
    nestedObject(settings, "enabledPlugins", settingsFile)[pluginKey] = true;
    const separator = installDir.includes("\\") ? "\\" : "/";
    const statuslinePath = `${installDir.replace(/[\\/]+$/, "")}${separator}hooks${separator}statusline.js`;
    settings.statusLine = { type: "command", command: `node "${statuslinePath}"`, padding: 0 };
    marketplaces.engram = {
      source: { source: "directory", path: installDir },
      installLocation: installDir,
      lastUpdated: timestamp,
    };

    const outputs = [plugins, settings, marketplaces].map((value, index) => {
      const bom = originals[index].exists && originals[index].bytes.subarray(0, 3).equals(Buffer.from([0xef, 0xbb, 0xbf])) ? "\uFEFF" : "";
      return entry(files[index], originals[index], Buffer.from(`${bom}${JSON.stringify(value, null, 2)}\n`, "utf8"));
    });
    try {
      for (const output of outputs) fs.mkdirSync(path.dirname(output.target), { recursive: true, mode: 0o700 });
      for (const output of outputs) stage(output);
      for (const output of outputs) verifyOriginal(output);
      for (const output of outputs) {
        moveOriginal(output);
        installStage(output);
      }
    } catch (error) {
      const rollbackFailures = rollback(outputs);
      if (rollbackFailures.length) error.message += `\nRollback failures: ${rollbackFailures.join("; ")}`;
      throw error;
    }

    const backupFailures = cleanupBackups(outputs);
    if (backupFailures.length) console.error(`WARNING: Retained orphan registration backups: ${backupFailures.join("; ")}`);
  } catch (error) {
    primaryError = error;
  }
  try {
    releaseLock(lock);
  } catch (error) {
    if (primaryError) primaryError.message += `\nLock cleanup failed: ${error.message}`;
    else primaryError = error;
  }
  if (primaryError) throw primaryError;
}

register(process.argv.slice(2));
