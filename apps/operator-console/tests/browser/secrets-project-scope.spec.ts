import { expect, test } from '@playwright/test'

test.beforeEach(async ({ page }, testInfo) => {
  await page.context().addCookies([{
    name: 'mock-vault-session',
    value: crypto.randomUUID(),
    url: testInfo.project.use.baseURL!,
  }])
})

test('secrets reveal uses project scope when credential names collide', async ({ page }) => {
  const revealProjects: Array<string | null> = []
  const failedRequests: string[] = []
  const badResponses: string[] = []
  const pageErrors: string[] = []

  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.pathname === '/api/vault/credentials/shared-token') {
      revealProjects.push(url.searchParams.get('project'))
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

  await page.goto('/secrets')

  const betaRow = page.getByTestId('secret-row-2')
  await expect(betaRow).toBeVisible()
  await expect(betaRow.getByText('beta')).toBeVisible()

  await page.getByTestId('secret-reveal-2').click()

  await expect(betaRow.getByText('beta-secret-value')).toBeVisible()
  expect(revealProjects).toEqual(['beta'])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})

test('a second browser session deleting the same project credential cannot hide this session’s reveal', async ({ page, browser }) => {
  await page.goto('/secrets')
  const betaRow = page.getByTestId('secret-row-2')
  await expect(betaRow).toBeVisible()

  const otherSession = await browser.newContext({ baseURL: test.info().project.use.baseURL })
  try {
    await otherSession.addCookies([{
      name: 'mock-vault-session',
      value: crypto.randomUUID(),
      url: test.info().project.use.baseURL!,
    }])
    const deleted = await otherSession.request.delete('/api/vault/credentials/shared-token?project=beta')
    expect(deleted.status()).toBe(200)
    expect((await otherSession.request.get('/api/vault/credentials/shared-token?project=beta')).status()).toBe(404)

    const revealed = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/vault/credentials/shared-token')
    await page.getByTestId('secret-reveal-2').click()
    expect((await revealed).status()).toBe(200)
    await expect(betaRow.getByText('beta-secret-value')).toBeVisible()
  } finally {
    await otherSession.close()
  }
})

test('secrets delete confirmation is scoped to the selected credential', async ({ page }) => {
  const deleteProjects: Array<string | null> = []
  const failedRequests: string[] = []
  const badResponses: string[] = []
  const pageErrors: string[] = []

  page.on('request', (request) => {
    const url = new URL(request.url())
    if (request.method() === 'DELETE' && url.pathname === '/api/vault/credentials/shared-token') {
      deleteProjects.push(url.searchParams.get('project'))
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

  await page.goto('/secrets')

  const alphaRow = page.getByTestId('secret-row-1')
  const betaRow = page.getByTestId('secret-row-2')
  await expect(alphaRow).toBeVisible()
  await expect(betaRow).toBeVisible()

  await page.getByTestId('secret-delete-1').click()
  await page.getByTestId('secret-delete-2').click()

  await expect(alphaRow).toBeVisible()
  await expect(betaRow).toBeVisible()
  expect(deleteProjects).toEqual([])

  await page.getByTestId('secret-delete-2').click()

  await expect(betaRow).toBeHidden()
  await expect(alphaRow).toBeVisible()
  expect(deleteProjects).toEqual(['beta'])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
