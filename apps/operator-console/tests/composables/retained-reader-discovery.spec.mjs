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
async function mount(t, { location = '/projects/unresolved', catalogResponse = catalog, catalogStatus = 200, contentStatus = 200, emptyContent = false, language = 'en' } = {}) {
  const calls = [], errors = [], modules = new Map(), storage = new Map()
  let context, reader
  const globals = {
    window: {}, sessionStorage: { getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value) }, useI18n, useRoute: routing.useRoute,
    fetch: async (path, init) => {
      const url = new URL(path, 'http://synthetic.invalid')
      calls.push({ url, method: init.method || 'GET', credentials: init.credentials, headers: init.headers })
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
  return { host, router, context, reader, calls, errors, api: load('useOperatorApi') }
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
