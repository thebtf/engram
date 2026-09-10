import { expect, test, type Page, type Route } from '@playwright/test'
import { executeMutation } from '../../composables/useApi'

function collectPageFailures(page: Page) {
  const failures: string[] = []
  page.on('requestfailed', (request) => failures.push(`${request.method()} ${request.url()} ${request.failure()?.errorText || 'failed'}`))
  page.on('response', (response) => {
    if (response.status() >= 400) failures.push(`${response.status()} ${response.request().method()} ${response.url()}`)
  })
  page.on('pageerror', (error) => failures.push(error.message))
  return failures
}

test.describe.configure({ mode: 'serial' })

test('books ingest remains pending until the server exposes an authoritative status reference', async ({ page }) => {
  const failures = collectPageFailures(page)
  const statusRequests: string[] = []
  page.on('request', (request) => {
    if (request.method() === 'GET' && /\/api\/books\/\d+\/status$/.test(new URL(request.url()).pathname)) {
      statusRequests.push(request.url())
    }
  })
  await page.goto('/books')

  const textInputs = page.locator('.books-page input[type="text"]')
  await textInputs.nth(0).fill('readback-book.md')
  await textInputs.nth(1).fill('operator-console')
  await textInputs.nth(2).fill('operator-console')
  await page.locator('.books-page textarea').fill('# Readback book\n\nA current book-ingestion fixture.')

  const createResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/books')
  await page.locator('.books-page button.act.primary').click()
  expect(await (await createResponse).json()).toMatchObject({ status: 'pending', source_ref: 'readback-book.md' })
  const outcome = page.getByTestId('mutation-result')
  await expect(outcome).toHaveAttribute('data-kind', 'committed_verification_pending')
  await expect(page.getByTestId('mutation-retained-input')).toBeVisible()
  await expect(textInputs.nth(0)).toHaveValue('readback-book.md')
  await page.waitForTimeout(100)
  expect(statusRequests).toEqual([])
  expect(failures).toEqual([])
})

test('rules create and edit round-trip through current control-plane routes', async ({ page }) => {
  const failures = collectPageFailures(page)
  await page.goto('/rules')

  await page.locator('.rules-page > .head > button.primary').click()
  const dialog = page.getByRole('dialog')
  await dialog.locator('select').selectOption('operator-console')
  await dialog.locator('textarea').fill('readback rule before edit')
  const createResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/rules')
  await dialog.locator('button.primary').click()
  const created = await (await createResponse).json()
  expect(created).toMatchObject({
    id: expect.any(Number),
    project: 'operator-console',
    content: 'readback rule before edit',
    enabled: true,
    version: 1,
    created_at: expect.stringMatching(/^\d{4}-\d{2}-\d{2}T/),
    updated_at: expect.stringMatching(/^\d{4}-\d{2}-\d{2}T/),
  })
  await expect(page.getByTestId('mutation-result')).toHaveAttribute('data-kind', 'committed_verified')
  const createRefresh = page.waitForResponse((response) => response.request().method() === 'GET' && new URL(response.url()).pathname === '/api/rules')
  await page.locator('.rules-page .toolbar .tbtn').click()
  await createRefresh

  const row = page.locator('.rule-row').filter({ hasText: 'readback rule before edit' })
  await expect(row).toBeVisible()
  await row.locator('button.act').first().click()
  await page.locator('.rule-row.editing textarea').fill('readback rule after edit')
  const editResponse = page.waitForResponse((response) => response.request().method() === 'PATCH' && new URL(response.url()).pathname === `/api/rules/${created.id}`)
  await page.locator('.rule-row.editing button.primary-line').click()
  expect(await (await editResponse).json()).toMatchObject({ id: created.id, content: 'readback rule after edit', version: 2 })
  const updateRefresh = page.waitForResponse((response) => response.request().method() === 'GET' && new URL(response.url()).pathname === '/api/rules')
  await page.locator('.rules-page .toolbar .tbtn').click()
  await updateRefresh
  await expect(page.locator('.rule-row').filter({ hasText: 'readback rule after edit' })).toBeVisible()
  await page.locator('.navbrand').click()
  await expect(page).toHaveURL(/\/$/)
  await expect(page.locator('.ov-card[href="/rules"] .ov-big')).toHaveText('3')
  expect(failures).toEqual([])
})

test('mismatched, bare, and accepted rule responses retain drafts for explicit recheck', async ({ page }) => {
  let createCalls = 0
  let deleteCalls = 0
  await page.route('**/api/rules', async (route: Route) => {
    if (route.request().method() !== 'POST') return route.continue()
    createCalls += 1
    const input = route.request().postDataJSON() as Record<string, unknown>
    const directCurrent = {
      id: 901,
      project: input.project,
      content: input.content,
      priority: input.priority,
      version: 1,
      enabled: true,
      created_at: '2026-09-10T12:00:00Z',
      updated_at: '2026-09-10T12:00:00Z',
    }
    await route.fulfill(createCalls === 1
      ? { status: 201, json: { ...directCurrent, version: 0 } }
      : { status: 202, json: directCurrent })
  })
  await page.route('**/api/rules/401', async (route: Route) => {
    if (route.request().method() !== 'DELETE') return route.continue()
    deleteCalls += 1
    await route.fulfill({ status: 200, json: { deleted: 401 } })
  })

  async function submitDraft(content: string) {
    await page.locator('.rules-page > .head > button.primary').click()
    const dialog = page.getByRole('dialog')
    await dialog.locator('textarea').fill(content)
    await dialog.locator('select').selectOption('operator-console')
    await dialog.locator('button.primary').click()
    return dialog
  }

  await page.goto('/rules')
  const mismatchDraft = await submitDraft('keep this mismatched rule draft')
  const outcome = page.getByTestId('mutation-result')
  await expect(outcome).toHaveAttribute('data-kind', 'committed_verification_pending')
  await expect(page.getByTestId('mutation-request-reference')).toHaveText(/\S+/)
  await expect(page.getByTestId('mutation-retained-input')).toBeVisible()
  await expect(mismatchDraft.locator('textarea')).toHaveValue('keep this mismatched rule draft')
  await expect(outcome).not.toContainText(/rollback/i)

  await page.reload({ waitUntil: 'networkidle' })
  const acceptedDraft = await submitDraft('keep this accepted rule draft')
  await expect(outcome).toHaveAttribute('data-kind', 'committed_verification_pending')
  await expect(acceptedDraft.locator('textarea')).toHaveValue('keep this accepted rule draft')
  expect(createCalls).toBe(2)

  await page.reload({ waitUntil: 'networkidle' })
  const seededRule = page.locator('.rule-row').filter({ hasText: 'operator console rules are live data' })
  const remove = seededRule.locator('button.act.danger')
  await remove.click()
  await remove.click()
  await expect(outcome).toHaveAttribute('data-kind', 'committed_verification_pending')
  await expect(seededRule).toBeVisible()
  await page.waitForTimeout(100)
  expect(deleteCalls).toBe(1)
})

test('partial memory retry keeps only unresolved selections and never replays automatically', async ({ page }) => {
  const rows = [
    { id: 9101, project: 'operator-console', content: 'T011 known success', tags: ['t011'], tier: 'semantic', epistemic_type: 'fact', confidence: 0.9, citation_count: 1, injection_count: 1, updated_at: '2026-09-10T12:00:00Z', status: 'active' },
    { id: 9102, project: 'operator-console', content: 'T011 unresolved selection', tags: ['t011'], tier: 'semantic', epistemic_type: 'fact', confidence: 0.9, citation_count: 1, injection_count: 1, updated_at: '2026-09-10T12:00:00Z', status: 'active' },
  ]
  const payloads: Array<Record<string, unknown>> = []
  await page.route('**/api/memories?**', (route: Route) => route.fulfill({ json: rows }))
  await page.route('**/api/memories/suppress', async (route: Route) => {
    if (route.request().method() !== 'POST') return route.continue()
    payloads.push(route.request().postDataJSON() as Record<string, unknown>)
    await route.fulfill(payloads.length === 1
      ? {
        status: 207,
        json: {
          operation_state: 'partial',
          item_results: [
            { target_id: 9101, outcome: 'committed' },
            { target_id: 9102, outcome: 'outcome_unknown' },
          ],
        },
      }
      : { status: 202, json: { operation_state: 'accepted' } })
  })

  await page.goto('/memory')
  await page.getByTestId('memory-row-9101').locator('.echk').click()
  await page.getByTestId('memory-row-9102').locator('.echk').click()
  const apply = page.locator('.bulkbar .act').first()
  await apply.click()
  await apply.click()

  const outcome = page.getByTestId('mutation-result')
  await expect(outcome).toHaveAttribute('data-kind', 'partial')
  await expect(page.getByTestId('mutation-item-outcomes')).toContainText('9101')
  await expect(page.getByTestId('memory-row-9101').locator('.echk')).not.toHaveClass(/on/)
  await expect(page.getByTestId('memory-row-9102').locator('.echk')).toHaveClass(/on/)
  await page.waitForTimeout(100)
  expect(payloads).toHaveLength(1)
  expect(payloads[0]).toMatchObject({ ids: [9101, 9102] })

  await apply.click()
  expect(payloads).toHaveLength(1)
  await apply.click()
  await expect(outcome).toHaveAttribute('data-kind', 'committed_verification_pending')
  expect(payloads).toHaveLength(2)
  expect(payloads[1]).toMatchObject({ ids: [9102] })
  await expect(outcome).not.toContainText(/rollback/i)
})

test('a browser transport loss retains the selected mutation for manual reconciliation', async ({ page }) => {
  const rows = [
    { id: 9103, project: 'operator-console', content: 'T011 lost transport selection', tags: ['t011'], tier: 'semantic', epistemic_type: 'fact', confidence: 0.9, citation_count: 1, injection_count: 1, updated_at: '2026-09-10T12:00:00Z', status: 'active' },
  ]
  let dispatches = 0
  await page.route('**/api/memories?**', (route: Route) => route.fulfill({ json: rows }))
  await page.route('**/api/memories/suppress', async (route: Route) => {
    if (route.request().method() !== 'POST') return route.continue()
    dispatches += 1
    await route.abort('connectionreset')
  })

  await page.goto('/memory')
  await page.getByTestId('memory-row-9103').locator('.echk').click()
  const apply = page.locator('.bulkbar .act').first()
  await apply.click()
  await apply.click()

  const outcome = page.getByTestId('mutation-result')
  await expect(outcome).toHaveAttribute('data-kind', 'outcome_unknown')
  await expect(page.getByTestId('mutation-request-reference')).toHaveText(/\S+/)
  await expect(page.getByTestId('mutation-retained-input')).toBeVisible()
  await expect(page.getByTestId('memory-row-9103').locator('.echk')).toHaveClass(/on/)
  await expect(outcome).not.toContainText(/rollback/i)
  await page.waitForTimeout(100)
  expect(dispatches).toBe(1)
})

test('dispatched and response-body loss preserve the exact request for manual reconciliation', async () => {
  const request = { requestId: 't011-lost-request', action: 'memory-store', intent: { content: 'retain this exact input' } }
  const dispatchedLoss = await executeMutation(request, Promise.reject(new TypeError('connection lost')), () => undefined)
  const bodyLoss = await executeMutation(request, Promise.resolve({
    ok: true,
    status: 200,
    text: async () => { throw new TypeError('response body lost') },
  } as unknown as Response), () => undefined)

  for (const result of [dispatchedLoss, bodyLoss]) {
    expect(result.kind).toBe('outcome_unknown')
    if (result.kind !== 'outcome_unknown') throw new Error(`expected unknown outcome, got ${result.kind}`)
    expect(result.request).toBe(request)
    expect(result.retry).toBe('manual')
  }
})
test('documents create, history, and readback render through the current documents surface', async ({ page }) => {
  const path = 'notes/current-readback.md'
  const first = '# Current readback\n\nVersion one.'
  const second = '# Current readback\n\nVersion two.'

  for (const content of [first, second]) {
    const response = await page.request.post('/api/documents', {
      data: { path, project: 'operator-console', content, doc_type: 'markdown', author: 'operator-console' },
    })
    expect(response.status()).toBe(201)
  }

  const historyResponse = await page.request.get(`/api/documents/history?path=${encodeURIComponent(path)}&project=operator-console`)
  expect(await historyResponse.json()).toMatchObject({ path, project: 'operator-console', count: 2, versions: [{ version: 2 }, { version: 1 }] })
  const readResponse = await page.request.get(`/api/documents/read?path=${encodeURIComponent(path)}&project=operator-console&version=2`)
  expect(await readResponse.json()).toMatchObject({ path, project: 'operator-console', version: 2, content: second })

  const failures = collectPageFailures(page)
  await page.goto('/documents?project=operator-console')
  await expect(page.locator('.doc-row').filter({ hasText: path })).toBeVisible()
  await expect(page.locator('.history-list')).toContainText('2')
  await expect(page.locator('.doc-content').first()).toHaveText(second)
  expect(failures).toEqual([])
})

test('orphan credential cleanup does not claim a refreshed vault from a bare receipt', async ({ page }) => {
  const failures = collectPageFailures(page)
  const statusReads: string[] = []
  page.on('request', (request) => {
    if (request.method() === 'GET' && new URL(request.url()).pathname === '/api/vault/status') {
      statusReads.push(request.url())
    }
  })
  await page.goto('/secrets')
  await expect(page.locator('.vault-status')).toContainText('3')

  const cleanupResponse = page.waitForResponse((response) => response.request().method() === 'DELETE' && new URL(response.url()).pathname === '/api/vault/orphaned-credentials')
  await page.locator('.rotation-card button.secondary').nth(1).click()
  expect(await (await cleanupResponse).json()).toEqual({ status: 'ok', deleted: 1 })
  await page.waitForTimeout(100)
  expect(statusReads).toHaveLength(1)
  await expect(page.locator('.vault-status')).toContainText('3')
  await expect(page.locator('.vault-tbl')).toContainText('orphaned-token')
  expect(failures).toEqual([])
})
