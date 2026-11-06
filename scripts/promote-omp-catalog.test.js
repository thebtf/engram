"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { createPolicy } = require("../plugin/engram/scripts/bootstrap-policy.js");
const { promote } = require("./promote-omp-catalog.js");

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "engram-catalog-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const git = (...args) => execFileSync("git", args, { cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, GIT_CONFIG_NOSYSTEM: "1", GIT_CONFIG_GLOBAL: path.join(root, "no-global-config"), GIT_AUTHOR_NAME: "Fixture", GIT_AUTHOR_EMAIL: "fixture@example.invalid", GIT_COMMITTER_NAME: "Fixture", GIT_COMMITTER_EMAIL: "fixture@example.invalid" } }).trim();
  const write = (name, value) => { const file = path.join(root, name); fs.mkdirSync(path.dirname(file), { recursive: true }); fs.writeFileSync(file, typeof value === "string" ? value : `${JSON.stringify(value, null, 2)}\n`); };
  write("fixture-id", path.basename(root));
  git("init", "-q");
  function packageVersion(version) {
    const bytes = Buffer.from(`raw-${version}`), crypto = require("node:crypto");
    const hash = crypto.createHash("sha256").update(bytes).digest("hex");
    const targets = Object.fromEntries(Object.entries(require("../plugin/engram/scripts/bootstrap-policy.js").TARGET_ASSETS).map(([key, asset]) => [key, { version, asset, size: bytes.length, sha256: hash }]));
    const policy = createPolicy(version, targets);
    const parser = { version, asset: "uci-parser-windows-amd64.exe", size: bytes.length, sha256: hash };
    write("plugin/engram/package.json", { version });
    write("plugin/engram/.omp-plugin/plugin.json", { version });
    write("plugin/engram/bootstrap-targets.json", policy);
    write("plugin/engram/parser-targets.json", { schema_version: 1, package_version: version, targets: { "linux-x64": null, "darwin-arm64": null, "win32-x64": parser } });
    return { id: 123, tag_name: `v${version}`, draft: false, prerelease: false, published_at: "2026-01-01T00:00:00Z", assets: [...Object.values(policy.targets).map(({ desired }) => desired), parser].map((item) => ({ name: item.asset, state: "uploaded", size: item.size, digest: `sha256:${item.sha256}` })) };
  }
  packageVersion("1.0.0");
  git("add", "."); git("commit", "-qm", "published old"); git("tag", "-a", "v1.0.0", "-m", "old");
  const old = git("rev-parse", "HEAD");
  const nextRelease = packageVersion("1.0.1");
  git("add", "."); git("commit", "-qm", "published new"); git("tag", "-a", "v1.0.1", "-m", "new");
  const next = git("rev-parse", "HEAD");
  packageVersion("1.0.2");
  git("add", "."); git("commit", "-qm", "next candidate");
  const catalog = { name: "engram", version: "1.0.0", plugins: [{ name: "engram", version: "1.0.0", source: { source: "git-subdir", url: "thebtf/engram", path: "plugin/engram", ref: "v1.0.0", sha: old } }] };
  write(".omp-plugin/marketplace.json", catalog);
  const file = path.join(root, ".omp-plugin", "marketplace.json");
  return { root, git, old, next, file, nextRelease, run: (tag, release = nextRelease) => promote({ root, tag, remote: root, getRelease: () => release }) };
}

test("last published subdirectory stays pinned when source advances; promotion selects verified tag", (t) => {
  const f = fixture(t);
  const before = JSON.parse(fs.readFileSync(f.file));
  const clone = path.join(f.root, "checkout");
  f.git("clone", "--no-local", "--branch", before.plugins[0].source.ref, f.root, clone);
  const checkoutHead = execFileSync("git", ["-C", clone, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  assert.equal(checkoutHead, before.plugins[0].source.sha);
  assert.equal(JSON.parse(fs.readFileSync(path.join(clone, before.plugins[0].source.path, "package.json"))).version, "1.0.0");
  const selected = JSON.parse(f.git("show", `${before.plugins[0].source.sha}:${before.plugins[0].source.path}/package.json`));
  assert.equal(selected.version, "1.0.0");
  assert.equal(JSON.parse(fs.readFileSync(path.join(f.root, "plugin/engram/package.json"))).version, "1.0.2");
  assert.deepEqual(f.run("v1.0.1"), { tag: "v1.0.1", commit: f.next, changed: true });
  const after = JSON.parse(fs.readFileSync(f.file));
  assert.equal(after.version, "1.0.1");
  assert.equal(after.plugins[0].source.sha, f.next);
  assert.equal(JSON.parse(f.git("show", `${after.plugins[0].source.ref}:${after.plugins[0].source.path}/package.json`)).version, "1.0.1");
  assert.deepEqual(f.run("v1.0.1"), { tag: "v1.0.1", commit: f.next, changed: false });
});

test("unpublished, absent or mismatched assets and bad refs never write the catalog", (t) => {
  const f = fixture(t), before = fs.readFileSync(f.file, "utf8");
  const reject = (tag, release) => { assert.throws(() => f.run(tag, release)); assert.equal(fs.readFileSync(f.file, "utf8"), before); };
  reject("v1.0.1", { ...f.nextRelease, draft: true });
  reject("v1.0.1", { ...f.nextRelease, prerelease: true });
  reject("v1.0.1", { ...f.nextRelease, assets: f.nextRelease.assets.slice(1) });
  reject("v1.0.1", { ...f.nextRelease, assets: f.nextRelease.assets.map((asset, i) => i ? asset : { ...asset, digest: `sha256:${"0".repeat(64)}` }) });
  reject("v1.0.9", f.nextRelease);
  reject("v1.0.01", f.nextRelease);
  reject("v1.0.1", { ...f.nextRelease, assets: f.nextRelease.assets.filter((asset) => asset.name !== "uci-parser-windows-amd64.exe") });
  reject("v1.0.1", { ...f.nextRelease, tag_name: "v1.0.0" });
  reject("v1.0.1", { ...f.nextRelease, published_at: null });
  const otherRemote = fixture(t);
  assert.throws(() => promote({ root: f.root, tag: "v1.0.1", remote: otherRemote.root, getRelease: () => f.nextRelease }), /does not match remote/);
  assert.equal(fs.readFileSync(f.file, "utf8"), before);
  reject("v0.9.9", f.nextRelease);
});
