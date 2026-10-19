import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import * as routing from 'vue-router'

const selectedProject = '11111111-1111-4111-8111-111111111111'
const issueSourceProject = '22222222-2222-4222-8222-222222222222'
const targetProject = '33333333-3333-4333-8333-333333333333'
const catalog = { items: [selectedProject, issueSourceProject, targetProject].map(project_id => ({ project_id, name: 'Same name' })), unresolved: [] }
const explicit = { domain: 'issues', kind: 'explicit', version: 3, targets: [{ id: '7' }, { id: '8' }] }
const snapshot = { domain: 'issues', kind: 'explicit', selection_version: 3, targets: explicit.targets }
const tick = async () => { await new Promise(setImmediate); await vue.nextTick() }
const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })

function evaluate(file, require, globals = {}) {
  const source = readFileSync(new URL(`../../composables/${file}.ts`, import.meta.url), 'utf8')
  const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(require, module, module.exports, ...Object.values(globals))
  return module.exports
}
const renderer = vue.createRenderer({
  createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
  setText: () => { }, setElementText: () => { }, patchProp: () => { }, parentNode: () => null, nextSibling: () => null, insert: () => { }, remove: () => { },
})

async function mount(t, project = selectedProject) {
  const calls = []
  let respond
  let deleted = false
  let row = { id: 7, title: 'Synthetic handoff', body: '', status: 'open', priority: 'medium', type: 'task', source_project: issueSourceProject, target_project: targetProject, source_project_id: issueSourceProject, target_project_id: targetProject, project_origin: { source_project: issueSourceProject, target_project: targetProject }, labels: [], created_at: '2026-10-01T12:00:00Z', updated_at: '2026-10-01T12:00:00Z' }
  const comments = []
  const fetch = async (path, init = {}) => {
    const url = new URL(path, 'http://synthetic.invalid')
    const call = { url, method: init.method || 'GET', body: init.body ? JSON.parse(init.body) : undefined, credentials: init.credentials }
    calls.push(call)
    if (respond) {
      const response = await respond(call)
      if (response) return response
    }
    if (url.pathname === '/api/projects') return json(catalog)
    if (url.pathname === '/api/issues' && call.method === 'GET') return json({ issues: [row], total: 1 })
    if (url.pathname === '/api/issues' && call.method === 'POST') { row = { ...row, ...call.body, source_project_id: call.body.source_project, target_project_id: call.body.target_project, project_origin: { source_project: call.body.source_project, target_project: call.body.target_project } }; return json({ id: row.id, operation_state: 'completed' }) }
    if (url.pathname === '/api/issues/7' && call.method === 'GET') return deleted ? json({}, 404) : json({ issue: row, comments })
    if (url.pathname === '/api/issues/7' && call.method === 'PATCH') {
      row = { ...row, ...Object.fromEntries(Object.entries(call.body).filter(([key]) => !['source_project', 'source_agent', 'comment'].includes(key))) }
      if (call.body.comment) comments.push({ id: comments.length + 1, issue_id: 7, body: call.body.comment, author_project: call.body.source_project, author_agent: call.body.source_agent })
      return json({ operation_state: 'completed' })
    }
    if (url.pathname === '/api/issues/7' && call.method === 'DELETE') { deleted = true; return new Response(null, { status: 204 }) }
    if (url.pathname === '/api/issues/acknowledge') { row.status = 'acknowledged'; return json({ operation_state: 'completed' }) }
    if (url.pathname === '/api/issues/selection') return json({ selection: snapshot })
    if (url.pathname === '/api/issues/selection/current') return json({ selection: snapshot })
    if (url.pathname === '/api/issues/selection/page') return json({ cursor: 'synthetic-cursor', next_cursor: '', filter_fingerprint: `sha256:${'a'.repeat(64)}`, targets: explicit.targets, total: 2 })
    if (url.pathname === '/api/issues/operations') return json({ request_id: call.body.request_id, operation_state: 'completed', readback: { authoritative: true, kind: 'current', current_state: [{ status: 'acknowledged', priority: 'medium', labels: [], updated_at: row.updated_at }] } })
    throw new Error(`Unexpected fixture request: ${call.method} ${url}`)
  }
  const modules = new Map()
  function require(name) {
    if (name === 'vue') return vue
    if (name === 'vue-router') return routing
    if (name.endsWith('operator-config')) return { operatorConfig: { apiBase: '/api' } }
    if (!name.startsWith('./')) throw new Error(`Unexpected fixture dependency: ${name}`)
    const file = name.slice(2)
    if (!modules.has(file)) modules.set(file, evaluate(file, require, { fetch }))
    return modules.get(file)
  }
  const core = require('./useOperatorProjectContext')
  const issueModule = require('./useOperatorIssues')
  const router = routing.createRouter({ history: routing.createMemoryHistory(), routes: [{ path: '/issues', component: { render: () => null } }] })
  await router.push({ path: '/issues', query: project ? { project } : {} })
  let context, issues
  const app = renderer.createApp({ setup() { context = core.useOperatorProjectContext(); issues = issueModule.useOperatorIssues(); return () => null } })
  app.use(router).mount({})
  t.after(() => app.unmount())
  await tick()
  return { context, issues, router, calls, setResponder: next => { respond = next } }
}

for (const scope of ['', 'all']) {
  test(`issue mutations refuse ${scope || 'missing'} source before dispatch while read-all stays available`, async t => {
    const { issues, calls } = await mount(t, scope)
    const listReads = calls.filter(call => call.url.pathname === '/api/issues')
    assert.equal(listReads.length, scope === 'all' ? 1 : 0)
    if (scope === 'all') {
      assert.equal(listReads[0].url.searchParams.has('project'), false)
      await issues.openIssue(7)
      assert.equal(issues.detail.value.id, 7)
      assert.equal(calls.at(-1).url.searchParams.has('project'), false)
    }
    calls.length = 0
    for (const mutation of [
      () => issues.createIssue({ title: 'Blocked', priority: 'high', type: 'bug', sourceProject: scope, targetProject }),
      () => issues.updateIssue(7, { status: 'resolved' }),
      () => issues.commentIssue(7, 'Blocked'),
      () => issues.rejectIssue(7, 'Blocked'),
      () => issues.acknowledgeIssue(7, selectedProject),
      () => issues.runIssueSelectionOperation(explicit, { kind: 'acknowledge' }, selectedProject),
      () => issues.runIssueSelection([7, 8], { kind: 'priority', priority: 'high' }, selectedProject),
      () => issues.deleteIssue(7),
      () => issues.saveIssueSelection(explicit),
      () => issues.freezeIssueSelection({}, []),
      () => issues.currentIssueSelection(),
      () => issues.loadIssuePage({}),
      () => issues.loadIssuePage({}, 'synthetic-cursor'),
    ]) await assert.rejects(mutation, TypeError)
    assert.deepEqual(calls, [])
  })
}

test('acknowledgement is one atomic POST attributed to selected human source, not the issue source or target', async t => {
  const { issues, calls } = await mount(t)
  calls.length = 0
  const result = await issues.acknowledgeIssue(7, selectedProject)
  assert.equal(result.kind, 'committed_verified')
  assert.deepEqual(calls.map(call => [call.method, call.url.pathname]), [['POST', '/api/issues/acknowledge'], ['GET', '/api/issues/7']])
  assert.deepEqual(calls[0].body, { ids: [7], source_project: selectedProject, source_agent: 'human' })
  assert.equal(result.readback.current.sourceProject, issueSourceProject)
  assert.equal(result.readback.current.targetProject, targetProject)
  for (const source of [issueSourceProject, targetProject, 'Same name', 'dashboard']) await assert.rejects(() => issues.acknowledgeIssue(7, source), TypeError)
  assert.equal(calls.length, 2)
})

test('acknowledgement cannot claim verification when server readback disagrees', async t => {
  const { issues, setResponder } = await mount(t)
  setResponder(call => call.url.pathname === '/api/issues/acknowledge' ? json({ operation_state: 'completed' }) : undefined)
  const result = await issues.acknowledgeIssue(7, selectedProject)
  assert.equal(result.kind, 'committed_verification_pending')
  assert.equal(result.reason, 'readback_invalid')
})

test('every atomic bulk action and selection shape retains selected human source separately from targets', async t => {
  const { issues, calls } = await mount(t)
  const selections = [explicit, { ...explicit, kind: 'page', cursor: 'synthetic-cursor' }, { kind: 'frozen_filter', domain: 'issues', version: 3, selectionToken: '8a546ef1-9d60-4b84-b155-72d7ab4b3000', filterFingerprint: `sha256:${'a'.repeat(64)}`, targetCount: 2, excludedIds: [], expiresAt: '2099-01-01T00:00:00Z', reconfirmationRequired: false }]
  calls.length = 0
  for (const selection of selections) {
    for (const action of [{ kind: 'acknowledge' }, { kind: 'status', status: 'resolved' }, { kind: 'priority', priority: 'high' }, { kind: 'labels', labels: ['evidence'] }]) {
      const result = await issues.runIssueSelectionOperation(selection, action, selectedProject)
      assert.equal(result.kind, 'committed_verified')
      const { kind, ...fields } = action
      const call = calls.at(-1)
      assert.equal(call.url.pathname, '/api/issues/operations')
      assert.equal(call.method, 'POST')
      assert.equal(call.credentials, 'include')
      assert.deepEqual(call.body, { request_id: result.request.requestId, action: kind, selection: { kind: selection.kind, selection_version: 3, ...(selection.kind === 'frozen_filter' ? { selection_token: selection.selectionToken } : {}) }, ...fields, source_project: selectedProject, source_agent: 'human' })
    }
  }
  assert.equal(calls.length, 12)
  await assert.rejects(() => issues.runIssueSelectionOperation(explicit, { kind: 'acknowledge' }, targetProject), TypeError)
  assert.equal(calls.length, 12)
})

test('public ID selection wrapper preserves one source through snapshot and atomic operation', async t => {
  const { issues, calls } = await mount(t)
  calls.length = 0
  const result = await issues.runIssueSelection([7, 8], { kind: 'acknowledge' }, selectedProject)
  assert.equal(result.kind, 'committed_verified')
  assert.deepEqual(calls.map(call => [call.method, call.url.pathname]), [['POST', '/api/issues/selection'], ['POST', '/api/issues/operations']])
  assert.deepEqual(calls[0].body, { selection: { kind: 'explicit', targets: [{ id: '7' }, { id: '8' }] }, source_project: selectedProject, source_agent: 'human' })
  assert.equal(calls[1].body.source_project, selectedProject)
  assert.equal(calls[1].body.source_agent, 'human')
})

test('a delayed public selection snapshot cannot dispatch after project scope changes and returns', async t => {
  const { issues, router, calls, setResponder } = await mount(t)
  let release
  const held = new Promise(resolve => { release = resolve })
  setResponder(async call => { if (call.url.pathname === '/api/issues/selection') { await held; return json({ selection: snapshot }) } })
  calls.length = 0
  const attempt = issues.runIssueSelection([7, 8], { kind: 'acknowledge' }, selectedProject)
  const rejected = assert.rejects(attempt, /scope changed/)
  await router.push(`/issues?project=${targetProject}`); await tick()
  await router.push(`/issues?project=${selectedProject}`); await tick()
  release()
  await rejected
  assert.equal(calls.filter(call => call.url.pathname === '/api/issues/operations').length, 0)
})

test('normal issue updates and deletion require selected source and preserve supported protocols', async t => {
  const { issues, calls } = await mount(t)
  calls.length = 0
  assert.equal((await issues.updateIssue(7, { priority: 'high' })).kind, 'committed_verified')
  assert.equal((await issues.commentIssue(7, 'Synthetic comment')).kind, 'committed_verified')
  assert.equal((await issues.rejectIssue(7, 'Synthetic rejection')).kind, 'committed_verified')
  const patches = calls.filter(call => call.method === 'PATCH')
  assert.equal(patches.length, 3)
  for (const call of patches) assert.deepEqual({ source_project: call.body.source_project, source_agent: call.body.source_agent }, { source_project: selectedProject, source_agent: 'human' })
  const deletion = await issues.deleteIssue(7)
  assert.equal(deletion.kind, 'committed_verified')
  assert.equal(deletion.request.intent.sourceProject, selectedProject)
  const request = calls.find(call => call.method === 'DELETE')
  assert.equal(request.url.pathname, '/api/issues/7')
  assert.equal(request.url.search, `?source_project=${selectedProject}&source_agent=human`)
  assert.equal(request.credentials, 'include')
  assert.equal(request.body, undefined)
})

test('selection snapshot, current and both page shapes carry actor context separately from row filters', async t => {
  const { issues, calls } = await mount(t)
  calls.length = 0
  await issues.saveIssueSelection({ kind: 'none', domain: 'issues', version: 0 })
  await issues.saveIssueSelection({ ...explicit, kind: 'page', cursor: 'synthetic-cursor' })
  await issues.freezeIssueSelection({ project: selectedProject, sourceProject: issueSourceProject, statuses: ['open'] }, ['8'])
  const current = await issues.currentIssueSelection()
  const page = await issues.loadIssuePage({ sourceProject: issueSourceProject })
  await issues.loadIssuePage({}, page.cursor)
  assert.deepEqual(current.targets, explicit.targets)
  assert.deepEqual(page.targets, explicit.targets)
  const actor = { source_project: selectedProject, source_agent: 'human' }
  assert.deepEqual(calls.map(call => [call.url.pathname, call.body]), [
    ['/api/issues/selection', { selection: { kind: 'none' }, ...actor }],
    ['/api/issues/selection', { selection: { kind: 'page', cursor: 'synthetic-cursor' }, ...actor }],
    ['/api/issues/selection', { selection: { kind: 'frozen_filter', filter: { project: selectedProject, source_project: issueSourceProject, statuses: ['open'] }, excluded_ids: ['8'] }, ...actor }],
    ['/api/issues/selection/current', actor],
    ['/api/issues/selection/page', { filter: { project: selectedProject, source_project: issueSourceProject }, limit: 50, ...actor }],
    ['/api/issues/selection/page', { cursor: 'synthetic-cursor', limit: 50, ...actor }],
  ])
  assert.ok(calls.every(call => call.method === 'POST' && call.credentials === 'include'))
  calls.length = 0
  for (const project of ['all', issueSourceProject, targetProject]) {
    await assert.rejects(() => issues.freezeIssueSelection({ project }, []), TypeError)
    await assert.rejects(() => issues.loadIssuePage({ project }), TypeError)
  }
  assert.deepEqual(calls, [])
})

test('pending selection and catalog errors cannot dispatch human issue actions despite a known UUID', async t => {
  const { context, issues, calls } = await mount(t)
  for (const [state, value] of [
    [context.selectionPending, true],
    [context.selectionError, { message: 'Synthetic selection error' }],
    [context.error, { message: 'Synthetic catalog error' }],
  ]) {
    state.value = value
    calls.length = 0
    for (const mutation of [
      () => issues.acknowledgeIssue(7, selectedProject),
      () => issues.runIssueSelectionOperation(explicit, { kind: 'acknowledge' }, selectedProject),
      () => issues.runIssueSelection([7, 8], { kind: 'labels', labels: ['evidence'] }, selectedProject),
      () => issues.updateIssue(7, { status: 'resolved' }),
      () => issues.deleteIssue(7),
      () => issues.saveIssueSelection(explicit),
      () => issues.freezeIssueSelection({}, []),
      () => issues.currentIssueSelection(),
      () => issues.loadIssuePage({}),
      () => issues.loadIssuePage({}, 'synthetic-cursor'),
    ]) await assert.rejects(mutation, TypeError)
    assert.deepEqual(calls, [])
    state.value = state === context.selectionPending ? false : null
    await tick()
  }
})

const migratedIssue = {
  id: 7, title: 'Migrated issue', body: 'Preserved historical issue body', status: 'open', priority: 'medium', type: 'task',
  source_project: 'historical-source-slug', target_project: 'historical-target-slug',
  source_project_id: issueSourceProject, target_project_id: targetProject,
  project_origin: { source_project: 'historical-source-slug', target_project: 'historical-target-slug' },
  labels: [], created_at: '2026-10-01T12:00:00Z', updated_at: '2026-10-01T12:00:00Z',
}

for (const project of [issueSourceProject, targetProject]) test(`canonical issue roles: migrated detail accepts its proven ${project === issueSourceProject ? 'source' : 'target'} UUID, not historical slugs`, async t => {
  const state = await mount(t, project)
  state.setResponder(call => call.url.pathname === '/api/issues/7' ? json({ issue: migratedIssue, comments: [] }) : undefined)
  await state.issues.openIssue(7)
  assert.equal(state.issues.detailState.value.kind, 'live', 'Proven canonical role must not be compared with a historical slug')
  assert.equal(state.issues.detail.value.body, migratedIssue.body)
  assert.equal(state.issues.detail.value.sourceProject, issueSourceProject)
  assert.equal(state.issues.detail.value.targetProject, targetProject)
  assert.deepEqual(state.issues.detail.value.projectOrigin, { sourceProject: migratedIssue.source_project, targetProject: migratedIssue.target_project })
  assert.equal(state.calls.at(-1).url.searchParams.get('project'), project)
})

test('canonical issue roles: an unrelated canonical scope cannot borrow a matching raw UUID or stale detail/comments', async t => {
  const state = await mount(t, issueSourceProject)
  await state.issues.openIssue(7)
  assert.equal(state.issues.detailState.value.kind, 'live')
  await state.router.push({ path: '/issues', query: { project: selectedProject } }); await tick()
  const forgedRaw = { ...migratedIssue, source_project: selectedProject, project_origin: { ...migratedIssue.project_origin, source_project: selectedProject } }
  state.setResponder(call => call.url.pathname === '/api/issues/7' ? json({ issue: forgedRaw, comments: [{ id: 901, body: 'Foreign comment must not render' }] }) : undefined)
  await state.issues.openIssue(7)
  assert.equal(state.issues.detailState.value.kind, 'error')
  assert.equal(state.issues.detail.value, null)
  assert.deepEqual(state.issues.comments, [])
})

test('canonical issue roles: a NULL source role remains unresolved even when its raw value equals the canonical target', async t => {
  const state = await mount(t, targetProject)
  const row = { ...migratedIssue, source_project: targetProject, source_project_id: null, project_origin: { ...migratedIssue.project_origin, source_project: targetProject } }
  state.setResponder(call => call.url.pathname === '/api/issues/7' ? json({ issue: row, comments: [] }) : undefined)
  await state.issues.openIssue(7)
  assert.equal(state.issues.detailState.value.kind, 'live')
  assert.equal(state.issues.detail.value.sourceProject, null)
  assert.equal(state.issues.detail.value.sourceDisplayName, '')
  assert.equal(state.issues.detail.value.targetProject, targetProject)
  assert.equal(state.issues.detail.value.projectOrigin.sourceProject, targetProject)
})

test('canonical issue roles: unscoped all-project reading preserves two NULL roles without autoassignment', async t => {
  const state = await mount(t, 'all')
  const row = { ...migratedIssue, source_project_id: null, target_project_id: null }
  state.setResponder(call => call.url.pathname === '/api/issues/7' ? json({ issue: row, comments: [] }) : undefined)
  await state.issues.openIssue(7)
  assert.equal(state.issues.detailState.value.kind, 'live')
  assert.equal(state.issues.detail.value.sourceProject, null)
  assert.equal(state.issues.detail.value.targetProject, null)
  assert.equal(state.calls.at(-1).url.searchParams.has('project'), false)
})

for (const status of [401, 403]) test(`canonical issue roles: real HTTP ${status} never exposes detail or comments`, async t => {
  const state = await mount(t, targetProject)
  state.setResponder(call => call.url.pathname === '/api/issues/7' ? json({ error: 'Denied' }, status) : undefined)
  await state.issues.openIssue(7)
  assert.equal(state.issues.detailState.value.kind, 'error')
  assert.equal(state.issues.detailState.value.error.status, status)
  assert.equal(state.issues.detail.value, null)
  assert.deepEqual(state.issues.comments, [])
})

test('canonical issue roles: incoming list scope does not become outgoing source or an implicit human actor', async t => {
  const state = await mount(t, targetProject)
  const list = state.calls.find(call => call.url.pathname === '/api/issues' && call.method === 'GET')
  assert.equal(list.url.searchParams.get('project'), targetProject)
  assert.equal(list.url.searchParams.has('source_project'), false)
  assert.equal(list.url.searchParams.has('source_agent'), false)
})

test('canonical issue roles: NULL target is not inferred from a source role or raw UUID', async t => {
  const state = await mount(t, issueSourceProject)
  const row = { ...migratedIssue, target_project: issueSourceProject, target_project_id: null, project_origin: { ...migratedIssue.project_origin, target_project: issueSourceProject } }
  state.setResponder(call => call.url.pathname === '/api/issues/7' ? json({ issue: row, comments: [] }) : undefined)
  await state.issues.openIssue(7)
  assert.equal(state.issues.detailState.value.kind, 'live')
  assert.equal(state.issues.detail.value.sourceProject, issueSourceProject)
  assert.equal(state.issues.detail.value.targetProject, null)
  assert.equal(state.issues.detail.value.targetDisplayName, '')
})

for (const fields of [
  { source_project_id: undefined },
  { source_project_id: 'historical-source-slug' },
  { target_project_id: undefined },
  { target_project_id: 'urn:engram:migration:unresolved:not-a-project' },
  { project_origin: undefined },
  { project_origin: { source_project: null, target_project: '' } },
]) test(`canonical issue roles: malformed role/origin ${JSON.stringify(fields)} is a visible refusal, not a raw fallback`, async t => {
  const state = await mount(t, issueSourceProject)
  const row = { ...migratedIssue, ...fields }
  state.setResponder(call => call.url.pathname === '/api/issues/7' ? json({ issue: row, comments: [] }) : call.url.pathname === '/api/issues' ? json({ issues: [row], total: 1 }) : undefined)
  await state.issues.openIssue(7)
  assert.equal(state.issues.detailState.value.kind, 'error')
  assert.equal(state.issues.detailState.value.error.category, 'invalid-response')
  assert.equal(state.issues.detail.value, null)
  assert.deepEqual(state.issues.comments, [])
  await state.issues.refresh()
  assert.equal(state.issues.loadState.value.kind, 'error')
  assert.equal(state.issues.loadState.value.error.category, 'invalid-response')
})
