import { expect, test } from '@playwright/test'

test('behavioral rules can be toggled through the live control-plane route', async ({ page }) => {
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

  const isSuccessfulRulesList = (response: import('@playwright/test').Response) =>
    response.request().method() === 'GET'
    && /\/api\/rules\?all=true&limit=200$/.test(response.url())
    && response.status() < 400

  const initialRulesResponsePromise = page.waitForResponse(isSuccessfulRulesList)
  await page.goto('/rules')
  await initialRulesResponsePromise

  const status = page.getByTestId('rule-status-401')
  const toggle = page.getByTestId('rule-enable-toggle-401')
  await expect(status).toBeVisible()
  const initialEnabled = await toggle.getAttribute('aria-checked') === 'true'
  const expectedEnabled = !initialEnabled

  const toggleResponsePromise = page.waitForResponse((response) =>
    response.request().method() === 'PATCH'
    && /\/api\/rules\/401\/enabled$/.test(response.url())
    && response.status() < 400
  )
  const refreshResponsePromise = page.waitForResponse(isSuccessfulRulesList)
  await toggle.click()
  await Promise.all([toggleResponsePromise, refreshResponsePromise])
  await expect(toggle).toHaveAttribute('aria-checked', String(expectedEnabled))
  await expect(toggle).toBeEnabled()

  const reloadRulesResponsePromise = page.waitForResponse(isSuccessfulRulesList)
  await page.reload()
  await reloadRulesResponsePromise
  await expect(page.getByTestId('rule-status-401')).toBeVisible()
  await expect(page.getByTestId('rule-enable-toggle-401')).toHaveAttribute('aria-checked', String(expectedEnabled))

  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
