import { writeFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { RouteTraffic } from './fixture-bootstrap'

const CLOSED_SETTINGS_PATHS: Record<string, true> = {
  '/api/config': true,
  '/api/memory-domains': true,
  '/api/models': true,
  '/api/vector/metrics': true,
  '/api/update/status': true,
  '/api/update/check': true,
  '/api/model-health': true,
}

interface BrowserApiResponse {
  status: number
  body: unknown
}

interface ShellRouteTrace {
  route: string
  initialStatus: number | null
  reloadStatus: number | null
  countState: string | null
  countLabel: string
  requestPaths: string[]
}

test('S1a live acceptance: clean Graph, Books, and Rules loads keep the shell honest', async ({ page }, testInfo) => {
  const fixture = await readLiveFixture()
  const requestPaths: string[] = []
  const responseTraffic: RouteTraffic[] = []
  const shellRouteTraces: ShellRouteTrace[] = []

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
    expect(fixture.mock).toEqual({ prohibited: true, liveConfigContainsMockCommand: false })

    const shell = await page.goto(`${fixture.frontend.baseUrl}/`, { waitUntil: 'domcontentloaded' })
    expect(shell?.status()).toBe(200)
    const login = await requestJSON(page, '/api/auth/user-login', fixture.browserCredential)
    expect(login.status).toBe(200)
    const identity = await requestJSON(page, '/api/auth/me')
    expectAuthenticatedIdentity(identity.body)

    for (const route of ['/graph', '/books', '/rules']) {
      const requestOffset = requestPaths.length
      const initial = await page.goto(`${fixture.frontend.baseUrl}${route}`, { waitUntil: 'domcontentloaded' })
      expect(initial?.status()).toBe(200)
      await expect(page.getByRole('dialog')).toHaveCount(0)

      const reload = await page.reload({ waitUntil: 'domcontentloaded' })
      expect(reload?.status()).toBe(200)
      await expect(page.getByRole('dialog')).toHaveCount(0)

      const count = page.getByTestId('shell-memory-count')
      await expect(count).toHaveAttribute('data-count-state', /^(bounded|zero|unknown)$/)
      const countState = await count.getAttribute('data-count-state')
      const countLabel = (await count.textContent())?.trim() || ''
      expectRussianCountTruth(countState, countLabel)
      await expect(page.getByTestId('shell-review-queue-count')).toContainText('неизвестно')

      if (route === '/books') {
        await expect(page.locator('.books-page .statebar')).toHaveAttribute('data-state', 'empty')
        await expect(page.locator('.books-page .books-brief')).toContainText('не Book Context')
      }

      await page.waitForTimeout(100)
      const routeRequests = requestPaths.slice(requestOffset)
      expect(routeRequests.filter((path) => path === 'GET /api/memories')).toEqual([])
      expect(routeRequests.filter((path) => path === 'GET /api/memory/candidates')).toEqual([])
      expect(routeRequests.filter((path) => isClosedSettingsRequest(path))).toEqual([])
      shellRouteTraces.push({
        route,
        initialStatus: initial?.status() ?? null,
        reloadStatus: reload?.status() ?? null,
        countState,
        countLabel,
        requestPaths: routeRequests,
      })
    }

    const settingsTrigger = page.locator('.status-action')
    await settingsTrigger.focus()
    await page.keyboard.press('Enter')
    const dialog = page.getByRole('dialog', { name: 'Настройки сервера' })
    await expect(dialog).toBeVisible()
    await expect(dialog).toHaveAttribute('aria-modal', 'true')
    await expect.poll(() => requestPaths.filter((path) => isClosedSettingsRequest(path)).length).toBeGreaterThan(0)
    await page.keyboard.press('Escape')
    await expect(dialog).toBeHidden()
    await expect(settingsTrigger).toBeFocused()

    const directSettingsOffset = requestPaths.length
    const directSettings = await page.goto(`${fixture.frontend.baseUrl}/settings`, { waitUntil: 'domcontentloaded' })
    expect(directSettings?.status()).toBe(200)
    await expect(dialog).toBeVisible()
    await expect.poll(() => new URL(page.url()).pathname).toBe('/')
    await expect.poll(() => requestPaths.slice(directSettingsOffset).filter(isClosedSettingsRequest).length).toBeGreaterThan(0)
    await page.keyboard.press('Escape')
    await expect(dialog).toBeHidden()
  } finally {
    const state = await appendBrowserTraffic(responseTraffic)
    const evidence = JSON.stringify({
      evidenceKind: 'real-authenticated-go-postgresql-browser',
      candidate: state.candidate,
      backend: {
        sourceCommit: state.backend.sourceCommit,
        binarySha256: state.backend.binarySha256,
      },
      frontend: {
        buildEntry: state.frontend.buildEntry,
        buildEntrySha256: state.frontend.buildEntrySha256,
      },
      mock: state.mock,
      traffic: state.traffic,
      shellRouteTraces,
    }, null, 2)
    await writeFile(testInfo.outputPath('s1a-live-honest-shell.json'), evidence, 'utf8')
    await testInfo.attach('s1a-live-honest-shell', {
      contentType: 'application/json',
      body: Buffer.from(evidence),
    })
  }
})

async function requestJSON(page: Page, path: string, body?: Record<string, string>): Promise<BrowserApiResponse> {
  const response = await page.evaluate(async ({ requestPath, requestBody }) => {
    const request = await fetch(requestPath, {
      method: requestBody ? 'POST' : 'GET',
      headers: requestBody ? { 'Content-Type': 'application/json' } : undefined,
      body: requestBody ? JSON.stringify(requestBody) : undefined,
    })
    return { status: request.status, text: await request.text() }
  }, { requestPath: path, requestBody: body })

  try {
    return { status: response.status, body: response.text === '' ? null : JSON.parse(response.text) }
  } catch {
    return { status: response.status, body: response.text }
  }
}

function expectAuthenticatedIdentity(value: unknown): void {
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

function expectRussianCountTruth(state: string | null, label: string): void {
  if (state === 'bounded') {
    expect(label).toMatch(/[1-9]\d*/)
    return
  }
  if (state === 'zero') {
    expect(label).toBe('нет активных записей')
    return
  }
  if (state === 'unknown') {
    expect(label).toBe('активные записи: неизвестно')
    return
  }
  throw new Error(`unexpected shell count state: ${String(state)}`)
}

function isClosedSettingsRequest(request: string): boolean {
  const [method, path] = request.split(' ', 2)
  return method === 'GET' && CLOSED_SETTINGS_PATHS[path] === true
}
