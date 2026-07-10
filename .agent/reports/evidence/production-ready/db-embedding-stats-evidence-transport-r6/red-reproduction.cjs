#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const TARGET_COMMIT = 'a538f6224ef31f612152470a4ecd45e78ff9d0f2';
const BASE_EVIDENCE =
  '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport';
const R5_EVIDENCE = `${BASE_EVIDENCE}-r5`;
const TEST_PATH = `${BASE_EVIDENCE}/verify-manifest.test.cjs`;
const VERIFIER_PATH = `${BASE_EVIDENCE}/verify-manifest.cjs`;
const R5_VERIFIER_PATH = `${R5_EVIDENCE}/verify-coverage-capture.cjs`;
const R5_TRANSCRIPT_PATH = `${R5_EVIDENCE}/coverage-run-1.tap`;
const CURRENT_DIR = `${BASE_EVIDENCE}-r6`;
const SENTINEL_BEFORE =
  'structural_errors: structuralErrors,\n' +
  '    validated_entries: structuralErrors.length === 0 ? validatedEntries : [],';
const SENTINEL_AFTER =
  'structural_errors: [],\n' +
  '    validated_entries: validatedEntries,';

function sha256(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function command(program, args, cwd, options = {}) {
  const result = spawnSync(program, args, {
    cwd,
    encoding: options.encoding === undefined ? 'utf8' : options.encoding,
    env: options.env || process.env,
    maxBuffer: 64 * 1024 * 1024,
    windowsHide: true,
  });
  if (result.error) throw result.error;
  return result;
}

function mustPass(program, args, cwd) {
  const result = command(program, args, cwd);
  if (result.status !== 0) {
    throw new Error(
      `${program} ${args.join(' ')} failed (${result.status}): ${String(result.stderr).trim()}`,
    );
  }
  return result;
}

function git(args, cwd) {
  return mustPass('git', args, cwd).stdout.trim();
}

function repoRelative(repoRoot, relativePath) {
  return path.join(repoRoot, ...relativePath.split('/'));
}

function parseTestCounts(output) {
  const count = (label) => {
    const match = output.match(new RegExp(`(?:ℹ|#) ${label} ([0-9]+)`));
    if (!match) throw new Error(`missing TAP count: ${label}`);
    return Number(match[1]);
  };
  return {
    tests: count('tests'),
    passed: count('pass'),
    failed: count('fail'),
  };
}

function main() {
  const repoRoot = path.resolve(git(['rev-parse', '--show-toplevel'], process.cwd()));
  if (git(['rev-parse', 'HEAD'], repoRoot) !== TARGET_COMMIT) {
    throw new Error(`RED reproduction must execute at exact target ${TARGET_COMMIT}`);
  }

  const tempParent = repoRelative(repoRoot, `${CURRENT_DIR}/.red-tmp`);
  fs.mkdirSync(tempParent, { recursive: true });
  const cloneRoot = fs.mkdtempSync(path.join(tempParent, 'lf-clone-'));
  try {
    mustPass('git', ['clone', '--shared', '--no-checkout', repoRoot, cloneRoot], repoRoot);
    mustPass('git', ['config', 'core.longpaths', 'true'], cloneRoot);
    mustPass('git', ['config', 'core.autocrlf', 'false'], cloneRoot);
    mustPass('git', ['checkout', '--detach', TARGET_COMMIT], cloneRoot);

    const lfEol = git(
      ['ls-files', '--eol', '--', VERIFIER_PATH, TEST_PATH],
      cloneRoot,
    ).split(/\r?\n/);
    if (lfEol.some((line) => !/^i\/lf\s+w\/lf\s+/.test(line))) {
      throw new Error(`LF clone did not materialize canonical files: ${lfEol.join(' | ')}`);
    }

    const committedTranscript = fs.readFileSync(repoRelative(cloneRoot, R5_TRANSCRIPT_PATH));
    const malicious = command(
      process.execPath,
      [
        '-e',
        "const fs=require('node:fs');process.stdout.write(fs.readFileSync(process.argv[1]));process.exit(7)",
        repoRelative(cloneRoot, R5_TRANSCRIPT_PATH),
      ],
      cloneRoot,
      { encoding: null },
    );
    if (malicious.status !== 7) {
      throw new Error(`malicious transcript process exited ${malicious.status}, expected 7`);
    }
    if (!Buffer.from(malicious.stdout).equals(committedTranscript)) {
      throw new Error('malicious process stdout differs from the committed R5 transcript');
    }

    const r5Verifier = command(
      process.execPath,
      [repoRelative(cloneRoot, R5_VERIFIER_PATH), '--mode=coverage-evidence'],
      cloneRoot,
    );
    const r5Result = JSON.parse(r5Verifier.stdout);
    if (r5Verifier.status !== 0 || r5Result.status !== 'PASS') {
      throw new Error('R5 baseline verifier unexpectedly rejected its committed packet');
    }
    if (r5Result.coverage.parsed_runs[0].exit_code !== 0) {
      throw new Error('R5 parsed run no longer synthesizes exit_code zero');
    }

    const crlfSuite = command(
      process.execPath,
      ['--test', '--test-concurrency=1', repoRelative(repoRoot, TEST_PATH)],
      repoRoot,
    );
    const crlfCounts = parseTestCounts(`${crlfSuite.stdout}\n${crlfSuite.stderr}`);
    if (
      crlfSuite.status !== 1 ||
      crlfCounts.tests !== 24 ||
      crlfCounts.passed !== 23 ||
      crlfCounts.failed !== 1
    ) {
      throw new Error(
        `CRLF blocker changed: exit=${crlfSuite.status}, counts=${JSON.stringify(crlfCounts)}`,
      );
    }

    const verifierFile = repoRelative(cloneRoot, VERIFIER_PATH);
    const verifierSource = fs.readFileSync(verifierFile, 'utf8');
    const occurrences = verifierSource.split(SENTINEL_BEFORE).length - 1;
    if (occurrences !== 1) {
      throw new Error(`schema sentinel anchor count is ${occurrences}, expected 1`);
    }
    fs.writeFileSync(verifierFile, verifierSource.replace(SENTINEL_BEFORE, SENTINEL_AFTER));

    const sentinelSuite = command(
      process.execPath,
      ['--test', '--test-concurrency=1', repoRelative(cloneRoot, TEST_PATH)],
      cloneRoot,
    );
    const sentinelCounts = parseTestCounts(`${sentinelSuite.stdout}\n${sentinelSuite.stderr}`);
    if (
      sentinelSuite.status !== 1 ||
      sentinelCounts.tests !== 24 ||
      sentinelCounts.passed !== 8 ||
      sentinelCounts.failed !== 16
    ) {
      throw new Error(
        `schema sentinel blocker changed: exit=${sentinelSuite.status}, ` +
          `counts=${JSON.stringify(sentinelCounts)}`,
      );
    }

    const evidence = {
      schema_version: 1,
      task_id: 'DB-EMBEDDING-EVIDENCE-TRANSPORT-R6',
      phase: 'RED',
      target_commit: TARGET_COMMIT,
      node_version: process.version,
      platform: `${process.platform}-${process.arch}`,
      blockers: {
        'ET-R5-001': {
          reproduced: true,
          malicious_process_exit_code: malicious.status,
          malicious_stdout_sha256: sha256(Buffer.from(malicious.stdout)),
          committed_transcript_sha256: sha256(committedTranscript),
          stdout_matches_committed_transcript: true,
          r5_verifier_exit_code: r5Verifier.status,
          r5_verifier_status: r5Result.status,
          r5_synthetic_parsed_exit_code: r5Result.coverage.parsed_runs[0].exit_code,
        },
        'ET-R5-002': {
          reproduced: true,
          checkout_core_autocrlf: git(['config', '--get', 'core.autocrlf'], repoRoot),
          checkout_eol: git(
            ['ls-files', '--eol', '--', VERIFIER_PATH, TEST_PATH],
            repoRoot,
          ).split(/\r?\n/),
          suite_exit_code: crlfSuite.status,
          ...crlfCounts,
        },
        'ET-R5-003': {
          reproduced: true,
          exact_final_r5_test_blob: git(['rev-parse', `HEAD:${TEST_PATH}`], cloneRoot),
          sentinel: 'discard structural errors and release validated subsets',
          suite_exit_code: sentinelSuite.status,
          ...sentinelCounts,
          stale_claim: { passed: 9, failed: 15 },
        },
      },
      temp_cleanup_required: true,
    };
    process.stdout.write(`${JSON.stringify(evidence, null, 2)}\n`);
  } finally {
    const resolvedParent = path.resolve(tempParent);
    const resolvedClone = path.resolve(cloneRoot);
    if (!resolvedClone.startsWith(`${resolvedParent}${path.sep}`)) {
      throw new Error(`refusing to clean unexpected temp path: ${resolvedClone}`);
    }
    fs.rmSync(resolvedClone, { recursive: true, force: true });
    if (fs.existsSync(tempParent) && fs.readdirSync(tempParent).length === 0) {
      fs.rmdirSync(tempParent);
    }
  }
}

try {
  main();
} catch (error) {
  process.stderr.write(`${error.stack || error.message}${os.EOL}`);
  process.exit(1);
}
