import { expect, test } from '@playwright/test'

test.beforeEach(async ({ page }, testInfo) => {
  await page.context().addCookies([{
    name: 'mock-rule-session',
    value: crypto.randomUUID(),
    url: testInfo.project.use.baseURL!,
  }])
})

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

  await page.goto('/rules?project=all')

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

test('a second browser session cannot replace a pending rule selection operation', async ({ page, browser }) => {
  await page.goto('/rules?project=all')
  const toggle = page.getByTestId('rule-enable-toggle-402')
  await expect(toggle).toHaveAttribute('aria-checked', 'false')

  const otherSession = await browser.newContext({ baseURL: test.info().project.use.baseURL })
  try {
    await otherSession.addCookies([{
      name: 'mock-rule-session',
      value: crypto.randomUUID(),
      url: test.info().project.use.baseURL!,
    }])
    await page.route('**/api/rules', async (route) => {
      if (route.request().method() === 'POST' && route.request().postDataJSON().action === 'enable') {
        const competingSave = await otherSession.request.post('/api/collections/selection', {
          data: { domain: 'rules', filter: { scope: 'all' }, selection: { kind: 'explicit', targets: [{ id: '401' }] } },
        })
        expect(competingSave.ok()).toBe(true)
      }
      await route.continue()
    })

    const operation = page.waitForResponse((response) => response.request().method() === 'POST'
      && new URL(response.url()).pathname === '/api/rules')
    await toggle.click()
    const result = await operation
    expect(result.status(), await result.text()).toBe(200)
    await expect(toggle).toHaveAttribute('aria-checked', 'true')
  } finally {
    await otherSession.close()
  }
})

test('Rules require an explicit scope and preserve its canonical ID through refresh', async ({ page }) => {
  const reads: URL[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (request.method() === 'GET' && url.pathname === '/api/rules') reads.push(url) })
  await page.goto('/rules')
  await expect(page.getByTestId('project-context-select')).toHaveValue('')
  expect(reads).toHaveLength(0)
  const project = '11111111-1111-4111-8111-111111111111'
  await page.goto(`/rules?project=${project}`)
  await expect.poll(() => reads.length).toBeGreaterThan(0)
  await page.locator('.toolbar button').click()
  await expect.poll(() => reads.length).toBeGreaterThan(1)
  expect(reads.every((url) => url.searchParams.get('project') === project)).toBe(true)
  await expect(page.getByTestId('project-context-select')).toHaveValue(project)
})
