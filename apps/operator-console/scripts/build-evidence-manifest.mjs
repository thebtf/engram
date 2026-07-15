#!/usr/bin/env node
/**
 * Build the immutable P0 acceptance evidence manifest.
 *
 * Usage:
 *   node scripts/build-evidence-manifest.mjs [evidenceDir]
 *
 * Reads every PNG in the evidence directory, computes SHA-256 per file, and
 * writes manifest.json with per-file hashes, source fixture, viewport,
 * expected runtime state, secret-scan note, and a content-addressed
 * `sourceProof`: a deterministic aggregate hash over the evidence-relevant
 * source files (git object hash per file, sorted by path, hashed together).
 * Regenerating this script on any commit that carries an identical source
 * tree yields the identical `sourceProof.aggregate` — the manifest proves
 * which source content the evidence depends on without ever pointing at its
 * own not-yet-existing commit SHA. Canonical evidence is generated ONCE
 * (playwright run with OPERATOR_EVIDENCE_DIR pointing at the tracked evidence
 * dir), then this script freezes the packet. Ordinary regression replays
 * never write here.
 */
import { createHash } from 'node:crypto'
import { execSync } from 'node:child_process'
import { readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const evidenceDir = process.argv[2] || join(root, 'evidence', 'p0-a3')

// Deterministic, documented source-file set: the composables and pages the
// 11 frozen screenshots depend on, plus the spec fixture that captures them.
const SOURCE_PROOF_FILES = [
  'composables/useOperatorApi.ts',
  'composables/useOperatorAccess.ts',
  'composables/useOperatorGraph.ts',
  'pages/access.vue',
  'pages/graph.vue',
  'pages/settings.vue',
  'tests/browser/truth-accessibility.spec.ts',
].sort()

function buildSourceProof() {
  const files = SOURCE_PROOF_FILES.map((path) => ({
    path,
    objectHash: execSync(`git hash-object "${path}"`, { cwd: root, encoding: 'utf8' }).trim(),
  }))
  const aggregate = createHash('sha256')
    .update(files.map((f) => `${f.path}:${f.objectHash}`).join('\n'))
    .digest('hex')
  return {
    algo: 'sha256(sorted "path:gitObjectHash" pairs joined by \\n)',
    files,
    aggregate,
  }
}

const CAPTURE_META = {
  'access-populated-1280.png': { viewport: '1280x720', state: 'live' },
  'access-error-390.png': { viewport: '390x844', state: 'error' },
  'access-recovery-980.png': { viewport: '980x844', state: 'live' },
  'access-unauthorized-1280.png': { viewport: '1280x720', state: 'unauthorized' },
  'access-forbidden-1280.png': { viewport: '1280x720', state: 'forbidden' },
  'graph-populated-1440.png': { viewport: '1440x900', state: 'live' },
  'graph-error-1440.png': { viewport: '1440x900', state: 'error' },
  'graph-gated-980.png': { viewport: '980x844', state: 'gated' },
  'graph-stale-1440.png': { viewport: '1440x900', state: 'stale-snapshot' },
  'settings-general-390.png': { viewport: '390x844', state: 'settings-general' },
  'settings-access-390.png': { viewport: '390x844', state: 'settings-access' },
}

const files = readdirSync(evidenceDir)
  .filter((name) => name.endsWith('.png'))
  .sort()
  .map((name) => {
    const body = readFileSync(join(evidenceDir, name))
    const meta = CAPTURE_META[name] || { viewport: 'unknown', state: 'unknown' }
    return {
      path: name,
      viewport: meta.viewport,
      state: meta.state,
      bytes: body.length,
      sha256: createHash('sha256').update(body).digest('hex'),
    }
  })

const missing = Object.keys(CAPTURE_META).filter((name) => !files.some((file) => file.path === name))
if (missing.length) {
  console.error(`missing required evidence captures: ${missing.join(', ')}`)
  process.exit(1)
}

const sourceProof = buildSourceProof()

const manifest = {
  sourceProof,
  sourceFixture: 'tests/browser/truth-accessibility.spec.ts',
  generation: 'OPERATOR_EVIDENCE_DIR=<this dir> npx playwright test tests/browser/truth-accessibility.spec.ts; then node scripts/build-evidence-manifest.mjs',
  secretScan: 'mock-only fixtures; no invitation code, credential, or live host data is captured',
  captures: files,
}

writeFileSync(join(evidenceDir, 'manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`)
console.log(`manifest.json written with ${files.length} captures; sourceProof.aggregate=${sourceProof.aggregate}`)
