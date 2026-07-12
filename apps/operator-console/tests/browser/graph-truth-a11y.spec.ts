import { expect, test, type Page, type Route } from '@playwright/test'

const graphFields = [
  ['graph-project-filter', 'Проект графа'],
  ['graph-node-type', 'Тип узла'],
  ['graph-node-external-ref', 'Внешний ref'],
  ['graph-node-privacy', 'Область приватности'],
  ['graph-edge-source-node', 'ID исходного узла'],
  ['graph-edge-target-node', 'ID целевого узла'],
  ['graph-edge-type', 'Тип ребра'],
  ['graph-edge-reasoning', 'Обоснование'],
  ['graph-traverse-memory-id', 'Стартовый memory ID'],
  ['graph-traverse-depth', 'Глубина'],
  ['graph-path-source-id', 'Source memory ID'],
  ['graph-path-target-id', 'Target memory ID'],
  ['graph-path-max-depth', 'Макс. глубина'],
  ['graph-delete-cascade', 'Каскадно удалить связанные ребра'],
] as const

async function routeGraph(page: Page) {
  await page.route('**/api/graph/**', async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/graph/nodes') {
      await route.fulfill({ json: { nodes: [{ id: 1, node_type: 'skill', external_ref: 'operator-console', project: 'operator-console', privacy_scope: 'project' }] } })
      return
    }
    if (url.pathname === '/api/graph/edges') {
      await route.fulfill({ json: { edges: [] } })
      return
    }
    await route.fulfill({ json: {} })
  })
}

async function fulfillFlags(route: Route, state: 'true' | 'false' | 'missing' | 'error' | 'malformed') {
  if (state === 'error') {
    await route.fulfill({ status: 500, json: { error: 'flag lookup failed' } })
    return
  }
  if (state === 'malformed') {
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{not-json' })
    return
  }
  await route.fulfill({ json: { flags: state === 'missing' ? {} : { ENGRAM_GRAPH_ENABLED: state === 'true' } } })
}

for (const state of ['false', 'missing', 'error', 'malformed'] as const) {
  test(`overview never claims graph enabled when flags are ${state}`, async ({ page }) => {
    await page.route('**/api/flags', (route) => fulfillFlags(route, state))
    await page.goto('/')
    const graphCard = page.locator('a.ov-card[href="/graph"]')
    await expect(graphCard).toBeVisible()
    await expect(graphCard.getByText('включено', { exact: true })).toHaveCount(0)
    await expect(graphCard).toContainText(state === 'false' || state === 'missing' ? 'выключено' : 'недоступно')
  })
}

test('overview claims graph enabled only after a true /api/flags response', async ({ page }) => {
  let observedTrueFlag = false
  await page.route('**/api/flags', async (route) => {
    observedTrueFlag = true
    await fulfillFlags(route, 'true')
  })
  await page.goto('/')
  const graphCard = page.locator('a.ov-card[href="/graph"]')
  await expect(graphCard.getByText('включено', { exact: true })).toBeVisible()
  expect(observedTrueFlag).toBe(true)
})

test('overview keeps graph pending non-live', async ({ page }) => {
  let release!: () => void
  const wait = new Promise<void>((resolve) => { release = resolve })
  await page.route('**/api/flags', async (route) => {
    await wait
    await fulfillFlags(route, 'false')
  })
  await page.goto('/')
  const graphCard = page.locator('a.ov-card[href="/graph"]')
  await expect(graphCard.getByText('включено', { exact: true })).toHaveCount(0)
  await expect(graphCard).toContainText('проверяем флаг')
  release()
  await expect(graphCard).toContainText('выключено')
})

for (const state of ['false', 'missing', 'error', 'malformed'] as const) {
  test(`graph makes no graph request and disables controls when flags are ${state}`, async ({ page }) => {
    const graphRequests: string[] = []
    page.on('request', (request) => {
      if (new URL(request.url()).pathname.startsWith('/api/graph/')) graphRequests.push(request.url())
    })
    await page.route('**/api/flags', (route) => fulfillFlags(route, state))
    await page.goto('/graph')
    await expect(page.locator('.statebar')).toBeVisible()
    await expect(page.locator('[data-cls="live"]')).toHaveCount(0)
    await expect(page.getByLabel('Проект графа')).toBeDisabled()
    expect(graphRequests).toEqual([])
  })
}

test('graph pending state is non-live and makes no graph request', async ({ page }) => {
  let release!: () => void
  const wait = new Promise<void>((resolve) => { release = resolve })
  const graphRequests: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname.startsWith('/api/graph/')) graphRequests.push(request.url())
  })
  await page.route('**/api/flags', async (route) => {
    await wait
    await fulfillFlags(route, 'false')
  })
  await page.goto('/graph')
  await expect(page.locator('.statebar')).toContainText('Загружаем граф')
  await expect(page.locator('[data-cls="live"]')).toHaveCount(0)
  await expect(page.getByLabel('Проект графа')).toBeDisabled()
  expect(graphRequests).toEqual([])
  release()
})

for (const width of [1440, 768, 375]) {
  test(`graph exposes explicit accessible names without overflow at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.route('**/api/flags', (route) => fulfillFlags(route, 'true'))
    await routeGraph(page)
    await page.goto('/graph')
    await expect(page.locator('.statebar')).toContainText('Из живого графа')

    for (const [id, label] of graphFields) {
      const control = page.locator(`#${id}`)
      await expect(control).toHaveCount(1)
      await expect(control).toHaveAccessibleName(label)
      await expect(control).toHaveAttribute('name', id)
    }

    const ids = await page.locator('[id]').evaluateAll((elements) => elements.map((element) => element.id))
    expect(new Set(ids).size).toBe(ids.length)
    await expect(page.locator('[role="status"][aria-live="polite"]')).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
  })
}

test('graph keyboard path has named focus targets and a secret-negative clean trace', async ({ page }) => {
  const problems: string[] = []
  const requestTrace: Array<{ url: URL; headers: Record<string, string>; body: string }> = []
  page.on('console', (message) => {
    if (message.type() === 'error' || message.type() === 'warning') problems.push(`${message.type()}: ${message.text()}`)
  })
  page.on('pageerror', (error) => problems.push(`pageerror: ${error.message}`))
  page.on('requestfailed', (request) => problems.push(`requestfailed: ${request.method()} ${request.url()}`))
  page.on('request', (request) => {
    if (new URL(request.url()).pathname.startsWith('/api/')) {
      requestTrace.push({ url: new URL(request.url()), headers: request.headers(), body: request.postData() || '' })
    }
  })
  await page.route('**/api/flags', (route) => fulfillFlags(route, 'true'))
  await routeGraph(page)
  await page.goto('/graph')

  await page.keyboard.press('Tab')
  for (let i = 0; i < 40; i += 1) {
    const focused = page.locator(':focus')
    if (await focused.count()) {
      const name = await focused.getAttribute('aria-label') || await focused.textContent() || await focused.getAttribute('name')
      expect(name?.trim()).toBeTruthy()
    }
    await page.keyboard.press('Tab')
  }

  await page.getByRole('button', { name: 'Создать узел' }).click()
  await expect(page.locator('.notice')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Закрыть уведомление' })).toBeVisible()
  expect(problems).toEqual([])
  for (const request of requestTrace) {
    expect(request.url.search).not.toMatch(/bearer|password|api[_-]?key|secret/i)
    expect(request.body).not.toMatch(/bearer|password|api[_-]?key|secret/i)
    expect(Object.entries(request.headers).filter(([name]) => name.toLowerCase() === 'authorization')).toEqual([])
  }
})
