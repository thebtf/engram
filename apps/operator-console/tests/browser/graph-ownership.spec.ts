import { expect, test, type Page, type Route } from '@playwright/test'

async function enabled(route: Route) {
  await route.fulfill({ json: { flags: { ENGRAM_GRAPH_ENABLED: true } } })
}

async function routeProjects(page: Page) {
  await page.route('**/api/projects', (route) => route.fulfill({ json: ['alpha'] }))
}

async function routeGraph(page: Page, refs = ['node-seven']) {
  await page.route('**/api/graph/nodes**', (route) => route.fulfill({
    json: { nodes: refs.map((external_ref, index) => ({
      id: index + 7,
      node_type: 'skill',
      external_ref,
      project: 'alpha',
      privacy_scope: 'project',
    })) },
  }))
}

test('latest graph refresh owns graph state over an older delayed response', async ({ page }) => {
  let flagsCalls = 0
  let releaseOlder!: () => void
  const older = new Promise<void>((resolve) => { releaseOlder = resolve })
  await page.route('**/api/flags', async (route) => {
    flagsCalls += 1
    if (flagsCalls === 2) {
      await older
      await route.fulfill({ json: { flags: { ENGRAM_GRAPH_ENABLED: false } } })
      return
    }
    await enabled(route)
  })
  await routeProjects(page)
  await routeGraph(page, ['newer-wins'])
  await page.route('**/api/graph/edges**', (route) => route.fulfill({ json: { edges: [] } }))

  await page.goto('/graph')
  await expect(page.locator('.node-row')).toContainText('newer-wins')
  await page.getByRole('button', { name: 'Обновить' }).click()
  await page.getByRole('button', { name: 'Обновить' }).click()
  await expect(page.locator('.node-row')).toContainText('newer-wins')
  releaseOlder()
  await expect.poll(() => flagsCalls).toBeGreaterThanOrEqual(3)
  await expect(page.locator('.node-row')).toContainText('newer-wins')
  await expect(page.locator('.statebar')).toHaveAttribute('data-state', 'live')
})

test('selected-node edge ownership rejects a delayed edge response', async ({ page }) => {
  let releaseNodeEight!: () => void
  const nodeEight = new Promise<void>((resolve) => { releaseNodeEight = resolve })
  await page.route('**/api/flags', enabled)
  await routeProjects(page)
  await routeGraph(page, ['node-seven', 'node-eight'])
  await page.route('**/api/graph/edges**', async (route) => {
    const nodeID = new URL(route.request().url()).searchParams.get('node_id')
    if (nodeID === '8') {
      await nodeEight
      await route.fulfill({ json: { edges: [{
        id: 88,
        edge_type: 'uses',
        source_type: 'node',
        target_type: 'node',
        node_source_id: 8,
        node_target_id: 8,
        reasoning: 'late-edge-for-eight',
      }] } })
      return
    }
    await route.fulfill({ json: { edges: [] } })
  })

  await page.goto('/graph')
  await expect(page.locator('.node-row')).toHaveCount(2)
  await page.locator('.node-row').nth(1).click()
  await page.locator('.node-row').nth(0).click()
  await expect(page.locator('.selection-note')).toContainText('node-seven')
  releaseNodeEight()
  await expect(page.getByText('late-edge-for-eight', { exact: true })).toHaveCount(0)
})

test('a delayed mutation notice cannot overwrite newer validation or dismissal', async ({ page }) => {
  let releaseMutation!: () => void
  const mutation = new Promise<void>((resolve) => { releaseMutation = resolve })
  await page.route('**/api/flags', enabled)
  await routeProjects(page)
  await routeGraph(page)
  await page.route('**/api/graph/edges**', (route) => route.fulfill({ json: { edges: [] } }))
  await page.route('**/api/graph/nodes', async (route) => {
    if (route.request().method() === 'POST') {
      await mutation
      await route.fulfill({ status: 409, json: { error: { code: 'invalid_request', message: 'late mutation failure' } } })
      return
    }
    await route.fallback()
  })

  await page.goto('/graph')
  const ref = page.locator('#graph-node-external-ref')
  const create = page.getByRole('button', { name: 'Создать узел' })
  await ref.fill('late')
  await create.click()
  await ref.fill('')
  await create.click()
  await expect(page.locator('.notice')).toHaveAttribute('data-kind', 'error')
  await expect(page.getByRole('button', { name: 'Закрыть уведомление' })).toBeVisible()
  await page.getByRole('button', { name: 'Закрыть уведомление' }).click()
  releaseMutation()
  await expect(page.locator('.notice')).toHaveCount(0)
  await expect(page.locator('.typed-error')).toHaveCount(0)
})

test('a delayed successful node creation cannot overwrite newer invalid-node validation', async ({ page }) => {
  let releaseMutation!: () => void
  const mutation = new Promise<void>((resolve) => { releaseMutation = resolve })
  await page.route('**/api/flags', enabled)
  await routeProjects(page)
  await routeGraph(page)
  await page.route('**/api/graph/edges**', (route) => route.fulfill({ json: { edges: [] } }))
  await page.route('**/api/graph/nodes', async (route) => {
    if (route.request().method() === 'POST') {
      await mutation
      await route.fulfill({ json: { id: 99, node_type: 'skill', external_ref: 'late-success', project: 'alpha' } })
      return
    }
    await route.fallback()
  })

  await page.goto('/graph')
  const ref = page.locator('#graph-node-external-ref')
  const create = page.getByRole('button', { name: 'Создать узел' })
  await ref.fill('late-success')
  await create.click()
  await ref.fill('')
  await create.click()
  await expect(page.locator('.notice')).toHaveAttribute('data-kind', 'error')
  const mutationResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/graph/nodes')
  releaseMutation()
  await mutationResponse
  await expect(page.locator('.notice')).toHaveAttribute('data-kind', 'error')
  await expect(page.locator('.notice')).toContainText('Нужны тип узла, проект и внешний ref.')
})

test('refresh invalidates a delayed traverse response', async ({ page }) => {
  let releaseTraverse!: () => void
  const traverse = new Promise<void>((resolve) => { releaseTraverse = resolve })
  await page.route('**/api/flags', enabled)
  await routeProjects(page)
  await routeGraph(page)
  await page.route('**/api/graph/edges**', (route) => route.fulfill({ json: { edges: [] } }))
  await page.route('**/api/graph/traverse**', async (route) => {
    await traverse
    await route.fulfill({ json: { results: [{ edge_id: 1, source_id: 'late-source', target_id: 'late-target', edge_type: 'uses', depth: 1 }] } })
  })

  await page.goto('/graph')
  const runTraverse = page.getByRole('button', { name: 'Запустить traverse' })
  await page.locator('#graph-traverse-memory-id').fill('memory-one')
  await runTraverse.click()
  await expect(runTraverse).toBeDisabled()
  await page.getByRole('button', { name: 'Обновить' }).click()
  await expect(runTraverse).toBeEnabled()
  releaseTraverse()
  await expect(page.getByText('late-source', { exact: true })).toHaveCount(0)
})
