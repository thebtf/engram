import { expect, test } from '@playwright/test'

test.use({ locale: 'ru' })


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

test('home exposes Workspace while primary navigation retires Graph and Books', async ({ page }) => {
  await page.goto('/')

  await expect(page.getByTestId('overview-workspace-entry')).toBeVisible()
  await page.getByTestId('overview-workspace-entry').click()
  await expect(page).toHaveURL(/\/code$/)
  await expect(page.getByRole('link', { name: 'Связи знаний', exact: true })).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'Устаревший импорт', exact: true })).toHaveCount(0)

  await page.getByRole('link', { name: 'Правила поведения', exact: true }).click()
  await expect(page).toHaveURL(/\/rules$/)
})

for (const scenario of [
  { route: '/rules', activeMemoryCount: undefined, state: 'unknown', visible: 'неизвест' },
] as const) {
  test(`shell renders ${scenario.state} memory count truth on ${scenario.route} without global candidate reads`, async ({ page }) => {
    const memoryBodyRequests: string[] = []
    const queueBodyRequests: string[] = []
    const flagRequests: string[] = []
    const settingsOwnedRequests: string[] = []

    page.on('request', (request) => {
      const path = new URL(request.url()).pathname
      if (path === '/api/memories') memoryBodyRequests.push(path)
      if (path === '/api/memory/candidates') queueBodyRequests.push(path)
      if (path === '/api/flags') flagRequests.push(path)
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
    await expect(page.locator('#primary-navigation a[href="/queue"]')).toBeVisible()
    await expect.poll(() => flagRequests.length).toBeGreaterThan(0)
    expect(memoryBodyRequests).toEqual([])
    expect(queueBodyRequests).toEqual([])
    expect(settingsOwnedRequests).toEqual([])
  })
}
