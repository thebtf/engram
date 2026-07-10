#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const TARGET = 'db2cf891dd9c6315fd17220ffe2d02302bea8844';
const ALTERNATE_ANCESTOR = '580b0cd0ff38bb55a5195a8004e60234a824b7a8';
const SOURCE = '38d6a4fb7ff5f5ae3b6c0066c0a1b806421137df';
const repositoryArgument = process.argv.find((value) => value.startsWith('--repository='));

if (!repositoryArgument) {
  process.stderr.write('usage: checker-edge-mutations.cjs --repository=<detached-target-worktree>\n');
  process.exit(2);
}

const repoRoot = path.resolve(repositoryArgument.slice('--repository='.length));
const evidenceDirectory = path.join(
  repoRoot,
  '.agent',
  'reports',
  'evidence',
  'production-ready',
  'db-embedding-stats-evidence-transport',
);
const verifierPath = path.join(evidenceDirectory, 'verify-manifest.cjs');
const contractPath = path.join(evidenceDirectory, 'content-manifest.v1.json');
const artifactManifestPath = path.join(evidenceDirectory, 'ARTIFACTS.sha256');
const legacyManifestPath = path.join(
  repoRoot,
  '.agent',
  'reports',
  'evidence',
  'production-ready',
  'db-embedding-stats',
  'SHA256SUMS.txt',
);

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd || repoRoot,
    encoding: options.encoding === undefined ? 'utf8' : options.encoding,
    windowsHide: true,
    maxBuffer: 64 * 1024 * 1024,
  });
  if (result.error) throw result.error;
  return result;
}

function gitText(args) {
  const result = run('git', args);
  if (result.status !== 0) throw new Error(result.stderr.trim());
  return result.stdout.trim();
}

function gitBytes(args) {
  const result = run('git', args, { encoding: null });
  if (result.status !== 0) throw new Error(result.stderr.toString('utf8').trim());
  return result.stdout;
}

function sha256(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function serializeJsonLike(original, value) {
  const eol = original.includes(Buffer.from('\r\n')) ? '\r\n' : '\n';
  return Buffer.from(`${JSON.stringify(value, null, 2).replace(/\n/g, eol)}${eol}`, 'utf8');
}

function mutateAnnotated(original, mutate) {
  const text = original.toString('utf8');
  const eol = text.includes('\r\n') ? '\r\n' : '\n';
  const lines = text.split(/\r?\n/);
  if (lines.at(-1) === '') lines.pop();
  mutate(lines);
  return Buffer.from(`${lines.join(eol)}${eol}`, 'utf8');
}

function runVerifier(mode) {
  const result = run(process.execPath, [verifierPath, `--mode=${mode}`]);
  let output = null;
  try {
    if (result.stdout.trim()) output = JSON.parse(result.stdout);
  } catch {
    output = null;
  }
  return {
    exit_code: result.status,
    status: output?.status || null,
    total: output?.total ?? null,
    matched: output?.matched ?? null,
    structural_errors: output?.structural_errors || [],
    entries: output?.entries || [],
    stderr: result.stderr.trim(),
  };
}

function withMutations(mutations, action) {
  const originals = new Map();
  try {
    for (const [filePath, bytes] of mutations) {
      originals.set(filePath, fs.readFileSync(filePath));
      fs.writeFileSync(filePath, bytes);
    }
    return action();
  } finally {
    for (const [filePath, bytes] of originals) fs.writeFileSync(filePath, bytes);
  }
}

function expectRejected(name, result, extraCheck = () => true) {
  const pass = result.exit_code !== 0 && result.status !== 'PASS' && extraCheck(result);
  return { name, expectation: 'reject', pass, ...result };
}

function expectAccepted(name, result, extraCheck = () => true) {
  const pass = result.exit_code === 0 && result.status === 'PASS' && extraCheck(result);
  return { name, expectation: 'accept', pass, ...result };
}

if (gitText(['rev-parse', 'HEAD']) !== TARGET) {
  throw new Error(`mutation worktree must be at exact target ${TARGET}`);
}
if (gitText(['status', '--porcelain']) !== '') {
  throw new Error('mutation worktree must start clean');
}

const cases = [];
cases.push(expectAccepted('baseline_git_object', runVerifier('git-object'), (result) => result.total === 7));

{
  const originalContract = fs.readFileSync(contractPath);
  const originalLegacy = fs.readFileSync(legacyManifestPath);
  const contract = JSON.parse(originalContract);
  const removed = contract.entries.pop();
  const legacy = mutateAnnotated(originalLegacy, (lines) => {
    const index = lines.findIndex((line) => line.endsWith(`  ${removed.path}`));
    if (index < 0) throw new Error('removed entry not present in legacy manifest');
    lines.splice(index, 1);
  });
  const result = withMutations(
    [
      [contractPath, serializeJsonLike(originalContract, contract)],
      [legacyManifestPath, legacy],
    ],
    () => runVerifier('git-object'),
  );
  cases.push(expectRejected('content_manifest_rejects_six_of_seven_when_legacy_agrees', result));
}

{
  const originalContract = fs.readFileSync(contractPath);
  const originalLegacy = fs.readFileSync(legacyManifestPath);
  const contract = JSON.parse(originalContract);
  const replaced = contract.entries.at(-1);
  const substitutePath = 'go.mod';
  const substituteBytes = gitBytes(['cat-file', 'blob', `${SOURCE}:${substitutePath}`]);
  const substitute = {
    path: substitutePath,
    git_blob_oid: gitText(['rev-parse', `${SOURCE}:${substitutePath}`]),
    byte_length: substituteBytes.length,
    sha256: sha256(substituteBytes),
  };
  contract.entries[contract.entries.length - 1] = substitute;
  const legacy = mutateAnnotated(originalLegacy, (lines) => {
    const index = lines.findIndex((line) => line.endsWith(`  ${replaced.path}`));
    if (index < 0) throw new Error('replaced entry not present in legacy manifest');
    lines[index] = `${substitute.sha256}  ${substitute.path}`;
  });
  const result = withMutations(
    [
      [contractPath, serializeJsonLike(originalContract, contract)],
      [legacyManifestPath, legacy],
    ],
    () => runVerifier('git-object'),
  );
  cases.push(expectRejected('content_manifest_rejects_wrong_path_with_same_cardinality', result));
}

{
  const originalContract = fs.readFileSync(contractPath);
  const originalLegacy = fs.readFileSync(legacyManifestPath);
  const contract = JSON.parse(originalContract);
  contract.representation.source_commit = ALTERNATE_ANCESTOR;
  const legacy = mutateAnnotated(originalLegacy, (lines) => {
    const index = lines.findIndex((line) => line.startsWith('# source-commit='));
    if (index < 0) throw new Error('source-commit metadata not present');
    lines[index] = `# source-commit=${ALTERNATE_ANCESTOR}`;
  });
  const result = withMutations(
    [
      [contractPath, serializeJsonLike(originalContract, contract)],
      [legacyManifestPath, legacy],
    ],
    () => runVerifier('git-object'),
  );
  cases.push(expectRejected('representation_rejects_alternate_ancestor_source_commit', result));
}

{
  const originalContract = fs.readFileSync(contractPath);
  const originalLegacy = fs.readFileSync(legacyManifestPath);
  const contract = JSON.parse(originalContract);
  contract.entries[1] = structuredClone(contract.entries[0]);
  const legacy = mutateAnnotated(originalLegacy, (lines) => {
    const dataIndexes = lines
      .map((line, index) => ({ line, index }))
      .filter(({ line }) => /^[0-9a-f]{64}  /.test(line))
      .map(({ index }) => index);
    lines[dataIndexes[1]] = lines[dataIndexes[0]];
  });
  const result = withMutations(
    [
      [contractPath, serializeJsonLike(originalContract, contract)],
      [legacyManifestPath, legacy],
    ],
    () => runVerifier('git-object'),
  );
  cases.push(expectRejected('content_manifest_rejects_duplicate_canonical_path', result));
}

{
  const storePath = path.join(repoRoot, 'internal', 'embedding', 'store.go');
  const original = fs.readFileSync(storePath);
  const result = withMutations(
    [[storePath, Buffer.concat([original, Buffer.from([13])])]],
    () => runVerifier('checkout-lf'),
  );
  cases.push(expectRejected(
    'checkout_lf_rejects_real_bare_cr_byte',
    result,
    (value) => value.entries.some((entry) => entry.bare_carriage_returns > 0),
  ));
}

{
  const original = fs.readFileSync(artifactManifestPath);
  const mutated = mutateAnnotated(original, (lines) => {
    const index = lines.findIndex((line) => line.startsWith('# representation='));
    if (index < 0) throw new Error('artifact representation metadata not present');
    lines[index] = '# representation=raw-checkout-files';
  });
  const result = withMutations([[artifactManifestPath, mutated]], () => runVerifier('artifact-files'));
  cases.push(expectRejected('artifact_manifest_rejects_representation_drift', result));
}

{
  const makerReportPath = path.join(evidenceDirectory, 'maker-report.md');
  const original = fs.readFileSync(makerReportPath);
  const result = withMutations(
    [[makerReportPath, Buffer.concat([original, Buffer.from([13])])]],
    () => runVerifier('artifact-files'),
  );
  cases.push(expectRejected(
    'artifact_files_reject_real_bare_cr_byte',
    result,
    (value) => value.entries.some((entry) => entry.bare_carriage_returns > 0),
  ));
}

const residue = gitText(['status', '--porcelain']);
const falsePasses = cases.filter((entry) => !entry.pass);
const result = {
  schema_version: 1,
  checker: 'DB-EMBEDDING-EVIDENCE-TRANSPORT-R2',
  target: TARGET,
  source_commit: SOURCE,
  alternate_ancestor: ALTERNATE_ANCESTOR,
  status: falsePasses.length === 0 && residue === '' ? 'PASS' : 'FAIL',
  total: cases.length,
  passed: cases.length - falsePasses.length,
  failed: falsePasses.length,
  false_pass_cases: falsePasses.map((entry) => entry.name),
  worktree_residue: residue,
  cases,
};

process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
if (result.status !== 'PASS') process.exit(1);
