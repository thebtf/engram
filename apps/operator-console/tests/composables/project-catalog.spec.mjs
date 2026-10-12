import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import * as routing from 'vue-router'

const A = '11111111-1111-4111-8111-111111111111'
const B = '22222222-2222-4222-8222-222222222222'
const catalog = {
  items: [{ project_id: A, name: 'Same name', repository_qualifier: 'org/one' }, { project_id: B, name: 'Same name', repository_qualifier: 'org/two' }],
  unresolved: [{ reference: 'opaque-retained', name: 'Retained knowledge', reason: 'Ambiguous membership' }],
}
const tick = async () => { await new Promise(setImmediate); await vue.nextTick() }
function evaluate(file, dependencies) {
  const source = readFileSync(new URL(`../../composables/${file}.ts`, import.meta.url), 'utf8')
  const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} }
  const storage = new Map()
  new Function('require', 'module', 'exports', 'window', 'sessionStorage', code)(dependencies, module, module.exports, {}, { getItem: (key) => storage.get(key), setItem: (key, value) => storage.set(key, value) })
  return module.exports
}
const renderer = vue.createRenderer({
  createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
  setText: () => { }, setElementText: () => { }, patchProp: () => { }, parentNode: () => null, nextSibling: () => null, insert: () => { }, remove: () => { },
})
async function mount(t, location, respond) {
  const calls = []
  const api = {
    operatorFetchJson: async (path) => { const url = new URL(path, 'http://synthetic.invalid'); calls.push(url); return respond(url) },
    toOperatorSourceError: (cause, fallback) => ({ ...fallback, message: cause.message || 'fixture failure', retryable: true, category: 'transport', status: cause.status }),
  }
  const dependencies = (name) => name === 'vue' ? vue : name === 'vue-router' ? routing : api
  const core = evaluate('useOperatorProjectContext', dependencies)
  const unresolved = evaluate('useOperatorUnresolved', (name) => name === './useOperatorProjectContext' ? core : dependencies(name))
  const router = routing.createRouter({ history: routing.createMemoryHistory(), routes: [...['/', '/memory', '/documents', '/projects', '/projects/unresolved', '/code'].map((path) => ({ path, component: { render: () => null } })), { name: 'issues', path: '/issues/:id(\\d+)?', component: { render: () => null } }] })
  await router.push(location)
  let context, reader
  const app = renderer.createApp({ setup() { context = core.useOperatorProjectContext(); reader = unresolved.useOperatorUnresolved(); return () => null } })
  app.use(router).mount({})
  t.after(() => app.unmount())
  await tick()
  return { core, context, reader, router, calls }
}
function usual(url) {
  if (url.pathname === '/api/projects') return catalog
  if (url.pathname === '/api/projects/resolve') {
    const legacy = url.searchParams.get('legacy_id')
    if (legacy === 'old-one') return { status: 'resolved', project_id: A, name: 'Same name' }
    if (legacy === 'old-two') return { status: 'resolved', project_id: B, name: 'Same name' }
    if (legacy === 'ambiguous') return { status: 'unresolved', reference: 'opaque-retained', reason: 'Ambiguous membership' }
    throw Object.assign(new Error('No proven mapping'), { status: 404 })
  }
  throw new Error(`Unexpected fixture request ${url}`)
}

test('exact DTO rejects legacy shapes and duplicate IDs, but keeps equal names distinct', async (t) => {
  const { core, context } = await mount(t, '/', usual)
  assert.deepEqual(context.projects.value, [A, B])
  assert.equal(core.projectName(A), 'Same name · org/one')
  assert.equal(core.projectName(B), 'Same name · org/two')
  assert.equal(context.isCatalogProject('opaque-retained'), false)
  assert.throws(() => core.parseProjectCatalog({ items: [{ id: 'old-one', display_name: 'Same name' }] }), TypeError)
  assert.throws(() => core.parseProjectCatalog({ items: [catalog.items[0], catalog.items[0]], unresolved: [] }), TypeError)
  assert.throws(() => core.parseProjectCatalog({ items: [{ project_id: 'Same name', name: 'Same name' }], unresolved: [] }), TypeError)
})

test('equal primary names remain visibly distinct with missing or repeated repository qualifiers, independent of catalog order', async (t) => {
  const { core, context, router } = await mount(t, '/', (url) => url.pathname === '/api/projects' ? { items: [{ project_id: B, name: 'Same name' }, { project_id: A, name: 'Same name' }], unresolved: [] } : usual(url))
  assert.equal(core.projectName(A), 'Same name #1')
  assert.equal(core.projectName(B), 'Same name #2')
  await context.select(B); await tick()
  assert.equal(router.currentRoute.value.query.project, B)
  assert.equal(context.label.value, 'Same name #2')
  const repeated = catalog.items.map((item) => ({ ...item, repository_qualifier: 'org/shared' }))
  assert.equal(core.catalogProjectName(A, [...repeated].reverse()), 'Same name · org/shared #1')
  assert.equal(core.catalogProjectName(B, repeated), 'Same name · org/shared #2')
  const mixed = [{ project_id: B, name: 'Same name', repository_qualifier: 'org/two' }, { project_id: A, name: 'Same name' }]
  assert.equal(core.catalogProjectName(A, mixed), 'Same name')
  assert.equal(core.catalogProjectName(B, mixed), 'Same name · org/two')
})

test('legacy deep links translate scope and anchors without losing content or navigation intent', async (t) => {
  const { context, router, calls } = await mount(t, '/memory?project=all&memoryProject=old-one&memory=501&q=known&filter=low&filter=chain&from=search&searchQuery=original', usual)
  await tick()
  assert.equal(context.project.value, 'all')
  assert.equal(router.currentRoute.value.query.memoryProject, A)
  assert.equal(router.currentRoute.value.query.memory, '501')
  assert.deepEqual(router.currentRoute.value.query.filter, ['low', 'chain'])
  assert.equal(router.currentRoute.value.query.searchQuery, 'original')
  assert.equal(calls.filter((url) => url.pathname === '/api/memories').length, 0)
  await router.push(context.to('/documents')); await tick()
  const back = context.to('/memory')
  assert.equal(back.query.memoryProject, A)
  assert.equal(back.query.memory, '501')
  assert.equal(back.query.q, 'known')
  await router.push('/documents?project=old-one&docProject=old-two&path=notes.md&version=3&compare=2&docType=%FF')
  await tick(); await tick()
  const query = router.currentRoute.value.query
  assert.equal(query.project, A)
  assert.equal(query.docProject, B)
  assert.equal(query.path, 'notes.md')
  assert.equal(query.version, '3')
  assert.equal(query.compare, '2')
  assert.equal(query.docType, null)
})

test('unknown and unresolved values never widen into a project or all-project read', async (t) => {
  const { core, context, router, calls } = await mount(t, '/memory?project=unknown&memory=501', usual)
  await tick()
  assert.equal(context.project.value, '')
  assert.ok(context.selectionError.value)
  await assert.rejects(core.resolveCatalogProject('unknown'))
  await assert.rejects(core.loadKnowledgePage('/api/memories', 'unknown', new URLSearchParams({ limit: '20' })))
  assert.equal(calls.filter((url) => url.pathname === '/api/memories').length, 0)
  await router.push('/memory?project=ambiguous&memory=501&q=original'); await tick(); await tick()
  assert.equal(router.currentRoute.value.path, '/projects/unresolved')
  assert.equal(router.currentRoute.value.query.reference, 'opaque-retained')
  assert.equal(router.currentRoute.value.query.memory, '501')
  assert.equal(router.currentRoute.value.query.originalLocation, '/memory?project=ambiguous&memory=501&q=original')
  assert.equal(router.currentRoute.value.query.returnTo, '/projects')
  assert.equal(context.project.value, '')
  assert.equal(await core.resolveCatalogProject('ambiguous'), null)
})

test('an unresolved legacy memory keeps its original identity and provenance while Back exits to the catalog', async (t) => {
  const original = '/memory?project=ambiguous&memory=501&q=retained&filter=low&filter=chain#original'
  const { context, reader, router, calls } = await mount(t, original, (url) => {
    if (url.pathname.endsWith('/content')) return { reference: 'opaque-retained', kind: 'memory', items: [{ id: 501, content: 'Original memory', source_project: 'ambiguous' }], next_cursor: null, provenance: { legacy_id: 'ambiguous' } }
    if (url.pathname.endsWith('/history')) return { reference: 'opaque-retained', kind: 'memory', id: '501', items: [{ id: 991, action: 'memory.store', author_project: 'ambiguous' }], next_cursor: null }
    return usual(url)
  })
  await tick(); await tick()
  assert.equal(router.currentRoute.value.path, '/projects/unresolved')
  assert.equal(reader.originalLocation.value, original)
  assert.equal(reader.selected.value.id, '501')
  assert.equal(reader.history.value.id, '501')
  assert.deepEqual(reader.content.value.provenance, [{ label: 'legacy_id', value: 'ambiguous' }])
  assert.equal(router.currentRoute.value.query.project, 'ambiguous')
  assert.equal(router.currentRoute.value.query.q, 'retained')
  assert.deepEqual(router.currentRoute.value.query.filter, ['low', 'chain'])
  assert.equal(router.currentRoute.value.hash, '#original')
  assert.equal(context.project.value, '')
  const retainedLocation = router.currentRoute.value.fullPath
  const resolutions = calls.filter((url) => url.pathname === '/api/projects/resolve').length
  await router.push(reader.returnTo.value); await tick(); await tick()
  assert.equal(router.currentRoute.value.path, '/projects')
  assert.equal(calls.filter((url) => url.pathname === '/api/projects/resolve').length, resolutions)
  assert.equal(router.resolve(retainedLocation).query.originalLocation, original)
})

test('Back returns to a distinct known pre-alias route without discarding retained document path/version/hash', async (t) => {
  const previous = `/memory?project=${A}&memory=501&q=known#previous`
  const { reader, router, context } = await mount(t, previous, (url) => {
    if (url.pathname.endsWith('/content')) return { reference: 'opaque-retained', kind: 'document', items: [{ id: 901, path: 'notes.md', version: 3, content: 'Original document' }], next_cursor: null, provenance: {} }
    if (url.pathname.endsWith('/history')) return { reference: 'opaque-retained', kind: 'document', id: '901', items: [{ id: 991, version: 3, content: 'Original version' }], next_cursor: null }
    return usual(url)
  })
  const original = '/documents?project=ambiguous&path=notes.md&version=3&compare=2&q=retained&filter=low#version'
  await router.push(original); await tick(); await tick()
  assert.equal(reader.returnTo.value, previous)
  assert.equal(reader.originalLocation.value, original)
  assert.equal(reader.selected.value.id, '901')
  assert.equal(reader.history.value.id, '901')
  assert.equal(router.currentRoute.value.query.path, 'notes.md')
  assert.equal(router.currentRoute.value.query.version, '3')
  assert.equal(router.currentRoute.value.query.compare, '2')
  assert.equal(router.currentRoute.value.hash, '#version')
  await router.push(reader.returnTo.value); await tick(); await tick()
  assert.equal(router.currentRoute.value.fullPath, previous)
  assert.equal(context.project.value, A)
})

test('incoming Back targets cannot re-enter unresolved, leave the app, or traverse paths', async (t) => {
  const { reader, router } = await mount(t, '/projects/unresolved', usual)
  const invalid = ['/memory?project=ambiguous&memory=501', '/memory?memoryProject=ambiguous', '/documents?docProject=ambiguous', '/memory?principalProject=ambiguous', '/memory?project=unknown', '/memory?project=all&project=all', '/projects/unresolved', '/projects/unresolved/', '//external.invalid/path', 'https://external.invalid/path', '/memory/../projects', '/%2e%2e/projects', '/%252e%252e/projects', '/projects\\unresolved', '/unknown-route', '/projects\n']
  for (const returnTo of invalid) {
    await router.push({ path: '/projects/unresolved', query: { returnTo } }); await tick()
    assert.equal(reader.returnTo.value, '/projects', returnTo)
    assert.equal(reader.originalLocation.value, returnTo)
    await router.push(reader.returnTo.value); await tick(); await tick()
    assert.equal(router.currentRoute.value.path, '/projects', returnTo)
  }
  const safe = `/memory?project=${B}&memory=501&q=known#previous`
  await router.push({ path: '/projects/unresolved', query: { returnTo: safe } }); await tick()
  assert.equal(reader.returnTo.value, safe)
  await router.push(reader.returnTo.value); await tick(); await tick()
  assert.equal(router.currentRoute.value.fullPath, safe)
})

test('a delayed mapping cannot overwrite a newer canonical route, including changes to the same alias query', async (t) => {
  let release, started
  const begun = new Promise((resolve) => { started = resolve })
  const delayed = new Promise((resolve) => { release = resolve })
  const { context, router } = await mount(t, '/memory?project=slow&memory=501&q=old', async (url) => {
    if (url.searchParams.get('legacy_id') === 'slow') { started(); await delayed; return { status: 'resolved', project_id: A, name: 'Same name' } }
    return usual(url)
  })
  await begun
  assert.equal(context.project.value, '')
  assert.equal(context.selectionPending.value, true)
  await router.push(`/memory?project=${B}&memory=102&q=new`); await tick()
  release(); await tick(); await tick()
  assert.equal(context.project.value, B)
  assert.equal(router.currentRoute.value.query.memory, '102')
  assert.equal(router.currentRoute.value.query.q, 'new')
  assert.equal(context.selectionPending.value, false)
})

test('unresolved content/history are bound to reference, kind and original IDs; stale and failed readers remain errors', async (t) => {
  let release
  const delayed = new Promise((resolve) => { release = resolve })
  const { core, reader, router, calls } = await mount(t, '/projects/unresolved?reference=slow&kind=memory&id=901', async (url) => {
    if (url.pathname === '/api/projects') return catalog
    const reference = url.pathname.split('/')[4]
    const kind = url.searchParams.get('kind')
    if (reference === 'slow') await delayed
    if (reference === 'stale') throw Object.assign(new Error('Unknown retained membership'), { status: 404 })
    if (url.pathname.endsWith('/history')) return { reference, kind, id: url.searchParams.get('id'), items: [{ id: 9901, action: 'memory.store', actor: reference, created_at: '2026-01-01' }], next_cursor: null }
    return { reference, kind, items: [{ id: 901, content: `${reference} retained content` }], next_cursor: null, provenance: { source: reference } }
  })
  await router.push('/projects/unresolved?reference=new&kind=memory&id=901&q=known'); await tick()
  assert.equal(reader.content.value.items[0].content, 'new retained content')
  assert.equal(reader.history.value.id, '901')
  release(); await tick()
  assert.equal(reader.content.value.reference, 'new')
  assert.equal(reader.history.value.reference, 'new')
  assert.ok(calls.every((url) => !url.pathname.includes('/unresolved/') || !url.searchParams.has('project')))
  await router.push('/projects/unresolved?reference=stale&kind=memory'); await tick()
  assert.equal(reader.content.value, null)
  assert.equal(reader.error.value.status, 404)
  assert.throws(() => core.parseProjectResolution({ status: 'resolved', project_id: 'raw-name', name: 'Guess' }), TypeError)
  const parser = evaluate('useOperatorUnresolved', (name) => name === 'vue' ? vue : name === 'vue-router' ? routing : {})
  assert.throws(() => parser.parseUnresolvedContent({ reference: 'other', kind: 'memory', items: [], next_cursor: null, provenance: {} }, 'new', 'memory', 20), TypeError)
  assert.throws(() => parser.parseUnresolvedHistory({ reference: 'new', kind: 'memory', id: '902', items: [], next_cursor: null }, 'new', 'memory', '901', 20), TypeError)
})

test('a later query on the same alias owns the translated deep link', async (t) => {
  let release
  let mappingReads = 0
  const delayed = new Promise((resolve) => { release = resolve })
  const { context, router } = await mount(t, '/memory?project=slow&memory=501&q=old', async (url) => {
    if (url.searchParams.get('legacy_id') === 'slow') {
      mappingReads += 1
      if (mappingReads === 1) await delayed
      return { status: 'resolved', project_id: A, name: 'Same name' }
    }
    return usual(url)
  })
  await router.push('/memory?project=slow&memory=102&q=new'); await tick(); await tick()
  assert.equal(context.project.value, A)
  assert.equal(router.currentRoute.value.query.q, 'new')
  release(); await tick()
  assert.equal(router.currentRoute.value.query.q, 'new')
  assert.equal(router.currentRoute.value.query.memory, '102')
})

test('retained paging/query/history preserve original IDs, and code metadata creates no source authority', async (t) => {
  const { reader, router, calls } = await mount(t, '/projects/unresolved?reference=opaque-retained&kind=memory&q=known&id=901', async (url) => {
    if (url.pathname === '/api/projects') return catalog
    const reference = 'opaque-retained'
    const kind = url.searchParams.get('kind')
    if (url.pathname.endsWith('/code')) return { reference, items: [{ project_id: null, unresolved_reference: reference, source_ref: 'old-source', checkout_ref: 'old-checkout', repository: 'Retained repo', working_copy: 'Retained checkout' }], next_cursor: null }
    if (url.pathname.endsWith('/history')) return { reference, kind, id: '901', items: [{ id: 9901, action: 'memory.store', actor: 'retained-author' }], next_cursor: url.searchParams.has('cursor') ? null : 'older-history' }
    return { reference, kind, items: [{ id: url.searchParams.has('cursor') ? 902 : 901, content: 'Retained original content' }], next_cursor: url.searchParams.has('cursor') ? null : 'next-content', provenance: { legacy_id: 'old-one' } }
  })
  await tick()
  assert.equal(reader.content.value.items[0].id, '901')
  await reader.update({ cursor: reader.content.value.nextCursor, historyCursor: reader.history.value.nextCursor }); await tick()
  const contentRead = calls.filter((url) => url.pathname.endsWith('/content')).at(-1)
  const historyRead = calls.filter((url) => url.pathname.endsWith('/history')).at(-1)
  assert.equal(contentRead.searchParams.get('cursor'), 'next-content')
  assert.equal(contentRead.searchParams.get('q'), 'known')
  assert.equal(historyRead.searchParams.get('cursor'), 'older-history')
  assert.equal(historyRead.searchParams.get('id'), '901')
  await router.push('/projects/unresolved?reference=opaque-retained&kind=code'); await tick()
  assert.equal(reader.content.value.kind, 'code')
  assert.equal(reader.content.value.items[0].title, 'Retained repo')
  assert.equal(reader.history.value, null)
  const codeRead = calls.at(-1)
  assert.ok(codeRead.pathname.endsWith('/code'))
  assert.equal(codeRead.searchParams.has('project'), false)
  assert.equal(codeRead.searchParams.has('kind'), false)
  await router.push('/projects/unresolved?reference=opaque-retained&kind=memory&q=one&q=two'); await tick()
  assert.equal(reader.content.value, null)
  assert.equal(reader.error.value.category, 'invalid-response')
})

test('an unresolved document link does not substitute another version or a same-path record for an explicit ID', async (t) => {
  const { reader, router } = await mount(t, '/projects/unresolved?reference=opaque-retained&kind=document&path=notes.md&version=1&compare=2', async (url) => {
    if (url.pathname === '/api/projects') return catalog
    if (url.pathname.endsWith('/history')) return { reference: 'opaque-retained', kind: 'document', id: url.searchParams.get('id'), items: [], next_cursor: null }
    return { reference: 'opaque-retained', kind: 'document', items: [{ id: 902, path: 'notes.md', version: 2, author: 'author' }, { id: 901, path: 'notes.md', version: 1, author: 'author' }], next_cursor: null, provenance: {} }
  })
  assert.equal(reader.selected.value.id, '901')
  await router.push('/projects/unresolved?reference=opaque-retained&kind=document&path=notes.md&version=3'); await tick()
  assert.equal(reader.selected.value, undefined)
  await router.push('/projects/unresolved?reference=opaque-retained&kind=document&path=notes.md&id=missing'); await tick()
  assert.equal(reader.selected.value, undefined)
})

test('knowledge paging validates server totals, continuation, scope and repeated filters without a legacy array fallback', async (t) => {
  const { core, calls } = await mount(t, '/', (url) => {
    if (url.pathname === '/api/projects') return catalog
    if (url.pathname === '/api/memories') {
      const offset = Number(url.searchParams.get('offset'))
      const limit = Number(url.searchParams.get('limit'))
      return { items: [{ id: 501, project: url.searchParams.get('project') }].slice(offset, offset + limit), total: 1, offset, limit, has_more: false }
    }
    return usual(url)
  })
  const params = new URLSearchParams({ offset: '0', limit: '20', query: 'known' })
  params.append('filter', 'low'); params.append('filter', 'chain')
  const page = await core.loadKnowledgePage('/api/memories', A, params)
  assert.equal(page.items[0].id, 501)
  const read = calls.at(-1)
  assert.equal(read.searchParams.get('project'), A)
  assert.equal(read.searchParams.get('query'), 'known')
  assert.deepEqual(read.searchParams.getAll('filter'), ['low', 'chain'])
  assert.throws(() => core.parseKnowledgePage([], 0, 20), TypeError)
  assert.throws(() => core.parseKnowledgePage({ items: [], total: 1, offset: 0, limit: 20, has_more: true }, 0, 20), TypeError)
  assert.throws(() => core.parseKnowledgePage({ items: [], total: 0, offset: -1, limit: 20, has_more: false }, -1, 20), TypeError)
  const count = calls.length
  await assert.rejects(core.loadKnowledgePage('/api/memories', A, new URLSearchParams({ limit: '0' })), TypeError)
  await assert.rejects(core.loadKnowledgePage('/api/memories', A, new URLSearchParams({ offset: 'NaN' })), TypeError)
  assert.equal(calls.length, count)
})

test('retained code requires the explicit unresolved variant and keeps independent view refs', () => {
  const parser = evaluate('useOperatorUnresolved', (name) => name === 'vue' ? vue : name === 'vue-router' ? routing : {})
  const row = { project_id: null, unresolved_reference: 'opaque-retained', source_ref: 'source', checkout_ref: 'checkout', repository: 'Retained repo' }
  const page = (items) => ({ reference: 'opaque-retained', items, next_cursor: null })
  const parsed = parser.parseUnresolvedCode(page([{ ...row, view_ref: 'view-one' }, { ...row, view_ref: 'view-two' }]), 'opaque-retained', 20)
  assert.deepEqual(parsed.items.map((item) => item.id), ['view-one', 'view-two'])
  for (const invalid of [{ ...row, project_id: undefined }, { ...row, project_id: A }, { ...row, unresolved_reference: undefined }, { ...row, unresolved_reference: 'foreign' }]) {
    assert.throws(() => parser.parseUnresolvedCode(page([invalid]), 'opaque-retained', 20), TypeError)
  }
})

test('unresolved issue and code links retain their real object identity and original return hash', async (t) => {
  const { router, reader } = await mount(t, '/issues/903?project=ambiguous#discussion', (url) => {
    if (url.pathname.endsWith('/history')) return { reference: 'opaque-retained', kind: 'issue', id: '903', items: [{ id: 991, body: 'Retained discussion', author_agent: 'human' }], next_cursor: null }
    if (url.pathname.endsWith('/content')) return { reference: 'opaque-retained', kind: 'issue', items: [{ id: 903, title: 'Retained task', body: 'Retained body' }], next_cursor: null, provenance: {} }
    if (url.pathname.endsWith('/code')) return { reference: 'opaque-retained', items: [{ project_id: null, unresolved_reference: 'opaque-retained', source_ref: 'source', repository: 'Retained repo' }], next_cursor: null }
    return usual(url)
  })
  const listRoute = router.resolve('/issues')
  const detailRoute = router.resolve('/issues/903')
  assert.equal(listRoute.name, 'issues')
  assert.equal(detailRoute.name, 'issues')
  assert.equal(listRoute.matched.length, 1)
  assert.equal(detailRoute.params.id, '903')
  assert.equal(listRoute.matched[0].components.default, detailRoute.matched[0].components.default)
  await tick(); await tick()
  assert.equal(router.currentRoute.value.query.kind, 'issue')
  assert.equal(router.currentRoute.value.query.id, '903')
  assert.equal(reader.history.value.id, '903')
  assert.equal(reader.originalLocation.value, '/issues/903?project=ambiguous#discussion')
  assert.equal(reader.returnTo.value, '/projects')
  await reader.update({ q: 'retained' }); await tick()
  assert.equal(router.currentRoute.value.hash, '#discussion')
  assert.equal(reader.originalLocation.value, '/issues/903?project=ambiguous#discussion')
  await router.push(reader.returnTo.value); await tick(); await tick()
  assert.equal(router.currentRoute.value.path, '/projects')
  await router.push(`/issues?project=${A}&q=known#list`); await tick()
  assert.equal(router.currentRoute.value.name, 'issues')
  assert.equal(Boolean(router.currentRoute.value.params.id), false)
  assert.equal(router.currentRoute.value.query.project, A)
  assert.equal(router.currentRoute.value.query.q, 'known')
  assert.equal(router.currentRoute.value.hash, '#list')
  await router.push(`/issues/903?project=${A}#discussion`); await tick()
  assert.equal(router.currentRoute.value.name, 'issues')
  assert.equal(router.currentRoute.value.params.id, '903')
  assert.equal(router.currentRoute.value.query.project, A)
  assert.equal(router.currentRoute.value.hash, '#discussion')
  await router.push('/code?project=ambiguous#retained'); await tick(); await tick()
  assert.equal(router.currentRoute.value.query.kind, 'code')
  assert.equal(reader.content.value.kind, 'code')
  assert.equal(reader.history.value, null)
})

test('a document path can read its retained history without selecting a different requested version', async (t) => {
  const { reader, calls } = await mount(t, '/projects/unresolved?reference=opaque-retained&kind=document&path=notes.md&version=1&compare=2', (url) => {
    if (url.pathname === '/api/projects') return catalog
    if (url.pathname.endsWith('/history')) return { reference: 'opaque-retained', kind: 'document', id: '902', items: [{ id: 9921, version: 1, content: 'Original version' }, { id: 9922, version: 2, content: 'Current version' }], next_cursor: null }
    return { reference: 'opaque-retained', kind: 'document', items: [{ id: 902, path: 'notes.md', version: 2, content: 'Current version' }], next_cursor: null, provenance: {} }
  })
  await tick()
  assert.equal(reader.selected.value, undefined)
  assert.equal(reader.historyID.value, '902')
  assert.equal(reader.history.value.items[0].content, 'Original version')
  assert.equal(calls.find((url) => url.pathname.endsWith('/history')).searchParams.get('id'), '902')
})
