import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import * as routing from 'vue-router'
import { createI18n } from 'vue-i18n'
import { parse, compileScript } from '@vue/compiler-sfc'

// Run with ordinary dependencies, or Main's disposable dependency-resolution hook.
// No Nuxt server, real HTTP, product writes, generated files or copied issue logic.
const appRoot = new URL('../app/', import.meta.url)
const sourceProject = '11111111-1111-4111-8111-111111111111'
const targetProject = '22222222-2222-4222-8222-222222222222'
const createdAt = '2026-10-01T12:00:00Z'
const catalog = { items: [sourceProject, targetProject].map((project_id, index) => ({ project_id, name: `Synthetic project ${index}` })), unresolved: [] }
const copy = value => JSON.parse(JSON.stringify(value))
const json = (value, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'content-type': 'application/json' } })
const row = (fields = {}) => ({ id: 701, revision: '1', title: 'Synthetic task', body: 'Original description', status: 'open', priority: 'medium', type: 'task', source_project: sourceProject, target_project: targetProject, source_agent: 'human', metadata: {}, labels: [], created_at: createdAt, updated_at: createdAt, ...fields })
const comment = (id, body = `Synthetic comment ${id}`) => ({ id, issue_id: 701, author_project: sourceProject, author_agent: 'human', body, metadata: {}, created_at: createdAt })
const history = revision => ({ revision, previous_revision: revision === '1' ? null : String(Number(revision) - 1), action: revision === '1' ? 'create' : 'update', changed_fields: ['body'], actor: { kind: 'operator', reference: 'operator:auth-disabled' }, attribution: { source_project: sourceProject, source_agent: 'human', session: null }, created_at: createdAt, changes: {} })
const detail = (fields = {}) => ({ issue: row(), comments: [], history: [], comment_count: 0, history_count: 0, comments_offset: 0, comments_limit: 50, history_offset: 0, history_limit: 50, comments_next_offset: null, history_next_offset: null, comments_complete: true, history_complete: true, comments_oversized: false, history_oversized: false, ...fields })

function evaluate(source, filename, require, globals) {
  const module = { exports: {} }
  const code = ts.transpileModule(source, { fileName: filename, compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(require, module, module.exports, ...Object.values(globals))
  return module.exports
}
const catalogUrl = new URL('../../../ui/src/i18n/messages.ts', import.meta.url)
const catalogs = evaluate(readFileSync(catalogUrl, 'utf8'), catalogUrl.pathname, () => { throw new Error('Unexpected catalog import') }, {})
const englishCatalog = catalogs.messages.en
const i18n = createI18n({
  legacy: false, locale: 'en', fallbackLocale: 'en', messages: {
    en: { ...englishCatalog, common: { ...englishCatalog.common, allProjects: 'All projects' } },
    ru: { ...catalogs.messages.ru, common: { ...catalogs.messages.ru.common, allProjects: 'Все проекты' } },
    zh: { issueFlow: catalogs.issueFlowZh, projectCatalog: catalogs.projectCatalogZh, common: { allProjects: '所有项目' } },
  }
}).global


// Only the framework boundary is doubled. These callbacks are the production
// queryFn/mutationFn/onSuccess/onError, not replacement issue implementations.
function queryBoundary(track, sharedCache = null) {
  const queries = []
  const mutations = []
  const invalidations = []
  function useQuery(input) {
    const options = vue.computed(() => typeof input === 'function' ? input() : vue.unref(input))
    const data = vue.shallowRef()
    const error = vue.shallowRef(null)
    const isPending = vue.ref(true)
    const isFetching = vue.ref(false)
    let generation = 0
    const query = {
      options, data, error, isPending, isFetching, isError: vue.computed(() => error.value !== null),
      refetch() {
        const current = ++generation
        const fn = options.value.queryFn
        const cacheKey = JSON.stringify(options.value.queryKey)
        isFetching.value = true
        return track(Promise.resolve().then(() => fn()).then(result => {
          if (sharedCache) sharedCache.set(cacheKey, result)
          if (current === generation) { data.value = result; error.value = null }
          return { data: result }
        }, failure => {
          if (current === generation) error.value = failure
          return { error: failure }
        }).finally(() => {
          if (current === generation) { isPending.value = false; isFetching.value = false }
        }))
      },
    }
    queries.push(query)
    vue.watch(() => JSON.stringify([options.value.queryKey, options.value.enabled !== false]), () => {
      if (sharedCache) {
        const key = JSON.stringify(options.value.queryKey)
        data.value = sharedCache.get(key)
        isPending.value = !sharedCache.has(key)
      }
      if (options.value.enabled !== false) void query.refetch()
    }, { immediate: true, flush: 'sync' })
    return query
  }
  function useMutation(options) {
    const isPending = vue.ref(false)
    async function execute(variables, callbacks = {}) {
      isPending.value = true
      try {
        const result = await options.mutationFn(variables)
        await options.onSuccess?.(result, variables)
        await callbacks.onSuccess?.(result, variables)
        return result
      } catch (error) {
        await options.onError?.(error, variables)
        await callbacks.onError?.(error, variables)
        throw error
      } finally { isPending.value = false }
    }
    const mutation = {
      isPending,
      mutate(variables, callbacks) { track(execute(variables, callbacks).catch(() => undefined)) },
      mutateAsync(variables, callbacks) { return track(execute(variables, callbacks)) },
    }
    mutations.push(mutation)
    return mutation
  }
  const client = {
    invalidateQueries({ queryKey }) {
      invalidations.push(copy(queryKey))
      return Promise.all(queries.filter(query => query.options.value.enabled !== false && queryKey.every((part, index) => query.options.value.queryKey[index] === part)).map(query => query.refetch()))
    },
  }
  return { useQuery, useMutation, useQueryClient: () => client, queries, mutations, invalidations, sharedCache }
}

function hostNode(tag = '') {
  return {
    tagName: tag.toUpperCase(), children: [], parent: null, text: '', props: {}, style: {}, value: '', selected: false,
    get options() { return this.children.filter(child => child.tagName === 'OPTION') },
    getRootNode() {
      let root = this
      while (root.parent) root = root.parent
      return root
    },
    addEventListener() { }, removeEventListener() { }, setAttribute(name, value) { this.props[name] = value }, removeAttribute(name) { delete this.props[name] },
  }
}
const renderer = vue.createRenderer({
  createElement: tag => hostNode(tag), createText: text => Object.assign(hostNode(), { text }), createComment: () => hostNode(),
  setText(node, text) { node.text = text }, setElementText(node, text) { node.text = text; node.children = [] },
  patchProp(node, key, _old, value) { node.props[key] = value; if (key === 'value') { node.value = value; node._value = value } },
  parentNode: node => node.parent, nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
  insert(node, parent, anchor = null) {
    if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index < 0 ? parent.children.length : index, 0, node)
    node.parent = parent
  },
  remove(node) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); node.parent = null },
})

function hidden(vnode) {
  return vnode.props?.hidden === true || vnode.props?.hidden === '' || vnode.props?.['aria-hidden'] === 'true' || vnode.props?.style?.display === 'none' || vnode.dirs?.some(binding => binding.dir === vue.vShow && !binding.value)
}
function childrenForDisplay(vnode) {
  if (!Array.isArray(vnode.children)) return []
  return vnode.type === 'details' && !vnode.props?.open ? vnode.children.filter(child => child.type === 'summary') : vnode.children
}
function nodes(vnode, result = []) {
  if (!vnode || typeof vnode !== 'object' || hidden(vnode)) return result
  result.push(vnode)
  if (vnode.component) nodes(vnode.component.subTree, result)
  else for (const child of childrenForDisplay(vnode)) nodes(child, result)
  return result
}
function text(vnode) {
  if (!vnode || hidden(vnode)) return ''
  if (vnode.component) return text(vnode.component.subTree)
  return typeof vnode.children === 'string' ? vnode.children : childrenForDisplay(vnode).map(text).join(' ')
}
function rawText(vnode) {
  if (!vnode) return ''
  if (vnode.component) return rawText(vnode.component.subTree)
  return typeof vnode.children === 'string' ? vnode.children : Array.isArray(vnode.children) ? vnode.children.map(rawText).join(' ') : ''
}
function disabled(node) {
  return node.props?.disabled === true || node.props?.disabled === '' || node.props?.['aria-disabled'] === 'true'
}
function accessibleName(node, root) {
  const labelledBy = node.props?.['aria-labelledby']?.split(/\s+/).map(id => text(nodes(root).find(candidate => candidate.props?.id === id))).join(' ')
  return (node.props?.['aria-label'] || labelledBy || text(node)).trim()
}

// Match accessible action purposes, not an exact caption or container layout.
const commentNext = /(?:load|more|next|ещ[её]|загруз|следующ).*(?:comment|коммент)|(?:comment|коммент).*(?:load|more|next|ещ[её]|загруз|следующ)/i
const commentPrevious = /(?:previous|back|first|предыдущ|назад|перв).*(?:comment|коммент)|(?:comment|коммент).*(?:previous|back|first|предыдущ|назад|перв)/i
const historyNext = /(?:load|more|next|ещ[её]|загруз|следующ).*(?:history|истори)|(?:history|истори).*(?:load|more|next|ещ[её]|загруз|следующ)/i
const reconcileIntent = /reconcil|retry|resume|повтор|свер|продолж/i

function assertComments(page, ids) {
  for (const id of ids) assert.equal(page.text().match(new RegExp(`Synthetic comment ${id}(?!\\d)`, 'g'))?.length ?? 0, 1, `comment ${id} is accessible exactly once on its page`)
}

async function accessEarlierComments(page, ids) {
  if (ids.every(id => page.text().match(new RegExp(`Synthetic comment ${id}(?!\\d)`, 'g'))?.length === 1)) {
    assertComments(page, ids)
    return
  }
  await page.click(commentPrevious)
  assertComments(page, ids)
}

for (const [status, code] of [[401, 'UNAUTHENTICATED'], [403, 'FORBIDDEN'], [404, 'ISSUE_NOT_FOUND']]) {
  test(`denial ${status}, transient failure and later success never reveal an unbound old draft`, async t => {
    const f = await fixture(t)
    const first = partialDetail()
    f.setDetail(first)
    let reads = 0
    f.setResponder(call => {
      if (call.method === 'PATCH') return json({ error: { code: 'REVISION_CONFLICT', message: 'Conflict' } }, 409)
      if (call.method !== 'GET' || call.url.pathname !== '/api/issues/701' || call.url.searchParams.get('comments_offset') !== '50') return
      reads++
      if (reads <= 2) {
        const query = f.query.queries.find(item => item.options.value.queryKey[0] === 'issue-detail')
        assert.ok(query)
        query.data.value = undefined
        return reads === 1 ? json({ error: { code, message: 'Denied' } }, status) : json({ error: { code: 'UNAVAILABLE', message: 'Transient' } }, 503)
      }
      return json({ ...first, comments: [], comment_count: 50, comments_offset: 50, comments_next_offset: null, comments_complete: true })
    })
    const page = await f.mount('[id]')
    await page.click('Edit')
    await page.field('Title', 'Private old draft')
    await page.click('Save edit')
    assert.equal(f.writes().length, 1)
    await page.click('Next comments page')
    assert.doesNotMatch(rawText(page.tree()), /Private old draft|Synthetic task/)
    await page.click('Read current issue again')
    assert.doesNotMatch(rawText(page.tree()), /Private old draft|Synthetic task/)
    await page.click('Read current issue again')
    assert.equal(reads, 3, 'the final read must be a real successful request')
    assert.doesNotMatch(rawText(page.tree()), /Private old draft/, 'a 200 cannot reassign an unbound draft')
    assert.equal(f.writes().length, 1, 'reads never replay the draft')
  })
}

test('an observed authentication change hides an unbound old draft after a new read', async t => {
  const f = await fixture(t)
  f.setDetail(partialDetail())
  f.setResponder(call => call.method === 'PATCH' ? json({ error: { code: 'REVISION_CONFLICT', message: 'Conflict' } }, 409) : undefined)
  const page = await f.mount('[id]')
  await page.click('Edit')
  await page.field('Title', 'Private old draft')
  await page.click('Save edit')
  f.setAuthState({ phase: 'loading' })
  f.setAuthState({ phase: 'authenticated', user: { id: 'other-user' }, authDisabled: false })
  await f.settle()
  assert.doesNotMatch(rawText(page.tree()), /Private old draft/)
  assert.equal(f.writes().length, 1)
})

test('a fresh authorized detail read releases the denial latch when there is no old draft', async t => {
  const f = await fixture(t)
  const first = partialDetail()
  f.setDetail(first)
  let reads = 0
  f.setResponder(call => {
    if (call.method !== 'GET' || call.url.pathname !== '/api/issues/701' || call.url.searchParams.get('comments_offset') !== '50') return
    reads++
    if (reads <= 2) {
      const query = f.query.queries.find(item => item.options.value.queryKey[0] === 'issue-detail')
      assert.ok(query)
      query.data.value = undefined
      return reads === 1 ? json({ error: { code: 'FORBIDDEN', message: 'Denied' } }, 403) : json({ error: { code: 'UNAVAILABLE', message: 'Transient' } }, 503)
    }
    return json({ ...first, comments: [], comment_count: 50, comments_offset: 50, comments_next_offset: null, comments_complete: true })
  })
  const page = await f.mount('[id]')
  await page.click('Next comments page')
  assert.doesNotMatch(rawText(page.tree()), /Synthetic task/)
  await page.click('Read current issue again')
  assert.doesNotMatch(rawText(page.tree()), /Synthetic task/)
  await page.click('Read current issue again')
  assert.equal(reads, 3)
  assert.match(page.text(), /Synthetic task/)
  assert.equal(f.writes().length, 0)
})

test('a read started before a denial cannot reopen cached detail after a later transient failure', async t => {
  const f = await fixture(t)
  const page = await f.mount('[id]')
  const query = f.query.queries.find(item => item.options.value.queryKey[0] === 'issue-detail')
  assert.ok(query)
  let release
  let reads = 0
  f.setResponder(call => {
    if (call.method !== 'GET' || call.url.pathname !== '/api/issues/701') return
    reads++
    if (reads === 1) return new Promise(resolve => { release = () => resolve(json(detail())) })
    if (reads === 2) return json({ error: { code: 'FORBIDDEN', message: 'Denied' } }, 403)
    return json({ error: { code: 'UNAVAILABLE', message: 'Transient' } }, 503)
  })
  const older = query.refetch()
  await new Promise(setImmediate)
  assert.equal(typeof release, 'function')
  await query.refetch()
  assert.doesNotMatch(rawText(page.tree()), /Synthetic task/)
  release()
  await older
  await query.refetch()
  await f.settle()
  assert.equal(reads, 3)
  assert.doesNotMatch(rawText(page.tree()), /Synthetic task/)
  assert.equal(f.writes().length, 0)
})

test('a fresh authorized read shows current issue data without exposing an unbound draft', async t => {
  const f = await fixture(t)
  const first = partialDetail()
  f.setDetail(first)
  let reads = 0
  f.setResponder(call => {
    if (call.method === 'PATCH') return json({ error: { code: 'REVISION_CONFLICT', message: 'Conflict' } }, 409)
    if (call.method !== 'GET' || call.url.pathname !== '/api/issues/701' || call.url.searchParams.get('comments_offset') !== '50') return
    if (++reads === 1) {
      const query = f.query.queries.find(item => item.options.value.queryKey[0] === 'issue-detail')
      assert.ok(query)
      query.data.value = undefined
      return json({ error: { code: 'FORBIDDEN', message: 'Denied' } }, 403)
    }
    return json({ ...first, issue: row({ revision: '2', title: 'Newly authorized current issue' }), comments: [], comment_count: 50, comments_offset: 50, comments_next_offset: null, comments_complete: true })
  })
  const page = await f.mount('[id]')
  await page.click('Edit')
  await page.field('Title', 'Private old draft')
  await page.click('Save edit')
  await page.click('Next comments page')
  assert.doesNotMatch(rawText(page.tree()), /Private old draft|Synthetic task/)
  await page.click('Read current issue again')
  assert.equal(reads, 2)
  assert.match(page.text(), /Newly authorized current issue/)
  assert.doesNotMatch(rawText(page.tree()), /Private old draft/)
  assert.equal(f.writes().length, 1)
})

for (const scenario of ['denial', 'auth change while unmounted']) {
  test(`shared cached issue stays hidden on remount after ${scenario} until a fresh authorized read`, async t => {
    const f = await fixture(t, { sharedCache: true })
    const first = await f.mount('[id]')
    assert.match(first.text(), /Synthetic task/)
    const originalQuery = f.query.queries.find(item => item.options.value.queryKey[0] === 'issue-detail')
    assert.ok(originalQuery)
    const originalKey = JSON.stringify(originalQuery.options.value.queryKey)
    const cached = f.query.sharedCache.get(originalKey)
    assert.equal(cached?.issue.title, 'Synthetic task', 'the shared keyed cache must actually retain the successful response')
    f.setResponder(call => {
      if (call.method === 'PATCH') return json({ error: { code: 'REVISION_CONFLICT', message: 'Conflict' } }, 409)
      if (scenario === 'denial' && call.method === 'GET' && call.url.pathname === '/api/issues/701') return json({ error: { code: 'FORBIDDEN', message: 'Denied' } }, 403)
    })
    await first.click('Edit')
    await first.field('Title', 'Private old remount draft')
    await first.click('Save edit')
    assert.equal(f.writes().length, 1)
    if (scenario === 'denial') {
      await originalQuery.refetch()
      await f.settle()
      assert.doesNotMatch(rawText(first.tree()), /Synthetic task|Private old remount draft/)
    }
    first.unmount()
    if (scenario === 'auth change while unmounted') {
      f.setAuthState({ phase: 'authenticated', user: { id: 'other-user' }, authDisabled: false })
    }
    let release
    f.setResponder(call => {
      if (call.method === 'GET' && call.url.pathname === '/api/issues/701') {
        return new Promise(resolve => { release = () => resolve(json(detail({ issue: row({ revision: '2', title: 'Fresh authorized issue' }) }))) })
      }
    })
    const reopened = await f.mount('[id]', { settleOnMount: false })
    try {
      await new Promise(setImmediate)
      await vue.nextTick()
      assert.equal(typeof release, 'function', 'the new authorized read must still be pending')
      const reopenedQuery = f.query.queries.filter(item => item.options.value.queryKey[0] === 'issue-detail').at(-1)
      assert.ok(reopenedQuery)
      if (JSON.stringify(reopenedQuery.options.value.queryKey) === originalKey) {
        assert.equal(reopenedQuery.data.value, cached, 'same-key remount must actually reuse cached data in the fixture')
      }
      assert.doesNotMatch(rawText(reopened.tree()), /Synthetic task|Private old remount draft/, 'old cached content must stay hidden before current authorization succeeds')
    } finally {
      if (release) release()
      await f.settle()
    }
    assert.match(reopened.text(), /Fresh authorized issue/)
    assert.doesNotMatch(rawText(reopened.tree()), /Private old remount draft/)
    assert.equal(f.writes().length, 1, 'remount and reads must not replay the draft')
  })
}

async function fixture(t, { sharedCache = false, crypto = globalThis.crypto } = {}) {
  const calls = [], pending = new Set(), violations = [], renderErrors = [], apps = [], modules = new Map()
  let responder = () => undefined
  let issueDetail = detail()
  let list = { issues: [row()], total: 1, limit: 50, offset: 0, next_offset: null }
  // Vue 3.5 vModelText resolves the root and checks its browser root types.
  // This renderer mounts into a detached host element, so getRootNode returns
  // that element rather than a focused Document or ShadowRoot.
  const tabStorage = new Map()
  const sessionStorage = {
    getItem: key => tabStorage.get(String(key)) ?? null,
    setItem: (key, value) => { tabStorage.set(String(key), String(value)) },
    removeItem: key => { tabStorage.delete(String(key)) },
  }
  const nuxtState = new Map()
  const useState = (key, initialize) => {
    if (!nuxtState.has(key)) nuxtState.set(key, vue.ref(initialize()))
    return nuxtState.get(key)
  }
  const browserGlobals = { document: { activeElement: null }, Document: class MemoryDocument { }, ShadowRoot: class MemoryShadowRoot { } }
  const addedGlobals = Object.entries(browserGlobals).filter(([name]) => !Object.getOwnPropertyDescriptor(globalThis, name))
  for (const [name, value] of addedGlobals) Object.defineProperty(globalThis, name, { configurable: true, value })
  const track = promise => { pending.add(promise); promise.then(() => pending.delete(promise), () => pending.delete(promise)); return promise }
  const query = queryBoundary(track, sharedCache ? new Map() : null)
  const router = routing.createRouter({ history: routing.createMemoryHistory(), routes: [{ path: '/issues/:id?', component: { render: () => null } }] })
  await router.push({ path: '/issues', query: { project: sourceProject } })
  const $fetch = async (path, options = {}) => {
    // Keep exact wire JSON before decoding it for the existing logical HTTP assertions.
    const serializedBody = typeof options.body === 'string' ? options.body : JSON.stringify(options.body)
    const call = { url: new URL(path, 'http://synthetic.invalid'), method: options.method || 'GET', body: undefined, serializedBody, credentials: options.credentials }
    calls.push(call)
    // Count attempted dispatch even if a malformed JSON body cannot be decoded.
    call.body = typeof options.body === 'string' ? JSON.parse(options.body) : options.body === undefined ? undefined : copy(options.body)
    let response = await responder(call)
    if (response === undefined && call.url.pathname === '/api/projects' && call.method === 'GET') response = json(catalog)
    if (response === undefined && call.url.pathname === '/api/auth/me' && call.method === 'GET') response = json({ actor: { kind: 'operator', reference: 'operator:auth-disabled' } })
    if (response === undefined && call.url.pathname === '/api/issues' && call.method === 'GET') response = json(list)
    if (response === undefined && call.url.pathname === '/api/issues/701' && call.method === 'GET') response = json(issueDetail)
    if (response === undefined) {
      const message = `Unexpected synthetic request: ${call.method} ${call.url.pathname}`
      violations.push(message)
      throw new Error(message)
    }
    assert.ok(response instanceof Response, 'responder must return HTTP data, not product behavior')
    const data = await response.json()
    if (!response.ok) {
      // ofetch-compatible transport shape; stable codes remain response data,
      // not prefixed into message by the fixture to make UI rendering pass.
      const error = new Error(data.error?.message || `HTTP ${response.status}`)
      Object.assign(error, { data, status: response.status, statusCode: response.status, response: { status: response.status, _data: data } })
      throw error
    }
    return data
  }
  const globals = { useRuntimeConfig: () => ({ public: { apiBase: '/api' } }), $fetch, useRoute: routing.useRoute, useRouter: routing.useRouter, sessionStorage, useState, crypto }
  function load(url) {
    const key = url.href
    if (modules.has(key)) return modules.get(key)
    let source = readFileSync(url, 'utf8')
    if (url.pathname.endsWith('.vue')) {
      const parsed = parse(source, { filename: url.pathname })
      assert.deepEqual(parsed.errors, [], 'real page must parse')
      // The host intentionally models observable Vue nodes, not a browser HTML parser.
      // Disable only template static hoisting so real static elements stay traversable;
      // this fixture does not prove optimized static-HTML insertion or browser mounting.
      source = compileScript(parsed.descriptor, { id: 'issue-513', inlineTemplate: true, genDefaultAs: '__page', templateOptions: { compilerOptions: { hoistStatic: false } } }).content + '\nexport default __page\n'
    }
    const exported = evaluate(source, url.pathname.endsWith('.vue') ? `${url.pathname}.ts` : url.pathname, name => {
      if (name === 'vue') return vue
      if (name === '@tanstack/vue-query') return query
      if (name === 'vue-i18n') return { useI18n: () => i18n }
      if (name === '~/components/ProjectCatalogStatus.vue') return { default: { render: () => null } }
      if (name.startsWith('~/')) return load(new URL(`${name.slice(2)}${/\.(vue|ts)$/.test(name) ? '' : '.ts'}`, appRoot))
      if (name.startsWith('.')) return load(new URL(`${name}${name.endsWith('.ts') ? '' : '.ts'}`, url))
      throw new Error(`Unexpected fixture import: ${name}`)
    }, globals)
    modules.set(key, exported)
    return exported
  }
  async function settle() {
    for (let pass = 0; pass < 30; pass++) {
      if (pending.size) await Promise.allSettled([...pending])
      await vue.nextTick()
      await new Promise(setImmediate)
      if (!pending.size) {
        assert.deepEqual(renderErrors, [], 'Vue/compiler/fixture errors are not behavioral RED')
        return
      }
    }
    assert.fail('fixture callbacks did not settle; this is not behavioral RED')
  }
  async function mount(page, { settleOnMount = true } = {}) {
    await router.push({ path: page === 'index' ? '/issues' : '/issues/701', query: { project: sourceProject } })
    const app = renderer.createApp(load(new URL(`pages/issues/${page}.vue`, appRoot)).default)
    app.component('NuxtLink', routing.RouterLink)
    app.config.errorHandler = error => renderErrors.push(String(error?.stack || error))
    app.use(router)
    app.mount(hostNode('root'))
    apps.push(app)
    if (settleOnMount) await settle()
    const tree = () => app._instance.subTree
    return {
      unmount() { app.unmount(); apps.splice(apps.indexOf(app), 1) },
      tree, text: () => text(tree()),
      find: predicate => nodes(tree()).find(predicate),
      hasEnabledAction(label) {
        return nodes(tree()).some(node => ['button', 'a'].includes(node.type) && !disabled(node) && label.test(accessibleName(node, tree())))
      },
      async click(label) {
        const button = nodes(tree()).find(node => {
          if (!['button', 'a', 'summary'].includes(node.type)) return false
          const name = accessibleName(node, tree())
          return typeof label === 'string' ? name === label : label.test(name)
        })
        assert.ok(button, `No rendered action ${label}; required UI capability is absent`)
        assert.ok(accessibleName(button, tree()), 'a reachable action must have an accessible name')
        assert.ok(!disabled(button), `Rendered action ${label} is disabled`)
        const handlers = button.props?.onClick
        for (const handler of Array.isArray(handlers) ? handlers : [handlers]) await handler?.({ button: 0, preventDefault() { }, stopPropagation() { } })
        if (button.type === 'summary') {
          const disclosure = nodes(tree()).find(node => node.type === 'details' && childrenForDisplay(node).includes(button))
          assert.ok(disclosure, 'a summary must belong to a visible disclosure')
          disclosure.props = { ...disclosure.props, open: !disclosure.props?.open }
        }
        await settle()
      },
      async field(label, value) {
        const node = nodes(tree()).find(node => node.type === 'label' && (typeof label === 'string' ? text(node).trim().startsWith(label) : label.test(text(node).trim())))
        const control = node && nodes(node).find(child => ['input', 'textarea', 'select'].includes(child.type))
        assert.ok(control, `No rendered field ${label}; required UI capability is absent`)
        assert.ok(!disabled(control) && !control.props?.readonly, `Rendered field ${label} is not editable`)
        const assign = control.props?.['onUpdate:modelValue']
        assert.equal(typeof assign, 'function', 'real compiled v-model setter must be reachable')
        assign(value)
        await settle()
      },
      fieldValue(label) {
        const node = nodes(tree()).find(node => node.type === 'label' && (typeof label === 'string' ? text(node).trim().startsWith(label) : label.test(text(node).trim())))
        const control = node && nodes(node).find(child => ['input', 'textarea', 'select'].includes(child.type))
        assert.ok(control, `Missing rendered field ${label}`)
        return control.dirs?.find(binding => binding.dir === vue.vModelText || binding.dir === vue.vModelSelect)?.value ?? control.props?.value
      },
      fieldEditable(label) {
        const labelNode = nodes(tree()).find(node => node.type === 'label' && text(node).trim().startsWith(label))
        const control = labelNode && nodes(labelNode).find(child => ['input', 'textarea', 'select'].includes(child.type))
        return !!control && !disabled(control) && !control.props?.readonly
      },
      async reconcileCreate() {
        // A dedicated recovery action may replace/disable the original submit.
        // Either route must actually dispatch the saved intent; no fake stores it.
        const before = calls.filter(call => call.method === 'POST' && call.url.pathname === '/api/issues').length
        for (let step = 0; step < 3; step++) {
          const recovery = nodes(tree()).find(node => ['button', 'a'].includes(node.type) && !disabled(node) && reconcileIntent.test(accessibleName(node, tree())))
          await this.click(recovery ? accessibleName(recovery, tree()) : 'Create issue')
          if (calls.filter(call => call.method === 'POST' && call.url.pathname === '/api/issues').length !== before) return
        }
        assert.fail('explicit recovery did not make the saved intent retrievable for reconciliation')
      },
    }
  }
  t.after(async () => {
    try {
      for (const app of apps) app.unmount()
      await settle()
      assert.deepEqual(violations, [], 'unexpected HTTP is a harness failure even when caught by production')
    } finally {
      for (const [name] of addedGlobals) delete globalThis[name]
    }
  })
  return {
    calls, query, router, mount, settle,
    api: load(new URL('composables/useOperatorApi.ts', appRoot)),
    actions: load(new URL('composables/useOperatorIssueActions.ts', appRoot)),
    setResponder(fn) { responder = fn }, setDetail(value) { issueDetail = value }, setList(value) { list = value },
    writes: () => calls.filter(call => call.method !== 'GET'),
    storedCreateIntents: () => [...tabStorage.values()].map(value => JSON.parse(value)),
    setAuthState(value) {
      const state = nuxtState.get('operator-auth')
      assert.ok(state, 'real useOperatorAuth must own the observed auth state')
      Object.assign(state.value, value)
    },
    async refreshDetail() {
      const current = query.queries.find(item => item.options.value.queryKey[0] === 'issue-detail')
      assert.ok(current, 'mounted page must use its real detail query')
      await current.refetch(); await settle()
    },
  }
}

async function fillCreate(page) {
  await page.field('Title', 'Synthetic pending intent')
  await page.field('Description', 'Synthetic pending description')
  await page.field('Target project', targetProject)
}
function createRequest(fields = {}) { return { title: 'Synthetic pending intent', body: 'Synthetic pending description', source_project: sourceProject, target_project: targetProject, ...fields } }

test('after denial and a fresh authorized read, explicitly discard quarantined local state and start fresh', async t => {
  const f = await fixture(t)
  const page = await f.mount('[id]')
  const discard = /discard.*(?:draft|local)|(?:start|begin).*fresh/i
  const bodyField = /^(?!.*(?:metadata|метадан|status|статус|edit|редакт)).*(?:comment|коммент)/i
  const metadataField = /^(?!.*(?:status|статус|edit|редакт))(?=.*(?:comment|коммент))(?=.*(?:metadata|метадан))/i
  await page.field('Change session (optional)', 'private-old-session')
  await page.field('Status outcome comment (optional)', 'private-old-status')
  await page.field(bodyField, 'private-old-comment')
  await page.field(metadataField, '{"private-old-metadata":true}')
  await page.click('Edit')
  await page.field('Title', 'private-old-title')
  f.setResponder(call => call.method === 'GET' && call.url.pathname === '/api/issues/701'
    ? json({ error: { code: 'FORBIDDEN', message: 'Denied' } }, 403) : undefined)
  f.setAuthState({ phase: 'authenticated', user: { id: 'other-user' }, authDisabled: false })
  await f.settle()
  assert.doesNotMatch(rawText(page.tree()), /private-old-/)
  assert.equal(page.hasEnabledAction(discard), false, 'discard must not be offered while the read is denied')
  assert.equal(f.writes().length, 0)

  f.setResponder(() => undefined)
  f.setDetail(detail({ issue: row({ title: 'Fresh authorized issue' }) }))
  await page.click('Read current issue again')
  assert.match(page.text(), /Fresh authorized issue/)
  assert.doesNotMatch(rawText(page.tree()), /private-old-/)
  assert.ok(page.hasEnabledAction(discard), 'a fresh authorized read must offer explicit discard')
  await page.click(discard)
  assert.equal(f.writes().length, 0, 'discard must not submit or replay anything')
  assert.doesNotMatch(rawText(page.tree()), /private-old-/)
  assert.equal(page.fieldValue('Change session (optional)'), '')
  assert.equal(page.fieldValue('Status outcome comment (optional)'), '')
  assert.equal(page.fieldValue(bodyField), '')
  assert.equal(page.fieldValue(metadataField), '{}')
  await page.click('Edit')
  assert.equal(page.fieldValue('Title'), 'Fresh authorized issue')
  assert.equal(f.writes().length, 0, 'starting a fresh edit must not submit the old draft')
})

test('quarantined state cannot be discarded while its mutation is pending', async t => {
  const f = await fixture(t)
  const page = await f.mount('[id]')
  const discard = /discard.*(?:draft|local)|(?:start|begin).*fresh/i
  let release, started
  const dispatched = new Promise(resolve => { started = resolve })
  f.setResponder(call => {
    if (call.method === 'PATCH') {
      started()
      return new Promise(resolve => { release = resolve })
    }
  })
  await page.click('Edit')
  await page.field('Title', 'private-pending-title')
  const saving = page.click('Save edit')
  try {
    await dispatched
    f.setDetail(detail({ issue: row({ title: 'Fresh authorized issue' }) }))
    f.setAuthState({ phase: 'authenticated', user: { id: 'other-user' }, authDisabled: false })
    await new Promise(setImmediate)
    await vue.nextTick()
    assert.match(page.text(), /Fresh authorized issue/)
    assert.doesNotMatch(rawText(page.tree()), /private-pending-title/)
    assert.equal(page.hasEnabledAction(discard), false, 'discard is unavailable until the in-flight mutation settles')
    assert.equal(f.writes().length, 1, 'the auth transition must not replay the mutation')
  } finally {
    release(json({ error: { code: 'REVISION_CONFLICT', message: 'Conflict' } }, 409))
    await saving
  }
})

test('initial issue read failure offers explicit retry and displays only the fresh result', async t => {
  const f = await fixture(t, { sharedCache: true })
  f.setDetail(detail({ issue: row({ title: 'Stale issue detail' }) }))
  let reads = 0
  f.setResponder(call => {
    if (call.method === 'GET' && call.url.pathname === '/api/issues/701') {
      reads++
      return reads === 1
        ? json({ error: { code: 'UNAVAILABLE', message: 'Service unavailable' } }, 503)
        : json(detail({ issue: row({ title: 'Fresh issue after retry' }) }))
    }
  })
  const page = await f.mount('[id]')
  const alert = page.find(node => node.props?.role === 'alert')
  assert.ok(alert, 'the first failed read must expose an error')
  assert.match(text(alert), /UNAVAILABLE/)
  assert.doesNotMatch(rawText(page.tree()), /Stale issue detail|Fresh issue after retry/)
  assert.equal(page.hasEnabledAction(/Read current issue again/), true)
  await page.click('Read current issue again')
  assert.match(page.text(), /Fresh issue after retry/)
  assert.doesNotMatch(rawText(page.tree()), /Stale issue detail/)
  assert.equal(reads, 2, 'only the explicit retry may issue the second read')
  assert.equal(f.writes().length, 0, 'read recovery must not dispatch a mutation')
})

test('issue detail works without randomUUID and hides cached data until a fresh remount read', async t => {
  const f = await fixture(t, { sharedCache: true, crypto: { getRandomValues: values => globalThis.crypto.getRandomValues(values) } })
  f.setDetail(detail({ issue: row({ title: 'Previously visible issue' }) }))
  const first = await f.mount('[id]')
  assert.match(first.text(), /Previously visible issue/)
  first.unmount()

  let resolveRead, signalStarted
  const started = new Promise(resolve => { signalStarted = resolve })
  const pendingRead = new Promise(resolve => { resolveRead = resolve })
  f.setResponder(call => {
    if (call.method === 'GET' && call.url.pathname === '/api/issues/701') {
      signalStarted()
      return pendingRead
    }
  })
  const reopened = await f.mount('[id]', { settleOnMount: false })
  try {
    await started
    assert.doesNotMatch(rawText(reopened.tree()), /Previously visible issue/)
  } finally {
    resolveRead(json(detail({ issue: row({ title: 'Fresh authorized issue' }) })))
    await f.settle()
  }
  assert.match(reopened.text(), /Fresh authorized issue/)
  assert.doesNotMatch(rawText(reopened.tree()), /Previously visible issue/)
  assert.equal(f.writes().length, 0, 'mounting and reading must not dispatch a mutation')
})

test('issue catalog has complete Russian, English and Chinese keys', () => {
  const keys = Object.keys(englishCatalog.issueFlow).sort()
  assert.deepEqual(Object.keys(catalogs.messages.ru.issueFlow).sort(), keys)
  assert.deepEqual(Object.keys(catalogs.issueFlowZh).sort(), keys)
  assert.deepEqual(Object.keys(catalogs.projectCatalogZh).sort(), Object.keys(englishCatalog.projectCatalog).sort())
  assert.notEqual(catalogs.issueFlowZh.statusResolved, catalogs.issueFlowZh.statusClosed)
})

test('issue language selector renders Chinese status and creation controls', async t => {
  const f = await fixture(t)
  const page = await f.mount('index')
  try {
    await page.field('Language / Язык / 语言', 'zh')
    assert.match(page.text(), /已完成 · 待验证/)
    assert.match(page.text(), /已关闭/)
    assert.match(page.text(), /所有项目/)
    assert.equal(i18n.t('projectCatalog.selectionRequired'), '继续前请从目录中选择项目。')
    assert.match(page.text(), /创建问题/)
    assert.equal(f.writes().length, 0)
  } finally {
    i18n.locale.value = 'en'
  }
})

test('reconciling a title-only conflict does not revert concurrently changed untouched fields', async t => {
  const f = await fixture(t)
  f.setDetail(detail({ issue: row({ revision: '1', body: 'Original body', metadata: { retained: 'original' } }) }))
  let writes = 0
  f.setResponder(call => {
    if (call.method !== 'PATCH') return
    if (++writes === 1) return json({ error: { code: 'REVISION_CONFLICT', message: 'Conflict' } }, 409)
    return json({ id: 701, revision: '3', changed: true, comment_id: null, message: 'issue updated' })
  })
  const page = await f.mount('[id]')
  await page.click('Edit')
  await page.field('Title', 'My title draft')
  await page.click('Save edit')
  assert.equal(f.writes().length, 1)
  assert.equal(f.writes()[0].body.expected_revision, '1')
  assert.equal(f.writes()[0].body.title, 'My title draft')
  assert.equal(Object.hasOwn(f.writes()[0].body, 'body'), false)
  assert.equal(Object.hasOwn(f.writes()[0].body, 'metadata'), false)
  f.setDetail(detail({ issue: row({ revision: '2', body: 'Concurrent body', metadata: { retained: 'concurrent', extra: true } }) }))
  await page.click('Read current issue to reconcile')
  await page.click('Use read revision for retained draft (do not submit)')
  assert.equal(f.writes().length, 1, 'reading and accepting a revision must not submit')
  assert.equal(page.fieldValue('Title'), 'My title draft')
  await page.click('Save edit')
  assert.equal(f.writes().length, 2)
  const submitted = f.writes()[1].body
  assert.equal(submitted.expected_revision, '2')
  assert.equal(submitted.title, 'My title draft')
  assert.equal(Object.hasOwn(submitted, 'body'), false, 'untouched concurrent body must not be reverted')
  assert.equal(Object.hasOwn(submitted, 'metadata'), false, 'untouched concurrent metadata must not be reverted')
})

for (const collection of ['comments', 'history']) test(`transient ${collection} continuation failure leaves recovery and the losing draft reachable`, async t => {
  const f = await fixture(t)
  const first = partialDetail()
  f.setDetail(first)
  f.setResponder(call => {
    if (call.method === 'PATCH') return json({ error: { code: 'REVISION_CONFLICT', message: 'Conflict' } }, 409)
    if (call.method === 'GET' && call.url.pathname === '/api/issues/701' && call.url.searchParams.get(`${collection}_offset`) === '50') {
      const query = f.query.queries.find(item => item.options.value.queryKey[0] === 'issue-detail')
      assert.ok(query, 'the page must use its real detail query')
      query.data.value = undefined
      return json({ error: { code: 'UNAVAILABLE', message: 'Transient failure' } }, 503)
    }
  })
  const page = await f.mount('[id]')
  await page.click('Edit')
  await page.field('Title', 'Retained recovery draft')
  await page.click('Save edit')
  assert.equal(f.writes().length, 1)
  await page.click(i18n.t('issueFlow.inspectDraft'))
  assert.match(page.text(), /Retained recovery draft/)
  await page.click(collection === 'comments' ? 'Next comments page' : 'Next history page')
  const recover = collection === 'comments' ? /previous comments page|reset|restart/i : /previous history page|reset|restart/i
  await page.click(i18n.t('issueFlow.inspectDraft'))
  assert.match(page.text(), /Retained recovery draft/, 'a transient read failure must not hide the retained draft')
  assert.ok(page.hasEnabledAction(recover), 'an explicit route back to readable data must remain reachable')
  await page.click(recover)
  assert.match(page.text(), /Synthetic task/)
  await page.click(i18n.t('issueFlow.inspectDraft'))
  assert.match(page.text(), /Retained recovery draft/)
  assert.equal(f.writes().length, 1, 'continuation recovery must not replay the draft')
})

test('a forbidden continuation does not expose the previously read issue', async t => {
  const f = await fixture(t)
  f.setDetail(partialDetail())
  f.setResponder(call => {
    if (call.method === 'GET' && call.url.pathname === '/api/issues/701' && call.url.searchParams.get('comments_offset') === '50') {
      const query = f.query.queries.find(item => item.options.value.queryKey[0] === 'issue-detail')
      assert.ok(query)
      query.data.value = undefined
      return json({ error: { code: 'FORBIDDEN', message: 'Access revoked' } }, 403)
    }
  })
  const page = await f.mount('[id]')
  assert.match(page.text(), /Synthetic task/)
  await page.click('Next comments page')
  assert.doesNotMatch(page.text(), /Synthetic task/)
  assert.equal(f.writes().length, 0)
})

test('active, awaiting-verification and closed filters request distinct states', async t => {
  const f = await fixture(t)
  const page = await f.mount('index')
  const requests = () => f.calls.filter(call => call.method === 'GET' && call.url.pathname === '/api/issues')
  assert.equal(requests().at(-1).url.searchParams.get('status'), 'open,acknowledged,reopened')
  await page.click('Done · awaiting verification')
  assert.equal(requests().at(-1).url.searchParams.get('status'), 'resolved')
  await page.click('Closed')
  assert.equal(requests().at(-1).url.searchParams.get('status'), 'closed')
  assert.equal(f.writes().length, 0)
})

for (const [name, overrides] of [
  ['missing total', { total: undefined }],
  ['invalid total', { total: -1 }],
  ['missing limit', { limit: undefined }],
  ['invalid limit', { limit: 201 }],
  ['missing offset', { offset: undefined }],
  ['invalid offset', { offset: -1 }],
  ['missing continuation', { next_offset: undefined }],
  ['invalid continuation', { next_offset: -1 }],
  ['premature completion', { total: 2, next_offset: null }],
  ['nonadvancing continuation', { total: 2, next_offset: 0 }],
]) test(`issue list exposes INVALID_RESPONSE for ${name}`, async t => {
  const f = await fixture(t)
  f.setList({ issues: [row()], total: 1, limit: 50, offset: 0, next_offset: null, ...overrides })
  const page = await f.mount('index')
  assert.match(page.text(), /INVALID_RESPONSE/)
  assert.doesNotMatch(page.text(), /Synthetic task/)
  assert.equal(f.writes().length, 0)
})

test('issue list admits an empty later offset after concurrent deletion', async t => {
  const f = await fixture(t)
  f.setList({ issues: [], total: 20, limit: 50, offset: 50, next_offset: null })
  const page = await f.api.fetchIssues({ offset: 50 })
  assert.deepEqual(page.issues, [])
  assert.equal(page.total, 20)
  assert.equal(page.next_offset, null)
})

test('session filters combine as exact attribution selectors without mutation', async t => {
  const f = await fixture(t)
  const page = await f.mount('index')
  await page.field('Creation session filter', 'source-session')
  await page.field('Participant session filter', 'participant-session')
  await page.field('Recipient session filter', 'target-session')
  const request = f.calls.filter(call => call.method === 'GET' && call.url.pathname === '/api/issues').at(-1)
  assert.equal(request.url.searchParams.get('source_session'), 'source-session')
  assert.equal(request.url.searchParams.get('participant_session'), 'participant-session')
  assert.equal(request.url.searchParams.get('target_session'), 'target-session')
  assert.equal(request.url.searchParams.get('offset'), '0')
  assert.equal(f.writes().length, 0)
})

// Controls exercise real page state/effects, not a mock echo of adapter inputs.
test('control: known create success clears the submitted draft and refreshes the visible inbox', async t => {
  const f = await fixture(t)
  f.setResponder(call => call.method === 'POST' ? json({ id: 701, revision: '1', replayed: false, message: 'issue created' }, 201) : undefined)
  const page = await f.mount('index')
  f.setList({ issues: [row({ title: 'Synthetic newly visible task' })], total: 1, limit: 50, offset: 0, next_offset: null })
  await fillCreate(page); await page.click('Create issue')
  assert.equal(f.writes().length, 1)
  assert.equal(page.fieldValue('Title'), '')
  assert.equal(page.fieldValue('Description'), '')
  assert.ok(f.query.invalidations.some(key => key[0] === 'issues'))
  assert.match(page.text(), /Synthetic newly visible task/)
})

test('compiled status admission refusal exposes its stable code without changing the read card', async t => {
  const f = await fixture(t)
  f.setDetail(detail({ issue: row({ revision: '9223372036854775806' }) }))
  f.setResponder(call => call.method === 'PATCH' ? json({ error: { code: 'INVALID_ARGUMENT', message: 'Synthetic status refusal' } }, 400) : undefined)
  const page = await f.mount('[id]')
  await page.field('Change session (optional)', 'session-change')
  await page.click('Close')
  assert.equal(f.writes().length, 1)
  assert.equal(f.writes()[0].body.expected_revision, '9223372036854775806')
  assert.equal(f.writes()[0].body.session_id, 'session-change')
  assert.match(page.text(), /INVALID_ARGUMENT/)
  assert.match(page.text(), /Synthetic task/)
  assert.ok(page.hasEnabledAction(/close|закры/i), 'refusal leaves the original card actionable')
  assert.deepEqual(f.query.invalidations, [])
})

for (const status of ['resolved', 'closed']) test(`reopen ${status} uses its read revision`, async t => {
  const f = await fixture(t)
  f.setDetail(detail({ issue: row({ status, revision: '7' }) }))
  f.setResponder(call => call.method === 'PATCH' ? json({ id: 701, revision: '8', changed: true, comment_id: null, message: 'issue updated' }) : undefined)
  const page = await f.mount('[id]')
  await page.click('Reopen')
  assert.equal(f.writes().length, 1)
  assert.equal(f.writes()[0].body.status, 'reopened')
  assert.equal(f.writes()[0].body.expected_revision, '7')
})

for (const code of ['INVALID_ARGUMENT', 'PAYLOAD_TOO_LARGE']) test(`compiled create page retains its draft and exposes ${code} after admission refusal`, async t => {
  const f = await fixture(t)
  f.setResponder(call => call.method === 'POST' ? json({ error: { code, message: 'Synthetic admission refusal' } }, code === 'PAYLOAD_TOO_LARGE' ? 413 : 400) : undefined)
  const page = await f.mount('index')
  await fillCreate(page); await page.click('Create issue')
  assert.equal(f.writes().length, 1, 'one user action must not retry a refused create')
  assert.equal(page.fieldValue('Title'), 'Synthetic pending intent')
  assert.equal(page.fieldValue('Description'), 'Synthetic pending description')
  assert.match(page.text(), new RegExp(code), 'stable admission code must be visible, not just an injected Error.message')
  assert.deepEqual(f.query.invalidations, [], 'refusal must not clear or refresh away the pending draft')
})

for (const [name, number] of [['NaN', NaN], ['positive infinity', Infinity], ['negative infinity', -Infinity]]) {
  test(`metadata ${name} must not be serialized into a different admitted value`, async t => {
    const f = await fixture(t)
    f.setResponder(call => call.method === 'POST' ? json({ id: 701, revision: '1', replayed: false }, 201) : undefined)
    await assert.rejects(() => f.api.createIssue(createRequest({ metadata: { numeric: number } })), /number|numeric|finite|integer|argument|metadata/i)
    assert.equal(f.writes().length, 0, 'known unrepresentable numeric input must fail before HTTP serialization')
  })
}

for (const boundary of ['bytes', 'depth']) test(`control: admitted metadata ${boundary} boundary reaches HTTP without truncation`, async t => {
  const f = await fixture(t)
  let admitted
  if (boundary === 'bytes') {
    const numeric = [null, false, 0.1, Number.MAX_SAFE_INTEGER, -Number.MAX_SAFE_INTEGER, 1e-300, 'quoted <data>']
    admitted = { numeric, value: 'x'.repeat(1024 * 1024 - Buffer.byteLength(JSON.stringify({ numeric, value: '' }))) }
    assert.equal(Buffer.byteLength(JSON.stringify(admitted)), 1024 * 1024)
  } else {
    admitted = {}; let cursor = admitted
    for (let container = 1; container < 256; container++) { cursor.child = {}; cursor = cursor.child }
  }
  f.setResponder(call => call.method === 'POST' ? json({ id: 701, revision: '1', replayed: false }, 201) : undefined)
  await f.api.createIssue(createRequest({ request_key: 'я'.repeat(256), metadata: admitted }))
  assert.equal(f.writes().length, 1)
  assert.deepEqual(f.writes()[0].body.metadata, admitted)
  assert.equal(Buffer.byteLength(f.writes()[0].body.request_key), 512, 'the admitted key boundary is UTF-8 bytes, not an ASCII restriction')
})

test('control: compiled create page retains fields on unknown outcome and does not auto-retry or invalidate', async t => {
  const f = await fixture(t)
  f.setResponder(call => { if (call.method === 'POST') throw new TypeError('Synthetic unknown outcome') })
  const page = await f.mount('index')
  await fillCreate(page)
  await page.click('Create issue')
  assert.equal(f.writes().length, 1)
  assert.equal(page.fieldValue('Title'), 'Synthetic pending intent')
  assert.equal(page.fieldValue('Description'), 'Synthetic pending description')
  assert.match(page.text(), /UNKNOWN_OUTCOME/, 'unknown transport wording is not a safe UI contract; the stable unknown-outcome signal must remain visible')
  assert.deepEqual(f.query.invalidations, [])
})

test('compiled create page explicitly reconciles the original unknown intent before starting a new keyed task', async t => {
  const f = await fixture(t)
  let attempts = 0
  f.setResponder(call => {
    if (call.method !== 'POST') return
    if (++attempts === 1) throw new TypeError('Synthetic unknown outcome')
    return attempts === 2
      ? json({ id: 701, revision: '1', replayed: true, message: 'issue replayed' })
      : json({ id: 702, revision: '1', replayed: false, message: 'issue created' }, 201)
  })
  const page = await f.mount('index')
  await fillCreate(page)
  await page.field('Creation session (optional)', 'session-source')
  await page.field('Recipient session (optional)', 'session-target')
  await page.click('Create issue')
  assert.equal(f.writes().length, 1, 'an unknown outcome must not automatically retry')
  const first = copy(f.writes()[0].body)
  assert.equal(typeof first.request_key, 'string', 'page must own the intent key before dispatch')
  assert.ok(first.request_key.length > 0)
  assert.ok(Buffer.byteLength(first.request_key) <= 512)
  assert.equal(first.created_by_session, 'session-source')
  assert.equal(first.target_session, 'session-target')
  await page.reconcileCreate()
  assert.equal(f.writes().length, 2, 'an explicit recovery action must retrieve and reconcile the pending intent, not indefinitely block it')
  const retry = f.writes()[1].body
  for (const field of ['request_key', 'expected_actor', 'title', 'body', 'target_project', 'source_project', 'created_by_session', 'target_session', 'type', 'priority', 'labels', 'metadata', 'owner_label', 'recipient_label']) {
    assert.deepEqual(retry[field], first[field], `reconciliation preserves original create field ${field}`)
  }
  await fillCreate(page)
  await page.field('Title', 'Deliberately new synthetic task')
  await page.click('Create issue')
  assert.equal(f.writes().length, 3)
  assert.equal(f.writes()[2].body.title, 'Deliberately new synthetic task')
  assert.notEqual(f.writes()[2].body.request_key, first.request_key, 'only a deliberately new intent after reconciliation gets a new key')
})

test('editing after unknown create still leaves the original pending intent explicitly recoverable', async t => {
  const f = await fixture(t)
  let attempts = 0
  f.setResponder(call => {
    if (call.method !== 'POST') return
    if (++attempts === 1) throw new TypeError('Synthetic unknown outcome')
    return json({ id: 701, revision: '1', replayed: true, message: 'issue replayed' })
  })
  const page = await f.mount('index')
  await fillCreate(page)
  const rawMetadata = '{"numeric":1.0, "nested": {"beta":false,"alpha":null}}'
  await page.field(/^(?!.*comment).*metadata/i, rawMetadata)
  await page.click('Create issue')
  const original = copy(f.writes()[0].body)
  assert.ok(carriesExactRawMetadata(f.writes()[0].serializedBody, rawMetadata), 'the original HTTP request preserves raw metadata spelling')
  if (page.fieldEditable('Title')) await page.field('Title', 'Different synthetic task')
  assert.equal(f.writes().length, 1, 'editing the draft cannot silently dispatch another intent')
  await page.reconcileCreate()
  assert.equal(f.writes().length, 2, 'the original intent must remain actionable despite a changed form')
  for (const field of ['request_key', 'title', 'body', 'target_project', 'source_project', 'type', 'priority', 'labels', 'metadata', 'owner_label', 'recipient_label']) {
    assert.deepEqual(f.writes()[1].body[field], original[field], `mutable form cannot replace pending ${field}`)
  }
  assert.ok(carriesExactRawMetadata(f.writes()[1].serializedBody, rawMetadata), 'explicit lost-response recovery preserves the original raw fragment, not a parsed/stringified replacement')
})

// Added after the page implementation began: post-hoc regression, not claimed RED.
test('compiled create keeps the original actor expectation and intent when auth changes between auth-me and POST', async t => {
  const f = await fixture(t)
  const originalActor = { kind: 'operator', reference: 'operator:master' }
  const otherActor = { kind: 'operator', reference: 'operator:session' }
  let actualActor = originalActor
  let firstAuthRead = true
  let posts = 0
  f.setResponder(call => {
    if (call.method === 'GET' && call.url.pathname === '/api/auth/me') {
      const response = json({ actor: actualActor })
      if (firstAuthRead) { firstAuthRead = false; actualActor = otherActor }
      return response
    }
    if (call.method !== 'POST' || call.url.pathname !== '/api/issues') return
    assert.deepEqual(call.body.expected_actor, originalActor, 'the create precondition is the trusted original descriptor, not a relabelled actor')
    return ++posts === 1
      ? json({ error: { code: 'ACTOR_CHANGED', message: 'Authentication changed; reconcile the original intent.' } }, 409)
      : json({ id: 701, revision: '1', replayed: false, message: 'issue created' }, 201)
  })
  const page = await f.mount('index')
  await fillCreate(page); await page.click('Create issue')
  assert.equal(f.writes().length, 1, 'actor mismatch must not cause automatic replay or another create')
  assert.match(page.text(), /ACTOR_CHANGED/)
  const first = copy(f.writes()[0].body)
  const saved = f.storedCreateIntents()
  assert.equal(saved.length, 1)
  assert.deepEqual(saved[0].actor, originalActor)
  assert.equal(saved[0].intent.request_key, first.request_key)
  for (const field of ['title', 'body', 'source_project', 'target_project', 'type', 'priority', 'labels', 'metadata', 'owner_label', 'recipient_label']) {
    const retained = field === 'metadata' && saved[0].intent.metadataRaw !== undefined ? JSON.parse(saved[0].intent.metadataRaw) : saved[0].intent[field]
    assert.deepEqual(retained, first[field], `actor-switch preserves the original pending ${field}`)
  }
  if (saved[0].intent.metadataRaw !== undefined) assert.ok(carriesExactRawMetadata(f.writes()[0].serializedBody, saved[0].intent.metadataRaw), 'the retained raw metadata is exactly the submitted fragment')
  await page.click('Check recovery context again')
  assert.equal(f.writes().length, 1, 'another authenticated actor cannot replay the original receipt or generate a replacement')
  assert.deepEqual(f.storedCreateIntents(), saved)
  assert.equal(page.hasEnabledAction(/^Create issue$/), false)
  assert.equal(page.fieldValue('Title'), '', 'the other actor is not shown the stored private draft')
  actualActor = originalActor
  await page.click('Check recovery context again')
  assert.equal(page.fieldValue('Title'), first.title)
  assert.equal(f.writes().length, 1, 'restoring the original actor reads recovery state but never auto-submits')
  await page.reconcileCreate()
  assert.equal(f.writes().length, 2)
  assert.deepEqual(f.writes()[1].body, first, 'explicit recovery preserves actor expectation, key, canonical source and every original field')
})

test('prepared create intent keeps its original actor after the caller mutates the source object', async t => {
  const f = await fixture(t)
  const actor = { kind: 'operator', reference: 'operator:master' }
  let posts = 0
  f.setResponder(call => {
    if (call.method !== 'POST' || call.url.pathname !== '/api/issues') return
    if (++posts === 1) throw new TypeError('Synthetic lost response')
    return json({ id: 701, revision: '1', replayed: true, message: 'issue replayed' })
  })
  const intent = await f.api.prepareIssueCreateIntent(createRequest({ request_key: 'synthetic-mutable-actor', expected_actor: actor }))
  actor.kind = 'client'
  actor.reference = 'synthetic-different-actor'
  await assert.rejects(() => f.api.createIssue(intent), /UNKNOWN_OUTCOME/)
  await f.api.createIssue(intent)
  assert.equal(f.writes().length, 2, 'one explicit caller retry is not an automatic retry')
  for (const request of f.writes()) {
    assert.deepEqual(request.body.expected_actor, { kind: 'operator', reference: 'operator:master' }, 'actual HTTP retains the original actor despite caller mutation')
    assert.equal(request.body.request_key, 'synthetic-mutable-actor')
    assert.equal(request.body.source_project, sourceProject)
    assert.equal(request.body.title, 'Synthetic pending intent')
  }
  assert.deepEqual(f.writes()[1].body, f.writes()[0].body, 'lost-response recovery sends the exact same original intent')
})

test('stale auth-me recovery response cannot reveal or replay a pending draft after an observed auth-state change', async t => {
  const f = await fixture(t)
  f.setResponder(call => {
    if (call.method === 'POST' && call.url.pathname === '/api/issues') throw new TypeError('Synthetic lost response')
    if (call.method === 'GET' && call.url.pathname === '/api/auth/me') return json({ actor: { kind: 'operator', reference: 'operator:master' } })
  })
  const originalPage = await f.mount('index')
  await fillCreate(originalPage); await originalPage.click('Create issue')
  const saved = f.storedCreateIntents()
  assert.equal(f.writes().length, 1)
  const resumedPage = await f.mount('index')
  assert.equal(resumedPage.fieldValue('Title'), 'Synthetic pending intent', 'the real restore path copied the retained title into the form')
  assert.equal(resumedPage.fieldValue('Description'), 'Synthetic pending description')
  let releaseOldAuth = null
  f.setResponder(call => {
    if (call.method === 'GET' && call.url.pathname === '/api/auth/me') {
      return new Promise(resolve => { releaseOldAuth = () => resolve(json({ actor: { kind: 'operator', reference: 'operator:master' } })) })
    }
  })
  const restoredPage = await f.mount('index')
  assert.equal(typeof releaseOldAuth, 'function', 'actual restore must be waiting for its auth-me response')
  f.setAuthState({ phase: 'loading' })
  f.setAuthState({ phase: 'authenticated', user: { id: 'synthetic-other-browser-user' }, authDisabled: false })
  await f.settle()
  assert.equal(resumedPage.fieldValue('Title'), '', 'observed auth change clears the independently restored title immediately')
  assert.equal(resumedPage.fieldValue('Description'), '', 'observed auth change clears the independently restored body immediately')
  assert.deepEqual(f.storedCreateIntents(), saved, 'clearing displayed copies never deletes the saved intent')
  releaseOldAuth()
  await f.settle()
  assert.equal(restoredPage.fieldValue('Title'), '', 'a stale auth reply must not disclose the persisted title')
  assert.equal(restoredPage.fieldValue('Description'), '', 'a stale auth reply must not disclose the persisted body')
  assert.equal(restoredPage.hasEnabledAction(/^Create issue$/), false)
  assert.equal(restoredPage.hasEnabledAction(/^Reconcile original create intent$/), false, 'old authorization cannot unlock a replay action')
  assert.equal(f.writes().length, 1, 'auth-state invalidation never sends the pending intent')
  assert.deepEqual(f.storedCreateIntents(), saved, 'the original actor, expectation, source, key and full payload remain retained')
  assert.match(restoredPage.text(), /ACTOR_CHANGED/)
})

const materialMetadataField = /^(?!.*(?:comment|коммент)).*(?:metadata|метадан)/i
const createPurpose = /^(?:create|создать).*(?:issue|task|задач)/i
const savePurpose = /^(?:save|сохранить|применить)/i

async function metadataPage(f, path) {
  const page = await f.mount(path === 'create' ? 'index' : '[id]')
  if (path === 'create') await fillCreate(page)
  else await page.click(/^(?:edit|редакт)/i)
  return page
}

function carriesExactRawMetadata(serialized, raw, memberName = 'metadata') {
  if (typeof serialized !== 'string') return false
  const member = memberName === 'comment_metadata' ? /"comment_metadata"\s*:\s*/g : /"metadata"\s*:\s*/g
  while (member.exec(serialized)) {
    if (serialized.slice(member.lastIndex, member.lastIndex + raw.length) === raw) return true
  }
  return false
}

// Post-hoc for T016 overall, pre-fix for the separately reported raw-input defect.
for (const path of ['create', 'material edit']) {
  for (const [name, raw] of [
    ['lossy decimal', '{"numeric":0.10000000000000001}'],
    ['duplicate member', '{"field":1,"field":2}'],
  ]) test(`raw metadata ${name} is refused without normalization through the ${path} page`, async t => {
    const f = await fixture(t)
    f.setResponder(call => ['POST', 'PATCH'].includes(call.method) && /^\/api\/issues(?:\/701)?$/.test(call.url.pathname)
      ? json({ error: { code: 'INVALID_ARGUMENT', message: 'Synthetic raw metadata admission refusal' } }, 400)
      : undefined)
    const page = await metadataPage(f, path)
    await page.field(materialMetadataField, raw)
    await page.click(path === 'create' ? createPurpose : savePurpose)
    const writes = f.writes()
    assert.ok(writes.length <= 1, 'one explicit action may reject locally or send one request, never retry automatically')
    if (writes.length === 1) {
      assert.ok(carriesExactRawMetadata(writes[0].serializedBody, raw), 'the actual serialized HTTP JSON must contain the original raw metadata object, not rounded numbers or collapsed duplicate members')
    }
    assert.equal(page.fieldValue(materialMetadataField), raw, 'admission refusal preserves the exact user draft')
    assert.match(page.text(), /INVALID_ARGUMENT/, 'local or fixed HTTP admission refusal remains explicit and safe')
    assert.deepEqual(f.query.invalidations, [], 'refusal cannot claim a mutation or refresh away the draft')
  })

  test(`raw metadata object-tail injection is refused locally through the ${path} page`, async t => {
    const f = await fixture(t)
    const raw = '{"ok":1},"status":"closed"'
    f.setResponder(call => ['POST', 'PATCH'].includes(call.method) && /^\/api\/issues(?:\/701)?$/.test(call.url.pathname)
      ? json({ error: { code: 'INVALID_ARGUMENT', message: 'Synthetic malformed JSON refusal' } }, 400)
      : undefined)
    const page = await metadataPage(f, path)
    await page.field(materialMetadataField, raw)
    await page.click(path === 'create' ? createPurpose : savePurpose)
    assert.equal(f.writes().length, 0, 'an object followed by an injected field is not one full JSON object and must never dispatch')
    assert.equal(page.fieldValue(materialMetadataField), raw, 'local refusal preserves the complete user draft for correction')
    assert.match(page.text(), /INVALID_ARGUMENT/)
    assert.deepEqual(f.query.invalidations, [], 'an injection refusal cannot claim a mutation')
  })

  test(`control: admitted raw metadata reaches the ${path} HTTP request intact as data`, async t => {
    const f = await fixture(t)
    const raw = '{"numeric":0.1,"nested":{"done":false},"list":[null,1],"field":2}'
    f.setResponder(call => {
      if (call.method === 'POST' && call.url.pathname === '/api/issues') return json({ id: 701, revision: '1', replayed: false, message: 'issue created' }, 201)
      if (call.method === 'PATCH' && call.url.pathname === '/api/issues/701') return json({ id: 701, revision: '2', changed: true, comment_id: null, message: 'issue updated' })
    })
    const page = await metadataPage(f, path)
    await page.field(materialMetadataField, raw)
    await page.click(path === 'create' ? createPurpose : savePurpose)
    assert.equal(f.writes().length, 1, 'admitted metadata must not be blocked by a blanket refusal')
    assert.deepEqual(JSON.parse(f.writes()[0].serializedBody).metadata, JSON.parse(raw), 'admitted values and unknown metadata keys survive actual HTTP serialization')
  })
}

test('comment metadata retains its raw fragment across unknown outcome and requires explicit append reconciliation', async t => {
  const f = await fixture(t)
  const body = 'Synthetic outcome comment with raw metadata'
  const raw = '{"numeric":1.0, "nested": {"beta":false,"alpha":null}}'
  const bodyField = /^(?!.*(?:metadata|метадан|status|статус|edit|редакт)).*(?:comment|коммент)/i
  const metadataField = /^(?!.*(?:status|статус|edit|редакт))(?=.*(?:comment|коммент))(?=.*(?:metadata|метадан))/i
  const sendPurpose = /^(?:send|отправить|добавить).*(?:comment|коммент)/i
  let appends = 0
  f.setResponder(call => {
    if (call.method !== 'PATCH' || call.url.pathname !== '/api/issues/701') return
    if (++appends === 1) throw new TypeError('Synthetic lost comment response')
    return json({ id: 701, revision: '1', changed: false, comment_id: 77, message: 'issue updated' })
  })
  const page = await f.mount('[id]')
  await page.field(bodyField, body)
  await page.field(metadataField, raw)
  await page.click(sendPurpose)
  assert.equal(f.writes().length, 1, 'an unknown comment outcome must not automatically append again')
  assert.ok(carriesExactRawMetadata(f.writes()[0].serializedBody, raw, 'comment_metadata'), 'the actual comment HTTP body preserves the original raw object spelling')
  assert.deepEqual(JSON.parse(f.writes()[0].serializedBody).comment_metadata, JSON.parse(raw))
  assert.equal(page.fieldValue(bodyField), body)
  assert.equal(page.fieldValue(metadataField), raw)
  assert.equal(page.hasEnabledAction(sendPurpose), false, 'another append remains held until explicit reconciliation')
  assert.match(page.text(), /UNKNOWN_OUTCOME/)
  await page.click(/(?:read|прочит).*?(?:comment|коммент).*?(?:reconcil|свер)/i)
  assert.equal(f.writes().length, 1, 'reading current comments is not an automatic retry')
  assert.equal(page.fieldValue(metadataField), raw, 'a fresh read cannot replace the pending comment metadata')
  assert.equal(page.hasEnabledAction(sendPurpose), false)
  await page.click(/checked.*comments.*allow|провер.*коммент.*разреш/i)
  assert.equal(f.writes().length, 1, 'acknowledging reconciliation alone does not append')
  assert.match(page.text(), /not deduplicated|не.*дедуп/i, 'the UI does not promise receipt deduplication for comments')
  await page.click(sendPurpose)
  assert.equal(f.writes().length, 2, 'a second append requires a separate explicit user send')
  assert.equal(f.writes()[1].body.comment, body)
  assert.ok(carriesExactRawMetadata(f.writes()[1].serializedBody, raw, 'comment_metadata'), 'the explicitly reconciled append retains the original raw metadata text')
})

async function losingDraft(t) {
  const f = await fixture(t)
  const page = await f.mount('[id]')
  await page.click('Edit'); await page.field('Title', 'Synthetic losing draft')
  f.setDetail(detail({ issue: row({ revision: '2', title: 'Synthetic winning title' }) }))
  await f.refreshDetail()
  f.setResponder(call => call.method === 'PATCH' ? json({ error: { code: 'REVISION_CONFLICT', message: 'Another writer saved first', current_revision: '2' } }, 409) : undefined)
  await page.click('Save edit')
  return { f, page }
}

test('control: real losing page draft survives a 409 and does not silently retry or claim success', async t => {
  const { f, page } = await losingDraft(t)
  assert.equal(f.writes().length, 1)
  assert.equal(f.writes()[0].body.title, 'Synthetic losing draft')
  assert.equal(page.fieldValue('Title'), 'Synthetic losing draft')
  assert.match(page.text(), /Synthetic winning title/)
  assert.match(page.text(), /REVISION_CONFLICT/, 'conflict guidance must be safe and code-based, not arbitrary server prose')
  assert.doesNotMatch(page.text(), /Issue fields updated\./)
  assert.deepEqual(f.query.invalidations, [])
})

test('compiled edit page submits the original base revision after a winner refresh, never the newer cache revision', async t => {
  const { f, page } = await losingDraft(t)
  assert.equal(f.writes()[0].body.expected_revision, '1', 'the edit started at revision 1; refreshed revision 2 cannot rebase the losing draft')
  assert.equal(page.fieldValue('Title'), 'Synthetic losing draft')
})

test('compiled edit page exposes stable conflict code with its losing draft', async t => {
  const { page } = await losingDraft(t)
  assert.match(page.text(), /REVISION_CONFLICT/)
  assert.equal(page.fieldValue('Title'), 'Synthetic losing draft')
})

test('compiled edit page can explicitly clear an existing body instead of claiming PATCH cannot do it', async t => {
  const f = await fixture(t)
  f.setResponder(call => call.method === 'PATCH' ? json({ id: 701, revision: '2', changed: true, comment_id: null, message: 'issue updated' }) : undefined)
  const page = await f.mount('[id]')
  await page.click('Edit'); await page.field('Description', ''); await page.click('Save edit')
  assert.equal(f.writes().length, 1, 'an explicit empty body is an admitted edit, not an obsolete UI refusal')
  assert.equal(f.writes()[0].body.body, '')
  assert.equal(f.writes()[0].body.expected_revision, '1')
})

test('compiled status action preserves the read decimal revision beyond JS integer precision', async t => {
  const f = await fixture(t)
  f.setDetail(detail({ issue: row({ revision: '9223372036854775806' }) }))
  f.setResponder(call => call.method === 'PATCH' ? json({ id: 701, revision: '9223372036854775807', changed: true, comment_id: null, message: 'issue updated' }) : undefined)
  const page = await f.mount('[id]')
  await page.click('Close')
  assert.equal(f.writes().length, 1)
  assert.equal(f.writes()[0].body.status, 'closed')
  assert.equal(f.writes()[0].body.expected_revision, '9223372036854775806', 'the actual page must not round or replace its read revision')
})

test('the actual detail query opts into bounded paged detail, not legacy all-comments', async t => {
  const f = await fixture(t)
  await f.mount('[id]')
  const read = f.calls.find(call => call.url.pathname === '/api/issues/701')
  assert.ok(read, 'real useIssueDetailQuery must call real fetchIssue')
  assert.equal(read.url.searchParams.get('detail_mode'), 'paged')
  for (const field of ['comments_limit', 'history_limit']) {
    const limit = read.url.searchParams.get(field)
    if (limit !== null) assert.ok(Number.isInteger(Number(limit)) && Number(limit) >= 1 && Number(limit) <= 200)
  }
  assert.equal(f.calls.filter(call => call.url.pathname === '/api/issues/701').length, 1, 'no automatic unbounded detail drain')
})

function partialDetail() {
  return detail({ comments: Array.from({ length: 50 }, (_, index) => comment(index + 1)), history: Array.from({ length: 50 }, (_, index) => history(String(index + 1))), comment_count: 52, history_count: 51, comments_next_offset: 50, history_next_offset: 50, comments_complete: false, history_complete: false })
}

test('compiled detail displays returned comment/history ranges against authorized totals, not page length as total', async t => {
  const f = await fixture(t); f.setDetail(partialDetail())
  const page = await f.mount('[id]')
  const display = page.text()
  assert.match(display, /52/, 'comments total comes from comment_count')
  assert.match(display, /51/, 'history total comes from history_count')
  assert.doesNotMatch(display, /\b50\s+total\b/, 'the first 50 comments are not all 52 comments')
  assert.match(display, /(?:1\s*(?:[-–—]|to|through|из|по)\s*50|\[?0\s*[,–-]\s*50\)?)/, 'returned comment range is visible in one-based or half-open notation')
  assert.match(display, /history|истори/i, 'history is not discarded by the page')
})

// Concrete DTO: accepted contracts/issues.md:55-59, contract HEAD e9467583.
// The transport returns static pages; it does not implement server paging.
test('compiled detail has independent bounded continuations and keeps earlier comments reachable', async t => {
  const f = await fixture(t); const first = partialDetail(); f.setDetail(first)
  const page = await f.mount('[id]')
  const commentPages = new Map([
    [0, { comments: first.comments, comments_offset: 0, comments_next_offset: 50, comments_complete: false }],
    [50, { comments: [comment(51), comment(52)], comments_offset: 50, comments_next_offset: null, comments_complete: true }],
  ])
  const historyPages = new Map([
    [0, { history: first.history, history_offset: 0, history_next_offset: 50, history_complete: false }],
    [50, { history: [history('51')], history_offset: 50, history_next_offset: null, history_complete: true }],
  ])
  f.setResponder(call => {
    if (call.method !== 'GET' || call.url.pathname !== '/api/issues/701') return
    const comments = commentPages.get(Number(call.url.searchParams.get('comments_offset') ?? 0))
    const events = historyPages.get(Number(call.url.searchParams.get('history_offset') ?? 0))
    assert.ok(comments && events, 'request must identify known static comment/history pages')
    return json(detail({ ...comments, ...events, comment_count: 52, history_count: 51 }))
  })
  const before = f.calls.length
  await page.click(commentNext)
  const commentReads = f.calls.slice(before).filter(call => call.url.pathname === '/api/issues/701')
  assert.equal(commentReads.length, 1, 'one explicit load-more does not drain all remaining pages')
  assert.equal(commentReads[0].url.searchParams.get('detail_mode'), 'paged')
  assert.equal(commentReads[0].url.searchParams.get('comments_offset'), '50')
  assertComments(page, [51, 52])
  await accessEarlierComments(page, Array.from({ length: 50 }, (_, index) => index + 1))
  const afterComments = f.calls.length
  await page.click(historyNext)
  const historyReads = f.calls.slice(afterComments).filter(call => call.url.pathname === '/api/issues/701')
  assert.equal(historyReads.length, 1)
  assert.equal(historyReads[0].url.searchParams.get('detail_mode'), 'paged')
  assert.equal(historyReads[0].url.searchParams.get('history_offset'), '50')
})

test('control: oversized comment read stays intact without automatic continuation draining', async t => {
  const f = await fixture(t)
  const body = 'Synthetic oversized historical record. ' + 'x'.repeat(4 * 1024 * 1024)
  f.setDetail(detail({ comments: [comment(1, body)], comment_count: 2, comments_next_offset: 1, comments_complete: false, comments_oversized: true }))
  await f.mount('[id]')
  const loaded = f.query.queries.find(query => query.options.value.queryKey[0] === 'issue-detail')?.data.value
  assert.equal(loaded?.comments[0].body, body, 'the real detail reader retains all admitted historical bytes')
  assert.equal(f.calls.filter(call => call.url.pathname === '/api/issues/701').length, 1)
})

test('control: ordinary complete comments render actual content and zero extra detail reads', async t => {
  const f = await fixture(t)
  f.setDetail(detail({ comments: [comment(1, 'Synthetic complete comment')], comment_count: 1 }))
  const page = await f.mount('[id]')
  assert.match(page.text(), /Synthetic complete comment/)
  assert.equal(f.calls.filter(call => call.url.pathname === '/api/issues/701').length, 1)
})

test('the final comment page stops continuation without making earlier pages unreachable', async t => {
  const f = await fixture(t)
  const first = detail({ comments: Array.from({ length: 50 }, (_, index) => comment(index + 1)), comment_count: 51, comments_next_offset: 50, comments_complete: false })
  f.setDetail(first)
  const page = await f.mount('[id]')
  f.setResponder(call => {
    if (call.method !== 'GET' || call.url.pathname !== '/api/issues/701') return
    const offset = Number(call.url.searchParams.get('comments_offset') ?? 0)
    assert.ok(offset === 0 || offset === 50)
    assert.equal(Number(call.url.searchParams.get('history_offset') ?? 0), 0)
    return json(offset === 0 ? first : detail({ comments: [comment(51)], comment_count: 51, comments_offset: 50 }))
  })
  await page.click(commentNext)
  assertComments(page, [51])
  assert.match(page.text(), /51/, 'the authorized collection total remains visible on its singleton page')
  assert.equal(page.hasEnabledAction(commentNext), false, 'no later comments remain; previous-page navigation may still be available')
  assert.equal(f.calls.filter(call => call.url.pathname === '/api/issues/701').length, 2)
  await accessEarlierComments(page, Array.from({ length: 50 }, (_, index) => index + 1))
})

test('an empty later page after concurrent deletion keeps earlier comments reachable without a backwards range', async t => {
  const f = await fixture(t)
  const first = detail({ comments: Array.from({ length: 50 }, (_, index) => comment(index + 1)), comment_count: 51, comments_next_offset: 50, comments_complete: false })
  f.setDetail(first)
  const page = await f.mount('[id]')
  f.setResponder(call => {
    if (call.method !== 'GET' || call.url.pathname !== '/api/issues/701') return
    const offset = Number(call.url.searchParams.get('comments_offset') ?? 0)
    assert.ok(offset === 0 || offset === 50)
    assert.equal(Number(call.url.searchParams.get('history_offset') ?? 0), 0)
    // Record 51 disappeared between the two ordinary live-offset reads.
    return json(offset === 0 ? { ...first, comment_count: 50, comments_next_offset: null, comments_complete: true } : detail({ comment_count: 50, comments_offset: 50 }))
  })
  await page.click(commentNext)
  assert.match(page.text(), /50/, 'an empty page is not an empty authorized collection')
  assert.doesNotMatch(page.text(), /51\s*[-–—]\s*50/)
  assert.equal(page.hasEnabledAction(commentNext), false)
  assert.equal(f.calls.filter(call => call.url.pathname === '/api/issues/701').length, 2)
  await accessEarlierComments(page, Array.from({ length: 50 }, (_, index) => index + 1))
})

for (const collection of ['comments', 'history']) test(`compiled detail marks an intact oversized ${collection} singleton explicitly`, async t => {
  const f = await fixture(t)
  const body = `Synthetic historical ${collection} content. ` + 'x'.repeat(4 * 1024 * 1024)
  f.setDetail(collection === 'comments'
    ? detail({ comments: [comment(1, body)], comment_count: 2, comments_next_offset: 1, comments_complete: false, comments_oversized: true })
    : detail({ history: [{ ...history('1'), changes: { body: { after: body } } }], history_count: 2, history_next_offset: 1, history_complete: false, history_oversized: true }))
  const page = await f.mount('[id]')
  const loaded = f.query.queries.find(query => query.options.value.queryKey[0] === 'issue-detail')?.data.value
  assert.equal(collection === 'comments' ? loaded?.comments[0].body : loaded?.history[0].changes.body.after, body, 'real detail admission keeps authorized historical values intact')
  assert.match(page.text(), /oversized|large record|4\s*MiB|превыш|больш.*запис/i, 'response oversized indication must be visible')
  // A full inline view, native disclosure, or accessible raw/download link are
  // equivalent access paths; the default page need not expand a >4 MiB value.
  function accessPath() {
    const disclosure = page.find(node => node.type === 'details' && rawText(node).includes(body) && nodes(node).some(child => child.type === 'summary' && accessibleName(child, page.tree())))
    const rawAccess = page.find(node => {
      if (node.type !== 'a' || !accessibleName(node, page.tree()) || typeof node.props?.href !== 'string') return false
      const href = node.props.href
      if (href.startsWith('data:')) {
        const separator = href.indexOf(',')
        const payload = href.slice(separator + 1)
        const content = href.slice(0, separator).endsWith(';base64') ? Buffer.from(payload, 'base64').toString('utf8') : decodeURIComponent(payload)
        return content.includes(body)
      }
      const url = new URL(href, 'http://synthetic.invalid')
      return url.origin === 'http://synthetic.invalid' && url.pathname === '/api/issues/701' && url.searchParams.get('detail_mode') === 'paged' && Number(url.searchParams.get(`${collection}_offset`) ?? 0) === 0
    })
    return page.text().includes(body) || disclosure || rawAccess
  }
  if (!accessPath()) {
    await page.click(/(?:show|view|inspect|open|download|показ|просмотр|откры|скача).*(?:record|history|comment|value|запис|истори|коммент|значен)|(?:record|history|comment|value|запис|истори|коммент|значен).*(?:show|view|inspect|open|download|показ|просмотр|откры|скача)/i)
  }
  assert.ok(accessPath(), 'an explicit disclosure, full view or raw/download link must expose the intact record; invisible cache retention alone is insufficient')
  assert.equal(f.calls.filter(call => call.url.pathname === '/api/issues/701').length, 1)
})
