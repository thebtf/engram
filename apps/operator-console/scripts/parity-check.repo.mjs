#!/usr/bin/env node
/** Curated-contract parity gate. It deliberately never reads .od/. */
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const appRoot = join(dirname(fileURLToPath(import.meta.url)), '..')
const repoRoot = resolve(appRoot, '..', '..')
const designRoot = join(repoRoot, 'design', 'operator-console')
const strict = process.argv.includes('--strict')
let failed = false
const fail = (message) => { failed = true; console.error(`✗ ${message}`) }
const ok = (message) => console.log(`✓ ${message}`)
const hash = (path) => createHash('sha256').update(readFileSync(path)).digest('hex')
const readJson = (path) => JSON.parse(readFileSync(path, 'utf8'))

function checkedPath(root, child, label) {
  const path = resolve(root, child)
  if (path !== root && !path.startsWith(`${root}\\`) && !path.startsWith(`${root}/`)) throw new Error(`${label} escapes root: ${child}`)
  return path
}

function load(path, label) {
  if (!existsSync(path)) { fail(`missing ${label}: ${relative(repoRoot, path)}`); return null }
  try { return readJson(path) } catch (error) { fail(`invalid ${label}: ${error.message}`); return null }
}

const promotion = load(join(designRoot, 'PROMOTION-MANIFEST.json'), 'promotion manifest')
const parity = load(join(appRoot, 'PARITY.json'), 'PARITY.json')
if (!promotion || !parity) process.exitCode = 1

if (promotion && parity) {
  if (promotion.schema_version !== 1) fail(`unsupported promotion manifest schema: ${promotion.schema_version}`)
  if (!promotion.design_version) fail('promotion manifest has no design_version')
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(promotion.promoted_at_utc || '')) fail('promotion manifest has no valid promoted_at_utc')
  if (!/^[a-f0-9]{64}$/.test(promotion.content_sha256 || '')) fail('promotion manifest has no valid content_sha256')
  if (parity.design_version !== promotion.design_version) fail(`PARITY.json design_version ${parity.design_version} does not match promoted contract ${promotion.design_version}`)
  else ok(`parity uses promoted contract version ${promotion.design_version}`)

  const files = Array.isArray(promotion.files) ? promotion.files : []
  if (!files.length) fail('promotion manifest has no files')
  const seenFiles = new Set()
  for (const file of files) {
    if (!file?.path || !/^[a-z0-9][a-z0-9._/-]*$/i.test(file.path)) { fail(`invalid promoted path: ${file?.path}`); continue }
    if (seenFiles.has(file.path)) fail(`duplicate promoted path: ${file.path}`)
    seenFiles.add(file.path)
    if (file.source_classification !== 'private-authoring-snapshot') fail(`${file.path} has invalid source classification`)
    if (!/^[a-f0-9]{64}$/.test(file.sha256 || '')) fail(`${file.path} has invalid SHA-256`)
    let path
    try { path = checkedPath(designRoot, file.path, 'promoted file') } catch (error) { fail(error.message); continue }
    if (!existsSync(path)) fail(`promoted file missing: ${file.path}`)
    else if (hash(path) !== file.sha256) fail(`promoted file hash mismatch: ${file.path}`)
  }
  if (Array.isArray(promotion.private_material_excluded) && promotion.private_material_excluded.length) ok(`verified ${files.length} promoted hashes; private exclusions declared`)
  else fail('promotion manifest does not declare private-material exclusions')
  const contentHash = createHash('sha256').update(`${promotion.design_version}\n${files.map((file) => `${file.path}\0${file.sha256}`).join('\n')}`, 'utf8').digest('hex')
  if (promotion.content_sha256 !== contentHash) fail('promotion manifest content_sha256 does not match promoted files')
  if (parity.design_snapshot_sha256 !== promotion.content_sha256) fail('PARITY.json design_snapshot_sha256 does not match promoted contract')

  const expected = Array.isArray(promotion.route_frames) ? promotion.route_frames : []
  const rows = Array.isArray(parity.sections) ? parity.sections : []
  const expectedIds = new Set(expected.map((item) => item.id))
  const rowIds = new Set()
  for (const row of rows) {
    if (!row?.id) { fail('parity row missing id'); continue }
    if (rowIds.has(row.id)) fail(`duplicate parity row: ${row.id}`)
    rowIds.add(row.id)
    if (!expectedIds.has(row.id)) fail(`unknown parity row: ${row.id}`)
    const mapping = expected.find((item) => item.id === row.id)
    if (!mapping || row.kind !== mapping.kind || row.mockup !== mapping.mockup) fail(`mapping mismatch for ${row.id}`)
    if ((mapping.route || null) !== (row.route || null)) fail(`route mismatch for ${row.id}`)
    if (!row.runtime || !existsSync(checkedPath(appRoot, row.runtime, `runtime for ${row.id}`))) fail(`missing runtime evidence target for ${row.id}: ${row.runtime}`)
    if (!['synced', 'drifted', 'ahead'].includes(row.sync)) fail(`invalid sync status for ${row.id}: ${row.sync}`)
    if (row.sync === 'synced') {
      if (row.synced_to !== promotion.design_version) fail(`synced row ${row.id} has stale synced_to ${row.synced_to}`)
      if (row.synced_snapshot !== promotion.content_sha256) fail(`synced row ${row.id} has stale synced_snapshot`)
      if (!Array.isArray(row.visual_evidence) || row.visual_evidence.length === 0) fail(`synced row ${row.id} lacks visual evidence`)
      for (const evidence of row.visual_evidence || []) {
        if (typeof evidence !== 'string' || !evidence.startsWith('evidence/')) fail(`synced row ${row.id} has invalid evidence reference: ${evidence}`)
        else if (!existsSync(checkedPath(designRoot, evidence, `visual evidence for ${row.id}`))) fail(`synced row ${row.id} cites missing evidence: ${evidence}`)
      }
    }
    if (row.sync === 'drifted' && row.synced_to === promotion.design_version) fail(`drifted row ${row.id} misleadingly claims current synced_to`)
    if (row.sync === 'drifted' && row.synced_snapshot === promotion.content_sha256) fail(`drifted row ${row.id} misleadingly claims current synced_snapshot`)
  }
  for (const mapping of expected) if (!rowIds.has(mapping.id)) fail(`missing parity row: ${mapping.id}`)
  const routes = expected.filter((item) => item.kind === 'route')
  const frames = expected.filter((item) => item.kind === 'frame')
  if (routes.length !== 15 || frames.length !== 2) fail(`route/frame contract must have 15 routes + 2 frames (has ${routes.length} + ${frames.length})`)

  const pageRows = rows.filter((row) => row.kind === 'route').map((row) => row.runtime)
  const pages = readdirSync(join(appRoot, 'pages')).filter((name) => name.endsWith('.vue')).map((name) => `pages/${name}`)
  for (const page of pages) if (!pageRows.includes(page)) fail(`runtime page has no route parity row: ${page}`)
  const drifted = rows.filter((row) => row.sync === 'drifted').map((row) => row.id)
  console.log(`Acknowledged drift: ${drifted.length ? drifted.join(', ') : 'none'}`)
  if (strict && drifted.length) fail(`strict parity rejects ${drifted.length} acknowledged drift row(s)`)
  else if (drifted.length) console.log('! acknowledged drift is unfinished and not a visual-sync claim')
}

if (failed) { console.error('\n✗ parity gate failed.'); process.exitCode = 1 } else console.log('\n✓ parity gate passed (acknowledged drift may still require convergence).')
