import { expect, test } from '@playwright/test'

test('behavioral rules apply selected-rule disable with an authoritative readback', async ({ page }) => {
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
  await expect(status).toBeVisible()
  await expect(toggle).toHaveAttribute('aria-checked', 'true')

  const selectionResponsePromise = page.waitForResponse((response) =>
    response.request().method() === 'POST'
    && /\/api\/collections\/selection$/.test(new URL(response.url()).pathname)
    && response.status() < 400
  )
  const toggleResponsePromise = page.waitForResponse((response) =>
    response.request().method() === 'POST'
    && /\/api\/rules$/.test(new URL(response.url()).pathname)
    && response.status() < 400
  )
  const ruleRefreshResponsePromise = page.waitForResponse((response) =>
    response.request().method() === 'GET'
    && /\/api\/rules(?:\?|$)/.test(response.url())
    && response.status() < 400
  )
  await toggle.click()
  await selectionResponsePromise
  const toggleResponse = await toggleResponsePromise
  expect(await toggleResponse.json()).toMatchObject({
    operation_state: 'completed',
    readback: { authoritative: true, kind: 'current', current_state: { id: 401, enabled: false } },
  })
  await ruleRefreshResponsePromise
  await expect(page.getByTestId('mutation-result')).toHaveAttribute('data-kind', 'committed_verified')
  await expect(toggle).toBeEnabled()
  await expect(toggle).toHaveAttribute('aria-checked', 'false')

  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.getByTestId('rule-status-401')).toBeVisible()
  await expect(page.getByTestId('rule-enable-toggle-401')).toHaveAttribute('aria-checked', 'false')

  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
