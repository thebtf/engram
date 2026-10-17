import assert from 'node:assert/strict'
import { createHash, randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import * as routing from 'vue-router'
import { compileScript, parse } from '@vue/compiler-sfc'

const root = new URL('../../', import.meta.url)
const A = '11111111-1111-4111-8111-111111111111'
const B = '22222222-2222-4222-8222-222222222222'
const tick = async () => { await new Promise(setImmediate); await vue.nextTick() }
const clone = (value) => structuredClone(vue.toRaw(value))
const fingerprint = (scope) => `sha256:${createHash('sha256').update(`rules-collection-filter/v1\0priority:desc\0created_at:desc\0id:desc\0scope:${scope}`).digest('hex')}`

// Same transpile/renderer seam as document-export and auth-session; no production rewrite.
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
const renderer = vue.createRenderer({
  createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
  setText() { }, setElementText() { }, patchProp() { }, parentNode: () => null, nextSibling: () => null, insert() { }, remove() { },
})

async function mount(t, enabledA = true) {
  const calls = [], writes = [], records = new Map(), projections = new Map(), pages = new Map(), storage = new Map()
  let user = 'synthetic-owner', session = 'synthetic-session'
  const rows = new Map([[401, A], [402, A], [403, B], [404, null]].map(([id, project]) => [id, {
    id, project, content: `Synthetic rule ${id}`, priority: 500 - id, version: 1,
    enabled: id === 401 ? enabledA : true, created_at: '2026-10-01T12:00:00Z', updated_at: '2026-10-01T12:00:00Z',
  }]))
  const scopeKey = (domain) => JSON.stringify([user, session, domain])
  const current = (domain) => records.get(scopeKey(domain)) || { domain, kind: 'none', selection_version: 0 }
  const members = (scope) => [...rows.values()].filter((row) => scope === 'all' || row.project === null || row.project === scope)
  const targets = (values) => values.map((row) => ({ id: String(row.id), expected_version: row.version }))
  const response = (body, status = 200) => Response.json(body, { status })
  const fetch = async (path, init = {}) => {
    const url = new URL(path, 'http://synthetic.invalid')
    const body = init.body ? JSON.parse(init.body) : undefined
    const call = { path: url.pathname, query: Object.fromEntries(url.searchParams), method: init.method || 'GET', body, user, session, credentials: init.credentials }
    calls.push(call)
    if (url.pathname === '/api/projects') return response({ items: [A, B].map(project_id => ({ project_id, name: project_id === A ? 'A' : 'B' })), unresolved: [] })
    if (url.pathname === '/api/rules' && call.method === 'GET') return response(members(url.searchParams.get('all') === 'true' ? 'all' : url.searchParams.get('project') || 'global'))
    if (url.pathname === '/api/collections/selection/current') {
      if (!body.filter?.scope) return response({}, 400)
      const saved = current(body.domain)
      if (saved.kind !== 'none' && projections.get(scopeKey(body.domain)) !== body.filter.scope && !saved.reconfirmation_required) {
        saved.reconfirmation_required = true
        saved.reconfirmation_reason = 'context_changed'
        saved.selection_version += 1
      }
      return response({ selection: clone(saved) })
    }
    if (url.pathname === '/api/collections/selection/page') {
      const scope = body.filter.scope
      const all = members(scope)
      const offset = body.cursor ? pages.get(body.cursor)?.offset : 0
      if (offset === undefined || (body.cursor && pages.get(body.cursor).scope !== scope)) return response({}, 400)
      // One-row pages make off-page membership observable without a large fixture.
      const cursor = body.cursor || randomUUID()
      pages.set(cursor, { scope, offset, targets: targets(all.slice(offset, offset + 1)) })
      let next = ''
      if (offset + 1 < all.length) { next = randomUUID(); pages.set(next, { scope, offset: offset + 1 }) }
      return response({ cursor, next_cursor: next, filter_fingerprint: fingerprint(scope), targets: pages.get(cursor).targets, total: all.length })
    }
    if (url.pathname === '/api/collections/selection') {
      if (!body.filter?.scope) return response({}, 400)
      const intent = body.selection
      const prior = current(body.domain)
      const saved = { domain: body.domain, kind: intent.kind, selection_version: prior.selection_version + 1, reconfirmation_required: false }
      if (intent.kind === 'explicit') saved.targets = clone(intent.targets)
      else if (intent.kind === 'page') {
        const page = pages.get(intent.cursor)
        if (!page?.targets || page.scope !== body.filter.scope) return response({}, 412)
        saved.cursor = intent.cursor
        saved.targets = clone(page.targets)
      } else if (intent.kind === 'frozen_filter') {
        if (intent.filter.scope !== body.filter.scope) return response({}, 400)
        saved.targets = targets(members(intent.filter.scope))
        saved.filter_fingerprint = fingerprint(intent.filter.scope)
        saved.excluded_ids = clone(intent.excluded_ids)
        saved.target_count = saved.targets.length - saved.excluded_ids.length
        saved.selection_token = randomUUID()
        saved.expires_at = '2099-01-01T00:00:00Z'
      } else assert.equal(intent.kind, 'none', 'fixture accepts only real selection kinds')
      records.set(scopeKey(body.domain), saved)
      projections.set(scopeKey(body.domain), body.filter.scope)
      return response({ selection: clone(saved) })
    }
    if (url.pathname === '/api/rules' && call.method === 'POST') {
      // Stateful transport, not an action stub: only this owner's saved snapshot can write.
      const saved = current('rules'), requested = body.selection
      if (!body.filter?.scope) return response({}, 400)
      if (saved.kind !== 'none' && projections.get(scopeKey('rules')) !== body.filter.scope) return response({}, 412)
      if (saved.kind === 'none' || saved.kind !== requested.kind || saved.selection_version !== requested.selection_version
        || saved.reconfirmation_required || (saved.kind === 'frozen_filter' && saved.selection_token !== requested.selection_token)) return response({}, 403)
      const effective = saved.targets.filter((target) => !(saved.excluded_ids || []).includes(target.id))
      if (effective.some((target) => rows.get(Number(target.id))?.version !== target.expected_version)) return response({}, 409)
      assert.ok(['enable', 'disable', 'delete', 'update'].includes(body.action))
      const changed = []
      for (const target of effective) {
        const id = Number(target.id), row = rows.get(id)
        writes.push({ id, action: body.action, project: row.project, user, session })
        if (body.action === 'delete') rows.delete(id)
        else {
          if (body.action === 'update') row.content = body.content
          else row.enabled = body.action === 'enable'
          row.version += 1
          changed.push(clone(row))
        }
      }
      const readback = body.action === 'delete' ? { authoritative: true, kind: 'authorized_absence' }
        : { authoritative: true, kind: 'current', current_state: changed.length === 1 ? changed[0] : changed }
      return response({
        request_id: body.request_id, operation_state: 'completed', readback,
        item_results: effective.map((target) => ({
          target_id: Number(target.id), outcome: 'committed', readback: body.action === 'delete'
            ? { authoritative: true, kind: 'authorized_absence' }
            : { authoritative: true, kind: 'current', current_state: clone(rows.get(Number(target.id))) }
        })),
      })
    }
    throw new Error(`Unexpected credential-free Rules fixture request: ${call.method} ${path}`)
  }
  const startedLoads = new Set(), modules = new Map()
  const globals = {
    fetch, window: {}, importMeta: { env: { DEV: false } },
    sessionStorage: { getItem: (key) => storage.get(key) || null, setItem: (key, value) => storage.set(key, value) },
    useI18n: () => ({ t: (key) => key }), computed: vue.computed, ref: vue.ref,
    crypto: { randomUUID },
  }
  function dependency(name) {
    if (name === 'vue') return vue
    if (name === 'vue-router') return routing
    if (name.endsWith('operator-config')) return { operatorConfig: { apiBase: '/api' } }
    if (name.endsWith('useConsolePreferences')) return { startedLoads }
    const file = name.split('/').at(-1)
    if (!modules.has(file)) modules.set(file, evaluate(readFileSync(new URL(`composables/${file}.ts`, root), 'utf8'), dependency, globals))
    return modules.get(file)
  }
  const descriptor = parse(readFileSync(new URL('pages/rules.vue', root), 'utf8')).descriptor
  const page = evaluate(compileScript(descriptor, { id: 'rules-selection-context-consumer' }).content, dependency, globals).default
  page.render = () => null
  const router = routing.createRouter({
    history: routing.createMemoryHistory(), routes: [
      { path: '/projects', component: { render: () => null } }, { path: '/rules', component: page },
    ]
  })
  await router.push('/projects')
  let context, instance
  const app = renderer.createApp({
    setup() {
      context = dependency('./useOperatorProjectContext').useOperatorProjectContext()
      return () => vue.h(routing.RouterView, null, { default: ({ Component }) => Component ? vue.h(Component, { ref: (value) => { instance = value } }) : null })
    }
  })
  app.use(router).mount({})
  t.after(() => app.unmount())
  await tick()
  assert.deepEqual([...context.projects.value], [A, B], 'catalog must be cached before mounting Rules')
  async function enter(project) {
    await context.select(project, '/projects')
    await tick()
    await router.push(context.to('/rules'))
    await tick()
    return instance.$.setupState
  }
  await enter(A)
  const state = () => instance.$.setupState
  await state().refresh(A)
  assert.equal(state().busy, false, 'fixture prerequisites must finish before selecting')
  return {
    state, context, router, enter, calls, writes, rows,
    selectionModel: () => dependency('./useOperatorSelection'),
    identity: (nextUser, nextSession) => { user = nextUser; session = nextSession },
  }
}

async function select(consumer, kind) {
  const state = consumer.state()
  if (kind === 'explicit') await state.toggleRuleSelection(state.visibleRows.find((row) => row.id === 401))
  else if (kind === 'page') { await state.loadSelectionPage(); await state.selectCurrentPage() }
  else await state.freezeCurrentFilter()
  assert.equal(state.selectionError, '', 'selection setup must succeed, not masquerade as RED')
  assert.equal(state.selection.current.value.kind, kind)
  assert.ok(state.selectedCount > 0)
}

for (const action of ['enable', 'disable', 'delete']) {
  test(`restored A selection must not ${action} A after cached-catalog navigation, B remount and Refresh`, async (t) => {
    const consumer = await mount(t, action !== 'enable')
    await select(consumer, 'explicit')
    const beforeA = clone(consumer.rows.get(401)), beforeB = clone(consumer.rows.get(403))
    const savedVersion = consumer.state().selection.current.value.version
    const catalogReads = consumer.calls.filter((call) => call.path === '/api/projects').length
    const oldInstance = consumer.state()
    await consumer.enter(B)
    const state = consumer.state()
    assert.notEqual(state, oldInstance, 'ordinary navigation must remount Rules')
    assert.equal(state.scopeFilter, B)
    assert.equal(consumer.calls.filter((call) => call.path === '/api/projects').length, catalogReads, 'remount must use the existing catalog')
    await state.refresh(state.scopeFilter)
    assert.deepEqual(state.visibleRows.map((row) => row.id), [403], 'Refresh must visibly load B, not A')
    assert.equal(state.busy, false)
    const operationsBefore = consumer.writes.length
    await state.runSelectionAction(action)
    t.diagnostic(JSON.stringify({ action, savedVersion, visibleProject: B, selectedKind: state.selection.current.value.kind, mutation: state.mutationResult?.kind, writes: consumer.writes.slice(operationsBefore) }))
    assert.deepEqual(consumer.rows.get(401), beforeA, `bulk ${action} under visible B must not mutate restored A without a deliberate new selection/scope`)
    assert.deepEqual(consumer.rows.get(403), beforeB, 'a refused stale action must not silently retarget B either')
  })
}

for (const kind of ['explicit', 'page', 'frozen_filter']) {
  test(`same-context restored ${kind} remains usable across query/page navigation and off-page display`, async (t) => {
    const consumer = await mount(t)
    await select(consumer, kind)
    const before = clone(consumer.state().selection.current.value)
    await consumer.enter(A)
    const state = consumer.state()
    await state.refresh(A)
    await consumer.router.replace({ path: '/rules', query: { project: A, q: 'presentation-only', page: '2', size: '1' } })
    await tick()
    assert.deepEqual(state.selection.current.value, before, 'query/page navigation is not a new Rules scope')
    assert.equal(state.selectionActionReady, true, 'do not solve cross-scope restore by banning every restore')
    if (kind === 'frozen_filter') {
      await state.loadSelectionPage()
      assert.equal(state.visibleRows.length, 1)
      assert.equal(state.selectedCount, 3, 'frozen scope retains off-page and applicable global membership')
    }
    assert.equal(await state.runSelectionAction('disable'), true)
    assert.equal(state.mutationResult.kind, 'committed_verified')
    assert.deepEqual(consumer.writes.map((write) => write.id), kind === 'frozen_filter' ? [401, 402, 404] : [401])
    assert.equal(consumer.rows.get(403).version, 1)
  })
}

test('deliberately selecting all scopes preserves a cross-project restored snapshot', async (t) => {
  const consumer = await mount(t)
  const state = consumer.state()
  state.scopeFilter = 'all'
  await tick()
  await state.freezeCurrentFilter()
  assert.equal(state.selectedCount, 4)
  const snapshot = clone(state.selection.current.value)
  await consumer.enter('all')
  await consumer.state().refresh('all')
  assert.deepEqual(consumer.state().selection.current.value, snapshot)
  assert.equal(await consumer.state().runSelectionAction('disable'), true)
  assert.deepEqual(consumer.writes.map((write) => write.id), [401, 402, 403, 404])
})

for (const action of ['toggle', 'update', 'delete']) {
  test(`per-row ${action} deliberately replaces restored A with visible B`, async (t) => {
    const consumer = await mount(t)
    await select(consumer, 'explicit')
    await consumer.enter(B)
    const state = consumer.state()
    await state.refresh(B)
    const rule = state.visibleRows[0], beforeA = clone(consumer.rows.get(401))
    if (action === 'toggle') await state.toggleRule(rule)
    else if (action === 'update') { state.startEdit(rule); state.editContent = 'Deliberate B edit'; await state.saveEdit(rule) }
    else { await state.confirmDelete(rule); assert.equal(consumer.writes.length, 0); await state.confirmDelete(rule) }
    assert.equal(state.mutationResult.kind, 'committed_verified')
    assert.deepEqual(consumer.writes.map((write) => write.id), [403])
    assert.deepEqual(consumer.rows.get(401), beforeA)
    if (action === 'delete') assert.equal(consumer.rows.has(403), false)
    else assert.equal(consumer.rows.get(403).version, 2)
  })
}

for (const kind of ['explicit', 'page', 'frozen_filter']) {
  test(`live scope change invalidates ${kind} until deliberate new scope selection`, async (t) => {
    const consumer = await mount(t)
    await select(consumer, kind)
    const state = consumer.state()
    state.scopeFilter = B
    await tick()
    assert.equal(state.selectionActionReady, false)
    if (kind === 'frozen_filter') assert.equal(state.selection.current.value.reconfirmationRequired, true)
    else assert.equal(state.selection.current.value.kind, 'none')
    await state.runSelectionAction('delete')
    assert.deepEqual(consumer.writes, [])
    await state.clearSelection()
    await state.toggleRuleSelection(state.visibleRows[0])
    assert.equal(state.selectionActionReady, true)
    assert.equal(await state.runSelectionAction('disable'), true)
    assert.deepEqual(consumer.writes.map((write) => write.id), [403])
  })
}

for (const changed of ['owner', 'session']) {
  test(`stateful transport does not restore or execute another ${changed}'s snapshot`, async (t) => {
    const consumer = await mount(t)
    await select(consumer, 'explicit')
    const saved = clone(consumer.state().selection.current.value), before = clone(consumer.rows.get(401))
    consumer.identity(changed === 'owner' ? 'other-owner' : 'synthetic-owner', changed === 'session' ? 'other-session' : 'synthetic-session')
    await consumer.enter(A)
    const state = consumer.state()
    assert.equal(state.selection.current.value.kind, 'none')
    assert.equal(state.selectionActionReady, false)
    // Exercise the actual model's operation path against the scoped transport too.
    const result = await state.runRuleSelectionOperation({ action: 'disable', selection: saved, filter: { scope: A } })
    assert.notEqual(result.mutation.kind, 'committed_verified')
    assert.deepEqual(consumer.writes, [])
    assert.deepEqual(consumer.rows.get(401), before)
    assert.ok(consumer.calls.filter((call) => call.path.startsWith('/api/collections/') || call.method === 'POST').every((call) => call.credentials === 'include'))
  })
}

test('actual selection parser/model preserves versions and rejects a foreign domain', async (t) => {
  const consumer = await mount(t)
  const { createOperatorSelection, parseOperatorSelectionSnapshot } = consumer.selectionModel()
  const selection = createOperatorSelection('rules')
  const snapshot = parseOperatorSelectionSnapshot({ domain: 'rules', kind: 'explicit', selection_version: 7, targets: [{ id: '401', expected_version: 1 }] })
  selection.applySnapshot(snapshot)
  assert.deepEqual(selection.current.value, { domain: 'rules', kind: 'explicit', version: 7, targets: [{ id: '401', expectedVersion: 1 }] })
  assert.throws(() => selection.applySnapshot({ ...snapshot, domain: 'issues' }), /another domain/)
  assert.throws(() => parseOperatorSelectionSnapshot({ domain: 'rules', kind: 'explicit', targets: [{ id: '401', expected_version: 0 }] }), /version/)
})

for (const kind of ['explicit', 'page', 'frozen_filter']) {
  test(`direct model execution cannot replay ${kind} from A under B without relying on UI restore`, async (t) => {
    const consumer = await mount(t)
    await select(consumer, kind)
    const state = consumer.state(), saved = clone(state.selection.current.value)
    const before = clone(consumer.rows.get(401))
    const result = await state.runRuleSelectionOperation({ action: 'delete', selection: saved, filter: { scope: B } })
    assert.notEqual(result.mutation.kind, 'committed_verified')
    assert.deepEqual(consumer.writes, [])
    assert.deepEqual(consumer.rows.get(401), before)
  })
}
