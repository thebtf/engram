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
  await expect(page.getByText('ok · 1')).toBeVisible()
  await expect(page.getByText('standby · 2')).toBeVisible()
  await expect(page.getByText('live endpoint').first()).toBeVisible()

  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
