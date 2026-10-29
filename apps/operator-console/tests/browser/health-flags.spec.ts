import { expect, test } from '@playwright/test'

test.use({ locale: 'ru' })

test('health renders feature flags by area from the live endpoint', async ({ page }) => {
  const consoleProblems: string[] = []
  const failedRequests: string[] = []
  const badResponses: string[] = []
  const pageErrors: string[] = []

  page.on('console', (message) => {
    if (message.type() === 'error' || message.type() === 'warning') {
      consoleProblems.push(`${message.type()}: ${message.text()}`)
    }
  })
  page.on('requestfailed', (request) => {
    failedRequests.push(`${request.method()} ${request.url()} ${request.failure()?.errorText || 'failed'}`)
  })
  page.on('response', (response) => {
    if (response.status() >= 400) {
      badResponses.push(`${response.status()} ${response.request().method()} ${response.url()}`)
    }
  })
  page.on('pageerror', (error) => {
    pageErrors.push(error.message)
  })

  await page.goto('/health')
  await expect(page.getByRole('heading', { name: 'Состояние' })).toBeVisible()

  const flagsCard = page.locator('article.card', { hasText: 'Флаги по областям' })
  await expect(flagsCard).toBeVisible()
  await expect(flagsCard.getByText('/api/flags')).toBeVisible()
  await expect(flagsCard.getByText('vnext')).toBeVisible()
  await expect(flagsCard.getByText(/включено 0.*выключено 1.*restart 1/)).toBeVisible()
  await expect(flagsCard.getByText('code-intel')).toBeVisible()
  await expect(flagsCard.getByText('memory')).toBeVisible()
  await expect(flagsCard.getByText('ENGRAM_VNEXT_ENABLED')).toHaveCount(0)

  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})

test('settings keeps ordinary runtime controls without an injection rollback switch', async ({ page }) => {
  await page.goto('/settings')
  const modal = page.getByRole('dialog')
  await expect(modal).toBeVisible()
  await modal.locator('.settings-tab').filter({ hasText: 'Флаги сервера' }).click()
  const switches = modal.locator('.switches')
  await expect(switches).toBeVisible()
  await expect(switches).toContainText('Telemetry')
  await expect(switches).toContainText('Enforce source project')
  await expect(switches).not.toContainText('Единая инъекция памяти')
  await expect(switches.getByRole('switch')).toHaveCount(2)
  await switches.getByRole('switch').nth(1).click()
  const saved = page.waitForRequest((request) => request.method() === 'PATCH' && new URL(request.url()).pathname === '/api/config')
  await modal.locator('.config-actions .tbtn.primary').click()
  expect((await saved).postDataJSON()).toEqual({ features: { enforce_source_project: false } })
})
