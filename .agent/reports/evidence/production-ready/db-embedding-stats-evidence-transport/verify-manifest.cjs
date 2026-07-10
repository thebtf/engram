#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const allowedModes = new Set(['git-object', 'checkout-lf', 'legacy-raw-audit', 'artifact-files']);
const modeArgument = process.argv.find((argument) => argument.startsWith('--mode='));
const mode = modeArgument ? modeArgument.slice('--mode='.length) : 'git-object';

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
      metadata[metadataMatch[1]] = metadataMatch[2];
      continue;
    }
    if (line.startsWith('#')) continue;

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
  if (new Set(manifest.entries.map((entry) => entry.path)).size !== manifest.entries.length) {
    structuralErrors.push('artifact manifest paths must be unique');
  }

  const artifactManifestRelativePath = path.relative(repoRoot, artifactManifestPath).split(path.sep).join('/');
  const allowedExactPath = '.agent/reports/evidence/production-ready/db-embedding-stats/SHA256SUMS.txt';
  const allowedPrefix = '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/';
  const entryResults = manifest.entries.map((entry) => {
    const entryPath = path.resolve(repoRoot, ...entry.path.split('/'));
    const relativeEntryPath = path.relative(repoRoot, entryPath);
    const insideRepository =
      relativeEntryPath.length > 0 &&
      !relativeEntryPath.startsWith(`..${path.sep}`) &&
      relativeEntryPath !== '..' &&
      !path.isAbsolute(relativeEntryPath);
    const insideOwnedNamespace = entry.path === allowedExactPath || entry.path.startsWith(allowedPrefix);
    if (!insideRepository || !insideOwnedNamespace || entry.path === artifactManifestRelativePath) {
      return {
        path: entry.path,
        match: false,
        checkout_eol: 'not-read-outside-boundary',
        bare_carriage_returns: null,
      };
    }
    const canonical = canonicalizeCheckout(fs.readFileSync(entryPath));
    const actualHash = sha256(canonical.bytes);
    const matches =
      canonical.bare_carriage_returns === 0 &&
      actualHash === entry.sha256;

    return {
      path: entry.path,
      match: matches,
      checkout_eol: eolStyle(canonical),
      bare_carriage_returns: canonical.bare_carriage_returns,
    };
  });

  const matched = entryResults.filter((entry) => entry.match).length;
  const eolCounts = entryResults.reduce((counts, entry) => {
    counts[entry.checkout_eol] = (counts[entry.checkout_eol] || 0) + 1;
    return counts;
  }, {});
  const status = structuralErrors.length === 0 && matched === entryResults.length ? 'PASS' : 'FAIL';
  const result = {
    schema_version: 1,
    slice: 'DB-EMBEDDING-EVIDENCE-TRANSPORT',
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
  const legacyManifestPath = path.join(repoRoot, ...contract.legacy_manifest.split('/'));
  const manifest = parseAnnotatedManifest(legacyManifestPath);

  const structuralErrors = [];
  if (contract.schema_version !== 1) structuralErrors.push('schema_version must be 1');
  if (contract.algorithm !== 'sha256') structuralErrors.push('algorithm must be sha256');
  if (contract.representation?.kind !== 'git-blob-content') {
    structuralErrors.push('representation.kind must be git-blob-content');
  }
  if (!/^[0-9a-f]{40}$/.test(contract.representation?.source_commit || '')) {
    structuralErrors.push('representation.source_commit must be a full Git commit SHA');
  }
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
  if (!compareEntryShape(contract.entries, manifest.entries)) {
    structuralErrors.push('legacy manifest entries disagree with contract entries or order');
  }
  if (new Set(contract.entries.map((entry) => entry.path)).size !== contract.entries.length) {
    structuralErrors.push('contract paths must be unique');
  }

  const sourceCommit = contract.representation.source_commit;
  const sourceCommitIsAncestor = isAncestor(repoRoot, sourceCommit, 'HEAD');
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
  const entryResults = contract.entries.map((entry) => {
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
