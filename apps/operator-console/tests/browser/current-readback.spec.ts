import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'

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

test('books ingest reads its current job status back from the server', async ({ page }) => {
  const failures = collectPageFailures(page)
  await page.goto('/books')

  const textInputs = page.locator('.books-page input[type="text"]')
  await textInputs.nth(0).fill('readback-book.md')
  await textInputs.nth(1).fill('operator-console')
  await textInputs.nth(2).fill('operator-console')
  await page.locator('.books-page textarea').fill('# Readback book\n\nA current book-ingestion fixture.')

  const createResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/books')
  await page.locator('.books-page button.act.primary').click()
  expect(await (await createResponse).json()).toMatchObject({ status: 'pending', source_ref: 'readback-book.md' })

  const statusResponse = page.waitForResponse((response) => response.request().method() === 'GET' && /\/api\/books\/\d+\/status$/.test(new URL(response.url()).pathname))
  await page.locator('.books-page button.tbtn').first().click()
  expect(await (await statusResponse).json()).toMatchObject({ status: 'done', source_ref: 'readback-book.md', documents_path_prefix: 'books/jobs/1/' })
  await expect(page.locator('.books-page .status-chip')).toHaveAttribute('data-state', 'done')
  await expect(page.locator('.books-page .fields')).toContainText('readback-book.md')
  expect(failures).toEqual([])
})

test('rules create and edit round-trip through current control-plane routes', async ({ page }) => {
  const failures = collectPageFailures(page)
  await page.goto('/rules')

  await page.locator('.rules-page > .head > button.primary').click()
  const dialog = page.getByRole('dialog')
  await dialog.locator('textarea').fill('readback rule before edit')
  const createResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/rules')
  await dialog.locator('button.primary').click()
  const created = await (await createResponse).json()
  expect(created).toMatchObject({ content: 'readback rule before edit', enabled: true, version: 1 })

  const row = page.locator('.rule-row').filter({ hasText: 'readback rule before edit' })
  await expect(row).toBeVisible()
  await row.locator('button.act').first().click()
  await page.locator('.rule-row.editing textarea').fill('readback rule after edit')
  const editResponse = page.waitForResponse((response) => response.request().method() === 'PATCH' && new URL(response.url()).pathname === `/api/rules/${created.id}`)
  await page.locator('.rule-row.editing button.primary-line').click()
  expect(await (await editResponse).json()).toMatchObject({ id: created.id, content: 'readback rule after edit', version: 2 })
  await expect(page.locator('.rule-row').filter({ hasText: 'readback rule after edit' })).toBeVisible()
  expect(failures).toEqual([])
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

test('orphan credential cleanup returns current receipt and refreshes truthful vault status', async ({ page }) => {
  const failures = collectPageFailures(page)
  await page.goto('/secrets')
  await expect(page.locator('.vault-status')).toContainText('3')

  const cleanupResponse = page.waitForResponse((response) => response.request().method() === 'DELETE' && new URL(response.url()).pathname === '/api/vault/orphaned-credentials')
  const statusResponse = page.waitForResponse((response) => response.request().method() === 'GET' && new URL(response.url()).pathname === '/api/vault/status')
  await page.locator('.rotation-card button.secondary').nth(1).click()
  expect(await (await cleanupResponse).json()).toEqual({ status: 'ok', deleted: 1 })
  expect(await (await statusResponse).json()).toMatchObject({ credential_count: 2 })
  await expect(page.locator('.vault-status')).toContainText('2')
  expect(failures).toEqual([])
})
