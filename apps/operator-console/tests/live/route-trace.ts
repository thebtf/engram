import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { RouteTraffic } from './fixture-bootstrap'

interface BrowserApiResponse {
  status: number
  body: unknown
}

test('live harness boots the real authenticated fixture without the mock API', async ({ page }, testInfo) => {
  const fixture = await readLiveFixture()
  const requestPaths: string[] = []
  const responseTraffic: RouteTraffic[] = []

  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/')) {
      requestPaths.push(`${request.method()} ${url.pathname}`)
    }
  })
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/')) {
      responseTraffic.push({
        origin: 'browser',
        method: response.request().method(),
        path: url.pathname,
        status: response.status(),
        at: new Date().toISOString(),
      })
    }
  })

  try {
    expect(fixture.candidate.commit).toBe(fixture.backend.sourceCommit)
    expect(fixture.backend.ready.status).toBe(200)
    expect(fixture.frontend.buildEntry).toBe('.output/server/index.mjs')
    expect(fixture.mock.prohibited).toBe(true)
    expect(fixture.mock.liveConfigContainsMockCommand).toBe(false)

    const shell = await page.goto(`${fixture.frontend.baseUrl}/settings`, { waitUntil: 'domcontentloaded' })
    expect(shell?.status()).toBe(200)

    const login = await requestJSON(page, '/api/auth/user-login', {
      email: fixture.browserCredential.email,
      password: fixture.browserCredential.password,
    })
    expect(login.status).toBe(200)

    const identity = await requestJSON(page, '/api/auth/me')
    expect(identity.status).toBe(200)
    assertAuthenticatedFixture(identity.body)

    const readiness = await requestJSON(page, '/api/ready')
    expect(readiness.status).toBe(200)

    expect(requestPaths).toEqual(expect.arrayContaining([
      'POST /api/auth/user-login',
      'GET /api/auth/me',
      'GET /api/ready',
    ]))
    expect(responseTraffic).toEqual(expect.arrayContaining([
      expect.objectContaining({ method: 'POST', path: '/api/auth/user-login', status: 200 }),
      expect.objectContaining({ method: 'GET', path: '/api/auth/me', status: 200 }),
      expect.objectContaining({ method: 'GET', path: '/api/ready', status: 200 }),
    ]))
  } finally {
    const state = await appendBrowserTraffic(responseTraffic)
    await testInfo.attach('live-harness-route-trace', {
      contentType: 'application/json',
      body: Buffer.from(JSON.stringify({
        candidate: state.candidate,
        backend: {
          sourceCommit: state.backend.sourceCommit,
          binarySha256: state.backend.binarySha256,
        },
        frontend: {
          buildEntry: state.frontend.buildEntry,
          buildEntrySha256: state.frontend.buildEntrySha256,
        },
        traffic: state.traffic,
      }, null, 2)),
    })
  }
})

async function requestJSON(page: Page, path: string, body?: Record<string, string>): Promise<BrowserApiResponse> {
  const response = await page.evaluate(async ({ requestBody, requestPath }) => {
    const init = requestBody === undefined
      ? undefined
      : {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(requestBody),
      }
    const result = await fetch(requestPath, init)
    return { status: result.status, text: await result.text() }
  }, { requestBody: body, requestPath: path })

  try {
    return { status: response.status, body: response.text === '' ? null : JSON.parse(response.text) }
  } catch {
    return { status: response.status, body: response.text }
  }
}

function assertAuthenticatedFixture(value: unknown): void {
  if (
    value === null
    || typeof value !== 'object'
    || Array.isArray(value)
    || !('authenticated' in value)
    || !('auth_disabled' in value)
    || value.authenticated !== true
    || value.auth_disabled !== false
  ) {
    throw new Error(`live fixture returned an unauthenticated identity: ${JSON.stringify(value)}`)
  }
}
