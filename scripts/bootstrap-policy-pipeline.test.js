const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
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

test("generator check mode and raw artifact gate accept only the shared target rows", () => {
  const temp = temporaryDirectory();
  try {
    const fakeBin = path.join(temp, "bin");
    const fakeGo = path.join(fakeBin, "go");
    const policyPath = path.join(temp, "bootstrap-targets.json");
    const dist = path.join(temp, "dist");
    fs.mkdirSync(fakeBin, { recursive: true });
    fs.writeFileSync(fakeGo, "#!/usr/bin/env bash\nif [[ $1 == version ]]; then echo 'go version go1.25.12 linux/amd64'; exit 0; fi\nwhile [[ $# -gt 0 ]]; do if [[ $1 == -o ]]; then shift; printf '%s-%s' \"$GOOS\" \"$GOARCH\" > \"$1\"; exit 0; fi; shift; done\nexit 1\n", { mode: 0o755 });
    const fakeGoArgument = shellQuote(bashPath(fakeGo));
    const policyArgument = shellQuote(bashPath(policyPath));
    run("bash", ["-c", `ENGRAM_BOOTSTRAP_GO=${fakeGoArgument} scripts/prepare-bootstrap-policy.sh --version 6.47.1 --output ${policyArgument}`]);
    run("bash", ["-c", `ENGRAM_BOOTSTRAP_GO=${fakeGoArgument} scripts/prepare-bootstrap-policy.sh --version 6.47.1 --output ${policyArgument} --check`]);
    const policy = parsePolicy(fs.readFileSync(policyPath, "utf8"), "6.47.1");
    fs.mkdirSync(dist, { recursive: true });
    for (const { desired } of Object.values(policy.targets)) {
      const bytes = desired.asset.includes("windows") ? "windows-amd64" : desired.asset.includes("linux") ? "linux-amd64" : "darwin-arm64";
      fs.writeFileSync(path.join(dist, desired.asset), bytes);
    }
    const archiveRoot = path.join(temp, "archive");
    fs.mkdirSync(archiveRoot);
    fs.copyFileSync(policyPath, path.join(archiveRoot, "bootstrap-targets.json"));
    const archive = path.join(dist, "engram_6.47.1_linux_amd64.tar.gz");
    const archived = spawnSync("tar", ["-czf", archive, "-C", archiveRoot, "bootstrap-targets.json"], { encoding: "utf8" });
    assert.equal(archived.status, 0, archived.stderr);
    run("bash", ["scripts/check-bootstrap-policy-artifacts.sh", "--policy", bashPath(policyPath), "--dist", bashPath(dist)]);
    fs.appendFileSync(path.join(dist, policy.targets["linux-x64"].desired.asset), "drift");
    const rejected = spawnSync("bash", ["scripts/check-bootstrap-policy-artifacts.sh", "--policy", bashPath(policyPath), "--dist", bashPath(dist)], { cwd: root, encoding: "utf8" });
    assert.notEqual(rejected.status, 0);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
});

test("direct installer rejects archive self-validation before install mutation", () => {
  const temp = temporaryDirectory();
  try {
    const archiveRoot = path.join(temp, "archive");
    const fakeBin = path.join(temp, "bin");
    const home = path.join(temp, "home");
    for (const directory of ["hooks", "scripts", ".claude-plugin"]) fs.mkdirSync(path.join(archiveRoot, directory), { recursive: true });
    fs.mkdirSync(fakeBin, { recursive: true });
    fs.writeFileSync(path.join(archiveRoot, "hooks", "hook.js"), "module.exports = {};\n");
    fs.writeFileSync(path.join(archiveRoot, "hooks", "hooks.json"), "{}\n");
    fs.writeFileSync(path.join(archiveRoot, "scripts", "bootstrap-policy.js"), "process.exit(0);\n");
    fs.writeFileSync(path.join(archiveRoot, ".claude-plugin", "plugin.json"), "{}\n");
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
    assert.ok(powerShellInstaller.indexOf("$ValidatorScript | & node") < powerShellInstaller.indexOf("New-Item -ItemType Directory -Path \"$InstallDir\\hooks\""));
    for (const source of [powerShellInstaller, fs.readFileSync(path.join(root, "scripts", "install.sh"), "utf8")]) {
      const semver = installerSemver(source);
      assert.equal(semver.test("6.47.1-rc.1"), true);
      assert.equal(semver.test("6.47.1+build.1"), false);
    }
    assert.match(powerShellInstaller, /Node\.js 18\+/);
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
    const policyVersion = JSON.parse(policyJSON).package_version;
    const policy = parsePolicy(policyJSON, policyVersion);
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
