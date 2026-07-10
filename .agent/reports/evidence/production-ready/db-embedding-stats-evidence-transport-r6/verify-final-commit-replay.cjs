#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const TARGET_BASE = 'a538f6224ef31f612152470a4ecd45e78ff9d0f2';
const BASE_DIRECTORY =
  '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport';
const DIRECTORY = `${BASE_DIRECTORY}-r6`;
const VERIFIER_PATH = `${BASE_DIRECTORY}/verify-manifest.cjs`;
const TEST_PATH = `${BASE_DIRECTORY}/verify-manifest.test.cjs`;
const WRAPPER_PATH = `${DIRECTORY}/capture-coverage-run.cjs`;
const CAPTURE_PATH = `${DIRECTORY}/coverage-capture.v2.json`;
const REPEAT_PATH = `${DIRECTORY}/coverage-repeat.v2.json`;
const COMMAND_ARGS = Object.freeze([
  '--test',
  '--test-concurrency=1',
  '--experimental-test-coverage',
  TEST_PATH,
]);
const SOURCE_PATHS = Object.freeze([VERIFIER_PATH, TEST_PATH, WRAPPER_PATH]);
const METRIC_SCOPES = Object.freeze(['aggregate', 'verifier', 'test_harness']);
const METRIC_KEYS = Object.freeze(['line_percent', 'branch_percent', 'functions_percent']);
const ALLOWED_PREFIXES = Object.freeze([
  `${BASE_DIRECTORY}/`,
  `${BASE_DIRECTORY}-r5/`,
  `${DIRECTORY}/`,
  '.agent/specs/db-embedding-stats-evidence-transport/evidence/',
]);

function sha256(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function run(program, args, cwd, encoding = null, env = process.env) {
  const result = spawnSync(program, args, {
    cwd,
    encoding,
    env,
    maxBuffer: 64 * 1024 * 1024,
    windowsHide: true,
  });
  if (result.error) throw result.error;
  return result;
}

function git(args, cwd, encoding = null) {
  const result = run('git', args, cwd, encoding);
  if (result.status !== 0) {
    const stderr = Buffer.isBuffer(result.stderr)
      ? result.stderr.toString('utf8').trim()
      : String(result.stderr || '').trim();
    throw new Error(`git ${args.join(' ')} failed (${result.status}): ${stderr}`);
  }
  return result.stdout;
}

function repoPath(repoRoot, relativePath) {
  return path.join(repoRoot, ...relativePath.split('/'));
}

function lineEndings(bytes) {
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

function canonicalize(bytes) {
  const output = [];
  for (let index = 0; index < bytes.length; index += 1) {
    if (bytes[index] === 13 && bytes[index + 1] === 10) {
      output.push(10);
      index += 1;
    } else {
      if (bytes[index] === 13) throw new Error('bare CR is not canonicalizable');
      output.push(bytes[index]);
    }
  }
  return Buffer.from(output);
}

function classifyCheckout(repoRoot, fact) {
  const bytes = fs.readFileSync(repoPath(repoRoot, fact.path));
  const normalized = canonicalize(bytes);
  if (!normalized.equals(fact.bytes)) {
    throw new Error(`checkout is not canonical-equivalent to final blob: ${fact.path}`);
  }
  const endings = lineEndings(bytes);
  let classification = 'lf-exact';
  if (!bytes.equals(fact.bytes)) {
    classification = endings.crlf_pairs > 0 && endings.lone_lf > 0
      ? 'mixed-lf-crlf-equivalent'
      : 'crlf-equivalent';
  }
  return {
    path: fact.path,
    classification,
    filesystem_sha256: sha256(bytes),
    byte_length: bytes.length,
    ...endings,
  };
}

function withCanonicalExecutionClone(repoRoot, sourceTree, execute) {
  const tempBase = path.resolve(os.tmpdir());
  const cloneRoot = fs.mkdtempSync(path.join(tempBase, 'engram-r6c-'));
  try {
    const clone = run(
      'git',
      ['clone', '--shared', '--no-checkout', '--quiet', repoRoot, cloneRoot],
      repoRoot,
      'utf8',
    );
    if (clone.status !== 0) throw new Error(`canonical clone failed: ${clone.stderr.trim()}`);
    git(['config', 'core.longpaths', 'true'], cloneRoot, 'utf8');
    git(['config', 'core.autocrlf', 'false'], cloneRoot, 'utf8');
    git(['read-tree', sourceTree], cloneRoot, 'utf8');
    git(['checkout-index', '--all', '--force'], cloneRoot, 'utf8');
    return execute(cloneRoot);
  } finally {
    const resolvedClone = path.resolve(cloneRoot);
    if (!resolvedClone.startsWith(`${tempBase}${path.sep}`)) {
      throw new Error(`refusing to clean unexpected canonical clone path: ${resolvedClone}`);
    }
    fs.rmSync(resolvedClone, { recursive: true, force: true });
  }
}

function finalBlobFact(repoRoot, relativePath) {
  const oid = String(git(['rev-parse', `HEAD:${relativePath}`], repoRoot, 'utf8')).trim();
  const bytes = Buffer.from(git(['cat-file', 'blob', oid], repoRoot));
  const endings = lineEndings(bytes);
  if (endings.crlf_pairs !== 0 || endings.bare_carriage_returns !== 0 || endings.lone_lf === 0) {
    throw new Error(`final committed source blob must be LF-only: ${relativePath}`);
  }
  return {
    path: relativePath,
    git_blob_oid: oid,
    git_blob_sha256: sha256(bytes),
    byte_length: bytes.length,
    ...endings,
    bytes,
  };
}

function parseMetric(text, filename) {
  const escaped = filename.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = text.match(
    new RegExp(`${escaped}\\s+\\|\\s+([0-9.]+)\\s+\\|\\s+([0-9.]+)\\s+\\|\\s+([0-9.]+)\\s+\\|`),
  );
  if (!match) throw new Error(`replay transcript is missing metric row: ${filename}`);
  return {
    line_percent: Number(match[1]),
    branch_percent: Number(match[2]),
    functions_percent: Number(match[3]),
  };
}

function parseReplay(stdout) {
  const text = canonicalize(stdout).toString('utf8');
  const count = (label) => {
    const match = text.match(new RegExp(`(?:ℹ|#) ${label} ([0-9]+)`));
    if (!match) throw new Error(`replay transcript is missing ${label}`);
    return Number(match[1]);
  };
  return {
    tests: count('tests'),
    passed: count('pass'),
    failed: count('fail'),
    aggregate: parseMetric(text, 'all files'),
    verifier: parseMetric(text, 'verify-manifest.cjs'),
    test_harness: parseMetric(text, 'verify-manifest.test.cjs'),
  };
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

function isAllowedChangedPath(relativePath) {
  return ALLOWED_PREFIXES.some((prefix) => relativePath.startsWith(prefix));
}

function runPrefixBoundarySelfTest() {
  const accepted = [
    `${BASE_DIRECTORY}/verify-manifest.test.cjs`,
    `${BASE_DIRECTORY}-r5/maker-report.md`,
    `${DIRECTORY}/maker-report.md`,
    '.agent/specs/db-embedding-stats-evidence-transport/evidence/example.json',
  ];
  const rejected = [
    `${BASE_DIRECTORY}-evil/payload.cjs`,
    `${DIRECTORY}-evil/payload.cjs`,
    '.agent/specs/db-embedding-stats-evidence-transport/evidence-evil/payload.json',
    'internal/embedding/store.go',
  ];
  if (accepted.some((entry) => !isAllowedChangedPath(entry))) {
    throw new Error('prefix-boundary self-test rejected an authorized path');
  }
  if (rejected.some((entry) => isAllowedChangedPath(entry))) {
    throw new Error('prefix-boundary self-test accepted a collision/disallowed path');
  }
  return { status: 'PASS', accepted, rejected };
}

function main() {
  const repoRoot = path.resolve(String(git(['rev-parse', '--show-toplevel'], process.cwd(), 'utf8')).trim());
  const head = String(git(['rev-parse', 'HEAD'], repoRoot, 'utf8')).trim();
  const parents = String(git(['show', '-s', '--format=%P', 'HEAD'], repoRoot, 'utf8')).trim().split(/\s+/);
  if (parents.length !== 1 || parents[0] !== TARGET_BASE) {
    throw new Error(`final maker commit must be a direct child of ${TARGET_BASE}`);
  }
  const clean = String(git(['status', '--porcelain', '--untracked-files=no'], repoRoot, 'utf8'));
  if (clean.trim() !== '') throw new Error(`final replay requires a clean tracked checkout: ${clean.trim()}`);

  const changedPaths = String(
    git(['diff-tree', '--no-commit-id', '--name-only', '-r', 'HEAD'], repoRoot, 'utf8'),
  ).trim().split(/\r?\n/).filter(Boolean).sort();
  runPrefixBoundarySelfTest();
  const disallowed = changedPaths.filter((entry) => !isAllowedChangedPath(entry));
  if (disallowed.length > 0) throw new Error(`final commit changed disallowed paths: ${disallowed.join(', ')}`);
  const pathFacts = changedPaths.map((entry) => ({
    path: entry,
    blob_oid: String(git(['rev-parse', `HEAD:${entry}`], repoRoot, 'utf8')).trim(),
  }));
  const pathDigestBytes = Buffer.from(
    pathFacts.map((entry) => `${entry.path}\0${entry.blob_oid}\n`).join(''),
    'utf8',
  );

  const capture = JSON.parse(fs.readFileSync(repoPath(repoRoot, CAPTURE_PATH), 'utf8'));
  const repeat = JSON.parse(fs.readFileSync(repoPath(repoRoot, REPEAT_PATH), 'utf8'));
  const finalFacts = SOURCE_PATHS.map((entry) => finalBlobFact(repoRoot, entry));
  finalFacts.forEach((fact, index) => {
    const captured = capture.sources[index];
    for (const key of [
      'path',
      'git_blob_oid',
      'git_blob_sha256',
      'byte_length',
      'crlf_pairs',
      'lone_lf',
      'bare_carriage_returns',
    ]) {
      if (captured[key] !== fact[key]) {
        throw new Error(`final committed source differs from captured execution source: ${fact.path}`);
      }
    }
  });
  const hostCheckout = finalFacts.slice(0, 2).map((fact) => classifyCheckout(repoRoot, fact));

  let replay;
  let replayCwd = null;
  const finalTree = String(git(['rev-parse', 'HEAD^{tree}'], repoRoot, 'utf8')).trim();
  withCanonicalExecutionClone(repoRoot, finalTree, (cloneRoot) => {
    for (const fact of finalFacts.slice(0, 2)) {
      if (!fs.readFileSync(repoPath(cloneRoot, fact.path)).equals(fact.bytes)) {
        throw new Error(`canonical clone differs from final source blob: ${fact.path}`);
      }
    }
    const env = { ...process.env };
    delete env.NODE_V8_COVERAGE;
    replayCwd = cloneRoot;
    replay = run(process.execPath, COMMAND_ARGS, cloneRoot, null, env);
  });
  if (!replay) throw new Error('final replay process did not start');
  const stdout = Buffer.from(replay.stdout || []);
  const stderr = Buffer.from(replay.stderr || []);
  const parsed = parseReplay(stdout);
  if (
    replay.status !== 0 ||
    replay.signal !== null ||
    stderr.length !== 0 ||
    parsed.tests !== 24 ||
    parsed.passed !== 24 ||
    parsed.failed !== 0
  ) {
    throw new Error(
      `final replay failed: exit=${replay.status}, signal=${replay.signal}, ` +
      `stderr_bytes=${stderr.length}, counts=${JSON.stringify(parsed)}`,
    );
  }
  const expectedMetrics = repeat.runs[0];
  for (const key of ['aggregate', 'verifier', 'test_harness']) {
    if (JSON.stringify(parsed[key]) !== JSON.stringify(expectedMetrics[key])) {
      throw new Error(`final replay ${key} metrics differ from committed real-run packet`);
    }
  }
  const replayDimensions = expectedDimensionContract(parsed);
  if (JSON.stringify(repeat.dimensions) !== JSON.stringify(replayDimensions)) {
    throw new Error('final replay coverage dimensions/floors differ from committed packet');
  }
  if (replayDimensions.some((entry) => entry.normative && entry.status !== 'PASS')) {
    throw new Error('final replay failed a normative coverage floor');
  }
  const restoredClean = String(
    git(['status', '--porcelain', '--untracked-files=no'], repoRoot, 'utf8'),
  );
  if (restoredClean.trim() !== '') {
    throw new Error(`final replay left tracked residue: ${restoredClean.trim()}`);
  }

  const result = {
    schema_version: 1,
    status: 'PASS',
    final_commit: head,
    direct_parent: parents[0],
    final_tree: finalTree,
    changed_path_count: changedPaths.length,
    changed_path_digest_sha256: sha256(pathDigestBytes),
    changed_paths: pathFacts,
    captured_execution_index_tree: capture.execution_index_tree,
    final_source_blobs_match_capture: true,
    host_checkout: hostCheckout,
    canonical_execution: {
      materialization: 'temporary independent Git clone from final committed tree',
      repository_topology: 'git-directory',
      core_autocrlf: 'false',
      cwd: replayCwd,
    },
    command: {
      executable: process.execPath,
      argv: [...COMMAND_ARGS],
      cwd: repoRoot,
    },
    process: {
      exit_code: replay.status,
      signal: replay.signal,
      stdout_sha256: sha256(stdout),
      stdout_byte_length: stdout.length,
      stderr_sha256: sha256(stderr),
      stderr_byte_length: stderr.length,
    },
    parsed,
    coverage_dimensions: replayDimensions,
    tracked_residue: false,
  };
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
}

try {
  if (process.argv.includes('--self-test-prefix-boundaries')) {
    process.stdout.write(`${JSON.stringify(runPrefixBoundarySelfTest(), null, 2)}\n`);
  } else {
    main();
  }
} catch (error) {
  process.stderr.write(`${error.stack || error.message}\n`);
  process.exit(1);
}
