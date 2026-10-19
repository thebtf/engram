import { expect, test, type Route } from '@playwright/test'
const sourceProject = '11111111-1111-4111-8111-111111111111'
const targetProject = '33333333-3333-4333-8333-333333333333'

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
  source_project: sourceProject,
  target_project: targetProject,
  source_project_id: sourceProject,
  target_project_id: targetProject,
  project_origin: { source_project: sourceProject, target_project: targetProject },
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

 await page.route('**/api/issues?**', (route: Route) => route.fulfill({
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

 await page.goto(`/issues?project=${targetProject}`)
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
  { selection: { kind: 'frozen_filter', filter: { project: targetProject }, excluded_ids: [] }, source_project: targetProject, source_agent: 'human' },
  { selection: { kind: 'frozen_filter', filter: { project: targetProject, statuses: ['open'] }, excluded_ids: [] }, source_project: targetProject, source_agent: 'human' },
 ])
 expect(operationRequests).toEqual([
  expect.objectContaining({
   action: 'status',
   source_project: targetProject,
   source_agent: 'human',
   status: 'acknowledged',
   selection: {
    kind: 'frozen_filter',
    selection_version: frozenSelection.selection_version,
    selection_token: frozenSelection.selection_token,
   },
  }),
 ])
})

test('issue source and target choices use distinct catalog IDs, never row aliases', async ({ page }) => {
 await page.route('**/api/projects', (route) => route.fulfill({
  json: {
   items: [
    { project_id: sourceProject, name: 'Same name', repository_qualifier: 'team/source' },
    { project_id: targetProject, name: 'Same name', repository_qualifier: 'team/target' },
   ], unresolved: []
  }
 }))
 await page.route('**/api/issues?**', (route) => route.fulfill({ json: { issues: [{ ...issue(1), source_project: 'historical-row-alias', project_origin: { source_project: 'historical-row-alias', target_project: targetProject } }], total: 1 } }))
 let trackedReads = 0
 page.on('request', (request) => { if (new URL(request.url()).pathname === '/api/issues/tracked-projects') trackedReads++ })
 await page.goto(`/issues?project=${targetProject}`)
 await expect(page.locator('.issue-row')).toHaveCount(1)
 await page.locator('.issue-create-strip button').click()
 const source = page.locator('select[name="new-issue-source-project"]')
 const target = page.locator('select[name="new-issue-target-project"]')
 await expect(source.locator('option')).toHaveCount(3)
 await expect(target.locator('option')).toHaveCount(3)
 await expect(target.locator(`option[value="${sourceProject}"]`)).toContainText('team/source')
 await expect(target.locator(`option[value="${targetProject}"]`)).toContainText('team/target')
 await expect(target.locator('option[value="historical-row-alias"]')).toHaveCount(0)
 await expect(source).toHaveValue(targetProject)
 await expect(source).toBeDisabled()
 await target.selectOption(sourceProject)
 expect(trackedReads).toBe(0)
})

test('human issue create and scoped detail links retain source UUID and explicit cross-project target', async ({ page }) => {
 let created: Record<string, unknown> | null = null
 let requested: Record<string, unknown> | null = null
 const detailProjects: Array<string | null> = []
 await page.route('**/api/issues?**', route => route.fulfill({ json: { issues: created ? [created] : [], total: created ? 1 : 0 } }))
 await page.route('**/api/issues', async route => {
  requested = route.request().postDataJSON()
  created = { ...issue(99), title: 'Synthetic cross-project handoff', source_project: targetProject, target_project: sourceProject, source_project_id: targetProject, target_project_id: sourceProject, project_origin: { source_project: targetProject, target_project: sourceProject }, priority: 'high', type: 'bug', labels: ['operator-created'] }
  await route.fulfill({ json: { id: 99, operation_state: 'completed' } })
 })
 await page.route('**/api/issues/99**', route => {
  detailProjects.push(new URL(route.request().url()).searchParams.get('project'))
  return route.fulfill({ json: { issue: created, comments: [] } })
 })
 await page.goto(`/issues?project=${targetProject}`)
 await page.locator('.issue-create-strip button').click()
 await expect(page.locator('select[name="new-issue-source-project"]')).toHaveValue(targetProject)
 await page.locator('input[name="new-issue-title"]').fill('Synthetic cross-project handoff')
 await page.locator('select[name="new-issue-target-project"]').selectOption(sourceProject)
 await page.locator('.issue-create-modal .tbtn.primary').click()
 await expect(page.getByTestId('mutation-result')).toHaveAttribute('data-kind', 'committed_verified')
 expect(requested).toMatchObject({ source_project: targetProject, target_project: sourceProject, source_agent: 'human' })
 await page.goto(`/issues/99?project=${targetProject}`)
 await expect(page.locator('.issues-page h1')).toContainText('99')
 await expect(page.locator('.issues-page')).toContainText('Synthetic cross-project handoff')
 expect(detailProjects[detailProjects.length - 1]).toBe(targetProject)
})

test('late frozen issue selection cannot restore a prior project scope', async ({ page }) => {
 let started!: () => void
 let release!: () => void
 const requested = new Promise<void>(resolve => { started = resolve })
 const held = new Promise<void>(resolve => { release = resolve })
 await page.route('**/api/issues?**', route => route.fulfill({ json: { issues: [issue(1)], total: 1 } }))
 await page.route('**/api/issues/selection', async route => {
  started()
  await held
  await route.fulfill({ json: { selection: frozenSelection } })
 })
 await page.goto(`/issues?project=${targetProject}`)
 await page.getByRole('button', { name: 'Выделить все' }).click()
 await requested
 await page.getByTestId('project-context-select').selectOption(sourceProject)
 await expect(page.getByTestId('project-context-select')).toHaveValue(sourceProject)
 const response = page.waitForResponse(result => new URL(result.url()).pathname === '/api/issues/selection')
 release()
 await response
 await expect(page.locator('.bulkbar .bc')).toContainText('0')
 await expect(page.locator('.bulkbar .act').first()).toBeDisabled()
})

test('all-project issue browsing keeps detail readable but disables every issue mutation', async ({ page }) => {
 const mutations: string[] = []
 const listScopes: Array<string | null> = []
 await page.route('**/api/issues?**', route => {
  listScopes.push(new URL(route.request().url()).searchParams.get('project'))
  return route.fulfill({ json: { issues: [issue(7)], total: 1 } })
 })
 await page.route('**/api/issues/7**', route => route.fulfill({ json: { issue: issue(7), comments: [] } }))
 await page.route('**/api/issues/selection', route => route.fulfill({ json: { selection: { ...frozenSelection, target_count: 1 } } }))
 page.on('request', request => {
  if (request.method() !== 'GET' && /^\/api\/issues(?:\/(?:acknowledge|operations|selection(?:\/(?:current|page))?|7))?$/.test(new URL(request.url()).pathname)) mutations.push(request.method())
 })
 await page.goto('/issues?project=all')
 await expect(page.locator('.issue-row')).toHaveCount(1)
 expect(listScopes.length).toBeGreaterThan(0)
 expect(listScopes.every(scope => scope === null)).toBe(true)
 await expect(page.getByRole('button', { name: 'Выделить все' })).toBeDisabled()
 await page.locator('.issue-row-check').click()
 await expect(page.locator('.bulkbar .bc')).toContainText('1')
 for (const index of [0, 1, 3]) await expect(page.locator('.bulkbar .act').nth(index)).toBeDisabled()
 await page.locator('.issue-row').click()
 await expect(page.locator('.issue-workspace')).toContainText('matching issue 7')
 await expect(page.locator('select[name="issue-status"]')).toBeDisabled()
 await expect(page.locator('select[name="issue-priority"]')).toBeDisabled()
 await expect(page.locator('select[name="issue-type"]')).toBeDisabled()
 for (const button of await page.locator('.issue-rail .tag-option').all()) await expect(button).toBeDisabled()
 await expect(page.locator('.issue-rail .issue-card').first().locator('.dactions .act')).toBeDisabled()
 await expect(page.locator('.issue-rail .act.danger')).toBeDisabled()
 await expect(page.locator('.issue-danger-actions .act').first()).toBeDisabled()
 await expect(page.locator('.issue-danger-actions .act.danger')).toBeDisabled()
 await page.locator('textarea[name="issue-comment"]').fill('No implicit source')
 await expect(page.locator('.composer .act.primary')).toBeDisabled()
 expect(mutations).toEqual([])
})

test('detail acknowledgement sends one atomic human-source payload and verifies server readback', async ({ page }) => {
 let current = issue(7)
 const mutations: Array<{ method: string; path: string; body: unknown }> = []
 let detailReads = 0
 await page.route('**/api/issues?**', route => route.fulfill({ json: { issues: [current], total: 1 } }))
 await page.route('**/api/issues/7**', route => {
  detailReads++
  return route.fulfill({ json: { issue: current, comments: [] } })
 })
 await page.route('**/api/issues/acknowledge', async route => {
  mutations.push({ method: route.request().method(), path: new URL(route.request().url()).pathname, body: route.request().postDataJSON() })
  current = { ...current, status: 'acknowledged' }
  await route.fulfill({ json: { operation_state: 'completed' } })
 })
 await page.goto(`/issues/7?project=${targetProject}`)
 await expect(page.locator('.issue-workspace')).toContainText('matching issue 7')
 const before = detailReads
 await page.locator('.issue-rail .issue-card').first().locator('.dactions .act').click()
 await expect(page.getByTestId('mutation-result')).toHaveAttribute('data-kind', 'committed_verified')
 await expect(page.locator('select[name="issue-status"]')).toHaveValue('acknowledged')
 expect(mutations).toEqual([{ method: 'POST', path: '/api/issues/acknowledge', body: { ids: [7], source_project: targetProject, source_agent: 'human' } }])
 expect(detailReads).toBeGreaterThan(before)
})
