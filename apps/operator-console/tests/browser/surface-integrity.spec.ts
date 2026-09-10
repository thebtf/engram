import { expect, test } from '@playwright/test'

test.use({ locale: 'ru' })

test('runtime-derived navigation stays neutral until delayed flags prove its state', async ({ page }) => {
  let releaseFlags: (() => void) | undefined
  const flagsReleased = new Promise<void>((resolve) => { releaseFlags = resolve })

  await page.route('**/api/flags', async (route) => {
    await flagsReleased
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ flags: { ENGRAM_GRAPH_ENABLED: false, ENGRAM_VNEXT_F_ENABLED: false } }),
    })
  })

  await page.goto('/')
  const graphDot = page.getByRole('link', { name: 'Связи знаний' }).locator('.ndot')
  const queueDot = page.getByRole('link', { name: 'На проверку' }).locator('.ndot')

  await expect(graphDot).toHaveAttribute('data-s', 'off')
  await expect(queueDot).toHaveAttribute('data-s', 'off')

  releaseFlags?.()
  await expect(graphDot).toHaveAttribute('data-s', 'gated')
  await expect(queueDot).toHaveAttribute('data-s', 'gated')
})

test('mobile topbar keeps primary controls reachable and settings access copy stays localized', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/settings')

  const topbar = page.locator('.topbar')
  await expect(topbar.locator('.mobile-menu-button')).toBeVisible()
  await expect(topbar.locator('.gsearch')).toBeVisible()
  const secondaryControls = topbar.locator('.topbar-secondary')
  await expect(secondaryControls).toHaveCount(3)
  for (let index = 0; index < 3; index += 1) await expect(secondaryControls.nth(index)).toBeHidden()

  const dialog = page.getByRole('dialog', { name: 'Настройки сервера' })
  await dialog.getByRole('button', { name: 'Доступ', exact: true }).click()
  await expect(dialog.getByRole('heading', { name: 'Управление доступом' })).toBeVisible()
  await expect(dialog.locator('.settings-head p').filter({ hasText: 'Отдельный редактор политик недоступен.' })).toBeVisible()
  await expect(dialog.getByText('Access policy')).toHaveCount(0)
})

test('primary shell navigation reaches graph, legacy import, and rules', async ({ page }) => {
  await page.goto('/')

  for (const [label, route] of [
    ['Связи знаний', '/graph'],
    ['Устаревший импорт', '/books'],
    ['Правила поведения', '/rules'],
  ]) {
    await page.getByRole('link', { name: label, exact: true }).click()
    await expect(page).toHaveURL(new RegExp(`${route}$`))
  }
})

for (const scenario of [
  { route: '/graph', activeMemoryCount: 7, state: 'bounded', visible: '7' },
  { route: '/books', activeMemoryCount: 0, state: 'zero', visible: 'нет' },
  { route: '/rules', activeMemoryCount: undefined, state: 'unknown', visible: 'неизвест' },
] as const) {
  test(`shell renders ${scenario.state} memory count truth on ${scenario.route}`, async ({ page }) => {
    const memoryBodyRequests: string[] = []
    const queueBodyRequests: string[] = []
    const settingsOwnedRequests: string[] = []

    page.on('request', (request) => {
      const path = new URL(request.url()).pathname
      if (path === '/api/memories') memoryBodyRequests.push(path)
      if (path === '/api/memory/candidates') queueBodyRequests.push(path)
      if (['/api/config', '/api/vector/metrics', '/api/update/status', '/api/update/check', '/api/model-health'].includes(path)) {
        settingsOwnedRequests.push(path)
      }
    })

    await page.route('**/api/stats/vnext', (route) => route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        noise_ratio: 0.25,
        ...(scenario.activeMemoryCount === undefined ? {} : {
          embedding: { active_memory_count: scenario.activeMemoryCount },
        }),
      }),
    }))

    await page.goto(scenario.route)

    const count = page.getByTestId('shell-memory-count')
    await expect(count).toHaveAttribute('data-count-state', scenario.state)
    await expect(count).toContainText(scenario.visible)
    await expect(page.getByTestId('shell-review-queue-count')).toContainText('неизвест')
    await page.waitForTimeout(100)
    expect(memoryBodyRequests).toEqual([])
    expect(queueBodyRequests).toEqual([])
    expect(settingsOwnedRequests).toEqual([])
  })
}
