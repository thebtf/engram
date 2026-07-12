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

test('late older refresh cannot replace a newer live graph with a gated state', async ({ page }) => {
  let calls = 0
  let delayNext = false
  let delayedCall = 0
  let releaseOld!: () => void
  const oldWait = new Promise<void>((resolve) => { releaseOld = resolve })
  await page.route('**/api/flags', async (route) => {
    calls += 1
    if (delayNext && delayedCall === 0) {
      delayedCall = calls
      await oldWait
      await fulfillFlags(route, 'false')
      return
    }
    await fulfillFlags(route, 'true')
  })
  await routeGraph(page)
  await page.goto('/graph')
  await expect(page.locator('.statebar')).toContainText('Из живого графа загружено 1')
  delayNext = true
  await page.getByRole('button', { name: 'Обновить' }).click()
  await expect.poll(() => delayedCall).toBeGreaterThan(0)
  await page.getByRole('button', { name: 'Обновить' }).click()
  await expect.poll(() => calls).toBeGreaterThan(delayedCall)
  await expect(page.locator('.statebar')).toContainText('Из живого графа загружено 1')
  releaseOld()
  await page.waitForTimeout(100)
  await expect(page.locator('.statebar')).toContainText('Из живого графа загружено 1')
  await expect(page.locator('.node-row')).toContainText('operator-console')
})

test('late edge response cannot render data for a previously selected node', async ({ page }) => {
  let releaseNode8!: () => void
  const node8Wait = new Promise<void>((resolve) => { releaseNode8 = resolve })
  await page.route('**/api/flags', (route) => fulfillFlags(route, 'true'))
  await page.route('**/api/projects', (route) => route.fulfill({ json: ['operator-console'] }))
  await page.route('**/api/graph/nodes**', (route) => route.fulfill({ json: { nodes: [
    { id: 7, node_type: 'skill', external_ref: 'node-seven', project: 'operator-console', privacy_scope: 'project' },
    { id: 8, node_type: 'skill', external_ref: 'node-eight', project: 'operator-console', privacy_scope: 'project' },
  ] } }))
  await page.route('**/api/graph/edges**', async (route) => {
    const nodeID = new URL(route.request().url()).searchParams.get('node_id')
    if (nodeID === '8') {
      await node8Wait
      await route.fulfill({ json: { edges: [{
        id: 88, edge_type: 'uses', source_type: 'node', target_type: 'node',
        node_source_id: 8, node_target_id: 8, reasoning: 'late-edge-for-eight',
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
  releaseNode8()
  await page.waitForTimeout(100)
  await expect(page.locator('.selection-note')).toContainText('node-seven')
  await expect(page.getByText('late-edge-for-eight', { exact: true })).toHaveCount(0)
})

test('late edge failure cannot replace a newer selection success', async ({ page }) => {
  let releaseNode8!: () => void
  const node8Wait = new Promise<void>((resolve) => { releaseNode8 = resolve })
  await page.route('**/api/flags', (route) => fulfillFlags(route, 'true'))
  await page.route('**/api/projects', (route) => route.fulfill({ json: ['operator-console'] }))
  await page.route('**/api/graph/nodes**', (route) => route.fulfill({ json: { nodes: [
    { id: 7, node_type: 'skill', external_ref: 'node-seven', project: 'operator-console', privacy_scope: 'project' },
    { id: 8, node_type: 'skill', external_ref: 'node-eight', project: 'operator-console', privacy_scope: 'project' },
  ] } }))
  await page.route('**/api/graph/edges**', async (route) => {
    if (new URL(route.request().url()).searchParams.get('node_id') === '8') {
      await node8Wait
      await route.fulfill({ status: 500, json: { error: { code: 'late_failure', message: 'late edge failure' } } })
      return
    }
    await route.fulfill({ json: { edges: [] } })
  })
  await page.goto('/graph')
  await page.locator('.node-row').nth(1).click()
  await page.locator('.node-row').nth(0).click()
  await expect(page.locator('.selection-note')).toContainText('node-seven')
  releaseNode8()
  await page.waitForTimeout(100)
  await expect(page.getByText('late edge failure', { exact: true })).toHaveCount(0)
  await expect(page.locator('.selection-note')).toContainText('node-seven')
})

test('unmounted graph request cannot overwrite the remounted graph state', async ({ page }) => {
  let calls = 0
  let delayNext = false
  let delayedCall = 0
  let releaseOld!: () => void
  const oldWait = new Promise<void>((resolve) => { releaseOld = resolve })
  await page.route('**/api/flags', async (route) => {
    calls += 1
    if (delayNext && delayedCall === 0) {
      delayedCall = calls
      await oldWait
      await fulfillFlags(route, 'false')
      return
    }
    await fulfillFlags(route, 'true')
  })
  await routeGraph(page)
  await page.goto('/graph')
  await expect(page.locator('.statebar')).toContainText('Из живого графа загружено 1')
  delayNext = true
  await page.getByRole('button', { name: 'Обновить' }).click()
  await expect.poll(() => delayedCall).toBeGreaterThan(0)
  await page.locator('a[href="/queue"]').first().click()
  await expect(page).toHaveURL(/\/queue$/)
  await page.locator('a[href="/graph"]').first().click()
  await expect.poll(() => calls).toBeGreaterThan(delayedCall)
  await expect(page.locator('.statebar')).toContainText('Из живого графа загружено 1')
  releaseOld()
  await page.waitForTimeout(100)
  await expect(page.locator('.statebar')).toContainText('Из живого графа загружено 1')
})

test('late traversal cannot publish detail after the whole graph becomes gated', async ({ page }) => {
  let gateNext = false
  let releaseTraverse!: () => void
  const traverseWait = new Promise<void>((resolve) => { releaseTraverse = resolve })
  await page.route('**/api/flags', (route) => fulfillFlags(route, gateNext ? 'false' : 'true'))
  await routeGraph(page)
  await page.route('**/api/graph/traverse**', async (route) => {
    await traverseWait
    await route.fulfill({ json: { results: [{ edge_id: 91, source_id: 1, target_id: 2, edge_type: 'uses', depth: 1 }] } })
  })
  await page.goto('/graph')
  await expect(page.locator('.statebar')).toContainText('Из живого графа загружено 1')
  await page.getByLabel('Стартовый memory ID').fill('1')
  await page.getByRole('button', { name: 'Запустить traverse' }).click()
  await expect(page.getByRole('button', { name: 'Выполняем…' })).toBeDisabled()
  gateNext = true
  await page.getByRole('button', { name: 'Обновить' }).click()
  await expect(page.locator('.statebar')).toContainText('отключен')
  releaseTraverse()
  await page.waitForTimeout(100)
  await expect(page.locator('.trace-list')).toHaveCount(0)
  await expect(page.locator('.analysis-grid .count').first()).toHaveText('0')
})

test('late path response cannot publish detail after the whole graph becomes gated', async ({ page }) => {
  let gateNext = false
  let releasePath!: () => void
  const pathWait = new Promise<void>((resolve) => { releasePath = resolve })
  await page.route('**/api/flags', (route) => fulfillFlags(route, gateNext ? 'false' : 'true'))
  await routeGraph(page)
  await page.route('**/api/graph/find-path**', async (route) => {
    await pathWait
    await route.fulfill({ json: { found: true, hops: 1, path: [{ edge_id: 92, source_id: 1, target_id: 2, edge_type: 'uses', depth: 1 }] } })
  })
  await page.goto('/graph')
  await expect(page.locator('.statebar')).toContainText('Из живого графа загружено 1')
  await page.getByLabel('Source memory ID').fill('1')
  await page.getByLabel('Target memory ID').fill('2')
  await page.getByRole('button', { name: 'Найти путь' }).click()
  await expect(page.getByRole('button', { name: 'Выполняем…' })).toBeDisabled()
  gateNext = true
  await page.getByRole('button', { name: 'Обновить' }).click()
  await expect(page.locator('.statebar')).toContainText('отключен')
  releasePath()
  await page.waitForTimeout(100)
  await expect(page.locator('.path-found')).toHaveCount(0)
  await expect(page.locator('.analysis-grid .count').last()).toHaveText('0')
})

test('late mutation failure cannot overwrite the newer successful action notice', async ({ page }) => {
  let postCalls = 0
  let releaseOld!: () => void
  const oldWait = new Promise<void>((resolve) => { releaseOld = resolve })
  await page.route('**/api/flags', (route) => fulfillFlags(route, 'true'))
  await routeGraph(page)
  await page.route('**/api/graph/nodes', async (route) => {
    if (route.request().method() !== 'POST') {
      await route.fallback()
      return
    }
    postCalls += 1
    if (postCalls === 1) {
      await oldWait
      await route.fulfill({ status: 500, json: { error: { code: 'late_mutation_failure', message: 'late mutation failure' } } })
      return
    }
    await route.fulfill({ json: { id: 9, node_type: 'skill', external_ref: 'newer', project: 'operator-console', privacy_scope: 'project' } })
  })
  await page.goto('/graph')
  await expect(page.locator('.statebar')).toContainText('Из живого графа загружено 1')
  await page.getByLabel('Внешний ref').fill('first')
  await page.getByRole('button', { name: 'Создать узел' }).click()
  await expect.poll(() => postCalls).toBe(1)
  await page.getByLabel('Внешний ref').fill('second')
  await page.getByRole('button', { name: 'Создать узел' }).click()
  await expect.poll(() => postCalls).toBe(2)
  await expect(page.locator('.notice[data-kind="success"]')).toBeVisible()
  releaseOld()
  await page.waitForTimeout(100)
  await expect(page.getByText('late mutation failure', { exact: true })).toHaveCount(0)
  await expect(page.locator('.typed-error')).toHaveCount(0)
  await expect(page.locator('.notice[data-kind="success"]')).toBeVisible()
})

for (const failure of ['500', 'malformed'] as const) {
  test(`graph node ${failure} response stays non-live and retry recovers`, async ({ page }) => {
    let failing = true
    await page.route('**/api/flags', (route) => fulfillFlags(route, 'true'))
    await routeGraph(page)
    await page.route('**/api/graph/nodes**', async (route) => {
      if (failing) {
        if (failure === '500') await route.fulfill({ status: 500, json: { error: { code: 'graph_read_failed', message: 'probe failure' } } })
        else await route.fulfill({ status: 200, contentType: 'application/json', body: '{bad-json' })
        return
      }
      await route.fulfill({ json: { nodes: [{ id: 7, node_type: 'skill', external_ref: 'recovered', project: 'operator-console', privacy_scope: 'project' }] } })
    })
    await page.goto('/graph')
    await expect(page.locator('.statebar')).toContainText('Не удалось загрузить граф')
    await expect(page.locator('[data-cls="live"]')).toHaveCount(0)
    failing = false
    await page.getByRole('button', { name: 'Повторить' }).click()
    await expect(page.locator('.statebar')).toContainText('Из живого графа загружено 1')
    await expect(page.locator('.node-row')).toContainText('recovered')
  })
}

test('typed mutation error exposes the server code contract as an alert', async ({ page }) => {
  await page.route('**/api/flags', (route) => fulfillFlags(route, 'true'))
  await routeGraph(page)
  await page.route('**/api/graph/nodes', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({ status: 409, json: { error: { code: 'duplicate_edge', message: 'typed-probe' } } })
      return
    }
    await route.fallback()
  })
  await page.goto('/graph')
  await page.getByLabel('Внешний ref').fill('duplicate')
  await page.getByRole('button', { name: 'Создать узел' }).click()
  await expect(page.getByRole('alert')).toContainText('Типизированная ошибка графа')
  await expect(page.getByRole('alert')).toContainText('уже существует')
})

test('notice close control remains translated and named in ru, en, and zh', async ({ page }) => {
  await page.route('**/api/flags', (route) => fulfillFlags(route, 'true'))
  await routeGraph(page)
  await page.goto('/graph')
  await page.getByRole('button', { name: 'Создать узел' }).click()
  await expect(page.getByRole('button', { name: 'Закрыть уведомление' })).toBeVisible()
  await page.locator('button.lang').click()
  await expect(page.getByRole('button', { name: 'Close notification' })).toBeVisible()
  await page.locator('button.lang').click()
  await expect(page.getByRole('button', { name: '关闭通知' })).toBeVisible()
})

test('locale key sets remain aligned for en, ru, and zh', async () => {
  const locales = await Promise.all(['en', 'ru', 'zh'].map(async (locale) => {
    const value = await import(`../../i18n/locales/${locale}.json`, { with: { type: 'json' } })
    return value.default as Record<string, unknown>
  }))
  const flatten = (value: Record<string, unknown>, prefix = ''): string[] => Object.entries(value).flatMap(([key, child]) => {
    const path = prefix ? `${prefix}.${key}` : key
    return child && typeof child === 'object' && !Array.isArray(child)
      ? flatten(child as Record<string, unknown>, path)
      : [path]
  }).sort()
  expect(flatten(locales[1])).toEqual(flatten(locales[0]))
  expect(flatten(locales[2])).toEqual(flatten(locales[0]))
})

test('pending flag recheck suppresses graph detail traffic from stale node rows', async ({ page }) => {
  let delayNext = false
  let releaseFlag!: () => void
  const flagWait = new Promise<void>((resolve) => { releaseFlag = resolve })
  const graphRequests: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname.startsWith('/api/graph/')) graphRequests.push(request.url())
  })
  await page.route('**/api/flags', async (route) => {
    if (delayNext) {
      await flagWait
      await fulfillFlags(route, 'false')
      return
    }
    await fulfillFlags(route, 'true')
  })
  await page.route('**/api/projects', (route) => route.fulfill({ json: ['operator-console'] }))
  await page.route('**/api/graph/nodes**', (route) => route.fulfill({ json: { nodes: [
    { id: 7, node_type: 'skill', external_ref: 'node-seven', project: 'operator-console', privacy_scope: 'project' },
    { id: 8, node_type: 'skill', external_ref: 'node-eight', project: 'operator-console', privacy_scope: 'project' },
  ] } }))
  await page.route('**/api/graph/edges**', (route) => route.fulfill({ json: { edges: [] } }))
  await page.goto('/graph')
  await expect(page.locator('.node-row')).toHaveCount(2)
  const baseline = graphRequests.length
  delayNext = true
  await page.getByRole('button', { name: 'Обновить' }).click()
  await expect(page.locator('.statebar')).toContainText('Загружаем граф')
  await page.locator('.node-row').nth(1).click()
  await page.waitForTimeout(100)
  expect(graphRequests).toHaveLength(baseline)
  releaseFlag()
  await expect(page.locator('.statebar')).toContainText('отключен')
})
