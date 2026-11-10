import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { cpSync, existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const repo = join(root, '..', '..')
const promotion = join(repo, 'scripts', 'promote-od-operator-console.ps1')
const files = [
  ['PRODUCT.md', 'contracts/PRODUCT.md'], ['DESIGN.md', 'contracts/DESIGN.md'], ['DESIGN-SYNC-PROTOCOL.md', 'contracts/DESIGN-SYNC-PROTOCOL.md'],
  ['DEVELOPER-PLAYBOOK.md', 'contracts/DEVELOPER-PLAYBOOK.md'], ['HANDOFF-data-integration.md', 'contracts/HANDOFF-data-integration.md'],
  ['RUNTIME-SUBSTRATE-MAP.md', 'contracts/RUNTIME-SUBSTRATE-MAP.md'], ['INTEGRATION-AGENT-PROMPT.md', 'contracts/INTEGRATION-AGENT-PROMPT.md'],
  ['ACCESS-ADMIN-spec.md', 'contracts/ACCESS-ADMIN-spec.md'], ['DESIGNER-endpoints-brief.md', 'contracts/DESIGNER-endpoints-brief.md'],
  ['index.html', 'mockups/index.html'], ['access-admin.html', 'mockups/access-admin.html'], ['saas-admin.html', 'mockups/saas-admin.html'], ['components.html', 'mockups/components.html']
]

function runAt(appRoot, ...args) {
  const check = join(appRoot, 'scripts', 'parity-check.repo.mjs')
  try { return { status: 0, output: execFileSync(process.execPath, [check, ...args], { cwd: appRoot, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }) } }
  catch (error) { return { status: error.status ?? 1, output: `${error.stdout ?? ''}${error.stderr ?? ''}` } }
}

function run(...args) {
  return runAt(root, ...args)
}

function withParityFixture(callback) {
  const base = mkdtempSync(join(tmpdir(), 'engram-parity-'))
  const appRoot = join(base, 'apps', 'operator-console')
  const designRoot = join(base, 'design', 'operator-console')
  try {
    mkdirSync(join(appRoot, 'scripts'), { recursive: true })
    cpSync(join(root, 'scripts', 'parity-check.repo.mjs'), join(appRoot, 'scripts', 'parity-check.repo.mjs'))
    cpSync(join(root, 'PARITY.json'), join(appRoot, 'PARITY.json'))
    cpSync(join(repo, 'design', 'operator-console'), designRoot, { recursive: true, dereference: true })
    const parity = JSON.parse(readFileSync(join(appRoot, 'PARITY.json'), 'utf8'))
    for (const row of parity.sections ?? []) {
      if (typeof row.runtime !== 'string') continue
      const source = resolve(root, row.runtime)
      if ((source !== root && !source.startsWith(`${root}${sep}`)) || !existsSync(source)) continue
      const destination = resolve(appRoot, row.runtime)
      mkdirSync(dirname(destination), { recursive: true })
      cpSync(source, destination, { recursive: true })
    }
    return callback({ base, appRoot, designRoot })
  } finally {
    rmSync(base, { recursive: true, force: true })
  }
}

function promote(source, destination, ...args) {
  try { return { status: 0, output: execFileSync('pwsh', ['-NoProfile', '-File', promotion, '-SourceRoot', source, '-RepoRoot', destination, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }) } }
  catch (error) { return { status: error.status ?? 1, output: `${error.stdout ?? ''}${error.stderr ?? ''}` } }
}

function fixture(eol) {
  const base = mkdtempSync(join(tmpdir(), 'engram-promotion-'))
  const source = join(base, 'source')
  const destination = join(base, 'repo')
  for (const [from, to] of files) {
    const text = readFileSync(join(repo, 'design', 'operator-console', to), 'utf8').replace(/\r\n/g, '\n').replace(/\n/g, eol)
    const path = join(source, from)
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, text, 'utf8')
  }
  mkdirSync(join(destination, 'design', 'operator-console'), { recursive: true })
  mkdirSync(join(destination, 'apps', 'operator-console'), { recursive: true })
  return { base, source, destination }
}

test('strict mode rejects acknowledged drift', () => {
  const result = run('--strict')
  assert.notEqual(result.status, 0)
  assert.match(result.output, /strict parity rejects/)
})

test('public manifest verifies without .od and mutations fail closed', () => {
  let base
  withParityFixture((fixture) => {
    const { appRoot, designRoot } = fixture
    base = fixture.base
    assert.equal(existsSync(join(base, '.od')), false)
    const baseline = runAt(appRoot)
    assert.equal(baseline.status, 0, baseline.output)
    assert.match(baseline.output, /Acknowledged drift:/)
    const mockupPath = join(designRoot, 'mockups', 'index.html')
    const parityPath = join(appRoot, 'PARITY.json')
    const mockup = readFileSync(mockupPath)
    const parity = readFileSync(parityPath)
    writeFileSync(mockupPath, `${mockup}\n<!-- hostile mutation -->\n`)
    let result = runAt(appRoot)
    assert.notEqual(result.status, 0)
    assert.match(result.output, /hash mismatch/)
    writeFileSync(mockupPath, mockup)

    const mutated = JSON.parse(parity)
    mutated.sections.push(mutated.sections[0])
    writeFileSync(parityPath, `${JSON.stringify(mutated, null, 2)}\n`)
    result = runAt(appRoot)
    assert.notEqual(result.status, 0)
    assert.match(result.output, /duplicate parity row/)
  })
  assert.equal(existsSync(base), false)
})

test('parity fixture is removed when its callback fails', () => {
  let base
  assert.throws(() => withParityFixture((fixture) => {
    base = fixture.base
    throw new Error('fixture failure')
  }), /fixture failure/)
  assert.ok(base)
  assert.equal(existsSync(base), false)
})

test('promotion is idempotent, EOL-stable, and refuses runtime writes', () => {
  const lf = fixture('\n')
  const crlf = fixture('\r\n')
  try {
    let result = promote(lf.source, lf.destination, '-DesignOnly')
    assert.equal(result.status, 0, result.output)
    const firstManifest = readFileSync(join(lf.destination, 'design', 'operator-console', 'PROMOTION-MANIFEST.json'))
    const firstMockup = readFileSync(join(lf.destination, 'design', 'operator-console', 'mockups', 'index.html'))
    result = promote(lf.source, lf.destination, '-DesignOnly')
    assert.equal(result.status, 0, result.output)
    assert.deepEqual(readFileSync(join(lf.destination, 'design', 'operator-console', 'PROMOTION-MANIFEST.json')), firstManifest)
    assert.deepEqual(readFileSync(join(lf.destination, 'design', 'operator-console', 'mockups', 'index.html')), firstMockup)

    result = promote(crlf.source, crlf.destination, '-DesignOnly')
    assert.equal(result.status, 0, result.output)
    assert.deepEqual(readFileSync(join(crlf.destination, 'design', 'operator-console', 'PROMOTION-MANIFEST.json')), firstManifest)
    assert.deepEqual(readFileSync(join(crlf.destination, 'design', 'operator-console', 'mockups', 'index.html')), firstMockup)
    assert.equal(firstMockup.includes(Buffer.from('\r\n')), false)

    writeFileSync(join(crlf.source, 'index.html'), `${readFileSync(join(crlf.source, 'index.html'), 'utf8')}<!-- real mutation -->\r\n`, 'utf8')
    result = promote(crlf.source, crlf.destination, '-DesignOnly')
    assert.notEqual(result.status, 0)
    assert.match(result.output, /Refusing promotion/)
    assert.deepEqual(readFileSync(join(crlf.destination, 'design', 'operator-console', 'PROMOTION-MANIFEST.json')), firstManifest)

    writeFileSync(join(crlf.source, 'DESIGN.md'), readFileSync(join(crlf.source, 'DESIGN.md'), 'utf8').replace(/design_version:\s*"[0-9.]+"/, 'design_version: "2099.01.01"'), 'utf8')
    result = promote(crlf.source, crlf.destination, '-DesignOnly')
    assert.equal(result.status, 0, result.output)
    assert.notDeepEqual(readFileSync(join(crlf.destination, 'design', 'operator-console', 'PROMOTION-MANIFEST.json')), firstManifest)
    assert.match(readFileSync(join(crlf.destination, 'design', 'operator-console', 'mockups', 'index.html'), 'utf8'), /real mutation/)
    const bumpedManifest = JSON.parse(readFileSync(join(crlf.destination, 'design', 'operator-console', 'PROMOTION-MANIFEST.json'), 'utf8'))
    assert.equal(bumpedManifest.design_version, '2099.01.01')
    assert.match(bumpedManifest.promoted_at_utc, /^2099-01-01T/)

    mkdirSync(join(lf.source, 'nuxt-port'), { recursive: true })
    writeFileSync(join(lf.source, 'nuxt-port', 'page.vue'), '<template />', 'utf8')
    const runtime = join(lf.destination, 'apps', 'operator-console', 'page.vue')
    writeFileSync(runtime, 'developer-owned', 'utf8')
    result = promote(lf.source, lf.destination, '-AllowAppWrite')
    assert.notEqual(result.status, 0)
    assert.match(result.output, /Runtime promotion is intentionally disabled/)
    assert.equal(readFileSync(runtime, 'utf8'), 'developer-owned')
  } finally {
    rmSync(lf.base, { recursive: true, force: true })
    rmSync(crlf.base, { recursive: true, force: true })
  }
})
