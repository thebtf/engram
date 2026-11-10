import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import * as routing from 'vue-router'

const root = new URL('../../', import.meta.url)
const projectID = '11111111-1111-4111-8111-111111111111'
const tick = async () => { await new Promise(setImmediate); await vue.nextTick() }

function evaluate(source, require, globals) {
  const module = { exports: {} }
  const code = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    transformers: {
      before: [(context) => (file) => ts.visitNode(file, function visit(node) {
        if (ts.isMetaProperty(node) && node.keywordToken === ts.SyntaxKind.ImportKeyword) return ts.factory.createIdentifier('importMeta')
        return ts.visitEachChild(node, visit, context)
      })]
    },
  }).outputText
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(require, module, module.exports, ...Object.values(globals))
  return module.exports
}

function hostNode(tag = '', text = '') { return { tag, text, props: {}, children: [], parent: null, focus() { }, scrollIntoView() { } } }
const renderer = vue.createRenderer({
  createElement: (tag) => hostNode(tag), createText: (text) => hostNode('', text), createComment: () => hostNode(),
  patchProp: (node, key, _previous, value) => { node.props[key] = value },
  setText: (node, text) => { node.text = text }, setElementText: (node, text) => { node.children = [hostNode('', text)] },
  parentNode: (node) => node.parent,
  nextSibling: (node) => node.parent?.children[node.parent.children.indexOf(node) + 1] || null,
  insert: (node, parent, anchor) => {
    if (node.parent) node.parent.children = node.parent.children.filter((child) => child !== node)
    node.parent = parent
    const position = anchor ? parent.children.indexOf(anchor) : -1
    if (position < 0) parent.children.push(node); else parent.children.splice(position, 0, node)
  },
  remove: (node) => { if (node.parent) node.parent.children = node.parent.children.filter((child) => child !== node) },
})
function walk(node) { return [node, ...node.children.flatMap(walk)] }

async function mountConsumer(t, location, { storage = new Map(), rejectMemory = false, catalogReady = Promise.resolve(), resolveLegacy = false, catalogFetch, memoryFetch, searchFetch } = {}) {
  const requests = []
  let reader
  const fetch = async (input, init) => {
    const url = new URL(input, 'http://synthetic.invalid')
    if (url.pathname === '/api/projects') { await catalogReady; return catalogFetch ? catalogFetch() : Response.json({ items: [{ project_id: projectID, name: 'Synthetic project' }], unresolved: [] }) }
    if (url.pathname === '/api/projects/resolve') return resolveLegacy ? Response.json({ status: 'resolved', project_id: projectID, name: 'Synthetic project' }) : Response.json({ error: 'Mapping unavailable' }, { status: 503 })
    if (url.pathname === '/api/context/search' && searchFetch) return searchFetch(url, init)
    if (url.pathname.startsWith('/api/search/') || url.pathname === '/api/stats/retrieval' || url.pathname === '/api/stats/vnext') return Response.json({ queries: [] })
    if (url.pathname.startsWith('/api/memories/') && memoryFetch) return memoryFetch(url, init)
    if (url.pathname === '/api/memories') {
      requests.push(url)
      if (memoryFetch) return memoryFetch(url, init)
      return rejectMemory ? Response.json({ error: 'Synthetic request refusal' }, { status: 400 }) : Response.json({
        items: [], total: 0, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false,
      })
    }
    throw new Error(`Unexpected fixture request: ${url.pathname}`)
  }
  const globals = {
    fetch, importMeta: { env: { DEV: false } },
    useRoute: routing.useRoute, useRouter: routing.useRouter,
    useI18n: () => ({ t: (key) => key, locale: vue.ref('en') }),
    sessionStorage: { getItem: (key) => storage.get(key) || null, setItem: (key, value) => storage.set(key, value) },
  }
  const modules = new Map()
  function load(name) {
    if (modules.has(name)) return modules.get(name)
    const value = evaluate(readFileSync(new URL(`composables/${name}.ts`, root), 'utf8'), dependency, name === 'useOperatorProjectContext' ? { ...globals, window: {} } : globals)
    modules.set(name, value)
    return value
  }
  function dependency(name) {
    if (name === 'vue') return vue
    if (name === 'vue-router') return routing
    if (name.endsWith('operator-config')) return { operatorConfig: { apiBase: '/api' } }
    if (name.endsWith('useConsolePreferences')) return { startedLoads: new Set() }
    if (name.endsWith('useOperatorMemoryLab')) {
      const actual = load('useOperatorMemoryLab')
      return { ...actual, useOperatorMemoryLab: (...args) => { reader = actual.useOperatorMemoryLab(...args); return reader } }
    }
    return load(name.split('/').at(-1))
  }
  const source = parse(readFileSync(new URL('pages/memory.vue', root), 'utf8'), { filename: fileURLToPath(new URL('pages/memory.vue', root)) }).descriptor
  // Native text/checkbox v-model is outside this Node renderer; filter button handlers remain compiled unchanged.
  const page = evaluate(compileScript(source, {
    id: 'memory-filters-consumer', inlineTemplate: true,
    templateOptions: { compilerOptions: { directiveTransforms: { model: () => ({ props: [] }) } } },
  }).content, dependency, globals).default
  const searchSource = parse(readFileSync(new URL('pages/search.vue', root), 'utf8'), { filename: fileURLToPath(new URL('pages/search.vue', root)) }).descriptor
  const searchPage = evaluate(compileScript(searchSource, { id: 'search-memory-consumer', inlineTemplate: true, templateOptions: { compilerOptions: { directiveTransforms: { model: () => ({ props: [] }) } } } }).content, dependency, globals).default
  const router = routing.createRouter({
    history: routing.createMemoryHistory(), routes: [
      { path: '/memory', component: page }, { path: '/documents', component: { render: () => null } },
      { path: '/search', component: searchPage },
    ]
  })
  await router.push(location)
  let context
  const app = renderer.createApp({ setup() { context = load('useOperatorProjectContext').useOperatorProjectContext(); return () => vue.h(routing.RouterView) } })
  app.component('MutationResultNotice', { props: ['result', 'recheckLabel'], render: () => null })
  app.use(router)
  const host = hostNode('root')
  app.mount(host)
  t.after(() => app.unmount())
  await router.isReady()
  await tick()
  const chip = (value) => walk(host).find((node) => node.props['data-memory-filter'] === value)
  return { router, context, host, chip, requests, storage, reader: () => reader }
}

test('principal scope resolves a confirmed alias to the canonical choice', async t => {
  const alias = '22222222-2222-4222-8222-222222222222'
  const catalogFetch = () => Response.json({ items: [{ project_id: projectID, name: 'Primary' }, { project_id: alias, name: 'Historical' }], unresolved: [], logical_groups: [{ primary_project_id: projectID, alias_project_ids: [alias] }] })
  const consumer = await mountConsumer(t, `/memory?project=${projectID}&principal=person&principalProject=${alias}`, { catalogFetch })
  const select = walk(consumer.host).find(node => node.props['data-testid'] === 'project-scope')
  const options = walk(select).filter(node => node.tag === 'option')
  assert.equal(options.filter(node => node.props.value === alias).length, 0)
  assert.equal(options.filter(node => node.props.value === projectID).length, 1)
  assert.deepEqual(consumer.context.projects.value, [projectID])
  assert.equal(consumer.router.currentRoute.value.query.principalProject, projectID)
})

test('detail mutation uses server-native binding after alias route and union list', async t => {
  const alias = '22222222-2222-4222-8222-222222222222'
  const catalogFetch = () => Response.json({ items: [{ project_id: projectID, name: 'Primary' }, { project_id: alias, name: 'Alias' }], unresolved: [], logical_groups: [{ primary_project_id: projectID, alias_project_ids: [alias] }] })
  const deletes = []
  const memoryFetch = (url, init) => {
    if (init?.method === 'DELETE') { deletes.push(url.searchParams.get('project')); return Response.json({ status: 'ok' }) }
    const row = { id: 501, project: projectID, mutation_project: alias, content: 'alias native', status: 'active', version: 1 }
    if (url.pathname.endsWith('/501')) return Response.json(row)
    return Response.json({ items: [row], total: 1, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
  }
  const consumer = await mountConsumer(t, `/memory?project=${alias}`, { catalogFetch, memoryFetch })
  const reader = consumer.reader()
  const detail = await reader.loadMemoryByID('501', projectID)
  await reader.deleteMemory(detail.id, detail.mutationProject, detail.project)
  await consumer.router.push('/memory?project=all&allRead=1')
  await tick()
  const fromUnion = await reader.loadMemoryByID('501', projectID)
  await reader.deleteMemory(fromUnion.id, fromUnion.mutationProject, fromUnion.project)
  assert.deepEqual(deletes, [alias, alias])
})

test('search opens a confirmed alias result in its native memory scope', async t => {
  const alias = '22222222-2222-4222-8222-222222222222'
  const reads = []
  const catalogFetch = () => Response.json({ items: [{ project_id: projectID, name: 'Primary' }, { project_id: alias, name: 'Alias' }], unresolved: [], logical_groups: [{ primary_project_id: projectID, alias_project_ids: [alias] }] })
  const searchFetch = () => Response.json({ project: projectID, observations: [{ id: 501, project: alias, type: 'discovery', memory_type: 'context', content: 'Alias result' }] })
  const memoryFetch = (url) => {
    if (url.pathname.endsWith('/audit')) return Response.json({ memory_id: 501, entries: [] })
    if (url.pathname.endsWith('/501')) { reads.push(url.searchParams.get('project')); return Response.json({ id: 501, project: alias, mutation_project: alias, content: 'Native memory content', status: 'active', version: 1 }) }
    return Response.json({ items: [], total: 0, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
  }
  const consumer = await mountConsumer(t, `/search?project=${projectID}&q=alias`, { catalogFetch, searchFetch, memoryFetch })
  await tick(); await tick()
  const open = walk(consumer.host).find(node => node.props['data-testid'] === 'search-result-open-501')
  assert.ok(open, 'confirmed native result remains actionable')
  open.props.onClick()
  await tick(); await tick(); await tick()
  assert.equal(consumer.router.currentRoute.value.path, '/memory')
  assert.equal(consumer.router.currentRoute.value.query.project, projectID)
  assert.equal(consumer.router.currentRoute.value.query.memoryProject, alias)
  assert.deepEqual(reads, [alias])
  assert.ok(walk(consumer.host).some(node => node.text.includes('Native memory content')))
})

for (const [name, resultProject, groups] of [
  ['foreign', '33333333-3333-4333-8333-333333333333', [{ primary_project_id: projectID, alias_project_ids: ['22222222-2222-4222-8222-222222222222'] }]],
  ['unconfirmed', '22222222-2222-4222-8222-222222222222', []],
  ['malformed group', '22222222-2222-4222-8222-222222222222', [{ primary_project_id: projectID, alias_project_ids: ['22222222-2222-4222-8222-222222222222', 'invalid'] }]],
  ['malformed project', 'invalid', [{ primary_project_id: projectID, alias_project_ids: ['22222222-2222-4222-8222-222222222222'] }]],
]) {
  test(`search rejects ${name} result`, async t => {
    const ids = [projectID, '22222222-2222-4222-8222-222222222222', '33333333-3333-4333-8333-333333333333']
    const catalogFetch = () => Response.json({ items: ids.map(project_id => ({ project_id, name: project_id })), unresolved: [], logical_groups: groups })
    const searchFetch = () => Response.json({ project: projectID, observations: [{ id: 501, project: resultProject, type: 'discovery', memory_type: 'context', content: 'Untrusted result' }] })
    const consumer = await mountConsumer(t, `/search?project=${projectID}&q=alias`, { catalogFetch, searchFetch })
    await tick(); await tick()
    assert.equal(walk(consumer.host).some(node => node.props['data-testid'] === 'search-result-open-501'), false)
  })
}

test('memory deep link does not read a foreign native anchor under primary scope', async t => {
  const foreign = '33333333-3333-4333-8333-333333333333'
  const reads = []
  const catalogFetch = () => Response.json({ items: [{ project_id: projectID, name: 'Primary' }, { project_id: foreign, name: 'Foreign' }], unresolved: [], logical_groups: [] })
  const memoryFetch = url => {
    if (url.pathname.endsWith('/501')) reads.push(url)
    return Response.json({ items: [], total: 0, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
  }
  await mountConsumer(t, `/memory?project=${projectID}&memoryProject=${foreign}&memory=501`, { catalogFetch, memoryFetch })
  await tick(); await tick()
  assert.deepEqual(reads, [])
})

test('combined filters survive scoped paging, navigation return and session reload', async (t) => {
  const consumer = await mountConsumer(t, `/memory?project=${projectID}&q=known&page=3&size=10&filter=low&filter=chain`)
  const request = consumer.requests.at(-1)
  assert.deepEqual(request.searchParams.getAll('filter'), ['low', 'chain'])
  assert.equal(request.searchParams.get('project'), projectID)
  assert.equal(request.searchParams.get('offset'), '20')
  assert.equal(request.searchParams.get('limit'), '10')
  await consumer.router.push(consumer.context.to('/documents'))
  await tick()
  const back = consumer.context.to('/memory')
  assert.deepEqual(back.query.filter, ['low', 'chain'])
  assert.equal(back.query.q, 'known')
  assert.equal(back.query.page, '3')
  await consumer.router.push(back)
  await tick()
  assert.deepEqual(consumer.requests.at(-1).searchParams.getAll('filter'), ['low', 'chain'])
  const reloaded = await mountConsumer(t, `/documents?project=${projectID}`, { storage: consumer.storage })
  assert.deepEqual(reloaded.context.to('/memory').query.filter, ['low', 'chain'])
  assert.equal(reloaded.context.to('/memory').query.page, '3')
})

test('chips toggle independently and an empty selection sends no filter parameter', async (t) => {
  const consumer = await mountConsumer(t, `/memory?project=${projectID}&q=known&page=3&size=10`)
  assert.deepEqual(consumer.requests.at(-1).searchParams.getAll('filter'), [])
  for (const value of ['low', 'chain']) {
    const button = consumer.chip(value)
    assert.ok(button, `${value} chip must be available to the operator`)
    await button.props.onClick()
    await tick()
  }
  assert.deepEqual(consumer.requests.at(-1).searchParams.getAll('filter'), ['low', 'chain'])
  assert.equal(consumer.router.currentRoute.value.query.page, '1')
  assert.equal(consumer.router.currentRoute.value.query.q, 'known')
  assert.equal(consumer.chip('low').props['aria-pressed'], true)
  assert.equal(consumer.chip('chain').props['aria-pressed'], true)
  await consumer.chip('low').props.onClick()
  await tick()
  assert.deepEqual(consumer.requests.at(-1).searchParams.getAll('filter'), ['chain'])
  await consumer.chip('chain').props.onClick()
  await tick()
  assert.deepEqual(consumer.requests.at(-1).searchParams.getAll('filter'), [])
  assert.equal(consumer.requests.at(-1).searchParams.get('project'), projectID)
})

for (const suffix of ['filter=low&filter=unknown', 'filter=']) {
  test(`invalid route input stays explicit and a server refusal is not empty knowledge: ${suffix}`, async (t) => {
    const consumer = await mountConsumer(t, `/memory?project=${projectID}&${suffix}`, { rejectMemory: true })
    const expected = suffix === 'filter=' ? [''] : ['low', 'unknown']
    assert.deepEqual(consumer.requests.at(-1).searchParams.getAll('filter'), expected)
    assert.equal(consumer.reader().loadState.value.kind, 'error')
    assert.equal(consumer.reader().loadState.value.error.status, 400)
    assert.equal(consumer.reader().total.value, null)
  })
}

test('pending and failed legacy resolution never dispatch a raw or broader memory scope', async (t) => {
  let release
  const catalogReady = new Promise((resolve) => { release = resolve })
  const consumer = await mountConsumer(t, '/memory?project=legacy-project&filter=low', { catalogReady, resolveLegacy: true })
  assert.equal(consumer.context.project.value, '')
  assert.equal(consumer.requests.length, 0)
  release()
  await tick()
  await tick()
  assert.equal(consumer.router.currentRoute.value.query.project, projectID)
  assert.ok(consumer.requests.length > 0)
  assert.ok(consumer.requests.every((request) => request.searchParams.get('project') === projectID))
  const failed = await mountConsumer(t, '/memory?project=unknown-project')
  assert.equal(failed.requests.length, 0)
  assert.equal(failed.context.project.value, '')
  assert.equal(failed.context.selectionError.value.status, 503)
  assert.ok(walk(failed.host).some((node) => node.props['data-testid'] === 'memory-project-error'))
})

test('catalog rollback clears and recomputes the explicit all-memory total', async t => {
  const alias = '22222222-2222-4222-8222-222222222222'
  const items = [{ project_id: projectID, name: 'Primary' }, { project_id: alias, name: 'Alias' }]
  let releaseCatalog, catalogCalls = 0
  const nextCatalog = new Promise(resolve => { releaseCatalog = resolve })
  const catalogFetch = () => ++catalogCalls === 1 ? Response.json({ items, unresolved: [], logical_groups: [{ primary_project_id: projectID, alias_project_ids: [alias] }] }) : nextCatalog
  const memoryFetch = url => {
    const project = url.searchParams.get('project')
    return Response.json({ items: [{ id: project === projectID ? 1 : 2, project, content: 'synthetic memory' }], total: 1, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
  }
  const consumer = await mountConsumer(t, '/memory?project=all&allRead=1', { catalogFetch, memoryFetch })
  assert.equal(consumer.reader().total.value, 1)
  const refresh = consumer.context.refresh()
  assert.equal(consumer.context.project.value, '')
  assert.equal(consumer.reader().total.value, null)
  assert.equal(consumer.reader().rows.length, 0)
  releaseCatalog(Response.json({ items, unresolved: [], logical_groups: [] }))
  await refresh; await tick(); await tick()
  assert.equal(consumer.context.project.value, 'all')
  assert.equal(consumer.reader().total.value, 2)
  assert.equal(consumer.reader().rows.length, 2)
})

test('all-memory catalog error retries the catalog and recovers', async t => {
  let catalogCalls = 0
  const items = [{ project_id: projectID, name: 'Synthetic project' }]
  const catalogFetch = () => Response.json({ items, unresolved: [], logical_groups: ++catalogCalls === 1 ? null : [] })
  const consumer = await mountConsumer(t, '/memory?project=all&allRead=1', { catalogFetch })
  assert.equal(consumer.reader().loadState.value.kind, 'error')
  const retry = walk(consumer.host).find(node => node.props['data-testid'] === 'retry-project-catalog')
  assert.ok(retry)
  await retry.props.onClick(); await tick(); await tick()
  assert.equal(catalogCalls, 2)
  assert.equal(consumer.reader().loadState.value.kind, 'empty')
  assert.equal(consumer.reader().total.value, 0)
})
