#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const SLICE = 'DB-EMBEDDING-EVIDENCE-TRANSPORT-R6';
const TARGET_BASE = 'a538f6224ef31f612152470a4ecd45e78ff9d0f2';
const BASE_DIRECTORY =
  '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport';
const DIRECTORY = `${BASE_DIRECTORY}-r6`;
const VERIFIER_PATH = `${BASE_DIRECTORY}/verify-manifest.cjs`;
const TEST_PATH = `${BASE_DIRECTORY}/verify-manifest.test.cjs`;
const WRAPPER_PATH = `${DIRECTORY}/capture-coverage-run.cjs`;
const CAPTURE_PATH = `${DIRECTORY}/coverage-capture.v2.json`;
const REPEAT_PATH = `${DIRECTORY}/coverage-repeat.v2.json`;
const CHECKSUM_MANIFEST_PATH = `${DIRECTORY}/checksum-layers.v1.json`;
const CHECKSUM_SUMS_PATH = `${DIRECTORY}/R6-SHA256SUMS.txt`;
const CHECKSUM_SELF_EXCLUSION = Object.freeze([CHECKSUM_MANIFEST_PATH, CHECKSUM_SUMS_PATH]);
const LOAD_BEARING_UNCHANGED_PATHS = Object.freeze([VERIFIER_PATH]);
const COMMAND_ARGS = Object.freeze([
  '--test',
  '--test-concurrency=1',
  '--experimental-test-coverage',
  TEST_PATH,
]);
const SOURCE_IDENTITIES = Object.freeze([
  Object.freeze({ role: 'verifier', path: VERIFIER_PATH }),
  Object.freeze({ role: 'test_harness', path: TEST_PATH }),
  Object.freeze({ role: 'capture_wrapper', path: WRAPPER_PATH }),
]);
const METRIC_KEYS = Object.freeze(['line_percent', 'branch_percent', 'functions_percent']);
const METRIC_SCOPES = Object.freeze(['aggregate', 'verifier', 'test_harness']);
const CHECKSUM_LAYERS = Object.freeze([
  Object.freeze({
    name: 'covered-final-blobs',
    paths: Object.freeze([VERIFIER_PATH, TEST_PATH]),
  }),
  Object.freeze({
    name: 'real-process-capture',
    paths: Object.freeze([
      CAPTURE_PATH,
      REPEAT_PATH,
      `${DIRECTORY}/coverage-run-1.envelope.v2.json`,
      `${DIRECTORY}/coverage-run-1.stderr.bin`,
      `${DIRECTORY}/coverage-run-1.stdout.bin`,
      `${DIRECTORY}/coverage-run-1.tap`,
      `${DIRECTORY}/coverage-run-2.envelope.v2.json`,
      `${DIRECTORY}/coverage-run-2.stderr.bin`,
      `${DIRECTORY}/coverage-run-2.stdout.bin`,
      `${DIRECTORY}/coverage-run-2.tap`,
    ]),
  }),
  Object.freeze({
    name: 'implementation-and-verification',
    paths: Object.freeze([
      `${DIRECTORY}/.gitattributes`,
      `${DIRECTORY}/assemble-coverage-repeat.cjs`,
      `${DIRECTORY}/build-checksums.cjs`,
      `${DIRECTORY}/capture-coverage-run.cjs`,
      `${DIRECTORY}/prove-it.cjs`,
      `${DIRECTORY}/red-reproduction.cjs`,
      `${DIRECTORY}/verify-evidence.cjs`,
      `${DIRECTORY}/verify-evidence.test.cjs`,
      `${DIRECTORY}/verify-final-commit-replay.cjs`,
    ]),
  }),
  Object.freeze({
    name: 'r6-reports-and-tdd',
    paths: Object.freeze([
      `${DIRECTORY}/maker-report.md`,
      `${DIRECTORY}/maker-summary.v2.json`,
      '.agent/specs/db-embedding-stats-evidence-transport/evidence/DB-EMBEDDING-EVIDENCE-TRANSPORT-R6.red.json',
      '.agent/specs/db-embedding-stats-evidence-transport/evidence/DB-EMBEDDING-EVIDENCE-TRANSPORT-R6.tdd.json',
    ]),
  }),
  Object.freeze({
    name: 'r5-truth-corrections',
    paths: Object.freeze([
      `${BASE_DIRECTORY}-r5/R5-SHA256SUMS.txt`,
      `${BASE_DIRECTORY}-r5/maker-report.md`,
      `${BASE_DIRECTORY}-r5/maker-summary.v1.json`,
      `${BASE_DIRECTORY}-r5/verification-matrix.v1.json`,
      `${BASE_DIRECTORY}-r5/verify-coverage-capture.cjs`,
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

function runGit(args, cwd, encoding = null) {
  const result = spawnSync('git', args, {
    cwd,
    encoding,
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

function readJson(repoRoot, relativePath) {
  return JSON.parse(fs.readFileSync(repoPath(repoRoot, relativePath), 'utf8'));
}

function isPlainObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function validateExactKeys(value, keys, label, errors) {
  if (!isPlainObject(value)) {
    errors.push(`${label} must be an object`);
    return false;
  }
  const expected = new Set(keys);
  for (const key of keys) {
    if (!Object.hasOwn(value, key)) errors.push(`${label} is missing required key: ${key}`);
  }
  for (const key of Object.keys(value)) {
    if (!expected.has(key)) errors.push(`${label} contains unknown key: ${key}`);
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
  return { crlf_pairs: crlfPairs, lone_lf: loneLf, bare_carriage_returns: bareCarriageReturns };
}

function replaceCrlf(bytes) {
  const output = [];
  for (let index = 0; index < bytes.length; index += 1) {
    if (bytes[index] === 13 && bytes[index + 1] === 10) {
      output.push(10);
      index += 1;
    } else {
      output.push(bytes[index]);
    }
  }
  return Buffer.from(output);
}

function indexFact(repoRoot, identity) {
  const oid = String(runGit(['rev-parse', `:${identity.path}`], repoRoot, 'utf8')).trim();
  const bytes = Buffer.from(runGit(['cat-file', 'blob', oid], repoRoot));
  return {
    role: identity.role,
    path: identity.path,
    git_blob_oid: oid,
    git_blob_sha256: sha256(bytes),
    byte_length: bytes.length,
    ...analyzeLineEndings(bytes),
    bytes,
  };
}

function serializableFact(fact) {
  const { bytes, ...result } = fact;
  return result;
}

function classifyCurrentCheckout(repoRoot, fact, errors) {
  const bytes = fs.readFileSync(repoPath(repoRoot, fact.path));
  const endings = analyzeLineEndings(bytes);
  const normalized = replaceCrlf(bytes);
  let classification = 'invalid';
  if (bytes.equals(fact.bytes)) classification = 'lf-exact';
  else if (endings.bare_carriage_returns === 0 && normalized.equals(fact.bytes)) {
    classification = endings.crlf_pairs > 0 && endings.lone_lf > 0
      ? 'mixed-lf-crlf-equivalent'
      : 'crlf-equivalent';
  }
  if (classification === 'invalid') {
    errors.push(`current checkout is not Git-blob-equivalent: ${fact.path}`);
  }
  return {
    role: fact.role,
    path: fact.path,
    classification,
    filesystem_sha256: sha256(bytes),
    byte_length: bytes.length,
    ...endings,
  };
}

function canonicalizeTranscript(stdoutBytes) {
  const rawEndings = analyzeLineEndings(stdoutBytes);
  if (rawEndings.bare_carriage_returns !== 0) {
    throw new Error('raw stdout contains a bare carriage return');
  }
  const lfBytes = replaceCrlf(stdoutBytes);
  const text = lfBytes.toString('utf8');
  if (!Buffer.from(text, 'utf8').equals(lfBytes)) {
    throw new Error('raw stdout is not valid round-trippable UTF-8');
  }
  let trimmedTrailingBytes = 0;
  const lines = text.split('\n').map((line) => {
    if (!line.includes('|')) return line;
    const trimmed = line.replace(/[ \t]+$/, '');
    trimmedTrailingBytes += Buffer.byteLength(line) - Buffer.byteLength(trimmed);
    return trimmed;
  });
  return {
    bytes: Buffer.from(lines.join('\n'), 'utf8'),
    stats: {
      name: 'crlf-to-lf plus coverage-table trailing-padding trim',
      raw_crlf_pairs_replaced: rawEndings.crlf_pairs,
      table_trailing_padding_bytes_removed: trimmedTrailingBytes,
      semantic_content_changes: 0,
    },
  };
}

function parseMetricLine(text, filename) {
  const escaped = filename.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = text.match(
    new RegExp(`${escaped}\\s+\\|\\s+([0-9.]+)\\s+\\|\\s+([0-9.]+)\\s+\\|\\s+([0-9.]+)\\s+\\|`),
  );
  if (!match) throw new Error(`canonical transcript is missing metric row: ${filename}`);
  return {
    line_percent: Number(match[1]),
    branch_percent: Number(match[2]),
    functions_percent: Number(match[3]),
  };
}

function parseTranscript(bytes) {
  const text = bytes.toString('utf8');
  const count = (label) => {
    const match = text.match(new RegExp(`(?:ℹ|#) ${label} ([0-9]+)`));
    if (!match) throw new Error(`canonical transcript is missing ${label} count`);
    return Number(match[1]);
  };
  return {
    tests: count('tests'),
    passed: count('pass'),
    failed: count('fail'),
    aggregate: parseMetricLine(text, 'all files'),
    verifier: parseMetricLine(text, 'verify-manifest.cjs'),
    test_harness: parseMetricLine(text, 'verify-manifest.test.cjs'),
  };
}

function validateMetric(value, label, errors) {
  if (!validateExactKeys(value, METRIC_KEYS, label, errors)) return;
  for (const key of METRIC_KEYS) {
    if (typeof value[key] !== 'number' || !Number.isFinite(value[key])) {
      errors.push(`${label}.${key} must be a finite number`);
    }
  }
}

function expectedDimensionContract(run) {
  return METRIC_SCOPES.flatMap((scope) => METRIC_KEYS.map((metric) => {
    const observed = run[scope][metric];
    const normative = scope === 'aggregate' && metric === 'line_percent';
    const floor = normative ? 80 : null;
    return {
      scope,
      metric,
      observed_percent: observed,
      normative,
      floor_percent: floor,
      margin_percent: normative ? Number((observed - floor).toFixed(2)) : null,
      status: normative ? (observed >= floor ? 'PASS' : 'FAIL') : 'OBSERVED_NON_NORMATIVE',
    };
  }));
}

function validateFact(value, expected, label, errors) {
  const keys = [
    'role',
    'path',
    'git_blob_oid',
    'git_blob_sha256',
    'byte_length',
    'crlf_pairs',
    'lone_lf',
    'bare_carriage_returns',
  ];
  if (!validateExactKeys(value, keys, label, errors)) return;
  if (JSON.stringify(value) !== JSON.stringify(serializableFact(expected))) {
    errors.push(`${label} disagrees with the current Git index blob`);
  }
  if (value.crlf_pairs !== 0 || value.bare_carriage_returns !== 0 || value.lone_lf === 0) {
    errors.push(`${label} must describe a strict LF-only Git blob`);
  }
}

function validateCapture(repoRoot, capture, facts, errors) {
  const captureKeys = [
    'schema_version',
    'slice',
    'target_base',
    'source_head',
    'execution_index_tree',
    'node_version',
    'command',
    'representation',
    'sources',
  ];
  if (!validateExactKeys(capture, captureKeys, 'capture', errors)) return;
  if (capture.schema_version !== 2) errors.push('capture.schema_version must be 2');
  if (capture.slice !== SLICE) errors.push(`capture.slice must be ${SLICE}`);
  if (capture.target_base !== TARGET_BASE || capture.source_head !== TARGET_BASE) {
    errors.push(`capture must bind to exact rejected target ${TARGET_BASE}`);
  }
  if (!/^[0-9a-f]{40}$/.test(capture.execution_index_tree || '')) {
    errors.push('capture.execution_index_tree must be a full Git tree OID');
  }
  if (capture.node_version !== process.version) {
    errors.push(`capture.node_version must equal ${process.version}`);
  }
  if (validateExactKeys(
    capture.command,
    ['executable', 'executable_basename', 'argv', 'cwd'],
    'capture.command',
    errors,
  )) {
    if (capture.command.executable_basename.toLowerCase() !== 'node.exe') {
      errors.push('capture.command.executable_basename must be node.exe');
    }
    if (JSON.stringify(capture.command.argv) !== JSON.stringify(COMMAND_ARGS)) {
      errors.push('capture.command.argv is not the exact coverage command');
    }
    if (capture.command.cwd !== '.') errors.push('capture.command.cwd must be repository root (.)');
  }

  if (validateExactKeys(
    capture.representation,
    ['checkout_observation', 'canonical_execution'],
    'capture.representation',
    errors,
  )) {
    const observation = capture.representation.checkout_observation;
    if (validateExactKeys(observation, ['core_autocrlf', 'files'], 'checkout_observation', errors)) {
      if (!['true', 'false', 'input', null].includes(observation.core_autocrlf)) {
        errors.push('checkout_observation.core_autocrlf is invalid');
      }
      if (!Array.isArray(observation.files) || observation.files.length !== 2) {
        errors.push('checkout_observation.files must contain exactly two entries');
      } else {
        observation.files.forEach((entry, index) => {
          const label = `checkout_observation.files[${index}]`;
          validateExactKeys(
            entry,
            [
              'role',
              'path',
              'classification',
              'filesystem_sha256',
              'byte_length',
              'crlf_pairs',
              'lone_lf',
              'bare_carriage_returns',
            ],
            label,
            errors,
          );
          if (entry.role !== facts[index].role || entry.path !== facts[index].path) {
            errors.push(`${label} identity is invalid`);
          }
          if (!['lf-exact', 'crlf-equivalent', 'mixed-lf-crlf-equivalent'].includes(entry.classification)) {
            errors.push(`${label}.classification is invalid`);
          }
          if (entry.bare_carriage_returns !== 0) errors.push(`${label} contains bare CR`);
          if (!/^[0-9a-f]{64}$/.test(entry.filesystem_sha256 || '')) {
            errors.push(`${label}.filesystem_sha256 is invalid`);
          }
          const sourceFact = facts[index];
          const totalLogicalLf = entry.crlf_pairs + entry.lone_lf;
          if (
            !Number.isSafeInteger(entry.crlf_pairs) ||
            !Number.isSafeInteger(entry.lone_lf) ||
            entry.crlf_pairs < 0 ||
            entry.lone_lf < 0 ||
            totalLogicalLf !== sourceFact.lone_lf ||
            entry.byte_length !== sourceFact.byte_length + entry.crlf_pairs
          ) {
            errors.push(`${label} line-ending counts do not map to the canonical Git blob`);
          }
          if (
            entry.classification === 'lf-exact' &&
            (entry.crlf_pairs !== 0 ||
              entry.lone_lf !== sourceFact.lone_lf ||
              entry.filesystem_sha256 !== sourceFact.git_blob_sha256)
          ) {
            errors.push(`${label} is falsely labelled lf-exact`);
          }
          if (
            entry.classification === 'crlf-equivalent' &&
            (entry.crlf_pairs !== sourceFact.lone_lf || entry.lone_lf !== 0)
          ) {
            errors.push(`${label} is falsely labelled crlf-equivalent`);
          }
          if (
            entry.classification === 'mixed-lf-crlf-equivalent' &&
            (entry.crlf_pairs === 0 || entry.lone_lf === 0)
          ) {
            errors.push(`${label} is falsely labelled mixed-lf-crlf-equivalent`);
          }
        });
      }
    }
    const execution = capture.representation.canonical_execution;
    if (validateExactKeys(
      execution,
      ['materialization', 'repository_topology', 'core_autocrlf', 'line_endings', 'files'],
      'canonical_execution',
      errors,
    )) {
      if (
        execution.materialization !==
        'temporary independent Git clone materialized from the exact index tree'
      ) {
        errors.push('canonical_execution.materialization is invalid');
      }
      if (execution.repository_topology !== 'git-directory') {
        errors.push('canonical_execution.repository_topology must be git-directory');
      }
      if (execution.core_autocrlf !== 'false') {
        errors.push('canonical_execution.core_autocrlf must be false');
      }
      if (execution.line_endings !== 'lf-only') {
        errors.push('canonical_execution.line_endings must be lf-only');
      }
      if (!Array.isArray(execution.files) || execution.files.length !== 2) {
        errors.push('canonical_execution.files must contain exactly two entries');
      } else {
        execution.files.forEach((entry, index) =>
          validateFact(entry, facts[index], `canonical_execution.files[${index}]`, errors),
        );
      }
    }
  }
  if (!Array.isArray(capture.sources) || capture.sources.length !== facts.length) {
    errors.push(`capture.sources must contain exactly ${facts.length} entries`);
  } else {
    capture.sources.forEach((entry, index) =>
      validateFact(entry, facts[index], `capture.sources[${index}]`, errors),
    );
  }
}

function validateArtifact(repoRoot, value, expectedPath, label, errors) {
  if (!validateExactKeys(value, ['path', 'sha256', 'byte_length'], label, errors)) {
    return Buffer.alloc(0);
  }
  if (value.path !== expectedPath) errors.push(`${label}.path must be ${expectedPath}`);
  let bytes = Buffer.alloc(0);
  try {
    bytes = fs.readFileSync(repoPath(repoRoot, expectedPath));
  } catch (error) {
    errors.push(`${label} cannot be read: ${error.code || error.message}`);
    return bytes;
  }
  if (value.sha256 !== sha256(bytes)) errors.push(`${label}.sha256 disagrees with file bytes`);
  if (value.byte_length !== bytes.length) errors.push(`${label}.byte_length disagrees with file bytes`);
  return bytes;
}

function validateEnvelope(repoRoot, run, capture, captureBytes, facts, errors) {
  const envelopePath = `${DIRECTORY}/coverage-run-${run}.envelope.v2.json`;
  const envelopeBytes = fs.readFileSync(repoPath(repoRoot, envelopePath));
  const envelope = JSON.parse(envelopeBytes);
  const envelopeKeys = [
    'schema_version',
    'slice',
    'run',
    'capture_manifest',
    'source',
    'process',
    'artifacts',
    'normalization',
    'parse_error',
    'derived_results',
    'restoration',
  ];
  validateExactKeys(envelope, envelopeKeys, `envelope[${run}]`, errors);
  if (envelope.schema_version !== 2 || envelope.slice !== SLICE || envelope.run !== run) {
    errors.push(`envelope[${run}] identity is invalid`);
  }
  if (validateExactKeys(
    envelope.capture_manifest,
    ['path', 'sha256'],
    `envelope[${run}].capture_manifest`,
    errors,
  )) {
    if (envelope.capture_manifest.path !== CAPTURE_PATH) {
      errors.push(`envelope[${run}] capture path is invalid`);
    }
    if (envelope.capture_manifest.sha256 !== sha256(captureBytes)) {
      errors.push(`envelope[${run}] capture hash is stale`);
    }
  }
  if (validateExactKeys(
    envelope.source,
    ['head_commit', 'index_tree', 'files'],
    `envelope[${run}].source`,
    errors,
  )) {
    if (envelope.source.head_commit !== TARGET_BASE) {
      errors.push(`envelope[${run}] source head is stale`);
    }
    if (envelope.source.index_tree !== capture.execution_index_tree) {
      errors.push(`envelope[${run}] source tree disagrees with capture`);
    }
    if (!Array.isArray(envelope.source.files) || envelope.source.files.length !== facts.length) {
      errors.push(`envelope[${run}] source files length is invalid`);
    } else {
      envelope.source.files.forEach((entry, index) =>
        validateFact(entry, facts[index], `envelope[${run}].source.files[${index}]`, errors),
      );
    }
  }
  if (validateExactKeys(
    envelope.process,
    [
      'executable',
      'argv',
      'cwd',
      'started_at_utc',
      'finished_at_utc',
      'duration_ms',
      'exit_code',
      'signal',
      'spawn_error',
    ],
    `envelope[${run}].process`,
    errors,
  )) {
    if (envelope.process.executable !== capture.command.executable) {
      errors.push(`envelope[${run}] executable disagrees with capture`);
    }
    if (JSON.stringify(envelope.process.argv) !== JSON.stringify(COMMAND_ARGS)) {
      errors.push(`envelope[${run}] argv is not the exact coverage command`);
    }
    if (!path.isAbsolute(envelope.process.cwd)) {
      errors.push(`envelope[${run}] cwd must be the exact absolute generation cwd`);
    }
    if (!path.basename(envelope.process.cwd).startsWith('engram-r6c-')) {
      errors.push(`envelope[${run}] cwd must identify the independent canonical clone`);
    }
    const started = Date.parse(envelope.process.started_at_utc);
    const finished = Date.parse(envelope.process.finished_at_utc);
    if (!Number.isFinite(started) || !Number.isFinite(finished) || finished < started) {
      errors.push(`envelope[${run}] timestamps are invalid`);
    }
    if (envelope.process.duration_ms !== finished - started) {
      errors.push(`envelope[${run}] duration disagrees with timestamps`);
    }
    if (envelope.process.exit_code !== 0) {
      errors.push(`envelope[${run}] process exit_code must be real zero`);
    }
    if (envelope.process.signal !== null) errors.push(`envelope[${run}] signal must be null`);
    if (envelope.process.spawn_error !== null) errors.push(`envelope[${run}] spawn_error must be null`);
  }

  const prefix = `${DIRECTORY}/coverage-run-${run}`;
  const artifacts = envelope.artifacts;
  validateExactKeys(artifacts, ['stdout', 'stderr', 'transcript'], `envelope[${run}].artifacts`, errors);
  const stdout = validateArtifact(
    repoRoot,
    artifacts.stdout,
    `${prefix}.stdout.bin`,
    `envelope[${run}].artifacts.stdout`,
    errors,
  );
  const stderr = validateArtifact(
    repoRoot,
    artifacts.stderr,
    `${prefix}.stderr.bin`,
    `envelope[${run}].artifacts.stderr`,
    errors,
  );
  const transcript = validateArtifact(
    repoRoot,
    artifacts.transcript,
    `${prefix}.tap`,
    `envelope[${run}].artifacts.transcript`,
    errors,
  );
  if (stderr.length !== 0) errors.push(`envelope[${run}] real stderr must be empty`);
  if (envelope.parse_error !== null) {
    errors.push(`envelope[${run}] parse_error must be null`);
  }

  let parsed = null;
  try {
    const canonical = canonicalizeTranscript(stdout);
    if (!canonical.bytes.equals(transcript)) {
      errors.push(`envelope[${run}] transcript is not the canonical form of raw stdout`);
    }
    if (JSON.stringify(envelope.normalization) !== JSON.stringify(canonical.stats)) {
      errors.push(`envelope[${run}] normalization declaration is hand-entered or stale`);
    }
    parsed = parseTranscript(transcript);
  } catch (error) {
    errors.push(`envelope[${run}] transcript derivation failed: ${error.message}`);
  }
  if (parsed) {
    const expectedDerived = { exit_code: envelope.process.exit_code, ...parsed };
    if (JSON.stringify(envelope.derived_results) !== JSON.stringify(expectedDerived)) {
      errors.push(`envelope[${run}] derived_results are hand-entered or stale`);
    }
    if (parsed.tests !== 24 || parsed.passed !== 24 || parsed.failed !== 0) {
      errors.push(`envelope[${run}] canonical transcript must record 24/24 passing tests`);
    }
    validateMetric(parsed.aggregate, `envelope[${run}].parsed.aggregate`, errors);
    validateMetric(parsed.verifier, `envelope[${run}].parsed.verifier`, errors);
    validateMetric(parsed.test_harness, `envelope[${run}].parsed.test_harness`, errors);
  }

  if (!Array.isArray(envelope.restoration) || envelope.restoration.length !== 2) {
    errors.push(`envelope[${run}].restoration must contain exactly two entries`);
  } else {
    envelope.restoration.forEach((entry, index) => {
      const label = `envelope[${run}].restoration[${index}]`;
      validateExactKeys(entry, ['path', 'before_sha256', 'after_sha256', 'restored'], label, errors);
      const observation = capture.representation.checkout_observation.files[index];
      if (
        entry.path !== facts[index].path ||
        entry.before_sha256 !== observation.filesystem_sha256 ||
        entry.after_sha256 !== observation.filesystem_sha256 ||
        entry.restored !== true
      ) {
        errors.push(`${label} does not prove byte-for-byte restoration`);
      }
    });
  }
  return { envelopePath, envelopeBytes, envelope, parsed };
}

function validateRepeat(repoRoot, repeat, captureBytes, runs, errors) {
  const repeatKeys = [
    'schema_version',
    'slice',
    'capture_manifest',
    'envelopes',
    'runs',
    'reproducible',
    'dimensions',
    'threshold',
  ];
  validateExactKeys(repeat, repeatKeys, 'repeat', errors);
  if (repeat.schema_version !== 2 || repeat.slice !== SLICE) errors.push('repeat identity is invalid');
  validateExactKeys(repeat.capture_manifest, ['path', 'sha256'], 'repeat.capture_manifest', errors);
  if (
    repeat.capture_manifest.path !== CAPTURE_PATH ||
    repeat.capture_manifest.sha256 !== sha256(captureBytes)
  ) {
    errors.push('repeat capture binding is stale');
  }
  if (!Array.isArray(repeat.envelopes) || repeat.envelopes.length !== 2) {
    errors.push('repeat.envelopes must contain exactly two entries');
  } else {
    repeat.envelopes.forEach((entry, index) => {
      const run = index + 1;
      const actual = runs[index];
      validateExactKeys(entry, ['run', 'path', 'sha256'], `repeat.envelopes[${index}]`, errors);
      if (
        entry.run !== run ||
        entry.path !== actual.envelopePath ||
        entry.sha256 !== sha256(actual.envelopeBytes)
      ) {
        errors.push(`repeat.envelopes[${index}] binding is stale`);
      }
    });
  }
  const expectedRuns = runs.map((actual, index) => ({
    run: index + 1,
    envelope_exit_code: actual.envelope.process.exit_code,
    exit_code: actual.envelope.process.exit_code,
    ...(actual.parsed || {}),
  }));
  if (JSON.stringify(repeat.runs) !== JSON.stringify(expectedRuns)) {
    errors.push('repeat.runs contains hand-entered or stale process/coverage results');
  }
  if (repeat.reproducible !== true) errors.push('repeat.reproducible must be true');
  const expectedDimensions = expectedRuns.length > 0
    ? expectedDimensionContract(expectedRuns[0])
    : [];
  if (!Array.isArray(repeat.dimensions) || repeat.dimensions.length !== 9) {
    errors.push('repeat.dimensions must contain all nine measured dimensions');
  } else {
    repeat.dimensions.forEach((entry, index) => validateExactKeys(
      entry,
      [
        'scope',
        'metric',
        'observed_percent',
        'normative',
        'floor_percent',
        'margin_percent',
        'status',
      ],
      `repeat.dimensions[${index}]`,
      errors,
    ));
    if (JSON.stringify(repeat.dimensions) !== JSON.stringify(expectedDimensions)) {
      errors.push('repeat.dimensions is stale or misstates normative coverage floors');
    }
  }
  validateExactKeys(
    repeat.threshold,
    ['percent', 'basis', 'observed_percent', 'status'],
    'repeat.threshold',
    errors,
  );
  const observed = expectedRuns[0]?.aggregate?.line_percent;
  if (
    repeat.threshold.percent !== 80 ||
    repeat.threshold.basis !== 'aggregate line coverage' ||
    repeat.threshold.observed_percent !== observed ||
    repeat.threshold.status !== 'PASS' ||
    typeof observed !== 'number' ||
    observed < 80
  ) {
    errors.push('repeat threshold declaration is invalid or below 80 percent');
  }
  const metrics = (run) => JSON.stringify({
    aggregate: run.aggregate,
    verifier: run.verifier,
    test_harness: run.test_harness,
  });
  if (expectedRuns.length !== 2 || metrics(expectedRuns[0]) !== metrics(expectedRuns[1])) {
    errors.push('two real coverage runs must have identical metrics');
  }
}

function runProductParity(repoRoot, errors) {
  const modes = [
    { mode: 'git-object', expected_total: 7 },
    { mode: 'checkout-lf', expected_total: 7 },
    { mode: 'artifact-files', expected_total: 5 },
  ];
  return modes.map((expectation) => {
    const result = spawnSync(
      process.execPath,
      [repoPath(repoRoot, VERIFIER_PATH), `--mode=${expectation.mode}`],
      { cwd: repoRoot, encoding: 'utf8', windowsHide: true },
    );
    let output = null;
    try {
      output = result.stdout.trim() ? JSON.parse(result.stdout) : null;
    } catch (error) {
      errors.push(`product verifier ${expectation.mode} emitted invalid JSON`);
    }
    if (
      result.status !== 0 ||
      output?.status !== 'PASS' ||
      output?.total !== expectation.expected_total ||
      output?.matched !== expectation.expected_total
    ) {
      errors.push(
        `product verifier ${expectation.mode} must PASS ` +
        `${expectation.expected_total}/${expectation.expected_total}`,
      );
    }
    return {
      mode: expectation.mode,
      exit_code: result.status,
      status: output?.status || null,
      total: output?.total ?? null,
      matched: output?.matched ?? null,
    };
  });
}

function genericIndexEntry(repoRoot, relativePath, classification) {
  const oid = String(runGit(['rev-parse', `:${relativePath}`], repoRoot, 'utf8')).trim();
  const bytes = Buffer.from(runGit(['cat-file', 'blob', oid], repoRoot));
  return {
    path: relativePath,
    git_blob_oid: oid,
    sha256: sha256(bytes),
    byte_length: bytes.length,
    classification,
    bytes,
  };
}

function changedPathsFromIndex(repoRoot) {
  const indexTree = String(runGit(['write-tree'], repoRoot, 'utf8')).trim();
  const output = String(
    runGit(
      ['diff-tree', '--no-commit-id', '--name-only', '-r', TARGET_BASE, indexTree],
      repoRoot,
      'utf8',
    ),
  ).trim();
  return output ? output.split(/\r?\n/).filter(Boolean).sort() : [];
}

function validateChecksums(repoRoot, errors) {
  let manifest;
  let manifestBytes;
  try {
    manifestBytes = fs.readFileSync(repoPath(repoRoot, CHECKSUM_MANIFEST_PATH));
    manifest = JSON.parse(manifestBytes);
  } catch (error) {
    errors.push(`checksum manifest cannot be read: ${error.message}`);
    return null;
  }
  validateExactKeys(
    manifest,
    [
      'schema_version',
      'slice',
      'algorithm',
      'representation',
      'ordering',
      'self_exclusion',
      'diff_coverage',
      'layer_count',
      'entry_count',
      'path_digest_sha256',
      'layers',
    ],
    'checksums',
    errors,
  );
  if (
    manifest.schema_version !== 1 ||
    manifest.slice !== SLICE ||
    manifest.algorithm !== 'sha256' ||
    manifest.representation !==
      'Git index blob bytes; R6 subtree is -text and byte-exact in every checkout' ||
    manifest.ordering !==
      'layer declaration order; entries lexicographic by repository-relative path' ||
    JSON.stringify(manifest.self_exclusion) !== JSON.stringify(CHECKSUM_SELF_EXCLUSION)
  ) {
    errors.push('checksum manifest metadata is invalid');
  }
  const changedPaths = changedPathsFromIndex(repoRoot);
  const changedSet = new Set(changedPaths);
  const selfExcludedSet = new Set(CHECKSUM_SELF_EXCLUSION);
  const directlyListedChangedPaths = changedPaths.filter((entry) => !selfExcludedSet.has(entry));
  const expectedDiffCoverage = {
    target_base: TARGET_BASE,
    comparison: 'target base to current Git index tree; path membership only',
    changed_path_count: changedPaths.length,
    directly_listed_changed_paths: directlyListedChangedPaths,
    load_bearing_unchanged_paths: [...LOAD_BEARING_UNCHANGED_PATHS],
  };
  if (!validateExactKeys(
    manifest.diff_coverage,
    [
      'target_base',
      'comparison',
      'changed_path_count',
      'directly_listed_changed_paths',
      'load_bearing_unchanged_paths',
    ],
    'checksums.diff_coverage',
    errors,
  ) || JSON.stringify(manifest.diff_coverage) !== JSON.stringify(expectedDiffCoverage)) {
    errors.push('checksum diff coverage declaration is stale');
  }
  if (!Array.isArray(manifest.layers) || manifest.layers.length !== CHECKSUM_LAYERS.length) {
    errors.push(`checksum manifest must contain exactly ${CHECKSUM_LAYERS.length} layers`);
    return null;
  }
  const actualEntries = [];
  const directlyValidatedPaths = [];
  manifest.layers.forEach((layer, layerIndex) => {
    const expectedLayer = CHECKSUM_LAYERS[layerIndex];
    const label = `checksums.layers[${layerIndex}]`;
    validateExactKeys(layer, ['name', 'entries'], label, errors);
    if (layer.name !== expectedLayer.name) errors.push(`${label}.name is invalid`);
    if (!Array.isArray(layer.entries)) {
      errors.push(`${label}.entries must be an array`);
      return;
    }
    if (layer.entries.length !== expectedLayer.paths.length) {
      errors.push(`${label}.entries length is invalid`);
    }
    layer.entries.forEach((entry, entryIndex) => {
      const expectedPath = expectedLayer.paths[entryIndex];
      const entryLabel = `${label}.entries[${entryIndex}]`;
      validateExactKeys(
        entry,
        ['path', 'git_blob_oid', 'sha256', 'byte_length', 'classification'],
        entryLabel,
        errors,
      );
      if (expectedPath === undefined || entry.path !== expectedPath) {
        errors.push(`${entryLabel}.path ordering is invalid`);
        return;
      }
      const expectedClassification = changedSet.has(expectedPath)
        ? 'changed'
        : 'load-bearing-unchanged';
      let actual;
      try {
        actual = genericIndexEntry(repoRoot, expectedPath, expectedClassification);
      } catch (error) {
        errors.push(`${entryLabel} cannot resolve Git index blob: ${error.message}`);
        return;
      }
      const { bytes, ...serializable } = actual;
      if (JSON.stringify(entry) !== JSON.stringify(serializable)) {
        errors.push(`${entryLabel} disagrees with Git index blob`);
      }
      directlyValidatedPaths.push(expectedPath);
      if (expectedPath.startsWith(`${DIRECTORY}/`)) {
        try {
          const filesystemBytes = fs.readFileSync(repoPath(repoRoot, expectedPath));
          if (!filesystemBytes.equals(bytes)) {
            errors.push(`${entryLabel} R6 filesystem bytes differ from -text Git blob`);
          }
        } catch (error) {
          errors.push(`${entryLabel} R6 file cannot be read: ${error.message}`);
        }
      }
      actualEntries.push({ layer: expectedLayer.name, ...serializable });
    });
  });
  const duplicatePaths = directlyValidatedPaths.filter(
    (entry, index) => directlyValidatedPaths.indexOf(entry) !== index,
  );
  if (duplicatePaths.length > 0) {
    errors.push(`checksum entries contain duplicate paths: ${[...new Set(duplicatePaths)].join(', ')}`);
  }
  const validatedSet = new Set(directlyValidatedPaths);
  const missingChangedPaths = directlyListedChangedPaths.filter((entry) => !validatedSet.has(entry));
  for (const missing of missingChangedPaths) {
    errors.push(`changed path is not directly checksummed: ${missing}`);
  }
  const unexpectedUnchangedPaths = directlyValidatedPaths.filter(
    (entry) => !changedSet.has(entry) && !LOAD_BEARING_UNCHANGED_PATHS.includes(entry),
  );
  if (unexpectedUnchangedPaths.length > 0) {
    errors.push(
      `unchanged checksum entries lack load-bearing classification: ${unexpectedUnchangedPaths.join(', ')}`,
    );
  }
  for (const expectedExtra of LOAD_BEARING_UNCHANGED_PATHS) {
    if (!validatedSet.has(expectedExtra) || changedSet.has(expectedExtra)) {
      errors.push(`load-bearing unchanged classification is stale: ${expectedExtra}`);
    }
  }
  for (const selfExcluded of CHECKSUM_SELF_EXCLUSION) {
    if (!changedSet.has(selfExcluded)) {
      errors.push(`self-excluded checksum path is not changed: ${selfExcluded}`);
    }
  }
  const digestBytes = Buffer.from(
    actualEntries.map((entry) =>
      `${entry.layer}\0${entry.classification}\0${entry.path}\0${entry.git_blob_oid}\0${entry.sha256}\n`,
    ).join(''),
    'utf8',
  );
  const expectedDigest = sha256(digestBytes);
  if (
    manifest.layer_count !== CHECKSUM_LAYERS.length ||
    manifest.entry_count !== actualEntries.length ||
    manifest.path_digest_sha256 !== expectedDigest
  ) {
    errors.push('checksum counts or ordered path digest are stale');
  }
  try {
    const expectedSums = [
      ...actualEntries.map((entry) => `${entry.sha256}  ${entry.path}`),
      `${sha256(manifestBytes)}  ${CHECKSUM_MANIFEST_PATH}`,
      '',
    ].join('\n');
    const actualSums = fs.readFileSync(repoPath(repoRoot, CHECKSUM_SUMS_PATH), 'utf8');
    if (actualSums !== expectedSums) errors.push('R6-SHA256SUMS.txt is stale or out of order');
  } catch (error) {
    errors.push(`R6-SHA256SUMS.txt cannot be read: ${error.message}`);
  }
  return {
    layers: CHECKSUM_LAYERS.length,
    entries: actualEntries.length,
    path_digest_sha256: expectedDigest,
    manifest_sha256: sha256(manifestBytes),
    diff_coverage: {
      changed_paths: changedPaths.length,
      directly_checksummed_changed_paths: directlyListedChangedPaths.length,
      self_excluded_changed_paths: [...CHECKSUM_SELF_EXCLUSION],
      load_bearing_unchanged_paths: [...LOAD_BEARING_UNCHANGED_PATHS],
    },
  };
}

function main() {
  const repoRoot = path.resolve(
    String(runGit(['rev-parse', '--show-toplevel'], process.cwd(), 'utf8')).trim(),
  );
  const errors = [];
  const facts = SOURCE_IDENTITIES.map((identity) => indexFact(repoRoot, identity));
  const currentRepresentation = facts.slice(0, 2).map((fact) =>
    classifyCurrentCheckout(repoRoot, fact, errors),
  );
  let capture = null;
  let captureBytes = Buffer.alloc(0);
  let repeat = null;
  let runs = [];
  try {
    captureBytes = fs.readFileSync(repoPath(repoRoot, CAPTURE_PATH));
    capture = JSON.parse(captureBytes);
    validateCapture(repoRoot, capture, facts, errors);
  } catch (error) {
    errors.push(`capture verification failed: ${error.message}`);
  }
  if (capture) {
    for (const run of [1, 2]) {
      try {
        runs.push(validateEnvelope(repoRoot, run, capture, captureBytes, facts, errors));
      } catch (error) {
        errors.push(`envelope[${run}] verification failed: ${error.message}`);
      }
    }
  }
  try {
    repeat = readJson(repoRoot, REPEAT_PATH);
    if (runs.length === 2) validateRepeat(repoRoot, repeat, captureBytes, runs, errors);
  } catch (error) {
    errors.push(`repeat verification failed: ${error.message}`);
  }
  const productParity = runProductParity(repoRoot, errors);
  const checksums = validateChecksums(repoRoot, errors);
  const status = errors.length === 0 ? 'PASS' : 'FAIL';
  const result = {
    schema_version: 2,
    slice: SLICE,
    status,
    structural_errors: errors,
    current_representation: currentRepresentation,
    process_runs: runs.map((run) => ({
      run: run.envelope.run,
      exit_code: run.envelope.process.exit_code,
      stdout_sha256: run.envelope.artifacts.stdout.sha256,
      stderr_sha256: run.envelope.artifacts.stderr.sha256,
      transcript_sha256: run.envelope.artifacts.transcript.sha256,
      parsed: run.parsed,
    })),
    product_parity: productParity,
    checksums,
  };
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
  if (status !== 'PASS') process.exit(1);
}

try {
  main();
} catch (error) {
  process.stderr.write(`${error.stack || error.message}\n`);
  process.exit(1);
}
