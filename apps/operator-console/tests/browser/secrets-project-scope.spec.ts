import { expect, test } from '@playwright/test'
const alphaProject = '44444444-4444-4444-8444-444444444444'
const betaProject = '55555555-5555-4555-8555-555555555555'

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
  expect(revealProjects).toEqual([betaProject])
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
    const deleted = await otherSession.request.delete(`/api/vault/credentials/shared-token?project=${betaProject}`)
    expect(deleted.status()).toBe(200)
    expect((await otherSession.request.get(`/api/vault/credentials/shared-token?project=${betaProject}`)).status()).toBe(404)

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
  expect(deleteProjects).toEqual([betaProject])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})

test('Vault create choices and same-name credential metadata keep canonical project identity', async ({ page }) => {
  await page.route('**/api/projects', (route) => route.fulfill({
    json: {
      items: [
        { project_id: alphaProject, name: 'Shared project name', repository_qualifier: 'team/alpha' },
        { project_id: betaProject, name: 'Shared project name', repository_qualifier: 'team/beta' },
      ], unresolved: []
    }
  }))
  await page.route('**/api/vault/credentials', (route) => route.fulfill({
    json: [
      { id: 1, name: 'shared-token', project: alphaProject, scope: 'project' },
      { id: 2, name: 'shared-token', project: betaProject, scope: 'project' },
    ]
  }))
  await page.goto(`/secrets?project=${betaProject}`)
  const selector = page.getByTestId('secret-create-project')
  await expect(selector).toHaveValue(betaProject)
  await expect(selector.locator('option')).toHaveCount(3)
  await expect(selector.locator(`option[value="${alphaProject}"]`)).toContainText('team/alpha')
  await expect(selector.locator(`option[value="${betaProject}"]`)).toContainText('team/beta')
  await expect(page.getByTestId('secret-row-1')).toContainText('team/alpha')
  await expect(page.getByTestId('secret-row-2')).toContainText('team/beta')
  await page.reload()
  await expect(selector).toHaveValue(betaProject)
  await expect(page.getByTestId('secret-row-1')).toBeVisible()
  await expect(page.getByTestId('secret-row-2')).toBeVisible()
})

for (const matchingScope of [true, false]) {
  test(`Vault create receipt without a project echo needs exact metadata scope: ${matchingScope}`, async ({ page }) => {
    let created = false
    let requestedProject: unknown
    await page.route('**/api/vault/credentials', async (route) => {
      if (route.request().method() === 'POST') {
        const input = route.request().postDataJSON()
        requestedProject = input.project
        created = true
        await route.fulfill({ json: { operation_state: 'completed', readback: { authoritative: true, kind: 'current', current_state: { id: 99, name: 'fixture-token', scope: 'project' } } } })
      } else {
        await route.fulfill({ json: created ? [{ id: 99, name: 'fixture-token', project: matchingScope ? betaProject : alphaProject, scope: 'project' }] : [] })
      }
    })
    await page.goto(`/secrets?project=${betaProject}`)
    await expect(page.getByTestId('secret-create-project')).toHaveValue(betaProject)
    await page.locator('.create-card input:not([type="password"])').fill('fixture-token')
    await page.locator('.create-card input[type="password"]').fill('synthetic-test-value')
    await page.locator('.create-card button.primary').click()
    await expect(page.getByTestId('mutation-result')).toHaveAttribute('data-kind', matchingScope ? 'committed_verified' : 'committed_verification_pending')
    expect(requestedProject).toBe(betaProject)
  })
}

test('global Vault reveal and delete never borrow the selected project', async ({ page }) => {
  let deleted = false
  const operations: Array<{ method: string; project: string | null; scope: string | null }> = []
  await page.route('**/api/vault/credentials', route => route.fulfill({
    json: [
      { id: 1, name: 'shared-token', project_id: alphaProject, scope: 'project' },
      ...(deleted ? [] : [{ id: 9, name: 'shared-token', project_id: null, scope: 'global' }]),
    ]
  }))
  await page.route('**/api/vault/credentials/shared-token?**', async route => {
    const url = new URL(route.request().url())
    operations.push({ method: route.request().method(), project: url.searchParams.get('project'), scope: url.searchParams.get('scope') })
    if (route.request().method() === 'DELETE') {
      deleted = true
      await route.fulfill({ json: { operation_state: 'completed', readback: { authoritative: true, kind: 'current', current_state: { deleted: true, name: 'shared-token', project_id: null } } } })
    } else {
      await route.fulfill({ json: { id: 9, name: 'shared-token', project_id: null, scope: 'global', value: 'synthetic-global-value' } })
    }
  })
  await page.goto(`/secrets?project=${betaProject}`)
  await expect(page.getByTestId('secret-create-project')).toHaveValue(betaProject)
  const global = page.getByTestId('secret-row-9')
  await expect(global).toContainText('global')
  await page.getByTestId('secret-reveal-9').click()
  await expect(global.getByText('synthetic-global-value')).toBeVisible()
  await page.getByTestId('secret-delete-9').click()
  await page.getByTestId('secret-delete-9').click()
  await expect(global).toHaveCount(0)
  await expect(page.getByTestId('secret-row-1')).toBeVisible()
  await expect(page.getByTestId('mutation-result')).toHaveAttribute('data-kind', 'committed_verified')
  expect(operations).toEqual([
    { method: 'GET', project: null, scope: 'global' },
    { method: 'DELETE', project: null, scope: 'global' },
  ])
})

test('Vault project_id echo cannot verify a different same-name project', async ({ page }) => {
  await page.route('**/api/vault/credentials', async route => {
    if (route.request().method() === 'POST') {
      expect(route.request().postDataJSON().project).toBe(betaProject)
      await route.fulfill({ json: { operation_state: 'completed', readback: { authoritative: true, kind: 'current', current_state: { id: 99, name: 'fixture-token', scope: 'project', project_id: alphaProject } } } })
    } else {
      await route.fulfill({ json: [{ id: 99, name: 'fixture-token', project_id: betaProject, scope: 'project' }] })
    }
  })
  await page.goto(`/secrets?project=${betaProject}`)
  await expect(page.getByTestId('secret-create-project')).toHaveValue(betaProject)
  await page.locator('.create-card input:not([type="password"])').fill('fixture-token')
  await page.locator('.create-card input[type="password"]').fill('synthetic-test-value')
  await page.locator('.create-card button.primary').click()
  await expect(page.getByTestId('mutation-result')).toHaveAttribute('data-kind', 'committed_verification_pending')
  await expect(page.locator('.create-card input[type="password"]')).toHaveValue('')
})
