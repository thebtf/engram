const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawn, spawnSync } = require("node:child_process");
const test = require("node:test");

const root = path.resolve(__dirname, "..");
const { BootstrapError, createPolicy, parsePolicy } = require("../plugin/engram/scripts/bootstrap-policy.js");

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: root, encoding: "utf8", ...options });
  assert.equal(result.status, 0, result.stderr || result.stdout);
}

function bashPath(value) { return path.relative(root, value).split(path.sep).join("/"); }
function installerPath(fakeBin) {
  return process.platform === "win32"
    ? `${bashPath(fakeBin)}:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`
    : `${bashPath(fakeBin)}:${process.env.PATH}`;
}
function shellQuote(value) { return `'${value.replaceAll("'", `'\\''`)}'`; }
function temporaryDirectory() { return fs.mkdtempSync(path.join(root, ".tmp-bootstrap-policy-")); }
function directInstallerFixture(temp) {
  const archiveRoot = path.join(temp, "archive");
  const fakeBin = path.join(temp, "bin");
  for (const directory of ["hooks", "scripts", ".claude-plugin", "extensions"]) fs.mkdirSync(path.join(archiveRoot, directory), { recursive: true });
  fs.mkdirSync(fakeBin, { recursive: true });
  fs.writeFileSync(path.join(archiveRoot, "hooks", "hook.js"), "module.exports = {};\n");
  fs.writeFileSync(path.join(archiveRoot, "hooks", "hooks.json"), "{}\n");
  fs.writeFileSync(path.join(archiveRoot, "scripts", "bootstrap-policy.js"), "module.exports = {};\n");
  fs.copyFileSync(path.join(root, "plugin", "engram", "scripts", "register-plugin.js"), path.join(archiveRoot, "scripts", "register-plugin.js"));
  fs.writeFileSync(path.join(archiveRoot, "package.json"), "{}\n");
  fs.writeFileSync(path.join(archiveRoot, "extensions", "engram-memory.mjs"), "export {};\n");
  fs.writeFileSync(path.join(archiveRoot, ".claude-plugin", "plugin.json"), "{}\n");
  fs.copyFileSync(path.join(root, "plugin", "engram", "bootstrap-targets.json"), path.join(archiveRoot, "bootstrap-targets.json"));
  const archive = path.join(temp, "release.tar.gz");
  const archived = spawnSync("tar", ["-czf", archive, "-C", archiveRoot, "."], { encoding: "utf8" });
  assert.equal(archived.status, 0, archived.stderr);
  fs.writeFileSync(path.join(fakeBin, "curl"), "#!/usr/bin/env bash\nwhile [[ $# -gt 0 ]]; do if [[ $1 == -o ]]; then cp \"$FAKE_RELEASE_ARCHIVE\" \"$2\"; exit 0; fi; shift; done\nexit 1\n", { mode: 0o755 });
  return { archive, fakeBin };
}
const registrationStaged = (file) => /(?:installed_plugins|settings|known_marketplaces)\.json\.staged-\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}\.tmp$/.test(String(file));
const registrationBackup = (file) => /(?:installed_plugins|settings|known_marketplaces)\.json\.backup-\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/.test(String(file));
const absentTargetRaceSentinel = Buffer.from("foreign absent-target registration race sentinel\n");
function renameFailurePreload(temp) {
  const preload = path.join(temp, "fail-second-registration-link.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const linkSync = fs.linkSync;
const stagedSuffix = /\\.staged-\\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}\\.tmp$/;
const registrationStaged = (file) => /(?:installed_plugins|settings|known_marketplaces)\\.json\\.staged-\\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}\\.tmp$/.test(String(file));
let registrationLinks = 0;
fs.linkSync = (from, to, ...rest) => {
  if (registrationStaged(from) && String(to) === String(from).replace(stagedSuffix, "") && ++registrationLinks === 2) {
    const error = new Error("injected second registration-stage link failure");
    error.code = "EIO";
    throw error;
  }
  return linkSync.call(fs, from, to, ...rest);
};
`);
  return preload;
}
function absentTargetRacePreload(temp) {
  const preload = path.join(temp, "fail-absent-target-registration-link.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const linkSync = fs.linkSync;
const stagedSuffix = /\\.staged-\\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}\\.tmp$/;
const registrationStaged = (file) => /(?:installed_plugins|settings|known_marketplaces)\\.json\\.staged-\\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}\\.tmp$/.test(String(file));
const sentinel = "foreign absent-target registration race sentinel\\n";
let failed = false;
fs.linkSync = (from, to, ...rest) => {
  const target = String(from).replace(stagedSuffix, "");
  if (!failed && registrationStaged(from) && String(to) === target && !fs.existsSync(to)) {
    failed = true;
    fs.writeFileSync(to, sentinel);
    const error = new Error("injected absent-target registration link race");
    error.code = "EIO";
    throw error;
  }
  return linkSync.call(fs, from, to, ...rest);
};
`);
  return preload;
}
function partialWriteFailurePreload(temp) {
  const preload = path.join(temp, "fail-second-registration-partial-write.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const openSync = fs.openSync;
const writeFileSync = fs.writeFileSync;
const registrationStaged = (file) => /(?:installed_plugins|settings|known_marketplaces)\\.json\\.staged-\\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}\\.tmp$/.test(String(file));
const registrationDescriptors = new Set();
let registrationWrites = 0;
fs.openSync = (file, flags, ...rest) => {
  const descriptor = openSync.call(fs, file, flags, ...rest);
  if (flags === "wx" && registrationStaged(file)) registrationDescriptors.add(descriptor);
  return descriptor;
};
fs.writeFileSync = (file, data, ...rest) => {
  if (registrationDescriptors.has(file) && ++registrationWrites === 2) {
    writeFileSync.call(fs, file, typeof data === "string" ? data.slice(0, 1) : data.subarray(0, 1), ...rest);
    const error = new Error("injected second registration partial-write failure");
    error.code = "EIO";
    throw error;
  }
  return writeFileSync.call(fs, file, data, ...rest);
};
`);
  return preload;
}
function backupCleanupFailurePreload(temp) {
  const preload = path.join(temp, "fail-first-registration-backup-unlink.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const unlinkSync = fs.unlinkSync;
const registrationBackup = (file) => /(?:installed_plugins|settings|known_marketplaces)\\.json\\.backup-\\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/.test(String(file));
let failed = false;
fs.unlinkSync = (file, ...rest) => {
  if (!failed && registrationBackup(file)) {
    failed = true;
    fs.appendFileSync(process.env.ENGRAM_TEST_BACKUP_UNLINK_LOG, \`\${file}\\n\`);
    const error = new Error("injected first registration backup unlink failure");
    error.code = "EIO";
    throw error;
  }
  return unlinkSync.call(fs, file, ...rest);
};
`);
  return preload;
}
function registryContentionPreload(temp) {
  const preload = path.join(temp, "observe-registry-lock-contention.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const mkdirSync = fs.mkdirSync;
const writeFileSync = fs.writeFileSync;
const existsSync = fs.existsSync;
const lock = path.resolve(process.env.ENGRAM_TEST_REGISTRY_LOCK);
const owner = path.join(lock, "owner");
let contended = false;
fs.mkdirSync = (file, ...rest) => {
  try {
    return mkdirSync.call(fs, file, ...rest);
  } catch (error) {
    if (!contended && process.env.ENGRAM_TEST_REGISTRY_ROLE === "waiter" && error.code === "EEXIST" && path.resolve(String(file)) === lock) {
      contended = true;
      writeFileSync.call(fs, process.env.ENGRAM_TEST_REGISTRY_CONTENDED, "EEXIST\\n");
    }
    throw error;
  }
};
fs.writeFileSync = (file, data, ...rest) => {
  const result = writeFileSync.call(fs, file, data, ...rest);
  if (process.env.ENGRAM_TEST_REGISTRY_ROLE === "holder" && path.resolve(String(file)) === owner) {
    writeFileSync.call(fs, process.env.ENGRAM_TEST_REGISTRY_ACQUIRED, "owner\\n");
    while (!existsSync.call(fs, process.env.ENGRAM_TEST_REGISTRY_RELEASE)) Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10);
  }
  return result;
};
process.on("exit", () => writeFileSync.call(fs, process.env.ENGRAM_TEST_REGISTRY_COMPLETED, "done\\n"));
`);
  return preload;
}
function registrationArtifacts(home) {
  const claude = path.join(home, ".claude");
  const directories = [path.join(claude, "plugins"), claude];
  return directories.flatMap((directory) => fs.existsSync(directory)
    ? fs.readdirSync(directory).filter((name) => (registrationStaged(name) || registrationBackup(name)) && fs.lstatSync(path.join(directory, name)).isFile()).map((name) => path.join(directory, name))
    : []);
}
const registryHelper = path.join(root, "plugin", "engram", "scripts", "register-plugin.js");
function registryArguments(home) {
  const claude = path.join(home, ".claude");
  return [
    path.join(claude, "plugins", "installed_plugins.json"),
    path.join(claude, "settings.json"),
    path.join(claude, "plugins", "known_marketplaces.json"),
    "engram@engram",
    path.join(claude, "plugins", "cache", "engram", "engram", "v6.47.5"),
    "6.47.5",
    "2026-08-13T00:00:00.000Z",
    path.join(claude, "plugins", "marketplaces", "engram"),
  ];
}

function registryDurabilityPreload(temp) {
  const preload = path.join(temp, "record-registry-durability.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const openSync = fs.openSync;
const fsyncSync = fs.fsyncSync;
const renameSync = fs.renameSync;
const linkSync = fs.linkSync;
const unlinkSync = fs.unlinkSync;
const rmdirSync = fs.rmdirSync;
const closeSync = fs.closeSync;
const writeFileSync = fs.writeFileSync;
const descriptors = new Map();
const events = [];
const resolve = (file) => path.resolve(String(file));
const record = (event) => events.push(event);
fs.openSync = (file, flags, ...rest) => {
  const descriptor = openSync.call(fs, file, flags, ...rest);
  if (fs.fstatSync(descriptor).isDirectory()) descriptors.set(descriptor, resolve(file));
  return descriptor;
};
fs.fsyncSync = (descriptor, ...rest) => {
  const result = fsyncSync.call(fs, descriptor, ...rest);
  if (descriptors.has(descriptor)) record({ kind: "sync", path: descriptors.get(descriptor) });
  return result;
};
fs.closeSync = (descriptor, ...rest) => {
  try { return closeSync.call(fs, descriptor, ...rest); } finally { descriptors.delete(descriptor); }
};

fs.renameSync = (from, to, ...rest) => {
  const result = renameSync.call(fs, from, to, ...rest);
  record({ kind: "rename", from: resolve(from), to: resolve(to) });
  return result;
};
fs.linkSync = (from, to, ...rest) => {
  const result = linkSync.call(fs, from, to, ...rest);
  record({ kind: "link", from: resolve(from), to: resolve(to) });
  return result;
};
fs.unlinkSync = (file, ...rest) => {
  const result = unlinkSync.call(fs, file, ...rest);
  record({ kind: "unlink", path: resolve(file) });
  return result;
};
fs.rmdirSync = (directory, ...rest) => {
  const result = rmdirSync.call(fs, directory, ...rest);
  record({ kind: "rmdir", path: resolve(directory) });
  return result;
};

process.on("exit", () => writeFileSync.call(fs, process.env.ENGRAM_TEST_REGISTRY_DURABILITY_LOG, JSON.stringify(events)));
`);
  return preload;
}

function registryDescriptorFailurePreload(temp) {
  const preload = path.join(temp, "fail-registry-descriptor.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const openSync = fs.openSync;
const fsyncSync = fs.fsyncSync;
const closeSync = fs.closeSync;
const renameSync = fs.renameSync;
const unlinkSync = fs.unlinkSync;
const rmdirSync = fs.rmdirSync;
const writeFileSync = fs.writeFileSync;
const descriptors = new Map();
const descriptorFiles = new Map();
const directories = new Map();
const events = [];
let bodyFailed = false;
let cleanupFailed = false;
let replaced = false;
const resolve = (file) => path.resolve(String(file));
const phaseFor = (file) => {
  const name = path.basename(String(file));
  const parent = path.basename(path.dirname(String(file)));
  if (/(?:installed_plugins|settings|known_marketplaces)\\.json\\.staged-\\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}\\.tmp$/.test(name)) return "staged";
  if (name === "manifest.json" && parent.startsWith(".engram-registry-transaction.recovery.pending-")) return "manifest";
  if (name.startsWith(".engram-registry-transaction.recovery.receipt-") && name.endsWith(".tmp")) return "receipt";
  return null;
};
const cleanupPhase = (file) => {
  const name = path.basename(String(file));
  if (name.startsWith(".engram-registry-transaction.recovery.pending-")) return "manifest";
  if (name.startsWith(".engram-registry-transaction.recovery.receipt-") && name.endsWith(".tmp")) return "receipt";
  return null;
};
const record = (event) => events.push(event);
const recordCleanup = (phase, claimed) => {
  if (process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_CLEANUP_LOG) writeFileSync.call(fs, process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_CLEANUP_LOG, phase + "\\t" + claimed + "\\n", { flag: "a" });
};
const foreign = (file, phase) => {
  if (phase === "manifest") {
    fs.mkdirSync(file, { mode: 0o700 });
    writeFileSync.call(fs, path.join(file, "foreign"), "foreign descriptor cleanup race sentinel\\n");
  } else writeFileSync.call(fs, file, "foreign descriptor cleanup race sentinel\\n");
};
fs.openSync = (file, flags, ...rest) => {
  const descriptor = openSync.call(fs, file, flags, ...rest);
  if (fs.fstatSync(descriptor).isDirectory()) directories.set(descriptor, resolve(file));
  const phase = flags === "wx" && phaseFor(file);
  if (phase) { descriptors.set(descriptor, phase); descriptorFiles.set(descriptor, String(file)); }
  return descriptor;
};
fs.fsyncSync = (descriptor, ...rest) => {
  const phase = descriptors.get(descriptor);
  if (phase === process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_PHASE && process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_BODY_FAILURE === "1" && !bodyFailed) {
    bodyFailed = true;
    const error = new Error(\`injected \${phase} body persistence failure\`);
    error.code = "EIO";
    throw error;
  }
  const result = fsyncSync.call(fs, descriptor, ...rest);
  if (directories.has(descriptor)) record({ kind: "sync", path: directories.get(descriptor) });
  return result;
};
fs.closeSync = (descriptor, ...rest) => {
  const phase = descriptors.get(descriptor);
  try { return closeSync.call(fs, descriptor, ...rest); } finally {
    descriptors.delete(descriptor);
    descriptorFiles.delete(descriptor);
    directories.delete(descriptor);
    if (phase === process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_PHASE && process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_CLOSE_FAILURE === "1") {
      fs.appendFileSync(process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_LOG, \`close:\${phase}\\n\`);
      const error = new Error(\`injected \${phase} close failure\`);
      error.code = "EIO";
      throw error;
    }
  }
};
fs.renameSync = (from, to, ...rest) => {
  const phase = cleanupPhase(from);
  if (phase === process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_PHASE && process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_REPLACE_BEFORE_CLAIM === "1" && !replaced) {
    replaced = true;
    renameSync.call(fs, from, String(from) + ".owned-before");
    foreign(from, phase);
  }
  const result = renameSync.call(fs, from, to, ...rest);
  if (phase) {
    record({ kind: "rename", from: resolve(from), to: resolve(to) });
    recordCleanup(phase, resolve(to));
    if (phase === process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_PHASE && process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_REPLACE_AFTER_CLAIM === "1" && !replaced) {
      replaced = true;
      renameSync.call(fs, to, String(to) + ".owned-after");
      foreign(to, phase);
    }
  }
  return result;
};
fs.unlinkSync = (file, ...rest) => {
  const phase = process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_PHASE;
  if (String(file).includes(".engram-registry-cleanup-") && process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_CLEANUP_FAILURE === "1" && !cleanupFailed) {
    cleanupFailed = true;
    fs.appendFileSync(process.env.ENGRAM_TEST_REGISTRY_DESCRIPTOR_LOG, \`cleanup:\${phase}\\n\`);
    const error = new Error(\`injected \${phase} cleanup failure\`);
    error.code = "EIO";
    throw error;
  }
  const result = unlinkSync.call(fs, file, ...rest);
  record({ kind: "unlink", path: resolve(file) });
  return result;
};
fs.rmdirSync = (directory, ...rest) => {
  const result = rmdirSync.call(fs, directory, ...rest);
  record({ kind: "rmdir", path: resolve(directory) });
  return result;
};
process.on("exit", () => {
  if (process.env.ENGRAM_TEST_REGISTRY_DURABILITY_LOG) writeFileSync.call(fs, process.env.ENGRAM_TEST_REGISTRY_DURABILITY_LOG, JSON.stringify(events));
});
`);
  return preload;
}
function waitForFile(file, timeoutMs = 1_000) {
  const deadline = Date.now() + timeoutMs;
  while (!fs.existsSync(file)) {
    if (Date.now() >= deadline) throw new Error(`timed out waiting for ${file}`);
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10);
  }
}

function crashAfterRegistrationBackupPreload(temp, backupNumber) {
  const preload = path.join(temp, `crash-after-registration-backup-${backupNumber}.cjs`);
  fs.writeFileSync(preload, `const fs = require("node:fs");
const renameSync = fs.renameSync;
const backup = /(?:installed_plugins|settings|known_marketplaces)\\.json\\.backup-\\d+-[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/;
let moved = 0;
fs.renameSync = (from, to, ...rest) => {
  const result = renameSync.call(fs, from, to, ...rest);
  if (backup.test(String(to)) && ++moved === ${backupNumber}) process.abort();
  return result;
};
`);
  return preload;
}

function terminalJournalCrashPreload(temp, point) {
  const preload = path.join(temp, `crash-terminal-journal-${point}.cjs`);
  fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const renameSync = fs.renameSync;
const unlinkSync = fs.unlinkSync;
fs.renameSync = (from, to, ...rest) => {
  const result = renameSync.call(fs, from, to, ...rest);
  if (("${point}" === "receipt" && path.basename(String(to)) === "receipt.json") || ("${point}" === "terminal" && String(to).includes(".engram-registry-transaction.recovery.terminal-"))) process.abort();
  return result;
};
fs.unlinkSync = (file, ...rest) => {
  const result = unlinkSync.call(fs, file, ...rest);
  if (("${point}" === "terminal-receipt" && path.basename(String(file)) === "receipt.json" && path.basename(path.dirname(String(file))).includes(".terminal-")) || ("${point}" === "terminal-manifest" && path.basename(String(file)) === "manifest.json" && path.basename(path.dirname(String(file))).includes(".terminal-"))) process.abort();
  return result;
};
`);
  return preload;
}

function releasedMarkerCrashPreload(temp, point) {
  const preload = path.join(temp, `crash-released-marker-${point}.cjs`);
  fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const renameSync = fs.renameSync;
const unlinkSync = fs.unlinkSync;
fs.renameSync = (from, to, ...rest) => {
  const result = renameSync.call(fs, from, to, ...rest);
  if ("${point}" === "released" && String(from).includes(".reclaim-") && String(to).includes(".released-")) process.abort();
  return result;
};
fs.unlinkSync = (file, ...rest) => {
  const result = unlinkSync.call(fs, file, ...rest);
  if ("${point}" === "owner" && path.basename(String(file)) === "owner" && path.basename(path.dirname(String(file))).includes(".released-")) process.abort();
  return result;
};
`);
  return preload;
}
function releasedMarkerDoubleCleanupPreload(temp) {
  const preload = path.join(temp, "released-marker-double-cleanup.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const unlinkSync = fs.unlinkSync;
const rmdirSync = fs.rmdirSync;
let cleaned = false;
fs.unlinkSync = (file, ...rest) => {
  if (!cleaned && path.basename(String(file)) === "owner" && path.basename(path.dirname(String(file))).includes(".released-")) {
    cleaned = true;
    unlinkSync.call(fs, file, ...rest);
    rmdirSync.call(fs, path.dirname(String(file)));
  }
  return unlinkSync.call(fs, file, ...rest);
};
`);
  return preload;
}
function releasedMarkerLstatToReaddirRemovalPreload(temp) {
  const preload = path.join(temp, "released-marker-lstat-to-readdir-removal.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const lstatSync = fs.lstatSync;
const rmSync = fs.rmSync;
let removed = false;
fs.lstatSync = (file, ...rest) => {
  const result = lstatSync.call(fs, file, ...rest);
  if (!removed && path.basename(String(file)).includes(".released-")) {
    removed = true;
    rmSync.call(fs, file, { recursive: true, force: true });
  }
  return result;
};
`);
  return preload;
}

function reclaimMarkerOwnerUnlinkCrashPreload(temp) {
  const preload = path.join(temp, "crash-reclaim-marker-owner-unlink.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const unlinkSync = fs.unlinkSync;
const lock = path.resolve(process.env.ENGRAM_TEST_REGISTRY_LOCK);
const reclaimingPrefix = lock + ".reclaiming-";
fs.unlinkSync = (file, ...rest) => {
  const result = unlinkSync.call(fs, file, ...rest);
  if (path.basename(String(file)) === "owner" && path.resolve(path.dirname(String(file))).startsWith(reclaimingPrefix)) process.abort();
  return result;
};
`);
  return preload;
}
function reclaimMarkerRenameCrashPreload(temp) {
  const preload = path.join(temp, "crash-reclaim-marker-rename.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const renameSync = fs.renameSync;
const lock = path.resolve(process.env.ENGRAM_TEST_REGISTRY_LOCK);
fs.renameSync = (from, to, ...rest) => {
  const result = renameSync.call(fs, from, to, ...rest);
  if (path.resolve(String(from)).startsWith(lock + ".reclaim-") && path.resolve(String(to)).startsWith(lock + ".reclaiming-")) process.abort();
  return result;
};
`);
  return preload;
}
function reclaimMarkerReplacementPreload(temp) {
  const preload = path.join(temp, "replace-post-validation-reclaim-marker.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const renameSync = fs.renameSync;
const lock = path.resolve(process.env.ENGRAM_TEST_REGISTRY_LOCK);
let replaced = false;
fs.renameSync = (from, to, ...rest) => {
  const raw = path.resolve(String(from));
  if (!replaced && raw.startsWith(lock + ".reclaim-") && path.resolve(String(to)).startsWith(lock + ".reclaiming-")) {
    replaced = true;
    renameSync.call(fs, raw, raw + ".validated");
    fs.mkdirSync(raw);
    fs.writeFileSync(path.join(raw, "owner"), process.env.ENGRAM_TEST_REPLACEMENT);
  }
  return renameSync.call(fs, from, to, ...rest);
};
`);
  return preload;
}
function publicationMarkerOwnerReplacementPreload(temp) {
  const preload = path.join(temp, "replace-publication-marker-owner.cjs");
  fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const renameSync = fs.renameSync;
const lock = path.resolve(process.env.ENGRAM_TEST_REGISTRY_LOCK);
let replaced = false;
fs.renameSync = (from, to, ...rest) => {
  const result = renameSync.call(fs, from, to, ...rest);
  if (!replaced && path.resolve(String(from)) === lock && path.resolve(String(to)).startsWith(lock + ".publication-reclaim-")) {
    replaced = true;
    fs.writeFileSync(path.join(to, "owner"), process.env.ENGRAM_TEST_REPLACEMENT);
  }
  return result;
};
`);
  return preload;
}

function seededRegistries(home) {
  const claude = path.join(home, ".claude");
  const registries = path.join(claude, "plugins");
  fs.mkdirSync(registries, { recursive: true });
  const files = [
    path.join(registries, "installed_plugins.json"),
    path.join(claude, "settings.json"),
    path.join(registries, "known_marketplaces.json"),
  ];
  const values = ["{\"plugins\":{},\"keep\":\"plugins\"}\n", "{\"keep\":\"settings\"}\n", "{\"keep\":\"marketplaces\"}\n"];
  files.forEach((file, index) => fs.writeFileSync(file, values[index]));
  return { claude, files, before: files.map((file) => fs.readFileSync(file)) };
}

function registryJournal(home) { return path.join(home, ".claude", ".engram-registry-transaction.recovery"); }

test("registry transaction rejects a symlinked registry parent before outside writes", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const claude = path.join(home, ".claude");
    const outside = path.join(temp, "outside");
    fs.mkdirSync(outside, { recursive: true });
    fs.mkdirSync(claude, { recursive: true });
    try { fs.symlinkSync(outside, path.join(claude, "plugins"), "junction"); } catch { fs.symlinkSync(outside, path.join(claude, "plugins"), "dir"); }
    const result = runRegistry(home);
    assert.notEqual(result.status, 0);
    assert.deepEqual(fs.readdirSync(outside), []);
    assert.equal(fs.existsSync(path.join(claude, "settings.json")), false);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});
test("registry transaction serializes concurrent registration without losing either update", async () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const claude = path.join(home, ".claude");
    const lockDirectory = path.join(claude, ".engram-registry-transaction.lock");
    const acquired = path.join(temp, "registry-lock-acquired");
    const contended = path.join(temp, "registry-lock-contended");
    const release = path.join(temp, "registry-lock-release");
    const completed = {
      holder: path.join(temp, "registry-holder-completed"),
      waiter: path.join(temp, "registry-waiter-completed"),
    };
    const preload = registryContentionPreload(temp);
    const contentionTimeoutMs = process.platform === "win32" ? 3_000 : 1_000;

    const argumentsFor = (key, version) => {
      const arguments_ = registryArguments(home);
      arguments_[3] = key;
      arguments_[4] = path.join(claude, "plugins", "cache", "engram", "engram", `v${version}`);
      arguments_[5] = version;
      arguments_[6] = `2026-08-13T00:00:0${version.endsWith("1") ? "1" : "2"}.000Z`;
      arguments_[7] = path.join(claude, "plugins", "marketplaces", `engram-${version}`);
      return arguments_;
    };
    const start = (arguments_, role) => {
      const result = new Promise((resolve, reject) => {
        const child = spawn(process.execPath, [registryHelper, ...arguments_], {
          cwd: root,
          env: {
            ...process.env,
            NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" "),
            ENGRAM_REGISTRY_LOCK_TIMEOUT_MS: String(contentionTimeoutMs),
            ENGRAM_TEST_REGISTRY_ROLE: role,
            ENGRAM_TEST_REGISTRY_LOCK: lockDirectory,
            ENGRAM_TEST_REGISTRY_ACQUIRED: acquired,
            ENGRAM_TEST_REGISTRY_CONTENDED: contended,
            ENGRAM_TEST_REGISTRY_RELEASE: release,
            ENGRAM_TEST_REGISTRY_COMPLETED: completed[role],
          },
        });
        let stdout = "";
        let stderr = "";
        child.stdout.on("data", (chunk) => { stdout += chunk; });
        child.stderr.on("data", (chunk) => { stderr += chunk; });
        child.once("error", reject);
        child.once("close", (status) => resolve({ status, stdout, stderr }));
      });
      return { result };
    };

    const first = start(argumentsFor("first@engram", "6.47.1"), "holder");
    waitForFile(acquired, contentionTimeoutMs);
    assert.equal(fs.existsSync(lockDirectory), true, "holder must own the registry lock");
    assert.deepEqual(Object.keys(JSON.parse(fs.readFileSync(path.join(lockDirectory, "owner"), "utf8"))).sort(), ["hostname", "incarnation", "pid", "token"]);
    assert.equal(fs.existsSync(path.join(lockDirectory, "owner")), true, "holder must create the registry lock owner file");
    const second = start(argumentsFor("second@engram", "6.47.2"), "waiter");
    waitForFile(contended, contentionTimeoutMs);
    assert.equal(fs.existsSync(completed.holder), false, "holder must remain blocked before release");
    assert.equal(fs.existsSync(completed.waiter), false, "waiter must remain blocked before release");
    fs.writeFileSync(release, "release\n");

    for (const { status, stderr, stdout } of await Promise.all([first.result, second.result])) {
      assert.equal(status, 0, stderr || stdout);
    }
    const plugins = JSON.parse(fs.readFileSync(path.join(claude, "plugins", "installed_plugins.json"), "utf8"));
    const settings = JSON.parse(fs.readFileSync(path.join(claude, "settings.json"), "utf8"));
    assert.equal(plugins.plugins["first@engram"][0].version, "6.47.1");
    assert.equal(plugins.plugins["second@engram"][0].version, "6.47.2");
    assert.equal(settings.enabledPlugins["first@engram"], true);
    assert.equal(settings.enabledPlugins["second@engram"], true);
    assert.equal(fs.existsSync(lockDirectory), false);
    assert.deepEqual(registrationArtifacts(home), []);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

const registryLockName = ".engram-registry-transaction.lock";
function registryLock(home) { return path.join(home, ".claude", registryLockName); }
function currentProcessIncarnation() {
  if (process.platform === "linux") {
    const stat = fs.readFileSync(`/proc/${process.pid}/stat`, "utf8");
    const close = stat.lastIndexOf(")");
    const startTime = close < 0 ? "" : stat.slice(close + 2).trim().split(/\s+/)[19];
    assert.match(startTime, /^\d+$/);
    return `linux:${startTime}`;
  }
  const command = process.platform === "darwin"
    ? ["/usr/sbin/sysctl", ["-n", `kern.proc.pid.${process.pid}`]]
    : process.platform === "win32"
      ? ["powershell.exe", ["-NoLogo", "-NoProfile", "-NonInteractive", "-Command", `$ErrorActionPreference = 'Stop'; [Console]::Out.Write((Get-Process -Id ${process.pid}).StartTime.ToUniversalTime().Ticks)`]]
      : null;
  assert.ok(command, `unsupported process-incarnation platform: ${process.platform}`);
  const result = spawnSync(command[0], command[1], { encoding: "utf8", windowsHide: true });
  assert.equal(result.status, 0, result.stderr || result.error?.message);
  if (process.platform === "darwin") {
    const match = /p_starttime\s*=\s*\{\s*tv_sec\s*=\s*(\d+)\s*,\s*tv_usec\s*=\s*(\d+)\s*\}/.exec(result.stdout);
    assert.ok(match, result.stdout);
    return `darwin:${match[1]}:${match[2]}`;
  }
  const ticks = result.stdout.trim();
  assert.match(ticks, /^\d+$/);
  return `win32:${ticks}`;
}
function lockIdentity(pid, hostname = os.hostname(), token = crypto.randomUUID(), incarnation = pid === process.pid && hostname === os.hostname() ? currentProcessIncarnation() : `${process.platform}:0`) { return { hostname, pid, token, incarnation }; }
function legacyLockIdentity(...arguments_) { const { incarnation, ...owner } = lockIdentity(...arguments_); return owner; }
function writeRegistryLock(directory, identity) {
  fs.mkdirSync(directory, { recursive: true });
  fs.writeFileSync(path.join(directory, "owner"), `${JSON.stringify(identity)}\n`);
}
function runRegistry(home, environment = {}) {
  return spawnSync(process.execPath, [registryHelper, ...registryArguments(home)], {
    cwd: root,
    encoding: "utf8",
    env: { ...process.env, ENGRAM_REGISTRY_LOCK_TIMEOUT_MS: "100", ...environment },
  });
}
function registryError(result) { return `${result.stdout}\n${result.stderr}`; }
function terminatedProcessId() {
  const child = spawnSync(process.execPath, ["-e", ""], { encoding: "utf8" });
  assert.equal(child.status, 0, child.stderr);
  assert.ok(Number.isSafeInteger(child.pid) && child.pid > 0);
  return child.pid;
}
function reclaimMarker(home) { return `${registryLock(home)}.reclaim-1-${crypto.randomUUID()}`; }
function publicationReclaimMarker(home) {
  const lock = registryLock(home);
  return fs.readdirSync(path.dirname(lock)).map((name) => path.join(path.dirname(lock), name)).find((file) => path.basename(file).startsWith(`${registryLockName}.publication-reclaim-`));
}
function claimedReclaimMarker(home, owner, claimant = lockIdentity(process.pid)) {
  return `${registryLock(home)}.reclaiming-${owner.pid}-${owner.token}-${crypto.createHash("sha256").update(owner.hostname).digest("hex")}-${claimant.pid}-${claimant.token}`;
}

for (const phase of ["staged", "manifest", "receipt"]) {
  test(`registry ${phase} descriptor preserves a body persistence error when close also fails`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const { files, before } = seededRegistries(home);
      const closeLog = path.join(temp, "descriptor-close.log");
      const preload = registryDescriptorFailurePreload(temp);
      const result = runRegistry(home, {
        NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" "),
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_PHASE: phase,
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_BODY_FAILURE: "1",
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_CLOSE_FAILURE: "1",
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_LOG: closeLog,
      });
      assert.ifError(result.error);
      assert.notEqual(result.status, 0);
      const error = registryError(result);
      assert.match(error, new RegExp(`injected ${phase} body persistence failure`));
      assert.doesNotMatch(error, new RegExp(`injected ${phase} close failure`));
      assert.equal(fs.readFileSync(closeLog, "utf8"), `close:${phase}\n`);
      assert.deepEqual(files.map((file) => fs.readFileSync(file)), before);
      if (phase !== "staged") {
        assert.equal(pendingArtifact(home, phase), undefined);
        assert.equal(cleanupQuarantine(home), undefined);
      }
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });

  test(`registry ${phase} descriptor reports a close-only failure`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const { files, before } = seededRegistries(home);
      const closeLog = path.join(temp, "descriptor-close.log");
      const preload = registryDescriptorFailurePreload(temp);
      const result = runRegistry(home, {
        NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" "),
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_PHASE: phase,
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_CLOSE_FAILURE: "1",
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_LOG: closeLog,
      });
      assert.ifError(result.error);
      assert.notEqual(result.status, 0);
      assert.match(registryError(result), new RegExp(`injected ${phase} close failure`));
      assert.equal(fs.readFileSync(closeLog, "utf8"), `close:${phase}\n`);
      assert.deepEqual(files.map((file) => fs.readFileSync(file)), before);
      if (phase !== "staged") {
        assert.equal(pendingArtifact(home, phase), undefined);
        assert.equal(cleanupQuarantine(home), undefined);
      }
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });
}
function pendingArtifact(home, phase) {
  const claude = path.join(home, ".claude");
  const names = fs.readdirSync(claude);
  if (phase === "manifest") return names.map((name) => path.join(claude, name)).find((file) => path.basename(file).startsWith(".engram-registry-transaction.recovery.pending-"));
  const journal = registryJournal(home);
  return fs.existsSync(journal) ? fs.readdirSync(journal).map((name) => path.join(journal, name)).find((file) => path.basename(file).startsWith(".engram-registry-transaction.recovery.receipt-")) : undefined;
}
function cleanupQuarantine(home) {
  const claude = path.join(home, ".claude");
  return fs.readdirSync(claude).map((name) => path.join(claude, name)).find((file) => path.basename(file).startsWith(".engram-registry-cleanup-"));
}
for (const phase of ["manifest", "receipt"]) {
  test(`registry ${phase} descriptor cleans its owned artifact after body failure`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const { files, before } = seededRegistries(home);
      const preload = registryDescriptorFailurePreload(temp);
      const result = runRegistry(home, {
        NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" "),
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_PHASE: phase,
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_BODY_FAILURE: "1",
      });
      assert.notEqual(result.status, 0);
      assert.match(registryError(result), new RegExp(`injected ${phase} body persistence failure`));
      assert.equal(pendingArtifact(home, phase), undefined);
      assert.equal(cleanupQuarantine(home), undefined);
      assert.deepEqual(files.map((file) => fs.readFileSync(file)), before);
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });

  test(`registry ${phase} descriptor preserves primary failure when cleanup fails`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const { files, before } = seededRegistries(home);
      const log = path.join(temp, "descriptor-cleanup.log");
      const preload = registryDescriptorFailurePreload(temp);
      const result = runRegistry(home, {
        NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" "),
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_PHASE: phase,
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_BODY_FAILURE: "1",
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_CLEANUP_FAILURE: "1",
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_LOG: log,
      });
      assert.notEqual(result.status, 0);
      const error = registryError(result);
      assert.match(error, new RegExp(`injected ${phase} body persistence failure`));
      assert.match(error, new RegExp(`Pending ${phase} cleanup failed: injected ${phase} cleanup failure`));
      assert.equal(fs.readFileSync(log, "utf8"), `cleanup:${phase}\n`);
      assert.ok(cleanupQuarantine(home), "failed cleanup must retain the owned artifact in quarantine");
      assert.deepEqual(files.map((file) => fs.readFileSync(file)), before);
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });

  for (const point of ["before", "after"]) test(`registry ${phase} descriptor retains a foreign ${point}-claim cleanup replacement`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const { files, before } = seededRegistries(home);
      const preload = registryDescriptorFailurePreload(temp);
      const result = runRegistry(home, {
        NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" "),
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_PHASE: phase,
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_BODY_FAILURE: "1",
        ...(point === "before" ? { ENGRAM_TEST_REGISTRY_DESCRIPTOR_REPLACE_BEFORE_CLAIM: "1" } : { ENGRAM_TEST_REGISTRY_DESCRIPTOR_REPLACE_AFTER_CLAIM: "1" }),
      });
      assert.notEqual(result.status, 0);
      const error = registryError(result);
      assert.match(error, new RegExp(`injected ${phase} body persistence failure`));
      assert.match(error, /cleanup failed: retained (foreign|pending manifest directory)/);
      const retained = /Retained foreign (?:[^:]+): (.+)/.exec(error);
      assert.ok(retained, "cleanup diagnostic must name the retained foreign artifact");
      const claimed = retained[1].trim();
      assert.ok(fs.existsSync(claimed), "foreign object must remain at its atomically claimed path");
      if (phase === "manifest") assert.equal(fs.readFileSync(path.join(claimed, "foreign"), "utf8"), "foreign descriptor cleanup race sentinel\n");
      else assert.equal(fs.readFileSync(claimed, "utf8"), "foreign descriptor cleanup race sentinel\n");
      assert.deepEqual(files.map((file) => fs.readFileSync(file)), before);
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });
}
(process.platform === "win32" ? test.skip : test)("registry descriptor cleanup durably orders successful pending removal", () => {
  const temp = temporaryDirectory();
  try {
    for (const phase of ["manifest", "receipt"]) {
      const home = path.join(temp, phase);
      seededRegistries(home);
      const log = path.join(temp, `${phase}-cleanup-durability.json`);
      const preload = registryDescriptorFailurePreload(temp);
      const result = runRegistry(home, {
        NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" "),
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_PHASE: phase,
        ENGRAM_TEST_REGISTRY_DESCRIPTOR_BODY_FAILURE: "1",
        ENGRAM_TEST_REGISTRY_DURABILITY_LOG: log,
      });
      assert.notEqual(result.status, 0);
      const events = JSON.parse(fs.readFileSync(log, "utf8"));
      const cleanupRename = events.findIndex((event) => event.kind === "rename" && event.from.includes(phase === "manifest" ? ".pending-" : ".receipt-") && event.to.includes(".engram-registry-cleanup-"));
      assert.ok(cleanupRename >= 0, `${phase}: cleanup must atomically claim its exact artifact`);
      const cleanupParent = path.join(home, ".claude");
      const nextMutation = events.findIndex((event, index) => index > cleanupRename && ["rename", "unlink", "rmdir"].includes(event.kind));
      const end = nextMutation < 0 ? events.length : nextMutation;
      assert.ok(events.slice(cleanupRename + 1, end).some((event) => event.kind === "sync" && event.path === cleanupParent), `${phase}: claimed namespace must be fsynced before the next cleanup mutation`);
    }
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction recovers a terminated same-host owner", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    writeRegistryLock(registryLock(home), lockIdentity(terminatedProcessId()));
    const result = runRegistry(home);
    assert.equal(result.status, 0, registryError(result));
    assert.equal(fs.existsSync(registryLock(home)), false);
    assert.deepEqual(registrationArtifacts(home), []);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction recovers a terminated legacy same-host owner", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    writeRegistryLock(registryLock(home), legacyLockIdentity(terminatedProcessId()));
    const result = runRegistry(home);
    assert.equal(result.status, 0, registryError(result));
    assert.equal(fs.existsSync(registryLock(home)), false);
    assert.deepEqual(registrationArtifacts(home), []);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction reclaims a same-host PID reused by a different incarnation", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const live = lockIdentity(process.pid);
    const reused = { ...live, incarnation: `${live.incarnation.slice(0, -1)}${live.incarnation.endsWith("0") ? "1" : "0"}` };
    writeRegistryLock(registryLock(home), reused);
    const result = runRegistry(home);
    assert.equal(result.status, 0, registryError(result));
    assert.equal(fs.existsSync(registryLock(home)), false);
    assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [true, true, true]);
    assert.deepEqual(registrationArtifacts(home), []);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction retains a same-host owner with an exact live incarnation", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const owner = lockIdentity(process.pid);
    writeRegistryLock(registryLock(home), owner);
    const result = runRegistry(home);
    assert.notEqual(result.status, 0);
    assert.match(registryError(result), /timed out waiting for registry transaction lock/);
    assert.equal(fs.readFileSync(path.join(registryLock(home), "owner"), "utf8"), `${JSON.stringify(owner)}\n`);
    assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [false, false, false]);
    assert.deepEqual(registrationArtifacts(home), []);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction retains a live legacy same-host owner", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const owner = legacyLockIdentity(process.pid);
    writeRegistryLock(registryLock(home), owner);
    const result = runRegistry(home);
    assert.notEqual(result.status, 0);
    assert.match(registryError(result), /timed out waiting for registry transaction lock/);
    assert.equal(fs.readFileSync(path.join(registryLock(home), "owner"), "utf8"), `${JSON.stringify(owner)}\n`);
    assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [false, false, false]);
    assert.deepEqual(registrationArtifacts(home), []);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});
test("registry transaction recovers stale empty and malformed locks from interrupted owner publication", () => {
  const temp = temporaryDirectory();
  try {
    for (const [name, setup] of [
      ["empty lock", () => { }],
      ["malformed owner", (lock) => fs.writeFileSync(path.join(lock, "owner"), "not JSON\n")],
    ]) {
      const home = path.join(temp, name);
      const lock = registryLock(home);
      fs.mkdirSync(lock, { recursive: true });
      setup(lock);
      const staleAt = new Date(Date.now() - 10_000);
      fs.utimesSync(lock, staleAt, staleAt);
      const result = runRegistry(home);
      assert.equal(result.status, 0, `${name}: ${registryError(result)}`);
      assert.equal(fs.existsSync(lock), false, `${name}: recovered lock must be released`);
      assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [true, true, true]);
      assert.deepEqual(registrationArtifacts(home), []);
    }
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});
for (const [name, setup] of [
  ["empty lock", () => { }],
  ["malformed owner", (lock) => fs.writeFileSync(path.join(lock, "owner"), "not JSON\n")],
]) {
  test(`registry transaction recovers when ${name} publication marker gains a dead owner`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const lock = registryLock(home);
      fs.mkdirSync(lock, { recursive: true });
      setup(lock);
      const staleAt = new Date(Date.now() - 10_000);
      fs.utimesSync(lock, staleAt, staleAt);
      const replacement = lockIdentity(terminatedProcessId());
      const result = runRegistry(home, {
        NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${publicationMarkerOwnerReplacementPreload(temp)}`].filter(Boolean).join(" "),
        ENGRAM_TEST_REGISTRY_LOCK: lock,
        ENGRAM_TEST_REPLACEMENT: `${JSON.stringify(replacement)}\n`,
      });
      assert.equal(result.status, 0, `${name}: ${registryError(result)}`);
      assert.equal(publicationReclaimMarker(home), undefined, `${name}: dead owner publication marker must be removed`);
      assert.equal(fs.existsSync(lock), false, `${name}: recovery must release its acquired lock`);
      assert.deepEqual(registrationArtifacts(home), []);
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });

  test(`registry transaction blocks when ${name} publication marker gains a live owner`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const lock = registryLock(home);
      fs.mkdirSync(lock, { recursive: true });
      setup(lock);
      const staleAt = new Date(Date.now() - 10_000);
      fs.utimesSync(lock, staleAt, staleAt);
      const replacement = lockIdentity(process.pid);
      const replacementText = `${JSON.stringify(replacement)}\n`;
      const result = runRegistry(home, {
        NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${publicationMarkerOwnerReplacementPreload(temp)}`].filter(Boolean).join(" "),
        ENGRAM_TEST_REGISTRY_LOCK: lock,
        ENGRAM_TEST_REPLACEMENT: replacementText,
      });
      assert.notEqual(result.status, 0);
      assert.match(registryError(result), /timed out waiting for registry transaction lock/);
      const marker = publicationReclaimMarker(home);
      assert.ok(marker, `${name}: live owner publication marker must remain`);
      assert.equal(fs.readFileSync(path.join(marker, "owner"), "utf8"), replacementText);
      assert.equal(fs.existsSync(lock), false);
      assert.deepEqual(registrationArtifacts(home), []);
      assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [false, false, false]);
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });
}


for (const [name, identity] of [
  ["empty lock", () => null],
  ["live owner", () => lockIdentity(process.pid)],
  ["malformed owner", () => "not JSON\n"],
  ["foreign owner", () => lockIdentity(1, "foreign-host")],
]) {
  test(`registry transaction times out for ${name}`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const lock = registryLock(home);
      fs.mkdirSync(lock, { recursive: true });
      const value = identity();
      if (value !== null) fs.writeFileSync(path.join(lock, "owner"), typeof value === "string" ? value : `${JSON.stringify(value)}\n`);
      const result = runRegistry(home);
      assert.notEqual(result.status, 0);
      assert.match(registryError(result), /timed out waiting for registry transaction lock/);
      assert.equal(fs.existsSync(lock), true);
      assert.deepEqual(registrationArtifacts(home), []);
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });
}

test("registry transaction preserves a canonical ABA lock replacement", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const lock = registryLock(home);
    writeRegistryLock(lock, lockIdentity(terminatedProcessId()));
    const replacement = lockIdentity(process.pid);
    const preload = path.join(temp, "canonical-aba.cjs");
    fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const renameSync = fs.renameSync;
const canonical = path.resolve(process.env.ENGRAM_TEST_REGISTRY_LOCK);
let replaced = false;
fs.renameSync = (from, to, ...rest) => {
  const result = renameSync.call(fs, from, to, ...rest);
  if (!replaced && path.resolve(String(from)) === canonical && /\\.reclaim-/.test(String(to))) {
    replaced = true;
    fs.mkdirSync(canonical);
    fs.writeFileSync(path.join(canonical, "owner"), process.env.ENGRAM_TEST_REPLACEMENT);
  }
  return result;
};
`);
    const result = runRegistry(home, {
      NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" "),
      ENGRAM_TEST_REGISTRY_LOCK: lock,
      ENGRAM_TEST_REPLACEMENT: `${JSON.stringify(replacement)}\n`,
    });
    assert.notEqual(result.status, 0);
    assert.match(registryError(result), /timed out waiting for registry transaction lock/);
    assert.equal(fs.readFileSync(path.join(lock, "owner"), "utf8"), `${JSON.stringify(replacement)}\n`);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction recovers an interrupted stale quarantine", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const marker = reclaimMarker(home);
    writeRegistryLock(marker, lockIdentity(terminatedProcessId()));
    const result = runRegistry(home);
    assert.equal(result.status, 0, registryError(result));
    assert.equal(fs.existsSync(marker), false);
    assert.equal(fs.existsSync(registryLock(home)), false);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction recovers an empty claimed stale quarantine after owner-unlink crash", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const lock = registryLock(home);
    writeRegistryLock(lock, lockIdentity(terminatedProcessId()));
    const crashed = runRegistry(home, {
      NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${reclaimMarkerOwnerUnlinkCrashPreload(temp)}`].filter(Boolean).join(" "),
      ENGRAM_TEST_REGISTRY_LOCK: lock,
    });
    assert.notEqual(crashed.status, 0);
    const marker = fs.readdirSync(path.dirname(lock)).map((name) => path.join(path.dirname(lock), name)).find((file) => path.basename(file).startsWith(`${registryLockName}.reclaiming-`));
    assert.ok(marker, "owner-unlink crash must retain the strict claimed reclaim marker");
    assert.deepEqual(fs.readdirSync(marker), []);
    assert.equal(fs.existsSync(lock), false);
    assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [false, false, false], "crash before lock acquisition must not commit registry targets");

    const recovered = runRegistry(home);
    assert.equal(recovered.status, 0, registryError(recovered));
    assert.equal(fs.existsSync(marker), false);
    assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [true, true, true]);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction recovers a claimed stale quarantine after raw-to-claimed crash", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const lock = registryLock(home);
    writeRegistryLock(lock, lockIdentity(terminatedProcessId()));
    const crashed = runRegistry(home, {
      NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${reclaimMarkerRenameCrashPreload(temp)}`].filter(Boolean).join(" "),
      ENGRAM_TEST_REGISTRY_LOCK: lock,
    });
    assert.notEqual(crashed.status, 0);
    const marker = fs.readdirSync(path.dirname(lock)).map((name) => path.join(path.dirname(lock), name)).find((file) => path.basename(file).startsWith(`${registryLockName}.reclaiming-`));
    assert.ok(marker, "raw-to-claimed crash must retain the strict claimed reclaim marker");
    assert.ok(fs.existsSync(path.join(marker, "owner")));
    assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [false, false, false]);

    const recovered = runRegistry(home);
    assert.equal(recovered.status, 0, registryError(recovered));
    assert.equal(fs.existsSync(marker), false);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction preserves a post-validation reclaim replacement", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const lock = registryLock(home);
    const raw = reclaimMarker(home);
    const replacement = lockIdentity(process.pid);
    writeRegistryLock(raw, lockIdentity(terminatedProcessId()));
    const result = runRegistry(home, {
      NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${reclaimMarkerReplacementPreload(temp)}`].filter(Boolean).join(" "),
      ENGRAM_TEST_REGISTRY_LOCK: lock,
      ENGRAM_TEST_REPLACEMENT: `${JSON.stringify(replacement)}\n`,
    });
    assert.notEqual(result.status, 0);
    assert.match(registryError(result), /timed out waiting for registry transaction lock/);
    const claimed = fs.readdirSync(path.dirname(lock)).map((name) => path.join(path.dirname(lock), name)).find((file) => path.basename(file).startsWith(`${registryLockName}.reclaiming-`));
    assert.ok(claimed, "replacement must be retained under the claimed marker");
    assert.equal(fs.readFileSync(path.join(claimed, "owner"), "utf8"), `${JSON.stringify(replacement)}\n`);
    assert.ok(fs.existsSync(`${raw}.validated`), "validated marker must remain aside from the replacement");
    assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [false, false, false]);
    assert.deepEqual(registrationArtifacts(home), []);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

for (const [name, setup] of [
  ["empty marker", () => { }],
  ["malformed owner", (marker) => fs.writeFileSync(path.join(marker, "owner"), "not JSON\n")],
  ["foreign owner", (marker) => fs.writeFileSync(path.join(marker, "owner"), `${JSON.stringify(lockIdentity(1, "foreign-host"))}\n`)],
  ["unexpected foreign child", (marker) => {
    fs.writeFileSync(path.join(marker, "owner"), `${JSON.stringify(lockIdentity(terminatedProcessId()))}\n`);
    fs.writeFileSync(path.join(marker, "foreign"), "foreign state\n");
  }],
]) {
  test(`registry transaction retains a reclaim marker with ${name}`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const marker = reclaimMarker(home);
      fs.mkdirSync(marker, { recursive: true });
      setup(marker);
      const before = fs.readdirSync(marker).sort();
      const result = runRegistry(home);
      assert.notEqual(result.status, 0);
      assert.match(registryError(result), /timed out waiting for registry transaction lock/);
      assert.deepEqual(fs.readdirSync(marker).sort(), before);
      assert.deepEqual(registrationArtifacts(home), []);
      assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [false, false, false]);
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });
}

for (const [name, setup] of [
  ["malformed name", (home) => `${registryLock(home)}.reclaiming-malformed`],
  ["live owner", (home) => {
    const owner = lockIdentity(process.pid);
    return { marker: claimedReclaimMarker(home, owner), owner };
  }],
  ["foreign owner", (home) => {
    const owner = lockIdentity(1, "foreign-host");
    return { marker: claimedReclaimMarker(home, owner), owner };
  }],
  ["mismatched owner", (home) => {
    const expected = lockIdentity(terminatedProcessId());
    return { marker: claimedReclaimMarker(home, expected), owner: { ...expected, token: crypto.randomUUID() } };
  }],
  ["unexpected child", (home) => {
    const owner = lockIdentity(terminatedProcessId());
    return { marker: claimedReclaimMarker(home, owner), owner, foreign: true };
  }],
]) {
  test(`registry transaction retains a claimed reclaim marker with ${name}`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const fixture = setup(home);
      const marker = typeof fixture === "string" ? fixture : fixture.marker;
      fs.mkdirSync(marker, { recursive: true });
      if (typeof fixture !== "string") {
        fs.writeFileSync(path.join(marker, "owner"), `${JSON.stringify(fixture.owner)}\n`);
        if (fixture.foreign) fs.writeFileSync(path.join(marker, "foreign"), "foreign state\n");
      }
      const before = fs.readdirSync(marker).sort();
      const result = runRegistry(home);
      assert.notEqual(result.status, 0);
      assert.match(registryError(result), /timed out waiting for registry transaction lock/);
      assert.deepEqual(fs.readdirSync(marker).sort(), before);
      assert.deepEqual(registrationArtifacts(home), []);
      assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [false, false, false]);
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });
}

test("registry transaction treats a live reclaim marker as contention", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const marker = reclaimMarker(home);
    const owner = lockIdentity(process.pid);
    writeRegistryLock(marker, owner);
    const result = runRegistry(home);
    assert.notEqual(result.status, 0);
    assert.match(registryError(result), /timed out waiting for registry transaction lock/);
    assert.equal(fs.readFileSync(path.join(marker, "owner"), "utf8"), `${JSON.stringify(owner)}\n`);
    assert.equal(fs.existsSync(registryLock(home)), false);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction rejects release with a matching token and different PID", () => {
  const temp = temporaryDirectory();
  try {

    const home = path.join(temp, "home");
    const lock = registryLock(home);
    const preload = path.join(temp, "different-pid-release.cjs");
    fs.writeFileSync(preload, `const fs = require("node:fs");
const path = require("node:path");
const renameSync = fs.renameSync;
const writeFileSync = fs.writeFileSync;
const lock = path.resolve(process.env.ENGRAM_TEST_REGISTRY_LOCK);
let changed = false;
fs.renameSync = (from, to, ...rest) => {
  const result = renameSync.call(fs, from, to, ...rest);
  if (!changed && path.resolve(String(from)) === lock && String(to).includes(".reclaim-")) {
    changed = true;
    const ownerFile = path.join(to, "owner");
    const original = JSON.parse(fs.readFileSync(ownerFile, "utf8"));
    const replacement = { ...original, pid: original.pid + 1 };
    writeFileSync.call(fs, process.env.ENGRAM_TEST_RELEASE_IDENTITIES, JSON.stringify({ original, replacement }) + "\\n");
    writeFileSync.call(fs, ownerFile, JSON.stringify(replacement) + "\\n");
  }
  return result;
};
`);
    const identities = path.join(temp, "release-identities.json");
    const result = runRegistry(home, {
      NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" "),
      ENGRAM_TEST_REGISTRY_LOCK: lock,
      ENGRAM_TEST_RELEASE_IDENTITIES: identities,
    });
    assert.notEqual(result.status, 0);
    assert.match(registryError(result), /registry transaction lock ownership changed/);
    const marker = fs.readdirSync(path.dirname(lock)).map((name) => path.join(path.dirname(lock), name)).find((file) => file.startsWith(`${lock}.reclaim-`));
    assert.ok(marker, "mismatched owner must remain quarantined");
    const owner = JSON.parse(fs.readFileSync(path.join(marker, "owner"), "utf8"));
    const identitiesValue = JSON.parse(fs.readFileSync(identities, "utf8"));
    assert.equal(identitiesValue.original.pid, result.pid);
    assert.deepEqual(owner, identitiesValue.replacement);
    assert.equal(owner.token, identitiesValue.original.token);
    const [pluginsFile, settingsFile, marketplacesFile] = registryArguments(home);
    assert.ok(fs.existsSync(pluginsFile));
    assert.ok(fs.existsSync(settingsFile));
    assert.ok(fs.existsSync(marketplacesFile));
    assert.equal(fs.existsSync(lock), false);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});
for (const backupNumber of [1, 2, 3]) {
  test(`registry transaction recovers all originals after crash following backup ${backupNumber}`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const { files, before } = seededRegistries(home);
      const preload = crashAfterRegistrationBackupPreload(temp, backupNumber);
      const crashed = runRegistry(home, {
        NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" "),
      });
      assert.notEqual(crashed.status, 0);
      assert.equal(fs.existsSync(registryJournal(home)), true);
      const recovered = runRegistry(home);
      assert.equal(recovered.status, 0, registryError(recovered));
      for (const [index, file] of files.entries()) {
        const parsed = JSON.parse(fs.readFileSync(file, "utf8").replace(/^\uFEFF/, ""));
        assert.equal(parsed.keep, JSON.parse(before[index].toString("utf8")).keep);
      }
      assert.equal(fs.existsSync(registryJournal(home)), false);
      assert.deepEqual(registrationArtifacts(home), []);
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });
}

test("registry transaction recovers an interrupted legacy journal manifest", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const { files, before } = seededRegistries(home);
    const crashed = runRegistry(home, {
      NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${crashAfterRegistrationBackupPreload(temp, 1)}`].filter(Boolean).join(" "),
    });
    assert.notEqual(crashed.status, 0);
    const manifestFile = path.join(registryJournal(home), "manifest.json");
    const manifest = JSON.parse(fs.readFileSync(manifestFile, "utf8"));
    const { incarnation, ...legacy } = manifest.lock;
    manifest.lock = legacy;
    fs.writeFileSync(manifestFile, `${JSON.stringify(manifest)}\n`);
    const recovered = runRegistry(home);
    assert.equal(recovered.status, 0, registryError(recovered));
    for (const [index, file] of files.entries()) assert.equal(JSON.parse(fs.readFileSync(file, "utf8").replace(/^\uFEFF/, "")).keep, JSON.parse(before[index].toString("utf8")).keep);
    assert.equal(fs.existsSync(registryJournal(home)), false);
    assert.deepEqual(registrationArtifacts(home), []);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

const posixTest = process.platform === "win32" ? test.skip : test;
posixTest("registry transaction durably orders POSIX journal, target, receipt, and recovery namespaces", () => {
  const temp = temporaryDirectory();
  try {
    const trace = (home, label) => {
      const log = path.join(temp, `registry-durability-${label}.json`);
      const result = runRegistry(home, {
        NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${registryDurabilityPreload(temp)}`].filter(Boolean).join(" "),
        ENGRAM_TEST_REGISTRY_DURABILITY_LOG: log,
      });
      assert.equal(result.status, 0, registryError(result));
      return JSON.parse(fs.readFileSync(log, "utf8"));
    };
    const assertSyncBeforeNextMutation = (events, directory, after, message) => {
      const next = events.findIndex((event, index) => index > after && ["rename", "link", "unlink", "rmdir"].includes(event.kind));
      const end = next < 0 ? events.length : next;
      assert.ok(events.slice(after + 1, end).some((event) => event.kind === "sync" && event.path === directory), message);
    };
    const assertTargetMutations = (events, files, label) => {
      const targetFor = (event) => files.find((file) => event.from === file || event.to === file || event.from?.startsWith(`${file}.`) || event.to?.startsWith(`${file}.`) || event.path === file || event.path?.startsWith(`${file}.`));
      for (const [index, event] of events.entries()) if (["rename", "link", "unlink"].includes(event.kind) && targetFor(event)) {
        const file = targetFor(event);
        assertSyncBeforeNextMutation(events, path.dirname(file), index, `${label}: target namespace mutation must be durable before the next mutation: ${file}`);
      }
    };

    const home = path.join(temp, "success");
    const success = seededRegistries(home);
    const journal = registryJournal(home);
    const successEvents = trace(home, "success");
    assertTargetMutations(successEvents, success.files, "success");
    const journalPublication = successEvents.findIndex((event) => event.kind === "rename" && event.to === journal && event.from.startsWith(`${journal}.pending-`));
    const firstTargetMove = successEvents.findIndex((event) => event.kind === "rename" && success.files.some((file) => event.from === file && event.to.startsWith(`${file}.backup-`)));
    assert.ok(journalPublication >= 0 && firstTargetMove > journalPublication, "canonical journal must publish before the first target move");
    assertSyncBeforeNextMutation(successEvents, success.claude, journalPublication, "journal parent must be durable before the first target move");
    const receiptPublication = successEvents.findIndex((event) => event.kind === "rename" && event.to === path.join(journal, "receipt.json"));
    assert.ok(receiptPublication >= 0, "receipt must be published");
    assertSyncBeforeNextMutation(successEvents, journal, receiptPublication, "receipt publication must be durable before terminal cleanup");

    const rollbackHome = path.join(temp, "rollback");
    const rollback = seededRegistries(rollbackHome);
    assert.notEqual(runRegistry(rollbackHome, { NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${crashAfterRegistrationBackupPreload(temp, 1)}`].filter(Boolean).join(" ") }).status, 0);
    const rollbackEvents = trace(rollbackHome, "rollback");
    assertTargetMutations(rollbackEvents, rollback.files, "rollback");
    const restored = rollbackEvents.findIndex((event) => event.kind === "rename" && event.from.startsWith(`${rollback.files[0]}.backup-`) && event.to === rollback.files[0]);
    const rollbackManifestRemoval = rollbackEvents.findIndex((event) => event.kind === "unlink" && event.path === path.join(registryJournal(rollbackHome), "manifest.json"));
    const rollbackRecordRemoval = rollbackEvents.findIndex((event) => event.kind === "rmdir" && event.path === registryJournal(rollbackHome));
    assert.ok(restored >= 0 && rollbackManifestRemoval > restored && rollbackRecordRemoval > rollbackManifestRemoval, "rollback must restore the backup before deleting its recovery record");
    assert.ok(rollbackEvents.slice(0, restored).some((event) => event.kind === "sync" && event.path === rollback.claude), "pre-existing rollback journal parent must be durable before recovery mutation");
    assertSyncBeforeNextMutation(rollbackEvents, path.dirname(rollback.files[0]), restored, "rollback restore must be durable before recovery record removal");
    assertSyncBeforeNextMutation(rollbackEvents, registryJournal(rollbackHome), rollbackManifestRemoval, "rollback manifest removal must be durable before recovery record removal");
    assertSyncBeforeNextMutation(rollbackEvents, rollback.claude, rollbackRecordRemoval, "rollback recovery record removal must be durable before the next transaction");

    const terminalHome = path.join(temp, "terminal");
    const terminal = seededRegistries(terminalHome);
    assert.notEqual(runRegistry(terminalHome, { NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${terminalJournalCrashPreload(temp, "terminal-receipt")}`].filter(Boolean).join(" ") }).status, 0);
    const terminalEvents = trace(terminalHome, "terminal");
    assertTargetMutations(terminalEvents, terminal.files, "terminal");
    const terminalManifestRemoval = terminalEvents.findIndex((event) => event.kind === "unlink" && event.path.startsWith(`${registryJournal(terminalHome)}.terminal-`) && path.basename(event.path) === "manifest.json");
    assert.ok(terminalManifestRemoval >= 0, "manifest-only terminal recovery must remove its manifest");
    assert.ok(terminalEvents.slice(0, terminalManifestRemoval).some((event) => event.kind === "sync" && event.path === terminal.claude), "pre-existing terminal journal parent must be durable before recovery mutation");
    for (const file of terminal.files) assert.ok(
      terminalEvents.slice(0, terminalManifestRemoval).some((event) => event.kind === "sync" && event.path === path.dirname(file)),
      `terminal recovery must durably retain ${file} before journal cleanup`,
    );
    const terminalMarkerRemoval = terminalEvents.findIndex((event) => event.kind === "rmdir" && event.path.startsWith(`${registryJournal(terminalHome)}.terminal-`));
    assert.ok(terminalMarkerRemoval > terminalManifestRemoval, "manifest-only terminal recovery must remove the terminal marker");
    assertSyncBeforeNextMutation(terminalEvents, path.dirname(terminalEvents[terminalManifestRemoval].path), terminalManifestRemoval, "terminal manifest removal must be durable before marker removal");
    assertSyncBeforeNextMutation(terminalEvents, terminal.claude, terminalMarkerRemoval, "terminal marker removal must be durable before the next transaction");
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});


test("registry transaction leaves a foreign interrupted target and journal untouched", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const { files } = seededRegistries(home);
    const preload = crashAfterRegistrationBackupPreload(temp, 1);
    const crashed = runRegistry(home, { NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" ") });
    assert.notEqual(crashed.status, 0);
    fs.writeFileSync(files[0], "foreign target\n");
    const journal = registryJournal(home);
    const before = fs.readFileSync(path.join(journal, "manifest.json"));
    const recovered = runRegistry(home);
    assert.notEqual(recovered.status, 0);
    assert.match(registryError(recovered), /retained foreign target/);
    assert.deepEqual(fs.readFileSync(files[0]), Buffer.from("foreign target\n"));
    assert.deepEqual(fs.readFileSync(path.join(journal, "manifest.json")), before);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

for (const point of ["receipt", "terminal", "terminal-receipt", "terminal-manifest"]) {
  test(`registry transaction preserves committed outputs after ${point} cleanup interruption`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const { files } = seededRegistries(home);
      const preload = terminalJournalCrashPreload(temp, point);
      const crashed = runRegistry(home, { NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" ") });
      assert.notEqual(crashed.status, 0);
      const recovered = runRegistry(home);
      assert.equal(recovered.status, 0, registryError(recovered));
      for (const file of files) assert.match(fs.readFileSync(file, "utf8"), /"keep"\s*:/);
      assert.equal(fs.existsSync(registryJournal(home)), false);
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });
}

for (const point of ["released", "owner"]) {
  test(`registry transaction cleans interrupted ${point} released marker`, () => {
    const temp = temporaryDirectory();
    try {
      const home = path.join(temp, "home");
      const crashed = runRegistry(home, { NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${releasedMarkerCrashPreload(temp, point)}`].filter(Boolean).join(" ") });
      assert.notEqual(crashed.status, 0);
      const recovered = runRegistry(home);
      assert.equal(recovered.status, 0, registryError(recovered));
      assert.equal(fs.readdirSync(path.join(home, ".claude")).filter((name) => name.includes(".released-")).length, 0);
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
  });
}
test("registry transaction retains a static foreign released marker", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const { claude, files } = seededRegistries(home);
    const owner = lockIdentity(process.pid, "foreign-host");
    const marker = path.join(claude, `${registryLockName}.released-${owner.pid}-${owner.token}-${crypto.createHash("sha256").update(Buffer.from(owner.hostname, "utf8")).digest("hex")}`);
    const ownerBytes = Buffer.from(JSON.stringify(owner));
    fs.mkdirSync(marker);
    fs.writeFileSync(path.join(marker, "owner"), ownerBytes);
    const result = runRegistry(home);
    assert.equal(result.status, 0, registryError(result));
    const [plugins, settings, marketplaces] = files.map((file) => JSON.parse(fs.readFileSync(file, "utf8")));
    assert.equal(plugins.plugins["engram@engram"][0].version, "6.47.5");
    assert.equal(settings.enabledPlugins["engram@engram"], true);
    assert.equal(marketplaces.engram.source.source, "directory");
    assert.equal(fs.existsSync(marker), true);
    assert.deepEqual(fs.readdirSync(marker), ["owner"]);
    assert.deepEqual(fs.readFileSync(path.join(marker, "owner")), ownerBytes);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});


test("registry transaction tolerates cooperative released double cleanup", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const preload = releasedMarkerDoubleCleanupPreload(temp);
    const result = runRegistry(home, { NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" ") });
    assert.equal(result.status, 0, registryError(result));
    assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [true, true, true]);
    assert.equal(fs.readdirSync(path.join(home, ".claude")).filter((name) => name.includes(".released-")).length, 0);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction tolerates released marker lstat-to-readdir disappearance", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const preload = releasedMarkerLstatToReaddirRemovalPreload(temp);
    const result = runRegistry(home, { NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${preload}`].filter(Boolean).join(" ") });
    assert.equal(result.status, 0, registryError(result));
    assert.deepEqual(registryArguments(home).slice(0, 3).map((file) => fs.existsSync(file)), [true, true, true]);
    assert.equal(fs.readdirSync(path.join(home, ".claude")).filter((name) => name.includes(".released-")).length, 0);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction rejects malformed and ambiguous journal without mutations", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const { files, before } = seededRegistries(home);
    const journal = registryJournal(home);
    fs.mkdirSync(journal, { recursive: true });
    fs.writeFileSync(path.join(journal, "manifest.json"), "{}\n");
    fs.writeFileSync(path.join(journal, "foreign"), "foreign\n");
    const result = runRegistry(home);
    assert.notEqual(result.status, 0);
    assert.match(registryError(result), /invalid registry recovery journal/);
    assert.deepEqual(files.map((file) => fs.readFileSync(file)), before);
    assert.equal(fs.existsSync(path.join(journal, "foreign")), true);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("registry transaction resumes an interrupted recovery", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home");
    const { files, before } = seededRegistries(home);
    const crash = crashAfterRegistrationBackupPreload(temp, 2);
    assert.notEqual(runRegistry(home, { NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${crash}`].filter(Boolean).join(" ") }).status, 0);
    const journal = registryJournal(home);
    const interrupt = path.join(temp, "interrupt-recovery.cjs");
    fs.writeFileSync(interrupt, `const fs = require("node:fs"); const rename = fs.renameSync; let restored = false; fs.renameSync = (from, to, ...rest) => { const result = rename.call(fs, from, to, ...rest); if (!restored && /\\.backup-/.test(String(from))) { restored = true; process.abort(); } return result; };`);
    assert.notEqual(runRegistry(home, { NODE_OPTIONS: [process.env.NODE_OPTIONS, `--require=${interrupt}`].filter(Boolean).join(" ") }).status, 0);
    assert.equal(fs.existsSync(journal), true);
    const recovered = runRegistry(home);
    assert.equal(recovered.status, 0, registryError(recovered));
    for (const [index, file] of files.entries()) assert.equal(JSON.parse(fs.readFileSync(file, "utf8")).keep, JSON.parse(before[index].toString("utf8")).keep);
    assert.equal(fs.existsSync(journal), false);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

function nativePwsh() {
  const result = spawnSync("where.exe", ["pwsh.exe"], { cwd: root, encoding: "utf8", env: process.env });
  assert.equal(result.status, 0, result.stderr || result.stdout);
  const executable = result.stdout.split(/\r?\n/).find(Boolean);
  assert.ok(executable && path.isAbsolute(executable), "unpoisoned parent PATH must resolve an absolute pwsh.exe");
  return executable;
}
function powerShellQuote(value) { return value.replaceAll("'", "''"); }
function runPowerShellRegistrationFixture(temp, helper, failCacheCopy = false) {
  const home = path.join(temp, "home");
  const install = path.join(home, ".claude", "plugins", "marketplaces", "engram");
  const fakeBin = path.join(temp, "poisoned-path");
  const poisonedNode = path.join(temp, "poisoned-node-ran");
  fs.mkdirSync(path.join(install, "scripts"), { recursive: true });
  fs.mkdirSync(fakeBin, { recursive: true });
  fs.writeFileSync(path.join(install, "scripts", "register-plugin.js"), helper);
  fs.writeFileSync(path.join(install, "hook.js"), "module.exports = {};\n");
  fs.writeFileSync(path.join(fakeBin, "node.cmd"), `@echo off\r\necho poisoned > "${poisonedNode}"\r\nexit /b 97\r\n`);
  const ps = `$env:USERPROFILE = '${powerShellQuote(home)}'
$env:PATH = '${powerShellQuote(fakeBin)}'
$env:ENGRAM_TEST_NODE = '${powerShellQuote(process.execPath)}'
$source = Get-Content -LiteralPath '${powerShellQuote(path.join(root, "scripts", "install.ps1"))}' -Raw
$source = $source.Substring($source.IndexOf('$ErrorActionPreference = "Stop"'))
$source = [regex]::Split($source, '# ---------------------------------------------------------------------------\\r?\\n# Entry point')[0]
Invoke-Expression $source
function Get-Command { param($Name) if ($Name -eq 'node') { return [pscustomobject]@{ Source = $env:ENGRAM_TEST_NODE } }; return $null }
${failCacheCopy ? "function Copy-Item { throw 'injected cache copy failure' }" : ""}
$node = Assert-Node
Register-Plugin -Ver 'v6.47.5' -NodeExecutable $node
`;
  const result = spawnSync(nativePwsh(), ["-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", ps], {
    cwd: root,
    encoding: "utf8",
    env: { ...process.env, PATH: fakeBin },
  });
  return { home, poisonedNode, result };
}

const windowsTest = process.platform === "win32" ? test : test.skip;
windowsTest("PowerShell registration preserves diagnostics and stops before registry writes on cache copy failure", () => {
  const temp = temporaryDirectory();
  try {
    const warning = runPowerShellRegistrationFixture(temp, "console.error('registry warning');\nprocess.exit(0);\n");
    assert.equal(warning.result.status, 0, registryError(warning.result));
    assert.match(registryError(warning.result), /registry warning/);
    assert.equal(fs.existsSync(warning.poisonedNode), false);

    const failure = runPowerShellRegistrationFixture(path.join(temp, "failure"), "console.error('registry diagnostic');\nprocess.exit(42);\n");
    assert.notEqual(failure.result.status, 0);
    assert.match(registryError(failure.result), /registry diagnostic/);
    assert.match(registryError(failure.result), /exit code 42/);
    assert.equal(fs.existsSync(failure.poisonedNode), false);

    const copyFailure = runPowerShellRegistrationFixture(path.join(temp, "copy-failure"), "process.exit(99);\n", true);
    assert.notEqual(copyFailure.result.status, 0);
    assert.match(registryError(copyFailure.result), /injected cache copy failure/);
    assert.deepEqual(registryArguments(copyFailure.home).slice(0, 3).map((file) => fs.existsSync(file)), [false, false, false]);
    assert.equal(fs.existsSync(copyFailure.poisonedNode), false);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});


function installerSemver(source) {
  const match = source.match(/^const semver = (\/.+\/);$/m);
  assert.ok(match, "trusted inline validator must declare SemVer");
  return Function(`return ${match[1]}`)();
}

function target(version, asset, bytes) {
  return { version, asset, size: bytes.length, sha256: crypto.createHash("sha256").update(bytes).digest("hex") };
}

test("one strict schema rejects malformed fields and target matrices", () => {
  const bytes = Buffer.from("trusted");
  const policy = createPolicy("6.47.1", {
    "win32-x64": target("6.47.1", "engram-windows-amd64.exe", bytes),
    "linux-x64": target("6.47.1", "engram-linux-amd64", bytes),
    "darwin-arm64": target("6.47.1", "engram-darwin-arm64", bytes),
  });
  for (const mutate of [
    (value) => { value.extra = true; },
    (value) => { delete value.build_contract; },
    (value) => { value.targets["linux-x64"].desired.size = "7"; },
    (value) => { delete value.targets["darwin-arm64"]; },
    (value) => { value.targets["linux-x64"].desired.asset = "engram-darwin-arm64"; },
  ]) {
    const malformed = structuredClone(policy);
    mutate(malformed);
    assert.throws(() => parsePolicy(JSON.stringify(malformed), "6.47.1"), BootstrapError);
  }
  const duplicateTarget = JSON.stringify(policy).replace('"targets":{', '"targets":{"win32-x64":null,');
  assert.throws(() => parsePolicy(duplicateTarget, "6.47.1"), /duplicate fields/);
});

test("shared policy rejects build metadata while preserving prereleases", () => {
  const bytes = Buffer.from("trusted");
  const targets = (version) => ({
    "win32-x64": target(version, "engram-windows-amd64.exe", bytes),
    "linux-x64": target(version, "engram-linux-amd64", bytes),
    "darwin-arm64": target(version, "engram-darwin-arm64", bytes),
  });
  assert.doesNotThrow(() => createPolicy("6.47.1-rc.1", targets("6.47.1-rc.1")));
  assert.throws(() => createPolicy("6.47.1+build.1", targets("6.47.1+build.1")), BootstrapError);
});

test("Node 16 fails before curl or archive access", () => {
  const temp = temporaryDirectory();
  try {
    const fakeBin = path.join(temp, "bin");
    const accessed = path.join(temp, "curl-accessed");
    fs.mkdirSync(fakeBin, { recursive: true });
    fs.writeFileSync(path.join(fakeBin, "node"), "#!/usr/bin/env bash\necho v16.0.0\n", { mode: 0o755 });
    fs.writeFileSync(path.join(fakeBin, "curl"), "#!/usr/bin/env bash\ntouch \"$CURL_ACCESSED\"\n", { mode: 0o755 });
    const environment = `HOME=${shellQuote(bashPath(path.join(temp, "home")))} PATH=${shellQuote(installerPath(fakeBin))} CURL_ACCESSED=${shellQuote(bashPath(accessed))}`;
    const result = spawnSync("bash", ["-c", `${environment} bash scripts/install.sh v6.47.1`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(result.error);
    assert.notEqual(result.status, 0);
    assert.match(`${result.stdout}\n${result.stderr}`, /Node\.js 18\+/);
    assert.equal(fs.existsSync(accessed), false);
    assert.equal(fs.existsSync(path.join(temp, "home", ".claude")), false);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("Windows-compatible Bash requires unzip before download", () => {
  const temp = temporaryDirectory();
  try {
    const fakeBin = path.join(temp, "bin");
    const accessed = path.join(temp, "curl-accessed");
    fs.mkdirSync(fakeBin, { recursive: true });
    fs.writeFileSync(path.join(fakeBin, "node"), "#!/bin/sh\necho v18.0.0\n", { mode: 0o755 });
    fs.writeFileSync(path.join(fakeBin, "uname"), "#!/bin/sh\n[ \"$1\" = -s ] && echo MINGW64_NT || echo x86_64\n", { mode: 0o755 });
    fs.writeFileSync(path.join(fakeBin, "curl"), "#!/bin/sh\ntouch \"$CURL_ACCESSED\"\n", { mode: 0o755 });
    const environment = `HOME=${shellQuote(bashPath(path.join(temp, "home")))} PATH=${shellQuote(bashPath(fakeBin))} CURL_ACCESSED=${shellQuote(bashPath(accessed))}`;
    const result = spawnSync("bash", ["-c", `${environment} source scripts/install.sh v6.47.1`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(result.error);
    assert.notEqual(result.status, 0);
    assert.match(`${result.stdout}\n${result.stderr}`, /unzip is required/);
    assert.equal(fs.existsSync(accessed), false);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

function crc32(bytes) {
  let crc = 0xffffffff;
  for (const byte of bytes) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit += 1) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function writeZip(archiveRoot, archivePath, entries) {
  const records = [];
  const centralDirectory = [];
  let offset = 0;
  for (const entry of entries) {
    const name = Buffer.from(entry);
    const bytes = fs.readFileSync(path.join(archiveRoot, ...entry.split("/")));
    const crc = crc32(bytes);
    const local = Buffer.alloc(30 + name.length);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(bytes.length, 18);
    local.writeUInt32LE(bytes.length, 22);
    local.writeUInt16LE(name.length, 26);
    name.copy(local, 30);
    records.push(local, bytes);

    const central = Buffer.alloc(46 + name.length);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt32LE(crc, 16);
    central.writeUInt32LE(bytes.length, 20);
    central.writeUInt32LE(bytes.length, 24);
    central.writeUInt16LE(name.length, 28);
    central.writeUInt32LE(offset, 42);
    name.copy(central, 46);
    centralDirectory.push(central);
    offset += local.length + bytes.length;
  }
  const directory = Buffer.concat(centralDirectory);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(entries.length, 8);
  end.writeUInt16LE(entries.length, 10);
  end.writeUInt32LE(directory.length, 12);
  end.writeUInt32LE(offset, 16);
  fs.writeFileSync(archivePath, Buffer.concat([...records, directory, end]));
}

function buildServerArchive(archiveRoot, archivePath) {
  const entries = ["package.json", "extensions/engram-memory.mjs", "bootstrap-targets.json"];
  if (archivePath.endsWith(".tar.gz")) {
    const archived = spawnSync("tar", ["-czf", archivePath, "-C", archiveRoot, ...entries], { encoding: "utf8" });
    assert.equal(archived.status, 0, archived.stderr);
  } else {
    writeZip(archiveRoot, archivePath, entries);
  }
}

test("generator check mode and combined artifact gate accept only the shared target rows", () => {
  const temp = temporaryDirectory();
  try {
    const fakeBin = path.join(temp, "bin");
    const fakeGo = path.join(fakeBin, "go");
    const policyPath = path.join(temp, "bootstrap-targets.json");
    const dist = path.join(temp, "dist");
    const currentVersion = parsePolicy(fs.readFileSync(path.join(root, "plugin", "engram", "bootstrap-targets.json"), "utf8")).package_version;
    fs.mkdirSync(fakeBin, { recursive: true });
    fs.writeFileSync(fakeGo, "#!/usr/bin/env bash\nif [[ $1 == version ]]; then echo 'go version go1.25.12 linux/amd64'; exit 0; fi\nwhile [[ $# -gt 0 ]]; do if [[ $1 == -o ]]; then shift; printf '%s-%s' \"$GOOS\" \"$GOARCH\" > \"$1\"; exit 0; fi; shift; done\nexit 1\n", { mode: 0o755 });
    const fakeGoArgument = shellQuote(bashPath(fakeGo));
    const policyArgument = shellQuote(bashPath(policyPath));
    run("bash", ["-c", `ENGRAM_BOOTSTRAP_GO=${fakeGoArgument} scripts/prepare-bootstrap-policy.sh --version ${currentVersion} --output ${policyArgument}`]);
    run("bash", ["-c", `ENGRAM_BOOTSTRAP_GO=${fakeGoArgument} scripts/prepare-bootstrap-policy.sh --version ${currentVersion} --output ${policyArgument} --check`]);
    const policy = parsePolicy(fs.readFileSync(policyPath, "utf8"), currentVersion);
    fs.mkdirSync(dist, { recursive: true });
    for (const { desired } of Object.values(policy.targets)) {
      const bytes = desired.asset.includes("windows") ? "windows-amd64" : desired.asset.includes("linux") ? "linux-amd64" : "darwin-arm64";
      fs.writeFileSync(path.join(dist, desired.asset), bytes);
    }
    const archiveRoot = path.join(temp, "archive");
    fs.mkdirSync(path.join(archiveRoot, "extensions"), { recursive: true });
    fs.copyFileSync(path.join(root, "plugin", "engram", "package.json"), path.join(archiveRoot, "package.json"));
    fs.copyFileSync(path.join(root, "plugin", "engram", "extensions", "engram-memory.mjs"), path.join(archiveRoot, "extensions", "engram-memory.mjs"));
    fs.copyFileSync(policyPath, path.join(archiveRoot, "bootstrap-targets.json"));
    const archives = [
      `engram_${currentVersion}_linux_amd64.tar.gz`,
      `engram_${currentVersion}_darwin_arm64.tar.gz`,
      `engram_${currentVersion}_windows_amd64.zip`,
    ];
    for (const archive of archives) buildServerArchive(archiveRoot, path.join(dist, archive));
    const gate = ["scripts/check-bootstrap-policy-artifacts.sh", "--policy", bashPath(policyPath), "--dist", bashPath(dist)];
    run("bash", gate);

    const rawClientPath = path.join(dist, policy.targets["linux-x64"].desired.asset);
    const rawClient = fs.readFileSync(rawClientPath);
    fs.appendFileSync(rawClientPath, "drift");
    const rawRejected = spawnSync("bash", gate, { cwd: root, encoding: "utf8" });
    assert.notEqual(rawRejected.status, 0);
    assert.match(rawRejected.stderr, /policy mismatch/);
    fs.writeFileSync(rawClientPath, rawClient);

    const manifestPath = path.join(archiveRoot, "package.json");
    const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
    manifest.engines.node = ">=20";
    fs.writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
    buildServerArchive(archiveRoot, path.join(dist, archives[0]));
    const serverRejected = spawnSync("bash", gate, { cwd: root, encoding: "utf8" });
    assert.notEqual(serverRejected.status, 0);
    assert.match(serverRejected.stderr, /OMP package manifest does not match the release contract/);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("direct installer rejects archive self-validation before install mutation", () => {
  const temp = temporaryDirectory();
  try {
    const archiveRoot = path.join(temp, "archive");
    const fakeBin = path.join(temp, "bin");
    const home = path.join(temp, "home");
    for (const directory of ["hooks", "scripts", ".claude-plugin", "extensions"]) fs.mkdirSync(path.join(archiveRoot, directory), { recursive: true });
    fs.mkdirSync(fakeBin, { recursive: true });
    fs.writeFileSync(path.join(archiveRoot, "hooks", "hook.js"), "module.exports = {};\n");
    fs.writeFileSync(path.join(archiveRoot, "hooks", "hooks.json"), "{}\n");
    fs.writeFileSync(path.join(archiveRoot, "scripts", "bootstrap-policy.js"), "process.exit(0);\n");
    fs.copyFileSync(registryHelper, path.join(archiveRoot, "scripts", "register-plugin.js"));
    fs.writeFileSync(path.join(archiveRoot, ".claude-plugin", "plugin.json"), "{}\n");
    fs.writeFileSync(path.join(archiveRoot, "package.json"), "{}\n");
    fs.writeFileSync(path.join(archiveRoot, "extensions", "engram-memory.mjs"), "export {};\n");
    const validPolicy = fs.readFileSync(path.join(root, "plugin", "engram", "bootstrap-targets.json"), "utf8");
    const duplicatePolicy = validPolicy.replace('"schema_version": 1,', '"schema_version": 1,\n  "\\u0073chema_version": 1,');
    fs.writeFileSync(path.join(archiveRoot, "bootstrap-targets.json"), duplicatePolicy);
    const archive = path.join(temp, "hostile-release.tar.gz");
    const archived = spawnSync("tar", ["-czf", archive, "-C", archiveRoot, "."], { encoding: "utf8" });
    assert.equal(archived.status, 0, archived.stderr);
    const fakeCurl = path.join(fakeBin, "curl");
    fs.writeFileSync(fakeCurl, "#!/usr/bin/env bash\nwhile [[ $# -gt 0 ]]; do if [[ $1 == -o ]]; then cp \"$FAKE_RELEASE_ARCHIVE\" \"$2\"; exit 0; fi; shift; done\nexit 1\n", { mode: 0o755 });

    const bashEnvironment = `HOME=${shellQuote(bashPath(home))} PATH=${shellQuote(installerPath(fakeBin))} FAKE_RELEASE_ARCHIVE=${shellQuote(bashPath(archive))}`;
    const result = spawnSync("bash", ["-c", `${bashEnvironment} bash scripts/install.sh v6.47.1`], {
      cwd: root,
      encoding: "utf8",
      env: process.env,
    });
    assert.ifError(result.error);
    assert.notEqual(result.status, 0);
    assert.match(`${result.stdout}\n${result.stderr}`, /invalid bootstrap policy/);
    assert.equal(fs.existsSync(path.join(home, ".claude", "plugins", "marketplaces", "engram")), false);

    const powerShellInstaller = fs.readFileSync(path.join(root, "scripts", "install.ps1"), "utf8");
    assert.doesNotMatch(powerShellInstaller, /node\s+"\$TempDir\\scripts\\bootstrap-policy\.js"/);
    assert.ok(powerShellInstaller.indexOf("$ValidatorScript | & $NodeExecutable") < powerShellInstaller.indexOf("New-Item -ItemType Directory -Path \"$InstallDir\\hooks\""));
    for (const source of [powerShellInstaller, fs.readFileSync(path.join(root, "scripts", "install.sh"), "utf8")]) {
      const semver = installerSemver(source);
      assert.equal(semver.test("6.47.1-rc.1"), true);
      assert.equal(semver.test("6.47.1+build.1"), false);
    }
    assert.match(powerShellInstaller, /Node\.js 18\+/);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("direct installer registers all Claude registries without jq", () => {
  const temp = temporaryDirectory();
  try {
    const { archive, fakeBin } = directInstallerFixture(temp);
    const home = path.join(temp, "home with spaces");
    const registries = path.join(home, ".claude", "plugins");
    fs.mkdirSync(registries, { recursive: true });
    fs.writeFileSync(path.join(registries, "installed_plugins.json"), "\uFEFF{\"version\":2,\"plugins\":{},\"other\":true}\n");
    fs.writeFileSync(path.join(home, ".claude", "settings.json"), "{\"other\":true}\n");
    fs.writeFileSync(path.join(registries, "known_marketplaces.json"), "{\"other\":true}\n");

    const environment = `HOME=${shellQuote(bashPath(home))} PATH=${shellQuote(installerPath(fakeBin))} FAKE_RELEASE_ARCHIVE=${shellQuote(bashPath(archive))} ENGRAM_URL=http://localhost:37777/mcp ENGRAM_API_TOKEN=`;
    const result = spawnSync("bash", ["-c", `printf '\\n\\n' | ${environment} bash scripts/install.sh v6.47.5`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(result.error);
    assert.equal(result.status, 0, result.stderr || result.stdout);
    const output = `${result.stdout}\n${result.stderr}`;
    assert.doesNotMatch(output, /jq|Plugin registration failed/);

    const installRoot = path.join(home, ".claude", "plugins", "marketplaces", "engram");
    const cachePath = path.join(home, ".claude", "plugins", "cache", "engram", "engram", "v6.47.5");
    const installed = JSON.parse(fs.readFileSync(path.join(registries, "installed_plugins.json"), "utf8").replace(/^\uFEFF/, ""));
    const settings = JSON.parse(fs.readFileSync(path.join(home, ".claude", "settings.json"), "utf8").replace(/^\uFEFF/, ""));
    const marketplaces = JSON.parse(fs.readFileSync(path.join(registries, "known_marketplaces.json"), "utf8"));
    assert.equal(installed.other, true);
    assert.equal(installed.plugins["engram@engram"][0].installPath, bashPath(cachePath));
    assert.equal(installed.plugins["engram@engram"][0].version, "6.47.5");
    assert.equal(settings.other, true);
    assert.equal(settings.enabledPlugins["engram@engram"], true);
    assert.equal(settings.statusLine.command, `node "${bashPath(installRoot)}/hooks/statusline.js"`);
    assert.equal(marketplaces.other, true);
    assert.deepEqual(marketplaces.engram.source, { source: "directory", path: bashPath(installRoot) });
    assert.equal(marketplaces.engram.installLocation, bashPath(installRoot));
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});
test("register-only registers an installed plugin without installation side effects", () => {
  const temp = temporaryDirectory();
  try {
    const home = path.join(temp, "home with spaces");
    const installRoot = path.join(home, ".claude", "plugins", "marketplaces", "engram");
    const registries = path.join(home, ".claude", "plugins");
    const pluginsFile = path.join(registries, "installed_plugins.json");
    const settingsFile = path.join(home, ".claude", "settings.json");
    const marketplacesFile = path.join(registries, "known_marketplaces.json");
    const fakeBin = path.join(temp, "bin");
    const curlMarker = path.join(temp, "curl-accessed");
    const version = "6.47.5";
    fs.mkdirSync(fakeBin, { recursive: true });
    fs.mkdirSync(path.join(installRoot, ".claude-plugin"), { recursive: true });
    fs.writeFileSync(path.join(installRoot, ".claude-plugin", "plugin.json"), JSON.stringify({ version }));
    fs.mkdirSync(path.join(installRoot, "hooks"), { recursive: true });
    fs.mkdirSync(path.join(installRoot, "scripts"), { recursive: true });
    fs.copyFileSync(registryHelper, path.join(installRoot, "scripts", "register-plugin.js"));
    fs.writeFileSync(path.join(installRoot, "hooks", "statusline.js"), "module.exports = {};\n");
    fs.writeFileSync(pluginsFile, "\uFEFF{\"version\":2,\"plugins\":{},\"unrelated\":\"installed\"}\n");
    fs.writeFileSync(settingsFile, "{\"unrelated\":\"settings\"}\n");
    fs.writeFileSync(marketplacesFile, "{\"unrelated\":\"marketplaces\"}\n");
    fs.writeFileSync(path.join(fakeBin, "node"), `#!/usr/bin/env bash
node_path=${shellQuote(process.execPath)}
if command -v wslpath >/dev/null 2>&1; then node_path=$(wslpath -u "$node_path")
elif command -v cygpath >/dev/null 2>&1; then node_path=$(cygpath -u "$node_path")
fi
exec "$node_path" "$@"
`, { mode: 0o755 });
    for (const command of ["curl", "tar", "uname"]) fs.writeFileSync(path.join(fakeBin, command), "#!/usr/bin/env bash\nprintf %s \"$0\" > \"$FORBIDDEN_MARKER\"\nexit 1\n", { mode: 0o755 });

    const environment = `HOME=${shellQuote(bashPath(home))} PATH=${shellQuote(installerPath(fakeBin))} FORBIDDEN_MARKER=${shellQuote(bashPath(curlMarker))}`;
    const result = spawnSync("bash", ["-c", `${environment} bash scripts/install.sh --register-only`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(result.error);
    assert.equal(result.status, 0, result.stderr || result.stdout);
    const output = `${result.stdout}\n${result.stderr}`;
    for (const message of ["Plugin registered in installed_plugins.json", "Plugin enabled in settings.json", "Statusline configured in settings.json", "Marketplace registered in known_marketplaces.json"]) assert.match(output, new RegExp(message.replaceAll(".", "\\.")));
    assert.equal(fs.existsSync(curlMarker), false);
    assert.doesNotMatch(output, /Downloading|Extracting|Detected platform|connection|health|Installation Complete/i);

    const cachePath = path.join(home, ".claude", "plugins", "cache", "engram", "engram", `v${version}`);
    const installed = JSON.parse(fs.readFileSync(pluginsFile, "utf8").replace(/^\uFEFF/, ""));
    const settings = JSON.parse(fs.readFileSync(settingsFile, "utf8"));
    const marketplaces = JSON.parse(fs.readFileSync(marketplacesFile, "utf8"));
    assert.equal(installed.plugins["engram@engram"].length, 1);
    const registration = installed.plugins["engram@engram"][0];
    assert.deepEqual(Object.keys(registration).sort(), ["installPath", "installedAt", "isLocal", "lastUpdated", "scope", "version"]);
    assert.equal(registration.installPath, bashPath(cachePath));
    assert.equal(registration.version, version);
    assert.equal(registration.scope, "user");
    assert.equal(registration.isLocal, true);
    assert.equal(registration.installedAt, registration.lastUpdated);
    assert.equal(settings.unrelated, "settings");
    assert.equal(settings.enabledPlugins["engram@engram"], true);
    assert.deepEqual(settings.statusLine, { type: "command", command: `node "${bashPath(installRoot)}/hooks/statusline.js"`, padding: 0 });
    assert.equal(marketplaces.unrelated, "marketplaces");
    assert.deepEqual(Object.keys(marketplaces.engram).sort(), ["installLocation", "lastUpdated", "source"]);
    assert.deepEqual(marketplaces.engram.source, { source: "directory", path: bashPath(installRoot) });
    assert.equal(marketplaces.engram.installLocation, bashPath(installRoot));

    const beforePlugins = fs.readFileSync(pluginsFile);
    const beforeMarketplaces = fs.readFileSync(marketplacesFile);
    const malformedSettings = Buffer.from("{malformed settings}\n");
    fs.writeFileSync(settingsFile, malformedSettings);
    const rejected = spawnSync("bash", ["-c", `${environment} bash scripts/install.sh --register-only`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(rejected.error);
    assert.notEqual(rejected.status, 0);
    const rejectedOutput = `${rejected.stdout}\n${rejected.stderr}`;
    assert.match(rejectedOutput, /SyntaxError: .*JSON/);
    assert.doesNotMatch(rejectedOutput, /Plugin registered in installed_plugins\.json|Plugin enabled in settings\.json|Statusline configured in settings\.json|Marketplace registered in known_marketplaces\.json/);
    assert.equal(fs.existsSync(curlMarker), false);
    assert.deepEqual(fs.readFileSync(pluginsFile), beforePlugins);
    assert.deepEqual(fs.readFileSync(marketplacesFile), beforeMarketplaces);
    assert.deepEqual(fs.readFileSync(settingsFile), malformedSettings);
    assert.deepEqual(registrationArtifacts(home), []);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("direct installer stops when an existing registry is malformed", () => {
  const temp = temporaryDirectory();
  try {
    const { archive, fakeBin } = directInstallerFixture(temp);
    const home = path.join(temp, "home");
    const registries = path.join(home, ".claude", "plugins");
    fs.mkdirSync(registries, { recursive: true });
    fs.writeFileSync(path.join(registries, "installed_plugins.json"), "{not json}\n");
    const environment = `HOME=${shellQuote(bashPath(home))} PATH=${shellQuote(installerPath(fakeBin))} FAKE_RELEASE_ARCHIVE=${shellQuote(bashPath(archive))}`;
    const result = spawnSync("bash", ["-c", `${environment} bash scripts/install.sh v6.47.5`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(result.error);
    assert.notEqual(result.status, 0);
    assert.doesNotMatch(`${result.stdout}\n${result.stderr}`, /Plugin registered successfully|Installation Complete/);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});


test("direct installer restores every registry after a registration rename failure", () => {
  const temp = temporaryDirectory();
  try {
    const { archive, fakeBin } = directInstallerFixture(temp);
    const home = path.join(temp, "home");
    const registries = path.join(home, ".claude", "plugins");
    fs.mkdirSync(registries, { recursive: true });
    const pluginsFile = path.join(registries, "installed_plugins.json");
    const settingsFile = path.join(home, ".claude", "settings.json");
    const marketplacesFile = path.join(registries, "known_marketplaces.json");
    fs.writeFileSync(pluginsFile, "\uFEFF{\"plugins\":{},\"keep\":\"plugins\"}\n");
    fs.writeFileSync(settingsFile, "{\"keep\":\"settings\"}\n");
    fs.writeFileSync(marketplacesFile, "{\"keep\":\"marketplaces\"}\n");
    const files = [pluginsFile, settingsFile, marketplacesFile];
    const before = files.map((file) => fs.readFileSync(file));
    const preload = renameFailurePreload(temp);
    const environment = `HOME=${shellQuote(bashPath(home))} PATH=${shellQuote(installerPath(fakeBin))} FAKE_RELEASE_ARCHIVE=${shellQuote(bashPath(archive))} NODE_OPTIONS=${shellQuote(`--require=./${bashPath(preload)}`)}`;
    const result = spawnSync("bash", ["-c", `${environment} bash scripts/install.sh v6.47.5`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(result.error);
    assert.notEqual(result.status, 0);
    const output = `${result.stdout}\n${result.stderr}`;
    assert.match(output, /injected second registration-stage link failure/);
    assert.doesNotMatch(output, /Plugin registered successfully|Installation Complete/);
    assert.deepEqual(files.map((file) => fs.readFileSync(file)), before);
    assert.deepEqual(registrationArtifacts(home), []);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});
test("direct installer preserves a foreign target after an absent-target registration rename race", () => {
  const temp = temporaryDirectory();
  try {
    const { archive, fakeBin } = directInstallerFixture(temp);
    const home = path.join(temp, "home");
    const registries = path.join(home, ".claude", "plugins");
    const pluginsFile = path.join(registries, "installed_plugins.json");
    const settingsFile = path.join(home, ".claude", "settings.json");
    const marketplacesFile = path.join(registries, "known_marketplaces.json");
    const files = [pluginsFile, settingsFile, marketplacesFile];
    assert.deepEqual(files.map((file) => fs.existsSync(file)), [false, false, false]);
    const preload = absentTargetRacePreload(temp);
    const environment = `HOME=${shellQuote(bashPath(home))} PATH=${shellQuote(installerPath(fakeBin))} FAKE_RELEASE_ARCHIVE=${shellQuote(bashPath(archive))} NODE_OPTIONS=${shellQuote(`--require=./${bashPath(preload)}`)}`;
    const result = spawnSync("bash", ["-c", `${environment} bash scripts/install.sh v6.47.5`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(result.error);
    assert.notEqual(result.status, 0);
    const output = `${result.stdout}\n${result.stderr}`;
    assert.match(output, /injected absent-target registration link race/);
    assert.doesNotMatch(output, /Plugin registered in installed_plugins\.json|Plugin enabled in settings\.json|Statusline configured in settings\.json|Marketplace registered in known_marketplaces\.json|Installation Complete/);
    assert.deepEqual(fs.readFileSync(pluginsFile), absentTargetRaceSentinel);
    assert.equal(fs.existsSync(settingsFile), false);
    assert.equal(fs.existsSync(marketplacesFile), false);
    assert.equal(fs.existsSync(registryJournal(home)), true);
    assert.ok(registrationArtifacts(home).length > 0);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});
test("direct installer retains an orphan backup when post-commit cleanup fails", () => {
  const temp = temporaryDirectory();
  try {
    const { archive, fakeBin } = directInstallerFixture(temp);
    const home = path.join(temp, "home");
    const registries = path.join(home, ".claude", "plugins");
    fs.mkdirSync(registries, { recursive: true });
    const pluginsFile = path.join(registries, "installed_plugins.json");
    const settingsFile = path.join(home, ".claude", "settings.json");
    const marketplacesFile = path.join(registries, "known_marketplaces.json");
    const files = [pluginsFile, settingsFile, marketplacesFile];
    fs.writeFileSync(pluginsFile, "\uFEFF{\"plugins\":{},\"keep\":\"plugins\"}\n");
    fs.writeFileSync(settingsFile, "{\"keep\":\"settings\"}\n");
    fs.writeFileSync(marketplacesFile, "{\"keep\":\"marketplaces\"}\n");
    const before = files.map((file) => fs.readFileSync(file));
    const failureLog = path.join(temp, "backup-unlink-failures");
    const preload = backupCleanupFailurePreload(temp);
    const environment = `HOME=${shellQuote(bashPath(home))} PATH=${shellQuote(installerPath(fakeBin))} FAKE_RELEASE_ARCHIVE=${shellQuote(bashPath(archive))} NODE_OPTIONS=${shellQuote(`--require=./${bashPath(preload)}`)} ENGRAM_TEST_BACKUP_UNLINK_LOG=${shellQuote(bashPath(failureLog))}`;
    const result = spawnSync("bash", ["-c", `printf '\\n\\n' | ${environment} bash scripts/install.sh v6.47.5`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(result.error);
    assert.equal(result.status, 0, result.stderr || result.stdout);
    const output = `${result.stdout}\n${result.stderr}`;
    assert.match(output, /injected first registration backup unlink failure/);
    assert.match(output, /WARNING: Retained orphan registration backups: .*injected first registration backup unlink failure/);
    assert.match(output, /Plugin registered successfully/);
    assert.match(output, /Installation Complete/);

    const installRoot = path.join(home, ".claude", "plugins", "marketplaces", "engram");
    const cachePath = path.join(home, ".claude", "plugins", "cache", "engram", "engram", "v6.47.5");
    const installed = JSON.parse(fs.readFileSync(pluginsFile, "utf8").replace(/^\uFEFF/, ""));
    const settings = JSON.parse(fs.readFileSync(settingsFile, "utf8"));
    const marketplaces = JSON.parse(fs.readFileSync(marketplacesFile, "utf8"));
    const registration = installed.plugins["engram@engram"][0];
    assert.equal(registration.scope, "user");
    assert.equal(registration.installPath, bashPath(cachePath));
    assert.equal(registration.version, "6.47.5");
    assert.equal(settings.enabledPlugins["engram@engram"], true);
    assert.deepEqual(settings.statusLine, { type: "command", command: `node "${bashPath(installRoot)}/hooks/statusline.js"`, padding: 0 });
    assert.deepEqual(marketplaces.engram.source, { source: "directory", path: bashPath(installRoot) });
    assert.equal(marketplaces.engram.installLocation, bashPath(installRoot));
    assert.equal(marketplaces.engram.lastUpdated, registration.lastUpdated);
    assert.equal(registration.installedAt, registration.lastUpdated);
    assert.equal(fs.existsSync(registryJournal(home)), true);

    const artifacts = registrationArtifacts(home);
    const backups = artifacts.filter(registrationBackup);
    assert.equal(backups.length, 1);
    assert.ok(backups[0].startsWith(`${pluginsFile}.backup-`));
    assert.deepEqual(fs.readFileSync(backups[0]), before[0]);
    assert.ok(output.includes(bashPath(backups[0])));
    assert.deepEqual(artifacts.filter(registrationStaged), []);
    assert.match(fs.readFileSync(failureLog, "utf8").trimEnd().replaceAll("\\", "/"), new RegExp(`${path.basename(backups[0]).replaceAll(".", "\\.")}$`));
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});
test("direct installer removes owned staging after a registration partial write failure", () => {
  const temp = temporaryDirectory();
  try {
    const { archive, fakeBin } = directInstallerFixture(temp);
    const home = path.join(temp, "home");
    const registries = path.join(home, ".claude", "plugins");
    fs.mkdirSync(registries, { recursive: true });
    const pluginsFile = path.join(registries, "installed_plugins.json");
    const settingsFile = path.join(home, ".claude", "settings.json");
    const marketplacesFile = path.join(registries, "known_marketplaces.json");
    const files = [pluginsFile, settingsFile, marketplacesFile];
    fs.writeFileSync(pluginsFile, "{\"plugins\":{},\"keep\":1}\n");
    fs.writeFileSync(settingsFile, "{\"keep\":2}\n");
    fs.writeFileSync(marketplacesFile, "{\"keep\":3}\n");
    const before = files.map((file) => fs.readFileSync(file));
    const sentinelFile = `${settingsFile}.tmp`;
    const sentinel = Buffer.from("foreign legacy settings temp sentinel\n");
    fs.writeFileSync(sentinelFile, sentinel);
    const preload = partialWriteFailurePreload(temp);
    const environment = `HOME=${shellQuote(bashPath(home))} PATH=${shellQuote(installerPath(fakeBin))} FAKE_RELEASE_ARCHIVE=${shellQuote(bashPath(archive))} NODE_OPTIONS=${shellQuote(`--require=./${bashPath(preload)}`)}`;
    const result = spawnSync("bash", ["-c", `${environment} bash scripts/install.sh v6.47.5`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(result.error);
    assert.notEqual(result.status, 0);
    const output = `${result.stdout}\n${result.stderr}`;
    assert.match(output, /injected second registration partial-write failure/);
    assert.doesNotMatch(output, /Plugin registered|Plugin enabled|Statusline configured|Marketplace registered|Installation Complete/);
    assert.deepEqual(files.map((file) => fs.readFileSync(file)), before);
    assert.deepEqual(fs.readFileSync(sentinelFile), sentinel);
    assert.deepEqual(registrationArtifacts(home), []);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("release readback retries until the private draft is visible", () => {
  const temp = temporaryDirectory();
  try {
    const fakeBin = path.join(temp, "bin");
    const count = path.join(temp, "curl-count");
    const first = path.join(temp, "first.json");
    const second = path.join(temp, "second.json");
    const sleeps = path.join(temp, "sleeps");
    const release = path.join(temp, "release.json");
    const invocations = path.join(temp, "curl-invocations");
    fs.mkdirSync(fakeBin, { recursive: true });
    fs.writeFileSync(path.join(fakeBin, "node"), `#!/usr/bin/env bash
node_path=${shellQuote(process.execPath)}
if command -v wslpath >/dev/null 2>&1; then node_path=$(wslpath -u "$node_path")
elif command -v cygpath >/dev/null 2>&1; then node_path=$(cygpath -u "$node_path")
fi
exec "$node_path" "$@"
`, { mode: 0o755 });
    fs.writeFileSync(path.join(fakeBin, "mktemp"), "#!/usr/bin/env bash\nprintf '%s\\n' \"$FAKE_MKTEMP_PATH\"\n", { mode: 0o755 });
    const assertCurlInvocations = () => {
      const calls = fs.readFileSync(invocations, "utf8").trimEnd().split("\n");
      assert.equal(calls.length, Number(fs.readFileSync(count, "utf8")));
      assert.ok(calls.every((call) => /(?:^| )--max-time 5(?: |$)/.test(call)));
    };
    const policyJSON = fs.readFileSync(path.join(root, "plugin", "engram", "bootstrap-targets.json"), "utf8");
    const policy = parsePolicy(policyJSON);
    const policyVersion = policy.package_version;
    const tagName = `v${policyVersion}`;
    fs.writeFileSync(first, "[]");
    fs.writeFileSync(second, JSON.stringify([{
      id: 368199776,
      tag_name: tagName,
      draft: true,
      assets: Object.values(policy.targets).map(({ desired }) => ({
        name: desired.asset,
        state: "uploaded",
        size: desired.size,
        digest: `sha256:${desired.sha256}`,
      })),
    }]));
    fs.writeFileSync(path.join(fakeBin, "curl"), "#!/usr/bin/env bash\ncount=0\n[[ -f $FAKE_CURL_COUNT ]] && count=$(cat \"$FAKE_CURL_COUNT\")\ncount=$((count + 1))\nprintf '%s' \"$count\" > \"$FAKE_CURL_COUNT\"\nprintf '%s\\n' \"$*\" >> \"$FAKE_CURL_INVOCATIONS\"\nif [[ ${FAKE_CURL_ALWAYS_FAIL:-} == 1 ]] || { (( count == 1 )) && [[ ${FAKE_CURL_FAIL_FIRST:-} == 1 ]]; }; then echo \"transient release list failure $count\" >&2; exit 1; fi\nif (( count == 1 )); then cat \"$FAKE_RELEASE_FIRST\"; else cat \"$FAKE_RELEASE_SECOND\"; fi\n", { mode: 0o755 });
    fs.writeFileSync(path.join(fakeBin, "sleep"), "#!/usr/bin/env bash\nprintf '%s\\n' \"$1\" >> \"$FAKE_SLEEP_LOG\"\n", { mode: 0o755 });
    const environment = `PATH=${shellQuote(installerPath(fakeBin))} GITHUB_TOKEN=fake FAKE_MKTEMP_PATH=${shellQuote(bashPath(release))} FAKE_CURL_COUNT=${shellQuote(bashPath(count))} FAKE_CURL_INVOCATIONS=${shellQuote(bashPath(invocations))} FAKE_RELEASE_FIRST=${shellQuote(bashPath(first))} FAKE_RELEASE_SECOND=${shellQuote(bashPath(second))} FAKE_SLEEP_LOG=${shellQuote(bashPath(sleeps))}`;
    const result = spawnSync("bash", ["-c", `${environment} bash scripts/readback-bootstrap-policy-assets.sh --tag ${tagName}`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(result.error);
    assert.equal(result.status, 0, result.stderr || result.stdout);
    assert.equal(fs.readFileSync(count, "utf8"), "2");
    assert.equal(fs.readFileSync(sleeps, "utf8"), "10\n");
    assertCurlInvocations();
    fs.rmSync(count);
    fs.rmSync(sleeps);
    fs.rmSync(invocations);
    const recovered = spawnSync("bash", ["-c", `${environment} FAKE_CURL_FAIL_FIRST=1 bash scripts/readback-bootstrap-policy-assets.sh --tag ${tagName}`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(recovered.error);
    assert.equal(recovered.status, 0, recovered.stderr || recovered.stdout);
    assert.equal(fs.readFileSync(count, "utf8"), "2");
    assert.equal(fs.readFileSync(sleeps, "utf8"), "10\n");
    assertCurlInvocations();
    fs.rmSync(count);
    fs.rmSync(sleeps);
    fs.rmSync(invocations);
    const rejected = spawnSync("bash", ["-c", `${environment} FAKE_RELEASE_SECOND=${shellQuote(bashPath(first))} bash scripts/readback-bootstrap-policy-assets.sh --tag ${tagName}`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(rejected.error);
    assert.notEqual(rejected.status, 0);
    assert.equal(fs.readFileSync(count, "utf8"), "5");
    assert.equal(fs.readFileSync(sleeps, "utf8"), "10\n10\n10\n10\n");
    assert.match(rejected.stderr, /expected exactly one private draft release/);
    assertCurlInvocations();
    fs.rmSync(count);
    fs.rmSync(sleeps);
    fs.rmSync(invocations);
    const transportRejected = spawnSync("bash", ["-c", `${environment} FAKE_CURL_ALWAYS_FAIL=1 bash scripts/readback-bootstrap-policy-assets.sh --tag ${tagName}`], { cwd: root, encoding: "utf8", env: process.env });
    assert.ifError(transportRejected.error);
    assert.notEqual(transportRejected.status, 0);
    assert.equal(fs.readFileSync(count, "utf8"), "5");
    assert.equal(fs.readFileSync(sleeps, "utf8"), "10\n10\n10\n10\n");
    assert.match(transportRejected.stderr, /transient release list failure 5/);
    assertCurlInvocations();
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});
