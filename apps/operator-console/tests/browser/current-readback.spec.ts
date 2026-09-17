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

test('books bookmark exposes retirement without plaintext admission', async ({ page }) => {
 const failures = collectPageFailures(page)
 const writerRequests: string[] = []
 page.on('request', (request) => {
  if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/books') writerRequests.push(request.url())
 })

 await page.goto('/books')
 await expect(page.getByRole('heading', { name: 'Приём текстовых книг упразднён' })).toBeVisible()
 await expect(page.locator('.retirement-page').locator('input, textarea, input[type="file"]')).toHaveCount(0)
 await expect(page.getByRole('link', { name: 'Открыть документы' })).toHaveAttribute('href', '/documents')

 await page.goto('/settings')
 await page.getByRole('dialog').getByRole('button', { name: 'English' }).click()
 await expect(page.locator('html')).toHaveAttribute('lang', 'en')
 await page.goto('/books')
 await expect(page.getByRole('heading', { name: 'Plaintext book intake retired' })).toBeVisible()

 await page.goto('/settings')
 await page.getByRole('dialog').getByRole('button', { name: '中文' }).click()
 await expect(page.locator('html')).toHaveAttribute('lang', 'zh-Hans')
 await page.goto('/books')
 await expect(page.getByRole('heading', { name: '纯文本书籍导入已停用' })).toBeVisible()
 expect(writerRequests).toEqual([])
 expect(failures).toEqual([])

})

test('rules create and selection update round-trip through current control-plane routes', async ({ page }) => {
 const failures = collectPageFailures(page)
 const selectionRequests: Array<Record<string, unknown>> = []
 const updateRequests: Array<Record<string, unknown>> = []
 let createdRule: Record<string, unknown> | undefined
 let updatedRule: Record<string, unknown> | undefined

 await page.route('**/api/collections/selection', async (route: Route) => {
  if (route.request().method() === 'POST') {
   selectionRequests.push(route.request().postDataJSON() as Record<string, unknown>)
  }
  await route.continue()
 })
 await page.route(/\/api\/rules(?:\?.*)?$/, async (route: Route) => {
  if (route.request().method() === 'GET' && updatedRule !== undefined) {
   await route.fulfill({ json: [updatedRule] })
   return
  }
  if (route.request().method() !== 'POST') return route.continue()

  const input = route.request().postDataJSON() as Record<string, unknown>
  if (input.action !== 'update') return route.continue()
  if (createdRule === undefined || typeof input.content !== 'string') {
   throw new Error('Rule update requires the created rule and new content')
  }

  updateRequests.push(input)
  updatedRule = {
   ...createdRule,
   content: input.content,
   version: 2,
   updated_at: '2026-09-10T12:02:00.000Z',
  }
  await route.fulfill({
   status: 200,
   json: {
    request_id: input.request_id,
    operation_state: 'completed',
    item_results: [{
     target_id: createdRule.id,
     outcome: 'committed',
     observed_version: 2,
     readback: { authoritative: true, kind: 'current', current_state: updatedRule, current_version: 2 },
    }],
    readback: { authoritative: true, kind: 'current', current_state: updatedRule, current_version: 2 },
   },
  })
 })

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
 if (typeof created !== 'object' || created === null || Array.isArray(created)) {
  throw new Error('Rule create response is not a current DTO')
 }
 createdRule = created
 await expect(page.getByTestId('mutation-result')).toHaveAttribute('data-kind', 'committed_verified')
 const createRefresh = page.waitForResponse((response) => response.request().method() === 'GET' && new URL(response.url()).pathname === '/api/rules')
 await page.locator('.rules-page .toolbar .tbtn').click()
 await createRefresh

 const row = page.locator('.rule-row').filter({ hasText: 'readback rule before edit' })
 await expect(row).toBeVisible()
 await row.locator('button.act').first().click()
 await page.locator('.rule-row.editing textarea').fill('readback rule after edit')
 const editResponse = page.waitForResponse((response) => response.request().method() === 'POST'
  && new URL(response.url()).pathname === '/api/rules'
  && response.request().postDataJSON().action === 'update')
 await page.locator('.rule-row.editing button.primary-line').click()
 expect(await (await editResponse).json()).toMatchObject({
  operation_state: 'completed',
  readback: {
   authoritative: true,
   kind: 'current',
   current_state: { id: created.id, content: 'readback rule after edit', version: 2 },
  },
 })
 expect(selectionRequests).toEqual([
  expect.objectContaining({
   domain: 'rules',
   selection: { kind: 'explicit', targets: [{ id: String(created.id), expected_version: 1 }] },
  }),
 ])
 expect(updateRequests).toEqual([
  expect.objectContaining({
   action: 'update',
   content: 'readback rule after edit',
   selection: { kind: 'explicit', selection_version: expect.any(Number) },
  }),
 ])
 await expect(page.getByTestId('mutation-result')).toHaveAttribute('data-kind', 'committed_verified')
 await expect(page.locator('.rule-row').filter({ hasText: 'readback rule after edit' })).toBeVisible()
 expect(failures).toEqual([])
})

test('mismatched, bare, and accepted rule responses retain drafts for explicit recheck', async ({ page }) => {
 let createCalls = 0
 const deleteRequests: Array<Record<string, unknown>> = []
 await page.route('**/api/rules', async (route: Route) => {
  if (route.request().method() !== 'POST') return route.continue()
  const input = route.request().postDataJSON() as Record<string, unknown>
  if (input.action === 'delete') {
   deleteRequests.push(input)
   await route.fulfill({ status: 200, json: { deleted: 401 } })
   return
  }
  if (input.action !== undefined) return route.continue()

  createCalls += 1
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
 await expect.poll(() => deleteRequests.length).toBe(1)
 expect(deleteRequests).toEqual([
  expect.objectContaining({
   action: 'delete',
   selection: { kind: 'explicit', selection_version: expect.any(Number) },
  }),
 ])
})

test('partial memory retry keeps only unresolved selections and never replays automatically', async ({ page }) => {
 const rows = [
  { id: 9101, project: 'operator-console', content: 'T011 known success', tags: ['t011'], tier: 'semantic', epistemic_type: 'fact', confidence: 0.9, citation_count: 1, injection_count: 1, version: 1, created_at: '2026-09-10T12:00:00Z', updated_at: '2026-09-10T12:00:00Z', status: 'active' },
  { id: 9102, project: 'operator-console', content: 'T011 unresolved selection', tags: ['t011'], tier: 'semantic', epistemic_type: 'fact', confidence: 0.9, citation_count: 1, injection_count: 1, version: 1, created_at: '2026-09-10T12:00:00Z', updated_at: '2026-09-10T12:00:00Z', status: 'active' },
 ]
 const selectionPayloads: Array<Record<string, unknown>> = []
 const operationPayloads: Array<Record<string, unknown>> = []
 await page.route('**/api/memories?**', (route: Route) => route.fulfill({ json: rows }))
 for (const row of rows) {
  await page.route(`**/api/memories/${row.id}`, (route: Route) => route.fulfill({ json: row }))
 }
 await page.route('**/api/memories/selection', async (route: Route) => {
  if (route.request().method() !== 'POST') return route.continue()
  selectionPayloads.push(route.request().postDataJSON() as Record<string, unknown>)
  await route.fulfill({
   json: { selection: { kind: 'explicit', selection_version: selectionPayloads.length } },
  })
 })
 await page.route('**/api/memories/operations', async (route: Route) => {
  if (route.request().method() !== 'POST') return route.continue()
  operationPayloads.push(route.request().postDataJSON() as Record<string, unknown>)
  await route.fulfill(operationPayloads.length === 1
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
 await expect.poll(() => selectionPayloads.length).toBe(1)
 expect(selectionPayloads).toEqual([
  {
   selection: {
    kind: 'explicit',
    targets: [
     { id: '9101', expected_version: 1 },
     { id: '9102', expected_version: 1 },
    ],
   },
  },
 ])
 expect(operationPayloads).toEqual([
  expect.objectContaining({
   action: 'suppress',
   selection: { kind: 'explicit', selection_version: 1 },
  }),
 ])

 await apply.click()
 expect(operationPayloads).toHaveLength(1)
 await apply.click()
 await expect(outcome).toHaveAttribute('data-kind', 'committed_verification_pending')
 expect(selectionPayloads).toEqual([
  expect.objectContaining({
   selection: { kind: 'explicit', targets: [{ id: '9101', expected_version: 1 }, { id: '9102', expected_version: 1 }] },
  }),
  expect.objectContaining({
   selection: { kind: 'explicit', targets: [{ id: '9102', expected_version: 1 }] },
  }),
 ])
 expect(operationPayloads).toEqual([
  expect.objectContaining({ action: 'suppress', selection: { kind: 'explicit', selection_version: 1 } }),
  expect.objectContaining({ action: 'suppress', selection: { kind: 'explicit', selection_version: 2 } }),
 ])
 await expect(outcome).not.toContainText(/rollback/i)
})

test('a browser transport loss retains the selected mutation for manual reconciliation', async ({ page }) => {
 const rows = [
  { id: 9103, project: 'operator-console', content: 'T011 lost transport selection', tags: ['t011'], tier: 'semantic', epistemic_type: 'fact', confidence: 0.9, citation_count: 1, injection_count: 1, version: 1, created_at: '2026-09-10T12:00:00Z', updated_at: '2026-09-10T12:00:00Z', status: 'active' },
 ]
 let dispatches = 0
 await page.route('**/api/memories?**', (route: Route) => route.fulfill({ json: rows }))
 await page.route('**/api/memories/9103', (route: Route) => route.fulfill({ json: rows[0] }))
 await page.route('**/api/memories/selection', async (route: Route) => {
  if (route.request().method() !== 'POST') return route.continue()
  await route.fulfill({ json: { selection: { kind: 'explicit', selection_version: 1 } } })
 })
 await page.route('**/api/memories/operations', async (route: Route) => {
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
 await expect.poll(() => dispatches).toBe(1)
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
test('documents create, history, readback, and export render through the current documents surface', async ({ page }) => {
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

 const selectionRequests: Array<Record<string, unknown>> = []
 const exportRequests: Array<Record<string, unknown>> = []
 const artifactID = 'b857ebf7-c1cf-4a1f-a733-465ee492d3cb'
 const filename = `documents-export-${artifactID}.json`
 const apiBase = (process.env.NUXT_PUBLIC_API_BASE || '/api').replace(/\/+$/, '')
 const expectedDownloadURL = `${apiBase}/documents/exports/${artifactID}`
 await page.route('**/api/documents/selection', async (route: Route) => {
  selectionRequests.push(route.request().postDataJSON() as Record<string, unknown>)
  await route.fulfill({ json: { selection: { domain: 'documents', kind: 'explicit', selection_version: 7, targets: [{ id: '2', expected_version: 2 }] } } })
 })
 await page.route('**/api/documents', async (route: Route) => {
  if (route.request().method() !== 'POST') return route.continue()
  const input = route.request().postDataJSON() as Record<string, unknown>
  if (input.action !== 'export') return route.continue()
  exportRequests.push(input)
  await route.fulfill({
   json: {
    request_id: input.request_id,
    operation_id: 'documents-export:browser-proof',
    operation_state: 'completed',
    item_results: [{ target_id: 2, outcome: 'committed', observed_version: 2 }],
    readback: { authoritative: true, kind: 'non_disclosing', operation_status: 'export_ready' },
    artifact: { download_url: `/api/documents/exports/${artifactID}`, filename, content_type: 'application/json', byte_length: 108 },
   }
  })
 })
 await page.route(`**/api/documents/exports/${artifactID}`, (route: Route) => route.fulfill({
  headers: { 'content-type': 'application/json', 'content-disposition': `attachment; filename="${filename}"` },
  body: JSON.stringify({ schema_version: 'engram.documents.export/v1', documents: [{ id: 2, path, project: 'operator-console', version: 2, content_hash: 'safe-hash' }] }),
 }))

 const failures = collectPageFailures(page)
 await page.goto('/documents?project=operator-console')
 await expect(page.locator('.doc-row').filter({ hasText: path })).toBeVisible()
 await expect(page.locator('.history-list')).toContainText('2')
 await expect(page.locator('.doc-content').first()).toHaveText(second)
 const operation = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/documents' && response.request().postDataJSON().action === 'export')
 await page.getByTestId('document-export').click()
 await operation
 await expect(page.getByTestId('mutation-result')).toHaveAttribute('data-kind', 'committed_verified')
 const downloadLink = page.getByTestId('document-export-download')
 await expect(downloadLink).toHaveAttribute('href', expectedDownloadURL)
 await expect(downloadLink).toHaveAttribute('download', filename)
 const downloadPromise = page.waitForEvent('download')
 await downloadLink.click()
 expect((await downloadPromise).suggestedFilename()).toBe(filename)
 expect(selectionRequests).toEqual([{
  domain: 'documents',
  selection: { kind: 'explicit', targets: [{ id: expect.any(String), expected_version: 2 }] },
 }])
 expect(exportRequests).toEqual([expect.objectContaining({
  action: 'export',
  selection: { kind: 'explicit', selection_version: 7 },
 })])
 expect(failures.filter((failure) => failure !== `GET ${expectedDownloadURL} net::ERR_ABORTED`)).toEqual([])
})

test('orphan credential cleanup refreshes only after its validated synchronous receipt', async ({ page }) => {
 const failures = collectPageFailures(page)
 const statusReads: string[] = []
 let cleaned = false
 const credentials = [
  { id: 1, name: 'primary-token', project: 'engram', scope: 'project', created_at: '2026-01-01T00:00:00Z' },
  { id: 2, name: 'orphaned-token', project: 'engram', scope: 'project', created_at: '2026-01-01T00:00:00Z' },
  { id: 3, name: 'shared-token', project: 'beta', scope: 'project', created_at: '2026-01-01T00:00:00Z' },
 ]
 await page.route('**/api/vault/status', (route) => route.fulfill({
  json: {
   key_configured: true,
   fingerprint: 'abcddcba11223344',
   key_source: 'mock',
   credential_count: cleaned ? 2 : 3,
  }
 }))
 await page.route('**/api/vault/credentials', (route) => route.fulfill({ json: cleaned ? credentials.filter((credential) => credential.name !== 'orphaned-token') : credentials }))
 await page.route('**/api/vault/orphaned-credentials', (route) => {
  cleaned = true
  return route.fulfill({ json: { status: 'ok', deleted: 1 } })
 })
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
 await expect(page.getByTestId('mutation-result')).toHaveAttribute('data-kind', 'committed_verified')
 await expect.poll(() => statusReads.length).toBe(2)
 await expect(page.locator('.vault-status')).toContainText('2')
 await expect(page.locator('.vault-tbl')).not.toContainText('orphaned-token')
 expect(failures).toEqual([])
})
