import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import * as projectCatalog from './projectCatalog.ts'

const ts = createRequire(new URL('../../../apps/operator-console/package.json', import.meta.url))('typescript')
const { parseProjectCatalog, parseProjectResolution, parseRetainedPage, projectLabel, parseReaderPage, credentialBinding, assertCredentialMetadata } = projectCatalog

const first = '11111111-1111-4111-8111-111111111111'
const second = '22222222-2222-4222-8222-222222222222'
const items = [
  { project_id: first, name: 'Workspace', repository_qualifier: 'team-a/repository' },
  { project_id: second, name: 'Workspace', repository_qualifier: 'team-b/repository' },
]

test('catalog retains distinct IDs and separates opaque unresolved membership', () => {
  const catalog = parseProjectCatalog({ items, unresolved: [{ reference: 'retained/old-worktree', name: 'Old checkout', reason: 'Mapping is ambiguous', reader_kinds: ['memory'] }] })
  assert.deepEqual(catalog.items.map(item => item.project_id), [first, second])
  assert.equal(catalog.unresolved[0].reference, 'retained/old-worktree')
  assert.deepEqual(catalog.unresolved[0].reader_kinds, ['memory'])
  assert.equal(projectLabel(catalog.items, first, 'Unknown', 'Ambiguous'), 'Workspace · team-a/repository')
  assert.equal(projectLabel(catalog.items, second, 'Unknown', 'Ambiguous'), 'Workspace · team-b/repository')
})

test('catalog requires exact reader metadata and preserves metadata-only groups', () => {
  const row = { reference: 'registry-only', name: 'Registry metadata', reason: 'locator_is_not_identity', reader_kinds: [] }
  assert.deepEqual(parseProjectCatalog({ items, unresolved: [row] }).unresolved, [row])
  for (const reader_kinds of [undefined, null, ['native'], ['credential'], ['memory', 'memory']]) {
    assert.throws(() => parseProjectCatalog({ items, unresolved: [{ ...row, reader_kinds }] }))
  }
  const mixed = { ...row, reference: 'mixed', reader_kinds: ['document', 'issue', 'memory', 'code'] }
  assert.deepEqual(parseProjectCatalog({ items, unresolved: [mixed, row] }).unresolved, [mixed, row])
})

test('legacy string arrays, duplicate IDs and non-UUID project choices refuse', () => {
  assert.throws(() => parseProjectCatalog(['engram', 'D:/Dev/engram']))
  assert.throws(() => parseProjectCatalog({ items: [...items, items[0]], unresolved: [] }))
  assert.throws(() => parseProjectCatalog({ items: [{ project_id: 'worktree-name', name: 'Workspace' }], unresolved: [] }))
  const equalNames = items.map(({ project_id, name }) => ({ project_id, name }))
  assert.equal(projectLabel(equalNames, first, 'Unknown', 'Ambiguous'), 'Workspace #1')
  assert.equal(projectLabel(equalNames, second, 'Unknown', 'Ambiguous'), 'Workspace #2')
  assert.equal(projectLabel(equalNames, 'legacy/path', 'Unknown', 'Ambiguous'), 'Unknown')
})

for (const qualifier of [undefined, 'team/repository']) {
  test(`same-name labels remain distinct and UUID-stable with ${qualifier ?? 'no qualifier'}`, () => {
    const catalog = parseProjectCatalog({
      items: items.map(({ project_id, name }) => ({ project_id, name, ...(qualifier ? { repository_qualifier: qualifier } : {}) })),
      unresolved: [],
    })
    const before = structuredClone(catalog)
    const labels = catalog.items.map(item => projectLabel(catalog.items, item.project_id, 'Unknown', 'Ambiguous'))
    const base = qualifier ? `Workspace · ${qualifier}` : 'Workspace'
    assert.deepEqual(labels, [`${base} #1`, `${base} #2`])
    assert.notEqual(labels[0], labels[1])
    const reversed = [...catalog.items].reverse()
    assert.equal(projectLabel(reversed, first, 'Unknown', 'Ambiguous'), labels[0])
    assert.equal(projectLabel(reversed, second, 'Unknown', 'Ambiguous'), labels[1])
    assert.deepEqual(catalog, before)
    assert.deepEqual(catalog.items.map(item => item.project_id), [first, second])
    assert.deepEqual(catalog.unresolved, [])
    const credentials = catalog.items.map((item, index) => credentialBinding({ id: index + 1, name: 'same', scope: 'project', project_id: item.project_id }))
    assert.deepEqual(credentials.map(credential => credential.project_id), [first, second])
    assert.deepEqual(credentials.map(credential => projectLabel(reversed, credential.project_id, 'Unknown', 'Ambiguous')), labels)
  })
}

test('ordinals apply only to colliding visible base labels, not equal names alone', () => {
  const mixed = [{ project_id: first, name: 'Workspace' }, items[1]]
  assert.equal(projectLabel(mixed, first, 'Unknown', 'Ambiguous'), 'Workspace')
  assert.equal(projectLabel(mixed, second, 'Unknown', 'Ambiguous'), 'Workspace · team-b/repository')
  const equalVisible = [{ project_id: first, name: 'Workspace · team-b/repository' }, items[1]]
  assert.equal(projectLabel(equalVisible, first, 'Unknown', 'Ambiguous'), 'Workspace · team-b/repository #1')
  assert.equal(projectLabel(equalVisible, second, 'Unknown', 'Ambiguous'), 'Workspace · team-b/repository #2')
})

test('resolution admits only proven canonical or explicit unresolved responses', () => {
  assert.deepEqual(parseProjectResolution({ status: 'resolved', project_id: first, name: 'Renamed workspace' }), { status: 'resolved', project_id: first, name: 'Renamed workspace' })
  assert.deepEqual(parseProjectResolution({ status: 'unresolved', reference: 'old-membership', reason: 'Not mapped' }), { status: 'unresolved', reference: 'old-membership', reason: 'Not mapped' })
  assert.throws(() => parseProjectResolution({ status: 'resolved', project_id: 'engram', name: 'Workspace' }))
  assert.throws(() => parseProjectResolution({ status: 'unknown' }))
})

for (const [kind, row, historyRow, attribution] of [
  ['memory', { id: 87, content: 'Retained decision', project: 'old-checkout', source_agent: 'claude-code', source_sessions: ['session-original'] },
    { id: 701, memory_id: 87, action: 'memory.update', actor: 'operator', before_state_present: true, after_state_present: true, created_at: '2026-10-01T00:00:00Z' },
    { project: 'old-checkout', source_agent: 'claude-code', source_sessions: 'session-original' }],
  ['document', { id: 88, path: 'notes/retained.md', content: 'Retained document', project: 'old-checkout', version: 3, author: 'operator', content_hash: 'original-content-hash' },
    { id: 703, version: 2, author: 'original-author', content_hash: 'previous-content-hash', created_at: '2026-10-01T00:00:00Z' },
    { project: 'old-checkout', version: '3', author: 'operator', content_hash: 'original-content-hash' }],
  ['issue', { id: 89, title: 'Retained issue', body: 'Retained issue body', source_project: 'original-source', target_project: 'old-checkout', source_agent: 'codex' },
    { id: 702, issue_id: 89, body: 'Original discussion', author_project: 'original-source', author_agent: 'codex', created_at: '2026-10-01T00:00:00Z' },
    { source_project: 'original-source', target_project: 'old-checkout', source_agent: 'codex' }],
]) {
  test(`backend-shaped retained ${kind} exposes native content, attribution and selectable history without an envelope provenance key`, () => {
    const payload = { reference: 'old-membership', kind, items: [row], total: 1, next_cursor: null }
    const original = structuredClone(payload)
    const page = parseRetainedPage(payload, 'old-membership', kind)
    assert.equal(page.items[0].id, row.id)
    assert.equal(page.items[0].content, row.content ?? row.body)
    for (const [label, value] of Object.entries(attribution)) assert.ok(page.items[0].fields.some(field => field.label === label && field.value === value), label)
    const history = parseRetainedPage({ reference: 'old-membership', kind, id: page.items[0].id, items: [historyRow], total: 1, next_cursor: null }, 'old-membership', kind, page.items[0].id)
    assert.equal(history.items[0].id, historyRow.id ?? historyRow.version)
    assert.equal(history.items[0].content, historyRow.body ?? '')
    for (const [label, value] of Object.entries(historyRow).filter(([label]) => !['id', 'body', 'before_state', 'after_state'].includes(label))) {
      assert.ok(history.items[0].fields.some(field => field.label === label && field.value === String(value)), label)
    }
    assert.deepEqual(payload, original)
    assert.equal(page.next_cursor, null)
    assert.equal(page.provenance, undefined)
  })
}

test('retained parser preserves optional real provenance and rejects invalid scope, rows, history IDs and continuations', () => {
  const page = { reference: 'old-membership', kind: 'memory', items: [{ id: 87, content: 'Retained decision' }], total: 1, next_cursor: null, provenance: { source: 'original-membership', version: 2 } }
  assert.deepEqual(parseRetainedPage(page, 'old-membership', 'memory').provenance, page.provenance)
  assert.equal(parseRetainedPage({ ...page, next_cursor: 'opaque-next-page' }, 'old-membership', 'memory').next_cursor, 'opaque-next-page')
  for (const changed of [
    { reference: 'foreign' }, { kind: 'document' }, { items: null }, { items: {} }, { items: [null] },
    { items: [{ id: 0, content: 'Invalid ID' }] }, { items: [{ id: 87 }] },
    { items: Array.from({ length: 51 }, () => page.items[0]) },
    { next_cursor: 42 }, { next_cursor: '' }, { next_cursor: undefined }, { items: [], next_cursor: 'non-empty-cursor' },
  ]) assert.throws(() => parseRetainedPage({ ...page, ...changed }, 'old-membership', 'memory'), JSON.stringify(changed))
  assert.throws(() => parseRetainedPage({ ...page, id: 88 }, 'old-membership', 'memory', 87))
  assert.throws(() => parseRetainedPage({ error: 'membership unavailable' }, 'old-membership', 'memory'))
})

test('retained code metadata stays reference-bound and offers no content history', () => {
  const row = { project_id: null, unresolved_reference: 'retained', source_ref: 'source', checkout_ref: 'checkout', repository: 'Retained repository', working_copy: 'Retained checkout' }
  const page = { reference: 'retained', items: [row], next_cursor: null }
  assert.equal(parseRetainedPage(page, 'retained', 'code').items[0].title, 'Retained repository')
  assert.throws(() => parseRetainedPage(page, 'retained', 'code', 1))
  for (const foreign of [{ ...row, project_id: first }, { ...row, unresolved_reference: 'other' }, { ...row, project_id: undefined }]) {
    assert.throws(() => parseRetainedPage({ ...page, items: [foreign] }, 'retained', 'code'))
  }
})

test('reader pages retain server count and continuation, reject arrays and foreign scopes', () => {
  const page = { items: [{ id: 87, project: first, content: 'Decision' }], total: 501, offset: 100, limit: 100, has_more: true }
  assert.equal(parseReaderPage(page, first, 100, 100), page)
  assert.throws(() => parseReaderPage(page.items, first, 100, 100))
  assert.throws(() => parseReaderPage({ ...page, total: 1 }, first, 100, 100))
  assert.throws(() => parseReaderPage({ ...page, has_more: false }, first, 100, 100))
  assert.throws(() => parseReaderPage({ ...page, items: [] }, first, 100, 100))
  assert.throws(() => parseReaderPage(page, second, 100, 100))
  assert.throws(() => parseReaderPage(page, first, 0, 100))
  assert.deepEqual(parseReaderPage({ items: [], total: 0, offset: 0, limit: 100, has_more: false }, first, 0, 100).items, [])
})

test('vault binds identical names by ID and explicit project or global metadata', () => {
  const project = credentialBinding({ id: 1, name: 'same', scope: 'project', project: first, project_id: first })
  const global = credentialBinding({ id: 2, name: 'same', scope: 'global', project_id: null })
  assert.equal(project.project_id, first)
  assert.equal(global.project_id, null)
  assert.doesNotThrow(() => assertCredentialMetadata({ name: 'same' }, project))
  assert.doesNotThrow(() => assertCredentialMetadata(global, global))
  assert.throws(() => assertCredentialMetadata({ id: 2, name: 'same', project_id: second }, project))
  assert.throws(() => credentialBinding({ id: 1, name: 'same', scope: 'project' }))
  assert.throws(() => credentialBinding({ id: 1, name: 'same', scope: 'project', project: 'old/path' }))
  assert.throws(() => credentialBinding({ id: 1, name: 'same', scope: 'global', project: first }))
  assert.throws(() => credentialBinding({ id: 1, name: 'same', scope: 'project', project: second, project_id: first }))
})

function evaluate(file, dependencies, globals) {
  const source = readFileSync(new URL(file, import.meta.url), 'utf8')
  const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(dependencies, module, module.exports, ...Object.values(globals))
  return module.exports
}

for (const client of ['ui', 'operator-web']) {
  test(`${client} deletes the content ID with explicit selected human source query and no body`, async () => {
    const actor = '33333333-3333-4333-8333-333333333333'
    const calls = []
    const issue = { id: 7, source_project: first, target_project: second }
    const fetch = async (path, init = {}) => {
      const url = new URL(path, 'http://synthetic.invalid')
      calls.push({ url, init })
      if (url.pathname === '/api/projects') return Response.json({ items: [...items, { project_id: actor, name: 'Acting project' }], unresolved: [] })
      if (url.pathname === '/api/issues/7' && init.method === 'DELETE') return new Response(null, { status: 204 })
      if (url.pathname === '/api/issues/7') return Response.json({ issue, comments: [] })
      throw new Error(`Unexpected fixture request ${url}`)
    }
    const globals = {
      fetch,
      useRuntimeConfig: () => ({ public: { apiBase: '/api' } }),
      $fetch: async (path, init) => {
        const response = await fetch(path, init)
        const text = await response.text()
        return text ? JSON.parse(text) : undefined
      },
    }
    const api = evaluate(client === 'ui' ? './api.ts' : '../../../apps/operator-web/app/composables/useOperatorApi.ts', () => projectCatalog, globals)
    const actions = client === 'ui' ? api : evaluate('../../../apps/operator-web/app/composables/useOperatorIssueActions.ts', () => api, globals)
    await api.fetchProjects()
    const original = await api.fetchIssue(issue.id)
    calls.length = 0
    for (const source of [undefined, '', 'all', 'global']) await assert.rejects(() => actions.deleteIssue(original.issue.id, source))
    assert.deepEqual(calls, [])
    assert.equal(await actions.deleteIssue(original.issue.id, actor), undefined)
    assert.equal(calls.length, 1)
    assert.equal(calls[0].url.pathname, '/api/issues/7')
    assert.equal(calls[0].url.search, `?source_project=${actor}&source_agent=human`)
    assert.equal(calls[0].init.method, 'DELETE')
    assert.equal(calls[0].init.body, undefined)
    if (client === 'operator-web') assert.equal(calls[0].init.credentials, 'include')
    assert.deepEqual(original.issue, issue)
  })
}

for (const client of ['ui', 'operator-web']) {
  test(`${client} retained code uses only the advertised metadata endpoint and refuses history locally`, async () => {
    const calls = []
    const fetch = async path => {
      const url = new URL(path, 'http://synthetic.invalid')
      calls.push(url)
      return Response.json({ reference: 'retained', items: [{ project_id: null, unresolved_reference: 'retained', source_ref: 'source', repository: 'Repository' }], next_cursor: null })
    }
    const globals = { fetch, useRuntimeConfig: () => ({ public: { apiBase: '/api' } }), $fetch: async path => (await fetch(path)).json() }
    const api = evaluate(client === 'ui' ? './api.ts' : '../../../apps/operator-web/app/composables/useOperatorApi.ts', () => projectCatalog, globals)
    await api.fetchRetainedContent('retained', 'code', undefined, 'next', 'ignored search')
    assert.equal(calls.length, 1)
    assert.equal(calls[0].pathname, '/api/projects/unresolved/retained/code')
    assert.equal(calls[0].searchParams.get('cursor'), 'next')
    assert.equal(calls[0].searchParams.has('kind'), false)
    assert.equal(calls[0].searchParams.has('q'), false)
    assert.equal(calls[0].searchParams.has('project'), false)
    await assert.rejects(() => api.fetchRetainedContent('retained', 'code', 901))
    assert.equal(calls.length, 1)
  })
}
