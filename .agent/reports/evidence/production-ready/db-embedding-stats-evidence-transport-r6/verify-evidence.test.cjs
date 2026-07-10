#!/usr/bin/env node
'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { test } = require('node:test');

const directory = __dirname;
const repoRoot = path.resolve(
  spawnSync('git', ['rev-parse', '--show-toplevel'], {
    cwd: directory,
    encoding: 'utf8',
    windowsHide: true,
  }).stdout.trim(),
);
const verifierPath = path.join(directory, 'verify-evidence.cjs');
const finalReplayPath = path.join(directory, 'verify-final-commit-replay.cjs');
const repeatPath = path.join(directory, 'coverage-repeat.v2.json');
const envelopePath = path.join(directory, 'coverage-run-1.envelope.v2.json');
const stdoutPath = path.join(directory, 'coverage-run-1.stdout.bin');
const stderrPath = path.join(directory, 'coverage-run-1.stderr.bin');
const transcriptPath = path.join(directory, 'coverage-run-1.tap');
const checksumManifestPath = path.join(directory, 'checksum-layers.v1.json');
const checksumSumsPath = path.join(directory, 'R6-SHA256SUMS.txt');
const changedR5VerifierPath =
  '.agent/reports/evidence/production-ready/' +
  'db-embedding-stats-evidence-transport-r5/verify-coverage-capture.cjs';

function sha256(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function jsonBytes(value) {
  return Buffer.from(`${JSON.stringify(value, null, 2)}\n`, 'utf8');
}

function runVerifier() {
  const result = spawnSync(process.execPath, [verifierPath], {
    cwd: repoRoot,
    encoding: 'utf8',
    windowsHide: true,
  });
  return {
    exit_code: result.status,
    output: result.stdout.trim() ? JSON.parse(result.stdout) : null,
    stderr: result.stderr.trim(),
  };
}

function expectPass(result) {
  assert.equal(result.exit_code, 0, result.stderr || JSON.stringify(result.output));
  assert.equal(result.output?.status, 'PASS');
  assert.deepEqual(result.output?.structural_errors, []);
}

function expectFail(result, pattern) {
  assert.notEqual(result.exit_code, 0, 'attack must return a non-zero exit code');
  assert.equal(result.output?.status, 'FAIL', result.stderr || 'attack must emit FAIL');
  assert.ok(result.output.structural_errors.length > 0, 'attack must emit a structural error');
  if (pattern) {
    assert.match(result.output.structural_errors.join('\n'), pattern);
  }
}

function withRestored(paths, execute) {
  const originals = new Map(paths.map((filePath) => [filePath, fs.readFileSync(filePath)]));
  try {
    return execute();
  } finally {
    for (const [filePath, bytes] of originals) fs.writeFileSync(filePath, bytes);
  }
}

function withEnvelopeMutation(mutate, verify, extraPaths = []) {
  return withRestored([envelopePath, repeatPath, ...extraPaths], () => {
    const envelope = JSON.parse(fs.readFileSync(envelopePath, 'utf8'));
    const repeat = JSON.parse(fs.readFileSync(repeatPath, 'utf8'));
    mutate({ envelope, repeat });
    const envelopeBytes = jsonBytes(envelope);
    fs.writeFileSync(envelopePath, envelopeBytes);
    repeat.envelopes[0].sha256 = sha256(envelopeBytes);
    fs.writeFileSync(repeatPath, jsonBytes(repeat));
    return verify();
  });
}

function updateArtifact(envelope, name, filePath, bytes) {
  fs.writeFileSync(filePath, bytes);
  envelope.artifacts[name].sha256 = sha256(bytes);
  envelope.artifacts[name].byte_length = bytes.length;
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
    `${sha256(manifestBytes)}  ${path.relative(repoRoot, checksumManifestPath).split(path.sep).join('/')}`,
  ].join('\n')}\n`, 'utf8');
}

test('real process envelopes, raw streams, transcripts, and product parity pass', () => {
  const result = runVerifier();
  expectPass(result);
  assert.equal(result.output.process_runs.length, 2);
  assert.deepEqual(result.output.process_runs.map((run) => run.exit_code), [0, 0]);
  assert.deepEqual(
    result.output.process_runs.map((run) => [run.parsed.tests, run.parsed.passed, run.parsed.failed]),
    [[24, 24, 0], [24, 24, 0]],
  );
  assert.deepEqual(
    result.output.product_parity.map((entry) => [entry.mode, entry.matched, entry.total]),
    [['git-object', 7, 7], ['checkout-lf', 7, 7], ['artifact-files', 5, 5]],
  );
  const boundaryProbe = spawnSync(
    process.execPath,
    [finalReplayPath, '--self-test-prefix-boundaries'],
    { cwd: repoRoot, encoding: 'utf8', windowsHide: true },
  );
  assert.equal(boundaryProbe.status, 0, boundaryProbe.stderr);
  const boundaryResult = JSON.parse(boundaryProbe.stdout);
  assert.equal(boundaryResult.status, 'PASS');
  assert.ok(boundaryResult.rejected.some((entry) => entry.includes('transport-evil')));
});

test('parseable TAP cannot hide a non-zero real process exit', () => {
  const result = withEnvelopeMutation(({ envelope, repeat }) => {
    envelope.process.exit_code = 1;
    envelope.derived_results.exit_code = 1;
    repeat.runs[0].envelope_exit_code = 1;
    repeat.runs[0].exit_code = 1;
  }, runVerifier);
  expectFail(result, /process exit_code must be real zero/);
});

test('a stale source envelope is rejected even with refreshed envelope hash', () => {
  const result = withEnvelopeMutation(({ envelope }) => {
    envelope.source.files[0].git_blob_oid = '0'.repeat(40);
  }, runVerifier);
  expectFail(result, /disagrees with the current Git index blob/);
});

test('hand-entered metrics are rejected after envelope and repeat hashes are refreshed', () => {
  const result = withEnvelopeMutation(({ envelope, repeat }) => {
    envelope.derived_results.aggregate.line_percent = 99.99;
    repeat.runs[0].aggregate.line_percent = 99.99;
    repeat.threshold.observed_percent = 99.99;
  }, runVerifier);
  expectFail(result, /derived_results are hand-entered or stale/);
});

test('changed stdout is rejected after its declared hashes are refreshed', () => {
  const result = withEnvelopeMutation(({ envelope }) => {
    const stdout = fs.readFileSync(stdoutPath, 'utf8');
    assert.match(stdout, /ℹ tests 24/);
    updateArtifact(
      envelope,
      'stdout',
      stdoutPath,
      Buffer.from(stdout.replace('ℹ tests 24', 'ℹ tests 25'), 'utf8'),
    );
  }, runVerifier, [stdoutPath]);
  expectFail(result, /transcript is not the canonical form of raw stdout/);
});

test('changed stderr is rejected after its declared hashes are refreshed', () => {
  const result = withEnvelopeMutation(({ envelope }) => {
    updateArtifact(envelope, 'stderr', stderrPath, Buffer.from('synthetic stderr\n', 'utf8'));
  }, runVerifier, [stderrPath]);
  expectFail(result, /real stderr must be empty/);
});

test('changed transcript is rejected after its declared hashes are refreshed', () => {
  const result = withEnvelopeMutation(({ envelope }) => {
    const transcript = fs.readFileSync(transcriptPath, 'utf8');
    assert.match(transcript, /ℹ pass 24/);
    updateArtifact(
      envelope,
      'transcript',
      transcriptPath,
      Buffer.from(transcript.replace('ℹ pass 24', 'ℹ pass 23'), 'utf8'),
    );
  }, runVerifier, [transcriptPath]);
  expectFail(result, /transcript is not the canonical form of raw stdout/);
});

test('missing raw stdout is rejected fail-closed', () => {
  const heldPath = `${stdoutPath}.held`;
  assert.equal(fs.existsSync(heldPath), false);
  fs.renameSync(stdoutPath, heldPath);
  try {
    expectFail(runVerifier(), /cannot be read/);
  } finally {
    fs.renameSync(heldPath, stdoutPath);
  }
});

test('coverage-table padding normalization is semantic-preserving and exactly counted', () => {
  const envelope = JSON.parse(fs.readFileSync(envelopePath, 'utf8'));
  const stdout = fs.readFileSync(stdoutPath);
  const transcript = fs.readFileSync(transcriptPath);
  assert.equal(envelope.normalization.semantic_content_changes, 0);
  assert.ok(
    Number.isSafeInteger(envelope.normalization.table_trailing_padding_bytes_removed) &&
      envelope.normalization.table_trailing_padding_bytes_removed > 0,
    'normalization must record a positive exact padding-byte count',
  );
  assert.equal(
    stdout.length - transcript.length,
    envelope.normalization.table_trailing_padding_bytes_removed,
  );
  const result = runVerifier();
  expectPass(result);
});

test('semantic transcript mutation remains rejected with all local hashes refreshed', () => {
  const result = withEnvelopeMutation(({ envelope, repeat }) => {
    const stdout = fs.readFileSync(stdoutPath, 'utf8');
    const transcript = fs.readFileSync(transcriptPath, 'utf8');
    assert.match(stdout, /ℹ tests 24/);
    assert.match(transcript, /ℹ tests 24/);
    updateArtifact(
      envelope,
      'stdout',
      stdoutPath,
      Buffer.from(stdout.replace('ℹ tests 24', 'ℹ tests 25'), 'utf8'),
    );
    updateArtifact(
      envelope,
      'transcript',
      transcriptPath,
      Buffer.from(transcript.replace('ℹ tests 24', 'ℹ tests 25'), 'utf8'),
    );
    envelope.derived_results.tests = 25;
    repeat.runs[0].tests = 25;
  }, runVerifier, [stdoutPath, transcriptPath]);
  expectFail(result, /canonical transcript must record 24\/24 passing tests/);
});

test('unknown envelope fields are rejected fail-closed', () => {
  const result = withEnvelopeMutation(({ envelope }) => {
    envelope.hand_entered_success = true;
  }, runVerifier);
  expectFail(result, /contains unknown key: hand_entered_success/);
});

test('a changed path cannot be omitted after locally refreshing checksum declarations', () => {
  const result = withRestored([checksumManifestPath, checksumSumsPath], () => {
    const manifest = JSON.parse(fs.readFileSync(checksumManifestPath, 'utf8'));
    const layer = manifest.layers.find((entry) => entry.name === 'r5-truth-corrections');
    assert.ok(layer, 'r5 checksum layer must exist');
    const before = layer.entries.length;
    layer.entries = layer.entries.filter((entry) => entry.path !== changedR5VerifierPath);
    assert.equal(layer.entries.length, before - 1, 'attack must remove the changed R5 verifier');
    manifest.entry_count -= 1;
    manifest.diff_coverage.changed_path_count -= 1;
    manifest.diff_coverage.directly_listed_changed_paths =
      manifest.diff_coverage.directly_listed_changed_paths.filter(
        (entry) => entry !== changedR5VerifierPath,
      );
    manifest.path_digest_sha256 = checksumPathDigest(manifest);
    const manifestBytes = jsonBytes(manifest);
    fs.writeFileSync(checksumManifestPath, manifestBytes);
    fs.writeFileSync(checksumSumsPath, checksumSums(manifest, manifestBytes));
    return runVerifier();
  });
  expectFail(result, /changed path is not directly checksummed: .*verify-coverage-capture\.cjs/);
});
