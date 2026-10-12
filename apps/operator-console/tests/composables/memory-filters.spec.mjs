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

function hostNode(tag = '', text = '') { return { tag, text, props: {}, children: [], parent: null } }
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

async function mountConsumer(t, location, { storage = new Map(), rejectMemory = false, catalogReady = Promise.resolve(), resolveLegacy = false } = {}) {
  const requests = []
  let reader
  const fetch = async (input) => {
    const url = new URL(input, 'http://synthetic.invalid')
    if (url.pathname === '/api/projects') { await catalogReady; return Response.json({ items: [{ project_id: projectID, name: 'Synthetic project' }], unresolved: [] }) }
    if (url.pathname === '/api/projects/resolve') return resolveLegacy ? Response.json({ status: 'resolved', project_id: projectID, name: 'Synthetic project' }) : Response.json({ error: 'Mapping unavailable' }, { status: 503 })
    if (url.pathname === '/api/memories') {
      requests.push(url)
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
  const router = routing.createRouter({
    history: routing.createMemoryHistory(), routes: [
      { path: '/memory', component: page }, { path: '/documents', component: { render: () => null } },
      { path: '/search', component: { render: () => null } },
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
