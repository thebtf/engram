import assert from 'node:assert/strict'
import { test } from 'node:test'
import { parseProjectCatalog, parseProjectResolution, parseRetainedPage, projectLabel, parseReaderPage, credentialBinding, assertCredentialMetadata } from './projectCatalog.ts'

const first = '11111111-1111-4111-8111-111111111111'
const second = '22222222-2222-4222-8222-222222222222'
const items = [
  { project_id: first, name: 'Workspace', repository_qualifier: 'team-a/repository' },
  { project_id: second, name: 'Workspace', repository_qualifier: 'team-b/repository' },
]

test('catalog retains distinct IDs and separates opaque unresolved membership', () => {
  const catalog = parseProjectCatalog({ items, unresolved: [{ reference: 'retained/old-worktree', name: 'Old checkout', reason: 'Mapping is ambiguous' }] })
  assert.deepEqual(catalog.items.map(item => item.project_id), [first, second])
  assert.equal(catalog.unresolved[0].reference, 'retained/old-worktree')
  assert.equal(projectLabel(catalog.items, first, 'Unknown', 'Ambiguous'), 'Workspace · team-a/repository')
  assert.equal(projectLabel(catalog.items, second, 'Unknown', 'Ambiguous'), 'Workspace · team-b/repository')
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

test('retained readers preserve content IDs, history versions and scoped provenance', () => {
  const content = parseRetainedPage({ reference: 'old-membership', kind: 'memory', items: [{ id: 87, content: 'Retained decision' }], next_cursor: 'next-page', provenance: { source: 'retained membership' } }, 'old-membership', 'memory')
  assert.equal(content.items[0].id, 87)
  assert.equal(content.items[0].content, 'Retained decision')
  assert.equal(content.next_cursor, 'next-page')
  const history = parseRetainedPage({ reference: 'old-membership', kind: 'document', id: 87, items: [{ version: 3, author: 'operator', content_hash: 'old-content-hash' }], next_cursor: null }, 'old-membership', 'document', 87)
  assert.equal(history.items[0].id, 3)
  assert.ok(history.items[0].fields.some(field => field.label === 'content_hash' && field.value === 'old-content-hash'))
  assert.throws(() => parseRetainedPage({ reference: 'foreign', kind: 'memory', items: [], next_cursor: null, provenance: {} }, 'old-membership', 'memory'))
  assert.throws(() => parseRetainedPage({ reference: 'old-membership', kind: 'memory', items: [], next_cursor: 'same-page', provenance: {} }, 'old-membership', 'memory'))
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
