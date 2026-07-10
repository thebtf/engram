#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const allowedModes = new Set(['git-object', 'checkout-lf', 'legacy-raw-audit', 'artifact-files']);
const modeArgument = process.argv.find((argument) => argument.startsWith('--mode='));
const mode = modeArgument ? modeArgument.slice('--mode='.length) : 'git-object';

const SLICE = 'DB-EMBEDDING-EVIDENCE-TRANSPORT';
const LEGACY_MANIFEST_PATH =
  '.agent/reports/evidence/production-ready/db-embedding-stats/SHA256SUMS.txt';
const VERIFIER_PATH =
  '.agent/reports/evidence/production-ready/' +
  'db-embedding-stats-evidence-transport/verify-manifest.cjs';
const CONTRACT_PATH =
  '.agent/reports/evidence/production-ready/' +
  'db-embedding-stats-evidence-transport/content-manifest.v1.json';
const REQUIRED_ARTIFACT_PATHS = Object.freeze([
  LEGACY_MANIFEST_PATH,
  CONTRACT_PATH,
  VERIFIER_PATH,
  '.agent/reports/evidence/production-ready/' +
    'db-embedding-stats-evidence-transport/verification-observations.v1.json',
  '.agent/reports/evidence/production-ready/' +
    'db-embedding-stats-evidence-transport/maker-report.md',
]);
const CONTRACT_TOP_LEVEL_KEYS = Object.freeze([
  'schema_version',
  'slice',
  'algorithm',
  'representation',
  'legacy_manifest',
  'verifier',
  'entries',
]);
const REPRESENTATION_KEYS = Object.freeze([
  'kind',
  'source_commit',
  'checkout_equivalence',
]);
const CHECKOUT_EQUIVALENCE_KEYS = Object.freeze([
  'transform',
  'bare_cr',
  'required_result',
]);
const CONTRACT_ENTRY_KEYS = Object.freeze([
  'path',
  'git_blob_oid',
  'byte_length',
  'sha256',
]);
const LEGACY_METADATA_KEYS = Object.freeze([
  'manifest-version',
  'algorithm',
  'representation',
  'source-commit',
  'contract',
  'verifier',
  'checkout-equivalence',
]);
const ARTIFACT_METADATA_KEYS = Object.freeze([
  'manifest-version',
  'algorithm',
  'representation',
  'checkout-equivalence',
  'self-entry',
]);

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

  if (result.error) {
    throw result.error;
  }
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

function canonicalizeCheckout(bytes) {
  const output = [];
  let crlfPairs = 0;
  let bareCarriageReturns = 0;
  let lineFeeds = 0;

  for (let index = 0; index < bytes.length; index += 1) {
    const value = bytes[index];
    if (value === 13) {
      if (bytes[index + 1] === 10) {
        output.push(10);
        crlfPairs += 1;
        lineFeeds += 1;
        index += 1;
      } else {
        output.push(value);
        bareCarriageReturns += 1;
      }
    } else {
      output.push(value);
      if (value === 10) {
        lineFeeds += 1;
      }
    }
  }

  return {
    bytes: Buffer.from(output),
    crlf_pairs: crlfPairs,
    bare_carriage_returns: bareCarriageReturns,
    line_feeds: lineFeeds,
  };
}

function eolStyle(canonical) {
  if (canonical.bare_carriage_returns > 0) return 'bare-cr-present';
  if (canonical.crlf_pairs > 0) return 'crlf';
  if (canonical.line_feeds > 0) return 'lf';
  return 'no-line-ending';
}

function parseAnnotatedManifest(manifestPath) {
  const metadata = {};
  const entries = [];
  const lines = fs.readFileSync(manifestPath, 'utf8').split(/\r?\n/);

  for (const line of lines) {
    if (!line) continue;
    const metadataMatch = line.match(/^# ([a-z0-9-]+)=(.+)$/);
    if (metadataMatch) {
      if (Object.hasOwn(metadata, metadataMatch[1])) {
        throw new Error(`duplicate manifest metadata key: ${metadataMatch[1]}`);
      }
      metadata[metadataMatch[1]] = metadataMatch[2];
      continue;
    }
    if (line.startsWith('#')) {
      throw new Error(`invalid manifest metadata line: ${line}`);
    }

    const entryMatch = line.match(/^([0-9a-f]{64})  (.+)$/);
    if (!entryMatch) {
      throw new Error(`invalid manifest line: ${line}`);
    }
    entries.push({ sha256: entryMatch[1], path: entryMatch[2] });
  }

  return { metadata, entries };
}

function compareEntryShape(contractEntries, manifestEntries) {
  if (contractEntries.length !== manifestEntries.length) return false;
  return contractEntries.every((entry, index) =>
    entry.path === manifestEntries[index].path &&
    entry.sha256 === manifestEntries[index].sha256,
  );
}

function isPlainObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function validateExactKeys(value, requiredKeys, label, structuralErrors) {
  if (!isPlainObject(value)) {
    structuralErrors.push(`${label} must be an object`);
    return false;
  }
  const required = new Set(requiredKeys);
  for (const key of requiredKeys) {
    if (!Object.hasOwn(value, key)) {
      structuralErrors.push(`${label} is missing required key: ${key}`);
    }
  }
  for (const key of Object.keys(value)) {
    if (!required.has(key)) {
      structuralErrors.push(`${label} contains unknown key: ${key}`);
    }
  }
  return true;
}

function analyzeRepositoryRelativePath(repoRoot, rawPath) {
  const errors = [];
  if (typeof rawPath !== 'string' || rawPath.length === 0) {
    errors.push('path must be a non-empty string');
    return { errors, absolute_path: null, normalized_path: null };
  }
  if (rawPath.includes('\\')) errors.push('path must use POSIX separators');
  if (rawPath.includes('\0')) errors.push('path must not contain NUL');
  if (path.posix.isAbsolute(rawPath) || path.win32.isAbsolute(rawPath)) {
    errors.push('path must be repository-relative');
  }

  const segments = rawPath.split('/');
  if (segments.some((segment) => segment === '' || segment === '.' || segment === '..')) {
    errors.push('path must not contain empty, dot, or dot-dot segments');
  }
  if (segments.some((segment) => segment.includes(':'))) {
    errors.push('path must not contain a drive or URI separator');
  }

  const normalizedPath = path.posix.normalize(rawPath);
  if (normalizedPath !== rawPath) errors.push('path must already be POSIX-normalized');

  const absolutePath = path.resolve(repoRoot, ...segments);
  const relativePath = path.relative(repoRoot, absolutePath);
  const relativePosix = relativePath.split(path.sep).join('/');
  const insideRepository =
    relativePath.length > 0 &&
    relativePath !== '..' &&
    !relativePath.startsWith(`..${path.sep}`) &&
    !path.isAbsolute(relativePath);
  if (!insideRepository || relativePosix !== rawPath) {
    errors.push('path must resolve to the same repository-relative path');
  }

  return {
    errors,
    absolute_path: errors.length === 0 ? absolutePath : null,
    normalized_path: errors.length === 0 ? normalizedPath : null,
  };
}

function validateContractSchema(contract, repoRoot) {
  const structuralErrors = [];
  const topLevelIsObject = validateExactKeys(
    contract,
    CONTRACT_TOP_LEVEL_KEYS,
    'contract',
    structuralErrors,
  );
  if (!topLevelIsObject) return structuralErrors;

  if (contract.schema_version !== 1) structuralErrors.push('schema_version must be 1');
  if (contract.slice !== SLICE) structuralErrors.push(`slice must be ${SLICE}`);
  if (contract.algorithm !== 'sha256') structuralErrors.push('algorithm must be sha256');
  if (contract.legacy_manifest !== LEGACY_MANIFEST_PATH) {
    structuralErrors.push(`legacy_manifest must be ${LEGACY_MANIFEST_PATH}`);
  }
  if (contract.verifier !== VERIFIER_PATH) {
    structuralErrors.push(`verifier must be ${VERIFIER_PATH}`);
  }

  const representationIsObject = validateExactKeys(
    contract.representation,
    REPRESENTATION_KEYS,
    'contract.representation',
    structuralErrors,
  );
  if (representationIsObject) {
    if (contract.representation.kind !== 'git-blob-content') {
      structuralErrors.push('representation.kind must be git-blob-content');
    }
    if (!/^[0-9a-f]{40}$/.test(contract.representation.source_commit || '')) {
      structuralErrors.push('representation.source_commit must be a full Git commit SHA');
    }
    const checkoutEquivalenceIsObject = validateExactKeys(
      contract.representation.checkout_equivalence,
      CHECKOUT_EQUIVALENCE_KEYS,
      'contract.representation.checkout_equivalence',
      structuralErrors,
    );
    if (checkoutEquivalenceIsObject) {
      const equivalence = contract.representation.checkout_equivalence;
      if (equivalence.transform !== 'replace each CRLF byte pair with LF') {
        structuralErrors.push(
          'checkout_equivalence.transform must be replace each CRLF byte pair with LF',
        );
      }
      if (equivalence.bare_cr !== 'reject') {
        structuralErrors.push('checkout_equivalence.bare_cr must be reject');
      }
      if (equivalence.required_result !== 'byte-identical to the source commit Git blob') {
        structuralErrors.push(
          'checkout_equivalence.required_result must bind to the source commit Git blob',
        );
      }
    }
  }

  if (!Array.isArray(contract.entries) || contract.entries.length === 0) {
    structuralErrors.push('entries must be a non-empty array');
    return structuralErrors;
  }

  const canonicalPaths = [];
  contract.entries.forEach((entry, index) => {
    const label = `contract.entries[${index}]`;
    const entryIsObject = validateExactKeys(
      entry,
      CONTRACT_ENTRY_KEYS,
      label,
      structuralErrors,
    );
    if (!entryIsObject) return;

    const pathAnalysis = analyzeRepositoryRelativePath(repoRoot, entry.path);
    for (const error of pathAnalysis.errors) structuralErrors.push(`${label}.path ${error}`);
    if (pathAnalysis.normalized_path) canonicalPaths.push(pathAnalysis.normalized_path);
    if (!/^[0-9a-f]{40}$/.test(entry.git_blob_oid || '')) {
      structuralErrors.push(`${label}.git_blob_oid must be a full Git blob OID`);
    }
    if (!Number.isSafeInteger(entry.byte_length) || entry.byte_length < 0) {
      structuralErrors.push(`${label}.byte_length must be a non-negative safe integer`);
    }
    if (!/^[0-9a-f]{64}$/.test(entry.sha256 || '')) {
      structuralErrors.push(`${label}.sha256 must be a lowercase SHA-256`);
    }
  });

  if (new Set(canonicalPaths).size !== contract.entries.length) {
    structuralErrors.push('contract paths must be unique canonical paths');
  }
  return structuralErrors;
}

function validateManifestMetadata(metadata, requiredKeys, label, structuralErrors) {
  validateExactKeys(metadata, requiredKeys, label, structuralErrors);
}

function getCoreAutocrlf(repoRoot) {
  const result = spawnSync('git', ['config', '--get', 'core.autocrlf'], {
    cwd: repoRoot,
    encoding: 'utf8',
    windowsHide: true,
  });
  if (result.status === 1) return null;
  if (result.status !== 0) {
    throw new Error(`git config --get core.autocrlf failed (${result.status})`);
  }
  return result.stdout.trim();
}

function isAncestor(repoRoot, ancestor, descendant) {
  const result = spawnSync('git', ['merge-base', '--is-ancestor', ancestor, descendant], {
    cwd: repoRoot,
    encoding: 'utf8',
    windowsHide: true,
  });
  if (result.status === 0) return true;
  if (result.status === 1) return false;
  throw new Error(`git merge-base --is-ancestor failed (${result.status})`);
}

function verifyArtifactFiles(repoRoot, scriptDirectory, sourceCommit, sourceCommitIsAncestor, inheritedErrors) {
  const artifactManifestPath = path.join(scriptDirectory, 'ARTIFACTS.sha256');
  const manifest = parseAnnotatedManifest(artifactManifestPath);
  const structuralErrors = [...inheritedErrors];

  validateManifestMetadata(
    manifest.metadata,
    ARTIFACT_METADATA_KEYS,
    'artifact manifest metadata',
    structuralErrors,
  );
  if (manifest.metadata['manifest-version'] !== '1') {
    structuralErrors.push('artifact manifest-version must be 1');
  }
  if (manifest.metadata.algorithm !== 'sha256') {
    structuralErrors.push('artifact algorithm must be sha256');
  }
  if (manifest.metadata.representation !== 'canonical-lf-files') {
    structuralErrors.push('artifact representation must be canonical-lf-files');
  }
  if (manifest.metadata['checkout-equivalence'] !== 'crlf-to-lf-with-no-bare-cr') {
    structuralErrors.push('artifact checkout equivalence must reject bare CR');
  }
  if (manifest.metadata['self-entry'] !== 'excluded-to-avoid-recursion') {
    structuralErrors.push('artifact manifest must explicitly declare self exclusion');
  }
  if (manifest.entries.length !== REQUIRED_ARTIFACT_PATHS.length) {
    structuralErrors.push(
      `artifact manifest must contain exactly ${REQUIRED_ARTIFACT_PATHS.length} entries`,
    );
  }

  const requiredPaths = new Set(REQUIRED_ARTIFACT_PATHS);
  const canonicalPaths = [];
  const entryResults = manifest.entries.map((entry) => {
    const pathAnalysis = analyzeRepositoryRelativePath(repoRoot, entry.path);
    for (const error of pathAnalysis.errors) {
      structuralErrors.push(`artifact manifest entry ${entry.path}: ${error}`);
    }
    if (pathAnalysis.normalized_path) canonicalPaths.push(pathAnalysis.normalized_path);

    const isRequiredPath =
      pathAnalysis.normalized_path !== null &&
      requiredPaths.has(pathAnalysis.normalized_path);
    if (!isRequiredPath) {
      return {
        path: entry.path,
        normalized_path: pathAnalysis.normalized_path,
        match: false,
        checkout_eol: 'not-read-outside-boundary',
        bare_carriage_returns: null,
      };
    }

    let checkoutBytes;
    try {
      checkoutBytes = fs.readFileSync(pathAnalysis.absolute_path);
    } catch (error) {
      structuralErrors.push(`artifact file is not readable: ${entry.path}: ${error.code || error.message}`);
      return {
        path: entry.path,
        normalized_path: pathAnalysis.normalized_path,
        match: false,
        checkout_eol: 'not-readable',
        bare_carriage_returns: null,
      };
    }

    const canonical = canonicalizeCheckout(checkoutBytes);
    const actualHash = sha256(canonical.bytes);
    const matches =
      canonical.bare_carriage_returns === 0 &&
      actualHash === entry.sha256;

    return {
      path: entry.path,
      normalized_path: pathAnalysis.normalized_path,
      match: matches,
      checkout_eol: eolStyle(canonical),
      bare_carriage_returns: canonical.bare_carriage_returns,
    };
  });

  const canonicalPathSet = new Set(canonicalPaths);
  if (canonicalPathSet.size !== canonicalPaths.length) {
    structuralErrors.push('artifact manifest paths must be unique canonical paths');
  }
  const missingPaths = REQUIRED_ARTIFACT_PATHS.filter((entryPath) => !canonicalPathSet.has(entryPath));
  const extraPaths = [...canonicalPathSet].filter((entryPath) => !requiredPaths.has(entryPath));
  if (missingPaths.length > 0) {
    structuralErrors.push(`artifact manifest missing required paths: ${missingPaths.join(', ')}`);
  }
  if (extraPaths.length > 0) {
    structuralErrors.push(`artifact manifest contains extra paths: ${extraPaths.join(', ')}`);
  }

  const matched = entryResults.filter((entry) => entry.match).length;
  const eolCounts = entryResults.reduce((counts, entry) => {
    counts[entry.checkout_eol] = (counts[entry.checkout_eol] || 0) + 1;
    return counts;
  }, {});
  const status = structuralErrors.length === 0 && matched === entryResults.length ? 'PASS' : 'FAIL';
  const result = {
    schema_version: 1,
    slice: SLICE,
    mode: 'artifact-files',
    status,
    source_commit: sourceCommit,
    source_commit_is_ancestor: sourceCommitIsAncestor,
    algorithm: 'sha256',
    representation: 'canonical-lf-files',
    total: entryResults.length,
    matched,
    checkout: {
      core_autocrlf: getCoreAutocrlf(repoRoot),
      eol_counts: eolCounts,
    },
    structural_errors: structuralErrors,
    entries: entryResults,
  };

  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
  if (status === 'FAIL') process.exit(1);
}

function main() {
  const repoRoot = path.resolve(runGit(['rev-parse', '--show-toplevel'], { encoding: 'utf8' }).trim());
  const scriptDirectory = __dirname;
  const contractPath = path.join(scriptDirectory, 'content-manifest.v1.json');
  const contract = JSON.parse(fs.readFileSync(contractPath, 'utf8'));
  const structuralErrors = validateContractSchema(contract, repoRoot);
  const legacyManifestPath = path.join(repoRoot, ...LEGACY_MANIFEST_PATH.split('/'));
  const manifest = parseAnnotatedManifest(legacyManifestPath);

  validateManifestMetadata(
    manifest.metadata,
    LEGACY_METADATA_KEYS,
    'legacy manifest metadata',
    structuralErrors,
  );
  if (manifest.metadata['manifest-version'] !== '1') {
    structuralErrors.push('legacy manifest annotation manifest-version=1 missing');
  }
  if (manifest.metadata.algorithm !== contract.algorithm) {
    structuralErrors.push('legacy manifest algorithm annotation disagrees with contract');
  }
  if (manifest.metadata.representation !== contract.representation.kind) {
    structuralErrors.push('legacy manifest representation annotation disagrees with contract');
  }
  if (manifest.metadata['source-commit'] !== contract.representation.source_commit) {
    structuralErrors.push('legacy manifest source-commit annotation disagrees with contract');
  }
  if (manifest.metadata.contract !== path.relative(repoRoot, contractPath).split(path.sep).join('/')) {
    structuralErrors.push('legacy manifest contract path annotation disagrees with verifier location');
  }
  if (manifest.metadata.verifier !== path.relative(repoRoot, __filename).split(path.sep).join('/')) {
    structuralErrors.push('legacy manifest verifier path annotation disagrees with executing verifier');
  }
  if (manifest.metadata['checkout-equivalence'] !== 'crlf-to-lf-with-no-bare-cr') {
    structuralErrors.push('legacy manifest checkout equivalence must reject bare CR');
  }
  const contractEntries = Array.isArray(contract.entries) ? contract.entries : [];
  if (!compareEntryShape(contractEntries, manifest.entries)) {
    structuralErrors.push('legacy manifest entries disagree with contract entries or order');
  }

  const sourceCommit = contract.representation?.source_commit || '';
  const sourceCommitIsAncestor =
    /^[0-9a-f]{40}$/.test(sourceCommit || '') &&
    isAncestor(repoRoot, sourceCommit, 'HEAD');
  if (!sourceCommitIsAncestor) {
    structuralErrors.push('source commit is not an ancestor of the executing checkout HEAD');
  }
  if (mode === 'artifact-files') {
    verifyArtifactFiles(
      repoRoot,
      scriptDirectory,
      sourceCommit,
      sourceCommitIsAncestor,
      structuralErrors,
    );
    return;
  }
  const entryResults = contractEntries.map((entry) => {
    const objectSpec = `${sourceCommit}:${entry.path}`;
    const blob = runGit(['cat-file', 'blob', objectSpec], { cwd: repoRoot });
    const blobOid = runGit(['rev-parse', objectSpec], { cwd: repoRoot, encoding: 'utf8' }).trim();
    const checkout = fs.readFileSync(path.join(repoRoot, ...entry.path.split('/')));
    const canonical = canonicalizeCheckout(checkout);
    const rawHash = sha256(checkout);
    const canonicalHash = sha256(canonical.bytes);
    const objectHash = sha256(blob);
    const objectChecks =
      blobOid === entry.git_blob_oid &&
      blob.length === entry.byte_length &&
      objectHash === entry.sha256;
    const checkoutLfChecks =
      canonical.bare_carriage_returns === 0 &&
      canonical.bytes.equals(blob) &&
      canonical.bytes.length === entry.byte_length &&
      canonicalHash === entry.sha256;

    return {
      path: entry.path,
      git_blob_oid: blobOid,
      expected_sha256: entry.sha256,
      git_object_sha256: objectHash,
      raw_checkout_sha256: rawHash,
      checkout_lf_sha256: canonicalHash,
      git_object_match: objectChecks,
      raw_checkout_match: rawHash === entry.sha256 && checkout.length === entry.byte_length,
      checkout_lf_match: checkoutLfChecks,
      checkout_eol: eolStyle(canonical),
      crlf_pairs: canonical.crlf_pairs,
      bare_carriage_returns: canonical.bare_carriage_returns,
    };
  });

  const total = entryResults.length;
  const gitObjectMatches = entryResults.filter((entry) => entry.git_object_match).length;
  const rawCheckoutMatches = entryResults.filter((entry) => entry.raw_checkout_match).length;
  const checkoutLfMatches = entryResults.filter((entry) => entry.checkout_lf_match).length;
  const eolCounts = entryResults.reduce((counts, entry) => {
    counts[entry.checkout_eol] = (counts[entry.checkout_eol] || 0) + 1;
    return counts;
  }, {});

  let status = 'FAIL';
  let matched = 0;
  if (mode === 'git-object') {
    matched = gitObjectMatches;
    if (structuralErrors.length === 0 && matched === total) status = 'PASS';
  } else if (mode === 'checkout-lf') {
    matched = checkoutLfMatches;
    if (structuralErrors.length === 0 && gitObjectMatches === total && matched === total) status = 'PASS';
  } else {
    matched = checkoutLfMatches;
    if (structuralErrors.length === 0 && gitObjectMatches === total && checkoutLfMatches === total) {
      status = rawCheckoutMatches === total
        ? 'RAW_CHECKOUT_HAPPENS_TO_MATCH'
        : 'AMBIGUOUS_RAW_CHECKOUT_CONFIRMED';
    }
  }

  const result = {
    schema_version: 1,
    slice: contract.slice,
    mode,
    status,
    source_commit: sourceCommit,
    source_commit_is_ancestor: sourceCommitIsAncestor,
    algorithm: contract.algorithm,
    representation: contract.representation.kind,
    total,
    matched,
    git_object_matches: gitObjectMatches,
    raw_checkout_matches: rawCheckoutMatches,
    checkout_lf_matches: checkoutLfMatches,
    checkout: {
      core_autocrlf: getCoreAutocrlf(repoRoot),
      eol_counts: eolCounts,
    },
    structural_errors: structuralErrors,
    entries: entryResults.map((entry) => ({
      path: entry.path,
      git_blob_oid: entry.git_blob_oid,
      git_object_match: entry.git_object_match,
      raw_checkout_match: entry.raw_checkout_match,
      checkout_lf_match: entry.checkout_lf_match,
      checkout_eol: entry.checkout_eol,
      crlf_pairs: entry.crlf_pairs,
      bare_carriage_returns: entry.bare_carriage_returns,
    })),
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
