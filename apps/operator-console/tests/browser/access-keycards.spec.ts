import { expect, test, type Page, type Route } from '@playwright/test'

test.use({ locale: 'ru', screenshot: 'off', trace: 'off' })

const RAW_KEYCARD = 'engram_browser_contract_one_time_keycard'

const ACCESS_BODIES: Record<string, unknown> = {
  '/providers': {
    providers: [{ id: 'local', label: 'Local login', kind: 'password', enabled: true, configured: true, operable: true, honesty: 'live', evidence: '/api/access/providers', description: 'browser fixture' }],
    auth_disabled: false,
    local_login_enabled: true,
    authentik_trusted_proxy_count: 1,
  },
  '/invitations': { invitations: [] },
  '/users': { users: [] },
  '/roles': { roles: [{ role: 'admin', user_count: 1 }] },
  '/sessions': { sessions: [] },
  '/log': { entries: [] },
}

async function routeAccess(page: Page) {
  await page.route('**/api/access/**', (route: Route) => {
    const path = new URL(route.request().url()).pathname
    const suffix = Object.keys(ACCESS_BODIES).find((candidate) => path.startsWith(`/api/access${candidate}`))
    return route.fulfill({ contentType: 'application/json', body: JSON.stringify(suffix ? ACCESS_BODIES[suffix] : { entries: [] }) })
  })
}

test('Access issues a one-time workstation keycard and revokes an existing keycard', async ({ page }) => {
  const failedRequests: string[] = []
  const badResponses: string[] = []
  const pageErrors: string[] = []
  const consoleMessages: string[] = []
  const revokedIDs: string[] = []
  let createPayload: Record<string, unknown> | null = null
  let keycards = [{
    id: 'keycard-existing',
    name: 'existing-workstation',
    token_prefix: 'existing1',
    scope: 'read-write',
    principal: 'operator/existing',
    principal_kind: 'human',
    expires_at: '2030-01-01T00:00:00Z',
    created_at: '2026-01-01T00:00:00Z',
    last_used_at: null,
    request_count: 0,
    error_count: 0,
    revoked: false,
    revoked_at: null,
  }]

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
  page.on('console', (message) => {
    consoleMessages.push(message.text())
  })

  await routeAccess(page)
  await page.route('**/api/auth/tokens**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())

    if (url.pathname === '/api/auth/tokens' && request.method() === 'GET') {
      await route.fulfill({ json: { tokens: keycards } })
      return
    }

    if (url.pathname === '/api/auth/tokens' && request.method() === 'POST') {
      createPayload = request.postDataJSON()
      const issuedKeycard = {
        id: 'keycard-issued',
        name: String(createPayload.name),
        token_prefix: 'issued12',
        scope: String(createPayload.scope),
        principal: String(createPayload.principal),
        principal_kind: String(createPayload.principal_kind),
        expires_at: String(createPayload.expires_at),
        created_at: '2026-01-01T00:00:00Z',
        last_used_at: null,
        request_count: 0,
        error_count: 0,
        revoked: false,
        revoked_at: null,
      }
      keycards = [...keycards, issuedKeycard]
      await route.fulfill({ json: { ...issuedKeycard, token: RAW_KEYCARD } })
      return
    }

    const revokeMatch = url.pathname.match(/^\/api\/auth\/tokens\/([^/]+)$/)
    if (request.method() === 'DELETE' && revokeMatch) {
      const id = revokeMatch[1]
      revokedIDs.push(id)
      keycards = keycards.map((keycard) => keycard.id === id
        ? { ...keycard, revoked: true, revoked_at: '2026-01-02T00:00:00Z' }
        : keycard)
      await route.fulfill({ json: { revoked: true } })
      return
    }

    await route.fulfill({ status: 405, json: { error: 'unexpected keycard request' } })
  })

  await page.goto('/access')
  await expect(page.getByTestId('keycard-row-keycard-existing')).toBeVisible()

  await page.locator('#access-keycard-name').fill('browser-workstation')
  await page.locator('#access-keycard-principal').fill('operator/browser')
  await page.locator('#access-keycard-expires-at').fill('2030-01-02T03:04')
  await page.getByTestId('keycard-issue').click()

  const reveal = page.getByTestId('keycard-reveal')
  await expect(reveal).toBeVisible()
  await expect(reveal.locator('.rv-key')).toHaveText(RAW_KEYCARD)
  await expect(page.getByTestId('keycard-row-keycard-issued')).not.toContainText(RAW_KEYCARD)
  expect(createPayload).toMatchObject({
    name: 'browser-workstation',
    scope: 'read-write',
    principal: 'operator/browser',
    principal_kind: 'human',
  })
  expect(String(createPayload?.expires_at)).toMatch(/Z$/)

  const browserState = await page.evaluate(() => ({
    url: location.href,
    localStorage: Object.values(localStorage),
    sessionStorage: Object.values(sessionStorage),
  }))
  expect(JSON.stringify(browserState)).not.toContain(RAW_KEYCARD)
  expect(consoleMessages.join('\n')).not.toContain(RAW_KEYCARD)

  await page.getByTestId('keycard-dismiss').click()
  await expect(reveal).toHaveCount(0)
  await page.reload()
  await expect(page.getByTestId('keycard-reveal')).toHaveCount(0)
  await expect(page.getByTestId('keycard-row-keycard-issued')).not.toContainText(RAW_KEYCARD)

  await page.getByTestId('keycard-revoke-keycard-existing').click()
  await expect(page.getByTestId('keycard-row-keycard-existing')).toHaveAttribute('data-status', 'revoked')
  expect(revokedIDs).toEqual(['keycard-existing'])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
