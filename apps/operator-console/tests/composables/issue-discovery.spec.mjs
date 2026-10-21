import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test, { before, after } from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import * as routing from 'vue-router'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createI18n, useI18n } from 'vue-i18n'

const root = fileURLToPath(new URL('../../', import.meta.url))
const A = '11111111-1111-4111-8111-111111111111'
const B = '22222222-2222-4222-8222-222222222222'
const C = '33333333-3333-4333-8333-333333333333'
let server, origin
before(async () => {
  const reservation = createServer().listen(0, '127.0.0.1')
  await once(reservation, 'listening')
  const port = reservation.address().port
  await new Promise(resolve => reservation.close(resolve))
  server = spawn(process.execPath, [join(root, 'scripts/mock-operator-api.mjs')], { env: { ...process.env, PORT: String(port), HOST: '127.0.0.1', OPERATOR_ISSUE_DISCOVERY_FIXTURE: '1' }, stdio: ['ignore', 'pipe', 'pipe'] })
  origin = `http://127.0.0.1:${port}`
  await new Promise((resolve, reject) => { server.stdout.once('data', resolve); server.once('error', reject); server.once('exit', code => reject(new Error(`Fixture exited ${code}`))) })
})
after(async () => { const closed = once(server, 'exit'); server.kill(); await closed })
const tick = async () => { await new Promise(resolve => setTimeout(resolve, 15)); await vue.nextTick() }
async function until(predicate) { for (let i = 0; i < 100; i++) { if (predicate()) return; await tick() } assert.ok(predicate(), 'Rendered HTTP state did not settle') }
function node(tag = '', text = '') { return { tag, text, props: {}, children: [], parent: null } }
const renderer = vue.createRenderer({
  createElement: tag => node(tag), createText: text => node('', text), createComment: () => node(),
  patchProp: (item, key, _previous, value) => { item.props[key] = value },
  setText: (item, value) => { item.text = value }, setElementText: (item, value) => { item.children = [node('', value)] },
  parentNode: item => item.parent, nextSibling: item => item.parent?.children[item.parent.children.indexOf(item) + 1] || null,
  insert(item, parent, anchor) { if (item.parent) item.parent.children = item.parent.children.filter(child => child !== item); item.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1; if (index < 0) parent.children.push(item); else parent.children.splice(index, 0, item) },
  remove(item) { if (item.parent) item.parent.children = item.parent.children.filter(child => child !== item) },
})
const walk = item => [item, ...item.children.flatMap(walk)]
const text = item => item.text + (item.props.innerHTML || '') + item.children.map(text).join(' ')
function evaluate(source, dependency, globals) {
  const module = { exports: {} }
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  new Function('require', 'module', 'exports', ...Object.keys(globals), compiled)(dependency, module, module.exports, ...Object.values(globals))
  return module.exports
}
const descriptor = parse(readFileSync(join(root, 'pages/issues.vue'), 'utf8')).descriptor
const script = compileScript(descriptor, { id: 'issue-discovery' })
const template = compileTemplate({ source: descriptor.template.content, filename: 'issues.vue', id: 'issue-discovery', isTS: true, compilerOptions: { bindingMetadata: script.bindings } })
assert.deepEqual(template.errors, [])
async function mount(t, project = 'all') {
  const calls = [], errors = [], modules = new Map(), storage = new Map()
  let context, issues
  const globals = {
    window: { localStorage: { getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value) } },
    sessionStorage: { getItem: () => null, setItem() { } }, useI18n, useRoute: routing.useRoute, useRouter: routing.useRouter,
    fetch: async (path, init = {}) => { const url = new URL(path, origin); calls.push({ url, method: init.method || 'GET', credentials: init.credentials }); return fetch(url, init) },
  }
  function load(name) {
    if (!modules.has(name)) modules.set(name, evaluate(readFileSync(join(root, `composables/${name}.ts`), 'utf8'), dependency, globals))
    return modules.get(name)
  }
  function dependency(name) {
    if (name === 'vue') return { ...vue, vModelSelect: {}, vModelText: {} }
    if (name === 'vue-router') return routing
    if (name.endsWith('operator-config')) return { operatorConfig: { apiBase: '/api' } }
    const real = load(name.split('/').at(-1))
    if (name.endsWith('useOperatorProjectContext')) return { ...real, useOperatorProjectContext: () => { context = real.useOperatorProjectContext(); return context } }
    if (name.endsWith('useOperatorIssues')) return { ...real, useOperatorIssues: (...args) => { issues = real.useOperatorIssues(...args); return issues } }
    return real
  }
  const component = evaluate(script.content, dependency, globals).default
  component.render = evaluate(template.code, dependency, globals).render
  const router = routing.createRouter({ history: routing.createMemoryHistory(), routes: [{ path: '/issues/:id?', component: { render: () => null } }] })
  await router.push(`/issues?project=${project}`)
  const app = renderer.createApp(component)
  app.config.errorHandler = error => errors.push(error.message)
  app.config.warnHandler = warning => { if (!warning.includes('MutationResultNotice')) errors.push(warning) }
  app.component('MutationResultNotice', { render: () => null })
  const messages = JSON.parse(readFileSync(join(root, 'i18n/locales/en.json'), 'utf8'))
  app.use(router).use(createI18n({ legacy: false, locale: 'en', messages: { en: messages }, missingWarn: false, fallbackWarn: false }))
  const host = node('root')
  app.mount(host)
  t.after(() => app.unmount())
  await until(() => issues?.loadState.value.kind === 'live' || issues?.loadState.value.kind === 'empty')
  assert.deepEqual(errors, [])
  return { host, router, context, issues, calls, errors }
}
const rows = state => walk(state.host).filter(row => row.tag === 'button' && row.props.class?.includes?.('issue-row'))
const ids = state => rows(state).map(row => Number(text(row).match(/Discovery issue (\d+)/)?.[1]))
const control = (state, name) => walk(state.host).find(row => row.tag === 'select' && row.props.name === name)
async function choose(state, name, value) { const select = control(state, name); assert.ok(select, `Missing labelled ${name} control`); select.props['onUpdate:modelValue'](value); await tick(); await until(() => !state.issues.pending.value); assert.deepEqual(state.errors, []) }
async function click(row) { assert.ok(row); const handlers = Array.isArray(row.props.onClick) ? row.props.onClick : [row.props.onClick]; for (const handler of handlers) await handler({ preventDefault() { } }); await tick() }
const listCalls = state => state.calls.filter(call => call.url.pathname === '/api/issues')
const next = state => walk(state.host).find(row => row.tag === 'button' && text(row).trim() === '›')

// Installed RED is already preserved; this fixture exercises its real compiled UI and HTTP contract.
test('502 issues remain reachable in bounded All pages, including 477 beyond the first 200', async t => {
  const state = await mount(t)
  await choose(state, 'issues-page-size', 'all')
  assert.equal(rows(state).length, 200)
  assert.ok(!ids(state).includes(477))
  assert.ok(text(control(state, 'issues-page-size')).includes('200'), 'All must disclose its per-request page bound')
  assert.equal(next(state).props.disabled, false, '502 total must leave next enabled after the first 200')
  const seen = [...ids(state)]
  await click(next(state)); await until(() => !state.issues.pending.value)
  assert.equal(rows(state).length, 200); seen.push(...ids(state))
  await click(next(state)); await until(() => !state.issues.pending.value)
  assert.equal(rows(state).length, 102); seen.push(...ids(state))
  assert.equal(new Set(seen).size, 502)
  assert.deepEqual([...seen].sort((a, b) => a - b), Array.from({ length: 502 }, (_, index) => index + 1))
  assert.ok(ids(state).includes(477))
  assert.equal(next(state).props.disabled, true)
  assert.ok(text(state.host).includes('401–502 of 502'))
  assert.deepEqual(listCalls(state).slice(-3).map(call => [call.url.searchParams.get('limit'), call.url.searchParams.get('offset')]), [['200', '0'], ['200', '200'], ['200', '400']])
  await click(rows(state).find(row => text(row).includes('Discovery issue 477')))
  await until(() => state.issues.detail.value?.id === 477)
  assert.equal(state.issues.comments[0].id, 1562)
  assert.ok(text(state.host).includes('Existing discussion 1562'))
  assert.ok(state.calls.every(call => call.method === 'GET'), 'Discovery and existing discussion read must never write')
})

test('visible canonical role choice separates source-owned and incoming issues without raw-origin grants', async t => {
  const state = await mount(t, A)
  assert.deepEqual(ids(state), [481])
  await choose(state, 'issues-project-role', 'source')
  assert.deepEqual(ids(state).sort((a, b) => a - b), [477, 478])
  const request = listCalls(state).at(-1).url.searchParams
  assert.equal(request.get('source_project'), A)
  assert.equal(request.has('project'), false)
  assert.equal(request.has('source_agent'), false)
  assert.ok(!ids(state).includes(479), 'Raw source UUID with NULL canonical source must remain excluded')
  await choose(state, 'issues-project-role', 'target')
  assert.deepEqual(ids(state), [481])
  assert.ok(!ids(state).includes(480), 'Raw target UUID with NULL canonical target must remain excluded')
  assert.ok(state.calls.every(call => call.method === 'GET'))
})

test('server status filtering and numeric pages use the full matching total, not the loaded slice', async t => {
  const state = await mount(t)
  const closed = walk(state.host).find(row => row.tag === 'button' && row.props.class === 'fchip' && text(row).trim() === 'Closed')
  await click(closed); await until(() => !state.issues.pending.value)
  assert.equal(state.issues.registryTotal.value, 251)
  assert.equal(rows(state).length, 10)
  assert.ok(text(state.host).includes('1–10 of 251'))
  assert.equal(listCalls(state).at(-1).url.searchParams.get('status'), 'resolved,closed')
  await click(next(state)); await until(() => !state.issues.pending.value)
  assert.ok(text(state.host).includes('11–20 of 251'))
  assert.equal(listCalls(state).at(-1).url.searchParams.get('offset'), '10')
})

test('foreign scope changes clear source-owned detail and comments and refuse unrelated detail', async t => {
  const state = await mount(t, A)
  await choose(state, 'issues-project-role', 'source')
  await click(rows(state).find(row => text(row).includes('Discovery issue 477')))
  await until(() => state.issues.detail.value?.id === 477)
  await state.router.push(`/issues?project=${C}`); await tick(); await until(() => !state.issues.pending.value)
  assert.equal(state.issues.detail.value, null)
  assert.deepEqual(state.issues.comments, [])
  await state.issues.openIssue(477)
  assert.equal(state.issues.detailState.value.kind, 'error')
  assert.equal(state.issues.detailState.value.error.status, 403)
  assert.equal(state.issues.detail.value, null)
  assert.deepEqual(state.issues.comments, [])
  assert.ok(!text(state.host).includes('Existing discussion 1562'))
})
