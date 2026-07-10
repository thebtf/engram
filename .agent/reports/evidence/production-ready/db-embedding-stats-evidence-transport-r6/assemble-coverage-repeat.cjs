#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const SLICE = 'DB-EMBEDDING-EVIDENCE-TRANSPORT-R6';
const DIRECTORY =
  '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport-r6';
const CAPTURE_PATH = `${DIRECTORY}/coverage-capture.v2.json`;
const OUTPUT_PATH = `${DIRECTORY}/coverage-repeat.v2.json`;
const SCOPES = Object.freeze(['aggregate', 'verifier', 'test_harness']);
const METRICS = Object.freeze(['line_percent', 'branch_percent', 'functions_percent']);

function sha256(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function repoPath(repoRoot, relativePath) {
  return path.join(repoRoot, ...relativePath.split('/'));
}

function dimensionContract(run) {
  return SCOPES.flatMap((scope) => METRICS.map((metric) => {
    const observed = run[scope][metric];
    const normative = scope === 'aggregate' && metric === 'line_percent';
    const floor = normative ? 80 : null;
    const margin = normative ? Number((observed - floor).toFixed(2)) : null;
    return {
      scope,
      metric,
      observed_percent: observed,
      normative,
      floor_percent: floor,
      margin_percent: margin,
      status: normative ? (observed >= floor ? 'PASS' : 'FAIL') : 'OBSERVED_NON_NORMATIVE',
    };
  }));
}

function main() {
  const rootResult = spawnSync('git', ['rev-parse', '--show-toplevel'], {
    cwd: process.cwd(),
    encoding: 'utf8',
    windowsHide: true,
  });
  if (rootResult.status !== 0) throw new Error(rootResult.stderr.trim());
  const repoRoot = path.resolve(rootResult.stdout.trim());
  const captureBytes = fs.readFileSync(repoPath(repoRoot, CAPTURE_PATH));
  const envelopes = [1, 2].map((run) => {
    const envelopePath = `${DIRECTORY}/coverage-run-${run}.envelope.v2.json`;
    const bytes = fs.readFileSync(repoPath(repoRoot, envelopePath));
    const envelope = JSON.parse(bytes);
    if (envelope.run !== run || envelope.slice !== SLICE) {
      throw new Error(`run ${run} envelope identity is invalid`);
    }
    return { run, path: envelopePath, sha256: sha256(bytes), envelope };
  });
  const runs = envelopes.map(({ run, envelope }) => ({
    run,
    envelope_exit_code: envelope.process.exit_code,
    ...envelope.derived_results,
  }));
  const metrics = (run) => JSON.stringify({
    aggregate: run.aggregate,
    verifier: run.verifier,
    test_harness: run.test_harness,
  });
  if (metrics(runs[0]) !== metrics(runs[1])) {
    throw new Error('coverage metrics differ across real runs');
  }
  const repeat = {
    schema_version: 2,
    slice: SLICE,
    capture_manifest: { path: CAPTURE_PATH, sha256: sha256(captureBytes) },
    envelopes: envelopes.map(({ envelope, ...entry }) => entry),
    runs,
    reproducible: true,
    dimensions: dimensionContract(runs[0]),
    threshold: {
      percent: 80,
      basis: 'aggregate line coverage',
      observed_percent: runs[0].aggregate.line_percent,
      status: runs[0].aggregate.line_percent >= 80 ? 'PASS' : 'FAIL',
    },
  };
  if (
    repeat.threshold.status !== 'PASS' ||
    repeat.dimensions.some((entry) => entry.normative && entry.status !== 'PASS') ||
    runs.some((run) =>
      run.exit_code !== 0 ||
      run.envelope_exit_code !== 0 ||
      run.tests !== 24 ||
      run.passed !== 24 ||
      run.failed !== 0
    )
  ) {
    throw new Error('coverage run outcome is not releasable');
  }
  fs.writeFileSync(
    repoPath(repoRoot, OUTPUT_PATH),
    `${JSON.stringify(repeat, null, 2)}\n`,
    'utf8',
  );
  process.stdout.write(`${JSON.stringify(repeat, null, 2)}\n`);
}

try {
  main();
} catch (error) {
  process.stderr.write(`${error.stack || error.message}\n`);
  process.exit(1);
}
