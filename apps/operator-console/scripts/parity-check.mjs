#!/usr/bin/env node
/**
 * parity-check — the drift detector. No dependencies; run `node scripts/parity-check.mjs`.
 *
 * What it answers (the user's second question, mechanically):
 *  1. Is PARITY.json's design_version still equal to DESIGN.md's? If not, the contract
 *     moved and every section must be re-audited (or explicitly re-stamped).
 *  2. Which sections are marked `drifted` and need a catch-up pass?
 *  3. Fidelity distribution — the honest backlog at a glance.
 *  4. Sanity: every section.page actually exists on disk; no port page is missing a row.
 *
 * Exit code 1 on any drift/mismatch so it can gate CI later. This is the lightweight
 * accounting that replaces (correctly) a fragile mockup→Vue codegen pipeline.
 */
import { readFileSync, existsSync, readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const od = join(root, '..')

function fail(msg) { console.error('✗ ' + msg); process.exitCode = 1 }
function ok(msg) { console.log('✓ ' + msg) }

// 1. version stamps
const manifest = JSON.parse(readFileSync(join(root, 'PARITY.json'), 'utf8'))
const designMd = readFileSync(join(od, 'DESIGN.md'), 'utf8')
const dvMatch = designMd.match(/design_version:\s*["']?([0-9.]+)["']?/)
const designVer = dvMatch ? dvMatch[1] : null

console.log(`\nDESIGN.md design_version : ${designVer ?? '(none)'}`)
console.log(`PARITY.json design_version: ${manifest.design_version}\n`)

if (!designVer) fail('DESIGN.md has no design_version stamp.')
else if (designVer !== manifest.design_version)
  fail(`Version mismatch — contract moved (${manifest.design_version} → ${designVer}). Re-audit sections, then bump PARITY.json.`)
else ok(`Contract version in sync (${designVer}).`)

// 2. drifted sections + per-section synced_to lag
const drifted = manifest.sections.filter(s => s.sync === 'drifted')
const lagging = manifest.sections.filter(s => s.synced_to !== manifest.design_version)
if (drifted.length) fail(`${drifted.length} section(s) marked drifted: ${drifted.map(s => s.id).join(', ')}`)
else ok('No section flagged drifted.')
if (lagging.length) fail(`${lagging.length} section(s) synced_to an older stamp: ${lagging.map(s => `${s.id}@${s.synced_to}`).join(', ')}`)

// 3. fidelity distribution
const dist = manifest.sections.reduce((a, s) => (a[s.fidelity] = (a[s.fidelity] || 0) + 1, a), {})
console.log('\nFidelity:', Object.entries(dist).map(([k, v]) => `${k} ${v}`).join(' · '))
const openGaps = manifest.sections.flatMap(s => (s.gaps || []).map(g => `${s.id}: ${g}`))
console.log(`Open gaps: ${openGaps.length}`)

// 4. page existence + orphan pages
for (const s of manifest.sections)
  if (!existsSync(join(root, s.page))) fail(`section "${s.id}" → missing page ${s.page}`)
const pageFiles = readdirSync(join(root, 'pages')).filter(f => f.endsWith('.vue')).map(f => 'pages/' + f)
const covered = new Set(manifest.sections.map(s => s.page))
const orphans = pageFiles.filter(p => !covered.has(p))
if (orphans.length) fail(`port page(s) with no parity row: ${orphans.join(', ')}`)
else ok('Every port page has a parity row.')

console.log(process.exitCode ? '\n✗ parity-check found issues (see above).' : '\n✓ parity-check passed.\n')
