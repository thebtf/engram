#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const BASE_DIRECTORY =
  '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport';
const DIRECTORY = `${BASE_DIRECTORY}-r6`;
const BASE_VERIFIER = `${BASE_DIRECTORY}/verify-manifest.cjs`;
const BASE_TEST = `${BASE_DIRECTORY}/verify-manifest.test.cjs`;
const R6_VERIFIER = `${DIRECTORY}/verify-evidence.cjs`;
const R6_TEST = `${DIRECTORY}/verify-evidence.test.cjs`;
const CAPTURE = `${DIRECTORY}/coverage-capture.v2.json`;

function sha256(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function repoPath(repoRoot, relativePath) {
  return path.join(repoRoot, ...relativePath.split('/'));
}

function git(args, cwd, encoding = 'utf8') {
  const result = spawnSync('git', args, { cwd, encoding, windowsHide: true });
  if (result.status !== 0) throw new Error(result.stderr.trim());
  return result.stdout;
}

function parseCounts(text) {
  const count = (label) => {
    const match = text.match(new RegExp(`(?:ℹ|#) ${label} ([0-9]+)`));
    if (!match) throw new Error(`TAP output is missing ${label}`);
    return Number(match[1]);
  };
  return { tests: count('tests'), passed: count('pass'), failed: count('fail') };
}

function runTest(repoRoot, relativePath) {
  const result = spawnSync(
    process.execPath,
    ['--test', '--test-concurrency=1', relativePath],
    { cwd: repoRoot, encoding: 'utf8', windowsHide: true, maxBuffer: 64 * 1024 * 1024 },
  );
  const combined = `${result.stdout}\n${result.stderr}`;
  return {
    exit_code: result.status,
    ...parseCounts(combined),
    stdout_sha256: sha256(Buffer.from(result.stdout, 'utf8')),
    stderr_sha256: sha256(Buffer.from(result.stderr, 'utf8')),
  };
}

function withSourceMutation(filePath, mutate, execute) {
  const original = fs.readFileSync(filePath);
  try {
    const mutated = mutate(Buffer.from(original));
    if (mutated.equals(original)) throw new Error(`sentinel did not change ${filePath}`);
    fs.writeFileSync(filePath, mutated);
    return execute();
  } finally {
    fs.writeFileSync(filePath, original);
    if (!fs.readFileSync(filePath).equals(original)) {
      throw new Error(`failed to restore ${filePath}`);
    }
  }
}

function replaceOnce(bytes, expression, replacement, label) {
  const text = bytes.toString('utf8');
  const matches = text.match(expression);
  if (!matches || matches.length !== 1) throw new Error(`${label} anchor count is not one`);
  return Buffer.from(text.replace(expression, replacement), 'utf8');
}

function main() {
  const repoRoot = path.resolve(git(['rev-parse', '--show-toplevel'], process.cwd()).trim());
  const capture = JSON.parse(fs.readFileSync(repoPath(repoRoot, CAPTURE), 'utf8'));
  const expectedBase = capture.sources.slice(0, 2);
  expectedBase.forEach((entry) => {
    const oid = git(['rev-parse', `:${entry.path}`], repoRoot).trim();
    if (oid !== entry.git_blob_oid) throw new Error(`staged blob drift: ${entry.path}`);
  });

  const baseVerifierPath = repoPath(repoRoot, BASE_VERIFIER);
  const r6VerifierPath = repoPath(repoRoot, R6_VERIFIER);
  const schemaSentinel = withSourceMutation(
    baseVerifierPath,
    (bytes) => replaceOnce(
      bytes,
      /structural_errors: structuralErrors,\r?\n    validated_entries: structuralErrors\.length === 0 \? validatedEntries : \[\],/,
      'structural_errors: [],\n    validated_entries: validatedEntries,',
      'validateContractSchema sentinel',
    ),
    () => runTest(repoRoot, BASE_TEST),
  );
  const artifactSentinel = withSourceMutation(
    baseVerifierPath,
    (bytes) => replaceOnce(
      bytes,
      /const status = structuralErrors\.length === 0 && matched === entryResults\.length \? 'PASS' : 'FAIL';/,
      "const status = 'PASS';",
      'verifyArtifactFiles sentinel',
    ),
    () => runTest(repoRoot, BASE_TEST),
  );
  const r6StatusSentinel = withSourceMutation(
    r6VerifierPath,
    (bytes) => replaceOnce(
      bytes,
      /const status = errors\.length === 0 \? 'PASS' : 'FAIL';/,
      "const status = 'PASS';",
      'R6 status sentinel',
    ),
    () => runTest(repoRoot, R6_TEST),
  );
  const postRestoreBase = runTest(repoRoot, BASE_TEST);
  const postRestoreR6 = runTest(repoRoot, R6_TEST);
  for (const [label, value] of [
    ['validateContractSchema', schemaSentinel],
    ['verifyArtifactFiles', artifactSentinel],
    ['R6 status', r6StatusSentinel],
  ]) {
    if (value.exit_code === 0 || value.failed === 0) {
      throw new Error(`${label} mutation did not make the permanent suite fail`);
    }
  }
  for (const [label, value] of [
    ['base post-restore', postRestoreBase],
    ['R6 post-restore', postRestoreR6],
  ]) {
    if (value.exit_code !== 0 || value.failed !== 0) {
      throw new Error(`${label} did not return to GREEN`);
    }
  }

  process.stdout.write(`${JSON.stringify({
    schema_version: 1,
    task_id: 'DB-EMBEDDING-EVIDENCE-TRANSPORT-R6',
    exact_source_blobs: expectedBase.map(({ role, path: sourcePath, git_blob_oid, git_blob_sha256 }) => ({
      role,
      path: sourcePath,
      git_blob_oid,
      git_blob_sha256,
    })),
    sentinels: {
      validateContractSchema: schemaSentinel,
      verifyArtifactFiles: artifactSentinel,
      r6_status_gate: r6StatusSentinel,
    },
    post_restore: {
      base_suite: postRestoreBase,
      r6_tamper_suite: postRestoreR6,
    },
    residue: false,
  }, null, 2)}\n`);
}

try {
  main();
} catch (error) {
  process.stderr.write(`${error.stack || error.message}\n`);
  process.exit(1);
}
