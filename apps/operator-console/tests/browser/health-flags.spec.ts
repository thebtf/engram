import { expect, test } from '@playwright/test'

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
  await expect(flagsCard.getByText(/включено 2.*выключено 4.*restart 6/)).toBeVisible()
  await expect(flagsCard.getByText('code-intel')).toBeVisible()
  await expect(flagsCard.getByText('memory')).toBeVisible()
  await expect(flagsCard.getByText('ENGRAM_VNEXT_ENABLED')).toHaveCount(0)

  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
