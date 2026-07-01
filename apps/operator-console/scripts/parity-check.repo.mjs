#!/usr/bin/env node
/**
 * Repo-aware parity check for the promoted operator-console app.
 *
 * Unlike the design-owned `.od/nuxt-port/scripts/parity-check.mjs`, this wrapper validates
 * the promoted app against the curated public contract snapshot in
 * `design/operator-console/contracts/`.
 */
import { readFileSync, existsSync, readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const repoRoot = join(root, '..', '..')
const contractRoot = join(repoRoot, 'design', 'operator-console', 'contracts')

function fail(msg) {
  console.error('✗ ' + msg)
  process.exitCode = 1
}

function ok(msg) {
  console.log('✓ ' + msg)
}

const manifest = JSON.parse(readFileSync(join(root, 'PARITY.json'), 'utf8'))
const designMdPath = join(contractRoot, 'DESIGN.md')

if (!existsSync(designMdPath)) {
  fail(`missing curated contract DESIGN.md at ${designMdPath}`)
} else {
  const designMd = readFileSync(designMdPath, 'utf8')
  const dvMatch = designMd.match(/design_version:\s*["']?([0-9.]+)["']?/)
  const designVer = dvMatch ? dvMatch[1] : null

  console.log(`\ncontract DESIGN.md design_version : ${designVer ?? '(none)'}`)
  console.log(`PARITY.json design_version        : ${manifest.design_version}\n`)

  if (!designVer) fail('curated contract DESIGN.md has no design_version stamp.')
  else if (designVer !== manifest.design_version) {
    fail(`Version mismatch — curated contract moved (${manifest.design_version} → ${designVer}). Re-audit sections, then bump PARITY.json.`)
  } else {
    ok(`Contract version in sync (${designVer}).`)
  }
}

const drifted = manifest.sections.filter((section) => section.sync === 'drifted')
const lagging = manifest.sections.filter((section) => section.synced_to !== manifest.design_version)
if (drifted.length) fail(`${drifted.length} section(s) marked drifted: ${drifted.map((section) => section.id).join(', ')}`)
else ok('No section flagged drifted.')
if (lagging.length) fail(`${lagging.length} section(s) synced_to an older stamp: ${lagging.map((section) => `${section.id}@${section.synced_to}`).join(', ')}`)

const dist = manifest.sections.reduce((acc, section) => {
  acc[section.fidelity] = (acc[section.fidelity] || 0) + 1
  return acc
}, {})
console.log('\nFidelity:', Object.entries(dist).map(([key, value]) => `${key} ${value}`).join(' · '))
const openGaps = manifest.sections.flatMap((section) => (section.gaps || []).map((gap) => `${section.id}: ${gap}`))
console.log(`Open gaps: ${openGaps.length}`)

for (const section of manifest.sections) {
  if (!existsSync(join(root, section.page))) {
    fail(`section "${section.id}" → missing page ${section.page}`)
  }
}

const pageFiles = readdirSync(join(root, 'pages')).filter((file) => file.endsWith('.vue')).map((file) => 'pages/' + file)
const covered = new Set(manifest.sections.map((section) => section.page))
const orphans = pageFiles.filter((page) => !covered.has(page))
if (orphans.length) fail(`port page(s) with no parity row: ${orphans.join(', ')}`)
else ok('Every port page has a parity row.')

console.log(process.exitCode ? '\n✗ parity-check found issues (see above).' : '\n✓ parity-check passed.\n')
