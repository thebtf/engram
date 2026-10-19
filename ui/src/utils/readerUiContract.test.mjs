import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { test } from 'node:test'
import * as catalogParser from './projectCatalog.ts'

const require = createRequire(new URL('../../../apps/operator-console/package.json', import.meta.url))
const webRequire = createRequire(new URL('../../../apps/operator-web/package.json', import.meta.url))
let queryLibrary
let frameworkRequire = require
try { queryLibrary = webRequire('@tanstack/vue-query'); frameworkRequire = webRequire } catch (error) {
  if (error.code !== 'MODULE_NOT_FOUND') throw error
  try { queryLibrary = require('@tanstack/vue-query') } catch (fallback) {
    if (fallback.code !== 'MODULE_NOT_FOUND') throw fallback
  }
}
const ts = require('typescript')
const vue = frameworkRequire('vue')
const routing = frameworkRequire('vue-router')
const { compileScript, parse } = frameworkRequire('@vue/compiler-sfc')
const queryPrerequisite = queryLibrary ? undefined : 'BLOCKED: existing @tanstack/vue-query dependency is unavailable; do not replace it with a mock or install it in this stage'
const A = '11111111-1111-4111-8111-111111111111'
const B = '22222222-2222-4222-8222-222222222222'
const reference = `urn:engram:migration:unresolved:${'a'.repeat(64)}`
const catalog = {
  items: [{ project_id: A, name: 'Workspace A' }, { project_id: B, name: 'Workspace B' }],
  unresolved: [{ reference, name: 'Original checkout', reason: 'Membership unresolved', reader_kinds: ['memory', 'document', 'issue'] }],
}
const fixtures = {
  memory: {
    content: { reference, kind: 'memory', items: [{ id: 87, content: 'Original memory content', project: 'legacy-checkout', source_agent: 'claude-code', source_sessions: ['original-session'] }], total: 1, next_cursor: null },
    history: { reference, kind: 'memory', id: 87, items: [{ id: 701, memory_id: 87, action: 'memory.update', actor: 'original-operator', before_state_present: true, after_state_present: true, created_at: '2026-10-01T00:00:00Z' }], total: 1, next_cursor: null },
    attribution: ['project', 'legacy-checkout'], historyAttribution: ['actor', 'original-operator'],
  },
  document: {
    content: { reference, kind: 'document', items: [{ id: 88, path: 'retained.md', content: 'Original document content', project: 'legacy-checkout', version: 3, author: 'original-author', content_hash: 'current-document-hash', doc_type: 'note', metadata: '{}', created_at: '2026-10-01T00:00:00Z' }], total: 1, next_cursor: null },
    history: { reference, kind: 'document', id: 88, items: [{ id: 702, version: 2, author: 'previous-author', content_hash: 'previous-document-hash', created_at: '2026-09-30T00:00:00Z' }], total: 1, next_cursor: null },
    attribution: ['content_hash', 'current-document-hash'], historyAttribution: ['author', 'previous-author'],
  },
  issue: {
    content: { reference, kind: 'issue', items: [{ id: 89, title: 'Original issue', body: 'Original issue body', source_project: 'original-source', target_project: 'legacy-checkout', source_agent: 'codex' }], total: 1, next_cursor: null },
    history: { reference, kind: 'issue', id: 89, items: [{ id: 703, issue_id: 89, body: 'Original discussion', author_project: 'original-source', author_agent: 'codex', created_at: '2026-10-01T00:00:00Z' }], total: 1, next_cursor: null },
    attribution: ['source_project', 'original-source'], historyAttribution: ['author_agent', 'codex'],
  },
}
const tick = async () => { await new Promise(setImmediate); await vue.nextTick() }
const renderer = vue.createRenderer({
  createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
  setText() { }, setElementText() { }, patchProp() { }, parentNode: () => null, nextSibling: () => null, insert() { }, remove() { },
})

function evaluate(source, dependency, globals = {}) {
  const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(dependency, module, module.exports, ...Object.values(globals))
  return module.exports
}
function component(file, dependency, globals = {}) {
  const descriptor = parse(readFileSync(new URL(file, import.meta.url), 'utf8')).descriptor
  const result = evaluate(compileScript(descriptor, { id: file }).content, dependency, globals).default
  // Exercise real script-setup state/actions, not browser DOM or native v-model.
  result.render = () => null
  return result
}
function transport(kind = 'memory', options = {}) {
  const calls = []
  const savedRules = []
  const fixture = fixtures[kind]
  const fetch = async (path, init = {}) => {
    const url = new URL(path, 'http://synthetic.invalid')
    const method = init.method || 'GET'
    calls.push({ url, method, body: init.body === undefined ? undefined : structuredClone(init.body) })
    if (url.pathname === '/api/projects') return options.catalogError ? Response.json({ error: 'Catalog unavailable' }, { status: 503 }) : Response.json(catalog)
    if (url.pathname === '/api/projects/resolve') {
      if (options.resolveError) return Response.json({ error: 'Resolver unavailable' }, { status: 503 })
      if (url.searchParams.get('legacy_id') === 'legacy-checkout') return Response.json({ status: 'unresolved', reference, reason: 'Membership unresolved' })
      return Response.json({ error: 'Unknown project' }, { status: 404 })
    }
    if (url.pathname === '/api/rules' && method === 'POST') {
      const body = typeof init.body === 'string' ? JSON.parse(init.body) : init.body
      const rule = { ...body, id: savedRules.length + 1, version: 1 }
      savedRules.push(rule)
      return Response.json(rule)
    }
    if (url.pathname === '/api/rules' && method === 'GET') return Response.json({ rules: savedRules, total: savedRules.length })
    if (url.pathname === `/api/projects/unresolved/${encodeURIComponent(reference)}/content`) {
      assert.equal(url.searchParams.get('kind'), kind)
      assert.equal(url.searchParams.has('project'), false)
      if (options.status) return Response.json({ error: 'Retained access refused' }, { status: options.status })
      return Response.json(options.content ?? fixture.content)
    }
    if (url.pathname === `/api/projects/unresolved/${encodeURIComponent(reference)}/history`) {
      assert.equal(url.searchParams.get('kind'), kind)
      assert.equal(url.searchParams.get('id'), String(fixture.content.items[0].id))
      assert.equal(url.searchParams.has('project'), false)
      return Response.json(fixture.history)
    }
    throw new Error(`Unexpected fixed transport request ${method} ${url}`)
  }
  const globals = {
    fetch,
    useRoute: routing.useRoute, useRouter: routing.useRouter,
    useRuntimeConfig: () => ({ public: { apiBase: '/api' } }),
    $fetch: async (path, init) => {
      const response = await fetch(path, init)
      const data = await response.json()
      if (!response.ok) throw Object.assign(new Error(data.error), { status: response.status })
      return data
    },
  }
  const modules = new Map()
  function dependency(name) {
    if (name === 'vue') return vue
    if (name === '@tanstack/vue-query') {
      assert.ok(queryLibrary, queryPrerequisite)
      return queryLibrary
    }
    if (name.endsWith('projectCatalog')) return catalogParser
    if (name.endsWith('useOperatorI18n')) return { useOperatorI18n: () => ({ t: key => key }) }
    if (name.endsWith('formatters')) return { formatAbsoluteDate: value => value }
    if (name.endsWith('ProjectCatalogStatus.vue')) return component('../../../apps/operator-web/app/components/ProjectCatalogStatus.vue', dependency, globals)
    if (name.endsWith('ProjectCatalogReview.vue')) return component('../components/ProjectCatalogReview.vue', dependency, globals)
    const file = name.split('/').at(-1)
    if (!modules.has(file)) modules.set(file, evaluate(readFileSync(new URL(`../../../apps/operator-web/app/composables/${file}.ts`, import.meta.url), 'utf8'), dependency, globals))
    return modules.get(file)
  }
  const web = dependency('useOperatorApi')
  const ui = evaluate(readFileSync(new URL('./api.ts', import.meta.url), 'utf8'), () => catalogParser, globals)
  return { calls, savedRules, web, ui, dependency, globals }
}
function mountReview(t, consumer, props) {
  const review = component('../components/ProjectCatalogReview.vue', name => name === 'vue' ? vue : catalogParser)
  const app = renderer.createApp(review, { labels: { unknownProject: 'Unknown project' }, read: consumer.web.fetchRetainedContent, ...props })
  const instance = app.mount({})
  t.after(() => app.unmount())
  return instance.$.setupState
}
function hasField(item, [label, value]) { return item.fields.some(field => field.label === label && field.value === value) }

for (const client of ['ui', 'web']) {
  for (const kind of ['memory', 'document', 'issue']) test(`${client}: backend-shaped retained ${kind} reads native IDs/content/attribution then exact history`, async () => {
    const consumer = transport(kind)
    const fixture = fixtures[kind]
    const page = await consumer[client].fetchRetainedContent(reference, kind)
    const expected = fixture.content.items[0]
    assert.equal(page.items[0].id, expected.id)
    assert.equal(page.items[0].content, expected.content ?? expected.body)
    assert.ok(hasField(page.items[0], fixture.attribution))
    const history = await consumer[client].fetchRetainedContent(reference, kind, page.items[0].id)
    assert.equal(history.items[0].id, fixture.history.items[0].id)
    assert.ok(hasField(history.items[0], fixture.historyAttribution))
    assert.deepEqual(consumer.calls.map(call => call.url.pathname.split('/').at(-1)), ['content', 'history'])
  })
  for (const status of [403, 404, 409]) test(`${client}: retained backend ${status} refuses without fallback`, async () => {
    const consumer = transport('memory', { status })
    await assert.rejects(() => consumer[client].fetchRetainedContent(reference, 'memory'))
    assert.equal(consumer.calls.length, 1)
    assert.ok(consumer.calls[0].url.pathname.endsWith('/content'))
  })
  test(`${client}: invalid retained scope/items/cursor and explicit error payloads refuse`, async () => {
    const base = fixtures.memory.content
    for (const content of [
      { ...base, reference: 'foreign' }, { ...base, kind: 'document' }, { ...base, items: null },
      { ...base, items: [null] }, { ...base, items: [{ id: 0, content: 'invalid' }] },
      { ...base, next_cursor: 12 }, { ...base, next_cursor: '' }, { ...base, items: [], next_cursor: 'next' },
      { error: 'Retained backend unavailable' },
    ]) {
      const consumer = transport('memory', { content })
      await assert.rejects(() => consumer[client].fetchRetainedContent(reference, 'memory'))
      assert.equal(consumer.calls.length, 1)
    }
  })
}

for (const kind of ['memory', 'document', 'issue']) test(`retained ${kind} component exposes original rows and opens their selected history`, async t => {
  const consumer = transport(kind)
  const record = { ...catalog.unresolved[0], reader_kinds: [kind] }
  const state = mountReview(t, consumer, { records: [record], reference, pending: false, error: null })
  await tick()
  assert.equal(state.readError, '', 'valid backend-shaped content must not become a reader error')
  assert.equal(state.items.length, 1)
  assert.equal(state.items[0].id, fixtures[kind].content.items[0].id)
  assert.equal(state.items[0].content, fixtures[kind].content.items[0].content ?? fixtures[kind].content.items[0].body)
  assert.ok(hasField(state.items[0], fixtures[kind].attribution))
  state.openHistory(state.items[0].id)
  await tick()
  assert.equal(state.historyId, fixtures[kind].content.items[0].id)
  assert.equal(state.history[0].id, fixtures[kind].history.items[0].id)
  assert.ok(hasField(state.history[0], fixtures[kind].historyAttribution))
})

test('retained component keeps explicit backend errors visible without successful-empty fallback', async t => {
  const consumer = transport('memory', { status: 403 })
  const state = mountReview(t, consumer, { records: catalog.unresolved, reference, pending: false, error: null })
  await tick()
  assert.equal(state.readError, 'Retained access refused')
  assert.deepEqual(state.items, [])
  assert.deepEqual(state.history, [])
  assert.equal(consumer.calls.length, 1)
})

async function mountRules(t, location, options = {}) {
  const consumer = transport('memory', options)
  const queryClient = new queryLibrary.QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } } })
  if (!options.catalogError) queryClient.setQueryData(['projects'], catalog)
  const router = routing.createRouter({ history: routing.createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
  await router.push(location)
  const page = component('../../../apps/operator-web/app/pages/rules.vue', consumer.dependency, consumer.globals)
  const app = renderer.createApp(page)
  app.use(router)
  app.use(queryLibrary.VueQueryPlugin, { queryClient })
  const state = app.mount({}).$.setupState
  t.after(() => { app.unmount(); queryClient.clear() })
  await tick()
  return { ...consumer, state, router }
}

test('cached catalog initializes Rules scope from selected UUID before first submit', { skip: queryPrerequisite }, async t => {
  const mounted = await mountRules(t, `/rules?project=${A}`)
  assert.equal(mounted.state.selectedProject, A)
  assert.equal(mounted.state.ready, true)
  assert.equal(mounted.state.createProject, A, 'fresh form must not inherit implicit global scope from a missed watch')
  mounted.state.createContent = 'Project-only rule'
  mounted.state.submitCreate()
  await tick()
  assert.equal(mounted.savedRules.length, 1)
  assert.equal(mounted.savedRules[0].project, A)
  assert.equal(mounted.savedRules[0].content, 'Project-only rule')
})

test('Rules preserves deliberate global creation and resynchronizes subsequent project changes', { skip: queryPrerequisite }, async t => {
  const mounted = await mountRules(t, `/rules?project=${A}`)
  mounted.state.createProject = ''
  mounted.state.createContent = 'Deliberately global rule'
  mounted.state.submitCreate()
  await tick()
  assert.equal(mounted.savedRules.length, 1)
  assert.equal(mounted.savedRules[0].project, undefined)
  assert.equal(mounted.savedRules[0].content, 'Deliberately global rule')
  await mounted.router.push(`/rules?project=${B}`)
  await tick()
  assert.equal(mounted.state.createProject, B)
  mounted.state.createContent = 'Second project rule'
  mounted.state.submitCreate()
  await tick()
  assert.equal(mounted.savedRules[1].project, B)
})

test('legacy unresolved deeplink selects known retained reference while canonical operations remain unready', { skip: queryPrerequisite }, async t => {
  const mounted = await mountRules(t, '/rules?project=legacy-checkout&q=original')
  assert.equal(mounted.state.unresolvedReference, reference)
  assert.equal(mounted.router.currentRoute.value.query.project, 'legacy-checkout')
  assert.equal(mounted.router.currentRoute.value.query.q, 'original')
  assert.equal(mounted.state.selectedProject, '')
  assert.equal(mounted.state.ready, false)
  mounted.state.createContent = 'Must not widen into global'
  mounted.state.submitCreate()
  await tick()
  assert.equal(mounted.calls.filter(call => call.url.pathname === '/api/rules').length, 0)
  assert.equal(mounted.state.selectionError, null, 'expected unresolved resolution must not become fatal catalog-unavailable')
  const review = mountReview(t, mounted, vue.reactive({ records: mounted.state.unresolved, reference: mounted.state.unresolvedReference, pending: mounted.state.selectionPending, error: mounted.state.selectionError }))
  await tick()
  assert.equal(review.selected, reference)
  assert.equal(review.readError, '')
  assert.equal(review.items[0].id, 87)
  review.openHistory(review.items[0].id)
  await tick()
  assert.equal(review.history[0].id, 701)
  assert.equal(mounted.calls.filter(call => call.url.pathname === '/api/rules').length, 0)
})

for (const [label, location, options] of [
  ['catalog failure', `/rules?project=${A}&unresolved=${encodeURIComponent(reference)}`, { catalogError: true }],
  ['resolver failure', '/rules?project=legacy-checkout', { resolveError: true }],
  ['unknown legacy selector', '/rules?project=unknown', {}],
]) test(`${label} remains explicit and cannot enable canonical or retained reads`, { skip: queryPrerequisite }, async t => {
  const mounted = await mountRules(t, location, options)
  assert.equal(mounted.state.ready, false)
  assert.ok(mounted.state.selectionError)
  assert.equal(mounted.state.selectedProject, '')
  const review = mountReview(t, mounted, { records: mounted.state.unresolved, reference: mounted.state.unresolvedReference, pending: mounted.state.selectionPending, error: mounted.state.selectionError })
  await tick()
  assert.deepEqual(review.items, [])
  mounted.state.createContent = 'Must remain blocked'
  mounted.state.submitCreate()
  await tick()
  assert.equal(mounted.calls.filter(call => call.url.pathname === '/api/rules' || call.url.pathname.includes('/unresolved/')).length, 0)
})
