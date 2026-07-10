#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const TARGET_BASE = 'a538f6224ef31f612152470a4ecd45e78ff9d0f2';
const BASE = '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport';
const R5 = `${BASE}-r5`;
const R6 = `${BASE}-r6`;
const R5_SUMS = `${R5}/R5-SHA256SUMS.txt`;
const MANIFEST = `${R6}/checksum-layers.v1.json`;
const R6_SUMS = `${R6}/R6-SHA256SUMS.txt`;
const SELF_EXCLUSION = Object.freeze([MANIFEST, R6_SUMS]);
const LOAD_BEARING_UNCHANGED = Object.freeze([`${BASE}/verify-manifest.cjs`]);
const LAYERS = Object.freeze([
  Object.freeze({
    name: 'covered-final-blobs',
    paths: Object.freeze([
      `${BASE}/verify-manifest.cjs`,
      `${BASE}/verify-manifest.test.cjs`,
    ]),
  }),
  Object.freeze({
    name: 'real-process-capture',
    paths: Object.freeze([
      `${R6}/coverage-capture.v2.json`,
      `${R6}/coverage-repeat.v2.json`,
      `${R6}/coverage-run-1.envelope.v2.json`,
      `${R6}/coverage-run-1.stderr.bin`,
      `${R6}/coverage-run-1.stdout.bin`,
      `${R6}/coverage-run-1.tap`,
      `${R6}/coverage-run-2.envelope.v2.json`,
      `${R6}/coverage-run-2.stderr.bin`,
      `${R6}/coverage-run-2.stdout.bin`,
      `${R6}/coverage-run-2.tap`,
    ]),
  }),
  Object.freeze({
    name: 'implementation-and-verification',
    paths: Object.freeze([
      `${R6}/.gitattributes`,
      `${R6}/assemble-coverage-repeat.cjs`,
      `${R6}/build-checksums.cjs`,
      `${R6}/capture-coverage-run.cjs`,
      `${R6}/prove-it.cjs`,
      `${R6}/red-reproduction.cjs`,
      `${R6}/verify-evidence.cjs`,
      `${R6}/verify-evidence.test.cjs`,
      `${R6}/verify-final-commit-replay.cjs`,
    ]),
  }),
  Object.freeze({
    name: 'r6-reports-and-tdd',
    paths: Object.freeze([
      `${R6}/maker-report.md`,
      `${R6}/maker-summary.v2.json`,
      '.agent/specs/db-embedding-stats-evidence-transport/evidence/DB-EMBEDDING-EVIDENCE-TRANSPORT-R6.red.json',
      '.agent/specs/db-embedding-stats-evidence-transport/evidence/DB-EMBEDDING-EVIDENCE-TRANSPORT-R6.tdd.json',
    ]),
  }),
  Object.freeze({
    name: 'r5-truth-corrections',
    paths: Object.freeze([
      R5_SUMS,
      `${R5}/maker-report.md`,
      `${R5}/maker-summary.v1.json`,
      `${R5}/verification-matrix.v1.json`,
      `${R5}/verify-coverage-capture.cjs`,
      '.agent/specs/db-embedding-stats-evidence-transport/evidence/DB-EMBEDDING-EVIDENCE-TRANSPORT-R5.tdd.json',
    ]),
  }),
]);

function sha256(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function repoPath(repoRoot, relativePath) {
  return path.join(repoRoot, ...relativePath.split('/'));
}

function git(args, cwd, encoding = null) {
  const result = spawnSync('git', args, { cwd, encoding, windowsHide: true });
  if (result.status !== 0) {
    const stderr = Buffer.isBuffer(result.stderr)
      ? result.stderr.toString('utf8').trim()
      : String(result.stderr || '').trim();
    throw new Error(`git ${args.join(' ')} failed (${result.status}): ${stderr}`);
  }
  return result.stdout;
}

function indexEntry(repoRoot, relativePath, classification = null) {
  const oid = String(git(['rev-parse', `:${relativePath}`], repoRoot, 'utf8')).trim();
  const bytes = Buffer.from(git(['cat-file', 'blob', oid], repoRoot));
  const entry = { path: relativePath, git_blob_oid: oid, sha256: sha256(bytes), byte_length: bytes.length };
  if (classification !== null) entry.classification = classification;
  return entry;
}

function changedPathsFromIndex(repoRoot) {
  const indexTree = String(git(['write-tree'], repoRoot, 'utf8')).trim();
  const output = String(
    git(['diff-tree', '--no-commit-id', '--name-only', '-r', TARGET_BASE, indexTree], repoRoot, 'utf8'),
  ).trim();
  return output ? output.split(/\r?\n/).filter(Boolean).sort() : [];
}

function refreshR5(repoRoot) {
  const sumsPath = repoPath(repoRoot, R5_SUMS);
  const lines = fs.readFileSync(sumsPath, 'utf8').split(/\r?\n/);
  const output = lines.map((line) => {
    const match = line.match(/^[0-9a-f]{64}  (.+)$/);
    if (!match) return line;
    return `${indexEntry(repoRoot, match[1]).sha256}  ${match[1]}`;
  });
  fs.writeFileSync(sumsPath, output.join('\n'), 'utf8');
  process.stdout.write(`${JSON.stringify({ refreshed: R5_SUMS, entries: output.filter((line) => /^[0-9a-f]{64}  /.test(line)).length })}\n`);
}

function buildR6(repoRoot) {
  const changedPaths = changedPathsFromIndex(repoRoot);
  const changedSet = new Set(changedPaths);
  const selfExcludedSet = new Set(SELF_EXCLUSION);
  const loadBearingUnchangedSet = new Set(LOAD_BEARING_UNCHANGED);
  const declaredPaths = LAYERS.flatMap((layer) => layer.paths);
  if (new Set(declaredPaths).size !== declaredPaths.length) {
    throw new Error('checksum layer paths contain duplicates');
  }
  const declaredSet = new Set(declaredPaths);
  const directlyListedChangedPaths = changedPaths.filter((entry) => !selfExcludedSet.has(entry));
  const missingChangedPaths = directlyListedChangedPaths.filter((entry) => !declaredSet.has(entry));
  if (missingChangedPaths.length > 0) {
    throw new Error(`changed paths are not directly checksummed: ${missingChangedPaths.join(', ')}`);
  }
  const unexpectedUnchangedPaths = declaredPaths.filter(
    (entry) => !changedSet.has(entry) && !loadBearingUnchangedSet.has(entry),
  );
  if (unexpectedUnchangedPaths.length > 0) {
    throw new Error(`unchanged checksum entries lack load-bearing classification: ${unexpectedUnchangedPaths.join(', ')}`);
  }
  const missingLoadBearingPaths = LOAD_BEARING_UNCHANGED.filter(
    (entry) => !declaredSet.has(entry) || changedSet.has(entry),
  );
  if (missingLoadBearingPaths.length > 0) {
    throw new Error(`load-bearing unchanged classification is stale: ${missingLoadBearingPaths.join(', ')}`);
  }
  const invalidSelfExclusion = SELF_EXCLUSION.filter((entry) => !changedSet.has(entry));
  if (invalidSelfExclusion.length > 0) {
    throw new Error(`self-excluded checksum paths are not changed: ${invalidSelfExclusion.join(', ')}`);
  }
  const layers = LAYERS.map((layer) => {
    const sorted = [...layer.paths].sort();
    if (JSON.stringify(sorted) !== JSON.stringify(layer.paths)) {
      throw new Error(`checksum layer paths are not lexicographically ordered: ${layer.name}`);
    }
    return {
      name: layer.name,
      entries: layer.paths.map((entry) => indexEntry(
        repoRoot,
        entry,
        changedSet.has(entry) ? 'changed' : 'load-bearing-unchanged',
      )),
    };
  });
  const digestBytes = Buffer.from(
    layers.flatMap((layer) =>
      layer.entries.map((entry) =>
        `${layer.name}\0${entry.classification}\0${entry.path}\0${entry.git_blob_oid}\0${entry.sha256}\n`,
      ),
    ).join(''),
    'utf8',
  );
  const manifest = {
    schema_version: 1,
    slice: 'DB-EMBEDDING-EVIDENCE-TRANSPORT-R6',
    algorithm: 'sha256',
    representation: 'Git index blob bytes; R6 subtree is -text and byte-exact in every checkout',
    ordering: 'layer declaration order; entries lexicographic by repository-relative path',
    self_exclusion: [...SELF_EXCLUSION],
    diff_coverage: {
      target_base: TARGET_BASE,
      comparison: 'target base to current Git index tree; path membership only',
      changed_path_count: changedPaths.length,
      directly_listed_changed_paths: directlyListedChangedPaths,
      load_bearing_unchanged_paths: [...LOAD_BEARING_UNCHANGED],
    },
    layer_count: layers.length,
    entry_count: layers.reduce((count, layer) => count + layer.entries.length, 0),
    path_digest_sha256: sha256(digestBytes),
    layers,
  };
  const manifestBytes = Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`, 'utf8');
  fs.writeFileSync(repoPath(repoRoot, MANIFEST), manifestBytes);
  const flattened = layers.flatMap((layer) => layer.entries);
  const sums = [
    ...flattened.map((entry) => `${entry.sha256}  ${entry.path}`),
    `${sha256(manifestBytes)}  ${MANIFEST}`,
  ];
  fs.writeFileSync(repoPath(repoRoot, R6_SUMS), `${sums.join('\n')}\n`, 'utf8');
  process.stdout.write(`${JSON.stringify({
    manifest: MANIFEST,
    manifest_sha256: sha256(manifestBytes),
    sums: R6_SUMS,
    layers: layers.length,
    entries: flattened.length,
    path_digest_sha256: manifest.path_digest_sha256,
  }, null, 2)}\n`);
}

function main() {
  const mode = process.argv[2];
  if (!['--refresh-r5', '--build-r6'].includes(mode)) {
    throw new Error('usage: build-checksums.cjs --refresh-r5|--build-r6');
  }
  const repoRoot = path.resolve(String(git(['rev-parse', '--show-toplevel'], process.cwd(), 'utf8')).trim());
  if (mode === '--refresh-r5') refreshR5(repoRoot);
  else buildR6(repoRoot);
}

try {
  main();
} catch (error) {
  process.stderr.write(`${error.stack || error.message}\n`);
  process.exit(1);
}
