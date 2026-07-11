#!/usr/bin/env node
'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const DIRECTORY =
  '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport-r6';
const BASE_DIRECTORY =
  '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport';
const VERIFIER = `${DIRECTORY}/verify-evidence.cjs`;
const FINAL_REPLAY = `${DIRECTORY}/verify-final-commit-replay.cjs`;
const CAPTURE = `${DIRECTORY}/coverage-capture.v2.json`;
const REPEAT = `${DIRECTORY}/coverage-repeat.v2.json`;
const MANIFEST = `${DIRECTORY}/checksum-layers.v1.json`;
const SUMS = `${DIRECTORY}/R6-SHA256SUMS.txt`;
const CHANGED_R5_VERIFIER =
  `${BASE_DIRECTORY}-r5/verify-coverage-capture.cjs`;

function sha256(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function git(args, cwd, options = {}) {
  return spawnSync('git', args, {
    cwd,
    encoding: 'utf8',
    windowsHide: true,
    maxBuffer: 64 * 1024 * 1024,
    ...options,
  });
}

const rootResult = git(['rev-parse', '--show-toplevel'], process.cwd());
assert.equal(rootResult.status, 0, rootResult.stderr);
const repoRoot = path.resolve(rootResult.stdout.trim());

function absolute(relativePath) {
  return path.join(repoRoot, ...relativePath.split('/'));
}

function jsonBytes(value) {
  return Buffer.from(`${JSON.stringify(value, null, 2)}\n`, 'utf8');
}

function readJson(relativePath) {
  return JSON.parse(fs.readFileSync(absolute(relativePath), 'utf8'));
}

function writeJson(relativePath, value) {
  fs.writeFileSync(absolute(relativePath), jsonBytes(value));
}

function runVerifier() {
  const result = spawnSync(process.execPath, [absolute(VERIFIER)], {
    cwd: repoRoot,
    encoding: 'utf8',
    windowsHide: true,
    maxBuffer: 64 * 1024 * 1024,
  });
  let output = null;
  try {
    output = result.stdout.trim() ? JSON.parse(result.stdout) : null;
  } catch {
    output = null;
  }
  return {
    exit_code: result.status,
    status: output?.status ?? null,
    structural_errors: output?.structural_errors ?? [],
    stderr: result.stderr,
  };
}

function withRestored(relativePaths, execute) {
  const uniquePaths = [...new Set(relativePaths)];
  const originals = new Map(
    uniquePaths.map((relativePath) => [relativePath, fs.readFileSync(absolute(relativePath))]),
  );
  try {
    return execute();
  } finally {
    for (const [relativePath, bytes] of originals) {
      fs.writeFileSync(absolute(relativePath), bytes);
    }
    for (const [relativePath, bytes] of originals) {
      assert.ok(
        fs.readFileSync(absolute(relativePath)).equals(bytes),
        `restore failed: ${relativePath}`,
      );
    }
  }
}

function summarizeFailure(name, result, pattern) {
  const errors = result.structural_errors.join('\n');
  const rejected = result.exit_code !== 0 && result.status === 'FAIL';
  const matched = pattern ? pattern.test(errors) : rejected;
  return {
    name,
    rejected,
    exit_code: result.exit_code,
    status: result.status,
    stderr_bytes: Buffer.byteLength(result.stderr || ''),
    matched_expected_error: matched,
    structural_error_sample: result.structural_errors.slice(0, 4),
  };
}

function refreshRepeatEnvelopeHash(repeat, run, envelopeBytes) {
  repeat.envelopes[run - 1].sha256 = sha256(envelopeBytes);
}

function mutateCaptureCoherently(name, mutate, pattern) {
  const envelopePaths = [1, 2].map(
    (run) => `${DIRECTORY}/coverage-run-${run}.envelope.v2.json`,
  );
  return withRestored([CAPTURE, REPEAT, ...envelopePaths], () => {
    const capture = readJson(CAPTURE);
    const repeat = readJson(REPEAT);
    const envelopes = envelopePaths.map(readJson);
    mutate(capture);
    const captureBytes = jsonBytes(capture);
    fs.writeFileSync(absolute(CAPTURE), captureBytes);
    envelopes.forEach((envelope, index) => {
      envelope.capture_manifest.sha256 = sha256(captureBytes);
      const envelopeBytes = jsonBytes(envelope);
      fs.writeFileSync(absolute(envelopePaths[index]), envelopeBytes);
      refreshRepeatEnvelopeHash(repeat, index + 1, envelopeBytes);
    });
    repeat.capture_manifest.sha256 = sha256(captureBytes);
    writeJson(REPEAT, repeat);
    return summarizeFailure(name, runVerifier(), pattern);
  });
}

function mutateEnvelopeCoherently(name, mutate, pattern, extraPaths = []) {
  const envelopePath = `${DIRECTORY}/coverage-run-1.envelope.v2.json`;
  return withRestored([envelopePath, REPEAT, ...extraPaths], () => {
    const envelope = readJson(envelopePath);
    const repeat = readJson(REPEAT);
    mutate({ envelope, repeat });
    const envelopeBytes = jsonBytes(envelope);
    fs.writeFileSync(absolute(envelopePath), envelopeBytes);
    refreshRepeatEnvelopeHash(repeat, 1, envelopeBytes);
    writeJson(REPEAT, repeat);
    return summarizeFailure(name, runVerifier(), pattern);
  });
}

function mutateSingleJson(name, relativePath, mutate, pattern) {
  return withRestored([relativePath], () => {
    const value = readJson(relativePath);
    mutate(value);
    writeJson(relativePath, value);
    return summarizeFailure(name, runVerifier(), pattern);
  });
}

function checksumPathDigest(manifest) {
  const bytes = Buffer.from(
    manifest.layers.flatMap((layer) =>
      layer.entries.map((entry) =>
        `${layer.name}\0${entry.classification}\0${entry.path}\0${entry.git_blob_oid}\0${entry.sha256}\n`,
      ),
    ).join(''),
    'utf8',
  );
  return sha256(bytes);
}

function checksumSums(manifest, manifestBytes) {
  return Buffer.from(`${[
    ...manifest.layers.flatMap((layer) =>
      layer.entries.map((entry) => `${entry.sha256}  ${entry.path}`),
    ),
    `${sha256(manifestBytes)}  ${MANIFEST}`,
  ].join('\n')}\n`, 'utf8');
}

function coherentOmissionProbe() {
  return withRestored([MANIFEST, SUMS], () => {
    const manifest = readJson(MANIFEST);
    const layer = manifest.layers.find((entry) => entry.name === 'r5-truth-corrections');
    const before = layer.entries.length;
    layer.entries = layer.entries.filter((entry) => entry.path !== CHANGED_R5_VERIFIER);
    assert.equal(layer.entries.length, before - 1);
    manifest.entry_count -= 1;
    manifest.diff_coverage.changed_path_count -= 1;
    manifest.diff_coverage.directly_listed_changed_paths =
      manifest.diff_coverage.directly_listed_changed_paths.filter(
        (entry) => entry !== CHANGED_R5_VERIFIER,
      );
    manifest.path_digest_sha256 = checksumPathDigest(manifest);
    const manifestBytes = jsonBytes(manifest);
    fs.writeFileSync(absolute(MANIFEST), manifestBytes);
    fs.writeFileSync(absolute(SUMS), checksumSums(manifest, manifestBytes));
    return summarizeFailure(
      'coherent changed-path omission',
      runVerifier(),
      /changed path is not directly checksummed: .*verify-coverage-capture\.cjs/,
    );
  });
}

function coherentForgedMetricsProbe() {
  const runPaths = [1, 2].flatMap((run) => [
    `${DIRECTORY}/coverage-run-${run}.stdout.bin`,
    `${DIRECTORY}/coverage-run-${run}.tap`,
    `${DIRECTORY}/coverage-run-${run}.envelope.v2.json`,
  ]);
  return withRestored([...runPaths, REPEAT], () => {
    const repeat = readJson(REPEAT);
    for (const run of [1, 2]) {
      const stdoutPath = `${DIRECTORY}/coverage-run-${run}.stdout.bin`;
      const transcriptPath = `${DIRECTORY}/coverage-run-${run}.tap`;
      const envelopePath = `${DIRECTORY}/coverage-run-${run}.envelope.v2.json`;
      const replaceMetric = (bytes) => {
        const text = bytes.toString('utf8');
        const changed = text.replace(
          /(all files\s+\|\s+)89\.96/,
          (_match, prefix) => `${prefix}99.96`,
        );
        assert.notEqual(changed, text, `metric mutation did not apply: ${run}`);
        return Buffer.from(changed, 'utf8');
      };
      const stdout = replaceMetric(fs.readFileSync(absolute(stdoutPath)));
      const transcript = replaceMetric(fs.readFileSync(absolute(transcriptPath)));
      fs.writeFileSync(absolute(stdoutPath), stdout);
      fs.writeFileSync(absolute(transcriptPath), transcript);
      const envelope = readJson(envelopePath);
      envelope.artifacts.stdout.sha256 = sha256(stdout);
      envelope.artifacts.stdout.byte_length = stdout.length;
      envelope.artifacts.transcript.sha256 = sha256(transcript);
      envelope.artifacts.transcript.byte_length = transcript.length;
      envelope.derived_results.aggregate.line_percent = 99.96;
      const envelopeBytes = jsonBytes(envelope);
      fs.writeFileSync(absolute(envelopePath), envelopeBytes);
      refreshRepeatEnvelopeHash(repeat, run, envelopeBytes);
      repeat.runs[run - 1].aggregate.line_percent = 99.96;
    }
    repeat.dimensions[0].observed_percent = 99.96;
    repeat.dimensions[0].margin_percent = 19.96;
    repeat.threshold.observed_percent = 99.96;
    writeJson(REPEAT, repeat);
    return summarizeFailure(
      'coherent forged two-run metrics/transcripts/envelopes',
      runVerifier(),
      /R6 filesystem bytes differ from -text Git blob/,
    );
  });
}

function prefixProbe(relativeProbe) {
  const source = fs.readFileSync(absolute(FINAL_REPLAY), 'utf8');
  const marker = '  const rejected = [\n';
  assert.ok(source.includes(marker));
  const injected = source.replace(
    marker,
    `${marker}    ${JSON.stringify(relativeProbe)},\n`,
  );
  const tempBase = path.resolve(os.tmpdir());
  const tempPath = path.join(
    tempBase,
    `engram-r6-prefix-${crypto.randomBytes(8).toString('hex')}.cjs`,
  );
  try {
    fs.writeFileSync(tempPath, injected, 'utf8');
    const result = spawnSync(process.execPath, [tempPath, '--self-test-prefix-boundaries'], {
      cwd: repoRoot,
      encoding: 'utf8',
      windowsHide: true,
    });
    return {
      probe: relativeProbe,
      rejected_by_helper: result.status === 0,
      exit_code: result.status,
      stderr: result.stderr.trim(),
    };
  } finally {
    const resolved = path.resolve(tempPath);
    assert.ok(resolved.startsWith(`${tempBase}${path.sep}engram-r6-prefix-`));
    fs.rmSync(resolved, { force: true });
  }
}

function gitDotSegmentProducerProbe() {
  const tempBase = path.resolve(os.tmpdir());
  const indexPath = path.join(
    tempBase,
    `engram-r6-index-${crypto.randomBytes(8).toString('hex')}`,
  );
  const env = { ...process.env, GIT_INDEX_FILE: indexPath };
  try {
    const readTree = git(['read-tree', 'HEAD'], repoRoot, { env });
    assert.equal(readTree.status, 0, readTree.stderr);
    const oidResult = git(['rev-parse', 'HEAD:.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport-r6/.gitattributes'], repoRoot);
    assert.equal(oidResult.status, 0, oidResult.stderr);
    const invalidPath = `${BASE_DIRECTORY}/../internal/payload.cjs`;
    const update = git(
      ['update-index', '--add', '--cacheinfo', '100644', oidResult.stdout.trim(), invalidPath],
      repoRoot,
      { env },
    );
    return {
      path: invalidPath,
      git_rejected: update.status !== 0,
      exit_code: update.status,
      stderr: update.stderr.trim(),
    };
  } finally {
    for (const candidate of [indexPath, `${indexPath}.lock`]) {
      const resolved = path.resolve(candidate);
      assert.ok(resolved.startsWith(`${tempBase}${path.sep}engram-r6-index-`));
      fs.rmSync(resolved, { force: true });
    }
  }
}

const baseline = runVerifier();
assert.equal(baseline.exit_code, 0, baseline.stderr || baseline.structural_errors.join('\n'));
assert.equal(baseline.status, 'PASS');

const probes = [
  mutateCaptureCoherently(
    'capture.command.argv scalar',
    (capture) => { capture.command.argv = '--test'; },
    /capture\.command\.argv is not the exact coverage command/,
  ),
  mutateCaptureCoherently(
    'capture.representation null',
    (capture) => { capture.representation = null; },
    /capture\.representation must be an object/,
  ),
  mutateCaptureCoherently(
    'capture stale source blob',
    (capture) => { capture.sources[0].git_blob_oid = '0'.repeat(40); },
    /capture\.sources\[0\] disagrees with the current Git index blob/,
  ),
  mutateEnvelopeCoherently(
    'process exit_code string coercion',
    ({ envelope, repeat }) => {
      envelope.process.exit_code = '0';
      envelope.derived_results.exit_code = '0';
      repeat.runs[0].envelope_exit_code = '0';
      repeat.runs[0].exit_code = '0';
    },
    /process exit_code must be real zero/,
  ),
  mutateEnvelopeCoherently(
    'process signal synthetic',
    ({ envelope }) => { envelope.process.signal = 'SIGTERM'; },
    /signal must be null/,
  ),
  mutateEnvelopeCoherently(
    'parse_error synthetic object',
    ({ envelope }) => { envelope.parse_error = { message: 'forged' }; },
    /parse_error must be null/,
  ),
  mutateEnvelopeCoherently(
    'stderr forged with refreshed artifact and envelope hashes',
    ({ envelope }, repeat) => {
      void repeat;
      const bytes = Buffer.from('forged stderr\n', 'utf8');
      const stderrPath = `${DIRECTORY}/coverage-run-1.stderr.bin`;
      fs.writeFileSync(absolute(stderrPath), bytes);
      envelope.artifacts.stderr.sha256 = sha256(bytes);
      envelope.artifacts.stderr.byte_length = bytes.length;
    },
    /real stderr must be empty/,
    [`${DIRECTORY}/coverage-run-1.stderr.bin`],
  ),
  mutateSingleJson(
    'repeat.dimensions null',
    REPEAT,
    (repeat) => { repeat.dimensions = null; },
    /repeat\.dimensions must contain all nine measured dimensions/,
  ),
  mutateSingleJson(
    'repeat numeric count string coercion',
    REPEAT,
    (repeat) => { repeat.runs[0].tests = '24'; },
    /repeat\.runs contains hand-entered or stale process\/coverage results/,
  ),
  mutateSingleJson(
    'checksum self_exclusion null',
    MANIFEST,
    (manifest) => { manifest.self_exclusion = null; },
    /checksum manifest metadata is invalid/,
  ),
  mutateSingleJson(
    'checksum entries scalar object',
    MANIFEST,
    (manifest) => { manifest.layers[0].entries = {}; },
    /checksums\.layers\[0\]\.entries must be an array/,
  ),
  mutateSingleJson(
    'checksum duplicate entry',
    MANIFEST,
    (manifest) => { manifest.layers[0].entries.push({ ...manifest.layers[0].entries[0] }); },
    /entries length is invalid|path ordering is invalid/,
  ),
  mutateSingleJson(
    'checksum unknown entry key',
    MANIFEST,
    (manifest) => { manifest.layers[0].entries[0].unknown = true; },
    /contains unknown key: unknown/,
  ),
  coherentOmissionProbe(),
  coherentForgedMetricsProbe(),
];

for (const probe of probes) {
  assert.equal(probe.rejected, true, `${probe.name} did not fail closed`);
  assert.equal(probe.matched_expected_error, true, `${probe.name} missed expected error`);
}

const prefixProbes = [
  prefixProbe(`${BASE_DIRECTORY}-evil/payload.cjs`),
  prefixProbe(`${BASE_DIRECTORY}\\payload.cjs`),
  prefixProbe(`${BASE_DIRECTORY.toUpperCase()}/payload.cjs`),
  prefixProbe(`${BASE_DIRECTORY}/../internal/payload.cjs`),
];
assert.equal(prefixProbes[0].rejected_by_helper, true);
assert.equal(prefixProbes[1].rejected_by_helper, true);
assert.equal(prefixProbes[2].rejected_by_helper, true);
assert.equal(prefixProbes[3].rejected_by_helper, false);
const gitProducer = gitDotSegmentProducerProbe();
assert.equal(gitProducer.git_rejected, true);

const restored = runVerifier();
assert.equal(restored.exit_code, 0, restored.stderr || restored.structural_errors.join('\n'));
assert.equal(restored.status, 'PASS');
const trackedStatus = git(['status', '--porcelain', '--untracked-files=no'], repoRoot);
assert.equal(trackedStatus.status, 0, trackedStatus.stderr);
assert.equal(trackedStatus.stdout.trim(), '');

process.stdout.write(`${JSON.stringify({
  schema_version: 1,
  target: 'a1a3bfeb6546d1f3f24192b1c9f057402b6249a2',
  baseline: { exit_code: baseline.exit_code, status: baseline.status },
  probes,
  prefix_probes: prefixProbes,
  git_dot_segment_producer: gitProducer,
  post_restore: {
    exit_code: restored.exit_code,
    status: restored.status,
    tracked_residue: false,
  },
}, null, 2)}\n`);
