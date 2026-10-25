import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import * as routing from 'vue-router'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createI18n, useI18n } from 'vue-i18n'

const root = fileURLToPath(new URL('../../', import.meta.url))
const locale = JSON.parse(readFileSync(join(root, 'i18n/locales/en.json'), 'utf8'))
const A = '11111111-1111-4111-8111-111111111111'
const B = '22222222-2222-4222-8222-222222222222'
const kinds = ['memory', 'document', 'issue', 'code']
const opaque = (n) => `urn:engram:migration:unresolved:${n.toString(16).padStart(64, '0')}`
const metadata = Array.from({ length: 6133 }, (_, i) => ({ reference: opaque(i + 1), name: 'Unresolved data owner', reason: 'missing_membership_evidence', reader_kinds: [] }))
const readable = Array.from({ length: 104 }, (_, i) => ({ reference: opaque(i + 10000), name: `${i === 103 ? 'Needle' : 'Available'} ${kinds[i % 4]} group ${i}`, reason: 'unproven_retained_membership', reader_kinds: [kinds[i % 4]] }))
// Exact observed count/first-reader rank, not a copy of private owner payloads.
const catalog = { items: [{ project_id: A, name: 'Canonical A' }, { project_id: B, name: 'Canonical B' }], unresolved: [...metadata.slice(0, 74), ...readable, ...metadata.slice(74)] }
const tick = async () => { await new Promise(setImmediate); await vue.nextTick() }
function node(tag = '', text = '') { return { tag, text, props: {}, children: [], parent: null } }
const renderer = vue.createRenderer({
  createElement: (tag) => node(tag), createText: (text) => node('', text), createComment: () => node(),
  patchProp: (item, key, _previous, value) => { item.props[key] = value },
  setText: (item, value) => { item.text = value }, setElementText: (item, value) => { item.children = [node('', value)] },
  parentNode: (item) => item.parent, nextSibling: (item) => item.parent?.children[item.parent.children.indexOf(item) + 1] || null,
  insert(item, parent, anchor) { if (item.parent) item.parent.children = item.parent.children.filter(child => child !== item); item.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1; if (index < 0) parent.children.push(item); else parent.children.splice(index, 0, item) },
  remove(item) { if (item.parent) item.parent.children = item.parent.children.filter(child => child !== item) },
})
const walk = (item) => [item, ...item.children.flatMap(walk)]
const text = (item) => item.text + item.children.map(text).join(' ')
function evaluate(source, dependency, globals) {
  const module = { exports: {} }
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  new Function('require', 'module', 'exports', ...Object.keys(globals), compiled)(dependency, module, module.exports, ...Object.values(globals))
  return module.exports
}
const descriptor = parse(readFileSync(join(root, 'pages/unresolved.vue'), 'utf8')).descriptor
const script = compileScript(descriptor, { id: 'retained-discovery' })
const template = compileTemplate({ source: descriptor.template.content, filename: 'unresolved.vue', id: 'retained-discovery', isTS: true, compilerOptions: { bindingMetadata: script.bindings } })
assert.deepEqual(template.errors, [])
function payload(reference, kind) {
  const item = kind === 'document' ? { id: 901, path: 'retained.md', content: 'Document retained body', version: 2 }
    : kind === 'issue' ? { id: 901, title: 'Retained issue', body: 'Issue retained body', status: 'open' }
      : kind === 'code' ? { project_id: null, unresolved_reference: reference, source_ref: 'source-opaque', checkout_ref: 'checkout-opaque', view_ref: 'view-opaque', repository: 'Retained repository', working_copy: 'Retained working copy' }
        : { id: 901, content: 'Memory retained body', source_project: 'historical-owner-not-canonical-id' }
  return { reference, ...(kind === 'code' ? {} : { kind }), items: [item], total: 1, next_cursor: null }
}
async function mount(t, { location = '/projects/unresolved', catalogResponse = catalog, catalogStatus = 200, contentStatus = 200, emptyContent = false, language = 'en', respond } = {}) {
  const calls = [], errors = [], modules = new Map(), storage = new Map()
  let context, reader
  const globals = {
    window: {}, sessionStorage: { getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value) }, useI18n, useRoute: routing.useRoute,
    fetch: async (path, init) => {
      const url = new URL(path, 'http://synthetic.invalid')
      calls.push({ url, method: init.method || 'GET', credentials: init.credentials, headers: init.headers })
      if (respond) {
        const response = await respond(url, init)
        if (response !== undefined) return response
      }
      if (url.pathname === '/api/projects') return Response.json(catalogResponse, { status: catalogStatus })
      if (url.pathname.startsWith('/api/projects/unresolved/')) {
        const reference = decodeURIComponent(url.pathname.split('/')[4])
        const group = catalogResponse.unresolved.find(row => row.reference === reference)
        if (!group) return Response.json({ error: 'Unknown retained reference' }, { status: 404 })
        if (contentStatus !== 200) return Response.json({ error: 'Reader denied' }, { status: contentStatus })
        const kind = url.pathname.endsWith('/code') ? 'code' : url.searchParams.get('kind')
        const result = payload(reference, kind)
        return Response.json(emptyContent ? { ...result, items: [], total: 0 } : result)
      }
      throw new Error(`Unexpected request: ${url.pathname}`)
    },
  }
  function load(name) {
    if (!modules.has(name)) modules.set(name, evaluate(readFileSync(join(root, `composables/${name}.ts`), 'utf8'), dependency, globals))
    return modules.get(name)
  }
  function dependency(name) {
    // Only the host DOM directive is substituted; real compiler model updates remain.
    if (name === 'vue') return { ...vue, vModelText: {} }
    if (name === 'vue-router') return routing
    if (name.endsWith('operator-config')) return { operatorConfig: { apiBase: '/api' } }
    const real = load(name.split('/').at(-1))
    if (name.endsWith('useOperatorProjectContext')) return { ...real, useOperatorProjectContext: () => { context = real.useOperatorProjectContext(); return context } }
    if (name.endsWith('useOperatorUnresolved')) return { ...real, useOperatorUnresolved: (catalog) => { reader = real.useOperatorUnresolved(catalog); return reader } }
    return real
  }
  const component = evaluate(script.content, dependency, globals).default
  component.render = evaluate(template.code, dependency, globals).render
  const router = routing.createRouter({ history: routing.createMemoryHistory(), routes: ['/projects/unresolved', '/projects', '/memory', '/documents', '/issues', '/code'].map(path => ({ path, component: { render: () => null } })) })
  await router.push(location)
  const app = renderer.createApp(component)
  app.config.errorHandler = error => errors.push({ name: error.name, message: error.message })
  const messages = JSON.parse(readFileSync(join(root, `i18n/locales/${language}.json`), 'utf8'))
  app.use(router).use(createI18n({ legacy: false, locale: language, messages: { [language]: messages }, missingWarn: false, fallbackWarn: false }))
  const host = node('root')
  app.mount(host)
  t.after(() => app.unmount())
  await tick()
  assert.deepEqual(errors, [], 'Real compiled page, composables and transport must mount without harness errors')
  assert.equal(context.pending.value, false)
  return { host, router, context, reader, calls, errors, api: load('useOperatorApi'), load }
}
function groupLinks(host) { return walk(host).filter(row => row.tag === 'a' && row.props.href && new URL(row.props.href, 'http://synthetic.invalid').pathname === '/projects/unresolved' && new URL(row.props.href, 'http://synthetic.invalid').searchParams.has('reference')) }
const referenceOf = link => new URL(link.props.href, 'http://synthetic.invalid').searchParams.get('reference')
const article = host => walk(host).find(row => row.tag === 'article' && row.props['data-testid']?.startsWith('unresolved-content-'))
const readerCalls = calls => calls.filter(call => call.url.pathname.startsWith('/api/projects/unresolved/'))
async function click(link) { const handlers = Array.isArray(link.props.onClick) ? link.props.onClick : [link.props.onClick]; for (const handler of handlers) await handler({ button: 0, preventDefault() { }, currentTarget: { getAttribute: () => null } }); await tick() }
function assertReadOnly(state) {
  assert.ok(state.calls.every(call => call.method === 'GET' && call.credentials === 'include'))
  assert.ok(state.calls.every(call => !call.headers), 'Fixture must not manufacture identity or authorization headers')
  assert.ok(readerCalls(state.calls).every(call => !call.url.searchParams.has('project') && !call.url.searchParams.has('project_id')))
  assert.deepEqual(state.context.projects.value, [A, B])
}

for (const kind of kinds) test(`retained discovery: ${kind} can be selected and loaded on the first usable group page`, async t => {
  const state = await mount(t)
  const links = groupLinks(state.host)
  const group = readable.find(row => row.reader_kinds.includes(kind))
  const firstPage = links.slice(0, 20)
  const link = firstPage.find(link => referenceOf(link) === group.reference)
  t.diagnostic(`groups=${links.length}; metadata=6133; readerGroups=104; first reader rank=${links.findIndex(link => readable.some(group => group.reference === referenceOf(link))) + 1}`)
  assert.ok(link, `${kind} retained reader must be selectable within the first 20 group choices, not after 74 metadata-only cards`)
  await click(link)
  assert.equal(state.reader.reference.value, group.reference)
  assert.equal(state.reader.kind.value, kind)
  assert.equal(state.reader.error.value, null)
  assert.ok(article(state.host), 'Selection must load real-decoded content through the actual composable')
  assertReadOnly(state)
})

test('retained discovery: initial group choices are bounded and metadata-only rows do not masquerade as readers', async t => {
  const state = await mount(t)
  const links = groupLinks(state.host)
  assert.equal(links.length, 20, 'The initial reader-group page contains exactly 20 choices')
  assert.ok(links.every(link => readable.some(group => group.reference === referenceOf(link))), 'Default discovery lists reader-backed groups, not metadata-only groups')
})

test('retained discovery: total, reader-backed and metadata-only counts remain visible separately', async t => {
  const state = await mount(t)
  const summaries = walk(state.host).filter(row => ['p', 'output'].includes(row.tag) || row.props.role === 'status')
  assert.ok(summaries.some(row => ['6237', '6133', '104'].every(count => text(row).includes(count))), 'Show all 6237 retained groups: 104 reader-backed and 6133 metadata-only; filtering is not migration completion')
})

for (const kind of kinds) test(`retained discovery: selected ${kind} content is not buried after the full catalogue`, async t => {
  const group = readable.find(row => row.reader_kinds.includes(kind))
  const state = await mount(t, { location: { path: '/projects/unresolved', query: { reference: group.reference, returnTo: `/memory?project=${A}` } } })
  assert.equal(state.reader.content.value.kind, kind)
  assert.equal(state.reader.error.value, null)
  assert.ok(article(state.host))
  assertReadOnly(state)
  const flattened = walk(state.host)
  const before = flattened.slice(0, flattened.indexOf(article(state.host)))
  const linksBeforeContent = groupLinks(state.host).filter(link => before.includes(link)).length
  t.diagnostic(`selected=${kind}; decodedItems=${state.reader.content.value.items.length}; groupCardsBeforeContent=${linksBeforeContent}`)
  assert.equal(linksBeforeContent, 0, 'Selected content must precede every group choice')
})

test('retained discovery: group search reaches a later reader without searching retained payloads', async t => {
  const state = await mount(t)
  const input = walk(state.host).find(row => row.tag === 'input' && row.props.type === 'search')
  assert.ok(input, 'Group discovery needs a labelled search before a reference is selected')
  assert.ok(walk(state.host).some(row => row.tag === 'label' && (walk(row).includes(input) || row.props.for === input.props.id)) || input.props['aria-label'] || input.props['aria-labelledby'], 'Discovery search needs an accessible name')
  assert.equal(typeof input.props['onUpdate:modelValue'], 'function')
  input.props['onUpdate:modelValue']('Needle code group 103')
  const form = walk(state.host).find(row => row.tag === 'form' && walk(row).includes(input))
  if (form?.props.onSubmit) await form.props.onSubmit({ preventDefault() { } })
  await tick()
  const links = groupLinks(state.host)
  assert.deepEqual(links.map(referenceOf), [readable.at(-1).reference])
  assert.equal(readerCalls(state.calls).length, 0, 'Group search must not probe payloads or grants')
})

test('retained discovery: reader group pagination is usable without displacing the active reader', async t => {
  const state = await mount(t, { location: { path: '/projects/unresolved', query: { reference: readable[0].reference } } })
  assert.equal(state.reader.content.value.kind, 'memory')
  const retainedBody = text(article(state.host))
  const readsBefore = readerCalls(state.calls).length
  const first = groupLinks(state.host).map(referenceOf)
  const next = walk(state.host).find(row => row.tag === 'button' && !row.props.disabled && /next/i.test(text(row)))
  assert.ok(next, '104 reader groups need an operable next-page control')
  await click(next)
  const second = groupLinks(state.host).map(referenceOf)
  assert.equal(second.length, 20)
  assert.ok(second.every(reference => !first.includes(reference)))
  assert.equal(state.reader.reference.value, readable[0].reference)
  assert.equal(text(article(state.host)), retainedBody)
  assert.equal(readerCalls(state.calls).length, readsBefore, 'Group pagination must not change or reload the active content scope')
})

test('retained discovery: metadata stays countable and explicitly reviewable, not removed', async t => {
  const state = await mount(t)
  const review = walk(state.host).find(row => ['a', 'button', 'summary'].includes(row.tag) && /metadata/i.test(text(row)) && text(row).includes('6133'))
  assert.ok(review, 'Keep a deliberate metadata-only review entry with its 6133-group count')
  await click(review)
  const links = groupLinks(state.host)
  assert.equal(links.length, 20, 'Metadata review is paged too')
  assert.ok(links.every(link => metadata.some(group => group.reference === referenceOf(link))))
  assert.equal(readerCalls(state.calls).length, 0)
})

test('invariant: exact opaque references never become project selection or authorization', async t => {
  const state = await mount(t, { location: { path: '/projects/unresolved', query: { reference: readable[0].reference, returnTo: `/memory?project=${A}` } } })
  assert.equal(state.reader.reference.value, readable[0].reference)
  assert.equal(state.context.project.value, '')
  assert.equal(state.context.isCatalogProject(readable[0].reference), false)
  assert.equal(state.reader.returnTo.value, `/memory?project=${A}`)
  await state.context.select(readable[0].reference)
  assert.equal(state.router.currentRoute.value.path, '/projects/unresolved')
  assert.equal(state.router.currentRoute.value.query.project, undefined)
  assertReadOnly(state)
})

test('invariant: empty reader kinds never dispatch content/history or advertise readable content', async t => {
  const state = await mount(t, { location: { path: '/projects/unresolved', query: { reference: metadata[0].reference, kind: 'memory', id: '901' } } })
  assert.equal(state.reader.metadataOnly.value, true)
  assert.deepEqual(state.reader.availableKinds.value, [])
  assert.equal(state.reader.content.value, null)
  assert.equal(state.reader.history.value, null)
  assert.equal(readerCalls(state.calls).length, 0)
  assert.ok(text(state.host).includes(locale.projectCatalog.metadataOnly))
  assert.equal(article(state.host), undefined)
})

for (const status of [401, 403]) test(`invariant: real HTTP ${status} remains a reader refusal, not a 200 body or grant`, async t => {
  const state = await mount(t, { location: { path: '/projects/unresolved', query: { reference: readable[0].reference } }, contentStatus: status })
  assert.equal(state.reader.error.value.status, status)
  assert.equal(state.reader.error.value.category, status === 401 ? 'unauthorized' : 'forbidden')
  assert.equal(state.reader.content.value, null)
  assert.equal(article(state.host), undefined)
  assert.ok(walk(state.host).some(row => row.props['data-testid'] === 'unresolved-error' && row.props.role === 'alert'))
  assert.equal(state.api.operatorUnauthorizedRevision.value > 0, status === 401)
  assertReadOnly(state)
})

test('invariant: empty catalogue and empty accessible content retain honest empty states', async t => {
  const empty = await mount(t, { catalogResponse: { items: [], unresolved: [] } })
  assert.ok(text(empty.host).includes(locale.projectCatalog.noUnresolved))
  assert.equal(groupLinks(empty.host).length, 0)
  const state = await mount(t, { location: { path: '/projects/unresolved', query: { reference: readable[0].reference } }, emptyContent: true })
  assert.equal(state.reader.error.value, null)
  assert.ok(text(state.host).includes(locale.projectCatalog.noContent))
  assert.equal(article(state.host), undefined)
})

test('invariant: catalog failure remains visible and does not trigger retained reads', async t => {
  const state = await mount(t, { catalogStatus: 503 })
  assert.equal(state.context.error.value.status, 503)
  assert.ok(walk(state.host).some(row => row.props.role === 'alert'))
  assert.equal(readerCalls(state.calls).length, 0)
})

test('invariant: unsupported kind preserves the link without silently reading another kind', async t => {
  const state = await mount(t, { location: { path: '/projects/unresolved', query: { reference: readable[1].reference, kind: 'memory' } } })
  assert.equal(state.reader.unsupportedKind.value, true)
  assert.equal(state.reader.kind.value, 'memory')
  assert.equal(state.reader.content.value, null)
  assert.equal(readerCalls(state.calls).length, 0)
  assert.ok(text(state.host).includes(locale.projectCatalog.kindUnavailable))
})

const byTestID = (host, id) => walk(host).find(row => row.props['data-testid'] === id)
async function searchGroups(state, query) {
  byTestID(state.host, 'unresolved-group-search').props['onUpdate:modelValue'](query)
  await tick()
}

for (const mode of ['readers', 'metadata']) test(`retained discovery: every ${mode} group remains reachable through bounded pages`, async t => {
  const state = await mount(t)
  if (mode === 'metadata') await click(byTestID(state.host, 'unresolved-metadata-groups'))
  const expected = mode === 'metadata' ? metadata : readable
  const visited = []
  for (let page = 0; page < Math.ceil(expected.length / 20); page++) {
    const links = groupLinks(state.host)
    assert.equal(links.length, Math.min(20, expected.length - visited.length))
    visited.push(...links.map(referenceOf))
    const next = byTestID(state.host, 'unresolved-groups-next')
    assert.equal(Boolean(next.props.disabled), visited.length === expected.length)
    if (!next.props.disabled) await click(next)
  }
  assert.deepEqual(visited, expected.map(group => group.reference))
  assert.equal(new Set(visited).size, expected.length)
  assert.equal(readerCalls(state.calls).length, 0)
})

for (const language of ['en', 'ru', 'zh']) test(`retained discovery: ${language} counters derive from a different catalog, not observed owner constants`, async t => {
  const smallCatalog = { items: catalog.items, unresolved: [metadata[0], metadata[1], readable[0]] }
  const state = await mount(t, { catalogResponse: smallCatalog, language })
  const messages = JSON.parse(readFileSync(join(root, `i18n/locales/${language}.json`), 'utf8'))
  const expected = messages.projectCatalog.groupCounts.replace('{total}', '3').replace('{readers}', '1').replace('{metadata}', '2')
  assert.equal(text(byTestID(state.host, 'unresolved-group-counts')), expected)
  assert.deepEqual(groupLinks(state.host).map(referenceOf), [readable[0].reference])
  await click(byTestID(state.host, 'unresolved-metadata-groups'))
  assert.deepEqual(groupLinks(state.host).map(referenceOf), metadata.slice(0, 2).map(group => group.reference))
})

test('retained discovery: name, reason and raw/localized kind search reset paging without reloading active content', async t => {
  const state = await mount(t, { location: { path: '/projects/unresolved', query: { reference: readable[0].reference, q: 'retained body', cursor: 'opaque-content-cursor' } } })
  const callsBefore = readerCalls(state.calls).length
  await click(byTestID(state.host, 'unresolved-groups-next'))
  await searchGroups(state, 'Needle code group 103')
  assert.deepEqual(groupLinks(state.host).map(referenceOf), [readable.at(-1).reference])
  assert.equal(byTestID(state.host, 'unresolved-groups-previous').props.disabled, true)
  await searchGroups(state, 'unproven_retained_membership')
  assert.deepEqual(groupLinks(state.host).map(referenceOf), readable.slice(0, 20).map(group => group.reference))
  await searchGroups(state, 'no-such-retained-group')
  assert.equal(groupLinks(state.host).length, 0)
  assert.ok(text(state.host).includes(locale.projectCatalog.noGroupMatches))
  await searchGroups(state, '')
  assert.equal(groupLinks(state.host).length, 20)
  assert.equal(state.router.currentRoute.value.query.q, 'retained body')
  assert.equal(state.router.currentRoute.value.query.cursor, 'opaque-content-cursor')
  assert.equal(readerCalls(state.calls).length, callsBefore)
  const genericGroup = { ...readable[0], name: 'Retained alpha', reason: 'unproven' }
  for (const language of ['en', 'ru', 'zh']) {
    const kindState = await mount(t, { catalogResponse: { items: catalog.items, unresolved: [genericGroup] }, language })
    const messages = JSON.parse(readFileSync(join(root, `i18n/locales/${language}.json`), 'utf8'))
    for (const query of ['memory', messages.projectCatalog.memory]) {
      await searchGroups(kindState, query)
      assert.deepEqual(groupLinks(kindState.host).map(referenceOf), [genericGroup.reference])
    }
    assert.equal(readerCalls(kindState.calls).length, 0)
  }
})

test('retained discovery: selecting another group preserves exact origin and safe return without borrowing content query or kind', async t => {
  const originalLocation = '/memory?project=historical-owner&memory=901#original'
  const returnTo = `/memory?project=${A}`
  const state = await mount(t, { location: { path: '/projects/unresolved', query: { reference: readable[0].reference, kind: 'memory', q: 'old-content-query', cursor: 'old-cursor', returnTo, originalLocation }, hash: '#original' } })
  const link = groupLinks(state.host).find(row => referenceOf(row) === readable[1].reference)
  await click(link)
  assert.equal(state.router.currentRoute.value.query.reference, readable[1].reference)
  assert.equal(state.router.currentRoute.value.query.originalLocation, originalLocation)
  assert.equal(state.router.currentRoute.value.query.returnTo, returnTo)
  assert.equal(state.router.currentRoute.value.hash, '#original')
  assert.equal(state.router.currentRoute.value.query.kind, undefined)
  assert.equal(state.router.currentRoute.value.query.q, undefined)
  assert.equal(state.router.currentRoute.value.query.cursor, undefined)
  assert.equal(state.reader.kind.value, 'document')
  assert.equal(state.reader.originalLocation.value, originalLocation)
  assert.equal(state.reader.returnTo.value, returnTo)
  assertReadOnly(state)
})

test('retained discovery: a metadata-only catalog preserves its review and does not invent readers', async t => {
  const state = await mount(t, { catalogResponse: { items: catalog.items, unresolved: metadata.slice(0, 3) } })
  assert.equal(groupLinks(state.host).length, 0)
  assert.ok(text(state.host).includes(locale.projectCatalog.noReaderGroups))
  await click(byTestID(state.host, 'unresolved-metadata-groups'))
  assert.deepEqual(groupLinks(state.host).map(referenceOf), metadata.slice(0, 3).map(group => group.reference))
  await click(groupLinks(state.host)[0])
  assert.equal(state.reader.metadataOnly.value, true)
  assert.equal(readerCalls(state.calls).length, 0)
})

const historicalScope = 'unresolved_history'
const literalQuery = 'saved %_\\ note'
const discoveryLocation = (kind, extra = {}) => ({ path: '/projects/unresolved', query: { view: 'retained_records', attribution_scope: historicalScope, kind, q: literalQuery, ...extra } })
const savedRecord = (kind, id, reference = opaque(90000)) => ({
  reference, id,
  record: { id, attribution_scope: historicalScope, project: 'synthetic-historical-label', ...(kind === 'document' ? { path: 'same.md', version: id === 901 ? 1 : 2, content: `Saved document ${id}` } : kind === 'issue' ? { title: `Saved issue ${id}`, body: `Saved issue body ${id}`, status: 'open' } : { content: `Saved memory ${id}` }) },
})
const savedPage = (kind, items, next_cursor = null) => ({ view: 'retained_records', attribution_scope: historicalScope, kind, items, next_cursor })
const discoveryCalls = state => state.calls.filter(call => call.url.pathname === '/api/projects' && call.url.searchParams.get('view') === 'retained_records')
function assertDiscoveryRequest(call, kind, cursor) {
  assert.ok(call, 'The compiled page must request the separate historical record projection, not only the group catalog')
  assert.equal(call.url.searchParams.get('attribution_scope'), historicalScope)
  assert.equal(call.url.searchParams.get('kind'), kind)
  assert.equal(call.url.searchParams.get('q'), literalQuery, 'Search characters remain literal and are not replaced with group filters')
  assert.equal(call.url.searchParams.get('cursor'), cursor ?? null)
  assert.ok(Number(call.url.searchParams.get('limit')) > 0 && Number(call.url.searchParams.get('limit')) <= 200)
  assert.ok([...call.url.searchParams.keys()].every(key => ['view', 'attribution_scope', 'kind', 'q', 'limit', 'cursor'].includes(key)))
}
const moreButton = host => walk(host).find(row => row.tag === 'button' && text(row).trim() === locale.projectCatalog.more)

test('historical records C1: declared parser accepts the separate native envelope and empty work pages', async t => {
  const state = await mount(t, { catalogResponse: { items: catalog.items, unresolved: [] } })
  const parser = state.load('useOperatorUnresolved').parseRetainedRecordQuery
  assert.equal(typeof parser, 'function', 'Declared production seam parseRetainedRecordQuery is not implemented')
  for (const kind of ['memory', 'document', 'issue']) {
    const item = savedRecord(kind, 901)
    const page = parser(savedPage(kind, [item], 'work-next'), kind, 20)
    assert.equal(page.items.length, 1)
    assert.equal(page.items[0].reference, item.reference)
    assert.equal(String(page.items[0].id), '901')
    assert.equal(page.nextCursor, 'work-next')
    assert.equal(parser(savedPage(kind, [], 'work-next'), kind, 20).nextCursor, 'work-next')
    assert.equal(parser(savedPage(kind, []), kind, 20).nextCursor, null)
    for (const malformed of [
      { ...savedPage(kind, [item]), view: 'catalog' },
      { ...savedPage(kind, [item]), attribution_scope: 'canonical' },
      savedPage(kind === 'memory' ? 'issue' : 'memory', [item]),
      savedPage(kind, [{ ...item, id: 0 }]),
      savedPage(kind, [{ ...item, record: { ...item.record, id: 902 } }]),
      savedPage(kind, [{ ...item, record: { ...item.record, attribution_scope: 'canonical' } }]),
      savedPage(kind, [{ ...item, record: { ...item.record, project_key: A } }]),
      { ...savedPage(kind, []), next_cursor: undefined },
    ]) assert.throws(() => parser(malformed, kind, 20), TypeError)
  }
})

for (const kind of ['memory', 'document', 'issue']) test(`historical records C1: compiled ${kind} discovery requests and displays native records without group fan-out`, async t => {
  const item = savedRecord(kind, 901)
  const state = await mount(t, {
    location: discoveryLocation(kind), catalogResponse: { items: catalog.items, unresolved: [] },
    respond: url => url.pathname === '/api/projects' && url.searchParams.has('view') ? Response.json(savedPage(kind, [item])) : undefined,
  })
  assertDiscoveryRequest(discoveryCalls(state)[0], kind)
  assert.ok(text(state.host).includes(item.record.content || item.record.body), 'The actual compiled page must display the native record body')
  assert.equal(readerCalls(state.calls).length, 0, 'Discovery must not read every catalog group')
  assert.equal(state.context.unresolved.value.length, 0, 'A record envelope must not replace catalog metadata')
  assertReadOnly(state)
})

test('historical records C1: empty and short cursor pages stay actionable until observed exhaustion', async t => {
  const item = savedRecord('memory', 901)
  const state = await mount(t, {
    location: discoveryLocation('memory'), catalogResponse: { items: catalog.items, unresolved: [] },
    respond: url => {
      if (url.pathname !== '/api/projects' || !url.searchParams.has('view')) return
      const cursor = url.searchParams.get('cursor')
      return Response.json(savedPage('memory', cursor === 'work-1' ? [item] : [], cursor === null ? 'work-1' : cursor === 'work-1' ? 'work-2' : null))
    },
  })
  assertDiscoveryRequest(discoveryCalls(state)[0], 'memory')
  for (const cursor of ['work-1', 'work-2']) {
    assert.equal(walk(state.host).filter(row => row.props.role === 'alert').length, 0)
    assert.ok(!text(state.host).includes(locale.projectCatalog.noContent), 'A work cursor must not be labelled final empty')
    const more = moreButton(state.host)
    assert.ok(more && !more.props.disabled, 'Even an empty/short work page must offer continuation')
    await click(more)
    assertDiscoveryRequest(discoveryCalls(state).at(-1), 'memory', cursor)
    if (cursor === 'work-1') assert.ok(text(state.host).includes(item.record.content))
  }
  assert.equal(moreButton(state.host), undefined, 'Null cursor is observed exhaustion')
  assertReadOnly(state)
})

test('historical records C1: issued document body, history and original results survive navigation without rediscovery', async t => {
  const reference = opaque(90000), foreign = opaque(90001)
  const records = [savedRecord('document', 902), savedRecord('document', 901), savedRecord('document', 903, foreign)]
  let queries = 0
  const state = await mount(t, {
    location: discoveryLocation('document', { cursor: 'current-work' }),
    catalogResponse: { items: catalog.items, unresolved: [reference, foreign].map(reference => ({ reference, name: 'Synthetic document group', reason: 'unproven', reader_kinds: ['document'] })) },
    respond: url => {
      if (url.pathname === '/api/projects' && url.searchParams.has('view')) return ++queries === 1
        ? Response.json(savedPage('document', records, 'later-work'))
        : Response.json({ error: 'Discovery unavailable after issuing results' }, { status: 503 })
      if (!url.pathname.startsWith('/api/projects/unresolved/')) return
      const requested = decodeURIComponent(url.pathname.split('/')[4])
      assert.equal(requested, reference, 'Opening version 1 must not select another group with the same path')
      assert.equal(url.searchParams.get('kind'), 'document')
      assert.equal(url.searchParams.get('id'), '901')
      assert.equal(url.searchParams.get('attribution_scope'), historicalScope)
      assert.ok(!url.searchParams.has('q'), 'Discovery search must not leak into exact readers')
      if (url.pathname.endsWith('/history')) {
        const cursor = url.searchParams.get('cursor')
        assert.ok(cursor === null || cursor === 'history-next', 'Only the history continuation belongs in history requests')
        return Response.json({ reference, kind: 'document', attribution_scope: historicalScope, id: 901, items: (cursor ? records.slice(1, 2) : records.slice(0, 2)).map(row => row.record), next_cursor: cursor ? null : 'history-next' })
      }
      assert.ok(!url.searchParams.has('cursor'), 'Discovery work cursor must not leak into exact body reads')
      return Response.json({ reference, kind: 'document', attribution_scope: historicalScope, items: [records[1].record], next_cursor: null })
    },
  })
  assertDiscoveryRequest(discoveryCalls(state)[0], 'document', 'current-work')
  const original = state.reader.records.value
  const assertResults = () => {
    assert.equal(state.reader.records.value, original, 'Keep the original issued page, not a successful rediscovery echo')
    assert.equal(state.reader.records.value.nextCursor, 'later-work')
    assert.equal(state.reader.recordsError.value, null)
    assert.equal(state.router.currentRoute.value.query.q, literalQuery)
    assert.equal(state.router.currentRoute.value.query.cursor, 'current-work')
    assert.equal(discoveryCalls(state).length, 1, 'Detail/history/return-only navigation must not rediscover')
  }
  const read = async () => {
    const result = walk(state.host).find(row => row.tag === 'article' && text(row).includes('Saved document 901'))
    const open = result && walk(result).find(row => row.tag === 'button' || row.tag === 'a')
    assert.ok(open, 'An issued result must expose an actual reader/history action')
    await click(open)
    t.diagnostic(`issued read: discoveryRequests=${discoveryCalls(state).length}; discoveryStatus=${state.reader.recordsError.value?.status ?? 'ok'}; selected=${state.reader.selected.value?.id ?? 'none'}; history=${state.reader.history.value?.items.map(row => row.id).join(',') ?? 'none'}`)
    assert.equal(state.reader.selected.value?.id, '901', 'The issued exact body stays readable even if discovery would fail')
    assert.equal(state.reader.selected.value?.content, 'Saved document 901')
    assert.ok(text(article(state.host)).includes('Saved document 901'))
    assert.equal(state.reader.history.value?.reference, reference)
    assert.deepEqual(state.reader.history.value?.items.map(row => row.id), ['902', '901'])
    assert.ok(!text(byTestID(state.host, 'unresolved-history')).includes('Saved document 903'))
    assertResults()
  }
  for (const row of records) assert.ok(text(state.host).includes(row.record.content), 'Same-path versions and foreign-group rows must not be deduplicated by path')
  await read()
  assert.equal(state.router.currentRoute.value.query.reference, reference)
  assert.equal(state.router.currentRoute.value.query.id, '901')
  await click(moreButton(byTestID(state.host, 'unresolved-history')))
  assert.equal(state.router.currentRoute.value.query.historyCursor, 'history-next')
  assert.deepEqual(state.reader.history.value?.items.map(row => row.id), ['901'])
  assert.ok(text(byTestID(state.host, 'unresolved-history')).includes('Saved document 901'))
  assert.equal(state.reader.selected.value?.content, 'Saved document 901')
  assertResults()
  state.router.back(); await tick()
  assert.equal(state.router.currentRoute.value.query.historyCursor, undefined)
  assert.deepEqual(state.reader.history.value?.items.map(row => row.id), ['902', '901'])
  assert.equal(state.reader.selected.value?.id, '901')
  assertResults()
  state.router.back(); await tick()
  assert.deepEqual(state.router.currentRoute.value.query, discoveryLocation('document', { cursor: 'current-work' }).query)
  for (const row of records) assert.ok(text(state.host).includes(row.record.content))
  assertResults()
  await read()
  await click(walk(state.host).find(row => row.tag === 'button' && text(row).trim() === locale.projectCatalog.returnResults))
  assert.deepEqual(state.router.currentRoute.value.query, discoveryLocation('document', { cursor: 'current-work' }).query)
  for (const row of records) assert.ok(text(state.host).includes(row.record.content))
  assertResults()
  state.reader.retry(); await tick()
  assert.equal(discoveryCalls(state).length, 2, 'Explicit retry must still perform discovery and expose its genuine failure')
  assert.equal(state.reader.recordsError.value?.status, 503)
  assert.equal(state.reader.records.value, null)
  assertReadOnly(state)
})

test('historical records C1: discovery selectors and retry still refresh while aborted late pages cannot replace current results', async t => {
  let release, slowSignal
  const delayed = new Promise(resolve => { release = resolve })
  t.after(() => release())
  const stale = savedRecord('document', 901), current = savedRecord('document', 902), memory = savedRecord('memory', 903)
  const state = await mount(t, {
    location: discoveryLocation('document'), catalogResponse: { items: catalog.items, unresolved: [] },
    respond: async (url, init) => {
      if (url.pathname !== '/api/projects' || !url.searchParams.has('view')) return
      if (url.searchParams.get('q') === 'slow-query') {
        slowSignal = init.signal
        await delayed
        return Response.json(savedPage('document', [stale]))
      }
      return Response.json(url.searchParams.get('kind') === 'memory' ? savedPage('memory', [memory]) : savedPage('document', [current]))
    },
  })
  const update = async query => {
    const before = discoveryCalls(state).length
    await state.reader.update(query); await tick()
    assert.equal(discoveryCalls(state).length, before + 1, 'Each genuine discovery selector change must issue a fresh request')
  }
  await update({ q: 'slow-query' })
  assert.equal(state.reader.recordsPending.value, true)
  await update({ q: 'current-query' })
  assert.equal(slowSignal.aborted, true, 'A new discovery query must abort its predecessor')
  assert.equal(state.reader.records.value.items[0].content, 'Saved document 902')
  release(); await tick()
  assert.equal(state.reader.records.value.items[0].content, 'Saved document 902', 'An aborted late discovery response must not overwrite newer results')
  await update({ kind: 'memory' })
  assert.ok(text(state.host).includes('Saved memory 903'))
  await update({ cursor: 'real-work-cursor' })
  assert.equal(discoveryCalls(state).at(-1).url.searchParams.get('cursor'), 'real-work-cursor')
  await update({ limit: '5' })
  assert.equal(discoveryCalls(state).at(-1).url.searchParams.get('limit'), '5')
  for (const query of [{ attribution_scope: 'canonical' }, { view: 'catalog' }]) {
    const before = discoveryCalls(state).length
    await state.reader.update(query); await tick()
    assert.equal(discoveryCalls(state).length, before, 'An invalid discovery scope/view must fail before transport')
    assert.equal(state.reader.records.value, null)
    assert.equal(state.reader.recordsError.value?.category, 'invalid-response')
    await update({ attribution_scope: historicalScope, view: 'retained_records' })
    assert.ok(text(state.host).includes('Saved memory 903'))
  }
  const before = discoveryCalls(state).length
  state.reader.retry(); await tick()
  assert.equal(discoveryCalls(state).length, before + 1)
  assert.ok(text(state.host).includes('Saved memory 903'))
  assertReadOnly(state)
})


for (const refusal of [401, 403, 404, 503, 'foreign']) test(`historical records C1: ${refusal} clears an already open historical issue without widening`, async t => {
  const reference = opaque(90000)
  let denied = false
  const state = await mount(t, {
    location: discoveryLocation('issue', { reference, id: '901' }),
    catalogResponse: { items: catalog.items, unresolved: [{ reference, name: 'Synthetic issue group', reason: 'unproven', reader_kinds: ['issue'] }] },
    respond: url => {
      if (url.pathname === '/api/projects' && url.searchParams.has('view')) return Response.json(savedPage('issue', [savedRecord('issue', 901)]))
      if (!url.pathname.startsWith('/api/projects/unresolved/')) return
      if (denied && refusal !== 'foreign') return Response.json({ error: 'Synthetic refusal' }, { status: refusal })
      return Response.json({ reference: denied ? opaque(90001) : reference, kind: 'issue', attribution_scope: historicalScope, ...(url.pathname.endsWith('/history') ? { id: 901 } : {}), items: [{ id: 901, title: 'Saved issue 901', body: 'Saved issue body 901', action: 'issue.update', attribution_scope: historicalScope }], next_cursor: null })
    },
  })
  assert.ok(text(state.host).includes('Saved issue body 901'), 'Establish an actual open body before invalidating it')
  assert.ok(state.reader.history.value, 'Establish actual history before invalidating it')
  denied = true
  state.reader.retry(); await tick()
  assert.equal(state.reader.content.value, null)
  assert.equal(state.reader.history.value, null)
  assert.ok(!text(state.host).includes('Saved issue body 901'), 'Old open body/history must be absent after refusal')
  assert.ok(state.reader.error.value || state.reader.historyError.value)
  assert.ok(readerCalls(state.calls).every(call => decodeURIComponent(call.url.pathname.split('/')[4]) === reference))
  assert.ok(state.calls.every(call => call.url.pathname === '/api/projects' || call.url.pathname.startsWith('/api/projects/unresolved/')), 'No canonical issue/project guess or resolve fallback')
  const reads = readerCalls(state.calls)
  assert.ok(reads.every(call => call.url.searchParams.get('id') === '901' && call.url.searchParams.get('attribution_scope') === historicalScope), 'Every actual body/history read must use the issued historical binding')
  assertReadOnly(state)
})

test('historical records C1: a late historical body cannot overwrite a newer issued binding', async t => {
  const slow = opaque(90000), current = opaque(90001)
  let release
  const delayed = new Promise(resolve => { release = resolve })
  const state = await mount(t, {
    location: discoveryLocation('memory', { reference: slow, id: '901' }),
    catalogResponse: { items: catalog.items, unresolved: [slow, current].map(reference => ({ reference, name: 'Synthetic memory group', reason: 'unproven', reader_kinds: ['memory'] })) },
    respond: async url => {
      if (url.pathname === '/api/projects' && url.searchParams.has('view')) return Response.json(savedPage('memory', [savedRecord('memory', 901, slow), savedRecord('memory', 902, current)]))
      if (!url.pathname.startsWith('/api/projects/unresolved/')) return
      const reference = decodeURIComponent(url.pathname.split('/')[4])
      if (reference === slow) await delayed
      const id = reference === slow ? 901 : 902
      return Response.json({ reference, kind: 'memory', attribution_scope: historicalScope, ...(url.pathname.endsWith('/history') ? { id } : {}), items: [{ id, content: `Historical body ${id}`, action: 'memory.store', attribution_scope: historicalScope }], next_cursor: null })
    },
  })
  await state.router.push(discoveryLocation('memory', { reference: current, id: '902' })); await tick()
  assert.equal(state.reader.content.value?.reference, current)
  assert.equal(state.reader.history.value?.reference, current)
  release(); await tick()
  assert.equal(state.reader.content.value?.reference, current)
  assert.equal(state.reader.history.value?.reference, current)
  assert.ok(text(state.host).includes('Historical body 902'))
  assert.ok(!text(state.host).includes('Historical body 901'))
  assert.ok(readerCalls(state.calls).every(call => call.url.searchParams.get('attribution_scope') === historicalScope), 'Late-response safety must also retain historical attribution on the real requests')
  assertReadOnly(state)
})
