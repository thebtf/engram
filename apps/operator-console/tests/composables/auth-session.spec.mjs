import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import * as routing from 'vue-router'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'

const root = new URL('../../', import.meta.url)
const verifiedUser = { authenticated: true, auth_disabled: false, role: 'viewer', source: 'user', user: { email: 'person@example.test' } }

function evaluate(source, require, globals) {
  const module = { exports: {} }
  const code = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    transformers: {
      before: [(context) => (file) => ts.visitNode(file, function visit(node) {
        if (ts.isMetaProperty(node) && node.keywordToken === ts.SyntaxKind.ImportKeyword) return ts.factory.createIdentifier('importMeta')
        return ts.visitEachChild(node, visit, context)
      })],
    },
  }).outputText
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(require, module, module.exports, ...Object.values(globals))
  return module.exports
}

function session(t, identity = () => Response.json(verifiedUser), knowledgeFetch = () => undefined, diagnosticFetch = () => undefined, projectCatalog = [{ id: 'canonical-test', display_name: 'Synthetic project' }]) {
  const calls = []
  const storage = new Map()
  const scope = vue.effectScope()
  t.after(() => scope.stop())
  let loginStatus = 200
  let logoutStatus = 405
  let projectStatus = 200
  let protectedDiagnostics = false
  const globals = {
    sessionStorage: { getItem: (key) => storage.get(key) || null, setItem: (key, value) => storage.set(key, value) },
    importMeta: { env: { DEV: false } },
    computed: vue.computed, ref: vue.ref, onBeforeUnmount: vue.onBeforeUnmount,
    fetch: async (path, init) => {
      calls.push({ path, method: init.method || 'GET', credentials: init.credentials })
      if (path === '/api/auth/me') return identity()
      if (path === '/api/auth/user-login') {
        assert.equal(init.headers['Content-Type'], 'application/json')
        assert.deepEqual(JSON.parse(init.body), { email: 'person@example.test', password: 'synthetic-only' })
        return Response.json({}, { status: loginStatus })
      }
      if (path === '/api/auth/user-logout') return Response.json({}, { status: logoutStatus })
      if (path === '/api/projects') return Response.json({ items: projectCatalog }, { status: projectStatus })
      if (protectedDiagnostics && ['/api/selfcheck', '/api/stats', '/api/stats/vnext'].includes(path)) return Response.json({}, { status: 401 })
      if (['/api/selfcheck', '/api/stats', '/api/stats/vnext'].includes(path)) {
        const diagnostic = diagnosticFetch(path)
        if (diagnostic) return diagnostic
      }
      if (path === '/api/selfcheck') return Response.json({ overall: 'healthy' })
      if (path === '/api/stats') return Response.json({ uptime: '1m' })
      if (path === '/api/stats/vnext') return Response.json({ embedding: { active_memory_count: 0 } })
      const knowledge = knowledgeFetch(new URL(path, 'http://synthetic.invalid'), init)
      if (knowledge) return knowledge
      throw new Error(`Unexpected synthetic request: ${path}`)
    },
  }
  const modules = new Map()
  function load(name) {
    if (!modules.has(name)) modules.set(name, evaluate(readFileSync(new URL(`composables/${name}.ts`, root), 'utf8'), dependency, name === 'useOperatorProjectContext' ? { ...globals, window: {} } : globals))
    return modules.get(name)
  }
  function dependency(name) {
    if (name === 'vue') return vue
    if (name === 'vue-router') return routing
    if (name.endsWith('operator-config')) return { operatorConfig: { apiBase: '/api' } }
    if (name.endsWith('useConsolePreferences')) return { startedLoads: new Set() }
    return load(name.split('/').at(-1))
  }
  const shell = scope.run(() => load('useOperatorShell').useOperatorShellStatus())
  const api = load('useOperatorApi')
  return { shell, api, calls, storage, dependency, mutation: load('useApi'), loginStatus: (status) => { loginStatus = status }, logoutStatus: (status) => { logoutStatus = status }, projectStatus: (status) => { projectStatus = status }, protectDiagnostics: () => { protectedDiagnostics = true } }
}

function assertNoIdentity(info, posture) {
  assert.equal(info.authPosture, posture)
  assert.equal(info.authenticated, false)
  assert.equal(info.role, '')
  assert.equal(info.identityName, '')
  assert.equal(info.identityProvider, '')
}

for (const [label, response, posture] of [
  ['anonymous', () => Response.json({ authenticated: false, auth_disabled: false }), 'locked'],
  ['expired', () => Response.json({ authenticated: false, auth_disabled: false }, { status: 401 }), 'locked'],
  ['unavailable', () => Response.json({}, { status: 503 }), 'unknown'],
  ['missing auth evidence', () => Response.json({ role: 'admin', user: { name: 'Unverified' } }), 'unknown'],
  ['malformed booleans', () => Response.json({ authenticated: 'true', auth_disabled: false, role: 'admin' }), 'unknown'],
]) test(`${label} never presents a fabricated operator or session`, async (t) => {
  const consumer = session(t, response)
  assertNoIdentity(consumer.shell.info.value, 'unknown')
  await consumer.shell.refresh()
  assertNoIdentity(consumer.shell.info.value, posture)
})

test('verified user role comes from authMe, not the email form or an operator fallback', async (t) => {
  let identity = verifiedUser
  const consumer = session(t, () => Response.json(identity))
  await consumer.shell.refresh()
  assert.equal(consumer.shell.info.value.role, 'viewer')
  assert.equal(consumer.shell.info.value.identityName, 'person@example.test')
  identity = { authenticated: true, auth_disabled: false }
  await consumer.shell.refresh()
  assert.equal(consumer.shell.info.value.role, '')
  assert.equal(consumer.shell.info.value.identityName, '')
  assert.equal(consumer.shell.info.value.authPosture, 'authenticated')
})

test('server-confirmed auth-disabled mode remains distinct and never invents an admin role', async (t) => {
  let identity = { auth_disabled: true }
  const consumer = session(t, () => Response.json(identity))
  await consumer.shell.refresh()
  assert.equal(consumer.shell.info.value.authPosture, 'auth-disabled')
  assert.equal(consumer.shell.info.value.role, '')
  identity = { authenticated: true, auth_disabled: true, synthetic: true, role: 'admin', source: 'auth-disabled' }
  await consumer.shell.refresh()
  assert.equal(consumer.shell.info.value.authDisabled, true)
  assert.equal(consumer.shell.info.value.synthetic, true)
  assert.equal(consumer.shell.info.value.role, 'admin')
  assert.equal(consumer.shell.info.value.identityName, '')
})

test('a wrong password reports the actual 401 without granting or replacing identity', async (t) => {
  const consumer = session(t)
  consumer.loginStatus(401)
  await assert.rejects(consumer.shell.login('person@example.test', 'synthetic-only'), (error) => error.status === 401)
  assertNoIdentity(consumer.shell.info.value, 'unknown')
  assert.equal(consumer.calls.length, 1)
  assert.equal(consumer.calls[0].credentials, 'include')
})

test('a successful login requires a fresh authMe before the caller can resume', async (t) => {
  let resolveIdentity
  const consumer = session(t, () => new Promise((resolve) => { resolveIdentity = resolve }))
  const attempt = consumer.shell.login('person@example.test', 'synthetic-only')
  await new Promise(setImmediate)
  assertNoIdentity(consumer.shell.info.value, 'unknown')
  resolveIdentity(Response.json(verifiedUser))
  assert.equal(await attempt, true)
  assert.equal(consumer.shell.info.value.role, 'viewer')
  assert.deepEqual(consumer.calls.map((call) => call.path), ['/api/auth/user-login', '/api/auth/me'])
  assert.ok(consumer.calls.every((call) => call.credentials === 'include'))
})

test('login success without a confirmed session remains unverified', async (t) => {
  const consumer = session(t, () => Response.json({}, { status: 503 }))
  assert.equal(await consumer.shell.login('person@example.test', 'synthetic-only'), false)
  assertNoIdentity(consumer.shell.info.value, 'unknown')
})

test('knowledge 401 invalidates a verified identity while 403 does not widen or erase the server role', async (t) => {
  const consumer = session(t)
  await consumer.shell.refresh()
  consumer.projectStatus(403)
  await assert.rejects(consumer.api.operatorFetchJson('/api/projects'), (error) => error.status === 403)
  assert.equal(consumer.shell.info.value.role, 'viewer')
  consumer.projectStatus(401)
  await assert.rejects(consumer.api.operatorFetchJson('/api/projects'), (error) => error.status === 401)
  assertNoIdentity(consumer.shell.info.value, 'locked')
  assert.equal(consumer.shell.info.value.backendStatus, 'auth-required')
})

test('a late authMe cannot restore an identity invalidated by a newer knowledge 401', async (t) => {
  let resolveIdentity
  const consumer = session(t, () => new Promise((resolve) => { resolveIdentity = resolve }))
  const refresh = consumer.shell.refresh()
  consumer.projectStatus(401)
  await assert.rejects(consumer.api.operatorFetchJson('/api/projects'), (error) => error.status === 401)
  resolveIdentity(Response.json(verifiedUser))
  await refresh
  assertNoIdentity(consumer.shell.info.value, 'locked')
})

for (const status of [405, 503]) test(`account logout ${status} is a failure, never synthetic success`, async (t) => {
  const consumer = session(t)
  await consumer.shell.refresh()
  consumer.logoutStatus(status)
  await assert.rejects(consumer.shell.logout(), (error) => error.status === status)
  assert.equal(consumer.shell.info.value.authPosture, 'authenticated')
  assert.equal(consumer.shell.info.value.role, 'viewer')
  assert.equal(consumer.calls.at(-1).path, '/api/auth/user-logout')
  assert.equal(consumer.calls.at(-1).method, 'POST')
})

test('account logout is confirmed only after the server no longer authenticates the user', async (t) => {
  let signedIn = true
  const consumer = session(t, () => signedIn ? Response.json(verifiedUser) : Response.json({ authenticated: false, auth_disabled: false }, { status: 401 }))
  await consumer.shell.refresh()
  consumer.logoutStatus(200)
  assert.equal(await consumer.shell.logout(), false)
  assert.equal(consumer.shell.info.value.authPosture, 'authenticated')
  signedIn = false
  assert.equal(await consumer.shell.logout(), true)
  assertNoIdentity(consumer.shell.info.value, 'locked')
})

test('reachable protected diagnostics returning 401 show sign-in required, not a server outage', async (t) => {
  const consumer = session(t, () => Response.json({ authenticated: false, auth_disabled: false }, { status: 401 }))
  assert.equal(consumer.shell.info.value.backendStatus, 'unknown')
  consumer.protectDiagnostics()
  await consumer.shell.refresh()
  assert.equal(consumer.shell.info.value.backendStatus, 'auth-required')
  assertNoIdentity(consumer.shell.info.value, 'locked')
})

for (const status of [403, 404, 405]) test(`diagnostics ${status} do not declare an outage or fabricate health`, async (t) => {
  const consumer = session(t, undefined, undefined, () => Response.json({}, { status }))
  await consumer.shell.refresh()
  assert.equal(consumer.shell.info.value.backendStatus, 'diagnostics-unavailable')
  assert.equal(consumer.shell.info.value.authPosture, 'authenticated')
  assert.equal(consumer.shell.info.value.health, '-')
  assert.equal(consumer.shell.info.value.version, '-')
  assert.equal(consumer.shell.info.value.activeMemoryCount, null)
  assert.ok(consumer.shell.error.value.includes('/api/selfcheck'))
})

for (const failure of ['transport', 503]) test(`diagnostics ${failure} preserve genuine outage evidence`, async (t) => {
  const consumer = session(t, undefined, undefined, () => {
    if (failure === 'transport') throw new TypeError('Synthetic connection failure')
    return Response.json({}, { status: failure })
  })
  await consumer.shell.refresh()
  assert.equal(consumer.shell.info.value.backendStatus, 'unavailable')
  assert.equal(consumer.shell.info.value.authPosture, 'authenticated')
  assert.equal(consumer.shell.info.value.health, '-')
  assert.ok(consumer.shell.error.value.includes('/api/stats'))
})

test('diagnostics distinguish a partial outage, absent health, and observed degraded health', async (t) => {
  for (const [response, expected] of [
    [(path) => path === '/api/selfcheck' ? Response.json({}, { status: 503 }) : undefined, 'degraded'],
    [(path) => path === '/api/selfcheck' ? Response.json({}) : undefined, 'unknown'],
    [(path) => path === '/api/selfcheck' ? Response.json({ overall: 'unhealthy' }) : undefined, 'degraded'],
  ]) {
    const consumer = session(t, undefined, undefined, response)
    await consumer.shell.refresh()
    assert.equal(consumer.shell.info.value.backendStatus, expected)
    assert.notEqual(consumer.shell.info.value.health, 0)
  }
})

test('diagnostics retain observed version and healthy zero only from a healthy response', async (t) => {
  const consumer = session(t, undefined, undefined, (path) => path === '/api/selfcheck' ? Response.json({ overall: 'healthy', version: 'synthetic-version' }) : undefined)
  await consumer.shell.refresh()
  assert.equal(consumer.shell.info.value.backendStatus, 'ready')
  assert.equal(consumer.shell.info.value.health, 0)
  assert.equal(consumer.shell.info.value.version, 'synthetic-version')
})

test('diagnostics 401 invalidate only the current session and take priority over outages', async (t) => {
  const consumer = session(t, undefined, undefined, (path) => Response.json({}, { status: path === '/api/selfcheck' ? 401 : 503 }))
  await consumer.shell.refresh()
  assert.equal(consumer.shell.info.value.backendStatus, 'auth-required')
  assertNoIdentity(consumer.shell.info.value, 'locked')
})

test('raw mutation responses retain their denial contract while expiring the shared session', async (t) => {
  const consumer = session(t)
  await consumer.shell.refresh()
  consumer.projectStatus(401)
  const request = { requestId: 'synthetic-request', action: 'synthetic-action', intent: { project: 'canonical-test' } }
  const result = await consumer.mutation.executeMutation(request, consumer.api.operatorFetchResponse('/api/projects', { method: 'POST' }), () => undefined)
  assert.equal(result.kind, 'denied')
  assert.equal(result.httpStatus, 401)
  assert.deepEqual(result.request, request)
  assertNoIdentity(consumer.shell.info.value, 'locked')
})

test('actual login dialog clears submitted and closed passwords and never resumes an unverified session', async (t) => {
  const consumer = session(t, () => Response.json({}, { status: 503 }))
  const source = parse(readFileSync(new URL('components/LoginModal.vue', root), 'utf8')).descriptor
  const component = evaluate(compileScript(source, { id: 'normal-login-consumer' }).content, consumer.dependency, {
    useI18n: () => ({ t: (key) => key }), document: { activeElement: null }, HTMLElement: class { },
  }).default
  component.render = () => null
  const node = () => ({ children: [], parent: null })
  const renderer = vue.createRenderer({
    createElement: node, createText: node, createComment: node,
    setText: () => { }, setElementText: () => { }, patchProp: () => { },
    parentNode: (item) => item.parent, nextSibling: () => null,
    insert: (item, parent) => { item.parent = parent; parent.children.push(item) },
    remove: (item) => { if (item.parent) item.parent.children = item.parent.children.filter((child) => child !== item) },
  })
  const events = []
  const app = renderer.createApp(component, { onAuthenticated: () => events.push('authenticated'), onClose: () => events.push('close') })
  const instance = app.mount(node())
  t.after(() => app.unmount())
  const state = instance.$.setupState
  state.email = 'person@example.test'
  state.password = 'synthetic-only'
  consumer.loginStatus(401)
  const failed = state.submit()
  assert.equal(state.password, '')
  await failed
  assert.equal(state.email, '')
  assert.equal(state.error.status, 401)
  assert.deepEqual(events, [])
  consumer.loginStatus(200)
  state.email = 'person@example.test'
  state.password = 'synthetic-only'
  await state.submit()
  assert.equal(state.password, '')
  assert.equal(state.unverified, true)
  assert.deepEqual(events, [])
  state.password = 'synthetic-only'
  state.email = 'person@example.test'
  state.close()
  assert.equal(state.password, '')
  assert.equal(state.email, '')
  assert.deepEqual(events, ['close'])
})

for (const status of [400, 401, 403]) test(`credential login ${status} does not revoke a previously verified session`, async (t) => {
  const consumer = session(t)
  await consumer.shell.refresh()
  consumer.loginStatus(status)
  await assert.rejects(consumer.shell.login('person@example.test', 'synthetic-only'), (error) => error.status === status)
  assert.equal(consumer.shell.info.value.authPosture, 'authenticated')
  assert.equal(consumer.shell.info.value.role, 'viewer')
})

const tick = async () => { await new Promise(setImmediate); await vue.nextTick() }
function node(tag = '', text = '') { return { tag, text, props: {}, children: [], parent: null, focus() { }, scrollIntoView() { } } }
const renderer = vue.createRenderer({
  createElement: (tag) => node(tag), createText: (text) => node('', text), createComment: () => node(),
  patchProp: (item, key, _previous, value) => { item.props[key] = value },
  setText: (item, text) => { item.text = text }, setElementText: (item, text) => { item.children = [node('', text)] },
  parentNode: (item) => item.parent,
  nextSibling: (item) => item.parent?.children[item.parent.children.indexOf(item) + 1] || null,
  insert: (item, parent, anchor) => {
    if (item.parent) item.parent.children = item.parent.children.filter((child) => child !== item)
    item.parent = parent
    const position = anchor ? parent.children.indexOf(anchor) : -1
    if (position < 0) parent.children.push(item); else parent.children.splice(position, 0, item)
  },
  remove: (item) => { if (item.parent) item.parent.children = item.parent.children.filter((child) => child !== item) },
})
function text(item) { return item.text + item.children.map(text).join(' ') }

async function mountRecovery(t, consumer, location, pageName) {
  const navigation = []
  const globals = {
    useRoute: routing.useRoute, useRouter: routing.useRouter,
    computed: vue.computed, ref: vue.ref, watch: vue.watch, nextTick: vue.nextTick, onBeforeUnmount: vue.onBeforeUnmount,
    useI18n: () => ({ t: (key) => key, locale: vue.ref('en') }),
    useSettingsModal: () => ({ settingsModalOpen: vue.ref(false), settingsModalTab: vue.ref('general'), openSettingsModal() { } }),
    window: { innerWidth: 1200, localStorage: { getItem: () => null }, addEventListener() { }, removeEventListener() { }, location: { assign: (href) => navigation.push(href) } },
    document: { body: { style: { overflow: '' } }, querySelector: () => null, querySelectorAll: () => [] },
    CSS: { escape: (value) => value }, Node: class { },
  }
  const dependency = (name) => {
    if (name.endsWith('useConsolePreferences')) return { density: vue.ref('comfortable'), theme: vue.ref('dark') }
    if (name.endsWith('i18n.config')) return { locales: [{ code: 'en' }], setLocale() { } }
    if (name.endsWith('useNav')) return { useNav: () => ({ NAV: vue.ref([]) }) }
    return consumer.dependency(name)
  }
  function component(path) {
    const descriptor = parse(readFileSync(new URL(path, root), 'utf8')).descriptor
    const script = compileScript(descriptor, { id: path })
    const result = evaluate(script.content, dependency, globals).default
    // As in the existing filter tests, native input v-model belongs to the browser check.
    const template = compileTemplate({ source: descriptor.template.content, filename: path, id: path, isTS: true, compilerOptions: { bindingMetadata: script.bindings, directiveTransforms: { model: () => ({ props: [] }) } } })
    assert.deepEqual(template.errors, [])
    result.render = evaluate(template.code, dependency, globals).render
    return result
  }
  const layout = component('layouts/default.vue')
  const reader = pageName ? component(`pages/${pageName}.vue`) : { render: () => vue.h('article', 'synthetic cached payload') }
  const router = routing.createRouter({ history: routing.createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
  await router.push(location)
  let layoutState, readerState
  const app = renderer.createApp({ render: () => vue.h(layout, { ref: (instance) => { if (instance) layoutState = instance.$.setupState } }, { default: () => vue.h(reader, { ref: (instance) => { if (instance) readerState = instance.$.setupState } }) }) })
  for (const name of ['SettingsModal', 'ProfileModal', 'LoginModal', 'MutationResultNotice', 'HonestyBadge']) app.component(name, { render: () => null })
  app.use(router)
  const host = node('root')
  app.mount(host)
  t.after(() => app.unmount())
  await tick()
  return { host, router, navigation, layout: () => layoutState, reader: () => readerState }
}

test('actual layout removes cached payload on 401 and cannot redisplay it before fresh-document recovery', async (t) => {
  const consumer = session(t)
  const mounted = await mountRecovery(t, consumer, '/memory?project=canonical-test&q=known&filter=low&filter=chain&page=3&size=10&memory=7&from=search&searchQuery=return-query&password=discard&token=discard')
  assert.ok(!text(mounted.host).includes('synthetic cached payload'))
  await consumer.shell.refresh()
  await tick()
  assert.ok(text(mounted.host).includes('synthetic cached payload'))
  consumer.projectStatus(403)
  await assert.rejects(consumer.api.operatorFetchJson('/api/projects'))
  await tick()
  assert.ok(text(mounted.host).includes('synthetic cached payload'))
  consumer.projectStatus(401)
  await assert.rejects(consumer.api.operatorFetchJson('/api/projects'))
  await tick()
  assert.ok(!text(mounted.host).includes('synthetic cached payload'))
  assert.ok(text(mounted.host).includes('auth.signIn'))
  consumer.projectStatus(200)
  assert.equal(await consumer.shell.login('person@example.test', 'synthetic-only'), true)
  await tick()
  assert.ok(!text(mounted.host).includes('synthetic cached payload'))
  mounted.layout().authenticated()
  const restored = new URL(mounted.navigation[0], 'http://synthetic.invalid')
  assert.deepEqual(restored.searchParams.getAll('filter'), ['low', 'chain'])
  for (const [key, value] of [['project', 'canonical-test'], ['q', 'known'], ['page', '3'], ['size', '10'], ['memory', '7'], ['from', 'search'], ['searchQuery', 'return-query']]) assert.equal(restored.searchParams.get(key), value)
  assert.equal(restored.searchParams.has('password'), false)
  assert.equal(restored.searchParams.has('token'), false)
  assert.ok(![...consumer.storage.values()].join('').includes('discard'))
})

for (const status of [405, 503]) test(`actual layout retains protected content and shows failed logout ${status}`, async (t) => {
  const consumer = session(t)
  await consumer.shell.refresh()
  const mounted = await mountRecovery(t, consumer, '/memory?project=canonical-test')
  consumer.logoutStatus(status)
  await mounted.layout().logoutIdentity()
  await tick()
  assert.ok(text(mounted.host).includes('synthetic cached payload'))
  assert.equal(mounted.layout().logoutError.status, status)
  assert.deepEqual(mounted.navigation, [])
})

test('successful logout with unverified readback withholds cached payload even if stale authMe says authenticated', async (t) => {
  const consumer = session(t)
  await consumer.shell.refresh()
  const mounted = await mountRecovery(t, consumer, '/memory?project=canonical-test')
  consumer.logoutStatus(200)
  await mounted.layout().logoutIdentity()
  await tick()
  assert.ok(!text(mounted.host).includes('synthetic cached payload'))
  assert.equal(mounted.layout().logoutUnverified, true)
  assert.deepEqual(mounted.navigation, [])
})

function knowledgeResponse(content) {
  return (url) => {
    if (url.pathname === '/api/memories' || url.pathname === '/api/documents') return Response.json({ items: [], total: 0, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
    if (url.pathname === '/api/memories/7') return Response.json({ id: 7, project: 'canonical-test', content, status: 'active' })
    if (url.pathname === '/api/memories/7/audit') return url.searchParams.get('project') === 'canonical-test' ? Response.json({ memory_id: 7, entries: [] }) : Response.json({}, { status: 400 })
    if (url.pathname === '/api/documents/history') return Response.json({ path: 'synthetic.md', versions: [{ id: 7, version: 2 }, { id: 6, version: 1 }] })
    if (url.pathname === '/api/documents/comments') return Response.json({ comments: [] })
    if (url.pathname === '/api/documents/read') return Response.json({ id: 7, project: 'canonical-test', path: 'synthetic.md', version: Number(url.searchParams.get('version') || 2), content, content_hash: 'synthetic-hash' })
  }
}

for (const page of ['memory', 'documents']) test(`actual ${page} reader preserves unsynced safe controls, drops payload, and refetches after reauth`, async (t) => {
  const consumer = session(t, () => Response.json(verifiedUser), knowledgeResponse('old synthetic protected content'))
  await consumer.shell.refresh()
  const location = page === 'memory' ? '/memory?project=canonical-test&q=known&filter=low&filter=chain&page=3&size=10&memory=7&from=search&searchQuery=return-query' : '/documents?project=canonical-test&q=known&page=3&size=10&path=synthetic.md&version=2&compare=1&docType=decision&pathPrefix=spec'
  const mounted = await mountRecovery(t, consumer, location, page)
  await tick()
  assert.ok(text(mounted.host).includes('old synthetic protected content'))
  mounted.reader().draft = 'unsubmitted safe search'
  if (page === 'documents') {
    mounted.reader().prefixDraft = 'unsubmitted/path'
    mounted.reader().typeDraft = 'decision'
    mounted.reader().commentDraft = 'synthetic private comment not to persist'
  } else {
    mounted.reader().principalScope.principal = 'synthetic-owner'
    mounted.reader().principalScope.visibility = 'shared'
    mounted.reader().principalScope.includePrivate = false
  }
  consumer.projectStatus(401)
  await assert.rejects(consumer.api.operatorFetchJson('/api/projects'))
  await tick()
  assert.ok(!text(mounted.host).includes('old synthetic protected content'))
  consumer.projectStatus(200)
  await consumer.shell.login('person@example.test', 'synthetic-only')
  await tick()
  assert.ok(!text(mounted.host).includes('old synthetic protected content'))
  mounted.layout().authenticated()
  const restored = new URL(mounted.navigation[0], 'http://synthetic.invalid')
  assert.equal(restored.searchParams.get('draftQ'), 'unsubmitted safe search')
  assert.equal(restored.searchParams.get('q'), 'known')
  assert.equal(restored.searchParams.get('page'), '3')
  if (page === 'documents') {
    assert.equal(restored.searchParams.get('version'), '2')
    assert.equal(restored.searchParams.get('compare'), '1')
    assert.equal(restored.searchParams.get('draftPrefix'), 'unsubmitted/path')
  } else {
    assert.deepEqual(restored.searchParams.getAll('filter'), ['low', 'chain'])
    assert.equal(restored.searchParams.get('principalVisibility'), 'shared')
    assert.equal(restored.searchParams.get('principalPrivate'), '0')
  }
  assert.ok(![...consumer.storage.values()].join('').includes('old synthetic protected content'))
  assert.ok(![...consumer.storage.values()].join('').includes('synthetic private comment'))
  const fresh = session(t, () => Response.json(verifiedUser), knowledgeResponse('fresh synthetic current-source content'))
  await fresh.shell.refresh()
  const reloaded = await mountRecovery(t, fresh, restored.pathname + restored.search, page)
  await tick()
  assert.ok(text(reloaded.host).includes('fresh synthetic current-source content'))
  assert.ok(!text(reloaded.host).includes('old synthetic protected content'))
  assert.equal(reloaded.reader().draft, 'unsubmitted safe search')
  assert.ok(!fresh.calls.some((call) => call.path.startsWith('/api/memories/principal')))
})

function walk(item) { return [item, ...item.children.flatMap(walk)] }
function primaryText(item) { return item.tag === 'details' ? '' : item.text + item.children.map(primaryText).join(' ') }
function contextResponse(outcome) {
  return (url) => {
    if (url.pathname === '/api/context/search') {
      if (outcome === 'transport') throw new TypeError('Synthetic connection failure')
      if (outcome === 'stale') return Response.json({ deprecated: true, message: 'synthetic stale response evidence' })
      if (typeof outcome === 'number') return Response.json({}, { status: outcome })
      return Response.json({ observations: [{ id: 7, project: 'untrusted-result-label', type: 'discovery', memory_type: 'context', content: 'synthetic private reader body' }] })
    }
    if (url.pathname === '/api/search/recent') return Response.json({ queries: [] })
    if (['/api/search/analytics', '/api/stats/retrieval'].includes(url.pathname)) return Response.json({})
    return knowledgeResponse('synthetic private reader body')(url)
  }
}

for (const outcome of [403, 404, 405, 503, 'transport', 'stale']) test(`context search ${outcome} recovers to real canonical text tasks without copying content`, async (t) => {
  const literal = "literal %_\\ ' ; --"
  const consumer = session(t, undefined, contextResponse(outcome))
  await consumer.shell.refresh()
  const mounted = await mountRecovery(t, consumer, `/search?project=canonical-test&q=${encodeURIComponent(literal)}&token=discard`, 'search')
  await mounted.reader().refreshSearch()
  await tick()
  for (const destination of ['memory', 'documents']) {
    const link = walk(mounted.host).find((item) => item.props['data-testid'] === `search-text-${destination}`)
    assert.ok(link, `${destination} text task must remain reachable`)
    const target = new URL(link.props.href, 'http://synthetic.invalid')
    assert.equal(target.pathname, `/${destination}`)
    assert.equal(target.searchParams.get('project'), 'canonical-test')
    assert.equal(target.searchParams.get('q'), literal)
    assert.equal(target.searchParams.get('page'), '1')
    assert.equal(target.searchParams.has('token'), false)
    assert.ok(!target.href.includes('synthetic private reader body'))
  }
  const actual = consumer.calls.find((call) => call.path.startsWith('/api/context/search?'))
  assert.ok(actual)
  assert.equal(new URL(actual.path, 'http://synthetic.invalid').searchParams.get('query'), literal)
  assert.ok(!primaryText(mounted.host).includes('/api/context/search'))
  assert.ok(text(mounted.host).includes('/api/context/search'))
  assert.ok(!primaryText(mounted.host).includes('synthetic stale response evidence'))
  assert.ok(!walk(mounted.host).some((item) => item.props['data-testid'] === 'search-result-open-7'))
  const beforeRetry = consumer.calls.filter((call) => call.path.startsWith('/api/context/search?')).length
  await mounted.reader().refreshSearch()
  assert.ok(consumer.calls.filter((call) => call.path.startsWith('/api/context/search?')).length > beforeRetry)
})

test('context search memory result and reader return retain canonical query without private content in URL', async (t) => {
  const consumer = session(t, undefined, contextResponse('live'))
  await consumer.shell.refresh()
  const search = await mountRecovery(t, consumer, '/search?project=canonical-test&q=literal-safe-query', 'search')
  await search.reader().refreshSearch()
  await tick()
  const button = walk(search.host).find((item) => item.props['data-testid'] === 'search-result-open-7')
  assert.ok(button)
  button.props.onClick()
  await tick()
  const selected = search.router.currentRoute.value
  assert.equal(selected.path, '/memory')
  assert.equal(selected.query.project, 'canonical-test')
  assert.equal(selected.query.memory, '7')
  assert.equal(selected.query.from, 'search')
  assert.equal(selected.query.searchQuery, 'literal-safe-query')
  assert.ok(!selected.fullPath.includes('synthetic private reader body'))
  const memory = await mountRecovery(t, consumer, selected.fullPath, 'memory')
  await tick()
  await memory.reader().closeDetail()
  assert.equal(memory.router.currentRoute.value.path, '/search')
  assert.equal(memory.router.currentRoute.value.query.project, 'canonical-test')
  assert.equal(memory.router.currentRoute.value.query.q, 'literal-safe-query')
  assert.equal(memory.router.currentRoute.value.query.focusMemory, '7')
})

test('context search never offers broad text recovery for unknown or all-project scope', async (t) => {
  for (const project of ['unknown-canonical', 'all']) {
    const consumer = session(t, undefined, contextResponse(404))
    await consumer.shell.refresh()
    const mounted = await mountRecovery(t, consumer, `/search?project=${project}&q=literal-safe-query`, 'search')
    await mounted.reader().refreshSearch()
    await tick()
    assert.ok(!walk(mounted.host).some((item) => item.props['data-testid']?.startsWith('search-text-')))
    assert.ok(!consumer.calls.some((call) => call.path.startsWith('/api/context/search?')))
  }
})

const auditProjects = [{ id: 'canonical-test', display_name: 'Shared label' }, { id: 'canonical-other', display_name: 'Shared label' }]
function auditPayload(id, marker) {
  return { memory_id: id, entries: [{ id: id * 10, memory_id: id, action: 'suppress', actor: `synthetic actor ${marker}`, reason: `synthetic reason ${marker}`, before_state_present: false, after_state_present: true, created_at: '2026-10-11T00:00:00Z' }] }
}
function auditResponses(readAudit, readDetail = (url, id) => Response.json({ id, project: url.searchParams.get('project'), content: `synthetic scoped memory ${id}`, status: 'active' })) {
  return (url) => {
    if (url.pathname === '/api/memories') return Response.json({ items: [], total: 0, offset: Number(url.searchParams.get('offset')), limit: Number(url.searchParams.get('limit')), has_more: false })
    const detail = url.pathname.match(/^\/api\/memories\/(7|8)$/)
    if (detail) return readDetail(url, Number(detail[1]))
    const audit = url.pathname.match(/^\/api\/memories\/(7|8)\/audit$/)
    if (audit) {
      const project = url.searchParams.get('project')
      if (!project || project === 'all') return Response.json({}, { status: 400 })
      if (!auditProjects.some((row) => row.id === project)) return Response.json({}, { status: 403 })
      return readAudit(url, Number(audit[1]))
    }
  }
}

test('memory audit renders only the bound canonical reader history and preserves query/filter return', async (t) => {
  const consumer = session(t, undefined, auditResponses((_url, id) => Response.json(auditPayload(id, 'authorized'))), undefined, auditProjects)
  await consumer.shell.refresh()
  const mounted = await mountRecovery(t, consumer, '/memory?project=canonical-test&memory=7&q=literal-safe&filter=low&filter=chain&page=3&size=10', 'memory')
  await tick()
  assert.ok(text(mounted.host).includes('synthetic actor authorized'))
  assert.ok(text(mounted.host).includes('synthetic reason authorized'))
  const request = consumer.calls.find((call) => call.path.includes('/7/audit?'))
  assert.equal(new URL(request.path, 'http://synthetic.invalid').searchParams.get('project'), 'canonical-test')
  assert.equal(new URL(request.path, 'http://synthetic.invalid').searchParams.get('limit'), '200')
  assert.ok(!request.path.includes('Shared label'))
  assert.ok(![...consumer.storage.values()].join('').includes('synthetic reason authorized'))
  await mounted.reader().closeDetail()
  assert.equal(mounted.router.currentRoute.value.query.project, 'canonical-test')
  assert.equal(mounted.router.currentRoute.value.query.q, 'literal-safe')
  assert.deepEqual(mounted.router.currentRoute.value.query.filter, ['low', 'chain'])
  assert.equal(mounted.router.currentRoute.value.query.page, '3')
  assert.ok(!text(mounted.host).includes('synthetic reason authorized'))
})

for (const status of [400, 403, 404]) test(`memory audit denial ${status} stays an error rather than empty or private history`, async (t) => {
  const consumer = session(t, undefined, auditResponses(() => Response.json({ reason: 'synthetic hidden private reason' }, { status })))
  await consumer.shell.refresh()
  const mounted = await mountRecovery(t, consumer, '/memory?project=canonical-test&memory=7', 'memory')
  await tick()
  assert.equal(mounted.reader().auditState.kind, 'error')
  assert.equal(mounted.reader().auditState.error.status, status)
  assert.ok(text(mounted.host).includes('synthetic scoped memory 7'))
  assert.ok(!text(mounted.host).includes('synthetic hidden private reason'))
  assert.ok(!text(mounted.host).includes('knowledge.noMemoryHistory'))
})

test('memory audit rejects a mismatched response identity before rendering reasons or actors', async (t) => {
  const consumer = session(t, undefined, auditResponses(() => Response.json(auditPayload(8, 'wrong-memory'))))
  await consumer.shell.refresh()
  const mounted = await mountRecovery(t, consumer, '/memory?project=canonical-test&memory=7', 'memory')
  await tick()
  assert.equal(mounted.reader().auditState.kind, 'error')
  assert.equal(mounted.reader().auditState.error.category, 'invalid-response')
  assert.ok(!text(mounted.host).includes('synthetic actor wrong-memory'))
  assert.ok(!text(mounted.host).includes('synthetic reason wrong-memory'))
})

for (const change of ['project', 'record', 'all-detail-project']) test(`memory audit drops late history after ${change} changes`, async (t) => {
  let releaseOld
  let audits = 0
  const consumer = session(t, undefined, auditResponses((_url, id) => {
    if (++audits === 1) return new Promise((resolve) => { releaseOld = () => resolve(Response.json(auditPayload(id, 'old-context'))) })
    return Response.json(auditPayload(id, 'current-context'))
  }), undefined, auditProjects)
  await consumer.shell.refresh()
  const location = change === 'all-detail-project' ? '/memory?project=all&memoryProject=canonical-test&memory=7' : '/memory?project=canonical-test&memory=7'
  const mounted = await mountRecovery(t, consumer, location, 'memory')
  await tick()
  assert.ok(releaseOld)
  const next = { ...mounted.router.currentRoute.value.query }
  if (change === 'project') next.project = 'canonical-other'
  else if (change === 'record') next.memory = '8'
  else next.memoryProject = 'canonical-other'
  await mounted.router.push({ path: '/memory', query: next })
  await tick()
  assert.ok(text(mounted.host).includes('synthetic reason current-context'))
  releaseOld()
  await tick()
  assert.ok(!text(mounted.host).includes('synthetic reason old-context'))
  assert.ok(text(mounted.host).includes('synthetic reason current-context'))
  const request = new URL(consumer.calls.filter((call) => call.path.includes('/audit?')).at(-1).path, 'http://synthetic.invalid')
  assert.equal(request.searchParams.get('project'), change === 'record' ? 'canonical-test' : 'canonical-other')
  assert.notEqual(request.searchParams.get('project'), 'all')
})

test('memory audit does not dispatch for a stale detail resolved after changing the open record', async (t) => {
  let releaseDetail
  const consumer = session(t, undefined, auditResponses((_url, id) => Response.json(auditPayload(id, 'current-detail')), (url, id) => {
    if (id === 7) return new Promise((resolve) => { releaseDetail = () => resolve(Response.json({ id, project: url.searchParams.get('project'), content: 'synthetic old detail', status: 'active' })) })
    return Response.json({ id, project: url.searchParams.get('project'), content: 'synthetic new detail', status: 'active' })
  }))
  await consumer.shell.refresh()
  const mounted = await mountRecovery(t, consumer, '/memory?project=canonical-test&memory=7', 'memory')
  await tick()
  assert.ok(releaseDetail)
  await mounted.router.push('/memory?project=canonical-test&memory=8')
  await tick()
  releaseDetail()
  await tick()
  assert.ok(text(mounted.host).includes('synthetic new detail'))
  assert.ok(!text(mounted.host).includes('synthetic old detail'))
  assert.ok(!consumer.calls.some((call) => call.path.includes('/7/audit?')))
  assert.equal(mounted.reader().auditState.data.memory_id, 8)
})

test('memory audit clears rendered payload on logout and discards pending history after auth loss', async (t) => {
  let signedIn = true
  let releaseLate
  let audits = 0
  const consumer = session(t, () => signedIn ? Response.json(verifiedUser) : Response.json({ authenticated: false, auth_disabled: false }, { status: 401 }), auditResponses((_url, id) => {
    if (++audits === 1) return Response.json(auditPayload(id, 'before-logout'))
    return new Promise((resolve) => { releaseLate = () => resolve(Response.json(auditPayload(id, 'after-auth-loss'))) })
  }))
  await consumer.shell.refresh()
  const mounted = await mountRecovery(t, consumer, '/memory?project=canonical-test&memory=7&q=literal-safe&filter=low&filter=chain', 'memory')
  await tick()
  const reader = mounted.reader()
  assert.ok(text(mounted.host).includes('synthetic reason before-logout'))
  const pending = reader.readDetail()
  await tick()
  assert.ok(releaseLate)
  consumer.logoutStatus(200)
  signedIn = false
  await mounted.layout().logoutIdentity()
  await tick()
  assert.equal(reader.auditState, null)
  assert.equal(reader.opened, null)
  assert.ok(!text(mounted.host).includes('synthetic reason before-logout'))
  releaseLate()
  await pending
  await tick()
  assert.equal(reader.auditState, null)
  assert.ok(!text(mounted.host).includes('synthetic reason after-auth-loss'))
  assert.ok(![...consumer.storage.values()].join('').includes('synthetic reason'))
})

test('memory audit refuses absent and all-project scope without sending an unscoped request', async (t) => {
  const consumer = session(t, undefined, auditResponses((_url, id) => Response.json(auditPayload(id, 'unexpected'))))
  await consumer.shell.refresh()
  const mounted = await mountRecovery(t, consumer, '/memory?project=canonical-test', 'memory')
  for (const scope of [undefined, '', 'all']) await assert.rejects(mounted.reader().auditMemory('7', scope, 200), TypeError)
  assert.ok(!consumer.calls.some((call) => call.path.includes('/audit?')))
})
