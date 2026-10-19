#!/usr/bin/env node
"use strict";

const { BootstrapError, MAX_OBJECT_BYTES, TARGET_ASSETS, canonicalVersion, parsePolicy: parseSharedPolicy } = require("./bootstrap-policy.js");
const crypto = require("node:crypto");
const fs = require("node:fs");
const https = require("node:https");
const path = require("node:path");

const REPO = "thebtf/engram";
const MAX_REDIRECTS = 5;
const DOWNLOAD_DEADLINE_MS = 360_000;
const RELEASE_HOSTS = new Set([
  "github.com",
  "objects.githubusercontent.com",
  "github-releases.githubusercontent.com",
  "release-assets.githubusercontent.com",
]);

function fail(message) {
  throw new BootstrapError(message);
}

function checkAcquisition(options = {}) {
  if (options.signal?.aborted) fail("client acquisition aborted");
  if (options.deadlineAt !== undefined && Date.now() >= options.deadlineAt) fail("client acquisition deadline exceeded");
}

function platformKey(platform = process.platform, arch = process.arch) {
  const key = `${platform}-${arch}`;
  if (!Object.hasOwn(TARGET_ASSETS, key)) fail(`unsupported platform ${platform}/${arch}`);
  return key;
}

function parsePolicy(text, packageVersion, key) {
  const policy = parseSharedPolicy(text, packageVersion);
  const wantedKey = key || platformKey();
  if (!Object.hasOwn(policy.targets, wantedKey)) fail(`bootstrap policy has no target for ${wantedKey}`);
  return Object.freeze({ ...policy, target: policy.targets[wantedKey], platform: wantedKey });
}

function readActivePackageVersion(pluginRoot) {
  const candidates = [
    path.join(pluginRoot, ".omp-plugin", "plugin.json"),
    path.join(pluginRoot, ".codex-plugin", "plugin.json"),
    path.join(pluginRoot, ".claude-plugin", "plugin.json"),
  ];
  for (const candidate of candidates) {
    try {
      const manifest = JSON.parse(fs.readFileSync(candidate, "utf8"));
      return canonicalVersion(manifest.version, "active plugin version");
    } catch (error) {
      if (error.code !== "ENOENT") fail(`could not read active plugin manifest: ${error.message}`);
    }
  }
  fail("active plugin manifest is missing");
}

function loadPolicy(pluginRoot, options = {}) {
  const packageVersion = options.packageVersion || readActivePackageVersion(pluginRoot);
  const policyPath = path.join(pluginRoot, "bootstrap-targets.json");
  let text;
  try { text = fs.readFileSync(policyPath, "utf8"); } catch (error) { fail(`could not read bootstrap policy: ${error.message}`); }
  return parsePolicy(text, packageVersion, options.platformKey || platformKey(options.platform, options.arch));
}

function loadParserTarget(pluginRoot, packageVersion, key) {
  let policy;
  try { policy = JSON.parse(fs.readFileSync(path.join(pluginRoot, "parser-targets.json"), "utf8")); }
  catch (error) { fail(`could not read parser policy: ${error.message}`); }
  if (policy.schema_version !== 1 || policy.package_version !== packageVersion ||
    Object.keys(policy).sort().join() !== "package_version,schema_version,targets" ||
    !policy.targets || Object.keys(policy.targets).sort().join() !== "darwin-arm64,linux-x64,win32-x64") {
    fail("parser policy does not match installed client package");
  }
  const target = policy.targets[key];
  if (target === null && key !== "win32-x64") return null;
  if (!target || Object.keys(target).sort().join() !== "asset,sha256,size,version" ||
    target.version !== packageVersion || target.asset !== "uci-parser-windows-amd64.exe" ||
    !Number.isSafeInteger(target.size) || target.size < 1 || target.size > MAX_OBJECT_BYTES ||
    !/^[a-f0-9]{64}$/.test(target.sha256)) fail("parser target is invalid");
  return Object.freeze(target);
}

async function installParser(roots, clientPath, target, options = {}) {
  options = { ...options, deadlineAt: options.deadlineAt ?? (options.timeoutMs === undefined ? undefined : Date.now() + options.timeoutMs) };
  const object = await acquire(roots, target, options);
  if (!hashFile(object, target, roots.objects, options)) fail("parser object failed final verification");
  const directory = assertSafeDirectory(path.join(path.dirname(clientPath), "parser"));
  const parserPath = path.join(directory, `parser${path.extname(clientPath)}`);
  checkAcquisition(options);
  try { fs.linkSync(object, parserPath); }
  catch (error) { if (error.code !== "EEXIST") throw error; }
  if (!hashFile(parserPath, target, roots.objects, options)) fail("installed parser sibling failed verification");
  return parserPath;
}

function isInside(root, candidate) {
  const relative = path.relative(root, candidate);
  return relative === "" || (!relative.startsWith(`..${path.sep}`) && relative !== ".." && !path.isAbsolute(relative));
}

function assertSafeDirectory(root) {
  const absolute = path.resolve(root);
  const parsed = path.parse(absolute);
  let current = parsed.root;
  for (const part of absolute.slice(parsed.root.length).split(path.sep).filter(Boolean)) {
    current = path.join(current, part);
    let stat;
    try { stat = fs.lstatSync(current); } catch (error) {
      if (error.code !== "ENOENT") fail(`cannot inspect ${current}: ${error.message}`);
      fs.mkdirSync(current);
      stat = fs.lstatSync(current);
    }
    if (stat.isSymbolicLink() || !stat.isDirectory()) fail(`unsafe directory component ${current}`);
  }
  return fs.realpathSync.native(absolute);
}

function objectRoots(pluginData) {
  const bin = assertSafeDirectory(path.join(pluginData, "bin"));
  const objects = assertSafeDirectory(path.join(bin, "objects", "sha256"));
  const staging = assertSafeDirectory(path.join(bin, "staging"));
  const quarantine = assertSafeDirectory(path.join(bin, "quarantine"));
  return { bin, objects, staging, quarantine };
}

function objectPath(roots, target) {
  const digestDir = path.join(roots.objects, target.sha256);
  assertSafeDirectory(digestDir);
  const candidate = path.join(digestDir, target.asset);
  if (!isInside(roots.objects, candidate)) fail("object path escapes object store");
  return candidate;
}

function hashFile(filePath, target, root, options = {}) {
  checkAcquisition(options);
  let stat;
  try { stat = fs.lstatSync(filePath); } catch (error) { if (error.code === "ENOENT") return false; throw error; }
  if (!stat.isFile() || stat.isSymbolicLink()) fail(`object is not a regular file: ${filePath}`);
  const real = fs.realpathSync.native(filePath);
  if (!isInside(root, real)) fail(`object escapes object store: ${filePath}`);
  if (stat.size !== target.size) return false;
  const hash = crypto.createHash("sha256");
  const fd = fs.openSync(filePath, "r");
  try {
    const buffer = Buffer.allocUnsafe(64 * 1024);
    for (; ;) {
      checkAcquisition(options);
      const bytes = fs.readSync(fd, buffer, 0, buffer.length, null);
      if (!bytes) break;
      hash.update(buffer.subarray(0, bytes));
    }
  } finally { fs.closeSync(fd); }
  checkAcquisition(options);
  return crypto.timingSafeEqual(Buffer.from(hash.digest("hex")), Buffer.from(target.sha256));
}

function quarantineObject(filePath, roots, target) {
  try {
    const parent = path.dirname(filePath);
    if (!isInside(roots.objects, parent) || fs.lstatSync(parent).isSymbolicLink()) return false;
    const destinationDir = path.join(roots.quarantine, `${Date.now()}-${target.sha256}-${crypto.randomBytes(8).toString("hex")}`);
    fs.mkdirSync(destinationDir, { recursive: false });
    fs.renameSync(filePath, path.join(destinationDir, target.asset));
    return true;
  } catch { return false; }
}

function verifyObject(roots, target, quarantine = true, options = {}) {
  checkAcquisition(options);
  const candidate = objectPath(roots, target);
  try {
    if (hashFile(candidate, target, roots.objects, options)) return candidate;
    if (fs.existsSync(candidate) && quarantine) quarantineObject(candidate, roots, target);
    return "";
  } catch (error) {
    checkAcquisition(options);
    if (quarantine) quarantineObject(candidate, roots, target);
    if (error instanceof BootstrapError) throw error;
    throw new BootstrapError(`cannot verify object: ${error.message}`);
  }
}

function uniqueStage(roots) {
  for (let attempt = 0; attempt < 16; attempt += 1) {
    const stage = path.join(roots.staging, `${process.pid}-${crypto.randomBytes(16).toString("hex")}.part`);
    try { return { path: stage, fd: fs.openSync(stage, "wx", 0o600) }; } catch (error) { if (error.code !== "EEXIST") throw error; }
  }
  fail("could not allocate exclusive staging file");
}

function writeVerified(fd, source, target, options = {}) {
  const hash = crypto.createHash("sha256");
  let size = 0;
  return new Promise((resolve, reject) => {
    const remaining = options.deadlineAt === undefined ? DOWNLOAD_DEADLINE_MS : options.deadlineAt - Date.now();
    const deadline = setTimeout(() => source.destroy(new BootstrapError("download deadline exceeded")), Math.max(0, Math.min(DOWNLOAD_DEADLINE_MS, remaining)));
    const onAbort = () => source.destroy(new BootstrapError("client acquisition aborted"));
    const finish = (callback) => (value) => {
      clearTimeout(deadline);
      options.signal?.removeEventListener("abort", onAbort);
      callback(value);
    };
    source.on("data", (chunk) => {
      try {
        checkAcquisition(options);
        size += chunk.length;
        if (size > target.size) throw new BootstrapError("download exceeds expected size");
        hash.update(chunk);
        fs.writeSync(fd, chunk);
      } catch (error) { source.destroy(error); }
    });
    source.once("error", finish(reject));
    source.once("end", finish(() => {
      try {
        checkAcquisition(options);
        if (size !== target.size) throw new BootstrapError("download size differs from policy");
        if (hash.digest("hex") !== target.sha256) throw new BootstrapError("download digest differs from policy");
        resolve();
      } catch (error) { reject(error); }
    }));
    options.signal?.addEventListener("abort", onAbort, { once: true });
    if (options.signal?.aborted) onAbort();
  });
}

function requestStream(url, target, request = https.request, redirects = 0, options = {}) {
  return new Promise((resolve, reject) => {
    checkAcquisition(options);
    let parsed;
    try { parsed = new URL(url); } catch { reject(new BootstrapError("download URL is invalid")); return; }
    if (parsed.protocol !== "https:" || !RELEASE_HOSTS.has(parsed.hostname) || parsed.username || parsed.password) {
      reject(new BootstrapError("download URL violates HTTPS host policy")); return;
    }
    const req = request(parsed, { headers: { "User-Agent": "engram-plugin" }, timeout: 30_000, signal: options.signal }, (response) => {
      try { checkAcquisition(options); }
      catch (error) { response.destroy(); reject(error); return; }
      const status = response.statusCode || 0;
      if (status >= 300 && status < 400) {
        const location = response.headers.location;
        response.destroy();
        if (!location || redirects >= MAX_REDIRECTS) { reject(new BootstrapError("download redirect limit reached")); return; }
        resolve(requestStream(new URL(location, parsed).href, target, request, redirects + 1, options));
        return;
      }
      if (status !== 200) { response.destroy(); reject(new BootstrapError(`download returned HTTP ${status}`)); return; }
      const length = response.headers["content-length"];
      if (length === undefined || !/^\d+$/.test(String(length)) || Number(length) !== target.size) {
        response.destroy(); reject(new BootstrapError("download Content-Length differs from policy")); return;
      }
      resolve(response);
    });
    const deadline = options.deadlineAt === undefined ? undefined : setTimeout(
      () => req.destroy(new BootstrapError("client acquisition deadline exceeded")), Math.max(0, options.deadlineAt - Date.now()));
    req.once("close", () => clearTimeout(deadline));
    req.once("timeout", () => req.destroy(new BootstrapError("download connection timed out")));
    req.once("error", (error) => { clearTimeout(deadline); reject(error); });
    req.end();
  });
}

async function downloadObject(roots, target, options = {}) {
  options = { ...options, deadlineAt: options.deadlineAt ?? (options.timeoutMs === undefined ? undefined : Date.now() + options.timeoutMs) };
  checkAcquisition(options);
  const stage = uniqueStage(roots);
  try {
    const url = `https://github.com/${REPO}/releases/download/v${target.version}/${target.asset}`;
    const response = await requestStream(url, target, options.request || https.request, 0, options);
    await writeVerified(stage.fd, response, target, options);
    fs.closeSync(stage.fd);
    stage.fd = undefined;
    // Defend against write or filesystem corruption after streaming completion.
    if (!hashFile(stage.path, target, roots.staging, options)) fail("closed staging object failed verification");
    return publishStage(stage.path, roots, target, options);
  } finally {
    if (stage.fd !== undefined) fs.closeSync(stage.fd);
    try { fs.unlinkSync(stage.path); } catch (error) { if (error.code !== "ENOENT") throw error; }
  }
}

function publishStage(stage, roots, target, options = {}) {
  const finalPath = objectPath(roots, target);
  try {
    // link is create-only: unlike rename it can never replace a concurrent winner.
    checkAcquisition(options);
    fs.linkSync(stage, finalPath);
    if (process.platform !== "win32") fs.chmodSync(finalPath, 0o755);
  } catch (error) {
    if (error.code !== "EEXIST") throw new BootstrapError(`could not publish object: ${error.message}`);
  }
  const verified = verifyObject(roots, target, true, options);
  if (!verified) fail("published object did not pass independent verification");
  return verified;
}

function importLegacy(roots, target, options = {}) {
  checkAcquisition(options);
  const legacy = path.join(roots.bin, process.platform === "win32" ? "engram.exe" : "engram");
  let stat;
  try { stat = fs.lstatSync(legacy); } catch (error) { if (error.code === "ENOENT") return ""; throw error; }
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size !== target.size) return "";
  // Authenticate bytes before creating an object. Never execute or mutate the legacy file.
  if (!hashFile(legacy, target, roots.bin, options)) return "";
  const stage = uniqueStage(roots);
  try {
    let offset = 0;
    const source = fs.openSync(legacy, "r");
    try {
      const buffer = Buffer.allocUnsafe(64 * 1024);
      for (; ;) {
        checkAcquisition(options);
        const bytes = fs.readSync(source, buffer, 0, buffer.length, null);
        if (!bytes) break;
        fs.writeSync(stage.fd, buffer, 0, bytes, offset);
        offset += bytes;
      }
    } finally { fs.closeSync(source); }
    fs.closeSync(stage.fd); stage.fd = undefined;
    if (!hashFile(stage.path, target, roots.staging, options)) return "";
    return publishStage(stage.path, roots, target, options);
  } finally {
    if (stage.fd !== undefined) fs.closeSync(stage.fd);
    try { fs.unlinkSync(stage.path); } catch { }
  }
}

async function acquire(roots, target, options) {
  checkAcquisition(options);
  return verifyObject(roots, target, true, options) || importLegacy(roots, target, options) || await downloadObject(roots, target, options);
}

async function resolveForLaunch(options = {}) {
  options = { ...options, deadlineAt: options.deadlineAt ?? (options.timeoutMs === undefined ? undefined : Date.now() + options.timeoutMs) };
  checkAcquisition(options);
  const pluginRoot = options.pluginRoot || process.env.PLUGIN_ROOT || process.env.CLAUDE_PLUGIN_ROOT;
  const pluginData = options.pluginData || process.env.PLUGIN_DATA || process.env.CLAUDE_PLUGIN_DATA;
  if (!pluginRoot || !pluginData) fail("plugin root and plugin data are required");
  const policy = options.policy || loadPolicy(pluginRoot, options);
  const roots = objectRoots(pluginData);
  const candidates = [policy.target.desired, policy.target.predecessor].filter(Boolean);
  let lastError;
  for (const target of candidates) {
    try {
      const object = await acquire(roots, target, options);
      if (!hashFile(object, target, roots.objects, options)) fail("final object verification failed");
      const parserTarget = options.parserTarget === undefined
        ? loadParserTarget(pluginRoot, policy.package_version, policy.platform)
        : options.parserTarget;
      const parserPath = parserTarget ? await installParser(roots, object, parserTarget, options) : "";
      checkAcquisition(options);
      return Object.freeze({ path: object, target, parserPath, parserTarget });
    } catch (error) { checkAcquisition(options); lastError = error; }
  }
  throw new BootstrapError(`no authorized client object is available: ${lastError ? lastError.message : "none"}`);
}

async function prefetch(options = {}) {
  try {
    const resolved = await resolveForLaunch(options);
    return { ok: true, target: resolved.target, path: resolved.path };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    if (options.log !== false) process.stderr.write(`[engram] bootstrap prefetch warning: ${message}\n`);
    return { ok: false, error: message };
  }
}

async function main() {
  await prefetch();
}

if (require.main === module) main().catch((error) => process.stderr.write(`[engram] bootstrap error: ${error.message}\n`));

module.exports = {
  BootstrapError, MAX_OBJECT_BYTES, MAX_REDIRECTS, TARGET_ASSETS,
  assertSafeDirectory, hashFile, importLegacy, loadPolicy, objectPath, objectRoots, parsePolicy,
  platformKey, prefetch, publishStage, requestStream, resolveForLaunch, verifyObject,
  downloadObject, loadParserTarget, installParser,
};
