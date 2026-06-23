import { expect, test } from '@playwright/test'

test('overview renders live model-health snapshot', async ({ page }) => {
  const failedRequests: string[] = []
  const badResponses: string[] = []
  const pageErrors: string[] = []

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

  await page.goto('/')
  await expect(page.locator('h1', { hasText: /^Обзор$/ })).toBeVisible()
  await expect(page.getByText('/api/model-health')).toBeVisible()
  const modelLegend = page.locator('.seg-legend')
  await expect(modelLegend.getByText('ok · 0')).toBeVisible()
  await expect(modelLegend.getByText('standby · 3')).toBeVisible()
  await expect(page.getByText('live endpoint').first()).toBeVisible()

  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
