#!/usr/bin/env node
'use strict';

// Behavioral signal: release-evidence-false-pass-rate.
// Measurement: deterministic local mutation suite; target: zero false PASS results.
// Source: DB-EMBEDDING-EVIDENCE-TRANSPORT independent checker findings ET-001/ET-002.

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { after, test } = require('node:test');

const scriptDirectory = __dirname;
const verifierPath = path.join(scriptDirectory, 'verify-manifest.cjs');
const artifactManifestPath = path.join(scriptDirectory, 'ARTIFACTS.sha256');
const contractPath = path.join(scriptDirectory, 'content-manifest.v1.json');
const testPath = __filename;
const ACCEPTED_SOURCE_COMMIT = '38d6a4fb7ff5f5ae3b6c0066c0a1b806421137df';
const ALTERNATE_ANCESTOR = '580b0cd0ff38bb55a5195a8004e60234a824b7a8';
const REQUIRED_SOURCE_PATHS = Object.freeze([
  'internal/embedding/store.go',
  'internal/embedding/store_stats_test.go',
  '.agent/specs/db-embedding-stats/evidence/DB-EMBEDDING-STATS.red.json',
  '.agent/specs/db-embedding-stats/evidence/DB-EMBEDDING-STATS.tdd.json',
  '.agent/specs/db-embedding-stats/evidence/coverage.out',
  '.agent/reports/2026-07-10-db-embedding-stats-maker.md',
  '.agent/reports/evidence/production-ready/db-embedding-stats/DB-EMBEDDING-STATS.final.json',
]);
const repoRoot = path.resolve(
  spawnSync('git', ['rev-parse', '--show-toplevel'], {
    cwd: scriptDirectory,
    encoding: 'utf8',
    windowsHide: true,
  }).stdout.trim(),
);
const coverageCaptureDirectory = path.join(
  repoRoot,
  '.agent',
  'reports',
  'evidence',
  'production-ready',
  'db-embedding-stats-evidence-transport-r5',
);
const coverageCapturePath = path.join(coverageCaptureDirectory, 'coverage-capture.v1.json');
const coverageCaptureVerifierWrapperPath = path.join(
  coverageCaptureDirectory,
  'run-coverage-capture-verifier.cmd',
);
const legacyManifestPath = path.join(
  repoRoot,
  '.agent',
  'reports',
  'evidence',
  'production-ready',
  'db-embedding-stats',
  'SHA256SUMS.txt',
);
const sourceAbsolutePaths = new Set(
  REQUIRED_SOURCE_PATHS.map((entryPath) => path.resolve(repoRoot, ...entryPath.split('/'))),
);
const accessSpyDirectory = fs.mkdtempSync(
  path.join(os.tmpdir(), 'engram-embedding-evidence-access-spy-'),
);
const accessSpyPath = path.join(accessSpyDirectory, 'access-spy.cjs');
fs.writeFileSync(
  accessSpyPath,
  `'use strict';

const fs = require('node:fs');
const childProcess = require('node:child_process');

const logPath = process.env.EMBEDDING_EVIDENCE_ACCESS_LOG;
const originalReadFileSync = fs.readFileSync;
const originalSpawnSync = childProcess.spawnSync;

function append(record) {
  if (logPath) fs.appendFileSync(logPath, \`${'${JSON.stringify(record)}'}\\n\`, 'utf8');
}

fs.readFileSync = function observedReadFileSync(filePath, ...args) {
  append({
    kind: 'fs.readFileSync',
    path: Buffer.isBuffer(filePath) ? filePath.toString('utf8') : String(filePath),
  });
  return originalReadFileSync.call(this, filePath, ...args);
};

childProcess.spawnSync = function observedSpawnSync(command, args, options) {
  append({
    kind: 'child_process.spawnSync',
    command: String(command),
    args: Array.isArray(args) ? args.map(String) : [],
  });
  return originalSpawnSync.call(this, command, args, options);
};
`,
  'utf8',
);

const originalBytes = new Map([
  [artifactManifestPath, fs.readFileSync(artifactManifestPath)],
  [contractPath, fs.readFileSync(contractPath)],
  [legacyManifestPath, fs.readFileSync(legacyManifestPath)],
]);

function restoreOriginals() {
  for (const [filePath, bytes] of originalBytes) {
    fs.writeFileSync(filePath, bytes);
  }
  fs.rmSync(accessSpyDirectory, { recursive: true, force: true });
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

function withReplacements(replacements, verify) {
  const originals = new Map();
  try {
    for (const [filePath, bytes] of replacements) {
      originals.set(filePath, fs.readFileSync(filePath));
      fs.writeFileSync(filePath, bytes);
    }
    return verify();
  } finally {
    for (const [filePath, bytes] of originals) fs.writeFileSync(filePath, bytes);
  }
}

let accessSpyRun = 0;

function parseAccessLog(logPath) {
  if (!logPath || !fs.existsSync(logPath)) return [];
  return fs.readFileSync(logPath, 'utf8')
    .split(/\r?\n/)
    .filter(Boolean)
    .map((line) => JSON.parse(line));
}

function runVerifier(mode, options = {}) {
  let accessLogPath = null;
  const env = { ...process.env };
  if (options.preloadSpy) {
    accessSpyRun += 1;
    accessLogPath = path.join(accessSpyDirectory, `access-${accessSpyRun}.jsonl`);
    env.EMBEDDING_EVIDENCE_ACCESS_LOG = accessLogPath;
    env.NODE_OPTIONS = [process.env.NODE_OPTIONS, `--require=${accessSpyPath}`]
      .filter(Boolean)
      .join(' ');
  }
  const result = spawnSync(process.execPath, [verifierPath, `--mode=${mode}`], {
    cwd: repoRoot,
    encoding: 'utf8',
    env,
    windowsHide: true,
  });
  let output = null;
  if (result.stdout.trim()) {
    output = JSON.parse(result.stdout);
  }
  const accessLog = parseAccessLog(accessLogPath);
  const actualSourceAccesses = {
    git_cat_file: accessLog.filter((record) =>
      record.kind === 'child_process.spawnSync' &&
      path.basename(record.command).toLowerCase().startsWith('git') &&
      record.args[0] === 'cat-file' &&
      record.args[1] === 'blob',
    ),
    source_files: accessLog.filter((record) =>
      record.kind === 'fs.readFileSync' &&
      sourceAbsolutePaths.has(path.resolve(record.path)),
    ),
  };
  return {
    exit_code: result.status,
    output,
    stderr: result.stderr.trim(),
    actual_source_accesses: actualSourceAccesses,
  };
}

function runCoverageCaptureVerifier(mode) {
  const result = spawnSync(
    'cmd.exe',
    ['/d', '/c', coverageCaptureVerifierWrapperPath, `--mode=${mode}`],
    {
      cwd: repoRoot,
      encoding: 'utf8',
      windowsHide: true,
    },
  );
  return {
    exit_code: result.status,
    output: result.stdout.trim() ? JSON.parse(result.stdout) : null,
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

function expectFailClosedBeforeSourceAccess(result) {
  expectFailClosed(result);
  assert.deepEqual(
    result.output?.source_accesses,
    { git_objects: 0, checkout_files: 0 },
    'schema-invalid source paths must be rejected before Git or filesystem source access',
  );
  assert.deepEqual(result.output?.entries, [], 'schema-invalid source paths must not be verified');
}

function expectStableFailClosedBeforeActualSourceAccess(result, expectedStructuralError) {
  expectFailClosedBeforeSourceAccess(result);
  assert.equal(result.stderr, '', 'schema rejection must not escape as a raw runtime exception');
  assert.ok(
    result.output.structural_errors.includes(expectedStructuralError),
    `missing stable structural error: ${expectedStructuralError}`,
  );
  assert.deepEqual(
    result.actual_source_accesses,
    { git_cat_file: [], source_files: [] },
    'preload spy observed source access before schema rejection',
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

function gitBytes(args) {
  const result = spawnSync('git', args, {
    cwd: repoRoot,
    encoding: null,
    windowsHide: true,
  });
  assert.equal(result.status, 0, result.stderr.toString('utf8'));
  return result.stdout;
}

function gitText(args) {
  return gitBytes(args).toString('utf8').trim();
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

function mutateJson(mutator) {
  return (bytes) => {
    const value = JSON.parse(bytes.toString('utf8'));
    mutator(value);
    return Buffer.from(`${JSON.stringify(value, null, 2)}\n`, 'utf8');
  };
}

function makeMixedLineEndings(bytes) {
  const firstLf = bytes.indexOf(10);
  assert.notEqual(firstLf, -1, 'fixture must contain an LF');
  return Buffer.concat([
    bytes.subarray(0, firstLf),
    Buffer.from('\r\n', 'utf8'),
    bytes.subarray(firstLf + 1),
  ]);
}

test('evidence manifests reject incomplete sets and undeclared or mixed coverage capture', () => {
  const result = withMutation(
    artifactManifestPath,
    mutateArtifactManifest((lines) => {
      for (const index of dataLineIndexes(lines).reverse()) lines.splice(index, 1);
    }),
    () => runVerifier('artifact-files'),
  );
  expectFailClosed(result);

  const baseline = runCoverageCaptureVerifier('materialization');
  assert.equal(baseline.exit_code, 0, baseline.stderr || JSON.stringify(baseline.output));
  assert.equal(baseline.output?.status, 'PASS');

  const undeclared = withMutation(
    coverageCapturePath,
    mutateJson((coverage) => {
      delete coverage.materialization;
    }),
    () => runCoverageCaptureVerifier('materialization'),
  );
  expectFailClosed(undeclared);
  assert.ok(
    undeclared.output.structural_errors.includes(
      'capture is missing required key: materialization',
    ),
  );

  const mixed = withMutation(
    verifierPath,
    makeMixedLineEndings,
    () => runCoverageCaptureVerifier('materialization'),
  );
  expectFailClosed(mixed);
  assert.ok(
    mixed.output.structural_errors.includes(
      'coverage file must be LF-only and byte-identical to the Git index: ' +
        '.agent/reports/evidence/production-ready/' +
        'db-embedding-stats-evidence-transport/verify-manifest.cjs',
    ),
  );
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

test('contract rejects deleting one required source when the legacy manifest agrees', () => {
  const removedPath = REQUIRED_SOURCE_PATHS.at(-1);
  const contractBytes = mutateContract((contract) => {
    contract.entries = contract.entries.filter((entry) => entry.path !== removedPath);
  })(fs.readFileSync(contractPath));
  const legacyBytes = mutateArtifactManifest((lines) => {
    const index = lines.findIndex((line) => line.endsWith(`  ${removedPath}`));
    assert.notEqual(index, -1);
    lines.splice(index, 1);
  })(fs.readFileSync(legacyManifestPath));

  const result = withReplacements(
    [
      [contractPath, contractBytes],
      [legacyManifestPath, legacyBytes],
    ],
    () => runVerifier('git-object'),
  );
  expectFailClosed(result);
});

test('contract rejects a valid go.mod substitution that preserves cardinality', () => {
  const replacedPath = REQUIRED_SOURCE_PATHS.at(-1);
  const substitutePath = 'go.mod';
  const substituteBytes = gitBytes([
    'cat-file',
    'blob',
    `${ACCEPTED_SOURCE_COMMIT}:${substitutePath}`,
  ]);
  const substitute = {
    path: substitutePath,
    git_blob_oid: gitText(['rev-parse', `${ACCEPTED_SOURCE_COMMIT}:${substitutePath}`]),
    byte_length: substituteBytes.length,
    sha256: sha256(substituteBytes),
  };
  const contractBytes = mutateContract((contract) => {
    const index = contract.entries.findIndex((entry) => entry.path === replacedPath);
    assert.notEqual(index, -1);
    contract.entries[index] = substitute;
  })(fs.readFileSync(contractPath));
  const legacyBytes = mutateArtifactManifest((lines) => {
    const index = lines.findIndex((line) => line.endsWith(`  ${replacedPath}`));
    assert.notEqual(index, -1);
    lines[index] = `${substitute.sha256}  ${substitute.path}`;
  })(fs.readFileSync(legacyManifestPath));

  const result = withReplacements(
    [
      [contractPath, contractBytes],
      [legacyManifestPath, legacyBytes],
    ],
    () => runVerifier('git-object'),
  );
  expectFailClosed(result);
});

test('contract rejects rebinding source commit and legacy metadata to an ancestor', () => {
  const contractBytes = mutateContract((contract) => {
    contract.representation.source_commit = ALTERNATE_ANCESTOR;
  })(fs.readFileSync(contractPath));
  const legacyBytes = mutateArtifactManifest((lines) => {
    const index = lines.findIndex((line) => line.startsWith('# source-commit='));
    assert.notEqual(index, -1);
    lines[index] = `# source-commit=${ALTERNATE_ANCESTOR}`;
  })(fs.readFileSync(legacyManifestPath));

  const result = withReplacements(
    [
      [contractPath, contractBytes],
      [legacyManifestPath, legacyBytes],
    ],
    () => runVerifier('git-object'),
  );
  expectFailClosed(result);
});

test('contract rejects an invalid raw source path before source access', () => {
  const invalidPath = 'internal/embedding/../embedding/store.go';
  const originalPath = REQUIRED_SOURCE_PATHS[0];
  const contractBytes = mutateContract((contract) => {
    contract.entries[0].path = invalidPath;
  })(fs.readFileSync(contractPath));
  const legacyBytes = mutateArtifactManifest((lines) => {
    const index = lines.findIndex((line) => line.endsWith(`  ${originalPath}`));
    assert.notEqual(index, -1);
    lines[index] = lines[index].replace(originalPath, invalidPath);
  })(fs.readFileSync(legacyManifestPath));

  const result = withReplacements(
    [
      [contractPath, contractBytes],
      [legacyManifestPath, legacyBytes],
    ],
    () => runVerifier('git-object'),
  );
  expectFailClosedBeforeSourceAccess(result);
});

test('contract rejects null representation with structured FAIL before actual source access', () => {
  const result = withMutation(
    contractPath,
    mutateContract((contract) => {
      contract.representation = null;
    }),
    () => runVerifier('git-object', { preloadSpy: true }),
  );
  expectStableFailClosedBeforeActualSourceAccess(
    result,
    'contract.representation must be an object',
  );
  assert.equal(result.output.representation, null);
});

test('contract rejects a null entry with structured FAIL before actual source access', () => {
  const result = withMutation(
    contractPath,
    mutateContract((contract) => {
      contract.entries[0] = null;
    }),
    () => runVerifier('git-object', { preloadSpy: true }),
  );
  expectStableFailClosedBeforeActualSourceAccess(
    result,
    'contract.entries[0] must be an object',
  );
});
