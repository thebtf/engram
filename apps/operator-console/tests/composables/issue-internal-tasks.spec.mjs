import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import * as routing from 'vue-router'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'

const root = new URL('../../', import.meta.url)
const sourceProject = '11111111-1111-4111-8111-111111111111'
const targetProject = '22222222-2222-4222-8222-222222222222'
const timestamp = '2026-10-01T12:00:00Z'
const catalog = { items: [{ project_id: sourceProject, name: 'Synthetic source' }, { project_id: targetProject, name: 'Synthetic target' }], unresolved: [] }
const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
const tick = async () => { await new Promise(setImmediate); await vue.nextTick() }
const copy = value => JSON.parse(JSON.stringify(value))

function issue(overrides = {}) {
  return {
    id: 7, revision: '7', title: 'Synthetic retained task', body: 'Synthetic original body',
    status: 'open', priority: 'medium', type: 'task', labels: ['internal'], metadata: {},
    source_project: sourceProject, target_project: targetProject,
    source_project_id: sourceProject, target_project_id: targetProject,
    project_origin: { source_project: sourceProject, target_project: targetProject },
    created_at: timestamp, updated_at: timestamp, ...overrides,
  }
}
function comment(id, body = `Synthetic comment ${id}`) {
  return { id, issue_id: 7, body, metadata: {}, author_project: sourceProject, author_agent: 'human', created_at: timestamp }
}
function history(revision) {
  return {
    revision: String(revision), previous_revision: revision === 1 ? null : String(revision - 1),
    action: revision === 1 ? 'create' : 'update', changed_fields: ['priority'],
    actor: { kind: 'operator', reference: 'auth-disabled', label: `Synthetic operator ${revision}` },
    attribution: { source_project: sourceProject, source_agent: 'human', session: null },
    created_at: timestamp, changes: { priority: { before: 'medium', after: 'high' } },
  }
}
function detail(row = issue(), overrides = {}) {
  return {
    issue: row, comments: [], history: [], comment_count: 0, history_count: 0,
    comments_next_offset: null, history_next_offset: null, comments_complete: true, history_complete: true,
    comments_offset: 0, comments_limit: 50, history_offset: 0, history_limit: 50,
    comments_oversized: false, history_oversized: false,
    ...overrides,
  }
}
const createInput = overrides => ({
  title: 'Synthetic retained task', body: 'Synthetic original body', priority: 'medium', type: 'task',
  sourceProject, targetProject, labels: ['internal'], ...overrides,
})

function evaluate(source, dependency, globals) {
  const module = { exports: {} }
  const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(dependency, module, module.exports, ...Object.values(globals))
  return module.exports
}
function node(tag = '', text = '') {
  return { tag, text, props: {}, children: [], parent: null, scrollIntoView() { } }
}
const renderer = vue.createRenderer({
  createElement: tag => node(tag), createText: text => node('', text), createComment: () => node(),
  patchProp: (item, key, _previous, value) => { item.props[key] = value },
  setText: (item, value) => { item.text = value },
  setElementText: (item, value) => { item.children = [node('', value)] },
  parentNode: item => item.parent,
  nextSibling: item => item.parent?.children[item.parent.children.indexOf(item) + 1] || null,
  insert(item, parent, anchor) {
    if (item.parent) item.parent.children = item.parent.children.filter(child => child !== item)
    item.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    if (index < 0) parent.children.push(item)
    else parent.children.splice(index, 0, item)
  },
  remove(item) { if (item.parent) item.parent.children = item.parent.children.filter(child => child !== item) },
})
const walk = item => [item, ...item.children.flatMap(walk)]
const text = item => `${item.text} ${item.props.innerHTML || ''} ${item.children.map(text).join(' ')}`.replace(/\s+/g, ' ').trim()
const writes = state => state.calls.filter(call => call.method !== 'GET')
const creates = state => state.calls.filter(call => call.method === 'POST' && call.url.pathname === '/api/issues')
const patches = state => state.calls.filter(call => call.method === 'PATCH')
const detailReads = state => state.calls.filter(call => call.method === 'GET' && call.url.pathname === '/api/issues/7')

async function settle(state, predicate, message) {
  for (let turn = 0; turn < 40; turn++) {
    assert.deepEqual(state.errors, [], 'Real Vue consumer must not fail during fixture execution')
    if (predicate()) return
    await tick()
  }
  assert.ok(predicate(), message)
}

const messages = JSON.parse(readFileSync(new URL('i18n/locales/en.json', root), 'utf8'))
function translate(key, ...args) {
  const value = key.split('.').reduce((current, part) => current?.[part], messages)
  const params = args.find(arg => arg && typeof arg === 'object') || {}
  const count = args.find(arg => typeof arg === 'number')
  const alternatives = String(value ?? key).split(' | ')
  const selected = alternatives[count === 1 ? 0 : alternatives.length - 1]
  return selected.replace(/\{(\w+)\}/g, (match, name) => String(params[name] ?? (name === 'count' ? count : undefined) ?? match))
}
const compiled = new Map()
function componentSource(path) {
  if (!compiled.has(path)) {
    const filename = fileURLToPath(new URL(path, root))
    const parsed = parse(readFileSync(filename, 'utf8'), { filename })
    assert.deepEqual(parsed.errors, [])
    const script = compileScript(parsed.descriptor, { id: path })
    const template = compileTemplate({
      source: parsed.descriptor.template.content, filename, id: path, isTS: true,
      compilerOptions: { bindingMetadata: script.bindings },
    })
    assert.deepEqual(template.errors, [])
    compiled.set(path, { script: script.content, template: template.code })
  }
  return compiled.get(path)
}

async function mount(t, { page = false, route = '/issues', payload = detail(), respond, tabStorage = new Map(), auth = { authenticated: true, auth_disabled: true, role: 'admin', source: 'auth-disabled', synthetic: true, actor: { kind: 'operator', reference: 'operator:auth-disabled' } }, storageUnavailable = false, listOptions } = {}) {
  const calls = [], errors = [], modules = new Map(), storage = new Map()
  const textModelValues = new WeakMap()
  let context, issues, instance
  let responder = respond
  let currentDetail = payload
  const globals = {
    window: { localStorage: { getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value) } },
    sessionStorage: { getItem: key => { if (storageUnavailable) throw new Error('Synthetic storage unavailable'); return tabStorage.get(key) ?? null }, setItem: (key, value) => { if (storageUnavailable) throw new Error('Synthetic storage unavailable'); tabStorage.set(key, value) } },
    useI18n: () => ({ t: translate, locale: vue.ref('en') }),
    useRoute: routing.useRoute, useRouter: routing.useRouter,
    fetch: async (path, init = {}) => {
      const url = new URL(path, 'http://synthetic.invalid')
      const call = { url, method: init.method || 'GET', rawBody: init.body, body: init.body ? JSON.parse(init.body) : undefined, credentials: init.credentials }
      calls.push(call)
      if (responder) {
        const response = await responder(call)
        if (response !== undefined) return response
      }
      if (url.pathname === '/api/auth/me') return json(auth)
      if (url.pathname === '/api/projects') return json(catalog)
      if (url.pathname === '/api/issues' && call.method === 'GET') return json({ issues: [currentDetail.issue], total: 1 })
      if (url.pathname === '/api/issues/7' && call.method === 'GET') return json(currentDetail)
      if (url.pathname === '/api/issues' && call.method === 'POST') return json({ id: 7, revision: currentDetail.issue.revision, replayed: false, message: 'issue created' }, 201)
      if (url.pathname === '/api/issues/7' && call.method === 'PATCH') return json({ id: 7, revision: currentDetail.issue.revision, changed: false, comment_id: null, message: 'issue updated' })
      throw new Error(`Unexpected synthetic request: ${call.method} ${url.pathname}`)
    },
  }
  function load(name) {
    if (!modules.has(name)) modules.set(name, evaluate(readFileSync(new URL(`composables/${name}.ts`, root), 'utf8'), dependency, globals))
    return modules.get(name)
  }
  function dependency(name) {
    // Native DOM v-model is a browser concern. Compiled update handlers and page state remain real.
    if (name === 'vue') return { ...vue, vModelSelect: {}, vModelText: { created: (item, binding) => textModelValues.set(item, binding.value), beforeUpdate: (item, binding) => textModelValues.set(item, binding.value) }, vModelCheckbox: {} }
    if (name === 'vue-router') return routing
    if (name.endsWith('operator-config')) return { operatorConfig: { apiBase: '/api' } }
    if (!name.startsWith('.')) throw new Error(`Unexpected fixture dependency: ${name}`)
    const real = load(name.split('/').at(-1))
    if (name.endsWith('useOperatorProjectContext')) return { ...real, useOperatorProjectContext: (...args) => { context = real.useOperatorProjectContext(...args); return context } }
    if (name.endsWith('useOperatorIssues')) return { ...real, useOperatorIssues: (...args) => { issues = real.useOperatorIssues(...args); return issues } }
    return real
  }
  function compile(path) {
    const source = componentSource(path)
    const component = evaluate(source.script, dependency, globals).default
    component.render = evaluate(source.template, dependency, globals).render
    return component
  }
  const component = page ? compile('pages/issues.vue') : {
    setup() {
      context = load('useOperatorProjectContext').useOperatorProjectContext()
      issues = load('useOperatorIssues').useOperatorIssues(listOptions)
      return () => null
    }
  }
  const router = routing.createRouter({ history: routing.createMemoryHistory(), routes: [{ path: '/issues/:id?', component: { render: () => null } }] })
  await router.push({ path: route, query: { project: sourceProject } })
  const app = renderer.createApp({ setup: () => () => vue.h(component, { ref: value => { if (value) instance = value } }) })
  app.config.errorHandler = error => errors.push(error.message)
  app.config.warnHandler = warning => errors.push(warning)
  if (page) app.component('MutationResultNotice', compile('components/MutationResultNotice.vue'))
  const host = node('root')
  app.use(router).mount(host)
  let mounted = true
  const unmount = () => { if (mounted) { app.unmount(); mounted = false } }
  t.after(unmount)
  const state = {
    tabStorage,
    unmount,
    host, router, calls, errors,
    get context() { return context }, get issues() { return issues }, get page() { return instance?.$.setupState },
    get api() { return load('useOperatorApi') },
    inputValue(name) { return textModelValues.get(namedInput(state, name)) },
    setResponder(next) { responder = next }, setDetail(next) { currentDetail = next },
  }
  await settle(state, () => issues && ['live', 'empty'].includes(issues.loadState.value.kind), 'Real project catalog and issue list must settle')
  if (route !== '/issues') await settle(state, () => issues.detail.value?.id === 7 && issues.detailState.value.kind === 'live', 'Real detail route must settle')
  return state
}

test('resolved and closed filters request distinct lifecycle states', async t => {
  const state = await mount(t, { page: true })
  for (const status of ['resolved', 'closed']) {
    await click(translatedButton(state, `issues.filters.${status}`))
    await settle(state, () => state.calls.some(call => call.url.pathname === '/api/issues' && call.url.searchParams.get('status') === status), `Request ${status} separately`)
    const requests = state.calls.filter(call => call.url.pathname === '/api/issues' && call.method === 'GET')
    assert.equal(requests.at(-1).url.searchParams.get('status'), status)
  }
  assert.match(translate('issues.status.resolved'), /awaiting verification/i)
  assert.equal(translate('issues.status.closed'), 'closed')
  assert.equal(writes(state).length, 0)
})

for (const [name, total] of [['missing', undefined], ['null', null], ['string', '251'], ['negative', -1], ['fractional', 1.5], ['unsafe', Number.MAX_SAFE_INTEGER + 1], ['less than returned rows', 0]]) test(`issue list rejects ${name} total without presenting a partial count as complete`, async t => {
  const state = await mount(t)
  state.setResponder(call => call.method === 'GET' && call.url.pathname === '/api/issues' ? json({ issues: [issue()], ...(total === undefined ? {} : { total }) }) : undefined)
  await state.issues.refresh()
  assert.equal(state.issues.loadState.value.kind, 'error')
  assert.equal(state.issues.loadState.value.error.category, 'invalid-response')
})

test('an empty later issue page after concurrent shrink keeps its valid total', async t => {
  const listOptions = vue.ref({ limit: 10, offset: 10, role: 'target', statuses: [] })
  const state = await mount(t, { listOptions })
  state.setResponder(call => call.method === 'GET' && call.url.pathname === '/api/issues' ? json({ issues: [], total: 1 }) : undefined)
  await state.issues.refresh()
  assert.equal(state.issues.loadState.value.kind, 'empty')
  assert.equal(state.issues.registryTotal.value, 1)
  const request = state.calls.filter(call => call.method === 'GET' && call.url.pathname === '/api/issues').at(-1)
  assert.equal(request.url.searchParams.get('offset'), '10')
})

function rendered(state, testId) {
  const item = walk(state.host).find(item => item.props['data-testid'] === testId)
  assert.ok(item, `Render the ${testId} consumer control or disclosure`)
  return item
}
function namedInput(state, name) {
  const item = walk(state.host).find(item => ['input', 'textarea', 'select'].includes(item.tag) && item.props.name === name)
  assert.ok(item, `Keep the actual ${name} form control available`)
  return item
}
async function enter(state, name, value, change = false) {
  const input = namedInput(state, name)
  assert.ok(!input.props.disabled, `${name} must accept the user's draft`)
  const update = input.props['onUpdate:modelValue']
  assert.equal(typeof update, 'function', 'Exercise the compiled form binding, not a replacement draft model')
  update(value)
  await tick()
  if (change) {
    const handlers = Array.isArray(input.props.onChange) ? input.props.onChange : [input.props.onChange]
    assert.ok(handlers.every(handler => typeof handler === 'function'))
    for (const handler of handlers) await handler({ target: { value } })
    await tick()
  }
}
function translatedButton(state, key, scope = state.host) {
  const button = walk(scope).find(item => item.tag === 'button' && text(item) === translate(key))
  assert.ok(button, `Keep the existing ${key} action`)
  return button
}
function createLabelControl(state, label) {
  const dialog = walk(state.host).find(item => item.props.role === 'dialog')
  assert.ok(dialog, 'The actual create form must remain visible')
  const button = walk(dialog).find(item => item.tag === 'button' && text(item) === label)
  assert.ok(button, 'Use an actual admitted label option')
  return button
}
async function toggleCreateLabel(state, label) {
  await click(createLabelControl(state, label))
}
async function prepareCreate(state) {
  await click(translatedButton(state, 'issues.create.open'))
  await enter(state, 'new-issue-title', 'Synthetic retained task')
  await enter(state, 'new-issue-body', 'Synthetic original body')
  await enter(state, 'new-issue-priority', 'medium')
  await enter(state, 'new-issue-type', 'task')
  await enter(state, 'new-issue-target-project', targetProject)
  await toggleCreateLabel(state, 'operator-created')
  await toggleCreateLabel(state, 'internal')
  assert.equal(state.page.canCreate, true)
}
async function submitCreate(state) {
  await click(translatedButton(state, 'issues.create.submit'))
}
async function reconcileCreate(state) {
  const actions = availableActions(state)
  const recovery = actions.find(item => /reconcil|resum|recover|свер|продолж|восстанов/i.test(accessibleName(state, item))) ||
    actions.find(item => /retry|try again|repeat|повтор/i.test(accessibleName(state, item)) && /creat|intent|original|task|созда|исход|задач/i.test(accessibleName(state, item))) ||
    actions.find(item => /retry|try again|repeat|повтор/i.test(accessibleName(state, item)) && !/readback|list|список/i.test(accessibleName(state, item))) ||
    actions.find(item => /check|провер/i.test(accessibleName(state, item)))
  assert.ok(recovery, 'Expose a named explicit recovery action for the unresolved create; a permanently blocked form is not resumable')
  await click(recovery)
  await settle(state, () => state.page.createMutationResult?.kind === 'committed_verified', 'The selected recovery action must resolve the original pending create, not merely refresh a list')
}
function loseReply(call) {
  if (call.method === 'POST' && call.url.pathname === '/api/issues') throw new TypeError('Synthetic reply lost after dispatch')
}
function conflict(call) {
  if (call.method === 'PATCH') return json({ error: { code: 'REVISION_CONFLICT', message: 'Read and reconcile the retained draft', current_revision: '8' } }, 409)
}

for (const timing of ['completed before opening', 'completed after opening']) test(`unrelated bulk outcome stays outside create dialog: ${timing}`, async t => {
  let releaseOperation
  const heldOperation = new Promise(resolve => { releaseOperation = resolve })
  const state = await mount(t, { page: true, respond: async call => {
    if (call.url.pathname === '/api/issues/selection') return json({ selection: { domain: 'issues', kind: 'explicit', selection_version: 3, targets: [{ id: '7' }] } })
    if (call.url.pathname === '/api/issues/operations') {
      if (timing === 'completed after opening') await heldOperation
      return json({ request_id: call.body.request_id, operation_state: 'completed', readback: { authoritative: true, kind: 'current', current_state: [{ status: 'acknowledged', priority: 'medium', labels: [], updated_at: timestamp }] } })
    }
  } })
  t.after(() => releaseOperation())
  const checkbox = walk(state.host).find(item => String(item.props.class || '').includes('issue-row-check'))
  await click(checkbox)
  await click(translatedButton(state, 'issues.bulk.status'))
  const bulkDialog = walk(state.host).find(item => item.props.role === 'dialog' && item.props['aria-label'] === translate('issues.bulkField.title'))
  assert.ok(bulkDialog, 'Open the actual bulk action dialog')
  const submitting = click(translatedButton(state, 'issues.bulkField.submit', bulkDialog))
  await settle(state, () => state.calls.some(call => call.url.pathname === '/api/issues/operations'), 'Dispatch the actual bulk operation')
  if (timing === 'completed after opening') {
    await click(translatedButton(state, 'issues.modal.cancel', bulkDialog))
    await click(translatedButton(state, 'issues.create.open'))
    releaseOperation()
  }
  await submitting
  await settle(state, () => state.page.otherMutationResult?.kind === 'committed_verified', 'The real bulk operation must complete with verified readback')
  if (timing === 'completed before opening') await click(translatedButton(state, 'issues.create.open'))
  const createDialog = walk(state.host).find(item => item.props.role === 'dialog' && item.props['aria-label'] === translate('issues.create.modalTitle'))
  assert.ok(createDialog, 'Open the actual create dialog')
  const dialogResults = walk(createDialog).filter(item => item.props['data-testid'] === 'mutation-result')
  await click(translatedButton(state, 'issues.modal.close', createDialog))
  const pageResult = rendered(state, 'mutation-result')
  assert.equal(pageResult.props['data-kind'], 'committed_verified', 'Keep the bulk outcome available after closing the create dialog')
  assert.match(text(pageResult), /issues-status/, 'The page outcome must identify the actual bulk action')
  assert.equal(creates(state).length, 0, 'Opening and closing the create dialog must not dispatch a create')
  assert.equal(dialogResults.length, 0, 'An unrelated bulk outcome must not appear as a create result')
})

test('unknown bulk outcome remains independently checkable alongside an unresolved create', async t => {
  let createAttempts = 0
  const state = await mount(t, { page: true, respond: call => {
    if (call.url.pathname === '/api/issues' && call.method === 'POST') {
      if (++createAttempts === 1) throw new TypeError('Synthetic create reply lost after dispatch')
      return json({ id: 7, revision: '7', replayed: true })
    }
    if (call.url.pathname === '/api/issues/selection') return json({ selection: { domain: 'issues', kind: 'explicit', selection_version: 3, targets: [{ id: '7' }] } })
    if (call.url.pathname === '/api/issues/operations') throw new TypeError('Synthetic bulk reply lost after dispatch')
  } })
  await prepareCreate(state)
  await submitCreate(state)
  const original = copy(creates(state)[0].body)
  const originalReference = text(rendered(state, 'mutation-request-reference'))
  assert.ok(original.request_key && original.expected_actor, 'The original dispatched create carries its key and actor')
  assert.equal(creates(state).length, 1, 'An unknown create is not replayed automatically')
  const createDialog = walk(state.host).find(item => item.props.role === 'dialog' && item.props['aria-label'] === translate('issues.create.modalTitle'))
  await click(translatedButton(state, 'issues.modal.close', createDialog))
  await click(walk(state.host).find(item => String(item.props.class || '').includes('issue-row-check')))
  await click(translatedButton(state, 'issues.bulk.status'))
  const bulkDialog = walk(state.host).find(item => item.props.role === 'dialog' && item.props['aria-label'] === translate('issues.bulkField.title'))
  await click(translatedButton(state, 'issues.bulkField.submit', bulkDialog))
  await click(translatedButton(state, 'issues.modal.cancel', bulkDialog))
  const bulk = state.calls.find(call => call.url.pathname === '/api/issues/operations')
  assert.ok(bulk, 'The actual bulk operation was dispatched')
  assert.equal(creates(state).length, 1, 'The independent bulk operation must not replay the create')
  const notices = walk(state.host).filter(item => item.props['data-testid'] === 'mutation-result')
  const bulkNotice = notices.find(item => text(item).includes(bulk.body.request_id) && text(item).includes('issues-status'))
  assert.ok(bulkNotice, 'Identify the unknown bulk outcome by its own request and action')
  const bulkCheck = walk(bulkNotice).find(item => item.props['data-testid'] === 'mutation-recheck')
  assert.ok(bulkCheck, 'Expose an independent check for the unknown bulk outcome')
  const beforeCheck = writes(state).length
  await click(bulkCheck)
  assert.equal(writes(state).length, beforeCheck, 'Checking the bulk outcome must not dispatch a write')
  assert.equal(creates(state).length, 1, 'Checking the bulk outcome must not replay or replace the retained create')
  const createNotice = walk(state.host).find(item => item.props['data-testid'] === 'mutation-result' && text(item).includes(originalReference))
  assert.ok(createNotice, 'Keep the unknown create outcome independently identifiable by its original request reference')
  const retained = walk(state.host).find(item => item.props.role === 'status' && item.props['aria-label'] === translate('issues.feature012.retainedIntent'))
  assert.ok(retained, 'Keep the original create reconciliation available')
  await click(translatedButton(state, 'issues.feature012.reconcile', retained))
  await settle(state, () => creates(state).length === 2, 'Explicit reconciliation dispatches the retained create')
  assert.deepEqual(creates(state)[1].body, original, 'Reconciliation uses exactly the original actor, key and payload')
})

// These controls intentionally keep existing behavior green; synthetic HTTP does not prove server CAS/replay.
test('control: dispatched create with a lost reply stays unknown and retains the visible form without automatic retry', async t => {
  const state = await mount(t, { page: true, respond: loseReply })
  await prepareCreate(state)
  await submitCreate(state)
  await tick()
  assert.equal(state.page.createMutationResult.kind, 'outcome_unknown')
  assert.equal(state.page.createMutationResult.retry, 'manual')
  assert.equal(state.page.showCreate, true)
  assert.equal(state.page.createTitle, 'Synthetic retained task')
  assert.equal(state.page.createBody, 'Synthetic original body')
  assert.equal(creates(state).length, 1)
  assert.equal(walk(state.host).find(item => item.props['data-testid'] === 'mutation-result')?.props['data-kind'], 'outcome_unknown')
})

test('an unresolved create is explicitly reconciled with its original bounded wire key and payload before a new intent gets a fresh key', async t => {
  const state = await mount(t, { page: true, respond: loseReply })
  await prepareCreate(state)
  await submitCreate(state)
  const first = copy(creates(state)[0].body)
  assert.equal(typeof first.request_key, 'string', 'Generate request_key before the first create dispatch')
  assert.ok(first.request_key.length > 0)
  assert.ok(Buffer.byteLength(first.request_key, 'utf8') <= 512)
  assert.equal(first.request_key.includes('\0'), false)
  assert.equal(creates(state).length, 1, 'No automatic create retry after losing its reply')
  state.setResponder(call => call.method === 'POST' && call.url.pathname === '/api/issues'
    ? json({ id: 7, revision: '7', replayed: true, message: 'issue replayed' })
    : undefined)
  await reconcileCreate(state)
  assert.equal(creates(state).length, 2, 'The recovery action must actually reconcile the keyed create')
  assert.deepEqual(creates(state)[1].body, first, 'Recover the exact original pending key and payload')
  assert.equal(state.page.showCreate, false)
  state.setResponder(undefined)
  await prepareCreate(state)
  await submitCreate(state)
  assert.equal(creates(state).length, 3)
  assert.equal(typeof creates(state)[2].body.request_key, 'string')
  assert.ok(creates(state)[2].body.request_key.length > 0)
  assert.notEqual(creates(state)[2].body.request_key, first.request_key, 'Only a resolved intent permits a deliberately new task key')
})

test('session attribution survives a lost create reply and explicit keyed reconciliation', async t => {
  const state = await mount(t, { page: true, respond: loseReply })
  await prepareCreate(state)
  await enter(state, 'new-issue-source-session', 'source-session-1')
  await enter(state, 'new-issue-target-session', 'target-session-1')
  await submitCreate(state)
  const original = copy(creates(state)[0].body)
  assert.equal(original.created_by_session, 'source-session-1')
  assert.equal(original.target_session, 'target-session-1')
  state.setResponder(call => call.method === 'POST' && call.url.pathname === '/api/issues'
    ? json({ id: 7, revision: '7', replayed: true, message: 'issue replayed' }) : undefined)
  await reconcileCreate(state)
  assert.deepEqual(creates(state)[1].body, original)
})

test('list filters send bounded session and search selectors without broadening bulk selection', async t => {
  const state = await mount(t, { page: true })
  await enter(state, 'issues-query', 'handoff')
  await enter(state, 'issues-owner-filter', 'owner-1')
  await enter(state, 'issues-source-session-filter', 'source-1')
  await enter(state, 'issues-participant-session-filter', 'participant-1')
  await enter(state, 'issues-target-session-filter', 'target-1')
  await settle(state, () => state.calls.some(call => call.method === 'GET' && call.url.searchParams.get('target_session') === 'target-1'), 'Filtered list must be requested')
  const call = state.calls.filter(call => call.method === 'GET' && call.url.pathname === '/api/issues').at(-1)
  assert.equal(call.url.searchParams.get('q'), 'handoff')
  assert.equal(call.url.searchParams.get('owner_label'), 'owner-1')
  assert.equal(call.url.searchParams.get('source_session'), 'source-1')
  assert.equal(call.url.searchParams.get('participant_session'), 'participant-1')
  assert.equal(call.url.searchParams.get('target_session'), 'target-1')
  assert.ok(Number(call.url.searchParams.get('limit')) <= 200)
  assert.ok(translatedButton(state, 'issues.actions.selectAll').props.disabled)
})

test('editing the real label picker and title cannot mutate the original pending create replay', async t => {
  const state = await mount(t, { page: true, respond: loseReply })
  await prepareCreate(state)
  await submitCreate(state)
  const original = copy(creates(state)[0].body)
  const label = createLabelControl(state, 'handoff')
  const title = namedInput(state, 'new-issue-title')
  if (isEnabled(label)) {
    await click(label)
    assert.deepEqual(state.page.createLabels, ['internal', 'handoff'], 'The real picker replaces the editable label array')
  } else assert.deepEqual(state.page.createLabels, ['internal'], 'A blocked pending form must retain its original labels')
  if (isEnabled(title)) {
    await enter(state, 'new-issue-title', 'Another unsaved title')
    assert.equal(state.page.createTitle, 'Another unsaved title')
  } else assert.equal(state.page.createTitle, 'Synthetic retained task')
  assert.equal(creates(state).length, 1, 'Draft edits alone must not submit another create')
  state.setResponder(call => call.method === 'POST' && call.url.pathname === '/api/issues'
    ? json({ id: 7, revision: '7', replayed: true, message: 'issue replayed' })
    : undefined)
  await reconcileCreate(state)
  assert.equal(creates(state).length, 2, 'The retained original intent must remain reachable through explicit recovery')
  assert.deepEqual(creates(state)[1].body, original, 'Observe immutable intent at the real wire boundary, independent of internal request storage')
})

test('editing a form after an unknown create cannot silently replace the unresolved intent with a new keyed task', async t => {
  const state = await mount(t, { page: true, respond: loseReply })
  await prepareCreate(state)
  await submitCreate(state)
  const original = copy(creates(state)[0].body)
  if (isEnabled(namedInput(state, 'new-issue-title'))) await enter(state, 'new-issue-title', 'Different intended task')
  else assert.equal(state.page.createTitle, 'Synthetic retained task', 'A blocked pending form retains the unresolved draft')
  const submit = translatedButton(state, 'issues.create.submit')
  if (isEnabled(submit)) await click(submit)
  for (const post of creates(state).slice(1)) assert.deepEqual(post.body, original, 'Reconcile the existing create before dispatching a different payload')
})

test('control: a matching successful create clears the form only after an actual detail readback', async t => {
  const state = await mount(t, { page: true })
  await prepareCreate(state)
  await submitCreate(state)
  assert.equal(state.page.createMutationResult.kind, 'committed_verified')
  assert.equal(state.page.showCreate, false)
  assert.equal(state.page.createTitle, '')
  assert.equal(creates(state).length, 1)
  assert.equal(detailReads(state).length, 1)
  assert.equal(creates(state)[0].credentials, 'include')
})

test('a deliberately new create after verified reconciliation receives a different wire key', async t => {
  const state = await mount(t, { page: true })
  await prepareCreate(state)
  await submitCreate(state)
  assert.equal(state.page.createMutationResult.kind, 'committed_verified')
  await prepareCreate(state)
  await submitCreate(state)
  const posts = creates(state)
  assert.equal(posts.length, 2)
  assert.equal(typeof posts[0].body.request_key, 'string')
  assert.equal(typeof posts[1].body.request_key, 'string')
  assert.notEqual(posts[0].body.request_key, posts[1].body.request_key, 'A completed intent must not be reused for an explicitly new task')
})

test('admitted unknown metadata and safe numbers survive the real create request unchanged', async t => {
  const metadata = { acceptance: ['resume this card'], outcome: { state: 'pending' }, unknown: { decimal: 0.1, small: 1e-7, safeMax: Number.MAX_SAFE_INTEGER, safeMin: -Number.MAX_SAFE_INTEGER, data: '<synthetic quoted data>' } }
  const state = await mount(t)
  await state.issues.createIssue(createInput({ metadata }))
  assert.deepEqual(creates(state)[0].body.metadata, metadata, 'Metadata must not disappear during DTO mapping')
})

function metadataBytes(bytes) {
  return { note: 'x'.repeat(bytes - Buffer.byteLength(JSON.stringify({ note: '' }), 'utf8')) }
}
function metadataDepth(depth) {
  let value = {}
  for (let index = 1; index < depth; index++) value = { child: value }
  return value
}
for (const [name, metadata] of [
  ['exactly 1 MiB compact JSON', metadataBytes(1024 * 1024)],
  ['exactly 256 nested containers including root', metadataDepth(256)],
]) test(`admitted metadata boundary: ${name} is sent intact`, async t => {
  const state = await mount(t)
  await state.issues.createIssue(createInput({ metadata }))
  assert.ok(JSON.stringify(creates(state)[0].body.metadata) === JSON.stringify(metadata), 'An admitted metadata boundary must not be dropped, truncated or rewritten')
})

for (const [name, metadata, code, status] of [
  ['null', null, 'INVALID_ARGUMENT', 400], ['array root', [], 'INVALID_ARGUMENT', 400], ['scalar root', 'synthetic', 'INVALID_ARGUMENT', 400],
  ['unsafe positive integer', { number: Number.MAX_SAFE_INTEGER + 1 }, 'INVALID_ARGUMENT', 400],
  ['unsafe negative integer', { number: -Number.MAX_SAFE_INTEGER - 1 }, 'INVALID_ARGUMENT', 400],
  ['over 1 MiB compact JSON', metadataBytes(1024 * 1024 + 1), 'PAYLOAD_TOO_LARGE', 413],
  ['257 nested containers', metadataDepth(257), 'PAYLOAD_TOO_LARGE', 413],
]) test(`metadata admission refuses ${name} locally or preserves the unchanged value for a typed server refusal`, async t => {
  const state = await mount(t, {
    respond: call => call.method === 'POST' && call.url.pathname === '/api/issues'
      ? json({ error: { code, message: 'Synthetic metadata admission refusal' } }, status)
      : undefined
  })
  let result, failure
  try { result = await state.issues.createIssue(createInput({ metadata })) } catch (error) { failure = error }
  const posts = creates(state)
  assert.ok(posts.length <= 1, 'Validation must not cause an automatic create retry')
  if (posts.length) {
    assert.equal(Object.hasOwn(posts[0].body, 'metadata'), true, 'Server admission cannot validate metadata silently omitted by the client')
    assert.deepEqual(posts[0].body.metadata, metadata, 'The server must receive the original value without truncation or normalization')
    assert.equal(result?.code, code, 'Preserve the fixed typed refusal; this fixture does not validate or echo request business data')
    assert.notEqual(result?.kind, 'committed_verified')
    assert.notEqual(result?.kind, 'committed_verification_pending')
  } else {
    assert.ok(failure instanceof TypeError || result?.kind === 'validation_error', 'Local refusal must remain an actual validation failure')
  }
})

for (const [name, number] of [['NaN', NaN], ['positive infinity', Infinity], ['negative infinity', -Infinity]]) test(`nonfinite metadata ${name} cannot silently become JSON null`, async t => {
  const state = await mount(t)
  let result, failure
  try { result = await state.issues.createIssue(createInput({ metadata: { number } })) } catch (error) { failure = error }
  assert.equal(creates(state).length, 0, 'These JS values have no unchanged JSON representation; refuse rather than silently sending null or omitting metadata')
  assert.ok(failure instanceof TypeError || result?.kind === 'validation_error', 'Return an actual validation failure')
})

test('read metadata and canonical string revision remain available to the next material edit', async t => {
  const metadata = { acceptance: ['preserve this value'], unknown: { admitted: 1.25 } }
  const state = await mount(t, { payload: detail(issue({ metadata })) })
  await state.issues.openIssue(7)
  assert.equal(state.issues.detail.value.revision, '7')
  assert.deepEqual(state.issues.detail.value.metadata, metadata)
})

for (const revision of ['1', '9223372036854775807']) test(`read revision ${revision} is preserved as a canonical decimal string, never a rounded JS number`, async t => {
  const state = await mount(t, { payload: detail(issue({ revision })) })
  await state.issues.openIssue(7)
  assert.equal(state.issues.detail.value.revision, revision)
})

test('a material edit uses the revision actually read before another writer wins', async t => {
  const state = await mount(t, { respond: conflict })
  await state.issues.openIssue(7)
  state.setDetail(detail(issue({ revision: '8', priority: 'low' })))
  const result = await state.issues.updateIssue(7, { priority: 'high', comment: 'Synthetic losing compound edit' })
  assert.equal(result.kind, 'conflict')
  assert.equal(patches(state)[0].body.expected_revision, '7', 'Submit the observed base, not no revision and not the winner\'s unread revision')
  assert.equal(patches(state).length, 1)
})

test('session recipient and reported writer attribution use the read revision and remain in a conflict draft', async t => {
  const state = await mount(t, { page: true, route: '/issues/7', respond: conflict })
  await enter(state, 'issue-write-session', 'writer-session')
  await enter(state, 'issue-target-session', 'recipient-session')
  await click(translatedButton(state, 'issues.feature012.saveContent'))
  const request = patches(state)[0].body
  assert.equal(request.expected_revision, '7')
  assert.equal(request.target_session, 'recipient-session')
  assert.equal(request.session_id, 'writer-session')
  assert.equal(state.page.losingPatch.targetSession, 'recipient-session')
  assert.equal(state.page.losingPatch.sessionId, 'writer-session')
  assert.equal(patches(state).length, 1)
})

test('metadata replacement distinguishes omission from an explicit empty object in material PATCH', async t => {
  const state = await mount(t, { respond: conflict })
  await state.issues.openIssue(7)
  await state.issues.updateIssue(7, { priority: 'high' })
  await state.issues.updateIssue(7, { metadata: {} })
  assert.equal(Object.hasOwn(patches(state)[0].body, 'metadata'), false)
  assert.deepEqual(patches(state)[1].body.metadata, {}, 'An explicit empty object must replace metadata, not become an omitted patch')
})

test('control: an explicit empty body and label list survive material PATCH with the selected human source', async t => {
  const state = await mount(t, { respond: conflict })
  await state.issues.openIssue(7)
  await state.issues.updateIssue(7, { body: '', labels: [] })
  const body = patches(state)[0].body
  assert.equal(body.body, '')
  assert.deepEqual(body.labels, [])
  assert.equal(body.source_project, sourceProject)
  assert.equal(body.source_agent, 'human')
})

test('nested REVISION_CONFLICT remains a stable visible code rather than a generic 409', async t => {
  const state = await mount(t, { page: true, route: '/issues/7', respond: conflict })
  state.page.priorityDraft = 'high'
  await state.page.updateCurrentField('priority', 'high')
  await tick()
  assert.equal(state.page.otherMutationResult.kind, 'conflict')
  assert.equal(state.page.otherMutationResult.code, 'REVISION_CONFLICT')
  assert.match(text(state.host), /REVISION_CONFLICT/)
})

test('control: conflict does not refresh or automatically resend the actual losing priority and comment drafts', async t => {
  const state = await mount(t, { page: true, route: '/issues/7', respond: conflict })
  const reads = detailReads(state).length
  state.page.priorityDraft = 'high'
  state.page.commentDraft = 'Synthetic unsent losing comment'
  await state.page.updateCurrentField('priority', 'high')
  await tick()
  assert.equal(state.page.priorityDraft, 'high')
  assert.equal(state.page.commentDraft, 'Synthetic unsent losing comment')
  assert.equal(state.page.otherMutationResult.kind, 'conflict')
  assert.equal(patches(state).length, 1)
  assert.equal(detailReads(state).length, reads)
})

test('an explicit fresh read after conflict preserves the visible losing drafts and their original base', async t => {
  const state = await mount(t, {
    page: true, route: '/issues/7', payload: detail(issue({ revision: '41' })),
    respond: call => call.method === 'PATCH'
      ? json({ error: { code: 'REVISION_CONFLICT', message: 'Read and reconcile the retained draft', current_revision: '42' } }, 409)
      : undefined,
  })
  await enter(state, 'issue-comment', 'Synthetic unsent losing comment')
  await enter(state, 'issue-priority', 'high', true)
  const losingPatch = copy(patches(state)[0].body)
  assert.equal(losingPatch.expected_revision, '41')
  assert.equal(losingPatch.priority, 'high')
  const reads = detailReads(state).length
  state.setDetail(detail(issue({ revision: '42', priority: 'low', body: 'Synthetic winner body' })))
  const notice = rendered(state, 'mutation-result')
  const actions = availableActions(state).filter(item => /read|refresh|reload|check|latest|current|reconcil|обнов|прочит|провер|свер/i.test(accessibleName(state, item)))
  const recovery = actions.find(item => walk(notice).includes(item)) || actions[0]
  assert.ok(recovery, 'A revision conflict needs a named explicit fresh-read action, without prescribing its location')
  await click(recovery)
  await settle(state, () => detailReads(state).length > reads && state.page.activeIssue.priority === 'low', 'The explicit recovery must really read the other writer')
  assert.equal(detailReads(state).length, reads + 1)
  assert.equal(state.page.activeIssue.revision, '42')
  assert.equal(state.page.priorityDraft, 'high', 'Fresh detail must not silently rebase or erase the losing field draft')
  assert.equal(state.page.commentDraft, 'Synthetic unsent losing comment', 'Fresh detail must not erase the actual losing comment draft')
  namedInput(state, 'issue-priority')
  namedInput(state, 'issue-comment')
  assert.ok(visibleNodes(state).filter(item => !walk(item).some(child => child.props['data-testid'] === 'mutation-request-reference'))
    .some(item => /\b41\b/.test(visibleText(item))), 'The original base must remain available to the user, independently of the issue ID, request reference, and placement')
  assert.deepEqual(patches(state).map(call => call.body), [losingPatch], 'A newer read does not authorize an automatic retry or rebase')
})

test('detail reads explicitly opt into bounded comment and history paging', async t => {
  const state = await mount(t)
  await state.issues.openIssue(7)
  assert.equal(detailReads(state).at(-1).url.searchParams.get('detail_mode'), 'paged')
  assert.equal(detailReads(state).length, 1, 'Opening one card must not drain every history/comment page')
})

function firstPages() {
  return detail(issue({ revision: '53' }), {
    comments: Array.from({ length: 50 }, (_, index) => comment(501 + index)),
    history: Array.from({ length: 50 }, (_, index) => history(index + 1)),
    comment_count: 55, history_count: 53,
    comments_next_offset: 50, history_next_offset: 50, comments_complete: false, history_complete: false,
  })
}
function accessibleName(state, item) {
  if (item.props['aria-label']) return String(item.props['aria-label'])
  if (item.props['aria-labelledby']) return String(item.props['aria-labelledby']).split(/\s+/).map(id => {
    const label = walk(state.host).find(item => item.props.id === id)
    return label ? text(label) : ''
  }).join(' ').trim()
  return item.tag === 'button' || item.props.role === 'button' || item.tag === 'a' ? visibleText(item) : ''
}
function hidden(item) {
  return item.props.hidden || item.props['aria-hidden'] === true || item.props['aria-hidden'] === 'true' ||
    /display\s*:\s*none|visibility\s*:\s*hidden/i.test(String(item.props.style || '')) ||
    item.props.style?.display === 'none' || item.props.style?.visibility === 'hidden'
}
function visibleText(item) {
  if (hidden(item)) return ''
  return `${item.text} ${item.props.innerHTML || ''} ${item.children.map(visibleText).join(' ')}`.replace(/\s+/g, ' ').trim()
}
function visibleNodes(state) {
  const visit = item => hidden(item) || ['script', 'style', 'template'].includes(item.tag)
    ? [] : [item, ...item.children.flatMap(visit)]
  return visit(state.host)
}
function availableActions(state) {
  return visibleNodes(state).filter(item => (item.tag === 'button' || item.props.role === 'button' || item.tag === 'a') &&
    isEnabled(item) && accessibleName(state, item))
}
function discussionSnapshot(state) {
  const nodes = visibleNodes(state), admitted = new Set(nodes)
  const byId = new Map(nodes.filter(item => item.props.id).map(item => [item.props.id, item]))
  const parents = new Map(), texts = new Map(), names = new Map(), subtrees = new Map()
  for (const item of nodes) for (const child of item.children) if (admitted.has(child)) parents.set(child, item)
  function textOf(item) {
    if (!admitted.has(item)) return ''
    if (!texts.has(item)) texts.set(item, `${item.text} ${item.props.innerHTML || ''} ${item.children.map(textOf).join(' ')}`.replace(/\s+/g, ' ').trim())
    return texts.get(item)
  }
  function nameOf(item) {
    if (!names.has(item)) names.set(item, item.props['aria-label'] ? String(item.props['aria-label']) : item.props['aria-labelledby']
      ? String(item.props['aria-labelledby']).split(/\s+/).map(id => textOf(byId.get(id))).join(' ').trim()
      : ['button', 'a', 'summary'].includes(item.tag) || item.props.role === 'button' ? textOf(item) : '')
    return names.get(item)
  }
  function contains(parent, child) {
    if (!subtrees.has(parent)) subtrees.set(parent, new Set(walk(parent)))
    return subtrees.get(parent).has(child)
  }
  const hints = {}
  for (const kind of ['comments', 'history']) {
    const purpose = kind === 'comments' ? /comment|thread|коммент|обсужден/i : /histor|истори/i
    const marker = kind === 'comments' ? 'Synthetic comment' : 'Synthetic operator'
    const marked = new Map()
    function hasMarker(item) {
      if (!admitted.has(item)) return false
      if (!marked.has(item)) marked.set(item, item.text.includes(marker) || String(item.props.innerHTML || '').includes(marker) || item.children.some(hasMarker))
      return marked.get(item)
    }
    hints[kind] = nodes.filter(item => purpose.test(nameOf(item)) ||
      ((/^h[1-6]$/.test(item.tag) || item.tag === 'summary') && purpose.test(textOf(item))) ||
      (hasMarker(item) && !item.children.some(hasMarker)))
  }
  return { nodes, parents, textOf, nameOf, contains, hints, associations: { comments: new Map(), history: new Map() } }
}
function associatedWith(state, item, kind, snapshot = discussionSnapshot(state)) {
  const cached = snapshot.associations[kind]
  if (cached.has(item)) return cached.get(item)
  const hints = snapshot.hints[kind]
  const purpose = kind === 'comments' ? /comment|thread|коммент|обсужден/i : /histor|истори/i
  const ids = new Set(String(item.props['aria-controls'] || '').split(/\s+/))
  let associated = hints.some(hint => snapshot.contains(hint, item)) || purpose.test(snapshot.nameOf(item)) ||
    hints.some(hint => hint.props.id && ids.has(hint.props.id))
  if (!associated) {
    const otherHints = snapshot.hints[kind === 'comments' ? 'history' : 'comments']
    for (let ancestor = snapshot.parents.get(item); ancestor; ancestor = snapshot.parents.get(ancestor)) {
      if (hints.some(hint => snapshot.contains(ancestor, hint))) {
        associated = !otherHints.some(hint => snapshot.contains(ancestor, hint))
        break
      }
    }
  }
  cached.set(item, associated)
  return associated
}
function discussionStatus(state, kind) {
  // This immutable observation is local to one check, never retained across user updates.
  const snapshot = discussionSnapshot(state)
  const hints = snapshot.hints[kind]
  const otherHints = snapshot.hints[kind === 'comments' ? 'history' : 'comments']
  return snapshot.nodes.filter(item => associatedWith(state, item, kind, snapshot) &&
    !(hints.some(hint => snapshot.contains(item, hint)) && otherHints.some(hint => snapshot.contains(item, hint))))
    .map(snapshot.textOf).filter(Boolean)
}
function rangeMatches(statuses, from, to, total, accumulatedFrom) {
  const starts = accumulatedFrom === undefined ? [from] : [from, accumulatedFrom]
  const alternatives = starts.flatMap(start => [[start, to, total], ...(start > 0 ? [[start - 1, to, total]] : []), ...(start === to ? [[start, total]] : [])])
  return statuses.some(status => {
    const values = (status.match(/\d+/g) || []).map(Number)
    return alternatives.some(expected => values.some((_value, index) => expected.every((value, offset) => values[index + offset] === value)))
  })
}
function assertRange(state, kind, from, to, total, accumulatedFrom) {
  assert.ok(rangeMatches(discussionStatus(state, kind), from, to, total, accumulatedFrom), 'Disclose the returned or genuinely accumulated range and authorized total without prescribing its wording or punctuation')
}
function nextControls(state, kind) {
  const snapshot = discussionSnapshot(state)
  const purpose = kind === 'comments' ? /comment|thread|коммент|обсужден/i : /histor|истори/i
  const controls = snapshot.nodes.filter(item => (['button', 'a'].includes(item.tag) || item.props.role === 'button') && isEnabled(item) &&
    /next|more|continu|load|далее|ещ[её]|продолж|загруз/i.test(snapshot.nameOf(item)) &&
    !/previous|back|назад|предыдущ/i.test(snapshot.nameOf(item)) &&
    (purpose.test(snapshot.nameOf(item)) || !/issue|task|card|project|file|attach|image|ticket|карточ|проект|файл|вложен|изображ/i.test(snapshot.nameOf(item))))
  const scoped = controls.filter(item => associatedWith(state, item, kind, snapshot))
  if (scoped.length) return scoped
  const other = kind === 'comments' ? 'history' : 'comments'
  return controls.filter(item => !associatedWith(state, item, other, snapshot))
}
function isEnabled(item) {
  if (!item || item.props.readonly || item.props['aria-readonly'] === 'true') return false
  for (let ancestor = item; ancestor; ancestor = ancestor.parent) {
    if ((ancestor === item || ancestor.tag === 'fieldset') && ancestor.props.disabled) return false
    if (ancestor.props['aria-disabled'] === 'true' || ancestor.props['aria-disabled'] === true) return false
  }
  return true
}
function continuation(state, kind) {
  const controls = nextControls(state, kind)
  assert.ok(controls.length, `Incomplete ${kind} must expose its own named actionable continuation`)
  return controls.find(item => /more|load|ещ[её]|загруз/i.test(accessibleName(state, item))) || controls[0]
}
function assertComplete(state, kind) {
  assert.equal(nextControls(state, kind).length, 0, 'No later records means no enabled continuation; previous and unrelated actions remain permitted')
}
async function click(button) {
  assert.ok(button && isEnabled(button), 'The rendered action must be present and enabled')
  const handlers = Array.isArray(button.props.onClick) ? button.props.onClick : [button.props.onClick]
  assert.ok(handlers.every(handler => typeof handler === 'function'), 'The rendered action must have a real compiled handler')
  for (const handler of handlers) await handler({ preventDefault() { }, stopPropagation() { } })
  await tick()
}

async function assertHistoricalAccess(state, kind, body) {
  function accessPath() {
    const snapshot = discussionSnapshot(state)
    return snapshot.nodes.some(item => {
      if (item.tag === 'details' && snapshot.textOf(item).includes(body) &&
        walk(item).some(child => child.tag === 'summary' && snapshot.nameOf(child))) return true
      if (`${item.text}${item.props.innerHTML || ''}`.includes(body)) {
        let disclosed = true
        for (let ancestor = snapshot.parents.get(item); ancestor; ancestor = snapshot.parents.get(ancestor)) {
          if (ancestor.tag === 'details' && !ancestor.props.open) disclosed = false
        }
        if (disclosed) return true
      }
      if (item.tag !== 'a' || !snapshot.nameOf(item) || typeof item.props.href !== 'string') return false
      const href = item.props.href
      if (href.startsWith('data:')) {
        const separator = href.indexOf(',')
        if (separator < 0) return false
        const value = href.slice(0, separator).endsWith(';base64')
          ? Buffer.from(href.slice(separator + 1), 'base64').toString('utf8') : decodeURIComponent(href.slice(separator + 1))
        return value.includes(body)
      }
      const url = new URL(href, 'http://synthetic.invalid')
      return url.origin === 'http://synthetic.invalid' && url.pathname === '/api/issues/7' &&
        url.searchParams.get('detail_mode') === 'paged' && Number(url.searchParams.get(`${kind}_offset`) ?? 0) === 0
    })
  }
  const before = detailReads(state).length
  if (!accessPath()) {
    const action = availableActions(state).find(item => /(?:show|view|inspect|open|download|показ|просмотр|откры|скача).*(?:record|history|value|запис|истори|значен)|(?:record|history|value|запис|истори|значен).*(?:show|view|inspect|open|download|показ|просмотр|откры|скача)/i.test(accessibleName(state, item)))
    assert.ok(action, 'An intact historical value needs an explicit named disclosure or raw access path, not mandatory expansion on initial render')
    await click(action)
  }
  assert.ok(accessPath(), 'Inline content, a native disclosure, or an authorized raw/download path must expose the intact historical value')
  const reads = detailReads(state).slice(before)
  assert.ok(reads.length <= 1, 'Explicit access may fetch one bounded record page, never drain history automatically')
  for (const read of reads) {
    assert.equal(read.url.searchParams.get('detail_mode'), 'paged')
    assert.equal(Number(read.url.searchParams.get(`${kind}_offset`) ?? 0), 0)
    const limit = Number(read.url.searchParams.get(`${kind}_limit`) ?? 50)
    assert.ok(Number.isInteger(limit) && limit >= 1 && limit <= 200)
  }
}

for (const [kind, returned, total] of [['comments', 50, 55], ['history', 50, 53]]) test(`incomplete ${kind} discloses its own loaded range, total, and completeness`, async t => {
  const state = await mount(t, { page: true, route: '/issues/7', payload: firstPages() })
  assertRange(state, kind, 1, returned, total)
  continuation(state, kind)
  assert.equal(detailReads(state).length, 1, 'Incomplete detail must remain bounded until explicit load-more')
  assert.equal(writes(state).length, 0)
})

for (const kind of ['comments', 'history']) test(`${kind} load-more performs one bounded real read without draining or replacing the other stream`, async t => {
  const first = firstPages()
  const state = await mount(t, { page: true, route: '/issues/7', payload: first })
  const before = detailReads(state).length
  const button = continuation(state, kind)
  const later = kind === 'comments'
    ? { ...first, comments: Array.from({ length: 5 }, (_, index) => comment(551 + index)), comments_offset: 50, comments_next_offset: null, comments_complete: true }
    : { ...first, history: [history(51), history(52), history(53)], history_offset: 50, history_next_offset: null, history_complete: true }
  state.setDetail(later)
  await click(button)
  await settle(state, () => detailReads(state).length > before, 'Explicit continuation must reach the real REST reader')
  assert.equal(detailReads(state).length, before + 1, 'One click cannot drain all remaining pages')
  assert.equal(detailReads(state).at(-1).url.searchParams.get('detail_mode'), 'paged')
  const query = detailReads(state).at(-1).url.searchParams
  const other = kind === 'comments' ? 'history' : 'comments'
  assert.equal(query.get(`${kind}_offset`), '50', 'Use the first unreturned record, not the default or another collection offset')
  assert.equal(Number(query.get(`${other}_offset`) ?? 0), 0, 'Advancing one stream must not advance the other')
  assert.equal(Number(query.get('comments_limit') ?? 50), 50)
  assert.equal(Number(query.get('history_limit') ?? 50), 50)
  if (kind === 'comments') {
    assert.ok(state.issues.comments.some(item => item.id === 555), 'The later comment page must actually become readable')
    assertRange(state, 'comments', 51, 55, 55, 1)
    assertRange(state, 'history', 1, 50, 53)
  } else {
    assert.deepEqual(state.issues.comments.map(item => item.id), Array.from({ length: 50 }, (_, index) => 501 + index), 'Loading history must not replace the separately loaded comments')
    assertRange(state, 'history', 51, 53, 53, 1)
  }
  assert.equal(writes(state).length, 0)
})

test('control: an authorized historical comment exceeding the 4 MiB page target remains intact, not truncated by the UI', async t => {
  const body = 'x'.repeat(4 * 1024 * 1024 + 1)
  const state = await mount(t, { payload: detail(issue(), { comments: [comment(901, body)], comment_count: 1, comments_oversized: true }) })
  await state.issues.openIssue(7)
  assert.equal(state.issues.comments.length, 1)
  assert.equal(state.issues.comments[0].id, 901)
  assert.ok(state.issues.comments[0].body === body, 'A page target is not permission to truncate historical data')
  assert.equal(detailReads(state).length, 1)
})

test('control: synthetic comment markup remains visible data without executable markup in the real page', async t => {
  const body = '<img src=x onerror="synthetic()">'
  const state = await mount(t, { page: true, route: '/issues/7', payload: detail(issue(), { comments: [comment(902, body)], comment_count: 1 }) })
  const nodes = walk(state.host)
  assert.equal(nodes.some(item => item.tag === 'img' || item.tag === 'script'), false, 'Quoted comment data must not create executable host elements')
  assert.equal(nodes.some(item => /<\s*(?:img|script)\b/i.test(String(item.props.innerHTML || ''))), false, 'Injected HTML must not contain the hostile executable element')
  const entities = { lt: '<', gt: '>', quot: '"', apos: "'", amp: '&' }
  const visible = item => {
    const htmlText = String(item.props.innerHTML || '').replace(/<[^>]*>/g, '').replace(/&(?:(lt|gt|quot|apos|amp)|#(\d+)|#x([\da-f]+));/gi,
      (_match, named, decimal, hex) => named ? entities[named.toLowerCase()] : String.fromCodePoint(Number.parseInt(decimal || hex, decimal ? 10 : 16)))
    return item.text + htmlText + item.children.map(visible).join(' ')
  }
  assert.ok(visible(state.host).includes(body), 'Both a safe text-node renderer and escaped HTML must preserve the visible quoted text')
  assert.equal(state.issues.comments[0].body, body)
  assert.equal(writes(state).length, 0)
})

for (const kind of ['comments', 'history']) test(`a complete nonzero ${kind} page reached by load-more displays its returned or accumulated range honestly`, async t => {
  const first = firstPages()
  const state = await mount(t, { page: true, route: '/issues/7', payload: first })
  const later = kind === 'comments'
    ? { ...first, comments: Array.from({ length: 5 }, (_, index) => comment(551 + index)), comments_offset: 50, comments_next_offset: null, comments_complete: true }
    : { ...first, history: [history(51), history(52), history(53)], history_offset: 50, history_next_offset: null, history_complete: true }
  state.setDetail(later)
  await click(continuation(state, kind))
  const query = detailReads(state).at(-1).url.searchParams
  assert.equal(query.get(`${kind}_offset`), '50', 'The effective nonzero response offset must match the actual continuation request')
  const last = kind === 'comments' ? 55 : 53
  assertRange(state, kind, 51, last, last, 1)
  assertComplete(state, kind)
  assert.equal(detailReads(state).length, 2)
  assert.equal(writes(state).length, 0)
})

for (const kind of ['comments', 'history']) test(`an empty later ${kind} page after live collection shrink does not invent an interval or erase a valid accumulated range`, async t => {
  const first = firstPages()
  const state = await mount(t, { page: true, route: '/issues/7', payload: first })
  // The first page advertised a continuation. Concurrent deletion removes its tail before this explicit read.
  const later = kind === 'comments'
    ? { ...first, comments: [], comment_count: 50, comments_offset: 50, comments_next_offset: null, comments_complete: true }
    : { ...first, history: [], history_count: 50, history_offset: 50, history_next_offset: null, history_complete: true }
  state.setDetail(later)
  await click(continuation(state, kind))
  assert.equal(detailReads(state).at(-1).url.searchParams.get(`${kind}_offset`), '50')
  const status = discussionStatus(state, kind)
  assert.ok(rangeMatches(status, 1, 50, 50) || rangeMatches(status, 0, 0, 50), 'An empty returned page must show an empty interval or a genuinely retained 1–50 range, with authorized total 50')
  if (rangeMatches(status, 1, 50, 50)) {
    if (kind === 'comments') assert.deepEqual(state.issues.comments.map(item => item.id), first.comments.map(item => item.id), 'An accumulated range requires the earlier comments to remain readable')
    else for (const record of first.history) assert.ok(text(state.host).includes(record.actor.label), 'An accumulated history range requires every earlier change record to remain readable')
  }
  assertComplete(state, kind)
  assert.equal(detailReads(state).length, 2)
  assert.equal(writes(state).length, 0)
})

for (const kind of ['comments', 'history']) test(`an intact oversized ${kind} singleton has an explicit accessible page-target notice`, async t => {
  const body = 'x'.repeat(4 * 1024 * 1024 + 1)
  const record = { ...history(1), changed_fields: ['body'], changes: { body: { before: null, after: body } } }
  const payload = kind === 'comments'
    ? detail(issue(), { comments: [comment(901, body)], comment_count: 1, comments_oversized: true })
    : detail(issue({ revision: '1' }), { history: [record], history_count: 1, history_oversized: true })
  const state = await mount(t, { page: true, route: '/issues/7', payload })
  const notices = discussionStatus(state, kind)
  assertRange(state, kind, 1, 1, 1)
  assertComplete(state, kind)
  assert.equal(detailReads(state).length, 1, 'Opening an oversized singleton must not drain later pages')
  if (kind === 'comments') assert.ok(state.issues.comments[0].body === body, 'The notice must not accompany a truncated historical record')
  else await assertHistoricalAccess(state, kind, body)
  assert.equal(writes(state).length, 0)
  const normal = await mount(t, { page: true, route: '/issues/7', payload: { ...payload, [`${kind}_oversized`]: false } })
  const ordinaryNotices = discussionStatus(normal, kind)
  assert.ok(notices.some(notice => !ordinaryNotices.includes(notice)), 'The oversized flag must add a visible accessible explanation rather than leave an ordinary page indistinguishable; wording is unrestricted')
})

test('REVISION_REQUIRED retains the actual page draft and stable recovery code without a blind retry', async t => {
  const state = await mount(t, {
    page: true, route: '/issues/7', respond: call => call.method === 'PATCH'
      ? json({ error: { code: 'REVISION_REQUIRED', message: 'Read a current revision before editing' } }, 428)
      : undefined
  })
  state.page.priorityDraft = 'high'
  state.page.commentDraft = 'Synthetic revision-required draft'
  const reads = detailReads(state).length
  await state.page.updateCurrentField('priority', 'high')
  await tick()
  assert.equal(state.page.priorityDraft, 'high')
  assert.equal(state.page.commentDraft, 'Synthetic revision-required draft')
  assert.equal(patches(state).length, 1)
  assert.equal(detailReads(state).length, reads)
  assert.equal(state.page.otherMutationResult.code, 'REVISION_REQUIRED')
  assert.match(text(state.host), /REVISION_REQUIRED/)
})

test('a fresh consumer restores the original create intent and reconciles only after an explicit user action', async t => {
  const tabStorage = new Map()
  const first = await mount(t, { page: true, respond: loseReply, tabStorage })
  await prepareCreate(first)
  await submitCreate(first)
  const original = copy(creates(first)[0].body)
  first.unmount()
  const resumed = await mount(t, { page: true, tabStorage, respond: call => call.method === 'POST' && call.url.pathname === '/api/issues' ? json({ id: 7, revision: '7', replayed: true }) : undefined })
  await settle(resumed, () => resumed.issues.pendingCreate.value !== null, 'The original actor must be confirmed before restoring its intent')
  assert.equal(creates(resumed).length, 0, 'Reload cannot automatically replay a retained task')
  await reconcileCreate(resumed)
  assert.deepEqual(creates(resumed).map(call => call.body), [original])
})

test('unavailable tab storage refuses create before dispatch instead of losing resumability', async t => {
  const state = await mount(t, { storageUnavailable: true })
  await assert.rejects(() => state.issues.createIssue(createInput()), /CREATE_STORAGE_UNAVAILABLE/)
  assert.equal(creates(state).length, 0)
})

test('a retained intent is neither disclosed nor replayed by a different persisted browser actor', async t => {
  const tabStorage = new Map()
  const identity = id => ({ authenticated: true, auth_disabled: false, role: 'admin', user: { id, email: 'synthetic@example.test', role: 'admin' }, actor: { kind: 'operator', reference: `browser:${id}` } })
  const first = await mount(t, { respond: loseReply, tabStorage, auth: identity('synthetic-user-a') })
  await first.issues.createIssue(createInput())
  first.unmount()
  const other = await mount(t, { tabStorage, auth: identity('synthetic-user-b') })
  await settle(other, () => other.issues.createRecoveryError.value.includes('CREATE_ACTOR_MISMATCH'), 'A different authenticated subject cannot restore this intent')
  assert.equal(other.issues.pendingCreate.value, null)
  await assert.rejects(() => other.issues.reconcileCreate(), /CREATE_ACTOR_MISMATCH/)
  await assert.rejects(() => other.issues.createIssue(createInput({ title: 'Different task' })), /CREATE_RECONCILIATION_REQUIRED/)
  assert.equal(creates(other).length, 0)
})

test('an ambiguous authenticated master or session response cannot fabricate a resumable actor', async t => {
  const state = await mount(t, { auth: { authenticated: true, role: 'admin', auth_disabled: false } })
  await assert.rejects(() => state.issues.createIssue(createInput()), /CREATE_IDENTITY_UNAVAILABLE/)
  assert.equal(creates(state).length, 0)
})

test('ACTOR_CHANGED preserves the original expected actor, source, key and payload for explicit reconciliation', async t => {
  const identity = { authenticated: true, auth_disabled: true, role: 'admin', source: 'auth-disabled', synthetic: true, actor: { kind: 'operator', reference: 'operator:auth-disabled' } }
  const state = await mount(t, {
    auth: identity, respond: call => {
      if (call.method === 'POST' && call.url.pathname === '/api/issues') {
        identity.actor = { kind: 'operator', reference: 'operator:session' }
        identity.auth_disabled = false
        delete identity.source
        delete identity.synthetic
        return json({ error: { code: 'ACTOR_CHANGED', message: 'Reconcile the original intent' } }, 409)
      }
    }
  })
  const result = await state.issues.createIssue(createInput())
  assert.equal(result.kind, 'conflict')
  assert.equal(result.code, 'ACTOR_CHANGED')
  const original = copy(creates(state)[0].body)
  assert.deepEqual(original.expected_actor, { kind: 'operator', reference: 'operator:auth-disabled' })
  await assert.rejects(() => state.issues.reconcileCreate(), /CREATE_ACTOR_MISMATCH/)
  assert.equal(creates(state).length, 1, 'A different current actor cannot cause an automatic or unguarded replacement create')
  identity.actor = { kind: 'operator', reference: 'operator:auth-disabled' }
  Object.assign(identity, { auth_disabled: true, source: 'auth-disabled', synthetic: true })
  state.setResponder(call => call.method === 'POST' && call.url.pathname === '/api/issues' ? json({ id: 7, revision: '7', replayed: true }) : undefined)
  assert.equal((await state.issues.reconcileCreate()).kind, 'committed_verified')
  assert.deepEqual(creates(state)[1].body, original)
})

test('historical system actors remain readable without becoming authenticated create identities', async t => {
  const actor = { kind: 'system', reference: 'synthetic-system' }
  const state = await mount(t, { payload: detail(issue({ actor }), { history: [{ ...history(1), actor }], history_count: 1 }) })
  await state.issues.openIssue(7)
  assert.deepEqual(state.issues.detail.value.actor, actor)
  assert.deepEqual(state.issues.history[0].actor, actor)
  assert.equal(writes(state).length, 0)
  const unverified = await mount(t, { auth: { authenticated: true, auth_disabled: true, actor } })
  await assert.rejects(() => unverified.issues.createIssue(createInput()), /CREATE_IDENTITY_UNAVAILABLE/)
  assert.equal(creates(unverified).length, 0)
})

for (const [name, pages] of [
  ['comments falsely complete before the authorized total', { comments: [comment(901)], comment_count: 2, comments_next_offset: null, comments_complete: true }],
  ['history falsely complete before the authorized total', { history: [history(1)], history_count: 2, history_next_offset: null, history_complete: true }],
  ['unsafe comment range arithmetic', { comments: [comment(901)], comment_count: Number.MAX_SAFE_INTEGER, comments_offset: Number.MAX_SAFE_INTEGER, comments_next_offset: null, comments_complete: true }],
]) test(`real detail reading rejects ${name}`, async t => {
  const state = await mount(t, { payload: detail(issue(), pages) })
  await state.issues.openIssue(7)
  assert.equal(detailReads(state).length, 1)
  assert.equal(state.issues.detailState.value.kind, 'error')
  assert.equal(state.issues.detailState.value.error.category, 'invalid-response')
  assert.match(state.issues.detailState.value.error.message, /DETAIL_PAGING_REQUIRED/)
  assert.equal(state.issues.detail.value, null)
  assert.deepEqual(state.issues.comments, [])
  assert.deepEqual(state.issues.history, [])
  assert.equal(writes(state).length, 0)
})

test('a page draft without a base revision cannot borrow a subsequently read revision for material submission', async t => {
  const state = await mount(t, { page: true, route: '/issues/7', payload: detail(issue({ revision: undefined })) })
  await enter(state, 'issue-comment', 'Synthetic retained draft without a revision')
  state.setDetail(detail(issue({ revision: '8', priority: 'low' })))
  await state.issues.openIssue(7)
  await tick()
  assert.equal(state.page.activeIssue.revision, '8')
  assert.equal(state.page.editBase.revision, null)
  await enter(state, 'issue-priority', 'high', true)
  assert.equal(patches(state).length, 0, 'The real page cannot implicitly borrow the fresh composable revision')
  assert.equal(state.page.priorityDraft, 'high')
  assert.equal(state.page.commentDraft, 'Synthetic retained draft without a revision')
  assert.equal(state.page.editBase.revision, null)
  assert.match(text(state.host), /REVISION_REQUIRED/)
  assert.equal(state.page.editNeedsReconciliation, true)
  await state.page.readLatestIssue()
  assert.equal(patches(state).length, 0)
  assert.equal(state.page.editBase.revision, null, 'Fresh read alone cannot accept a new draft base')
  state.page.acceptReadRevision()
  assert.equal(state.page.editBase.revision, '8', 'Adopting a new base remains an explicit separate action')
  assert.equal(state.page.priorityDraft, 'high')
  assert.equal(patches(state).length, 0, 'Accepting a base does not submit the retained edit')
})

for (const generation of ['authGeneration', 'operatorUnauthorizedRevision']) test(`a stale retained-intent restore cannot disclose payload after ${generation} changes`, async t => {
  const tabStorage = new Map()
  const first = await mount(t, { tabStorage, respond: loseReply })
  await first.issues.createIssue(createInput())
  first.unmount()
  let release
  const held = new Promise(resolve => { release = resolve })
  const auth = { authenticated: true, auth_disabled: true, role: 'admin', source: 'auth-disabled', synthetic: true, actor: { kind: 'operator', reference: 'operator:auth-disabled' } }
  const resumed = await mount(t, {
    tabStorage, respond: async call => {
      if (call.url.pathname === '/api/auth/me') { await held; return json(auth) }
    }
  })
  const restoring = resumed.issues.restoreCreateIntent()
  resumed.api[generation].value++
  const recheckMessage = resumed.issues.createRecoveryError.value
  assert.match(recheckMessage, /CREATE_IDENTITY_RECHECK_REQUIRED/)
  release()
  await restoring
  await tick()
  assert.equal(resumed.issues.pendingCreate.value, null, 'The old matching auth response cannot republish the retained payload')
  assert.equal(resumed.issues.createRecoveryError.value, recheckMessage, 'A stale restore cannot erase the current identity-recheck message')
  assert.equal(creates(resumed).length, 0)
})

test('changing identity hides retained create form copies until the original actor explicitly recovers', async t => {
  const state = await mount(t, { page: true, respond: loseReply })
  await prepareCreate(state)
  await submitCreate(state)
  const original = copy(creates(state)[0].body)
  assert.equal(state.page.showCreate, true)
  const otherActor = { authenticated: true, auth_disabled: false, role: 'admin', actor: { kind: 'operator', reference: 'operator:session' } }
  state.setResponder(call => call.url.pathname === '/api/auth/me' ? json(otherActor) : undefined)
  state.api.authGeneration.value++
  await tick()
  assert.equal(state.page.showCreate, false)
  assert.equal(state.page.createTitle, '')
  assert.equal(state.page.createBody, '')
  assert.equal(state.issues.pendingCreate.value, null)
  await state.page.openCreate()
  assert.equal(state.page.showCreate, false, 'A mismatched actor cannot reopen copies of the retained form')
  assert.equal(creates(state).length, 1)
  state.setResponder(call => call.method === 'POST' && call.url.pathname === '/api/issues' ? json({ id: 7, revision: '7', replayed: true }) : undefined)
  state.api.authGeneration.value++
  await reconcileCreate(state)
  assert.deepEqual(creates(state)[1].body, original, 'Returning to the original actor recovers the same protected intent')
})

test('identity refresh does not discard a normal create draft that has never been dispatched', async t => {
  const state = await mount(t, { page: true })
  await prepareCreate(state)
  state.api.authGeneration.value++
  await tick()
  assert.equal(state.page.createTitle, 'Synthetic retained task')
  assert.equal(state.page.createBody, 'Synthetic original body')
  assert.equal(state.page.showCreate, true)
  assert.equal(creates(state).length, 0)
})

for (const [code, status, generation] of [['INVALID_ARGUMENT', 400, 'authGeneration'], ['ISSUE_GONE', 410, 'operatorUnauthorizedRevision']]) test(`a late ${code} response cannot republish a retained payload after identity invalidation`, async t => {
  let release
  const held = new Promise(resolve => { release = resolve })
  const state = await mount(t, {
    respond: async call => {
      if (call.method === 'POST' && call.url.pathname === '/api/issues') { await held; return json({ error: { code, message: 'Synthetic late resolution' } }, status) }
    }
  })
  const submitting = state.issues.createIssue(createInput())
  await settle(state, () => creates(state).length === 1, 'The original create must be dispatched before the identity changes')
  state.api[generation].value++
  const recheckMessage = state.issues.createRecoveryError.value
  assert.match(recheckMessage, /CREATE_IDENTITY_RECHECK_REQUIRED/)
  assert.equal(state.issues.pendingCreate.value, null)
  release()
  const result = await submitting
  await tick()
  assert.equal(result.code, code)
  assert.equal(state.issues.pendingCreate.value, null)
  assert.equal(state.issues.createRecoveryError.value, recheckMessage)
  assert.equal(creates(state).length, 1, 'Late resolution cannot submit another create')
})

function rawWireObjectField(source, field) {
  // JSON.parse checks the request syntax; this walk only extracts original wire bytes, not admission rules.
  assert.equal(typeof source, 'string', 'Observe the actual JSON request body before fixture parsing')
  const parsed = JSON.parse(source)
  assert.ok(parsed && typeof parsed === 'object' && !Array.isArray(parsed))
  const tokens = [...source.matchAll(/"(?:\\[\s\S]|[^"\\])*"|[{}\[\]:,]|[^\s{}\[\]:,]+/g)]
  const fields = []
  let depth = 0
  for (let index = 0; index < tokens.length; index++) {
    const token = tokens[index][0]
    if (token === '{' || token === '[') depth++
    else if (token === '}' || token === ']') depth--
    else if (depth === 1 && token.startsWith('"') && tokens[index + 1]?.[0] === ':' && JSON.parse(token) === field) {
      const first = tokens[index + 2]
      assert.equal(first?.[0], '{', 'The metadata wire value must remain an object')
      let containers = 0, end = index + 2
      for (; end < tokens.length; end++) {
        if (tokens[end][0] === '{' || tokens[end][0] === '[') containers++
        else if (tokens[end][0] === '}' || tokens[end][0] === ']') containers--
        if (containers === 0) break
      }
      fields.push(source.slice(first.index, tokens[end].index + 1))
      index = end
    }
  }
  assert.equal(fields.length, 1, 'Inspect the one real top-level metadata field, not an encoded copy elsewhere')
  return fields[0]
}

async function prepareRawMetadataPage(t, journey, respond) {
  const state = await mount(t, { page: true, route: journey === 'create' ? '/issues' : '/issues/7', respond })
  if (journey === 'create') await prepareCreate(state)
  if (journey === 'comment') await enter(state, 'issue-comment', 'Synthetic raw metadata comment')
  const inputName = journey === 'create' ? 'new-issue-metadata' : journey === 'comment' ? 'issue-comment-metadata' : 'issue-metadata'
  const input = namedInput(state, inputName)
  let region = input.parent
  while (region && region.tag !== 'details') region = region.parent
  if (region) region.props.open = true // The host renderer does not implement native details toggling.
  const submit = journey === 'create'
    ? translatedButton(state, 'issues.create.submit')
    : journey === 'comment' ? translatedButton(state, 'issues.thread.submit') : region && walk(region).find(item => item.tag === 'button' && item.props.onClick)
  assert.ok(submit, 'Use the actual metadata form submission control without prescribing its label')
  return { state, inputName, submit }
}

for (const journey of ['create', 'material edit']) {
  for (const [name, raw] of [
    ['lossy decimal', '{ "numeric": 0.10000000000000001, "note": "synthetic" }'],
    ['duplicate member', '{ "field": 1, "field": 2, "note": "synthetic" }'],
  ]) test(`raw metadata textarea ${journey}: ${name} is rejected locally or preserved unchanged on the wire`, async t => {
    const { state, inputName, submit } = await prepareRawMetadataPage(t, journey, call => call.method !== 'GET'
      ? json({ error: { code: 'INVALID_ARGUMENT', message: 'Synthetic raw metadata admission refusal' } }, 400)
      : undefined)
    await enter(state, inputName, raw)
    if (isEnabled(submit)) await click(submit)
    await tick()
    assert.equal(state.inputValue(inputName), raw, 'Preserve the actual textarea draft after refusing this metadata')
    const sent = writes(state)
    assert.ok(sent.length <= 1, 'Metadata refusal cannot automatically dispatch another write')
    if (sent.length) {
      assert.equal(sent[0].method, journey === 'create' ? 'POST' : 'PATCH')
      assert.equal(rawWireObjectField(sent[0].rawBody, 'metadata'), raw, 'Do not erase raw numeric tokens or duplicate members before server admission')
      assert.match(text(state.host), /INVALID_ARGUMENT/)
    } else {
      assert.ok(visibleNodes(state).some(item => (item.props.role === 'alert' && visibleText(item)) || item.props['aria-invalid'] === true || item.props['aria-invalid'] === 'true' || item.props['data-kind'] === 'validation_error'), 'A local refusal needs observable validation feedback, not a silent no-op')
    }
  })

  test(`raw metadata textarea ${journey}: admitted JSON reaches the real HTTP boundary`, async t => {
    const { state, inputName, submit } = await prepareRawMetadataPage(t, journey)
    const raw = '{ "numeric": 0.1, "field": 2, "unknown": { "note": "synthetic" } }'
    const admitted = JSON.parse(raw)
    await enter(state, inputName, raw)
    state.setDetail(detail(issue({ metadata: admitted })))
    await click(submit)
    const sent = writes(state)
    assert.equal(sent.length, 1, 'The admission oracle must not block ordinary valid metadata')
    assert.equal(sent[0].method, journey === 'create' ? 'POST' : 'PATCH')
    assert.deepEqual(JSON.parse(rawWireObjectField(sent[0].rawBody, 'metadata')), admitted)
  })
}

for (const journey of ['create', 'material edit', 'comment']) test(`raw metadata textarea ${journey}: trailing field injection is rejected before dispatch`, async t => {
  const { state, inputName, submit } = await prepareRawMetadataPage(t, journey)
  const raw = '{"ok":1},"status":"closed"'
  await enter(state, inputName, raw)
  await click(submit)
  assert.equal(writes(state).length, 0)
  assert.equal(state.inputValue(inputName), raw)
  assert.match(text(state.host), /INVALID_ARGUMENT/)
  assert.ok(visibleNodes(state).filter(item => item.props.role === 'alert').every(item => !visibleText(item).includes(raw)), 'Validation errors must not echo protected input')
})

test('raw metadata textarea comment: lossy tokens and duplicate members reach comment_metadata unchanged without retry', async t => {
  const { state, inputName, submit } = await prepareRawMetadataPage(t, 'comment', call => call.method === 'PATCH'
    ? json({ error: { code: 'INVALID_ARGUMENT', message: 'Synthetic raw comment metadata refusal' } }, 400) : undefined)
  const raw = '{ "numeric": 0.10000000000000001, "field": 1, "field": 2 }'
  await enter(state, inputName, raw)
  await click(submit)
  const sent = patches(state)
  assert.equal(sent.length, 1)
  assert.equal(rawWireObjectField(sent[0].rawBody, 'comment_metadata'), raw)
  assert.equal(state.inputValue(inputName), raw)
  assert.match(text(state.host), /INVALID_ARGUMENT/)
  await tick()
  assert.equal(patches(state).length, 1, 'Standalone comment refusal cannot automatically retry its append')
})

test('raw metadata textarea comment: admitted JSON keeps standalone append semantics', async t => {
  const { state, inputName, submit } = await prepareRawMetadataPage(t, 'comment')
  const raw = '{ "numeric": 0.1, "unknown": { "note": "synthetic" } }'
  const admitted = JSON.parse(raw)
  await enter(state, inputName, raw)
  state.setDetail(detail(issue(), { comments: [{ ...comment(901, 'Synthetic raw metadata comment'), metadata: admitted }], comment_count: 1 }))
  await click(submit)
  const sent = patches(state)
  assert.equal(sent.length, 1)
  assert.equal(rawWireObjectField(sent[0].rawBody, 'comment_metadata'), raw)
  assert.equal(Object.hasOwn(sent[0].body, 'expected_revision'), false)
  assert.equal(Object.hasOwn(sent[0].body, 'commentRawMetadata'), false)
})

test('a raw create intent retains its exact wire metadata through lost reply, reload and ACTOR_CHANGED', async t => {
  const tabStorage = new Map()
  const raw = '{ "acceptance": ["resume"], "unknown": { "safe": 0.1 } }'
  const first = await mount(t, { tabStorage, respond: loseReply })
  assert.equal((await first.issues.createIssue(createInput({ rawMetadata: raw }))).kind, 'outcome_unknown')
  const original = creates(first)[0].rawBody
  assert.equal(rawWireObjectField(original, 'metadata'), raw)
  assert.equal(Object.hasOwn(JSON.parse(original), 'rawMetadata'), false)
  first.unmount()
  const resumed = await mount(t, {
    tabStorage, respond: call => call.method === 'POST' && call.url.pathname === '/api/issues'
      ? json({ error: { code: 'ACTOR_CHANGED', message: 'Reconcile the original actor' } }, 409) : undefined
  })
  await settle(resumed, () => resumed.issues.pendingCreate.value !== null, 'Confirm the original actor before exposing its retained intent')
  assert.equal(creates(resumed).length, 0)
  assert.equal((await resumed.issues.reconcileCreate()).code, 'ACTOR_CHANGED')
  assert.equal(creates(resumed).length, 1)
  assert.equal(creates(resumed)[0].rawBody, original)
  await assert.rejects(() => resumed.issues.createIssue(createInput({ title: 'Different task' })), /CREATE_RECONCILIATION_REQUIRED/)
  assert.equal(creates(resumed).length, 1)
})

for (const action of ['create', 'material edit', 'comment']) test(`raw metadata inputs ${action}: object and text forms are mutually exclusive`, async t => {
  const state = await mount(t)
  const run = action === 'create'
    ? () => state.issues.createIssue(createInput({ metadata: {}, rawMetadata: '{}' }))
    : action === 'material edit'
      ? () => state.issues.updateIssue(7, { metadata: {}, rawMetadata: '{}' })
      : () => state.issues.commentIssue(7, 'Synthetic comment', {}, '{}')
  await assert.rejects(run, error => error instanceof TypeError && error.message.startsWith('INVALID_ARGUMENT:'))
  assert.equal(writes(state).length, 0)
})

test('raw material metadata remains the losing draft after conflict and an explicit fresh read', async t => {
  const { state, inputName, submit } = await prepareRawMetadataPage(t, 'material edit', conflict)
  const raw = '{ "outcome": { "state": "losing" }, "unknown": 0.1 }'
  await enter(state, inputName, raw)
  await click(submit)
  assert.equal(rawWireObjectField(patches(state)[0].rawBody, 'metadata'), raw)
  state.setDetail(detail(issue({ revision: '8', metadata: { winner: true } })))
  await state.page.readLatestIssue()
  assert.equal(state.inputValue(inputName), raw)
  assert.ok(visibleNodes(state).filter(item => item.tag === 'pre').some(item => walk(item).some(child => child.text === raw)), 'The exact losing raw fragment must remain inspectable beside the fresh read')
  assert.equal(patches(state).length, 1)
})

test('an unknown standalone comment retains its original raw metadata even if the editable draft changes', async t => {
  const { state, inputName, submit } = await prepareRawMetadataPage(t, 'comment', call => {
    if (call.method === 'PATCH') throw new TypeError('Synthetic comment reply lost after dispatch')
  })
  const raw = '{ "outcome": "original comment", "unknown": 0.1 }'
  await enter(state, inputName, raw)
  await click(submit)
  assert.equal(rawWireObjectField(patches(state)[0].rawBody, 'comment_metadata'), raw)
  await enter(state, inputName, '{"outcome":"another unsent draft"}')
  await state.page.readLatestIssue()
  assert.ok(visibleNodes(state).filter(item => item.tag === 'pre').some(item => walk(item).some(child => child.text === raw)), 'Retain the original unknown append separately from the editable next draft')
  assert.equal(patches(state).length, 1, 'Fresh read and draft edits must not retry an unknown comment append')
})
