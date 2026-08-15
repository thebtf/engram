const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const test = require("node:test");

const root = path.resolve(__dirname, "..");
const manifestPath = path.join(root, "plugin", "engram", "package.json");
const extensionPath = path.join(root, "plugin", "engram", "extensions", "engram-memory.mjs");
const version = JSON.parse(fs.readFileSync(manifestPath, "utf8")).version;
function expectedArchives(expectedVersion = version) {
  const normalizedVersion = expectedVersion.replace(/^v/, "");
  return [
    `engram_${normalizedVersion}_linux_amd64.tar.gz`,
    `engram_${normalizedVersion}_darwin_arm64.tar.gz`,
    `engram_${normalizedVersion}_windows_amd64.zip`,
  ];
}

function command(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: root, encoding: "utf8", ...options });
  assert.ifError(result.error);
  return result;
}

function run(commandName, args, options) {
  const result = command(commandName, args, options);
  assert.equal(result.status, 0, result.stderr || result.stdout);
  return result;
}

function bashPath(value) { return path.relative(root, value).split(path.sep).join("/"); }

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

function buildArchive(archiveRoot, archivePath, entries = ["package.json", "extensions/engram-memory.mjs"]) {
  if (archivePath.endsWith(".tar.gz")) {
    run("tar", ["-czf", archivePath, "-C", archiveRoot, ...entries]);
  } else {
    writeZip(archiveRoot, archivePath, entries);
  }
}
function createFixture() {
  const temporaryRoot = path.join(root, ".agent", "tmp");
  fs.mkdirSync(temporaryRoot, { recursive: true });
  const temporary = fs.mkdtempSync(path.join(temporaryRoot, "engram-server-plugin-gate-"));
  const archiveRoot = path.join(temporary, "archive");
  const dist = path.join(temporary, "dist");
  fs.mkdirSync(path.join(archiveRoot, "extensions"), { recursive: true });
  fs.mkdirSync(dist);
  fs.copyFileSync(manifestPath, path.join(archiveRoot, "package.json"));
  fs.copyFileSync(extensionPath, path.join(archiveRoot, "extensions", "engram-memory.mjs"));
  return { archiveRoot, dist, temporary };
}

function buildExpectedMatrix({ archiveRoot, dist }, expectedVersion = version) {
  for (const archiveName of expectedArchives(expectedVersion)) buildArchive(archiveRoot, path.join(dist, archiveName));
}

function gate(dist, expectedVersion = version) {
  return command("bash", ["scripts/check-server-plugin-artifacts.sh", "--version", expectedVersion, "--dist", bashPath(dist)]);
}

test("server-plugin archive gate runs through supported Bash invocation", () => {
  const fixture = createFixture();
  try {
    buildExpectedMatrix(fixture);
    const accepted = gate(fixture.dist);
    assert.equal(accepted.status, 0, accepted.stderr || accepted.stdout);
  } finally {
    fs.rmSync(fixture.temporary, { recursive: true, force: true });
  }
});

test("server-plugin archive gate accepts exactly the canonical three-archive OMP payload matrix", () => {
  const fixture = createFixture();
  const releaseVersion = `v${version}`;
  const archives = expectedArchives(releaseVersion);
  try {
    buildExpectedMatrix(fixture, releaseVersion);
    const accepted = gate(fixture.dist, releaseVersion);
    assert.equal(accepted.status, 0, accepted.stderr || accepted.stdout);
    assert.match(accepted.stdout, /3 server-plugin archive\(s\)/);

    fs.rmSync(path.join(fixture.dist, archives[1]));
    const missing = gate(fixture.dist, releaseVersion);
    assert.notEqual(missing.status, 0);
    assert.match(`${missing.stderr}\n${missing.stdout}`, /matrix mismatch/);

    const duplicateDirectory = path.join(fixture.dist, "duplicate");
    fs.mkdirSync(duplicateDirectory);
    fs.copyFileSync(path.join(fixture.dist, archives[0]), path.join(duplicateDirectory, archives[0]));
    const duplicateArchive = gate(fixture.dist, releaseVersion);
    assert.notEqual(duplicateArchive.status, 0);
    assert.match(`${duplicateArchive.stderr}\n${duplicateArchive.stdout}`, /missing or duplicates/);
  } finally {
    fs.rmSync(fixture.temporary, { recursive: true, force: true });
  }
});

test("server-plugin archive gate rejects duplicate or non-canonical OMP entry paths", () => {
  const fixture = createFixture();
  try {
    buildExpectedMatrix(fixture);
    buildArchive(fixture.archiveRoot, path.join(fixture.dist, expectedArchives()[0]), ["package.json", "package.json", "extensions/engram-memory.mjs"]);
    const duplicate = gate(fixture.dist);
    assert.notEqual(duplicate.status, 0);
    assert.match(`${duplicate.stderr}\n${duplicate.stdout}`, /exactly one canonical package\.json/);

    buildArchive(fixture.archiveRoot, path.join(fixture.dist, expectedArchives()[0]), ["package.json"]);
    const missingCanonicalPath = gate(fixture.dist);
    assert.notEqual(missingCanonicalPath.status, 0);
    assert.match(`${missingCanonicalPath.stderr}\n${missingCanonicalPath.stdout}`, /canonical extensions\/engram-memory\.mjs/);
  } finally {
    fs.rmSync(fixture.temporary, { recursive: true, force: true });
  }
});

test("server-plugin archive gate rejects manifest contract mutation and extension byte drift", () => {
  const fixture = createFixture();
  try {
    buildExpectedMatrix(fixture);
    const manifest = JSON.parse(fs.readFileSync(path.join(fixture.archiveRoot, "package.json"), "utf8"));
    manifest.version = "0.0.0";
    fs.writeFileSync(path.join(fixture.archiveRoot, "package.json"), `${JSON.stringify(manifest)}\n`);
    buildArchive(fixture.archiveRoot, path.join(fixture.dist, expectedArchives()[0]));
    const stringVersionMismatch = gate(fixture.dist);
    assert.notEqual(stringVersionMismatch.status, 0);
    assert.match(`${stringVersionMismatch.stderr}\n${stringVersionMismatch.stdout}`, /manifest does not match the release contract/);

    manifest.version = 6;
    fs.writeFileSync(path.join(fixture.archiveRoot, "package.json"), `${JSON.stringify(manifest)}\n`);
    buildArchive(fixture.archiveRoot, path.join(fixture.dist, expectedArchives()[0]));
    const numericVersionMismatch = gate(fixture.dist);
    assert.notEqual(numericVersionMismatch.status, 0);
    assert.match(`${numericVersionMismatch.stderr}\n${numericVersionMismatch.stdout}`, /manifest does not match the release contract/);

    delete manifest.version;
    fs.writeFileSync(path.join(fixture.archiveRoot, "package.json"), `${JSON.stringify(manifest)}\n`);
    buildArchive(fixture.archiveRoot, path.join(fixture.dist, expectedArchives()[0]));
    const missingVersion = gate(fixture.dist);
    assert.notEqual(missingVersion.status, 0);
    assert.match(`${missingVersion.stderr}\n${missingVersion.stdout}`, /manifest does not match the release contract/);

    manifest.version = version;
    manifest.omp.extensions = ["./wrong.mjs"];
    fs.writeFileSync(path.join(fixture.archiveRoot, "package.json"), `${JSON.stringify(manifest)}\n`);
    buildArchive(fixture.archiveRoot, path.join(fixture.dist, expectedArchives()[0]));
    const extensionPathMismatch = gate(fixture.dist);
    assert.notEqual(extensionPathMismatch.status, 0);
    assert.match(`${extensionPathMismatch.stderr}\n${extensionPathMismatch.stdout}`, /manifest does not match the release contract/);
    manifest.omp.extensions = ["./extensions/engram-memory.mjs"];

    delete manifest.engines;
    fs.writeFileSync(path.join(fixture.archiveRoot, "package.json"), `${JSON.stringify(manifest)}\n`);
    buildArchive(fixture.archiveRoot, path.join(fixture.dist, expectedArchives()[0]));
    const missingEngines = gate(fixture.dist);
    assert.notEqual(missingEngines.status, 0);
    assert.match(`${missingEngines.stderr}\n${missingEngines.stdout}`, /manifest does not match the release contract/);

    manifest.engines = { node: ">=20" };
    fs.writeFileSync(path.join(fixture.archiveRoot, "package.json"), `${JSON.stringify(manifest)}\n`);
    buildArchive(fixture.archiveRoot, path.join(fixture.dist, expectedArchives()[0]));
    const wrongEngines = gate(fixture.dist);
    assert.notEqual(wrongEngines.status, 0);
    assert.match(`${wrongEngines.stderr}\n${wrongEngines.stdout}`, /manifest does not match the release contract/);

    fs.copyFileSync(manifestPath, path.join(fixture.archiveRoot, "package.json"));
    fs.appendFileSync(path.join(fixture.archiveRoot, "extensions", "engram-memory.mjs"), "\n// mutation\n");
    buildArchive(fixture.archiveRoot, path.join(fixture.dist, expectedArchives()[0]));
    const byteDrift = gate(fixture.dist);
    assert.notEqual(byteDrift.status, 0);
    assert.match(`${byteDrift.stderr}\n${byteDrift.stdout}`, /extension differs from tagged source/);
  } finally {
    fs.rmSync(fixture.temporary, { recursive: true, force: true });
  }
});

test("release configuration and direct installers retain canonical OMP payload paths", () => {
  const goreleaser = fs.readFileSync(path.join(root, ".goreleaser.yaml"), "utf8");
  assert.match(goreleaser, /- src: plugin\/engram\/package\.json\s+dst: \.\s+strip_parent: true/);
  assert.match(goreleaser, /- src: plugin\/engram\/extensions\/engram-memory\.mjs\s+dst: extensions\s+strip_parent: true/);

  const shellInstaller = fs.readFileSync(path.join(root, "scripts", "install.sh"), "utf8");
  assert.match(shellInstaller, /\[\[ -f "\$tmp_dir\/package\.json" \]\]/);
  assert.match(shellInstaller, /cp "\$tmp_dir\/package\.json" "\$INSTALL_DIR\//);
  assert.match(shellInstaller, /cp "\$tmp_dir\/extensions\/engram-memory\.mjs" "\$INSTALL_DIR\/extensions\//);

  const powerShellInstaller = fs.readFileSync(path.join(root, "scripts", "install.ps1"), "utf8");
  assert.match(powerShellInstaller, /Join-Path \$TempDir "package\.json"/);
  assert.match(powerShellInstaller, /Join-Path \$TempDir "extensions\\engram-memory\.mjs"/);
  assert.match(powerShellInstaller, /Copy-Item \$ManifestPath "\$InstallDir\\package\.json"/);
  assert.match(powerShellInstaller, /Copy-Item \$ExtensionPath "\$InstallDir\\extensions\\engram-memory\.mjs"/);
});
