import { expect, test, type Page, type Route } from '@playwright/test'

test.use({ locale: 'ru', screenshot: 'off', trace: 'off' })

const RAW_INVITATION = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'

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

async function routeAccess(page: Page, invitations: Array<Record<string, unknown>>) {
  await page.route('**/api/access/**', async (route: Route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/access/invitations' && request.method() === 'POST') {
      const payload = request.postDataJSON()
      if (typeof payload !== 'object' || payload === null || Array.isArray(payload)) {
        await route.fulfill({ status: 400, json: { error: 'invalid invitation request' } })
        return
      }
      const email = Reflect.get(payload, 'email')
      const role = Reflect.get(payload, 'role')
      invitations.push({
        id: 9,
        email: typeof email === 'string' ? email : '',
        role: typeof role === 'string' ? role : '',
        created_by: 1,
        created_by_email: 'admin@example.test',
        expires_at: '2030-01-01T00:00:00Z',
        revocation_reason: '',
        created_at: '2026-01-01T00:00:00Z',
        status: 'pending',
      })
      await route.fulfill({
        status: 201,
        json: { invitation: { ...invitations[0], code: RAW_INVITATION } },
      })
      return
    }
    if (path === '/api/access/invitations' && request.method() === 'GET') {
      await route.fulfill({ json: { invitations } })
      return
    }
    const suffix = Object.keys(ACCESS_BODIES).find((candidate) => path.startsWith(`/api/access${candidate}`))
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify(suffix ? ACCESS_BODIES[suffix] : { entries: [] }) })
  })
}

test('Access renders one-time keycard and invitation secrets only from validated create receipts', async ({ page }) => {
  const failedRequests: string[] = []
  const badResponses: string[] = []
  const pageErrors: string[] = []
  const consoleMessages: string[] = []
  const revokedIDs: string[] = []
  const createPayloads: Array<Record<string, unknown>> = []
  const invitations: Array<Record<string, unknown>> = []
  type KeycardFixture = {
    id: string
    name: string
    token_prefix: string
    scope: string
    principal: string
    principal_kind: string
    expires_at: string
    created_at: string
    last_used_at: string | null
    request_count: number
    error_count: number
    revoked: boolean
    revoked_at: string | null
  }
  let keycards: KeycardFixture[] = [{
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

  await routeAccess(page, invitations)
  await page.route('**/api/auth/tokens**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())

    if (url.pathname === '/api/auth/tokens' && request.method() === 'GET') {
      await route.fulfill({ json: { tokens: keycards } })
      return
    }

    if (url.pathname === '/api/auth/tokens' && request.method() === 'POST') {
      const payload = request.postDataJSON()
      if (typeof payload !== 'object' || payload === null || Array.isArray(payload)) {
        await route.fulfill({ status: 400, json: { error: 'invalid keycard request' } })
        return
      }
      const createPayload = Object.fromEntries(Object.entries(payload))
      createPayloads.push(createPayload)
      const issuedKeycard: KeycardFixture = {
        id: 'keycard-issued',
        name: String(createPayload.name),
        token_prefix: '01234567',
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
      await route.fulfill({ json: { ...issuedKeycard, token: 'engram_0123456789abcdef0123456789abcdef' } })
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

  const outcome = page.getByTestId('mutation-result')
  await expect(outcome).toHaveAttribute('data-kind', 'committed_verified')
  await expect(page.locator('#access-keycard-name')).toHaveValue('')
  await expect(page.locator('#access-keycard-principal')).toHaveValue('')
  const keycardSecret = page.getByTestId('keycard-one-time-secret')
  await expect(keycardSecret).toHaveValue('engram_0123456789abcdef0123456789abcdef')
  await page.getByTestId('keycard-one-time-dismiss').click()
  await expect(keycardSecret).toHaveCount(0)
  await expect(page.getByTestId('keycard-row-keycard-issued')).toBeVisible()
  await expect(page.locator('body')).not.toContainText('engram_0123456789abcdef0123456789abcdef')

  await page.locator('#access-invitation-email').fill('operator@example.test')
  await page.locator('#access-invitation-role').selectOption('operator')
  await page.locator('.invite-form button[type="submit"]').click()
  const invitationSecret = page.getByTestId('invitation-one-time-secret')
  await expect(invitationSecret).toHaveValue(RAW_INVITATION)
  await page.getByTestId('invitation-one-time-dismiss').click()
  await expect(invitationSecret).toHaveCount(0)
  await expect(page.getByText('operator@example.test').first()).toBeVisible()
  await expect(page.locator('body')).not.toContainText(RAW_INVITATION)

  const createPayload = createPayloads.at(0)
  if (!createPayload) throw new Error('expected keycard create payload')
  expect(createPayload).toMatchObject({
    name: 'browser-workstation',
    scope: 'read-write',
    principal: 'operator/browser',
    principal_kind: 'human',
  })
  expect(String(createPayload.expires_at)).toMatch(/Z$/)

  const browserState = await page.evaluate(() => ({
    url: location.href,
    localStorage: Object.values(localStorage),
    sessionStorage: Object.values(sessionStorage),
  }))
  expect(JSON.stringify(browserState)).not.toContain('engram_0123456789abcdef0123456789abcdef')
  expect(JSON.stringify(browserState)).not.toContain(RAW_INVITATION)
  expect(consoleMessages.join('\n')).not.toContain('engram_0123456789abcdef0123456789abcdef')
  expect(consoleMessages.join('\n')).not.toContain(RAW_INVITATION)

  await page.getByTestId('keycard-revoke-keycard-existing').click()
  await expect(outcome).toHaveAttribute('data-kind', 'committed_verification_pending')
  await page.getByTestId('mutation-recheck').click()
  await expect(page.getByTestId('keycard-row-keycard-existing')).toHaveAttribute('data-status', 'revoked')

  expect(revokedIDs).toEqual(['keycard-existing'])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
