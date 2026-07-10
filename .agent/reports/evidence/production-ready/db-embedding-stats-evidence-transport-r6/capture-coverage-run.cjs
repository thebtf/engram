#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const SLICE = 'DB-EMBEDDING-EVIDENCE-TRANSPORT-R6';
const TARGET_BASE = 'a538f6224ef31f612152470a4ecd45e78ff9d0f2';
const BASE_DIRECTORY =
  '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport';
const EVIDENCE_DIRECTORY = `${BASE_DIRECTORY}-r6`;
const VERIFIER_PATH = `${BASE_DIRECTORY}/verify-manifest.cjs`;
const TEST_PATH = `${BASE_DIRECTORY}/verify-manifest.test.cjs`;
const WRAPPER_PATH = `${EVIDENCE_DIRECTORY}/capture-coverage-run.cjs`;
const CAPTURE_PATH = `${EVIDENCE_DIRECTORY}/coverage-capture.v2.json`;
const COMMAND_ARGS = Object.freeze([
  '--test',
  '--test-concurrency=1',
  '--experimental-test-coverage',
  TEST_PATH,
]);

function sha256(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
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

function relative(repoRoot, relativePath) {
  return path.join(repoRoot, ...relativePath.split('/'));
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

function indexFact(repoRoot, role, relativePath) {
  const blobOid = String(runGit(['rev-parse', `:${relativePath}`], repoRoot, 'utf8')).trim();
  const bytes = Buffer.from(runGit(['cat-file', 'blob', blobOid], repoRoot));
  const endings = analyzeLineEndings(bytes);
  if (
    endings.crlf_pairs !== 0 ||
    endings.bare_carriage_returns !== 0 ||
    endings.lone_lf === 0
  ) {
    throw new Error(`Git index blob must be LF-only: ${relativePath}`);
  }
  return {
    role,
    path: relativePath,
    git_blob_oid: blobOid,
    git_blob_sha256: sha256(bytes),
    byte_length: bytes.length,
    ...endings,
    bytes,
  };
}

function checkoutObservation(repoRoot, fact) {
  const bytes = fs.readFileSync(relative(repoRoot, fact.path));
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
    throw new Error(`checkout bytes are neither exact LF nor pure CRLF-equivalent: ${fact.path}`);
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
  const canonical = Buffer.from(lines.join('\n'), 'utf8');
  return {
    bytes: canonical,
    stats: {
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

function stableFact(fact) {
  const { bytes, ...serializable } = fact;
  return serializable;
}

function writeJson(filePath, value) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  fs.writeFileSync(filePath, `${JSON.stringify(value, null, 2)}\n`, 'utf8');
}

function withCanonicalExecutionClone(repoRoot, indexTree, execute) {
  const tempBase = path.resolve(os.tmpdir());
  const cloneRoot = fs.mkdtempSync(path.join(tempBase, 'engram-r6c-'));
  try {
    runGit(['clone', '--shared', '--no-checkout', '--quiet', repoRoot, cloneRoot], repoRoot);
    runGit(['config', 'core.longpaths', 'true'], cloneRoot);
    runGit(['config', 'core.autocrlf', 'false'], cloneRoot);
    runGit(['read-tree', indexTree], cloneRoot);
    runGit(['checkout-index', '--all', '--force'], cloneRoot);
    return execute(cloneRoot);
  } finally {
    const resolvedClone = path.resolve(cloneRoot);
    if (!resolvedClone.startsWith(`${tempBase}${path.sep}`)) {
      throw new Error(`refusing to clean unexpected canonical clone path: ${resolvedClone}`);
    }
    fs.rmSync(resolvedClone, { recursive: true, force: true });
  }
}

function main() {
  const runArgument = process.argv.find((argument) => argument.startsWith('--run='));
  const run = Number(runArgument?.slice('--run='.length));
  if (run !== 1 && run !== 2) throw new Error('--run must be 1 or 2');

  const repoRoot = path.resolve(
    String(runGit(['rev-parse', '--show-toplevel'], process.cwd(), 'utf8')).trim(),
  );
  const head = String(runGit(['rev-parse', 'HEAD'], repoRoot, 'utf8')).trim();
  if (head !== TARGET_BASE) {
    throw new Error(`capture generation must start at exact rejected target ${TARGET_BASE}`);
  }
  const facts = [
    indexFact(repoRoot, 'verifier', VERIFIER_PATH),
    indexFact(repoRoot, 'test_harness', TEST_PATH),
    indexFact(repoRoot, 'capture_wrapper', WRAPPER_PATH),
  ];
  const observations = facts.slice(0, 2).map((fact) => checkoutObservation(repoRoot, fact));
  const coreAutocrlfResult = spawnSync('git', ['config', '--get', 'core.autocrlf'], {
    cwd: repoRoot,
    encoding: 'utf8',
    windowsHide: true,
  });
  if (![0, 1].includes(coreAutocrlfResult.status)) throw new Error('cannot read core.autocrlf');
  const coreAutocrlf = coreAutocrlfResult.status === 0
    ? coreAutocrlfResult.stdout.trim()
    : null;
  const capture = {
    schema_version: 2,
    slice: SLICE,
    target_base: TARGET_BASE,
    source_head: head,
    execution_index_tree: String(runGit(['write-tree'], repoRoot, 'utf8')).trim(),
    node_version: process.version,
    command: {
      executable: process.execPath,
      executable_basename: path.basename(process.execPath),
      argv: [...COMMAND_ARGS],
      cwd: '.',
    },
    representation: {
      checkout_observation: {
        core_autocrlf: coreAutocrlf,
        files: observations,
      },
      canonical_execution: {
        materialization: 'temporary independent Git clone materialized from the exact index tree',
        repository_topology: 'git-directory',
        core_autocrlf: 'false',
        line_endings: 'lf-only',
        files: facts.slice(0, 2).map(stableFact),
      },
    },
    sources: facts.map(stableFact),
  };
  const capturePath = relative(repoRoot, CAPTURE_PATH);
  if (run === 1) {
    writeJson(capturePath, capture);
  } else {
    const existing = fs.readFileSync(capturePath);
    const expected = Buffer.from(`${JSON.stringify(capture, null, 2)}\n`, 'utf8');
    if (!existing.equals(expected)) {
      throw new Error('run 2 source/capture state differs from run 1');
    }
  }

  let child;
  let startedAt = null;
  let finishedAt = null;
  let executionCwd = null;
  withCanonicalExecutionClone(repoRoot, capture.execution_index_tree, (cloneRoot) => {
    for (const fact of facts.slice(0, 2)) {
      const absolutePath = relative(cloneRoot, fact.path);
      if (!fs.readFileSync(absolutePath).equals(fact.bytes)) {
        throw new Error(`canonical clone did not materialize exact Git-index LF bytes: ${fact.path}`);
      }
    }
    const env = { ...process.env };
    delete env.NODE_V8_COVERAGE;
    executionCwd = cloneRoot;
    startedAt = new Date();
    child = spawnSync(process.execPath, COMMAND_ARGS, {
      cwd: cloneRoot,
      encoding: null,
      env,
      maxBuffer: 64 * 1024 * 1024,
      windowsHide: true,
    });
    finishedAt = new Date();
  });
  if (!child || !startedAt || !finishedAt || !executionCwd) {
    throw new Error('coverage child process did not produce a timed result');
  }

  const stdout = Buffer.from(child.stdout || []);
  const stderr = Buffer.from(child.stderr || []);
  const prefix = `${EVIDENCE_DIRECTORY}/coverage-run-${run}`;
  const stdoutPath = `${prefix}.stdout.bin`;
  const stderrPath = `${prefix}.stderr.bin`;
  const transcriptPath = `${prefix}.tap`;
  fs.writeFileSync(relative(repoRoot, stdoutPath), stdout);
  fs.writeFileSync(relative(repoRoot, stderrPath), stderr);
  let transcript = Buffer.alloc(0);
  let normalization = null;
  let parsed = null;
  let parseError = null;
  try {
    const canonical = canonicalizeTranscript(stdout);
    transcript = canonical.bytes;
    normalization = {
      name: 'crlf-to-lf plus coverage-table trailing-padding trim',
      ...canonical.stats,
    };
    parsed = parseTranscript(transcript);
  } catch (error) {
    parseError = String(error.message || error);
  }
  fs.writeFileSync(relative(repoRoot, transcriptPath), transcript);

  const restored = facts.slice(0, 2).map((fact) => {
    const bytes = fs.readFileSync(relative(repoRoot, fact.path));
    const before = observations.find((entry) => entry.path === fact.path);
    return {
      path: fact.path,
      before_sha256: before.filesystem_sha256,
      after_sha256: sha256(bytes),
      restored: before.filesystem_sha256 === sha256(bytes),
    };
  });
  if (restored.some((entry) => !entry.restored)) {
    throw new Error('canonical execution overlay did not restore the checkout exactly');
  }

  const envelope = {
    schema_version: 2,
    slice: SLICE,
    run,
    capture_manifest: {
      path: CAPTURE_PATH,
      sha256: sha256(fs.readFileSync(capturePath)),
    },
    source: {
      head_commit: head,
      index_tree: capture.execution_index_tree,
      files: facts.map(stableFact),
    },
    process: {
      executable: process.execPath,
      argv: [...COMMAND_ARGS],
      cwd: executionCwd,
      started_at_utc: startedAt.toISOString(),
      finished_at_utc: finishedAt.toISOString(),
      duration_ms: finishedAt.getTime() - startedAt.getTime(),
      exit_code: child.status,
      signal: child.signal,
      spawn_error: child.error ? String(child.error.message || child.error) : null,
    },
    artifacts: {
      stdout: { path: stdoutPath, sha256: sha256(stdout), byte_length: stdout.length },
      stderr: { path: stderrPath, sha256: sha256(stderr), byte_length: stderr.length },
      transcript: {
        path: transcriptPath,
        sha256: sha256(transcript),
        byte_length: transcript.length,
      },
    },
    normalization,
    parse_error: parseError,
    derived_results: parsed ? { exit_code: child.status, ...parsed } : null,
    restoration: restored,
  };
  const envelopePath = `${prefix}.envelope.v2.json`;
  writeJson(relative(repoRoot, envelopePath), envelope);
  process.stdout.write(`${JSON.stringify({ envelope_path: envelopePath, ...envelope }, null, 2)}\n`);
  if (
    child.status !== 0 ||
    child.signal !== null ||
    child.error ||
    parseError !== null ||
    parsed.tests !== 24 ||
    parsed.passed !== 24 ||
    parsed.failed !== 0
  ) {
    process.exit(1);
  }
}

try {
  main();
} catch (error) {
  process.stderr.write(`${error.stack || error.message}\n`);
  process.exit(1);
}
