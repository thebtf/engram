import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { RouteTraffic } from './fixture-bootstrap'

type IssuesPage = { cursor: string, nextCursor: string, targets: Array<{ id: string }>, total: number }

function record(value: unknown): value is Record<string, unknown> {
 return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function parseIssuesPage(body: string): IssuesPage {
 const value: unknown = JSON.parse(body)
 if (!record(value) || typeof value.cursor !== 'string' || typeof value.next_cursor !== 'string' || !Array.isArray(value.targets) || !Number.isSafeInteger(value.total)) throw new TypeError('Issues page response is invalid')
 const targets = value.targets.map((target) => {
  if (!record(target) || typeof target.id !== 'string') throw new TypeError('Issues page target is invalid')
  return { id: target.id }
 })
 return { cursor: value.cursor, nextCursor: value.next_cursor, targets, total: value.total }
}

function parseSelectionVersion(body: string): number {
 const value: unknown = JSON.parse(body)
 if (!record(value) || !record(value.selection) || !Number.isSafeInteger(value.selection.selection_version)) throw new TypeError('Issues selection response is invalid')
 return value.selection.selection_version
}

async function requestJSON(page: Page, path: string, body: unknown, requestID = crypto.randomUUID()) {
 return page.evaluate(async ({ requestPath, requestBody, requestID: headerRequestID }) => {
  const response = await fetch(requestPath, {
   method: 'POST',
   credentials: 'include',
   headers: { 'Content-Type': 'application/json', 'X-Engram-Request-ID': headerRequestID },
   body: JSON.stringify(requestBody),
  })
  return { status: response.status, body: await response.text() }
 }, { requestPath: path, requestBody: body, requestID })
}

test('T027 Issues selection actions retain cursor, revision, and readback truth', async ({ page }, testInfo) => {
 const fixture = await readLiveFixture()
 const traffic: RouteTraffic[] = []
 const marker = `t027-${fixture.fixtureId}`

 page.on('response', (response) => {
  const url = new URL(response.url())
  if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/')) {
   traffic.push({ origin: 'browser', method: response.request().method(), path: url.pathname, status: response.status(), at: new Date().toISOString() })
  }
 })

 try {
  expect(fixture.candidate.commit).toBe(fixture.backend.sourceCommit)
  await page.goto(`${fixture.frontend.baseUrl}/issues`, { waitUntil: 'domcontentloaded' })
  expect((await requestJSON(page, '/api/auth/user-login', fixture.browserCredential)).status).toBe(200)

  for (let index = 0; index < 101; index += 1) {
   expect((await requestJSON(page, '/api/issues', {
    title: `${marker} issue ${index}`,
    target_project: marker,
    source_project: marker,
    source_agent: 'operator-console',
    priority: 'medium',
    type: 'task',
   })).status).toBe(201)
  }

  const first = await requestJSON(page, '/api/issues/selection/page', { filter: { project: marker }, limit: 50 })
  expect(first.status).toBe(200)
  const firstPage = parseIssuesPage(first.body)
  expect(firstPage.total).toBe(101)
  expect(firstPage.targets).toHaveLength(50)
  expect(firstPage.nextCursor).not.toBe('')

  const second = await requestJSON(page, '/api/issues/selection/page', { cursor: firstPage.nextCursor, limit: 50 })
  expect(second.status).toBe(200)
  expect(parseIssuesPage(second.body).targets).toHaveLength(50)

  const snapshot = await requestJSON(page, '/api/issues/selection', { selection: { kind: 'page', cursor: firstPage.cursor } })
  expect(snapshot.status).toBe(200)
  const selectionVersion = parseSelectionVersion(snapshot.body)
  const operation = await requestJSON(page, '/api/issues/operations', {
   request_id: 't027-page-priority',
   action: 'priority',
   priority: 'high',
   selection: { kind: 'page', selection_version: selectionVersion }
  }, 't027-page-priority')
  expect(operation.status).toBe(200)
  expect(operation.body).toContain('"operation_state":"completed"')
  expect(operation.body).not.toContain(marker)
  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.locator('.issue-row').first()).toBeVisible()
  await page.locator('.issue-row-check').first().click()
  await page.locator('.bulkbar .act').first().click()

  const bulkStatus = page.locator('select[name="issue-bulk-value"]')
  await expect(bulkStatus.locator('option[value="rejected"]')).toHaveCount(0)
  await bulkStatus.selectOption('acknowledged')

  const statusOperation = page.waitForResponse((response) => {
   const url = new URL(response.url())
   return url.origin === fixture.frontend.baseUrl && url.pathname === '/api/issues/operations' && response.request().method() === 'POST'
  })
  await page.locator('.modal .tbtn.primary').click()
  const statusResponse = await statusOperation
  expect(statusResponse.status()).toBe(200)
  expect(statusResponse.request().postDataJSON()).toMatchObject({ action: 'status', status: 'acknowledged' })
  expect(await statusResponse.text()).toContain('"operation_state":"completed"')
 } finally {
  const state = await appendBrowserTraffic(traffic)
  await testInfo.attach('t027-issues-selection-live', {
   contentType: 'application/json',
   body: Buffer.from(JSON.stringify({ candidate: state.candidate, backend: state.backend, traffic: state.traffic }, null, 2)),
  })
 }
})
