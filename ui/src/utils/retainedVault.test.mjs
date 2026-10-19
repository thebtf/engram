import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { test } from 'node:test'
import * as catalog from './projectCatalog.ts'

const ts = createRequire(new URL('../../../apps/operator-console/package.json', import.meta.url))('typescript')
const first = '11111111-1111-4111-8111-111111111111'
const second = '22222222-2222-4222-8222-222222222222'
const reference = `urn:engram:migration:unresolved:${'a'.repeat(64)}`
const foreignReference = `urn:engram:migration:unresolved:${'b'.repeat(64)}`
const rows = [
  { id: 1, name: 'same/name', scope: 'project', project_id: first },
  { id: 2, name: 'same/name', scope: 'project', project_id: second },
  { id: 3, name: 'same/name', scope: 'global', project_id: null },
  { id: 4, name: 'same/name', scope: 'project', project_id: null, unresolved_reference: reference },
  { id: 5, name: 'same/name', scope: 'project', project_id: null, unresolved_reference: foreignReference },
]
const unavailable = [
  { id: 6, name: 'same/name', scope: 'project', project_id: null, binding_error: 'CREDENTIAL_BINDING_UNAVAILABLE', origin_project: 'historical-a' },
  { id: 7, name: 'same/name', scope: 'project', project_id: null, binding_error: 'CREDENTIAL_BINDING_CONFLICT', origin_project: first },
]

function clientApi(client, options = {}) {
  const calls = []
  const values = rows.map(() => randomUUID())
  let metadata = structuredClone(options.metadata ?? rows)
  const responses = rows.map((row, index) => ({ ...row, value: values[index] }))
  const selections = [`project=${first}`, `project=${second}`, 'scope=global', `id=4&unresolved_reference=${reference}`, `id=5&unresolved_reference=${foreignReference}`]
  const fetch = async (path, init = {}) => {
    const url = new URL(path, 'http://synthetic.invalid')
    calls.push({ url, method: init.method || 'GET' })
    if (url.pathname === '/api/vault/credentials') return Response.json(metadata)
    assert.equal(url.pathname, '/api/vault/credentials/same%2Fname')
    const index = selections.indexOf([...url.searchParams].map(([key, value]) => `${key}=${value}`).join('&'))
    assert.ok(index >= 0, 'Reveal must select an exact canonical/global namespace or native ID/reference')
    if (init.method === 'DELETE') {
      assert.ok(index < 3, 'retained credentials must never dispatch DELETE')
      metadata = metadata.filter(row => row.id !== rows[index].id)
      return Response.json({ current_state: rows[index] })
    }
    if (options.status) return new Response(null, { status: options.status })
    return Response.json(options.response ?? responses[index])
  }
  const globals = {
    fetch,
    useRuntimeConfig: () => ({ public: { apiBase: '/api' } }),
    $fetch: async (path, init) => {
      const response = await fetch(path, init)
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      return response.json()
    },
  }
  const file = client === 'ui' ? './api.ts' : '../../../apps/operator-web/app/composables/useOperatorApi.ts'
  const code = ts.transpileModule(readFileSync(new URL(file, import.meta.url), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(() => catalog, module, module.exports, ...Object.values(globals))
  return { api: module.exports, calls, values, replaceMetadata: next => { metadata = next } }
}

test('retained binding preserves native ID/reference without turning a retained row into global', () => {
  const binding = catalog.credentialBinding(rows[3])
  assert.equal(binding.id, 4)
  assert.equal(binding.name, rows[3].name)
  assert.equal(binding.scope, 'project')
  assert.equal(binding.project_id, null)
  assert.equal(binding.unresolved_reference, reference)
  for (const changed of [
    { ...rows[3], id: 5 },
    { ...rows[3], name: 'other' },
    { ...rows[3], unresolved_reference: foreignReference },
    { ...rows[3], unresolved_reference: undefined },
    { ...rows[3], project_id: first },
    { ...rows[3], scope: 'global' },
  ]) assert.throws(() => catalog.assertCredentialMetadata(changed, binding))
})

test('retained metadata cannot fabricate canonical/global authority or use an incomplete selector', () => {
  for (const row of [
    { ...rows[3], id: 0 },
    { ...rows[3], id: Number.MAX_SAFE_INTEGER + 1 },
    { ...rows[3], project_id: first },
    { ...rows[3], scope: 'global' },
    { ...rows[3], unresolved_reference: undefined },
    { ...rows[3], unresolved_reference: '' },
    { ...rows[3], unresolved_reference: 'same/name' },
    { ...rows[3], unresolved_reference: `urn:engram:migration:unresolved:${'x'.repeat(300)}` },
    { id: 4, name: rows[3].name, scope: 'project', project_id: 'legacy-checkout' },
  ]) assert.throws(() => catalog.credentialBinding(row))
})

for (const client of ['ui', 'operator-web']) {
  test(`${client}: list rows reveal distinct same-name canonical/global/retained credentials`, async () => {
    const { api, calls, values } = clientApi(client)
    const metadata = await api.fetchCredentials()
    for (const [index, expected] of rows.entries()) {
      const row = metadata.find(row => row.id === expected.id)
      assert.ok(row, 'every same-name credential must remain selectable by its native ID')
      const result = await api.fetchCredential(row)
      assert.ok(result.value === values[index], 'Reveal must return only the selected synthetic value')
    }
    const reveals = calls.filter(call => call.url.pathname !== '/api/vault/credentials')
    for (const [index, call] of reveals.entries()) {
      assert.equal(call.method, 'GET')
      if (index < 2) assert.deepEqual([...call.url.searchParams], [['project', rows[index].project_id]])
      else if (index === 2) assert.deepEqual([...call.url.searchParams], [['scope', 'global']])
      else assert.deepEqual([...call.url.searchParams], [['id', String(rows[index].id)], ['unresolved_reference', rows[index].unresolved_reference]])
    }
  })

  test(`${client}: canonical/global deletion stays exact and retained deletion refuses before mutation`, async () => {
    const { api, calls } = clientApi(client)
    for (const row of rows.slice(0, 3)) await api.deleteCredential(row)
    assert.deepEqual((await api.fetchCredentials()).map(row => row.id), [4, 5])
    const deletes = calls.filter(call => call.method === 'DELETE')
    assert.deepEqual(deletes.map(call => [...call.url.searchParams]), [[['project', first]], [['project', second]], [['scope', 'global']]])
    calls.length = 0
    for (const row of rows.slice(3)) await assert.rejects(() => api.deleteCredential(row), /retained|unresolved|read.only|unsupported/i)
    assert.equal(calls.some(call => call.method !== 'GET'), false)
  })

  for (const changed of [
    { ...rows[3], id: 5 },
    { ...rows[3], unresolved_reference: foreignReference },
    { ...rows[3], unresolved_reference: undefined },
    { ...rows[3], project_id: first },
    { ...rows[3], scope: 'global' },
  ]) test(`${client}: Reveal rejects a foreign retained response (${Object.keys(changed).filter(key => changed[key] !== rows[3][key]).join(',')})`, async () => {
    const { api } = clientApi(client, { response: { ...changed, value: randomUUID() } })
    await assert.rejects(() => api.fetchCredential(rows[3]))
  })

  for (const current of [[], [{ ...rows[3], unresolved_reference: foreignReference }], [{ ...rows[3], project_id: first, unresolved_reference: undefined }]]) {
    test(`${client}: Reveal rejects removed or rebound current metadata (${JSON.stringify(current.map(row => row.project_id ?? row.unresolved_reference))})`, async () => {
      const { api, replaceMetadata } = clientApi(client)
      replaceMetadata(current)
      await assert.rejects(() => api.fetchCredential(rows[3]))
    })
  }

  for (const status of [403, 404, 409]) test(`${client}: retained HTTP ${status} remains a refusal without canonical/global fallback`, async () => {
    const { api, calls } = clientApi(client, { status })
    await assert.rejects(() => api.fetchCredential(rows[3]))
    assert.deepEqual(calls.map(call => [...call.url.searchParams]), [[['id', '4'], ['unresolved_reference', reference]]])
  })
}

test('unavailable binding retains safe code/origin but rejects mixed or untrusted selectors', () => {
  for (const row of unavailable) {
    const binding = catalog.credentialBinding(row)
    assert.equal(binding.binding_error, row.binding_error)
    assert.equal(binding.origin_project, row.origin_project)
    assert.equal(binding.project_id, null)
  }
  for (const row of [
    { ...unavailable[0], project_id: first },
    { ...unavailable[0], project: 'historical-a' },
    { ...unavailable[0], unresolved_reference: reference },
    { ...unavailable[0], binding_error: 'untrusted-error-text' },
    { ...unavailable[0], origin_project: {} },
  ]) assert.throws(() => catalog.credentialBinding(row))
})

for (const client of ['ui', 'operator-web']) {
  test(`${client}: unavailable same-name rows remain visible without disabling valid rows or dispatching actions`, async () => {
    const { api, calls, values } = clientApi(client, { metadata: [...rows, ...unavailable] })
    const metadata = await api.fetchCredentials()
    const blocked = metadata.filter(row => row.binding_error)
    assert.equal(blocked.length, 2)
    for (const [index, expected] of rows.entries()) {
      const result = await api.fetchCredential(metadata.find(row => row.id === expected.id))
      assert.ok(result.value === values[index], 'unavailable siblings must not change the selected synthetic value')
    }
    calls.length = 0
    for (const row of blocked) {
      await assert.rejects(() => api.fetchCredential(row), /unavailable/i)
      await assert.rejects(() => api.deleteCredential(row), /unavailable/i)
    }
    assert.deepEqual(calls, [])
  })

  test(`${client}: retained Reveal refuses a now-unavailable metadata binding after the real read`, async () => {
    const { api, calls, replaceMetadata } = clientApi(client)
    replaceMetadata([{ ...unavailable[0], id: 4 }])
    await assert.rejects(() => api.fetchCredential(rows[3]))
    assert.deepEqual(calls.map(call => call.url.pathname), ['/api/vault/credentials/same%2Fname', '/api/vault/credentials'])
  })
}
