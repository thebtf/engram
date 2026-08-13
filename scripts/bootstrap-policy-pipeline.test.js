const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
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
function waitForFile(file, timeoutMs = 1_000) {
  const deadline = Date.now() + timeoutMs;
  while (!fs.existsSync(file)) {
    if (Date.now() >= deadline) throw new Error(`timed out waiting for ${file}`);
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10);
  }
}
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
            ENGRAM_REGISTRY_LOCK_TIMEOUT_MS: "1000",
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
    waitForFile(acquired);
    assert.equal(fs.existsSync(lockDirectory), true, "holder must own the registry lock");
    assert.equal(fs.existsSync(path.join(lockDirectory, "owner")), true, "holder must create the registry lock owner file");
    const second = start(argumentsFor("second@engram", "6.47.2"), "waiter");
    waitForFile(contended);
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
    fs.mkdirSync(archiveRoot);
    fs.copyFileSync(policyPath, path.join(archiveRoot, "bootstrap-targets.json"));
    const archive = path.join(dist, `engram_${currentVersion}_linux_amd64.tar.gz`);
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
    assert.ok(powerShellInstaller.indexOf("$ValidatorScript | & node") < powerShellInstaller.indexOf("New-Item -ItemType Directory -Path \"$InstallDir\\hooks\""));
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
    assert.deepEqual(registrationArtifacts(home), []);
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
    assert.equal(registration.isLocal, true);
    assert.equal(settings.enabledPlugins["engram@engram"], true);
    assert.deepEqual(settings.statusLine, { type: "command", command: `node "${bashPath(installRoot)}/hooks/statusline.js"`, padding: 0 });
    assert.deepEqual(marketplaces.engram.source, { source: "directory", path: bashPath(installRoot) });
    assert.equal(marketplaces.engram.installLocation, bashPath(installRoot));
    assert.equal(marketplaces.engram.lastUpdated, registration.lastUpdated);
    assert.equal(registration.installedAt, registration.lastUpdated);

    const artifacts = registrationArtifacts(home);
    const backups = artifacts.filter(registrationBackup);
    assert.equal(backups.length, 1);
    assert.ok(backups[0].startsWith(`${pluginsFile}.backup-`));
    assert.deepEqual(fs.readFileSync(backups[0]), before[0]);
    assert.ok(output.includes(bashPath(backups[0])));
    assert.deepEqual(artifacts.filter(registrationStaged), []);
    assert.deepEqual(fs.readFileSync(failureLog, "utf8").trimEnd().split("\n"), [bashPath(backups[0])]);
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
