import { expect, test } from '@playwright/test'

test('behavioral rules can be disabled through the live control-plane route', async ({ page }) => {
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

  await page.goto('/rules')

  const status = page.getByTestId('rule-status-401')
  const toggle = page.getByTestId('rule-enable-toggle-401')
  await expect(status).toHaveText('включено')
  await expect(toggle).toHaveAttribute('aria-checked', 'true')

  await toggle.click()
  await expect(status).toHaveText('выключено')
  await expect(toggle).toHaveAttribute('aria-checked', 'false')

  await page.reload()
  await expect(page.getByTestId('rule-status-401')).toHaveText('выключено')
  await expect(page.getByTestId('rule-enable-toggle-401')).toHaveAttribute('aria-checked', 'false')

  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
