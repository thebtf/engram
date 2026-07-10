#!/usr/bin/env node
'use strict';

// Behavioral signal: release-evidence-false-pass-rate.
// Measurement: deterministic local mutation suite; target: zero false PASS results.
// Source: DB-EMBEDDING-EVIDENCE-TRANSPORT independent checker findings ET-001/ET-002.

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { after, test } = require('node:test');

const scriptDirectory = __dirname;
const verifierPath = path.join(scriptDirectory, 'verify-manifest.cjs');
const artifactManifestPath = path.join(scriptDirectory, 'ARTIFACTS.sha256');
const contractPath = path.join(scriptDirectory, 'content-manifest.v1.json');
const testPath = __filename;
const repoRoot = path.resolve(
  spawnSync('git', ['rev-parse', '--show-toplevel'], {
    cwd: scriptDirectory,
    encoding: 'utf8',
    windowsHide: true,
  }).stdout.trim(),
);

const originalBytes = new Map([
  [artifactManifestPath, fs.readFileSync(artifactManifestPath)],
  [contractPath, fs.readFileSync(contractPath)],
]);

function restoreOriginals() {
  for (const [filePath, bytes] of originalBytes) {
    fs.writeFileSync(filePath, bytes);
  }
}

after(restoreOriginals);

function withMutation(filePath, mutate, verify) {
  const original = fs.readFileSync(filePath);
  try {
    fs.writeFileSync(filePath, mutate(Buffer.from(original)));
    return verify();
  } finally {
    fs.writeFileSync(filePath, original);
  }
}

function runVerifier(mode) {
  const result = spawnSync(process.execPath, [verifierPath, `--mode=${mode}`], {
    cwd: repoRoot,
    encoding: 'utf8',
    windowsHide: true,
  });
  let output = null;
  if (result.stdout.trim()) {
    output = JSON.parse(result.stdout);
  }
  return {
    exit_code: result.status,
    output,
    stderr: result.stderr.trim(),
  };
}

function expectFailClosed(result) {
  assert.notEqual(result.exit_code, 0, 'mutation must return a non-zero exit code');
  assert.equal(result.output?.status, 'FAIL', result.stderr || 'mutation must emit FAIL');
  assert.ok(
    Array.isArray(result.output?.structural_errors) &&
      result.output.structural_errors.length > 0,
    'mutation must emit at least one structural error',
  );
}

function mutateArtifactManifest(mutator) {
  return (bytes) => {
    const text = bytes.toString('utf8');
    const eol = text.includes('\r\n') ? '\r\n' : '\n';
    const lines = text.split(/\r?\n/);
    if (lines.at(-1) === '') lines.pop();
    mutator(lines);
    return Buffer.from(`${lines.join(eol)}${eol}`, 'utf8');
  };
}

function dataLineIndexes(lines) {
  return lines
    .map((line, index) => ({ line, index }))
    .filter(({ line }) => /^[0-9a-f]{64}  /.test(line))
    .map(({ index }) => index);
}

function canonicalLf(bytes) {
  const output = [];
  for (let index = 0; index < bytes.length; index += 1) {
    if (bytes[index] === 13 && bytes[index + 1] === 10) {
      output.push(10);
      index += 1;
    } else {
      assert.notEqual(bytes[index], 13, 'fixture must not contain a bare CR');
      output.push(bytes[index]);
    }
  }
  return Buffer.from(output);
}

function sha256(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function mutateContract(mutator) {
  return (bytes) => {
    const text = bytes.toString('utf8');
    const eol = text.includes('\r\n') ? '\r\n' : '\n';
    const contract = JSON.parse(text);
    mutator(contract);
    return Buffer.from(`${JSON.stringify(contract, null, 2).replace(/\n/g, eol)}${eol}`);
  };
}

test('artifact manifest rejects a header-only zero-entry set', () => {
  const result = withMutation(
    artifactManifestPath,
    mutateArtifactManifest((lines) => {
      for (const index of dataLineIndexes(lines).reverse()) lines.splice(index, 1);
    }),
    () => runVerifier('artifact-files'),
  );
  expectFailClosed(result);
});

test('artifact manifest rejects a missing required entry', () => {
  const result = withMutation(
    artifactManifestPath,
    mutateArtifactManifest((lines) => {
      const index = lines.findIndex((line) => line.endsWith('/content-manifest.v1.json'));
      assert.notEqual(index, -1);
      lines.splice(index, 1);
    }),
    () => runVerifier('artifact-files'),
  );
  expectFailClosed(result);
});

test('artifact manifest rejects an extra entry', () => {
  const testRelativePath = path.relative(repoRoot, testPath).split(path.sep).join('/');
  const testHash = sha256(canonicalLf(fs.readFileSync(testPath)));
  const result = withMutation(
    artifactManifestPath,
    mutateArtifactManifest((lines) => {
      lines.push(`${testHash}  ${testRelativePath}`);
    }),
    () => runVerifier('artifact-files'),
  );
  expectFailClosed(result);
});

test('artifact manifest rejects a duplicate entry', () => {
  const result = withMutation(
    artifactManifestPath,
    mutateArtifactManifest((lines) => {
      const firstDataLine = lines.find((line) => /^[0-9a-f]{64}  /.test(line));
      assert.ok(firstDataLine);
      lines.push(firstDataLine);
    }),
    () => runVerifier('artifact-files'),
  );
  expectFailClosed(result);
});

test('artifact manifest rejects dot-segment traversal outside the evidence namespace', () => {
  const traversalPath =
    '.agent/reports/evidence/production-ready/' +
    'db-embedding-stats-evidence-transport/../../../../../internal/embedding/store.go';
  const storeHash = '7bfb06dfc0dda792147d5e2df9d2fe68b59edaac55d2396dece1b8a8a09eee5f';
  const result = withMutation(
    artifactManifestPath,
    mutateArtifactManifest((lines) => {
      const index = lines.findIndex((line) => line.endsWith('/content-manifest.v1.json'));
      assert.notEqual(index, -1);
      lines[index] = `${storeHash}  ${traversalPath}`;
    }),
    () => runVerifier('artifact-files'),
  );
  expectFailClosed(result);
});

test('artifact manifest rejects a non-canonical dot-segment alias', () => {
  const result = withMutation(
    artifactManifestPath,
    mutateArtifactManifest((lines) => {
      const index = lines.findIndex((line) => line.endsWith('/content-manifest.v1.json'));
      assert.notEqual(index, -1);
      lines[index] = lines[index].replace(
        '/content-manifest.v1.json',
        '/./content-manifest.v1.json',
      );
    }),
    () => runVerifier('artifact-files'),
  );
  expectFailClosed(result);
});

test('artifact manifest rejects absolute and backslash-separated paths', async (t) => {
  await t.test('absolute path', () => {
    const result = withMutation(
      artifactManifestPath,
      mutateArtifactManifest((lines) => {
        const index = lines.findIndex((line) => line.endsWith('/content-manifest.v1.json'));
        assert.notEqual(index, -1);
        lines[index] = lines[index].replace(
          '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/content-manifest.v1.json',
          path.resolve(contractPath),
        );
      }),
      () => runVerifier('artifact-files'),
    );
    expectFailClosed(result);
  });

  await t.test('backslash-separated path', () => {
    const result = withMutation(
      artifactManifestPath,
      mutateArtifactManifest((lines) => {
        const index = lines.findIndex((line) => line.endsWith('/content-manifest.v1.json'));
        assert.notEqual(index, -1);
        lines[index] = lines[index].replaceAll('/', '\\');
      }),
      () => runVerifier('artifact-files'),
    );
    expectFailClosed(result);
  });
});

test('contract rejects unsupported checkout-equivalence policy values', async (t) => {
  const cases = [
    ['bare_cr', (contract) => { contract.representation.checkout_equivalence.bare_cr = 'accept'; }],
    ['transform', (contract) => { contract.representation.checkout_equivalence.transform = 'identity'; }],
    ['required_result', (contract) => {
      contract.representation.checkout_equivalence.required_result = 'checkout bytes';
    }],
  ];
  for (const [name, mutate] of cases) {
    await t.test(name, () => {
      const result = withMutation(
        contractPath,
        mutateContract(mutate),
        () => runVerifier('checkout-lf'),
      );
      expectFailClosed(result);
    });
  }
});

test('contract rejects unknown schema keys', async (t) => {
  const cases = [
    ['top-level', (contract) => { contract.unknown = true; }],
    ['representation', (contract) => { contract.representation.unknown = true; }],
    ['checkout-equivalence', (contract) => {
      contract.representation.checkout_equivalence.unknown = true;
    }],
    ['entry', (contract) => { contract.entries[0].unknown = true; }],
  ];
  for (const [name, mutate] of cases) {
    await t.test(name, () => {
      const result = withMutation(
        contractPath,
        mutateContract(mutate),
        () => runVerifier('git-object'),
      );
      expectFailClosed(result);
    });
  }
});
