"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { verifyReleaseAssets } = require("./verify-bootstrap-release-assets.js");

const repository = "https://github.com/thebtf/engram.git";
const sourcePath = "plugin/engram";
const stableTag = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
const shaPattern = /^[0-9a-f]{40}$/;

function git(root, ...args) {
  return execFileSync("git", args, { cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
}

function versionParts(version) {
  const match = stableTag.exec(`v${version}`);
  if (!match) throw new Error("catalog and release versions must be stable canonical versions");
  return match.slice(1).map(BigInt);
}

function compare(a, b) {
  const left = versionParts(a), right = versionParts(b);
  for (let i = 0; i < left.length; i++) if (left[i] !== right[i]) return left[i] < right[i] ? -1 : 1;
  return 0;
}

function promote({ root, tag, remote = repository, getRelease }) {
  const match = stableTag.exec(tag);
  if (!match) throw new Error("expected a stable canonical tag vX.Y.Z");
  const version = tag.slice(1);
  const catalogPath = path.join(root, ".omp-plugin", "marketplace.json");
  const original = fs.readFileSync(catalogPath, "utf8");
  const catalog = JSON.parse(original);
  const plugins = catalog.plugins?.filter((plugin) => plugin.name === "engram");
  if (!Array.isArray(plugins) || plugins.length !== 1) throw new Error("expected one engram catalog entry");
  const plugin = plugins[0], current = plugin.source;
  if (catalog.version !== plugin.version || !current || current.source !== "git-subdir" || current.url !== "thebtf/engram" || current.path !== sourcePath || current.ref !== `v${plugin.version}` || !shaPattern.test(current.sha)) throw new Error("invalid current direct catalog binding");
  const order = compare(version, plugin.version);
  if (order < 0) throw new Error("refusing catalog rollback");

  const remoteRefs = git(root, "ls-remote", "--tags", remote, `refs/tags/${tag}`, `refs/tags/${tag}^{}`).split(/\r?\n/).filter(Boolean).map((line) => line.split(/\s+/));
  const direct = remoteRefs.filter((entry) => entry[1] === `refs/tags/${tag}`);
  const peeled = remoteRefs.filter((entry) => entry[1] === `refs/tags/${tag}^{}`);
  if (direct.length !== 1 || peeled.length > 1 || !shaPattern.test(direct[0][0]) || (peeled.length && !shaPattern.test(peeled[0][0]))) throw new Error("release tag missing or invalid on remote");
  const ref = `refs/tags/${tag}`;
  const tagObject = git(root, "rev-parse", "--verify", ref);
  const commit = git(root, "rev-parse", "--verify", `${ref}^{commit}`);
  if (tagObject !== direct[0][0] || commit !== (peeled[0]?.[0] || direct[0][0])) throw new Error("local tag does not match remote tag and commit");
  if (order === 0 && commit !== current.sha) throw new Error("refusing to change an existing version's commit");
  git(root, "merge-base", "--is-ancestor", current.sha, commit);
  git(root, "merge-base", "--is-ancestor", commit, "HEAD");
  const readTagged = (file) => git(root, "show", `${commit}:${sourcePath}/${file}`);
  const manifest = JSON.parse(readTagged(".omp-plugin/plugin.json"));
  const packageJson = JSON.parse(readTagged("package.json"));
  if (manifest.version !== version || packageJson.version !== version) throw new Error("tagged plugin version mismatch");
  const policy = readTagged("bootstrap-targets.json");
  const parserPolicy = readTagged("parser-targets.json");
  verifyReleaseAssets(policy, [getRelease(tag)], tag, { published: true, parserPolicy });
  const next = structuredClone(catalog);
  next.version = version;
  const entry = next.plugins.find((item) => item.name === "engram");
  entry.version = version;
  entry.source = { source: "git-subdir", url: "thebtf/engram", path: sourcePath, ref: tag, sha: commit };
  const output = `${JSON.stringify(next, null, 2)}\n`;
  if (output !== original) {
    if (fs.readFileSync(catalogPath, "utf8") !== original) throw new Error("catalog changed during verification");
    fs.writeFileSync(catalogPath, output);
  }
  return { tag, commit, changed: output !== original };
}

function main(args) {
  if (args.length !== 1) throw new Error("usage: node scripts/promote-omp-catalog.js vX.Y.Z");
  const root = git(process.cwd(), "rev-parse", "--show-toplevel");
  const result = promote({ root, tag: args[0], getRelease: (tag) => JSON.parse(execFileSync("gh", ["api", `repos/thebtf/engram/releases/tags/${tag}`], { cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] })) });
  process.stdout.write(`${JSON.stringify(result)}\n`);
}

if (require.main === module) {
  try { main(process.argv.slice(2)); } catch (error) { process.stderr.write(`${error.message}\n`); process.exitCode = 1; }
}

module.exports = { promote };
