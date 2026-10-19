import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))

test('repeated override copy removes deleted source assets and preserves current assets', (t) => {
  const fixture = mkdtempSync(join(scriptDir, '.copy-static-i18n-test-'))
  t.after(() => {
    rmSync(fixture, { recursive: true, force: true })
    assert.equal(existsSync(fixture), false)
    t.diagnostic('owned synthetic fixture removed')
  })
  const scripts = join(fixture, 'scripts')
  const sourcePublic = join(fixture, 'public')
  const locales = join(fixture, 'i18n', 'locales')
  const outputRoot = join(fixture, 'isolated output')
  for (const directory of [scripts, sourcePublic, locales, outputRoot]) mkdirSync(directory, { recursive: true })
  for (const name of ['copy-static-i18n.mjs', 'build-paths.mjs']) {
    const bytes = readFileSync(join(scriptDir, name))
    writeFileSync(join(scripts, name), bytes)
    t.diagnostic(`${name} sha256=${createHash('sha256').update(bytes).digest('hex')}`)
  }
  t.diagnostic(`node=${process.version} platform=${process.platform} test-sha256=${createHash('sha256').update(readFileSync(fileURLToPath(import.meta.url))).digest('hex')}`)
  writeFileSync(join(sourcePublic, 'obsolete.txt'), 'deleted fixture asset')
  writeFileSync(join(sourcePublic, 'current.txt'), 'first version')
  writeFileSync(join(locales, 'en.json'), '{"fixture":"current"}')
  writeFileSync(join(outputRoot, 'keep-output.txt'), 'outside static-public')
  const entrypoint = join(scripts, 'copy-static-i18n.mjs')
  const options = { cwd: fixture, env: { ...process.env, ENGRAM_OPERATOR_OUTPUT_ROOT: outputRoot }, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }
  const stage = join(outputRoot, 'static-public')

  execFileSync(process.execPath, [entrypoint], options)
  assert.equal(readFileSync(join(stage, 'obsolete.txt'), 'utf8'), 'deleted fixture asset')
  assert.equal(readFileSync(join(stage, 'current.txt'), 'utf8'), 'first version')
  rmSync(join(sourcePublic, 'obsolete.txt'))
  writeFileSync(join(sourcePublic, 'current.txt'), 'second version')
  execFileSync(process.execPath, [entrypoint], options)

  assert.equal(readFileSync(join(stage, 'current.txt'), 'utf8'), 'second version')
  assert.equal(readFileSync(join(stage, 'i18n', 'locales', 'en.json'), 'utf8'), '{"fixture":"current"}')
  assert.equal(readFileSync(join(outputRoot, 'keep-output.txt'), 'utf8'), 'outside static-public')
  assert.equal(existsSync(join(sourcePublic, 'obsolete.txt')), false)
  t.diagnostic(`copier exits=0,0; current asset/locales and sibling output preserved; stale staged asset exists=${existsSync(join(stage, 'obsolete.txt'))}`)
  assert.equal(existsSync(join(stage, 'obsolete.txt')), false, 'repeat copy must remove the asset deleted from source public')
})
