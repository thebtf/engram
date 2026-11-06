import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import * as routing from 'vue-router'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createI18n, useI18n } from 'vue-i18n'

const root = new URL('../../', import.meta.url)
const ids = Array.from({ length: 7 }, (_, index) => `12345678-${String(index + 1).padStart(4, '0')}-4000-8000-${String(index + 1).padStart(12, '0')}`)
const [GENERIC, SAME_A, SAME_B, QUALIFIED, VERSION, HASH, CONTROL] = ids
const catalog = {
  items: [
    { project_id: GENERIC, name: 'Registered project' },
    { project_id: SAME_A, name: 'Equal name' },
    { project_id: SAME_B, name: 'Equal name' },
    { project_id: QUALIFIED, name: 'Only qualified name', repository_qualifier: 'examples/authoritative-repository' },
    { project_id: VERSION, name: 'v9.12.3' },
    { project_id: HASH, name: 'abcdef0123456789' },
    { project_id: CONTROL, name: 'Ordinary project' },
  ],
  unresolved: [17, 0, 5].map((membership_count, index) => ({
    reference: `urn:engram:migration:unresolved:${String(index + 1).padStart(64, '0')}`,
    name: `Synthetic retained group ${index + 1}`, reason: 'unproven_membership', reader_kinds: [],
    families: [{ kind: 'memory', table_name: 'synthetic_memories', membership_count, reader_support: 'metadata_only' }],
  })),
}
const tick = async () => { await new Promise(setImmediate); await vue.nextTick() }
function node(tag = '', text = '') { return { tag, text, props: {}, children: [], parent: null, focus() { }, scrollIntoView() { } } }
const renderer = vue.createRenderer({
  createElement: tag => node(tag), createText: text => node('', text), createComment: () => node(),
  patchProp: (item, key, _previous, value) => { item.props[key] = value },
  setText: (item, value) => { item.text = value }, setElementText: (item, value) => { item.children = [node('', value)] },
  parentNode: item => item.parent, nextSibling: item => item.parent?.children[item.parent.children.indexOf(item) + 1] || null,
  insert(item, parent, anchor) { if (item.parent) item.parent.children = item.parent.children.filter(child => child !== item); item.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1; if (index < 0) parent.children.push(item); else parent.children.splice(index, 0, item) },
  remove(item) { if (item.parent) item.parent.children = item.parent.children.filter(child => child !== item) },
})
const walk = item => [item, ...item.children.flatMap(walk)]
const text = item => item.text + item.children.map(text).join(' ')
const hasClass = (item, name) => String(item.props.class || '').split(/\s+/).includes(name)
function evaluate(source, dependency, globals) {
  const module = { exports: {} }
  const code = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    transformers: {
      before: [context => file => ts.visitNode(file, function visit(item) {
        if (ts.isMetaProperty(item) && item.keywordToken === ts.SyntaxKind.ImportKeyword) return ts.factory.createIdentifier('importMeta')
        return ts.visitEachChild(item, visit, context)
      })]
    },
  }).outputText
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(dependency, module, module.exports, ...Object.values(globals))
  return module.exports
}

async function mount(t, { path = '/projects', language = 'ru', items = catalog.items, unresolved = catalog.unresolved, selected = '', logical_groups, scopedRows, documentFetch, catalogFetch, initialQuery = {}, homeDocuments = false, capturePageState = false, allowSyntheticMutations = false } = {}) {
  const modules = new Map(), storage = new Map(), calls = [], errors = []
  let context
  const globals = {
    computed: vue.computed, ref: vue.ref, watch: vue.watch, nextTick: vue.nextTick, onBeforeUnmount: vue.onBeforeUnmount,
    useI18n, useRoute: routing.useRoute, useRouter: routing.useRouter, importMeta: { env: { DEV: false } },
    useSettingsModal: () => ({ settingsModalOpen: vue.ref(false), settingsModalTab: vue.ref('general'), openSettingsModal() { } }),
    window: { innerWidth: 1200, location: { host: 'synthetic.invalid' }, localStorage: { getItem: () => null }, addEventListener() { }, removeEventListener() { } },
    document: { activeElement: null, body: { style: { overflow: '' } }, querySelector: () => null, querySelectorAll: () => [] },
    CSS: { escape: value => value }, Node: class { }, HTMLElement: class { }, HTMLSelectElement: class { },
    sessionStorage: { getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value) },
    fetch: async (path, init) => {
      const url = new URL(path, 'http://synthetic.invalid')
      calls.push({ url, method: init.method || 'GET', credentials: init.credentials, body: init.body })
      assert.ok((init.method || 'GET') === 'GET' || allowSyntheticMutations, 'Unexpected synthetic mutation')
      if (url.pathname === '/api/projects') return catalogFetch ? catalogFetch() : Response.json({ items, unresolved, ...(logical_groups === undefined ? {} : { logical_groups }) })
      if (url.pathname === '/api/auth/me') return Response.json({ authenticated: true, auth_disabled: false, role: 'viewer', user: { email: 'catalog@example.test' } })
      if (url.pathname === '/api/selfcheck') return Response.json({ overall: 'healthy' })
      if (url.pathname === '/api/stats') return Response.json({ uptime: '1m' })
      if (url.pathname === '/api/stats/vnext') return Response.json({ embedding: { active_memory_count: 0 } })
      if (url.pathname === '/api/sessions/list') return Response.json({ sessions: [], total: 0 })
      const documentResponse = documentFetch?.(url, init)
      if (documentResponse) return documentResponse
      if (['/api/memories', '/api/documents'].includes(url.pathname)) {
        const offset = Number(url.searchParams.get('offset'))
        const limit = Number(url.searchParams.get('limit'))
        const project = url.searchParams.get('project')
        const rows = scopedRows?.[url.pathname]?.[project] || []
        return Response.json({ items: rows.slice(offset, offset + limit).map(row => ({ ...row, project })), total: rows.length, offset, limit, has_more: offset + limit < rows.length })
      }
      throw new Error(`Unexpected synthetic request: ${url}`)
    },
  }
  function load(name) {
    if (!modules.has(name)) modules.set(name, evaluate(readFileSync(new URL(`composables/${name}.ts`, root), 'utf8'), dependency, globals))
    return modules.get(name)
  }
  function dependency(name) {
    if (name === 'vue') return vue
    if (name === 'vue-router') return routing
    if (name.endsWith('operator-config')) return { operatorConfig: { apiBase: '/api' } }
    // Unrelated preferences/navigation are host fixtures; catalog, shell, API and sessions are real.
    if (name.endsWith('useConsolePreferences')) return { density: vue.ref('comfortable'), theme: vue.ref('dark'), startedLoads: new Set() }
    if (name.endsWith('i18n.config')) return { locales: [{ code: language }], setLocale() { } }
    if (name.endsWith('useNav')) return { useNav: () => ({ NAV: vue.ref([]) }) }
    const real = load(name.split('/').at(-1))
    if (name.endsWith('useOperatorProjectContext')) return { ...real, useOperatorProjectContext: () => { context = real.useOperatorProjectContext(); return context } }
    return real
  }
  function component(path) {
    const descriptor = parse(readFileSync(new URL(path, root), 'utf8')).descriptor
    const script = compileScript(descriptor, { id: path })
    // Native DOM input directives are outside this renderer; real click/change handlers remain.
    const template = compileTemplate({ source: descriptor.template.content, filename: path, id: path, isTS: true, compilerOptions: { bindingMetadata: script.bindings, directiveTransforms: { model: () => ({ props: [] }) } } })
    assert.deepEqual(template.errors, [])
    const result = evaluate(script.content, dependency, globals).default
    result.render = evaluate(template.code, dependency, globals).render
    return result
  }
  const layout = component('layouts/default.vue')
  const pagePath = path.replace(/\/+$/, '').toLowerCase() || '/'
  const pageClass = pagePath === '/' ? 'home-page' : pagePath === '/projects' ? 'projects-page' : pagePath === '/documents' ? 'documents-page' : 'inert-page'
  const page = pagePath === '/' ? component('pages/index.vue') : pagePath === '/projects' ? component('pages/projects.vue') : pagePath === '/documents' ? component('pages/documents.vue') : { render: () => vue.h('main', { class: pageClass }) }
  let pageState
  if (capturePageState) {
    const setup = page.setup
    page.setup = (props, setupContext) => { pageState = setup(props, setupContext); return pageState }
  }
  const documentPage = homeDocuments ? component('pages/documents.vue') : null
  const router = routing.createRouter({ history: routing.createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
  await router.push({ path, query: { ...initialQuery, ...(selected ? { project: selected } : {}) } })
  const app = renderer.createApp({ render: () => vue.h(layout, null, { default: () => vue.h(homeDocuments && router.currentRoute.value.path === '/documents' ? documentPage : page) }) })
  app.component('ProjectContext', component('components/ProjectContext.vue'))
  for (const name of ['SettingsModal', 'ProfileModal', 'LoginModal', 'MutationResultNotice', 'HonestyBadge']) app.component(name, { render: () => null })
  const messages = Object.fromEntries(['ru', 'en', 'zh'].map(code => [code, JSON.parse(readFileSync(new URL(`i18n/locales/${code}.json`, root), 'utf8'))]))
  const i18n = createI18n({ legacy: false, locale: language, messages, missingWarn: false, fallbackWarn: false })
  app.use(router).use(i18n)
  app.config.errorHandler = error => errors.push(error.message)
  const host = node('root')
  app.mount(host)
  t.after(() => app.unmount())
  await tick()
  assert.deepEqual(errors, [], 'Actual compiled layout, ProjectContext and page must mount without harness errors')
  assert.equal(context.error.value, null)
  assert.equal(context.pending.value, false)
  assert.ok(walk(host).some(item => hasClass(item, pageClass)), 'Real authenticated layout must render the actual page or inert non-catalog slot')
  assert.ok(walk(host).some(item => item.props['data-testid'] === 'project-context-select'), 'Actual global ProjectContext must be present')
  return { host, router, context, calls, errors, path, globals, i18n, pageState: () => pageState, operatorApi: load('useOperatorApi'), loadKnowledgePage: load('useOperatorProjectContext').loadKnowledgePage }
}
const retainedLinks = host => walk(host).filter(item => item.tag === 'a' && item.props.href && new URL(item.props.href, 'http://synthetic.invalid').pathname === '/projects/unresolved')
const selector = host => walk(host).find(item => item.props['data-testid'] === 'project-context-select')
const options = host => walk(selector(host)).filter(item => item.tag === 'option' && ids.includes(item.props.value))
const pageRows = state => walk(state.host).filter(item => item.tag === 'button' && (state.path === '/projects' ? String(item.props['data-testid'] || '').startsWith('project-row-') : item.parent?.tag === 'li' && hasClass(item.parent.parent, 'project-list')))
async function chooseOption(state, id) {
  const target = Object.assign(new state.globals.HTMLSelectElement(), { value: id })
  selector(state.host).props.onChange({ target })
  await tick()
  assert.equal(state.router.currentRoute.value.query.project, id)
  assert.equal(state.context.project.value, id)
  assert.equal(selector(state.host).props.value, id)
}
async function choices(state, surface) {
  if (surface === 'global selector') return new Map(options(state.host).map(item => [item.props.value, text(item)]))
  const result = new Map()
  for (const row of pageRows(state)) {
    const label = text(row)
    await row.props.onClick()
    await tick()
    const id = state.router.currentRoute.value.query.project
    assert.ok(ids.includes(id), 'Actual page choice must select an unchanged catalog UUID')
    assert.ok(!result.has(id), 'Different page choices must not collapse to one identity')
    result.set(id, label)
    await state.router.push(state.path)
    await tick()
  }
  return result
}
function assertIDHint(label, id) {
  const tokens = label.match(/[0-9a-f][0-9a-f-]{6,}[0-9a-f]/gi) || []
  assert.ok(tokens.some(token => id.includes(token) && !ids.some(other => other !== id && other.includes(token))), `Choice ${id} needs an associated distinguishing UUID hint, not only a name or ordinal: ${label}`)
}

for (const language of ['ru', 'en']) {
  test(`/memory ${language}: global shortcut directly opens retained groups and counts groups, not records`, async t => {
    const state = await mount(t, { path: '/memory', language })
    assert.ok(!walk(state.host).some(item => hasClass(item, 'projects-page')), 'The non-Projects slot must not contribute a false Projects-page link')
    const links = retainedLinks(state.host)
    t.diagnostic(JSON.stringify(links.map(text)))
    assert.equal(links.length, 1, 'Other pages retain exactly one direct /projects/unresolved shortcut in the global layout')
    const label = text(links[0])
    assert.match(label, /\b3\b/, 'Count the three groups, not their 22 member records')
    assert.match(label, language === 'ru' ? /групп/i : /groups?/i, 'Localize the counted group unit')
    assert.doesNotMatch(label, /\b22\b/, 'Do not display the member-record total as the group count')
    assert.doesNotMatch(label, language === 'ru' ? /запис/i : /records?/i, 'A group count is not a record count')
  })
  test(`/memory ${language}: zero retained groups hides the global shortcut and keeps canonical choices`, async t => {
    const state = await mount(t, { path: '/memory', language, unresolved: [] })
    assert.equal(retainedLinks(state.host).length, 0)
    assert.deepEqual(options(state.host).map(item => item.props.value).sort(), [...ids].sort())
    assert.deepEqual([...state.context.projects.value].sort(), [...ids].sort())
  })
  test(`/projects/unresolved ${language}: retained reader has no redundant global self-link`, async t => {
    const state = await mount(t, { path: '/projects/unresolved', language })
    assert.ok(!walk(state.host).some(item => hasClass(item, 'projects-page')), 'The reader uses an inert slot, not the Projects page')
    assert.equal(retainedLinks(state.host).length, 0, 'The retained reader owns its navigation; the global selector must not link to the current reader')
  })
}

for (const language of ['ru', 'en']) {
  test(`/projects/ ${language}: trailing slash keeps one contextual entry on the actual Projects page`, async t => {
    const state = await mount(t, { path: '/projects/', language })
    assert.equal(state.router.currentRoute.value.path, '/projects/', 'Keep the trailing slash on the actual reactive route')
    const page = walk(state.host).find(item => hasClass(item, 'projects-page'))
    assert.ok(page, 'Mount the actual compiled Projects page, not an inert slot')
    assert.equal(retainedLinks(state.host).length, 1, 'The global selector must not duplicate the contextual entry')
    assert.equal(retainedLinks(page).length, 1, 'The retained-groups entry belongs to the actual Projects page')
  })
  test(`/projects/unresolved/ ${language}: trailing slash retains no redundant global self-link`, async t => {
    const state = await mount(t, { path: '/projects/unresolved/', language })
    assert.equal(state.router.currentRoute.value.path, '/projects/unresolved/', 'Keep the trailing slash on the actual reactive route')
    assert.ok(!walk(state.host).some(item => hasClass(item, 'projects-page')), 'The reader uses an inert slot, not the Projects page')
    assert.equal(retainedLinks(state.host).length, 0, 'The global selector must not link to the current retained reader')
  })
}

for (const language of ['ru', 'en']) {
  test(`/PROJECTS/ ${language}: case-insensitive route keeps one contextual entry on the actual Projects page`, async t => {
    const state = await mount(t, { path: '/PROJECTS/', language })
    assert.equal(state.router.currentRoute.value.path, '/PROJECTS/', 'Keep uppercase and the trailing slash on the actual reactive route')
    const page = walk(state.host).find(item => hasClass(item, 'projects-page'))
    assert.ok(page, 'Mount the actual compiled Projects page for the case-insensitive route')
    assert.equal(retainedLinks(page).length, 1, 'The contextual retained-groups entry belongs to the actual Projects page')
    assert.equal(retainedLinks(state.host).length, 1, 'The global selector must not duplicate the contextual entry on an uppercase Projects route')
  })
  test(`/PROJECTS/UNRESOLVED/ ${language}: case-insensitive reader route has no global self-link`, async t => {
    const state = await mount(t, { path: '/PROJECTS/UNRESOLVED/', language })
    assert.equal(state.router.currentRoute.value.path, '/PROJECTS/UNRESOLVED/', 'Keep uppercase and the trailing slash on the actual reactive route')
    assert.ok(!walk(state.host).some(item => hasClass(item, 'projects-page')), 'The reader uses an inert slot, not the Projects page')
    assert.equal(retainedLinks(state.host).length, 0, 'The global selector must not link to the current reader on an uppercase route')
  })
}

for (const path of ['/projects', '/']) {
  for (const language of ['ru', 'en']) {
    test(`${path} ${language}: one retained-group entry across real layout and page`, async t => {
      const state = await mount(t, { path, language })
      const links = retainedLinks(state.host)
      t.diagnostic(JSON.stringify(links.map(text)))
      assert.equal(links.length, 1, 'Show one contextual unresolved entry, not duplicate global and page links')
    })
    test(`${path} ${language}: unresolved count describes groups, not member records`, async t => {
      const state = await mount(t, { path, language })
      const labels = retainedLinks(state.host).map(text)
      assert.ok(labels.length, 'Nonempty retained groups remain discoverable')
      t.diagnostic(JSON.stringify(labels))
      for (const label of labels) {
        assert.match(label, /\b3\b/, 'Count the three groups, not their 22 member records')
        assert.match(label, language === 'ru' ? /групп/i : /groups?/i, 'Name the counted unit without fixing one copy wording')
        assert.doesNotMatch(label, language === 'ru' ? /запис/i : /records?/i, 'A group count is not a record count')
      }
    })
  }
  for (const surface of ['global selector', 'page choices']) {
    for (const language of ['ru', 'en']) {
      test(`${path} ${language} ${surface}: concise hints use catalog-wide case-insensitive uniqueness and keep exact selection`, async t => {
        const rows = [
          { project_id: 'abcdef01-0001-4000-8000-000000000001', name: 'Registered project' },
          { project_id: '23456789-0002-4000-8000-000000000002', name: 'Shared name', repository_qualifier: 'examples/shared' },
          { project_id: '34567890-0003-4000-8000-000000000003', name: 'Shared name', repository_qualifier: 'examples/shared' },
          { project_id: 'DEADBEEF-0004-4000-8000-000000000004', name: 'Collision one' },
          { project_id: 'deadbeef-0005-4000-8000-000000000005', name: 'Collision two' },
          { project_id: 'cafebabe-0006-4000-8000-000000000006', name: 'Catalog-wide collision' },
          { project_id: 'CAFEBABE-0007-4000-8000-000000000007', name: 'Qualified peer', repository_qualifier: 'examples/peer' },
          { project_id: '11111111-0008-4000-8000-000000000008', name: 'Forked name', repository_qualifier: 'examples/first' },
          { project_id: '22222222-0009-4000-8000-000000000009', name: 'Forked name', repository_qualifier: 'examples/second' },
          { project_id: 'f00dcafe-0010-4000-8000-000000000010', name: 'True collision', repository_qualifier: 'examples/same' },
          { project_id: 'F00DCAFE-0011-4000-8000-000000000011', name: 'True collision', repository_qualifier: 'examples/same' },
          { project_id: '33333333-0012-4000-8000-000000000012', name: 'Plain · examples/repo' },
          { project_id: '44444444-0013-4000-8000-000000000013', name: 'Plain', repository_qualifier: 'examples/repo' },
          { project_id: '77777777-0014-4000-8000-000000000014', name: 'Space case' },
          { project_id: '88888888-0015-4000-8000-000000000015', name: 'Space case ' },
          { project_id: '99999999-0016-4000-8000-000000000016', name: 'Alpha' },
          { project_id: 'aaaaaaaa-0017-4000-8000-000000000017', name: 'Alpha' },
          { project_id: 'bbbbbbbb-0018-4000-8000-000000000018', name: 'Alpha · 99999999' },
          { project_id: 'cccccccc-0019-4000-8000-000000000019', name: 'Alpha · 99999999-0016-4000-8000-000000000016' },
        ]
        const expected = [
          'Registered project · abcdef01',
          'Shared name · examples/shared · 23456789',
          'Shared name · examples/shared · 34567890',
          'Collision one',
          'Collision two',
          'Catalog-wide collision',
          'Qualified peer · examples/peer',
          'Forked name · examples/first',
          'Forked name · examples/second',
          `True collision · examples/same · ${rows[9].project_id}`,
          `True collision · examples/same · ${rows[10].project_id}`,
          'Plain · examples/repo · 33333333',
          'Plain · examples/repo · 44444444',
          'Space case · 77777777',
          'Space case  · 88888888',
          `Alpha · ${rows[15].project_id} · ${rows[15].project_id}`,
          'Alpha · aaaaaaaa',
          'Alpha · 99999999',
          'Alpha · 99999999-0016-4000-8000-000000000016',
        ]
        const state = await mount(t, { path, language, items: [...rows].reverse(), unresolved: [] })
        const selectOptions = walk(selector(state.host)).filter(item => item.tag === 'option' && item.props.value !== 'all')
        assert.deepEqual(selectOptions.map(item => item.props.value).filter(Boolean).sort(), rows.map(row => row.project_id).sort(), 'Every full UUID remains an option value')
        const pageChoices = pageRows(state)
        assert.equal(pageChoices.length, rows.length)
        for (const [index, row] of rows.entries()) {
          const option = selectOptions.find(item => item.props.value === row.project_id)
          const button = pageRows(state).find(item => text(item).trim() === expected[index])
          assert.equal(text(option).trim(), expected[index], 'Selector shows the concise hint or full collision fallback with the existing qualifier')
          assert.ok(button, 'Page choices use the identical distinctive presentation')
          if (surface === 'global selector') await chooseOption(state, row.project_id)
          else {
            await button.props.onClick()
            await tick()
            assert.equal(state.router.currentRoute.value.query.project, row.project_id)
            assert.equal(state.context.project.value, row.project_id)
            assert.equal(selector(state.host).props.value, row.project_id)
          }
          const page = walk(state.host).find(item => hasClass(item, path === '/' ? 'home-page' : 'project-content'))
          const heading = path === '/' ? page.children.find(item => item.tag === 'header')?.children.find(item => item.tag === 'h1') : page.children.find(item => item.tag === 'h2')
          assert.ok(heading && text(heading).includes(expected[index]), 'Visible selected heading keeps the same label, not the legacy projectName label')
          assert.ok(walk(page).some(item => item.tag === 'code' && text(item).trim() === row.project_id), 'Full unchanged UUID stays in selected identity disclosure')
          await state.router.push(path)
          await tick()
        }
        assert.deepEqual(state.errors, [])
        assert.ok(!state.calls.some(call => call.url.pathname.includes('/resolve')), 'Presentation never remaps canonical identities')
      })
    }
    test(`${path} RU ${surface}: generic no-qualifier project has a stable identity hint`, async t => {
      const state = await mount(t, { path })
      const labels = await choices(state, surface)
      assert.equal(labels.size, ids.length)
      assertIDHint(labels.get(GENERIC), GENERIC)
    })
    test(`${path} RU ${surface}: whitespace-padded generic name keeps its identity hint`, async t => {
      const state = await mount(t, { path, items: [{ project_id: GENERIC, name: 'Registered project ' }], unresolved: [] })
      const labels = await choices(state, surface)
      assert.equal(labels.size, 1)
      assert.equal(labels.get(GENERIC).trim(), 'Registered project  · 12345678')
      await chooseOption(state, GENERIC)
    })
    test(`${path} RU ${surface}: same-name choices have stable identity hints rather than ordinals alone`, async t => {
      const state = await mount(t, { path })
      const labels = await choices(state, surface)
      assert.notEqual(labels.get(SAME_A), labels.get(SAME_B), 'Keep existing equal-name distinction')
      assertIDHint(labels.get(SAME_A), SAME_A)
      assertIDHint(labels.get(SAME_B), SAME_B)
    })
    test(`${path} RU ${surface}: unique meaningful names need no UUID suffix`, async t => {
      const state = await mount(t, { path })
      const labels = await choices(state, surface)
      for (const id of [VERSION, HASH, CONTROL]) {
        assert.equal(labels.get(id).trim(), catalog.items.find(row => row.project_id === id).name)
        await chooseOption(state, id)
      }
    })
    test(`${path} RU ${surface}: distinct qualifiers disambiguate the same name without UUID suffixes`, async t => {
      const items = [
        { project_id: SAME_A, name: 'Shared repository', repository_qualifier: 'examples/first' },
        { project_id: SAME_B, name: 'Shared repository', repository_qualifier: 'examples/second' },
      ]
      const state = await mount(t, { path, items, unresolved: [] })
      const labels = await choices(state, surface)
      assert.equal(labels.size, 2)
      assert.equal(labels.get(SAME_A).trim(), 'Shared repository · examples/first')
      assert.equal(labels.get(SAME_B).trim(), 'Shared repository · examples/second')
      await chooseOption(state, SAME_A)
      await chooseOption(state, SAME_B)
    })
    test(`${path} RU ${surface}: uniquely named descriptor exposes its repository qualifier`, async t => {
      const state = await mount(t, { path })
      const labels = await choices(state, surface)
      assert.ok(labels.get(QUALIFIED).includes(catalog.items.find(row => row.project_id === QUALIFIED).repository_qualifier), 'Authoritative repository context must not disappear just because the primary name is unique')
    })
  }
  test(`${path}: every project, including version/hash-like names and normal control, keeps its exact selectable ID`, async t => {
    const state = await mount(t, { path })
    assert.deepEqual([...state.context.projects.value].sort(), [...ids].sort())
    assert.deepEqual(options(state.host).map(item => item.props.value).sort(), [...ids].sort())
    const labels = await choices(state, 'page choices')
    assert.deepEqual([...labels.keys()].sort(), [...ids].sort())
    for (const id of [VERSION, HASH, CONTROL]) assert.ok(labels.get(id).includes(catalog.items.find(row => row.project_id === id).name))
    for (const id of ids) await chooseOption(state, id)
    for (const group of catalog.unresolved) {
      assert.equal(state.context.isCatalogProject(group.reference), false)
      assert.ok(!walk(selector(state.host)).some(item => item.tag === 'option' && item.props.value === group.reference))
      const before = state.router.currentRoute.value.fullPath
      await state.context.select(group.reference)
      assert.equal(state.router.currentRoute.value.fullPath, before, 'Unresolved references are not project choices')
    }
    assert.ok(state.calls.every(call => call.method === 'GET' && call.credentials === 'include'))
    assert.ok(!state.calls.some(call => call.url.pathname.includes('/resolve') || call.url.pathname.includes('/unresolved/')), 'Canonical selection must not remap identities or probe retained content')
    assert.deepEqual(state.errors, [])
  })
  test(`${path}: selected-project context retains the full actual UUID`, async t => {
    const state = await mount(t, { path, selected: GENERIC })
    assert.equal(state.context.project.value, GENERIC)
    assert.equal(selector(state.host).props.value, GENERIC)
    const page = walk(state.host).find(item => hasClass(item, path === '/' ? 'home-page' : 'project-content'))
    assert.ok(walk(page).some(item => item.tag === 'code' && text(item).trim() === GENERIC), 'Full UUID remains available in selected-project context, including an identity disclosure')
    assert.deepEqual(state.errors, [])
    const heading = path === '/' ? page.children.find(item => item.tag === 'header')?.children.find(item => item.tag === 'h1') : page.children.find(item => item.tag === 'h2')
    assert.ok(heading, 'Actual selected-project heading must be visible outside the identity disclosure')
    assert.ok(text(heading).includes(catalog.items.find(row => row.project_id === GENERIC).name), 'Selected-project heading keeps its existing project label')
    assertIDHint(text(heading), GENERIC)
  })
  test(`${path}: zero unresolved groups produces no review entry and preserves all project choices`, async t => {
    const state = await mount(t, { path, unresolved: [] })
    assert.equal(retainedLinks(state.host).length, 0)
    assert.deepEqual(options(state.host).map(item => item.props.value).sort(), [...ids].sort())
    assert.equal(pageRows(state).length, ids.length)
  })
}

for (const path of ['/', '/projects']) {
  for (const language of ['ru', 'en', 'zh']) {
    for (const sentinel of ['allProjects', 'chooseProject', 'browseAll']) {
      test(`${path} ${language}: project named like ${sentinel} remains distinct from scope controls`, async t => {
        const messages = JSON.parse(readFileSync(new URL(`i18n/locales/${language}.json`, root), 'utf8'))
        const names = messages.knowledge
        const items = [
          { project_id: GENERIC, name: names[sentinel] },
          { project_id: CONTROL, name: 'Ordinary project' },
        ]
        const state = await mount(t, { path, language, items })
        const rendered = value => value.replace(/[ \t\r\n\f]+/g, ' ').trim()
        const selectOptions = walk(selector(state.host)).filter(item => item.tag === 'option')
        const projectOption = selectOptions.find(item => item.props.value === GENERIC)
        const allOption = selectOptions.find(item => item.props.value === 'all')
        const choose = selectOptions.find(item => item.props.value === '')
        assert.ok(projectOption && allOption && choose)
        assert.ok(Object.hasOwn(choose.props, 'disabled') && choose.props.disabled !== false && choose.props.disabled != null, 'The choose-project option remains disabled')
        assert.equal(rendered(text(allOption)), rendered(names.allProjects))
        assert.equal(rendered(text(choose)), rendered(names.chooseProject))
        const projectLabel = rendered(text(projectOption))
        assert.ok(projectLabel.includes(rendered(names[sentinel])), 'Preserve the actual project name')
        assert.notEqual(projectLabel, rendered(text(allOption)), 'A project choice must not look like all-project scope')
        assert.notEqual(projectLabel, rendered(text(choose)), 'A project choice must not look like the disabled prompt')
        assert.notEqual(projectLabel, rendered(names.browseAll), 'A project choice must not look like the browse-all action')
        for (const group of catalog.unresolved) {
          assert.ok(!selectOptions.some(item => item.props.value === group.reference), 'Retained groups are not project choices')
        }

        const row = pageRows(state).find(item => rendered(text(item)) === projectLabel)
        assert.ok(row, 'The mounted page and global selector present the same project label')
        await row.props.onClick()
        await tick()
        assert.equal(state.router.currentRoute.value.query.project, GENERIC)
        assert.equal(state.context.project.value, GENERIC)
        assert.equal(selector(state.host).props.value, GENERIC)
        const page = walk(state.host).find(item => hasClass(item, path === '/' ? 'home-page' : 'project-content'))
        const heading = path === '/' ? page.children.find(item => item.tag === 'header')?.children.find(item => item.tag === 'h1') : page.children.find(item => item.tag === 'h2')
        assert.equal(rendered(text(heading)), projectLabel, 'The selected page heading agrees with the selector')
        assert.ok(walk(page).some(item => item.tag === 'code' && text(item).trim() === GENERIC), 'The selected identity remains the exact UUID')

        await chooseOption(state, 'all')
        assert.equal(state.context.project.value, 'all', 'The all option selects all-project scope, not the UUID')
        await chooseOption(state, GENERIC)
        await state.router.push(path)
        await tick()
        assert.equal(state.context.project.value, '')
        const browse = walk(state.host).find(item => item.tag === 'button' && hasClass(item, 'btn') && rendered(text(item)) === rendered(names.browseAll))
        assert.ok(browse, 'The mounted page exposes its browse-all action')
        await browse.props.onClick()
        await tick()
        assert.equal(state.router.currentRoute.value.query.project, 'all')
        assert.equal(state.context.project.value, 'all')
        assert.equal(selector(state.host).props.value, 'all')
        assert.deepEqual(state.errors, [])
      })
    }
  }
}

for (const path of ['/', '/projects']) {
  test(`${path}: changing locale updates reserved labels without changing project identity`, async t => {
    const russian = JSON.parse(readFileSync(new URL('i18n/locales/ru.json', root), 'utf8')).knowledge.allProjects
    const state = await mount(t, { path, language: 'ru', items: [{ project_id: GENERIC, name: russian }], unresolved: [], selected: GENERIC })
    for (const language of ['ru', 'en', 'zh', 'ru']) {
      state.i18n.global.locale.value = language
      await tick()
      const option = options(state.host).find(item => item.props.value === GENERIC)
      const label = text(option).trim()
      if (language === 'ru') assert.notEqual(label, russian, 'The active all-project label needs an identity hint')
      else assert.equal(label, russian, 'A name no longer matching the active controls keeps its natural label')
      assert.equal(selector(state.host).props.value, GENERIC)
      assert.equal(state.router.currentRoute.value.query.project, GENERIC)
      assert.equal(state.context.project.value, GENERIC)
      const page = walk(state.host).find(item => hasClass(item, path === '/' ? 'home-page' : 'project-content'))
      const heading = path === '/' ? page.children.find(item => item.tag === 'header')?.children.find(item => item.tag === 'h1') : page.children.find(item => item.tag === 'h2')
      assert.equal(text(heading).trim(), label, 'The mounted page follows the selector after each locale change')
    }
    assert.deepEqual(state.errors, [])
  })
}

const logicalItems = [
  { project_id: SAME_A, name: 'North primary' }, { project_id: SAME_B, name: 'North alias' },
  { project_id: QUALIFIED, name: 'South primary' }, { project_id: CONTROL, name: 'South alias' },
  { project_id: GENERIC, name: 'Independent project' },
]
const logicalGroups = [
  { primary_project_id: SAME_A, alias_project_ids: [SAME_B] },
  { primary_project_id: QUALIFIED, alias_project_ids: [CONTROL] },
]
const realProjectOptions = host => walk(selector(host)).filter(item => item.tag === 'option' && item.props.value && item.props.value !== 'all')

for (const path of ['/', '/projects']) {
  for (const language of ['en', 'ru', 'zh']) {
    test(`${path} ${language}: verified logical groups retain individually selectable UUIDs`, async t => {
      const items = [...logicalItems, { project_id: VERSION, name: 'North second alias' }]
      const logical_groups = [{ primary_project_id: SAME_A, alias_project_ids: [SAME_B, VERSION] }, logicalGroups[1]]
      const state = await mount(t, { path, language, items, unresolved: [], logical_groups })
      const groups = walk(selector(state.host)).filter(item => item.tag === 'optgroup')
      assert.equal(groups.length, 2, 'Each verified logical group has a non-selectable selector heading')
      assert.ok(groups.every(group => typeof group.props.label === 'string' && group.props.label.trim()))
      assert.notEqual(groups[0].props.label, groups[1].props.label, 'Distinct logical groups remain distinguishable')
      for (const group of logical_groups) {
        const expected = [group.primary_project_id, ...group.alias_project_ids]
        assert.ok(groups.some(node => {
          const children = walk(node).filter(item => item.tag === 'option').map(item => item.props.value)
          return children.length === expected.length && expected.every(id => children.includes(id))
        }), 'A group presents its primary and every alias as individual choices')
      }
      const optionRows = realProjectOptions(state.host)
      assert.deepEqual(optionRows.map(item => item.props.value).sort(), items.map(item => item.project_id).sort())
      const page = walk(state.host).find(item => hasClass(item, path === '/' ? 'home-page' : 'projects-page'))
      const headings = walk(page).filter(item => ['h2', 'h3', 'h4', 'legend'].includes(item.tag)).map(item => text(item).trim())
      for (const group of groups) assert.ok(headings.includes(group.props.label), 'The page also shows a non-selectable group heading')
      for (const option of optionRows) {
        const row = walk(page).find(item => item.tag === 'button' && text(item).trim() === text(option).trim())
        assert.ok(row, 'The page offers the same individual project as the selector')
        await row.props.onClick()
        await tick()
        assert.equal(state.router.currentRoute.value.query.project, option.props.value)
        await state.router.push(path)
        await tick()
        await chooseOption(state, option.props.value)
        await state.router.push(path)
        await tick()
      }
      assert.ok(state.calls.filter(call => ['/api/memories', '/api/documents', '/api/sessions/list'].includes(call.url.pathname)).every(call => items.some(item => item.project_id === call.url.searchParams.get('project'))))
      assert.deepEqual(state.errors, [])
    })
  }

  test(`${path}: missing or malformed logical groups preserve flat selectable projects`, async t => {
    for (const logical_groups of [undefined, null, 'invalid', [{ primary_project_id: 'not-a-uuid', alias_project_ids: [SAME_A] }]]) {
      const state = await mount(t, { path, language: 'en', items: logicalItems, unresolved: [], logical_groups })
      assert.deepEqual(realProjectOptions(state.host).map(item => item.props.value).sort(), logicalItems.map(item => item.project_id).sort())
      assert.equal(walk(selector(state.host)).filter(item => item.tag === 'optgroup').length, 0)
      for (const item of logicalItems) await chooseOption(state, item.project_id)
      assert.ok(!state.calls.some(call => call.url.pathname.includes('/resolve') || call.url.pathname.includes('/unresolved/')))
      assert.deepEqual(state.errors, [])
    }
  })

  test(`${path}: conflicting and unknown aliases cannot create authority or hide an independent valid group`, async t => {
    const items = [...logicalItems, { project_id: VERSION, name: 'Independent primary' }, { project_id: HASH, name: 'Independent alias' }]
    const unknown = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
    const logical_groups = [
      { primary_project_id: SAME_A, alias_project_ids: [SAME_B] },
      { primary_project_id: QUALIFIED, alias_project_ids: [SAME_B] },
      { primary_project_id: CONTROL, alias_project_ids: [unknown] },
      { primary_project_id: VERSION, alias_project_ids: [HASH] },
    ]
    const state = await mount(t, { path, language: 'en', items, unresolved: [], logical_groups })
    const groups = walk(selector(state.host)).filter(item => item.tag === 'optgroup')
    const grouped = groups.map(group => walk(group).filter(item => item.tag === 'option').map(item => item.props.value))
    assert.ok(grouped.some(ids => ids.length === 2 && ids.includes(VERSION) && ids.includes(HASH)), 'An unrelated verified group remains available')
    assert.ok(grouped.every(ids => !ids.includes(SAME_B) && !ids.includes(unknown)), 'Ambiguous or unknown aliases must not be asserted as group members')
    const values = realProjectOptions(state.host).map(item => item.props.value)
    assert.deepEqual(values.sort(), items.map(item => item.project_id).sort())
    assert.equal(new Set(values).size, values.length)
    for (const item of items) await chooseOption(state, item.project_id)
    assert.ok(!state.calls.some(call => call.url.searchParams.get('project') === unknown || call.url.pathname.includes('/resolve')))
    assert.deepEqual(state.errors, [])
  })
}

test('Malformed group implicating a known project cannot authorize its otherwise valid group', async t => {
  const logical_groups = [logicalGroups[0], logicalGroups[1], { primary_project_id: SAME_A, alias_project_ids: ['not-a-uuid'] }]
  const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: logicalItems, unresolved: [], logical_groups, documentFetch: lineageResponses({ oldServer: true }), initialQuery: { path: lineagePath, document_id: '101' } })
  const groups = walk(selector(state.host)).filter(item => item.tag === 'optgroup')
  const grouped = groups.map(group => walk(group).filter(item => item.tag === 'option').map(item => item.props.value))
  assert.ok(grouped.some(ids => ids.includes(QUALIFIED) && ids.includes(CONTROL)))
  assert.ok(grouped.every(ids => !ids.includes(SAME_A) && !ids.includes(SAME_B)))
  assert.deepEqual(realProjectOptions(state.host).map(item => item.props.value).sort(), logicalItems.map(item => item.project_id).sort())
  assert.equal(documentReads(state).length, 0, 'Contradictory metadata must not grant lineage capability')
  assert.ok(text(state.host).includes(state.i18n.global.t('projectCatalog.lineageScopeUncertain')))
})

for (const [name, logical_groups] of [
  ['implicated project', [logicalGroups[0], logicalGroups[1], { primary_project_id: SAME_A, alias_project_ids: ['not-a-uuid'] }]],
  ['unparseable metadata', null],
]) {
  test(`Documents ${name} does not fall back to unscoped legacy reads or actions`, async t => {
    const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: logicalItems, unresolved: [], logical_groups, documentFetch: lineageResponses({ oldServer: true }), initialQuery: { path: lineagePath }, capturePageState: true, allowSyntheticMutations: true })
    await tick()
    assert.ok(text(state.host).includes(state.i18n.global.t('projectCatalog.lineageScopeUncertain')))
    assert.equal(documentContent(state), undefined)
    assert.ok(!state.calls.some(call => ['/api/documents/read', '/api/documents/history', '/api/documents/comments'].includes(call.url.pathname)), 'An uncertain scope cannot issue unscoped reads')
    await state.pageState().comment()
    await state.pageState().exportDocument()
    assert.ok(!state.calls.some(call => call.method === 'POST'))
    await state.router.push({ path: '/documents', query: { project: SAME_A } })
    await tick()
    const row = walk(state.host).find(item => item.tag === 'button' && hasClass(item, 'doc-row'))
    assert.ok(row)
    await row.props.onClick()
    await tick()
    assert.equal(state.router.currentRoute.value.query.document_id, '')
    assert.ok(text(state.host).includes(state.i18n.global.t('projectCatalog.lineageScopeUncertain')))
    assert.ok(!state.calls.some(call => ['/api/documents/read', '/api/documents/history', '/api/documents/comments'].includes(call.url.pathname)))
    assert.deepEqual(realProjectOptions(state.host).map(item => item.props.value).sort(), logicalItems.map(item => item.project_id).sort())
  })
  test(`Home ${name} does not create a legacy path-only document link`, async t => {
    const state = await mount(t, { path: '/', language: 'en', selected: SAME_A, items: logicalItems, unresolved: [], logical_groups, documentFetch: lineageResponses({ oldServer: true }), homeDocuments: true })
    const link = walk(state.host).find(item => item.tag === 'a' && item.props.href && new URL(item.props.href, 'http://synthetic.invalid').searchParams.get('path') === lineagePath)
    assert.ok(link)
    const target = new URL(link.props.href, 'http://synthetic.invalid')
    assert.equal(target.searchParams.get('document_id'), '')
    await state.router.push(target.pathname + target.search)
    await tick()
    assert.ok(text(state.host).includes(state.i18n.global.t('projectCatalog.lineageScopeUncertain')))
    assert.ok(!state.calls.some(call => ['/api/documents/read', '/api/documents/history', '/api/documents/comments'].includes(call.url.pathname)))
  })
}

for (const endpoint of ['/api/memories', '/api/documents']) {
  test(`${endpoint}: explicit all paginates a verified logical union without duplicates`, async t => {
    const items = [logicalItems[0], logicalItems[1], logicalItems[4]]
    const logical_groups = [logicalGroups[0]]
    const shared = [{ id: 101, path: 'synthetic-a', content: 'a' }, { id: 102, path: 'synthetic-b', content: 'b' }, { id: 103, path: 'synthetic-c', content: 'c' }]
    const separate = [{ id: 104, path: 'synthetic-d', content: 'd' }]
    const scopedRows = { [endpoint]: { [SAME_A]: shared, [SAME_B]: shared, [GENERIC]: separate } }
    const state = await mount(t, { path: '/', language: 'en', items, unresolved: [], logical_groups, scopedRows })
    const pages = []
    for (const offset of [0, 2, 4]) {
      const page = await state.loadKnowledgePage(endpoint, 'all', new URLSearchParams({ offset: String(offset), limit: '2' }))
      assert.equal(page.total, 4, 'The verified alias union is counted once together with the independent project')
      assert.equal(page.has_more, offset < 2)
      pages.push(page)
    }
    assert.deepEqual(pages.map(page => page.items.length), [2, 2, 0])
    assert.deepEqual(pages.flatMap(page => page.items.map(row => row.id)).sort(), [101, 102, 103, 104])
    assert.equal(new Set(pages.flatMap(page => page.items.map(row => row.id))).size, 4)
    await chooseOption(state, SAME_B)
    const before = state.calls.length
    const alias = await state.loadKnowledgePage(endpoint, SAME_B, new URLSearchParams({ offset: '0', limit: '2' }))
    assert.equal(alias.total, 3)
    assert.ok(alias.items.every(row => row.project === SAME_B))
    const reads = state.calls.slice(before).filter(call => call.url.pathname === endpoint)
    assert.ok(reads.length > 0 && reads.every(call => call.url.searchParams.get('project') === SAME_B), 'Direct alias selection must use the exact alias UUID')
    assert.deepEqual(state.errors, [])
  })
  test(`${endpoint}: explicit all keeps disjoint pre-upgrade readers when grouping is absent`, async t => {
    const items = [logicalItems[0], logicalItems[1], logicalItems[4]]
    const scopedRows = {
      [endpoint]: {
        [SAME_A]: [{ id: 201, path: 'synthetic-one' }],
        [SAME_B]: [{ id: 202, path: 'synthetic-two' }],
        [GENERIC]: [{ id: 203, path: 'synthetic-three' }],
      }
    }
    const state = await mount(t, { path: '/', language: 'en', items, unresolved: [], scopedRows })
    const first = await state.loadKnowledgePage(endpoint, 'all', new URLSearchParams({ offset: '0', limit: '2' }))
    const second = await state.loadKnowledgePage(endpoint, 'all', new URLSearchParams({ offset: '2', limit: '2' }))
    assert.equal(first.total, 3)
    assert.equal(second.total, 3)
    assert.equal(first.has_more, true)
    assert.equal(second.has_more, false)
    assert.deepEqual([...first.items, ...second.items].map(row => row.id).sort(), [201, 202, 203])
    assert.deepEqual(state.errors, [])
  })
}

test('explicit all refuses incomplete logical group metadata without hiding individual choices', async t => {
  for (const logical_groups of [null, 'invalid', [{ primary_project_id: SAME_A, alias_project_ids: [SAME_B] }, { primary_project_id: QUALIFIED, alias_project_ids: [SAME_B] }]]) {
    const state = await mount(t, { path: '/', language: 'en', items: logicalItems, unresolved: [], logical_groups })
    assert.deepEqual(realProjectOptions(state.host).map(item => item.props.value).sort(), logicalItems.map(item => item.project_id).sort())
    await chooseOption(state, SAME_B)
    for (const endpoint of ['/api/memories', '/api/documents']) {
      await assert.rejects(state.loadKnowledgePage(endpoint, 'all', new URLSearchParams({ offset: '0', limit: '2' })), 'Incomplete or conflicting group metadata must not claim a complete all-project total')
    }
  }
})

const lineagePath = 'shared/synthetic.md'
const lineageItems = [
  { id: 101, path: lineagePath, project: SAME_A, version: 2, author: 'Alpha entry', doc_type: 'markdown' },
  { id: 201, path: lineagePath, project: SAME_A, version: 2, author: 'Beta entry', doc_type: 'markdown' },
]
function lineageResponses({ oldServer = false, refusalStatus = 0 } = {}) {
  return url => {
    if (url.pathname === '/api/documents') {
      const rows = oldServer ? lineageItems.slice(0, 1) : lineageItems
      return Response.json({ items: rows, total: rows.length, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
    }
    if (url.pathname === '/api/documents/comments') return Response.json({ comments: [] })
    if (!['/api/documents/read', '/api/documents/history'].includes(url.pathname)) return undefined
    const anchor = url.searchParams.get('document_id')
    if (anchor && (oldServer || refusalStatus)) return Response.json({ error: 'synthetic lineage refusal' }, { status: refusalStatus || 400 })
    if (!anchor && !oldServer && !refusalStatus) return Response.json({ error: 'document lineage ambiguous' }, { status: 409 })
    const id = oldServer || (!anchor && refusalStatus) ? '101' : anchor
    if (!['101', '201'].includes(id) || url.searchParams.get('project') !== SAME_A || url.searchParams.get('path') !== lineagePath) return Response.json({ error: 'synthetic lineage mismatch' }, { status: 404 })
    const kind = id === '101' ? 'Alpha' : 'Beta'
    if (url.pathname === '/api/documents/history') return Response.json({
      path: lineagePath, project: SAME_A, versions: [
        { id: Number(id), version: 2, author: `${kind} version two`, content_hash: `${kind}-2` },
        { id: Number(id) - 1, version: 1, author: `${kind} version one`, content_hash: `${kind}-1` },
      ]
    })
    const version = Number(url.searchParams.get('version') || 2)
    return Response.json({ id: version === 2 ? Number(id) : Number(id) - 1, path: lineagePath, project: SAME_A, version, content: `${kind} body version ${version}`, content_hash: `${kind}-${version}`, author: `${kind} author`, doc_type: 'markdown' })
  }
}
const documentReads = state => state.calls.filter(call => ['/api/documents/read', '/api/documents/history'].includes(call.url.pathname))
const documentContent = state => walk(state.host).find(item => item.props['data-testid'] === 'document-content')

for (const source of ['Documents', 'Home']) {
  test(`${source} distinguishes same-path rows with otherwise identical metadata`, async t => {
    const base = lineageResponses()
    const documentFetch = (url, init) => url.pathname === '/api/documents'
      ? Response.json({ items: lineageItems.map(item => ({ ...item, author: 'Same author' })), total: 2, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
      : base(url, init)
    const state = await mount(t, { path: source === 'Home' ? '/' : '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups: [logicalGroups[0]], documentFetch, homeDocuments: source === 'Home' })
    const rows = walk(state.host).filter(item => source === 'Home'
      ? item.tag === 'a' && item.props.href && new URL(item.props.href, 'http://synthetic.invalid').searchParams.get('path') === lineagePath
      : item.tag === 'button' && hasClass(item, 'doc-row'))
    assert.equal(rows.length, 2)
    const labels = rows.map(row => text(row))
    assert.notEqual(labels[0], labels[1], 'A reader can distinguish the same-path choices')
    assert.ok(labels.some(label => label.includes('101')) && labels.some(label => label.includes('201')), 'The differentiators derive from the issued IDs')
  })
}

for (const [label, id] of [['missing', undefined], ['zero', 0], ['malformed', 'not-an-id']]) {
  for (const source of ['Documents', 'Home']) {
    test(`${source} grouped row with ${label} ID never opens a path-only reader`, async t => {
      const base = lineageResponses()
      const documentFetch = (url, init) => url.pathname === '/api/documents'
        ? Response.json({ items: [{ ...lineageItems[0], id }], total: 1, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
        : base(url, init)
      const state = await mount(t, { path: source === 'Home' ? '/' : '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups: [logicalGroups[0]], documentFetch, homeDocuments: source === 'Home' })
      if (source === 'Home') {
        const link = walk(state.host).find(item => item.tag === 'a' && item.props.href && new URL(item.props.href, 'http://synthetic.invalid').searchParams.get('path') === lineagePath)
        assert.ok(link, 'The row remains visible with an actionable diagnosis')
        const target = new URL(link.props.href, 'http://synthetic.invalid')
        await state.router.push(target.pathname + target.search)
      } else {
        const row = walk(state.host).find(item => item.tag === 'button' && hasClass(item, 'doc-row'))
        assert.ok(row)
        await row.props.onClick()
      }
      await tick()
      assert.equal(documentReads(state).length, 0, 'Invalid list identity must not reach a path-only or ID reader')
      assert.equal(documentContent(state), undefined)
      assert.ok(text(state.host).includes(state.i18n.global.t('projectCatalog.lineageUnsupported')))
      assert.deepEqual(state.errors, [])
    })
  }
}

test('Home on a pre-group server preserves the path-only link', async t => {
  const state = await mount(t, { path: '/', language: 'en', selected: SAME_A, items: [logicalItems[0]], unresolved: [], documentFetch: lineageResponses({ oldServer: true }), homeDocuments: true })
  const link = walk(state.host).find(item => item.tag === 'a' && item.props.href && new URL(item.props.href, 'http://synthetic.invalid').searchParams.get('path') === lineagePath)
  assert.ok(link)
  const target = new URL(link.props.href, 'http://synthetic.invalid')
  assert.equal(target.searchParams.has('document_id'), false)
  await state.router.push(target.pathname + target.search)
  await tick()
  assert.equal(text(documentContent(state)).trim(), 'Alpha body version 2')
  assert.ok(documentReads(state).length && documentReads(state).every(call => !call.url.searchParams.has('document_id')))
})

for (const source of ['all inventory', 'uncertain document']) {
  test(`Documents ${source} retries the catalog and recovers`, async t => {
    let catalogCalls = 0
    const items = logicalItems.slice(0, 2)
    const catalogFetch = () => Response.json({ items, unresolved: [], ...(++catalogCalls === 1 ? { logical_groups: null } : { logical_groups: [logicalGroups[0]] }) })
    const base = lineageResponses()
    const documentFetch = url => {
      if (source !== 'all inventory' || url.pathname !== '/api/documents') return base(url)
      const offset = Number(url.searchParams.get('offset')), limit = Number(url.searchParams.get('limit'))
      return Response.json({ items: lineageItems.slice(offset, offset + limit), total: lineageItems.length, offset, limit, has_more: offset + limit < lineageItems.length })
    }
    const state = await mount(t, { path: '/documents', language: 'en', selected: source === 'all inventory' ? 'all' : SAME_A, items, unresolved: [], catalogFetch, documentFetch, initialQuery: source === 'all inventory' ? { allRead: '1' } : { path: lineagePath, document_id: '101' }, capturePageState: true })
    const retry = walk(state.host).find(item => item.props['data-testid'] === 'retry-project-catalog')
    assert.ok(retry, 'The catalog-specific failure offers its actual recovery action')
    assert.deepEqual(realProjectOptions(state.host).map(item => item.props.value).sort(), items.map(item => item.project_id).sort())
    await retry.props.onClick(); await tick(); await tick()
    assert.equal(catalogCalls, 2)
    if (source === 'all inventory') assert.equal(state.pageState().total.value, 2)
    else assert.equal(text(documentContent(state)).trim(), 'Alpha body version 2')
    assert.deepEqual(state.errors, [])
  })
}

test('Catalog rollback clears same-project document detail and rejects a late grouped response', async t => {
  let grouped = true, releaseCatalog, releaseOld
  const nextCatalog = new Promise(resolve => { releaseCatalog = resolve })
  const oldRead = new Promise(resolve => { releaseOld = resolve })
  let catalogCalls = 0
  const catalogFetch = () => ++catalogCalls === 1 ? Response.json({ items: logicalItems.slice(0, 2), unresolved: [], logical_groups: [logicalGroups[0]] }) : nextCatalog
  const documentFetch = url => {
    if (url.pathname === '/api/documents') return Response.json({ items: lineageItems.slice(0, 1), total: 1, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
    if (url.pathname === '/api/documents/comments') return Response.json({ comments: [] })
    if (url.pathname === '/api/documents/history') return Response.json({ path: lineagePath, project: SAME_A, versions: [{ id: 101, version: 2 }, { id: 100, version: 1 }] })
    if (url.pathname === '/api/documents/read') {
      const version = Number(url.searchParams.get('version') || 2)
      const body = content => Response.json({ id: version === 2 ? 101 : 100, path: lineagePath, project: SAME_A, version, content, content_hash: content })
      if (grouped && version === 1) return oldRead.then(() => body('stale grouped version'))
      return body(grouped ? 'grouped content' : 'legacy content')
    }
  }
  const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: logicalItems.slice(0, 2), unresolved: [], catalogFetch, documentFetch, initialQuery: { path: lineagePath }, capturePageState: true })
  assert.equal(text(documentContent(state)).trim(), 'grouped content')
  await state.router.push({ path: '/documents', query: { project: SAME_A, path: lineagePath, version: '1' } })
  await tick()
  const refresh = state.context.refresh()
  assert.equal(state.context.project.value, '', 'The old scope is synchronously invalidated')
  assert.equal(state.pageState().primaryDocument.value, null)
  assert.equal(state.pageState().history.length, 0)
  assert.equal(state.pageState().comments.length, 0)
  grouped = false
  releaseCatalog(Response.json({ items: logicalItems.slice(0, 2), unresolved: [], logical_groups: [] }))
  await refresh; await tick(); await tick()
  assert.equal(state.context.project.value, SAME_A)
  assert.equal(text(documentContent(state)).trim(), 'legacy content')
  releaseOld()
  await tick(); await tick()
  assert.equal(text(documentContent(state)).trim(), 'legacy content', 'A late grouped response cannot overwrite the settled legacy scope')
  assert.deepEqual(state.errors, [])
})

test('Catalog rollback recomputes explicit all document totals for the settled scopes', async t => {
  let releaseCatalog, catalogCalls = 0
  const nextCatalog = new Promise(resolve => { releaseCatalog = resolve })
  const catalogFetch = () => ++catalogCalls === 1 ? Response.json({ items: logicalItems.slice(0, 2), unresolved: [], logical_groups: [logicalGroups[0]] }) : nextCatalog
  const documentFetch = url => {
    if (url.pathname !== '/api/documents') return undefined
    const id = url.searchParams.get('project')
    const rows = [{ id: id === SAME_A ? 101 : 201, path: id, project: id, version: 1 }]
    return Response.json({ items: rows, total: 1, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
  }
  const state = await mount(t, { path: '/documents', language: 'en', selected: 'all', items: logicalItems.slice(0, 2), unresolved: [], catalogFetch, documentFetch, initialQuery: { allRead: '1' }, capturePageState: true })
  assert.equal(state.pageState().total.value, 1)
  const refresh = state.context.refresh()
  assert.equal(state.pageState().total.value, null, 'Old union total is cleared before the catalog settles')
  releaseCatalog(Response.json({ items: logicalItems.slice(0, 2), unresolved: [], logical_groups: [] }))
  await refresh; await tick(); await tick()
  assert.equal(state.context.project.value, 'all')
  assert.equal(state.pageState().total.value, 2, 'Settled disjoint readers each contribute once')
  assert.deepEqual(state.errors, [])
})

test('Catalog rollback clears and reloads Home for the same canonical project', async t => {
  let grouped = true, releaseCatalog, catalogCalls = 0
  const nextCatalog = new Promise(resolve => { releaseCatalog = resolve })
  const catalogFetch = () => ++catalogCalls === 1 ? Response.json({ items: logicalItems.slice(0, 2), unresolved: [], logical_groups: [logicalGroups[0]] }) : nextCatalog
  const documentFetch = url => {
    if (!['/api/documents', '/api/memories'].includes(url.pathname)) return undefined
    const label = grouped ? 'grouped home row' : 'legacy home row'
    const row = url.pathname === '/api/memories' ? { id: 1, project: SAME_A, content: label } : { id: 101, project: SAME_A, path: label, version: 1 }
    return Response.json({ items: [row], total: 1, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
  }
  const state = await mount(t, { path: '/', language: 'en', selected: SAME_A, items: logicalItems.slice(0, 2), unresolved: [], catalogFetch, documentFetch })
  assert.ok(text(state.host).includes('grouped home row'))
  const refresh = state.context.refresh()
  assert.equal(state.context.project.value, '')
  await tick()
  assert.ok(!text(state.host).includes('grouped home row'))
  grouped = false
  releaseCatalog(Response.json({ items: logicalItems.slice(0, 2), unresolved: [], logical_groups: [] }))
  await refresh; await tick(); await tick()
  assert.equal(state.context.project.value, SAME_A)
  assert.ok(text(state.host).includes('legacy home row'))
  assert.ok(!text(state.host).includes('grouped home row'))
})

test('Documents opens same-path list-issued lineages independently across history, Back and refresh', async t => {
  const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups: [logicalGroups[0]], documentFetch: lineageResponses() })
  for (const [id, kind] of [['101', 'Alpha'], ['201', 'Beta']]) {
    const rows = walk(state.host).filter(item => item.tag === 'button' && hasClass(item, 'doc-row'))
    assert.equal(rows.length, 2, 'Distinct list-issued IDs at the same path remain distinct actionable rows')
    const row = rows.find(item => text(item).includes(`${kind} entry`))
    assert.ok(row)
    const start = state.calls.length
    await row.props.onClick()
    await tick()
    assert.equal(state.router.currentRoute.value.query.document_id, id)
    assert.equal(text(documentContent(state)).trim(), `${kind} body version 2`)
    const currentRows = walk(state.host).filter(item => item.tag === 'button' && hasClass(item, 'doc-row'))
    assert.equal(currentRows.filter(item => item.props['aria-pressed'] === true).length, 1, 'Same-path rows cannot both appear selected')
    assert.ok(text(state.host).includes(`${kind} version one`))
    assert.ok(!text(state.host).includes(`${kind === 'Alpha' ? 'Beta' : 'Alpha'} version one`))
    const historyButton = walk(state.host).find(item => item.tag === 'button' && text(item).includes(`${kind} version one`))
    assert.ok(historyButton)
    await historyButton.props.onClick()
    await tick()
    assert.equal(text(documentContent(state)).trim(), `${kind} body version 1`)
    assert.equal(state.router.currentRoute.value.query.document_id, id)
    const page = walk(state.host).find(item => hasClass(item, 'documents-page'))
    const refresh = walk(page.children.find(item => item.tag === 'header')).find(item => item.tag === 'button')
    await refresh.props.onClick()
    await tick()
    assert.equal(state.router.currentRoute.value.query.document_id, id)
    assert.equal(text(documentContent(state)).trim(), `${kind} body version 1`)
    const reads = state.calls.slice(start).filter(call => ['/api/documents/read', '/api/documents/history'].includes(call.url.pathname))
    assert.ok(reads.some(call => call.url.pathname === '/api/documents/history') && reads.some(call => call.url.pathname === '/api/documents/read' && call.url.searchParams.get('version') === '1'))
    assert.ok(reads.every(call => call.url.searchParams.get('document_id') === id && call.url.searchParams.get('project') === SAME_A && call.url.searchParams.get('path') === lineagePath))
    const back = walk(state.host).find(item => item.tag === 'button' && hasClass(item, 'back'))
    await back.props.onClick()
    await tick()
    assert.equal(state.router.currentRoute.value.query.document_id, undefined)
    state.router.back()
    await tick()
    await tick()
    assert.equal(state.router.currentRoute.value.query.document_id, id, 'Browser Back restores the exact selected lineage')
    assert.equal(text(documentContent(state)).trim(), `${kind} body version 1`)
    const backAgain = walk(state.host).find(item => item.tag === 'button' && hasClass(item, 'back'))
    await backAgain.props.onClick()
    await tick()
  }
  assert.deepEqual(state.errors, [])
})

test('Documents on a pre-group server keeps the existing path-only reader', async t => {
  const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0]], unresolved: [], documentFetch: lineageResponses({ oldServer: true }), initialQuery: { path: lineagePath } })
  await tick()
  assert.equal(text(documentContent(state)).trim(), 'Alpha body version 2')
  assert.ok(text(state.host).includes('Alpha version one'))
  const back = walk(state.host).find(item => item.tag === 'button' && hasClass(item, 'back'))
  await back.props.onClick()
  await tick()
  const row = walk(state.host).find(item => item.tag === 'button' && hasClass(item, 'doc-row'))
  assert.ok(row)
  await row.props.onClick()
  await tick()
  assert.equal(text(documentContent(state)).trim(), 'Alpha body version 2')
  assert.equal(state.router.currentRoute.value.query.document_id, undefined)
  const reads = documentReads(state)
  assert.ok(reads.some(call => call.url.pathname === '/api/documents/read') && reads.some(call => call.url.pathname === '/api/documents/history'))
  assert.ok(reads.every(call => !call.url.searchParams.has('document_id') && call.url.searchParams.get('path') === lineagePath))
  assert.ok(state.calls.some(call => call.url.pathname === '/api/documents/comments' && call.method === 'GET'), 'Legacy ungrouped comments remain readable')
  assert.deepEqual(state.errors, [])
})

for (const [name, status, oldServer] of [['unsupported', 400, true], ['stale', 404, false], ['foreign', 403, false], ['mismatched', 400, false]]) {
  test(`Documents explicit ${name} lineage never retries by path`, async t => {
    const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: oldServer ? [logicalItems[0]] : [logicalItems[0], logicalItems[1]], unresolved: [], ...(oldServer ? {} : { logical_groups: [logicalGroups[0]] }), documentFetch: lineageResponses({ oldServer, refusalStatus: status }), initialQuery: { path: lineagePath, document_id: '999' } })
    await tick()
    const reads = documentReads(state)
    if (oldServer) assert.equal(reads.length, 0, 'An unsupported server may ignore document_id, so do not issue an ambiguous read')
    else {
      assert.ok(reads.length > 0, 'The explicit lineage reaches the supported reader')
      assert.ok(reads.every(call => call.url.searchParams.get('document_id') === '999'), 'A refused lineage must never be retried without its ID')
    }
    assert.equal(documentContent(state), undefined, 'No path-only body may be substituted')
    const reader = walk(state.host).find(item => item.tag === 'article' && hasClass(item, 'reader'))
    assert.ok(reader && walk(reader).some(item => hasClass(item, 'error')), 'The user sees an honest reader error')
    assert.deepEqual(state.errors, [])
  })
}

test('Home recent documents open the exact same-path lineage in Documents', async t => {
  const state = await mount(t, { path: '/', language: 'en', selected: SAME_A, items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups: [logicalGroups[0]], documentFetch: lineageResponses(), homeDocuments: true })
  for (const [id, kind] of [['101', 'Alpha'], ['201', 'Beta']]) {
    const heading = walk(state.host).find(item => item.tag === 'h2' && text(item).trim() === state.i18n.global.t('knowledge.recentDocuments'))
    assert.ok(heading?.parent, 'The Home recent-documents section is present')
    const links = walk(heading.parent).filter(item => item.tag === 'a' && item.props.href && new URL(item.props.href, 'http://synthetic.invalid').searchParams.get('path') === lineagePath)
    assert.equal(links.length, 2, 'Both same-path recent documents remain distinct links')
    const targets = links.map(link => new URL(link.props.href, 'http://synthetic.invalid'))
    assert.deepEqual(targets.map(url => url.searchParams.get('document_id')).sort(), ['101', '201'])
    const target = targets.find(url => url.searchParams.get('document_id') === id)
    assert.equal(target.pathname, '/documents')
    assert.equal(target.searchParams.get('project'), SAME_A)
    await state.router.push(target.pathname + target.search)
    await tick()
    await tick()
    assert.equal(text(documentContent(state)).trim(), `${kind} body version 2`)
    const reads = documentReads(state)
    assert.ok(reads.length && reads.every(call => ['101', '201'].includes(call.url.searchParams.get('document_id'))))
    await state.router.push({ path: '/', query: { project: SAME_A } })
    await tick()
  }
  assert.deepEqual(state.errors, [])
})

test('SCOPED-ACTION Documents switching same-path lineage clears stale detail and targets subsequent actions correctly', async t => {
  let releaseBeta
  const betaReady = new Promise(resolve => { releaseBeta = resolve })
  const responses = lineageResponses()
  const documentFetch = (url, init) => {
    if ((init.method || 'GET') === 'POST') return Response.json({ error: 'synthetic denial' }, { status: 403 })
    if (['/api/documents/read', '/api/documents/history'].includes(url.pathname) && url.searchParams.get('document_id') === '201') return betaReady.then(() => responses(url))
    return responses(url)
  }
  const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups: [logicalGroups[0]], documentFetch, capturePageState: true, allowSyntheticMutations: true })
  const findRow = kind => walk(state.host).find(item => item.tag === 'button' && hasClass(item, 'doc-row') && text(item).includes(`${kind} entry`))
  await findRow('Alpha').props.onClick()
  await tick()
  assert.equal(text(documentContent(state)).trim(), 'Alpha body version 2')
  await findRow('Beta').props.onClick()
  await tick()
  assert.equal(state.router.currentRoute.value.query.document_id, '201')
  assert.ok(!text(state.host).includes('Alpha body version 2'), 'Old content is withheld while the other lineage loads')
  assert.ok(!text(state.host).includes('Alpha version one'), 'Old history cannot remain actionable during a switch')
  assert.ok(!walk(state.host).some(item => item.tag === 'form' && hasClass(item, 'comment-form')), 'Old comment target cannot remain actionable during the switch')
  assert.ok(!walk(state.host).some(item => item.props['data-testid'] === 'document-export' && !item.props.disabled), 'Old export cannot be triggered during the switch')
  releaseBeta()
  await tick()
  await tick()
  assert.equal(text(documentContent(state)).trim(), 'Beta body version 2')
  assert.ok(text(state.host).includes('Beta version one'))
  assert.ok(!text(state.host).includes('Alpha version one'))
  const comments = state.calls.filter(call => call.url.pathname === '/api/documents/comments')
  assert.ok(comments.some(call => call.url.searchParams.get('project') === SAME_A && call.url.searchParams.get('document_id') === '201' && call.url.searchParams.get('expected_version') === '2'), 'Beta comments use the requested project and immutable version')
  assert.ok(!comments.some(call => !call.url.searchParams.has('project') || !call.url.searchParams.has('expected_version')))
  assert.ok(walk(state.host).some(item => item.tag === 'form' && hasClass(item, 'comment-form')))
  state.pageState().commentDraft.value = 'synthetic comment'
  await state.pageState().comment()
  const comment = state.calls.find(call => call.method === 'POST' && call.url.pathname === '/api/documents/comment')
  assert.ok(comment)
  const payload = JSON.parse(comment.body)
  assert.deepEqual({ project: payload.project, document_id: payload.document_id, expected_version: payload.expected_version }, { project: SAME_A, document_id: 201, expected_version: 2 })
  assert.deepEqual(state.errors, [])
})

for (const language of ['en', 'ru', 'zh']) {
  test(`Documents ${language}: path-only ambiguous lineage offers a localized route back to choices`, async t => {
    const base = lineageResponses()
    const wrongSibling = lineageResponses({ oldServer: true })
    const attempts = new Map()
    const documentFetch = url => {
      if (!['/api/documents/read', '/api/documents/history'].includes(url.pathname)) return base(url)
      const count = (attempts.get(url.pathname) || 0) + 1
      attempts.set(url.pathname, count)
      if (count > 1) return wrongSibling(url)
      return Response.json({ error: 'document lineage ambiguous', detail: 'private arbitrary server text' }, { status: 409 })
    }
    const state = await mount(t, { path: '/documents', language, selected: SAME_A, items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups: [logicalGroups[0]], documentFetch, initialQuery: { path: lineagePath } })
    await tick()
    assert.equal(documentContent(state), undefined)
    assert.ok([...attempts.values()].every(count => count === 1), 'Ambiguity must not silently retry the same path and open a sibling')
    const reader = walk(state.host).find(item => item.tag === 'article' && hasClass(item, 'reader'))
    const error = walk(reader).find(item => hasClass(item, 'error'))
    assert.ok(error)
    assert.ok(!text(error).includes(state.i18n.global.t('errors.diagnosis.request-rejected')), 'A known ambiguity needs more than the generic 409 diagnosis')
    assert.ok(!text(error).includes('private arbitrary server text') && !text(error).includes('document lineage ambiguous'), 'Arbitrary server text is not rendered as operator copy')
    const back = walk(reader).find(item => item.tag === 'button' && hasClass(item, 'back'))
    assert.ok(back, 'The user can return to the list and choose a lineage')
    await back.props.onClick()
    await tick()
    assert.equal(state.router.currentRoute.value.query.path, undefined)
    assert.equal(walk(state.host).filter(item => item.tag === 'button' && hasClass(item, 'doc-row')).length, 2)
    assert.deepEqual(state.errors, [])
  })
}

test('Documents unrelated 409 stays a generic rejection rather than a lineage ambiguity', async t => {
  const base = lineageResponses()
  const documentFetch = url => ['/api/documents/read', '/api/documents/history'].includes(url.pathname)
    ? Response.json({ error: 'unrelated synthetic conflict', detail: 'private arbitrary server text' }, { status: 409 })
    : base(url)
  const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups: [logicalGroups[0]], documentFetch, initialQuery: { path: lineagePath } })
  await tick()
  const reader = walk(state.host).find(item => item.tag === 'article' && hasClass(item, 'reader'))
  const error = walk(reader).find(item => hasClass(item, 'error'))
  assert.ok(error)
  assert.ok(text(error).includes(state.i18n.global.t('errors.diagnosis.request-rejected')))
  assert.ok(!text(error).includes('private arbitrary server text'))
  assert.equal(documentContent(state), undefined)
  assert.deepEqual(state.errors, [])
})

function scopedActionResponses({ commentStatus = 200, selectionStatus = 200, actionStatus = 200, selectionReady } = {}) {
  const base = lineageResponses()
  return (url, init) => {
    const method = init.method || 'GET'
    if (url.pathname === '/api/documents/comments') return commentStatus === 200
      ? Response.json({ comments: [], count: 0, document_id: Number(url.searchParams.get('document_id')) })
      : Response.json({ error: 'synthetic scoped refusal' }, { status: commentStatus })
    if (url.pathname === '/api/documents/comment' && method === 'POST') return Response.json({ error: 'synthetic refusal' }, { status: 403 })
    if (url.pathname === '/api/documents/selection' && method === 'POST') {
      const payload = JSON.parse(init.body)
      const response = () => selectionStatus === 200
        ? Response.json({ selection: { domain: 'documents', kind: 'explicit', selection_version: 7, targets: payload.selection.targets } })
        : Response.json({ error: 'synthetic scoped refusal' }, { status: selectionStatus })
      return selectionReady ? selectionReady.then(response) : response()
    }
    if (url.pathname === '/api/documents' && method === 'POST') return actionStatus === 200
      ? Response.json({ operation_state: 'completed', readback: { authoritative: true, kind: 'non_disclosing', operation_status: 'export_ready' }, artifact: { download_url: '/api/documents/exports/b857ebf7-c1cf-4a1f-a733-465ee492d3cb', filename: 'synthetic.json', content_type: 'application/json', byte_length: 32 } })
      : Response.json({ error: 'synthetic scoped refusal' }, { status: actionStatus })
    return base(url)
  }
}

for (const [name, logical_groups] of [['grouped', [logicalGroups[0]]], ['empty groups', []]]) {
  test(`SCOPED-ACTION ${name} preserves issued lineage and uses selected historical row for scoped comments and export`, async t => {
    const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups, documentFetch: scopedActionResponses(), initialQuery: { path: lineagePath, document_id: '101' }, capturePageState: true, allowSyntheticMutations: true })
    await tick()
    assert.ok(documentContent(state), 'A valid feature signal supports the issued document ID')
    assert.equal(text(documentContent(state)).trim(), 'Alpha body version 2', 'A valid feature signal supports the issued document ID')
    const history = walk(state.host).find(item => item.tag === 'button' && text(item).includes('Alpha version one'))
    assert.ok(history)
    await history.props.onClick(); await tick()
    assert.equal(text(documentContent(state)).trim(), 'Alpha body version 1')
    const reads = documentReads(state)
    assert.ok(reads.length && reads.every(call => call.url.searchParams.get('document_id') === '101' && call.url.searchParams.get('project') === SAME_A))
    const comments = state.calls.filter(call => call.url.pathname === '/api/documents/comments')
    assert.ok(comments.some(call => call.url.searchParams.get('project') === SAME_A && call.url.searchParams.get('document_id') === '100' && call.url.searchParams.get('expected_version') === '1'))
    assert.ok(comments.every(call => call.url.searchParams.get('project') === SAME_A && call.url.searchParams.has('expected_version')))
    state.pageState().commentDraft.value = 'synthetic historical comment'
    await state.pageState().comment()
    const comment = state.calls.find(call => call.url.pathname === '/api/documents/comment' && call.method === 'POST')
    assert.ok(comment)
    const body = JSON.parse(comment.body)
    assert.deepEqual({ project: body.project, document_id: body.document_id, expected_version: body.expected_version }, { project: SAME_A, document_id: 100, expected_version: 1 })
    const button = walk(state.host).find(item => item.props['data-testid'] === 'document-export')
    assert.ok(button && !button.props.disabled)
    await button.props.onClick(); await tick()
    const selection = state.calls.find(call => call.url.pathname === '/api/documents/selection' && call.method === 'POST')
    const action = state.calls.find(call => call.url.pathname === '/api/documents' && call.method === 'POST')
    assert.ok(selection && action)
    const selected = JSON.parse(selection.body), exported = JSON.parse(action.body)
    assert.equal(selected.project, SAME_A)
    assert.deepEqual(selected.selection.targets, [{ id: '100', expected_version: 1 }])
    assert.equal(exported.project, SAME_A)
    assert.equal(exported.selection.selection_version, 7)
    const download = walk(state.host).find(item => item.props['data-testid'] === 'document-export-download')
    assert.ok(download)
    assert.equal(download.props.href, '/api/documents/exports/b857ebf7-c1cf-4a1f-a733-465ee492d3cb')
  })
}

test('SCOPED-ACTION empty groups preserves the Home list-issued lineage link', async t => {
  const state = await mount(t, { path: '/', language: 'en', selected: SAME_A, items: [logicalItems[0]], unresolved: [], logical_groups: [], documentFetch: scopedActionResponses(), homeDocuments: true })
  const links = walk(state.host).filter(item => item.tag === 'a' && item.props.href && new URL(item.props.href, 'http://synthetic.invalid').searchParams.get('path') === lineagePath)
  assert.equal(links.length, 2)
  assert.deepEqual(links.map(link => new URL(link.props.href, 'http://synthetic.invalid').searchParams.get('document_id')).sort(), ['101', '201'])
  await state.router.push(links[0].props.href); await tick(); await tick()
  assert.ok(documentReads(state).every(call => call.url.searchParams.get('document_id') === '101'))
})

for (const status of [403, 404, 409]) {
  test(`SCOPED-ACTION scoped comment refusal ${status} never retries without scope`, async t => {
    const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups: [logicalGroups[0]], documentFetch: scopedActionResponses({ commentStatus: status }), initialQuery: { path: lineagePath, document_id: '101' } })
    await tick()
    const calls = state.calls.filter(call => call.url.pathname === '/api/documents/comments')
    assert.ok(calls.length)
    assert.ok(calls.every(call => call.url.searchParams.get('project') === SAME_A && call.url.searchParams.get('document_id') === '101' && call.url.searchParams.get('expected_version') === '2'))
    assert.ok(walk(state.host).some(item => hasClass(item, 'comments') && walk(item).some(child => hasClass(child, 'error'))), 'A scoped refusal stays visible')
  })
  test(`SCOPED-ACTION scoped selection refusal ${status} does not dispatch export`, async t => {
    const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups: [logicalGroups[0]], documentFetch: scopedActionResponses({ selectionStatus: status }), initialQuery: { path: lineagePath, document_id: '101' }, capturePageState: true, allowSyntheticMutations: true })
    await tick()
    const button = walk(state.host).find(item => item.props['data-testid'] === 'document-export')
    assert.ok(button && !button.props.disabled)
    await button.props.onClick(); await tick()
    const selection = state.calls.filter(call => call.url.pathname === '/api/documents/selection' && call.method === 'POST')
    assert.equal(selection.length, 1)
    assert.equal(JSON.parse(selection[0].body).project, SAME_A)
    assert.ok(!state.calls.some(call => call.url.pathname === '/api/documents' && call.method === 'POST'))
    assert.ok(!walk(state.host).some(item => item.props['data-testid'] === 'document-export-download'))
  })
}

for (const transition of ['project', 'lineage', 'version', 'auth', 'rollback']) {
  test(`SCOPED-ACTION ${transition} transition during selection prevents export dispatch`, async t => {
    let releaseSelection
    const selectionReady = new Promise(resolve => { releaseSelection = resolve })
    t.after(() => releaseSelection())
    const responses = scopedActionResponses({ selectionReady })
    let grouped = true
    const catalogFetch = () => Response.json({ items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups: grouped ? [logicalGroups[0]] : [] })
    const documentFetch = (url, init) => url.pathname === '/api/synthetic-auth-check' ? Response.json({ error: 'unauthorized' }, { status: 401 }) : responses(url, init)
    const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0], logicalItems[1]], unresolved: [], catalogFetch, documentFetch, initialQuery: { path: lineagePath, document_id: '101' }, capturePageState: true, allowSyntheticMutations: true })
    await tick()
    const button = walk(state.host).find(item => item.props['data-testid'] === 'document-export')
    assert.ok(button && !button.props.disabled, 'A feature-aware valid lineage offers scoped export')
    const attempt = state.pageState().exportDocument()
    await tick()
    assert.ok(state.calls.some(call => call.url.pathname === '/api/documents/selection' && call.method === 'POST'))
    if (transition === 'project') await state.router.push({ path: '/documents', query: { project: SAME_B, path: lineagePath, document_id: '101' } })
    if (transition === 'lineage') await state.router.push({ path: '/documents', query: { project: SAME_A, path: lineagePath, document_id: '201' } })
    if (transition === 'version') await state.pageState().selectPrimaryVersion(1)
    if (transition === 'auth') await state.operatorApi.operatorFetchResponse('/api/synthetic-auth-check')
    if (transition === 'rollback') { grouped = false; await state.context.refresh() }
    await tick()
    releaseSelection()
    await attempt; await tick()
    assert.ok(!state.calls.some(call => call.url.pathname === '/api/documents' && call.method === 'POST'), 'Selection completion cannot export an obsolete target')
    assert.ok(!walk(state.host).some(item => item.props['data-testid'] === 'document-export-download'), 'A late result cannot contaminate the new view')
  })
}

for (const [name, missing] of [['omission', undefined], ['malformed', null]]) {
  test(`SCOPED-ACTION feature signal followed by ${name} fails closed and retry recovers`, async t => {
    let catalogCalls = 0
    const items = [logicalItems[0], logicalItems[1]]
    const catalogFetch = () => {
      const call = ++catalogCalls
      return Response.json({ items, unresolved: [], ...(call === 2 ? (missing === undefined ? {} : { logical_groups: missing }) : { logical_groups: [logicalGroups[0]] }) })
    }
    const base = scopedActionResponses()
    const documentFetch = (url, init) => {
      if (url.pathname !== '/api/documents' || (init.method || 'GET') !== 'GET') return base(url, init)
      const offset = Number(url.searchParams.get('offset')), limit = Number(url.searchParams.get('limit'))
      const rows = lineageItems.map(row => ({ ...row, project: url.searchParams.get('project') }))
      return Response.json({ items: rows.slice(offset, offset + limit), total: rows.length, offset, limit, has_more: offset + limit < rows.length })
    }
    const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items, unresolved: [], catalogFetch, documentFetch, initialQuery: { path: lineagePath, document_id: '101' }, capturePageState: true, allowSyntheticMutations: true })
    assert.equal(text(documentContent(state)).trim(), 'Alpha body version 2')
    const before = state.calls.length
    await state.context.refresh(); await tick()
    await assert.rejects(state.loadKnowledgePage('/api/documents', 'all', new URLSearchParams({ offset: '0', limit: '2' })), 'Stale feature metadata must not claim complete all-project totals')
    assert.deepEqual(realProjectOptions(state.host).map(item => item.props.value).sort(), items.map(item => item.project_id).sort())
    await state.pageState().comment(); await state.pageState().exportDocument()
    const later = state.calls.slice(before)
    assert.ok(!later.some(call => call.method === 'POST'))
    assert.ok(!later.some(call => ['/api/documents/read', '/api/documents/history'].includes(call.url.pathname) && !call.url.searchParams.has('document_id')))
    assert.ok(!later.some(call => call.url.pathname === '/api/documents/comments' && !call.url.searchParams.has('project')))
    const retry = walk(state.host).find(item => item.props['data-testid'] === 'retry-project-catalog')
    assert.ok(retry, 'Unknown capability remains recoverable')
    await retry.props.onClick(); await tick(); await tick()
    assert.equal(catalogCalls, 3)
    assert.equal(text(documentContent(state)).trim(), 'Alpha body version 2')
  })
}

for (const status of [403, 404, 409]) {
  test(`SCOPED-ACTION empty groups ID refusal ${status} has no path fallback`, async t => {
    const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0]], unresolved: [], logical_groups: [], documentFetch: lineageResponses({ refusalStatus: status }), initialQuery: { path: lineagePath, document_id: '999' } })
    await tick()
    const reads = documentReads(state)
    assert.ok(reads.length, 'The scoped request reaches the feature-aware backend')
    assert.ok(reads.every(call => call.url.searchParams.get('document_id') === '999' && call.url.searchParams.get('project') === SAME_A))
    assert.equal(documentContent(state), undefined)
  })
}

test('SCOPED-ACTION initial old-server omission retains legacy comment policy', async t => {
  const legacy = lineageResponses({ oldServer: true })
  const documentFetch = (url, init) => url.pathname === '/api/documents/comment' && init.method === 'POST' ? Response.json({ error: 'synthetic legacy refusal' }, { status: 403 }) : legacy(url)
  const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0]], unresolved: [], documentFetch, initialQuery: { path: lineagePath }, capturePageState: true, allowSyntheticMutations: true })
  await tick()
  assert.equal(text(documentContent(state)).trim(), 'Alpha body version 2')
  const comments = state.calls.filter(call => call.url.pathname === '/api/documents/comments')
  assert.ok(comments.length && comments.every(call => !call.url.searchParams.has('project') && !call.url.searchParams.has('expected_version')))
  state.pageState().commentDraft.value = 'synthetic legacy comment'
  await state.pageState().comment()
  const comment = state.calls.find(call => call.url.pathname === '/api/documents/comment' && call.method === 'POST')
  assert.ok(comment)
  assert.equal(JSON.parse(comment.body).document_id, 101)
  assert.ok(!Object.hasOwn(JSON.parse(comment.body), 'project'))
})

test('SCOPED-ACTION empty groups list row opens its exact issued ID', async t => {
  const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0]], unresolved: [], logical_groups: [], documentFetch: scopedActionResponses() })
  const row = walk(state.host).find(item => item.tag === 'button' && hasClass(item, 'doc-row') && text(item).includes('Alpha entry'))
  assert.ok(row)
  await row.props.onClick(); await tick()
  assert.equal(state.router.currentRoute.value.query.document_id, '101')
  assert.equal(text(documentContent(state)).trim(), 'Alpha body version 2')
  assert.ok(documentReads(state).every(call => call.url.searchParams.get('document_id') === '101'))
})

for (const status of [403, 404, 409]) {
  test(`SCOPED-ACTION scoped export action refusal ${status} stays visible without fallback`, async t => {
    const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups: [logicalGroups[0]], documentFetch: scopedActionResponses({ actionStatus: status }), initialQuery: { path: lineagePath, document_id: '101' }, capturePageState: true, allowSyntheticMutations: true })
    await tick()
    const button = walk(state.host).find(item => item.props['data-testid'] === 'document-export')
    assert.ok(button && !button.props.disabled)
    await button.props.onClick(); await tick()
    const actions = state.calls.filter(call => call.url.pathname === '/api/documents' && call.method === 'POST')
    assert.equal(actions.length, 1)
    assert.equal(JSON.parse(actions[0].body).project, SAME_A)
    assert.ok(!walk(state.host).some(item => item.props['data-testid'] === 'document-export-download'))
  })
}

test('SCOPED-ACTION alias comment uses the exact requested alias project', async t => {
  const scoped = scopedActionResponses()
  const documentFetch = (url, init) => {
    if (['/api/documents/read', '/api/documents/history'].includes(url.pathname)) {
      const forwarded = new URL(url)
      forwarded.searchParams.set('project', SAME_A)
      return Promise.resolve(scoped(forwarded, init)).then(async response => {
        const body = await response.json()
        return Response.json({ ...body, project: SAME_B }, { status: response.status })
      })
    }
    if (url.pathname === '/api/documents' && (init.method || 'GET') === 'GET') return Response.json({ items: lineageItems.map(row => ({ ...row, project: SAME_B })), total: 2, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
    return scoped(url, init)
  }
  const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_B, items: [logicalItems[0], logicalItems[1]], unresolved: [], logical_groups: [logicalGroups[0]], documentFetch, initialQuery: { path: lineagePath, document_id: '101' }, capturePageState: true, allowSyntheticMutations: true })
  await tick()
  assert.equal(text(documentContent(state)).trim(), 'Alpha body version 2')
  const comments = state.calls.filter(call => call.url.pathname === '/api/documents/comments')
  assert.ok(comments.some(call => call.url.searchParams.get('project') === SAME_B && call.url.searchParams.get('document_id') === '101' && call.url.searchParams.get('expected_version') === '2'))
  state.pageState().commentDraft.value = 'synthetic alias comment'
  await state.pageState().comment()
  const call = state.calls.find(item => item.method === 'POST' && item.url.pathname === '/api/documents/comment')
  assert.ok(call)
  assert.equal(JSON.parse(call.body).project, SAME_B)
})

test('SCOPED-ACTION initial old-server omission retains legacy export policy', async t => {
  const legacy = lineageResponses({ oldServer: true }), scoped = scopedActionResponses()
  const documentFetch = (url, init) => (init.method || 'GET') === 'POST' ? scoped(url, init) : legacy(url)
  const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0]], unresolved: [], documentFetch, initialQuery: { path: lineagePath }, capturePageState: true, allowSyntheticMutations: true })
  await tick()
  const button = walk(state.host).find(item => item.props['data-testid'] === 'document-export')
  assert.ok(button && !button.props.disabled)
  await button.props.onClick(); await tick()
  const selection = state.calls.find(call => call.method === 'POST' && call.url.pathname === '/api/documents/selection')
  const action = state.calls.find(call => call.method === 'POST' && call.url.pathname === '/api/documents')
  assert.ok(selection && action)
  assert.ok(!Object.hasOwn(JSON.parse(selection.body), 'project'))
  assert.ok(!Object.hasOwn(JSON.parse(action.body), 'project'))
  assert.deepEqual(JSON.parse(selection.body).selection.targets, [{ id: '101', expected_version: 2 }])
})

test('SCOPED-ACTION backend ambiguity response offers the lineage-specific diagnosis', async t => {
  const base = scopedActionResponses()
  const documentFetch = (url, init) => ['/api/documents/read', '/api/documents/history'].includes(url.pathname)
    ? Response.json({ error: 'document lineage ambiguous; select a document_id from the list' }, { status: 409 })
    : base(url, init)
  const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items: [logicalItems[0]], unresolved: [], logical_groups: [], documentFetch, initialQuery: { path: lineagePath } })
  await tick()
  assert.equal(documentContent(state), undefined)
  assert.ok(text(state.host).includes(state.i18n.global.t('errors.diagnosis.document-lineage-ambiguous')))
  assert.ok(!text(state.host).includes('document lineage ambiguous; select a document_id from the list'))
  assert.ok(documentReads(state).every(call => !call.url.searchParams.has('document_id')))
})

test('SCOPED-ACTION initially malformed capability cannot establish old-server fallback for an unmentioned project', async t => {
  const logical_groups = [logicalGroups[0], { primary_project_id: SAME_A, alias_project_ids: ['not-a-uuid'] }]
  const state = await mount(t, { path: '/documents', language: 'en', selected: GENERIC, items: logicalItems, unresolved: [], logical_groups, documentFetch: lineageResponses({ oldServer: true }), initialQuery: { path: lineagePath }, capturePageState: true, allowSyntheticMutations: true })
  await tick()
  assert.ok(text(state.host).includes(state.i18n.global.t('projectCatalog.lineageScopeUncertain')))
  assert.equal(documentContent(state), undefined)
  assert.ok(!state.calls.some(call => ['/api/documents/read', '/api/documents/history', '/api/documents/comments'].includes(call.url.pathname)))
  await state.pageState().comment(); await state.pageState().exportDocument()
  assert.ok(!state.calls.some(call => call.method === 'POST'))
  assert.deepEqual(realProjectOptions(state.host).map(item => item.props.value).sort(), logicalItems.map(item => item.project_id).sort())
})

test('SCOPED-ACTION omission after an initially malformed capability remains uncertain until valid recovery', async t => {
  let count = 0
  const items = [logicalItems[0]]
  const catalogFetch = () => Response.json({ items, unresolved: [], ...(++count === 1 ? { logical_groups: null } : count === 2 ? {} : { logical_groups: [] }) })
  const state = await mount(t, { path: '/documents', language: 'en', selected: SAME_A, items, unresolved: [], catalogFetch, documentFetch: scopedActionResponses(), initialQuery: { path: lineagePath, document_id: '101' }, capturePageState: true, allowSyntheticMutations: true })
  await state.context.refresh(); await tick()
  assert.ok(text(state.host).includes(state.i18n.global.t('projectCatalog.lineageScopeUncertain')))
  await assert.rejects(state.loadKnowledgePage('/api/documents', 'all', new URLSearchParams({ offset: '0', limit: '2' })))
  assert.ok(!state.calls.some(call => ['/api/documents/read', '/api/documents/history', '/api/documents/comments'].includes(call.url.pathname)))
  const retry = walk(state.host).find(item => item.props['data-testid'] === 'retry-project-catalog')
  assert.ok(retry)
  await retry.props.onClick(); await tick(); await tick()
  assert.equal(count, 3)
  assert.equal(text(documentContent(state)).trim(), 'Alpha body version 2')
})
