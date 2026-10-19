import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import * as routing from 'vue-router'
import * as projectCatalog from '../../../../ui/src/utils/projectCatalog.ts'

const first = '11111111-1111-4111-8111-111111111111'
const second = '22222222-2222-4222-8222-222222222222'
const reference = `urn:engram:migration:unresolved:${'a'.repeat(64)}`
const otherReference = `urn:engram:migration:unresolved:${'b'.repeat(64)}`
const rows = [
  { id: 1, name: 'same/name', scope: 'project', project_id: first },
  { id: 2, name: 'same/name', scope: 'project', project_id: second },
  { id: 3, name: 'same/name', scope: 'global', project_id: null },
  { id: 4, name: 'same/name', scope: 'project', project_id: null, unresolved_reference: reference },
  { id: 5, name: 'same/name', scope: 'project', project_id: null, unresolved_reference: otherReference },
]
const unavailable = [
  { id: 6, name: 'same/name', scope: 'project', project_id: null, binding_error: 'CREDENTIAL_BINDING_UNAVAILABLE', origin_project: 'historical-a' },
  { id: 7, name: 'same/name', scope: 'project', project_id: null, binding_error: 'CREDENTIAL_BINDING_CONFLICT', origin_project: first },
]
const renderer = vue.createRenderer({
  createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
  setText: () => { }, setElementText: () => { }, patchProp: () => { }, parentNode: () => null,
  nextSibling: () => null, insert: () => { }, remove: () => { },
})

async function mount(t, initial = rows, options = {}) {
  const calls = []
  const values = rows.map(() => randomUUID())
  const reveals = rows.map((row, index) => ({ ...row, value: values[index] }))
  const selectors = [`project=${first}`, `project=${second}`, 'scope=global', `id=4&unresolved_reference=${reference}`, `id=5&unresolved_reference=${otherReference}`]
  let metadata = structuredClone(initial)
  const fetch = async (path, init = {}) => {
    const url = new URL(path, 'http://synthetic.invalid')
    const method = init.method || 'GET'
    calls.push({ url, method })
    if (url.pathname === '/api/projects') return Response.json({ items: [first, second].map(project_id => ({ project_id, name: 'Same label' })), unresolved: [] })
    if (url.pathname === '/api/vault/status') return Response.json({ key_configured: true, credential_count: metadata.length })
    if (url.pathname === '/api/vault/credentials') return Response.json(metadata)
    assert.equal(url.pathname, '/api/vault/credentials/same%2Fname')
    const index = selectors.indexOf([...url.searchParams].map(([key, value]) => `${key}=${value}`).join('&'))
    assert.ok(index >= 0, 'the real console must build an exact credential selector')
    if (method === 'DELETE') {
      assert.ok(index < 3, 'retained credentials are read-only')
      metadata = metadata.filter(row => row.id !== rows[index].id)
      return Response.json({ operation_state: 'completed', readback: { authoritative: true, kind: 'current', current_state: { ...rows[index], deleted: true } } })
    }
    if (options.status) return new Response(null, { status: options.status })
    return Response.json(options.response ?? reveals[index])
  }
  const storage = new Map()
  const modules = new Map()
  function dependency(name) {
    if (name === 'vue') return vue
    if (name === 'vue-router') return routing
    if (name.endsWith('operator-config')) return { operatorConfig: { apiBase: '/api' } }
    if (name.endsWith('useConsolePreferences')) return { startedLoads: new Set() }
    if (name.endsWith('projectCatalog')) return projectCatalog
    const file = name.split('/').at(-1)
    if (!modules.has(file)) {
      const source = readFileSync(new URL(`../../composables/${file}.ts`, import.meta.url), 'utf8')
      const code = ts.transpileModule(source, {
        compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
        transformers: {
          before: [context => source => ts.visitNode(source, function visit(node) {
            if (ts.isMetaProperty(node) && node.keywordToken === ts.SyntaxKind.ImportKeyword) return ts.factory.createIdentifier('importMeta')
            return ts.visitEachChild(node, visit, context)
          })]
        },
      }).outputText
      const globals = { fetch, importMeta: { env: { DEV: false } }, ...(file === 'useOperatorProjectContext' ? { window: {}, sessionStorage: { getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value) } } : {}) }
      const module = { exports: {} }
      new Function('require', 'module', 'exports', ...Object.keys(globals), code)(dependency, module, module.exports, ...Object.values(globals))
      modules.set(file, module.exports)
    }
    return modules.get(file)
  }
  const core = dependency('./useOperatorProjectContext')
  const vault = dependency('./useOperatorSecrets')
  const router = routing.createRouter({ history: routing.createMemoryHistory(), routes: [{ path: '/secrets', component: { render: () => null } }] })
  await router.push('/secrets?project=all')
  let secrets
  const app = renderer.createApp({ setup() { core.useOperatorProjectContext(); secrets = vault.useOperatorSecrets(); return () => null } })
  app.use(router).mount({})
  t.after(() => app.unmount())
  await secrets.refresh()
  return { secrets, calls, values, replaceMetadata: next => { metadata = next } }
}

test('console loads same-name canonical/global/retained metadata and reveals each exact row', async t => {
  const { secrets, calls, values } = await mount(t)
  assert.equal(secrets.loadState.value.kind, 'live')
  assert.deepEqual(secrets.creds.map(row => row.id), ['1', '2', '3', '4', '5'])
  for (const [index, row] of secrets.creds.entries()) {
    assert.ok(await secrets.revealSecret(row) === values[index], 'console must expose only the selected synthetic value')
  }
  const reads = calls.filter(call => call.url.pathname === '/api/vault/credentials/same%2Fname')
  assert.deepEqual(reads.map(call => [...call.url.searchParams]), [
    [['project', first]], [['project', second]], [['scope', 'global']],
    [['id', '4'], ['unresolved_reference', reference]], [['id', '5'], ['unresolved_reference', otherReference]],
  ])
})

test('console canonical/global deletes remain scoped and cannot affect a same-name sibling', async t => {
  const { secrets, calls } = await mount(t, rows.slice(0, 3))
  assert.equal(secrets.loadState.value.kind, 'live')
  const selected = [...secrets.creds]
  for (const row of selected) {
    const receipt = await secrets.deleteSecret(row)
    assert.equal(receipt.kind, 'committed_verified')
  }
  assert.deepEqual(calls.filter(call => call.method === 'DELETE').map(call => [...call.url.searchParams]), [
    [['project', first]], [['project', second]], [['scope', 'global']],
  ])
})

test('console retained Delete clearly refuses before any mutating request', async t => {
  const { secrets, calls } = await mount(t)
  assert.equal(secrets.loadState.value.kind, 'live')
  const retained = secrets.creds.find(row => row.id === '4')
  assert.ok(retained)
  calls.length = 0
  await assert.rejects(() => secrets.deleteSecret(retained), /retained|unresolved|read.only|unsupported/i)
  assert.equal(calls.some(call => call.method !== 'GET'), false)
})

for (const changed of [
  { ...rows[3], id: 5 },
  { ...rows[3], unresolved_reference: otherReference },
  { ...rows[3], unresolved_reference: undefined },
  { ...rows[3], project_id: first },
  { ...rows[3], scope: 'global' },
]) test(`console retained Reveal rejects mismatched ${Object.keys(changed).filter(key => changed[key] !== rows[3][key]).join(',')}`, async t => {
  const { secrets } = await mount(t, rows, { response: { ...changed, value: randomUUID() } })
  assert.equal(secrets.loadState.value.kind, 'live')
  await assert.rejects(() => secrets.revealSecret(secrets.creds.find(row => row.id === '4')))
})

for (const status of [403, 404, 409]) test(`console retained HTTP ${status} cannot fall back to a name or global credential`, async t => {
  const { secrets, calls } = await mount(t, rows, { status })
  assert.equal(secrets.loadState.value.kind, 'live')
  const retained = secrets.creds.find(row => row.id === '4')
  calls.length = 0
  await assert.rejects(() => secrets.revealSecret(retained))
  assert.deepEqual(calls.map(call => [...call.url.searchParams]), [[['id', '4'], ['unresolved_reference', reference]]])
})

test('console keeps distinct unavailable same-name rows and useful siblings with no blocked action dispatch', async t => {
  const { secrets, calls, values } = await mount(t, [...rows, ...unavailable])
  assert.equal(secrets.loadState.value.kind, 'live')
  const blocked = secrets.creds.filter(row => row.binding_error)
  assert.deepEqual(blocked.map(row => [row.id, row.binding_error, row.origin_project]), unavailable.map(row => [String(row.id), row.binding_error, row.origin_project]))
  for (const [index, expected] of rows.entries()) {
    assert.ok(await secrets.revealSecret(secrets.creds.find(row => row.id === String(expected.id))) === values[index], 'unavailable siblings must not hide or alter useful credentials')
  }
  calls.length = 0
  for (const row of blocked) {
    await assert.rejects(() => secrets.revealSecret(row), /unavailable/i)
    await assert.rejects(() => secrets.deleteSecret(row), /unavailable/i)
  }
  assert.deepEqual(calls, [])
})

test('console rejects contradictory unavailable metadata rather than giving it a canonical/global selector', async t => {
  const { secrets, calls } = await mount(t, [...rows, { ...unavailable[0], project_id: first }])
  assert.equal(secrets.loadState.value.kind, 'error')
  assert.equal(calls.some(call => call.url.pathname === '/api/vault/credentials/same%2Fname'), false)
})

test('console rejects a retained binding that becomes unavailable during metadata readback', async t => {
  const { secrets, calls, replaceMetadata } = await mount(t)
  const selected = secrets.creds.find(row => row.id === '4')
  replaceMetadata([{ ...unavailable[0], id: 4 }])
  calls.length = 0
  await assert.rejects(() => secrets.revealSecret(selected))
  assert.deepEqual(calls.map(call => call.url.pathname), ['/api/vault/credentials/same%2Fname', '/api/vault/credentials'])
})
