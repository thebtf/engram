#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const allowedModes = new Set(['materialization', 'coverage-evidence']);
const modeArgument = process.argv.find((argument) => argument.startsWith('--mode='));
const mode = modeArgument ? modeArgument.slice('--mode='.length) : 'coverage-evidence';
const SLICE = 'DB-EMBEDDING-EVIDENCE-TRANSPORT-R5';
const BASE_COMMIT = '369951b61ee07cb0c405558e0f677cd1c9e90362';
const COVERAGE_COMMAND =
  'node.exe --test --test-concurrency=1 --experimental-test-coverage ' +
  '.agent/reports/evidence/production-ready/' +
  'db-embedding-stats-evidence-transport/verify-manifest.test.cjs';
const EVIDENCE_DIRECTORY =
  '.agent/reports/evidence/production-ready/' +
  'db-embedding-stats-evidence-transport-r5';
const CAPTURE_PATH = `${EVIDENCE_DIRECTORY}/coverage-capture.v1.json`;
const COVERAGE_PATH = `${EVIDENCE_DIRECTORY}/coverage-repeat.v1.json`;
const REQUIRED_FILES = Object.freeze([
  Object.freeze({
    role: 'verifier',
    path:
      '.agent/reports/evidence/production-ready/' +
      'db-embedding-stats-evidence-transport/verify-manifest.cjs',
  }),
  Object.freeze({
    role: 'test_harness',
    path:
      '.agent/reports/evidence/production-ready/' +
      'db-embedding-stats-evidence-transport/verify-manifest.test.cjs',
  }),
]);
const CAPTURE_KEYS = Object.freeze([
  'schema_version',
  'slice',
  'materialization',
  'base_commit',
  'core_autocrlf',
  'tracked_eol',
  'line_endings',
  'files',
]);
const FILE_KEYS = Object.freeze([
  'role',
  'path',
  'git_blob_oid',
  'git_blob_sha256',
  'filesystem_sha256',
  'byte_length',
  'crlf_pairs',
  'lone_lf',
  'bare_carriage_returns',
]);
const COVERAGE_KEYS = Object.freeze([
  'schema_version',
  'slice',
  'node_version',
  'command',
  'capture_manifest_path',
  'capture_manifest_sha256',
  'transcript_representation',
  'transcripts',
  'runs',
  'reproducible',
  'threshold',
]);
const TRANSCRIPT_KEYS = Object.freeze(['run', 'path', 'sha256']);
const RUN_KEYS = Object.freeze([
  'run',
  'exit_code',
  'tests',
  'passed',
  'failed',
  'aggregate',
  'verifier',
  'test_harness',
]);
const METRIC_KEYS = Object.freeze(['line_percent', 'branch_percent', 'functions_percent']);
const THRESHOLD_KEYS = Object.freeze(['percent', 'basis', 'observed_percent', 'status']);

if (!allowedModes.has(mode)) {
  process.stderr.write(`unsupported mode: ${mode}\n`);
  process.exit(2);
}

function runGit(args, options = {}) {
  const result = spawnSync('git', args, {
    cwd: options.cwd,
    encoding: options.encoding === undefined ? null : options.encoding,
    maxBuffer: 64 * 1024 * 1024,
    windowsHide: true,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    const stderr = Buffer.isBuffer(result.stderr)
      ? result.stderr.toString('utf8').trim()
      : String(result.stderr || '').trim();
    throw new Error(`git ${args.join(' ')} failed (${result.status}): ${stderr}`);
  }
  return result.stdout;
}

function sha256(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function isPlainObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function validateExactKeys(value, requiredKeys, label, errors) {
  if (!isPlainObject(value)) {
    errors.push(`${label} must be an object`);
    return false;
  }
  const required = new Set(requiredKeys);
  for (const key of requiredKeys) {
    if (!Object.hasOwn(value, key)) errors.push(`${label} is missing required key: ${key}`);
  }
  for (const key of Object.keys(value)) {
    if (!required.has(key)) errors.push(`${label} contains unknown key: ${key}`);
  }
  return true;
}

function analyzeLineEndings(bytes) {
  let crlfPairs = 0;
  let loneLf = 0;
  let bareCarriageReturns = 0;
  for (let index = 0; index < bytes.length; index += 1) {
    if (bytes[index] === 13) {
      if (bytes[index + 1] === 10) {
        crlfPairs += 1;
        index += 1;
      } else {
        bareCarriageReturns += 1;
      }
    } else if (bytes[index] === 10) {
      loneLf += 1;
    }
  }
  return { crlfPairs, loneLf, bareCarriageReturns };
}

function readJson(repoRoot, relativePath) {
  return JSON.parse(fs.readFileSync(path.join(repoRoot, ...relativePath.split('/')), 'utf8'));
}

function getCoreAutocrlf(repoRoot) {
  const result = spawnSync('git', ['config', '--get', 'core.autocrlf'], {
    cwd: repoRoot,
    encoding: 'utf8',
    windowsHide: true,
  });
  if (result.status === 1) return null;
  if (result.status !== 0) throw new Error('git config --get core.autocrlf failed');
  return result.stdout.trim();
}

function verifyMaterialization(repoRoot, errors) {
  const capture = readJson(repoRoot, CAPTURE_PATH);
  const captureIsObject = validateExactKeys(capture, CAPTURE_KEYS, 'capture', errors);
  if (captureIsObject) {
    if (capture.schema_version !== 1) errors.push('capture.schema_version must be 1');
    if (capture.slice !== SLICE) errors.push(`capture.slice must be ${SLICE}`);
    if (capture.materialization !== 'fresh-core-autocrlf-false-lf') {
      errors.push('capture.materialization must be fresh-core-autocrlf-false-lf');
    }
    if (capture.base_commit !== BASE_COMMIT) errors.push('capture.base_commit must equal R4 target');
    if (capture.core_autocrlf !== 'false') errors.push('capture.core_autocrlf must be false');
    if (capture.tracked_eol !== '2/2 i/lf w/lf') {
      errors.push('capture.tracked_eol must be 2/2 i/lf w/lf');
    }
    if (capture.line_endings !== 'lf-only') errors.push('capture.line_endings must be lf-only');
  }
  if (getCoreAutocrlf(repoRoot) !== 'false') {
    errors.push('executing checkout core.autocrlf must be false');
  }

  const declaredFiles = Array.isArray(capture.files) ? capture.files : [];
  if (!Array.isArray(capture.files)) errors.push('capture.files must be an array');
  if (declaredFiles.length !== REQUIRED_FILES.length) {
    errors.push(`capture.files must contain exactly ${REQUIRED_FILES.length} entries`);
  }

  return REQUIRED_FILES.map((required, index) => {
    const entry = declaredFiles[index];
    const label = `capture.files[${index}]`;
    if (!validateExactKeys(entry, FILE_KEYS, label, errors)) {
      return { role: required.role, path: required.path, match: false };
    }
    if (entry.role !== required.role) errors.push(`${label}.role must be ${required.role}`);
    if (entry.path !== required.path) errors.push(`${label}.path must be ${required.path}`);

    const indexBlobOid = runGit(['rev-parse', `:${required.path}`], {
      cwd: repoRoot,
      encoding: 'utf8',
    }).trim();
    const indexBlob = runGit(['cat-file', 'blob', indexBlobOid], { cwd: repoRoot });
    const checkoutBytes = fs.readFileSync(path.join(repoRoot, ...required.path.split('/')));
    const trackedEol = runGit(['ls-files', '--eol', '--', required.path], {
      cwd: repoRoot,
      encoding: 'utf8',
    }).trim();
    const lineEndings = analyzeLineEndings(checkoutBytes);
    const gitBlobSha256 = sha256(indexBlob);
    const filesystemSha256 = sha256(checkoutBytes);
    const lfExact =
      lineEndings.crlfPairs === 0 &&
      lineEndings.bareCarriageReturns === 0 &&
      lineEndings.loneLf > 0 &&
      checkoutBytes.equals(indexBlob) &&
      /^i\/lf\s+w\/lf\s+/.test(trackedEol);
    if (!lfExact) {
      errors.push(
        `coverage file must be LF-only and byte-identical to the Git index: ${required.path}`,
      );
    }
    const declaredMatch =
      entry.git_blob_oid === indexBlobOid &&
      entry.git_blob_sha256 === gitBlobSha256 &&
      entry.filesystem_sha256 === filesystemSha256 &&
      entry.byte_length === checkoutBytes.length &&
      entry.crlf_pairs === lineEndings.crlfPairs &&
      entry.lone_lf === lineEndings.loneLf &&
      entry.bare_carriage_returns === lineEndings.bareCarriageReturns;
    if (!declaredMatch) {
      errors.push(`coverage file declaration disagrees with actual bytes: ${required.path}`);
    }
    return {
      role: required.role,
      path: required.path,
      git_blob_oid: indexBlobOid,
      git_blob_sha256: gitBlobSha256,
      filesystem_sha256: filesystemSha256,
      byte_length: checkoutBytes.length,
      crlf_pairs: lineEndings.crlfPairs,
      lone_lf: lineEndings.loneLf,
      bare_carriage_returns: lineEndings.bareCarriageReturns,
      tracked_eol: trackedEol,
      match: lfExact && declaredMatch,
    };
  });
}

function parseMetricLine(text, filename) {
  const escaped = filename.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = text.match(
    new RegExp(`${escaped}\\s+\\|\\s+([0-9.]+)\\s+\\|\\s+([0-9.]+)\\s+\\|\\s+([0-9.]+)\\s+\\|`),
  );
  if (!match) throw new Error(`coverage transcript is missing metric row: ${filename}`);
  return {
    line_percent: Number(match[1]),
    branch_percent: Number(match[2]),
    functions_percent: Number(match[3]),
  };
}

function parseCoverageTranscript(bytes) {
  const text = bytes.toString('utf8');
  const count = (label) => {
    const match = text.match(new RegExp(`(?:ℹ|#) ${label} ([0-9]+)`));
    if (!match) throw new Error(`coverage transcript is missing ${label} count`);
    return Number(match[1]);
  };
  return {
    exit_code: 0,
    tests: count('tests'),
    passed: count('pass'),
    failed: count('fail'),
    aggregate: parseMetricLine(text, 'all files'),
    verifier: parseMetricLine(text, 'verify-manifest.cjs'),
    test_harness: parseMetricLine(text, 'verify-manifest.test.cjs'),
  };
}

function validateMetricObject(value, label, errors) {
  if (!validateExactKeys(value, METRIC_KEYS, label, errors)) return;
  for (const key of METRIC_KEYS) {
    if (typeof value[key] !== 'number' || !Number.isFinite(value[key])) {
      errors.push(`${label}.${key} must be a finite number`);
    }
  }
}

function verifyCoverageEvidence(repoRoot, errors) {
  const coverage = readJson(repoRoot, COVERAGE_PATH);
  const coverageIsObject = validateExactKeys(coverage, COVERAGE_KEYS, 'coverage', errors);
  if (coverageIsObject) {
    if (coverage.schema_version !== 1) errors.push('coverage.schema_version must be 1');
    if (coverage.slice !== SLICE) errors.push(`coverage.slice must be ${SLICE}`);
    if (coverage.node_version !== process.version) {
      errors.push(`coverage.node_version must equal executing Node ${process.version}`);
    }
    if (coverage.command !== COVERAGE_COMMAND) errors.push('coverage.command is not canonical');
    if (coverage.capture_manifest_path !== CAPTURE_PATH) {
      errors.push(`coverage.capture_manifest_path must be ${CAPTURE_PATH}`);
    }
    if (coverage.transcript_representation !== 'canonical-lf-tap-trim-trailing-table-padding') {
      errors.push('coverage.transcript_representation is not canonical');
    }
  }
  const captureBytes = fs.readFileSync(path.join(repoRoot, ...CAPTURE_PATH.split('/')));
  if (coverage.capture_manifest_sha256 !== sha256(captureBytes)) {
    errors.push('coverage.capture_manifest_sha256 disagrees with capture manifest bytes');
  }

  const transcripts = Array.isArray(coverage.transcripts) ? coverage.transcripts : [];
  const declaredRuns = Array.isArray(coverage.runs) ? coverage.runs : [];
  if (transcripts.length !== 2) errors.push('coverage.transcripts must contain exactly two entries');
  if (declaredRuns.length !== 2) errors.push('coverage.runs must contain exactly two entries');
  const parsedRuns = transcripts.map((entry, index) => {
    const label = `coverage.transcripts[${index}]`;
    validateExactKeys(entry, TRANSCRIPT_KEYS, label, errors);
    const expectedPath = `${EVIDENCE_DIRECTORY}/coverage-run-${index + 1}.tap`;
    if (entry.run !== index + 1) errors.push(`${label}.run must be ${index + 1}`);
    if (entry.path !== expectedPath) errors.push(`${label}.path must be ${expectedPath}`);
    const bytes = fs.readFileSync(path.join(repoRoot, ...expectedPath.split('/')));
    if (entry.sha256 !== sha256(bytes)) errors.push(`${label}.sha256 disagrees with transcript`);
    return { run: index + 1, ...parseCoverageTranscript(bytes) };
  });

  declaredRuns.forEach((run, index) => {
    const label = `coverage.runs[${index}]`;
    if (!validateExactKeys(run, RUN_KEYS, label, errors)) return;
    validateMetricObject(run.aggregate, `${label}.aggregate`, errors);
    validateMetricObject(run.verifier, `${label}.verifier`, errors);
    validateMetricObject(run.test_harness, `${label}.test_harness`, errors);
    if (JSON.stringify(run) !== JSON.stringify(parsedRuns[index])) {
      errors.push(`${label} disagrees with parsed canonical transcript`);
    }
  });
  if (parsedRuns.some((run) => run.tests !== 24 || run.passed !== 24 || run.failed !== 0)) {
    errors.push('coverage transcripts must record 24/24 passing tests');
  }
  if (parsedRuns.length === 2) {
    const firstMetrics = JSON.stringify({
      aggregate: parsedRuns[0].aggregate,
      verifier: parsedRuns[0].verifier,
      test_harness: parsedRuns[0].test_harness,
    });
    const secondMetrics = JSON.stringify({
      aggregate: parsedRuns[1].aggregate,
      verifier: parsedRuns[1].verifier,
      test_harness: parsedRuns[1].test_harness,
    });
    if (firstMetrics !== secondMetrics) errors.push('coverage metrics must be identical across runs');
  }
  if (coverage.reproducible !== true) errors.push('coverage.reproducible must be true');

  const threshold = isPlainObject(coverage.threshold) ? coverage.threshold : {};
  validateExactKeys(threshold, THRESHOLD_KEYS, 'coverage.threshold', errors);
  const observed = parsedRuns[0]?.aggregate?.line_percent;
  if (threshold.percent !== 80) errors.push('coverage.threshold.percent must be 80');
  if (threshold.basis !== 'aggregate line coverage') {
    errors.push('coverage.threshold.basis must be aggregate line coverage');
  }
  if (threshold.observed_percent !== observed) {
    errors.push('coverage.threshold.observed_percent must equal parsed aggregate line percent');
  }
  if (typeof observed !== 'number' || observed < 80) {
    errors.push('coverage aggregate line percent must be at least 80');
  }
  if (threshold.status !== 'PASS') errors.push('coverage.threshold.status must be PASS');

  return { coverage, parsed_runs: parsedRuns };
}

function main() {
  const repoRoot = path.resolve(
    runGit(['rev-parse', '--show-toplevel'], { encoding: 'utf8' }).trim(),
  );
  const structuralErrors = [];
  const entries = verifyMaterialization(repoRoot, structuralErrors);
  const coverageResult = mode === 'coverage-evidence'
    ? verifyCoverageEvidence(repoRoot, structuralErrors)
    : null;
  const matched = entries.filter((entry) => entry.match).length;
  const status = structuralErrors.length === 0 && matched === entries.length ? 'PASS' : 'FAIL';
  const result = {
    schema_version: 1,
    slice: SLICE,
    mode,
    status,
    representation: 'fresh-core-autocrlf-false-lf',
    total: entries.length,
    matched,
    structural_errors: structuralErrors,
    entries,
    coverage: coverageResult,
  };
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
  if (status === 'FAIL') process.exit(1);
}

try {
  main();
} catch (error) {
  process.stderr.write(`${error.stack || error.message}\n`);
  process.exit(1);
}
