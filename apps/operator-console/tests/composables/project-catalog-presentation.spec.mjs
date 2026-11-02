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

async function mount(t, { path = '/projects', language = 'ru', items = catalog.items, unresolved = catalog.unresolved, selected = '' } = {}) {
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
      calls.push({ url, method: init.method || 'GET', credentials: init.credentials })
      assert.equal(init.method || 'GET', 'GET', 'Catalog presentation must never mutate even the synthetic backend')
      if (url.pathname === '/api/projects') return Response.json({ items, unresolved })
      if (url.pathname === '/api/auth/me') return Response.json({ authenticated: true, auth_disabled: false, role: 'viewer', user: { email: 'catalog@example.test' } })
      if (url.pathname === '/api/selfcheck') return Response.json({ overall: 'healthy' })
      if (url.pathname === '/api/stats') return Response.json({ uptime: '1m' })
      if (url.pathname === '/api/stats/vnext') return Response.json({ embedding: { active_memory_count: 0 } })
      if (url.pathname === '/api/sessions/list') return Response.json({ sessions: [], total: 0 })
      if (['/api/memories', '/api/documents'].includes(url.pathname)) return Response.json({ items: [], total: 0, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
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
  const pageClass = pagePath === '/' ? 'home-page' : pagePath === '/projects' ? 'projects-page' : 'inert-page'
  const page = pagePath === '/' ? component('pages/index.vue') : pagePath === '/projects' ? component('pages/projects.vue') : { render: () => vue.h('main', { class: pageClass }) }
  const router = routing.createRouter({ history: routing.createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
  await router.push({ path, query: selected ? { project: selected } : {} })
  const app = renderer.createApp({ render: () => vue.h(layout, null, { default: () => vue.h(page) }) })
  app.component('ProjectContext', component('components/ProjectContext.vue'))
  for (const name of ['SettingsModal', 'ProfileModal', 'LoginModal', 'MutationResultNotice', 'HonestyBadge']) app.component(name, { render: () => null })
  const messages = JSON.parse(readFileSync(new URL(`i18n/locales/${language}.json`, root), 'utf8'))
  app.use(router).use(createI18n({ legacy: false, locale: language, messages: { [language]: messages }, missingWarn: false, fallbackWarn: false }))
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
  return { host, router, context, calls, errors, path, globals }
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
        ]
        const expected = [
          'Registered project · abcdef01',
          'Shared name · examples/shared · 23456789',
          'Shared name · examples/shared · 34567890',
          `Collision one · ${rows[3].project_id}`,
          `Collision two · ${rows[4].project_id}`,
          `Catalog-wide collision · ${rows[5].project_id}`,
          'Qualified peer · examples/peer',
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
    test(`${path} RU ${surface}: same-name choices have stable identity hints rather than ordinals alone`, async t => {
      const state = await mount(t, { path })
      const labels = await choices(state, surface)
      assert.notEqual(labels.get(SAME_A), labels.get(SAME_B), 'Keep existing equal-name distinction')
      assertIDHint(labels.get(SAME_A), SAME_A)
      assertIDHint(labels.get(SAME_B), SAME_B)
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
