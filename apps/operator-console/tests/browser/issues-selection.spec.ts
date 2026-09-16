import { expect, test, type Route } from '@playwright/test'

const fingerprint = `sha256:${'a'.repeat(64)}`
const frozenSelection = {
 domain: 'issues',
 kind: 'frozen_filter',
 selection_version: 7,
 selection_token: '8a546ef1-9d60-4b84-b155-72d7ab4b3000',
 filter_fingerprint: fingerprint,
 target_count: 201,
 excluded_ids: [],
 expires_at: '2026-09-17T12:00:00.000Z',
 reconfirmation_required: false,
}

function issue(id: number) {
 return {
  id,
  title: `matching issue ${id}`,
  body: '',
  status: 'open',
  priority: 'medium',
  type: 'task',
  source_project: 'operator-console',
  target_project: 'engram',
  labels: [],
  created_at: '2026-09-16T12:00:00.000Z',
  updated_at: '2026-09-16T12:00:00.000Z',
  comment_count: 0,
 }
}

test('Select all freezes every matching issue when the loaded page is capped at 200 rows', async ({ page }) => {
 const rows = Array.from({ length: 201 }, (_, index) => issue(index + 1))
 const selectionRequests: Array<Record<string, unknown>> = []
 const operationRequests: Array<Record<string, unknown>> = []

 await page.route('**/api/issues/tracked-projects', (route: Route) => route.fulfill({ json: { projects: ['engram'] } }))
 await page.route('**/api/issues?limit=200', (route: Route) => route.fulfill({
  json: { issues: rows.slice(0, 200), total: rows.length, project_names: {} },
 }))
 await page.route('**/api/issues/selection', async (route: Route) => {
  selectionRequests.push(route.request().postDataJSON() as Record<string, unknown>)
  await route.fulfill({ json: { selection: frozenSelection } })
 })
 await page.route('**/api/issues/operations', async (route: Route) => {
  const request = route.request().postDataJSON() as Record<string, unknown>
  operationRequests.push(request)
  await route.fulfill({
   json: {
    request_id: request.request_id,
    operation_state: 'completed',
    readback: {
     authoritative: true,
     kind: 'current',
     current_state: [{ status: 'acknowledged', priority: 'medium', labels: [], updated_at: '2026-09-16T12:00:00.000Z' }],
    },
   },
  })
 })

 await page.goto('/issues')
 await expect(page.locator('.issue-row')).toHaveCount(10)

 await page.getByRole('button', { name: 'Выделить все' }).click()
 await expect(page.locator('.bulkbar .bc')).toContainText('201')
 await page.getByRole('button', { name: 'Открытые' }).click()
 await expect(page.locator('.bulkbar .act').first()).toBeDisabled()
 await page.getByRole('button', { name: 'Выделить все' }).click()
 await page.locator('.bulkbar .act').first().click()
 await page.locator('select[name="issue-bulk-value"]').selectOption('acknowledged')
 await page.locator('.modal .tbtn.primary').click()

 expect(selectionRequests).toEqual([
  { selection: { kind: 'frozen_filter', filter: {}, excluded_ids: [] } },
  { selection: { kind: 'frozen_filter', filter: { statuses: ['open'] }, excluded_ids: [] } },
 ])
 expect(operationRequests).toEqual([
  expect.objectContaining({
   action: 'status',
   status: 'acknowledged',
   selection: {
    kind: 'frozen_filter',
    selection_version: frozenSelection.selection_version,
    selection_token: frozenSelection.selection_token,
   },
  }),
 ])
})
