#!/usr/bin/env node
"use strict";

const { BootstrapError, MAX_OBJECT_BYTES, TARGET_ASSETS, canonicalVersion, parsePolicy: parseSharedPolicy } = require("./bootstrap-policy.js");
const crypto = require("node:crypto");
const fs = require("node:fs");
const https = require("node:https");
const path = require("node:path");

const REPO = "thebtf/engram";
const MAX_REDIRECTS = 5;
const DOWNLOAD_DEADLINE_MS = 120_000;
const RELEASE_HOSTS = new Set([
  "github.com",
  "objects.githubusercontent.com",
  "github-releases.githubusercontent.com",
  "release-assets.githubusercontent.com",
]);

function fail(message) {
  throw new BootstrapError(message);
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

function hashFile(filePath, target, root) {
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
      const bytes = fs.readSync(fd, buffer, 0, buffer.length, null);
      if (!bytes) break;
      hash.update(buffer.subarray(0, bytes));
    }
  } finally { fs.closeSync(fd); }
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

function verifyObject(roots, target, quarantine = true) {
  const candidate = objectPath(roots, target);
  try {
    if (hashFile(candidate, target, roots.objects)) return candidate;
    if (fs.existsSync(candidate) && quarantine) quarantineObject(candidate, roots, target);
    return "";
  } catch (error) {
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

function writeVerified(fd, source, target, deadlineMs = DOWNLOAD_DEADLINE_MS) {
  const hash = crypto.createHash("sha256");
  let size = 0;
  return new Promise((resolve, reject) => {
    const deadline = setTimeout(() => source.destroy(new BootstrapError("download deadline exceeded")), deadlineMs);
    const finish = (callback) => (value) => { clearTimeout(deadline); callback(value); };
    source.on("data", (chunk) => {
      size += chunk.length;
      if (size > target.size) {
        source.destroy(new BootstrapError("download exceeds expected size"));
        return;
      }
      hash.update(chunk);
      try { fs.writeSync(fd, chunk); } catch (error) { source.destroy(error); }
    });
    source.once("error", finish(reject));
    source.once("end", finish(() => {
      if (size !== target.size) return reject(new BootstrapError("download size differs from policy"));
      const actual = hash.digest("hex");
      if (actual !== target.sha256) return reject(new BootstrapError("download digest differs from policy"));
      resolve();
    }));
  });
}

function requestStream(url, target, request = https.request, redirects = 0) {
  return new Promise((resolve, reject) => {
    let parsed;
    try { parsed = new URL(url); } catch { reject(new BootstrapError("download URL is invalid")); return; }
    if (parsed.protocol !== "https:" || !RELEASE_HOSTS.has(parsed.hostname) || parsed.username || parsed.password) {
      reject(new BootstrapError("download URL violates HTTPS host policy")); return;
    }
    const req = request(parsed, { headers: { "User-Agent": "engram-plugin" }, timeout: 30_000 }, (response) => {
      const status = response.statusCode || 0;
      if (status >= 300 && status < 400) {
        const location = response.headers.location;
        response.resume();
        if (!location || redirects >= MAX_REDIRECTS) { reject(new BootstrapError("download redirect limit reached")); return; }
        resolve(requestStream(new URL(location, parsed).href, target, request, redirects + 1));
        return;
      }
      if (status !== 200) { response.resume(); reject(new BootstrapError(`download returned HTTP ${status}`)); return; }
      const length = response.headers["content-length"];
      if (length === undefined || !/^\d+$/.test(String(length)) || Number(length) !== target.size) {
        response.resume(); reject(new BootstrapError("download Content-Length differs from policy")); return;
      }
      resolve(response);
    });
    req.once("timeout", () => req.destroy(new BootstrapError("download connection timed out")));
    req.once("error", reject);
    req.end();
  });
}

async function downloadObject(roots, target, options = {}) {
  const stage = uniqueStage(roots);
  try {
    const url = `https://github.com/${REPO}/releases/download/v${target.version}/${target.asset}`;
    const response = await requestStream(url, target, options.request || https.request);
    await writeVerified(stage.fd, response, target);
    fs.closeSync(stage.fd);
    stage.fd = undefined;
    // Defend against write or filesystem corruption after streaming completion.
    if (!hashFile(stage.path, target, roots.staging)) fail("closed staging object failed verification");
    return publishStage(stage.path, roots, target);
  } finally {
    if (stage.fd !== undefined) fs.closeSync(stage.fd);
    try { fs.unlinkSync(stage.path); } catch (error) { if (error.code !== "ENOENT") throw error; }
  }
}

function publishStage(stage, roots, target) {
  const finalPath = objectPath(roots, target);
  try {
    // link is create-only: unlike rename it can never replace a concurrent winner.
    fs.linkSync(stage, finalPath);
    if (process.platform !== "win32") fs.chmodSync(finalPath, 0o755);
  } catch (error) {
    if (error.code !== "EEXIST") throw new BootstrapError(`could not publish object: ${error.message}`);
  }
  const verified = verifyObject(roots, target);
  if (!verified) fail("published object did not pass independent verification");
  return verified;
}

function importLegacy(roots, target) {
  const legacy = path.join(roots.bin, process.platform === "win32" ? "engram.exe" : "engram");
  let stat;
  try { stat = fs.lstatSync(legacy); } catch (error) { if (error.code === "ENOENT") return ""; throw error; }
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size !== target.size) return "";
  // Authenticate bytes before creating an object. Never execute or mutate the legacy file.
  if (!hashFile(legacy, target, roots.bin)) return "";
  const stage = uniqueStage(roots);
  try {
    let offset = 0;
    const source = fs.openSync(legacy, "r");
    try {
      const buffer = Buffer.allocUnsafe(64 * 1024);
      for (; ;) {
        const bytes = fs.readSync(source, buffer, 0, buffer.length, null);
        if (!bytes) break;
        fs.writeSync(stage.fd, buffer, 0, bytes, offset);
        offset += bytes;
      }
    } finally { fs.closeSync(source); }
    fs.closeSync(stage.fd); stage.fd = undefined;
    if (!hashFile(stage.path, target, roots.staging)) return "";
    return publishStage(stage.path, roots, target);
  } finally {
    if (stage.fd !== undefined) fs.closeSync(stage.fd);
    try { fs.unlinkSync(stage.path); } catch { }
  }
}

async function acquire(roots, target, options) {
  return verifyObject(roots, target) || importLegacy(roots, target) || await downloadObject(roots, target, options);
}

async function resolveForLaunch(options = {}) {
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
      // Result authority exists only after this final, independent verification.
      if (!hashFile(object, target, roots.objects)) fail("final object verification failed");
      return Object.freeze({ path: object, target });
    } catch (error) { lastError = error; }
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
  hashFile, importLegacy, loadPolicy, objectPath, objectRoots, parsePolicy,
  platformKey, prefetch, publishStage, requestStream, resolveForLaunch, verifyObject,
  downloadObject,
};
