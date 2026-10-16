import { expect, test } from '@playwright/test'

const CONSOLE = '11111111-1111-4111-8111-111111111111'
const OTHER = '22222222-2222-4222-8222-222222222222'
const ALPHA = '44444444-4444-4444-8444-444444444444'
const BETA = '55555555-5555-4555-8555-555555555555'

test('catalog choices retain equal names as different UUIDs and omit unresolved references', async ({ page }) => {
  await page.goto('/')
  const selector = page.getByTestId('project-context-select')
  await expect(selector.locator(`option[value="${ALPHA}"]`)).toHaveText('Alpha · examples/alpha')
  await expect(selector.locator(`option[value="${BETA}"]`)).toHaveText('Alpha · examples/beta')
  await expect(selector.locator('option[value="retained-legacy-fixture"]')).toHaveCount(0)
  await selector.selectOption(ALPHA)
  await expect(selector).toHaveValue(ALPHA)
  await selector.selectOption(BETA)
  await expect(selector).toHaveValue(BETA)
})

test('old alias deep link preserves exact memory ID and search/filter return', async ({ page }) => {
  const reads: URL[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname.startsWith('/api/memories')) reads.push(url) })
  await page.goto('/memory?project=operator-console&memory=501&q=older&filter=active&from=search&searchQuery=original')
  await expect(page.getByTestId('memory-content')).toHaveText('older memory omitted by the 500-row project list')
  await expect(page.getByTestId('project-context-select')).toHaveValue(CONSOLE)
  const query = new URL(page.url()).searchParams
  expect(query.get('memory')).toBe('501')
  expect(query.get('q')).toBe('older')
  expect(query.getAll('filter')).toEqual(['active'])
  expect(query.get('searchQuery')).toBe('original')
  expect(reads.length).toBeGreaterThan(0)
  expect(reads.every((url) => url.searchParams.get('project') === CONSOLE)).toBe(true)
})

test('unresolved alias opens useful read-only knowledge/history and a stale reference is an error', async ({ page }) => {
  const reads: URL[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname.includes('/unresolved/')) reads.push(url) })
  const legacyLocation = '/memory?project=legacy&memory=901&q=retained&filter=low&filter=chain#original'
  await page.goto(legacyLocation)
  await expect(page).toHaveURL(/\/projects\/unresolved\?/)
  await expect(page.getByTestId('project-context-select')).toHaveValue('')
  await expect(page.getByTestId('unresolved-content-901')).toContainText('Retained memory: the original content ID survives')
  await expect(page.getByTestId('unresolved-history')).toContainText('Original retained history')
  expect(reads.every((url) => !url.searchParams.has('project'))).toBe(true)
  const retainedQuery = new URL(page.url()).searchParams
  expect(retainedQuery.get('originalLocation')).toBe(legacyLocation)
  expect(retainedQuery.get('returnTo')).toBe('/projects')
  expect(retainedQuery.get('memory')).toBe('901')
  expect(retainedQuery.get('q')).toBe('retained')
  expect(retainedQuery.getAll('filter')).toEqual(['low', 'chain'])
  expect(new URL(page.url()).hash).toBe('#original')
  await page.getByTestId('unresolved-original-location').locator('..').locator('summary').click()
  await expect(page.getByTestId('unresolved-original-location')).toHaveText(legacyLocation)
  await page.getByTestId('unresolved-back').click()
  await expect(page).toHaveURL(/\/projects$/)
  await expect(page.getByTestId('unresolved-content-901')).toHaveCount(0)
  await page.goto('/projects/unresolved?reference=stale-reference&kind=memory')
  await expect(page.getByTestId('unresolved-error')).toBeVisible()
  await expect(page.getByTestId('unresolved-content-901')).toHaveCount(0)
})

test('catalog reader kinds distinguish memory, mixed and metadata-only groups without unsupported requests', async ({ page }) => {
  const groups = [
    { reference: 'registry-only', name: 'Retained registry metadata', reason: 'locator_is_not_identity', reader_kinds: [] },
    { reference: 'document-only', name: 'Retained document group', reason: 'unproven', reader_kinds: ['document'] },
    { reference: 'memory-only', name: 'Retained memory group', reason: 'unproven', reader_kinds: ['memory'] },
    { reference: 'mixed', name: 'Retained mixed group', reason: 'unproven', reader_kinds: ['document', 'issue', 'memory'] },
  ]
  const reads: URL[] = []
  page.on('request', request => { const url = new URL(request.url()); if (url.pathname.includes('/unresolved/')) reads.push(url) })
  await page.route('**/api/projects', route => route.fulfill({ json: { items: [{ project_id: CONSOLE, name: 'Console' }], unresolved: groups } }))
  await page.route('**/api/projects/unresolved/**', route => {
    const url = new URL(route.request().url())
    const reference = url.pathname.split('/')[4]
    const kind = url.searchParams.get('kind')
    return route.fulfill({ json: { reference, kind, items: [{ id: 901, content: `Retained ${kind} content`, path: 'retained.md' }], next_cursor: null, provenance: { source: reference } } })
  })
  await page.goto('/projects/unresolved?reference=registry-only&kind=memory&id=901')
  await expect(page.getByTestId('unresolved-metadata-only')).toBeVisible()
  await expect(page.getByTestId('unresolved-content-901')).toHaveCount(0)
  await expect(page.getByTestId('unresolved-history')).toHaveCount(0)
  expect(reads).toEqual([])
  await page.getByRole('link', { name: 'Retained document group' }).click()
  await expect(page.getByTestId('unresolved-content-901')).toContainText('Retained document content')
  const kinds = page.locator('.reader select')
  await expect(kinds.locator('option')).toHaveCount(1)
  await expect(kinds).toHaveValue('document')
  await page.getByRole('link', { name: 'Retained memory group' }).click()
  await expect(kinds).toHaveValue('memory')
  await expect(page.getByTestId('unresolved-content-901')).toContainText('Retained memory content')
  await page.getByRole('link', { name: 'Retained mixed group' }).click()
  await expect(kinds.locator('option')).toHaveCount(3)
  await expect(kinds).toHaveValue('memory')
  await kinds.selectOption('issue')
  await expect(page.getByTestId('unresolved-content-901')).toContainText('Retained issue content')
  await page.getByRole('link', { name: 'Retained registry metadata' }).click()
  await expect(page.getByTestId('unresolved-metadata-only')).toBeVisible()
  await expect(page.getByTestId('unresolved-content-901')).toHaveCount(0)
  expect(reads.every(url => !url.pathname.includes('/registry-only/') && !url.searchParams.has('project'))).toBe(true)
  expect(reads.filter(url => url.pathname.includes('/document-only/')).every(url => url.searchParams.get('kind') === 'document')).toBe(true)
})

test('a late alias response cannot borrow the newer selected project', async ({ page }) => {
  let release!: () => void
  let started!: () => void
  const blocked = new Promise<void>((resolve) => { release = resolve })
  const begun = new Promise<void>((resolve) => { started = resolve })
  await page.route('**/api/projects/resolve?legacy_id=delayed-alias', async (route) => {
    started()
    await blocked
    await route.fulfill({ json: { status: 'resolved', project_id: CONSOLE, name: 'Operator Console' } })
  })
  await page.goto('/memory?project=delayed-alias&memory=501')
  await begun
  await page.getByTestId('project-context-select').selectOption(OTHER)
  await expect(page.getByTestId('project-context-select')).toHaveValue(OTHER)
  release()
  await expect(page.getByTestId('project-context-select')).toHaveValue(OTHER)
  await expect(page.getByTestId('memory-content')).toHaveCount(0)
})

test('retained code metadata is review-only and never creates a pin', async ({ page }) => {
  const mutations: string[] = []
  page.on('request', (request) => { if (request.method() !== 'GET' && request.url().includes('/api/code/')) mutations.push(request.url()) })
  await page.goto('/projects/unresolved?reference=retained-legacy-fixture&kind=code')
  await expect(page.getByText('Retained fixture repository', { exact: true })).toBeVisible()
  await expect(page.getByText('Historical fixture working copy')).toBeVisible()
  await expect(page.getByTestId('project-context-select')).toHaveValue('')
  expect(mutations).toEqual([])
})

test('legacy document links retain version/history and issue deep links retain their object ID', async ({ page }) => {
  await page.goto('/documents?project=legacy&path=retained%2Fnotes.md&version=1&compare=2')
  await expect(page).toHaveURL(/\/projects\/unresolved\?/)
  await expect(page.getByTestId('unresolved-history')).toContainText('Earlier retained document version.')
  const query = new URL(page.url()).searchParams
  expect(query.get('path')).toBe('retained/notes.md')
  expect(query.get('version')).toBe('1')
  expect(query.get('compare')).toBe('2')
  await page.goto('/issues/903?project=legacy#discussion')
  await expect(page.getByTestId('unresolved-content-903')).toContainText('Keep the original issue and its discussion available.')
  await expect(page.getByTestId('unresolved-history')).toContainText('Original retained discussion.')
  expect(new URL(page.url()).searchParams.get('id')).toBe('903')
  expect(new URL(page.url()).searchParams.get('originalLocation')).toBe('/issues/903?project=legacy#discussion')
  expect(new URL(page.url()).searchParams.get('returnTo')).toBe('/projects')
})

test('code catalog POST uses binding proof and query membership without collapsing snapshots', async ({ request }) => {
  const handshake = await request.post('/api/code/tabs/handshake', { data: {} })
  expect(handshake.ok()).toBe(true)
  const binding = await handshake.json()
  const body = { tab_binding_id: binding.tab_binding_id, document_proof: binding.document_proof }
  const denied = await request.post('/api/code/contexts', { data: { tab_binding_id: binding.tab_binding_id } })
  expect(denied.status()).toBe(403)
  const catalog = await request.post('/api/code/contexts', { data: body })
  expect(catalog.ok()).toBe(true)
  const rows = (await catalog.json()).contexts
  expect(rows.some((row: { project_id: string | null; unresolved_reference?: string }) => row.project_id === null && row.unresolved_reference === 'retained-legacy-fixture')).toBe(true)
  const scoped = await request.post('/api/code/contexts?project=33333333-3333-4333-8333-333333333333', { data: body })
  expect(scoped.ok()).toBe(true)
  const resolved = (await scoped.json()).contexts.filter((row: { project_id: string | null }) => row.project_id !== null)
  expect(resolved.length).toBeGreaterThan(1)
  expect(resolved.every((row: { project_id: string }) => row.project_id === '33333333-3333-4333-8333-333333333333')).toBe(true)
  expect(new Set(resolved.map((row: { checkout_ref: string }) => row.checkout_ref)).size).toBeGreaterThan(1)
  const invalid = await request.post('/api/code/contexts?project=Engram', { data: body })
  expect(invalid.status()).toBe(400)
})

test('My Issues predicates apply before paging/count and human creation never defaults its source', async ({ request }) => {
  const mine = `/api/issues?source_project=${BETA}&source_agent=human`
  const createdIDs: number[] = []
  for (const source of [ALPHA, BETA, BETA]) {
    const created = await request.post('/api/issues', { data: { title: `Scoped human issue ${source}`, source_project: source, source_agent: 'human', target_project: CONSOLE } })
    expect(created.status()).toBe(201)
    createdIDs.push((await created.json()).id)
  }
  const inventory = await request.get(`${mine}&limit=500`)
  expect(inventory.ok()).toBe(true)
  const result = await inventory.json()
  expect(result.total).toBe(result.issues.length)
  expect(result.issues.every((row: { source_project: string; source_agent: string }) => row.source_project === BETA && row.source_agent === 'human')).toBe(true)
  expect(result.issues.some((row: { id: number }) => row.id === createdIDs[0])).toBe(false)
  expect(createdIDs.slice(1).every((id) => result.issues.some((row: { id: number }) => row.id === id))).toBe(true)
  const page = await request.get(`${mine}&limit=1&offset=1`)
  expect(page.ok()).toBe(true)
  const paged = await page.json()
  expect(paged.issues).toHaveLength(1)
  expect(paged.issues[0].source_project).toBe(BETA)
  expect(paged.issues[0].source_agent).toBe('human')
  const invalid = await request.post('/api/issues', { data: { title: 'Missing source', source_agent: 'human', target_project: CONSOLE } })
  expect(invalid.status()).toBe(400)
})

test('same-name Vault fixtures retain exact project/global metadata and do not echo values in receipts', async ({ request }) => {
  const headers = { Cookie: `mock-vault-session=catalog-${Date.now()}` }
  const metadata = await request.get('/api/vault/credentials', { headers })
  expect(metadata.ok()).toBe(true)
  const rows = await metadata.json()
  expect(rows.every((row: object) => !Object.hasOwn(row, 'value'))).toBe(true)
  const matches = rows.filter((row: { name: string }) => row.name === 'shared-token')
  expect(new Set(matches.map((row: { project_id: string | null }) => row.project_id)).size).toBe(3)
  for (const project of [ALPHA, BETA]) {
    const reveal = await request.get(`/api/vault/credentials/shared-token?project=${project}`, { headers })
    expect(reveal.ok()).toBe(true)
    expect((await reveal.json()).project_id).toBe(project)
  }
  const global = await request.get('/api/vault/credentials/shared-token?scope=global', { headers })
  expect(global.ok()).toBe(true)
  expect((await global.json()).project_id).toBe(null)
  expect((await request.get('/api/vault/credentials/shared-token', { headers })).status()).toBe(400)
  expect((await request.get(`/api/vault/credentials/shared-token?scope=global&project=${ALPHA}`, { headers })).status()).toBe(400)
  const deleted = await request.delete('/api/vault/credentials/shared-token?scope=global', { headers })
  expect(deleted.ok()).toBe(true)
  const state = (await deleted.json()).readback.current_state
  expect(state.project_id).toBe(null)
  expect(Object.hasOwn(state, 'value')).toBe(false)
  expect((await request.get(`/api/vault/credentials/shared-token?project=${ALPHA}`, { headers })).ok()).toBe(true)
})

test('equal names without repository qualifiers remain distinguishable before selection', async ({ page }) => {
  await page.route('**/api/projects', (route) => route.fulfill({ json: { items: [{ project_id: CONSOLE, name: 'Same name' }, { project_id: OTHER, name: 'Same name' }], unresolved: [] } }))
  await page.goto('/')
  const selector = page.getByTestId('project-context-select')
  await expect(selector.locator(`option[value="${CONSOLE}"]`)).toHaveText('Same name #1')
  await expect(selector.locator(`option[value="${OTHER}"]`)).toHaveText('Same name #2')
  await selector.selectOption(OTHER)
  await expect(selector).toHaveValue(OTHER)
})

test('same-name primary choices with an identical repository qualifier remain distinct UUID selections', async ({ page }) => {
  await page.route('**/api/projects', (route) => route.fulfill({ json: { items: [{ project_id: OTHER, name: 'Same name', repository_qualifier: 'examples/shared' }, { project_id: CONSOLE, name: 'Same name', repository_qualifier: 'examples/shared' }], unresolved: [] } }))
  await page.goto('/')
  const selector = page.getByTestId('project-context-select')
  await expect(selector.locator(`option[value="${CONSOLE}"]`)).toHaveText('Same name · examples/shared #1')
  await expect(selector.locator(`option[value="${OTHER}"]`)).toHaveText('Same name · examples/shared #2')
  await selector.selectOption(CONSOLE)
  await expect(selector).toHaveValue(CONSOLE)
  await selector.selectOption(OTHER)
  await expect(selector).toHaveValue(OTHER)
})

test('an explicit known Back destination preserves its scope and content while the legacy location stays inspectable', async ({ page }) => {
  const destination = `/memory?project=${CONSOLE}&memory=501&q=older#previous`
  const original = `/memory?project=legacy&memory=901&q=retained&returnTo=${encodeURIComponent(destination)}#original`
  await page.goto(original)
  await expect(page.getByTestId('unresolved-content-901')).toContainText('Retained memory: the original content ID survives')
  await page.getByTestId('unresolved-original-location').locator('..').locator('summary').click()
  await expect(page.getByTestId('unresolved-original-location')).toHaveText(original)
  await expect(page.getByTestId('unresolved-back')).toHaveAttribute('href', destination)
  await page.getByTestId('unresolved-back').click()
  await expect(page).toHaveURL(new RegExp(`/memory\\?project=${CONSOLE}&memory=501&q=older#previous$`))
  await expect(page.getByTestId('project-context-select')).toHaveValue(CONSOLE)
  await expect(page.getByTestId('memory-content')).toHaveText('older memory omitted by the 500-row project list')
})

test('incoming unresolved Back targets cannot loop, traverse paths or navigate outside the console', async ({ page }) => {
  for (const returnTo of ['/memory?project=legacy&memory=901', '/projects/unresolved?reference=retained-legacy-fixture', '//external.invalid/path', '/memory/../projects', '/%2e%2e/projects']) {
    await page.goto(`/projects/unresolved?reference=retained-legacy-fixture&kind=memory&returnTo=${encodeURIComponent(returnTo)}`)
    await expect(page.getByTestId('unresolved-content-901')).toContainText('Retained memory: the original content ID survives')
    await expect(page.getByTestId('unresolved-back')).toHaveAttribute('href', '/projects')
    await page.getByTestId('unresolved-back').click()
    await expect(page).toHaveURL(/\/projects$/)
    await expect(page.getByTestId('unresolved-content-901')).toHaveCount(0)
  }
})
