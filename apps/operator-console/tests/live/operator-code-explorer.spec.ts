import { writeFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'
import type { Browser, Page } from '@playwright/test'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { LiveFixtureState, RouteTraffic } from './fixture-bootstrap'

const RESUME_STORAGE_KEY = 'engram.operator-code.resume.v1'

interface OperatorCodeLiveFixture {
  query: string
  expectedSearch: string
  expectedGraph: string
  expectedSource: string
}

interface BrowserApiResponse {
  status: number
  body: unknown
}

function provisionedOperatorCodeFixture(state: LiveFixtureState): OperatorCodeLiveFixture | null {
  const raw = Reflect.get(state, 'operatorCode')
  if (raw === null || typeof raw !== 'object' || Array.isArray(raw)) return null
  const query = Reflect.get(raw, 'query')
  const expectedSearch = Reflect.get(raw, 'expectedSearch')
  const expectedGraph = Reflect.get(raw, 'expectedGraph')
  const expectedSource = Reflect.get(raw, 'expectedSource')
  if (
    typeof query !== 'string' || query.trim() === ''
    || typeof expectedSearch !== 'string' || expectedSearch.trim() === ''
    || typeof expectedGraph !== 'string' || expectedGraph.trim() === ''
    || typeof expectedSource !== 'string' || expectedSource.trim() === ''
  ) return null
  return { query, expectedSearch, expectedGraph, expectedSource }
}

test('S2 live acceptance: explicit pin keeps search, graph, and source inside one released view', async ({ browser }, testInfo) => {
  const fixture = await readLiveFixture()
  const scenario = provisionedOperatorCodeFixture(fixture)
  test.skip(scenario === null, 'requires the server-owned CodeGrantApplication/UCI live fixture provisioner')

  const requestPaths: string[] = []
  const responseTraffic: RouteTraffic[] = []
  const context = await browser.newContext()
  const page = await context.newPage()
  const transitions: Array<{ label: string; opener: boolean; navigationType: string; transition: string | null }> = []

  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/code/')) {
      requestPaths.push(`${request.method()} ${url.pathname}`)
    }
  })
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/code/')) {
      responseTraffic.push({ origin: 'browser', method: response.request().method(), path: url.pathname, status: response.status(), at: new Date().toISOString() })
    }
  })

  try {
    expect(fixture.candidate.commit).toBe(fixture.backend.sourceCommit)
    expect(fixture.mock.prohibited).toBe(true)
    expect(fixture.mock.liveConfigContainsMockCommand).toBe(false)

    await page.goto(`${fixture.frontend.baseUrl}/`, { waitUntil: 'domcontentloaded' })
    const login = await requestJSON(page, '/api/auth/user-login', fixture.browserCredential)
    expect(login.status).toBe(200)

    await page.goto(`${fixture.frontend.baseUrl}/code`, { waitUntil: 'domcontentloaded' })
    await expect(page.getByTestId('code-context-candidate')).toBeVisible()
    await expect(page.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
    await expect(page.getByTestId('code-results-unselected')).toBeVisible()
    await recordTransition(page, transitions, 'fresh')

    await page.getByTestId('code-pin-context').focus()
    await page.keyboard.press('Enter')
    await expect(page.getByTestId('code-context-pinned')).toBeVisible()
    await expect(page.getByTestId('code-status')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Graph evidence' })).toBeVisible()
    await expect(page.getByTestId('code-status')).toContainText('unavailable')

    await page.getByTestId('code-query-input').fill(scenario.query)
    await page.keyboard.press('Enter')
    await expect(page.getByTestId('code-search-results')).toContainText(scenario.expectedSearch)
    await page.getByRole('button', { name: 'Explore graph' }).first().click()
    await expect(page.getByTestId('code-graph-results')).toContainText(scenario.expectedGraph)
    await page.getByRole('button', { name: 'Read source' }).first().click()
    await expect(page.getByTestId('code-source-result')).toContainText(scenario.expectedSource)

    const resumePair = await page.evaluate((key) => sessionStorage.getItem(key), RESUME_STORAGE_KEY)
    expect(resumePair).not.toBeNull()

    await page.reload({ waitUntil: 'domcontentloaded' })
    if (await page.getByRole('button', { name: 'Retry reload binding' }).isVisible().catch(() => false)) {
      await page.getByRole('button', { name: 'Retry reload binding' }).click()
    }
    await expect(page.getByTestId('code-context-pinned')).toBeVisible()
    await recordTransition(page, transitions, 'hard-reload')

    const copied = await context.newPage()
    await copied.addInitScript(({ key, value }) => {
      if (value !== null) sessionStorage.setItem(key, value)
    }, { key: RESUME_STORAGE_KEY, value: resumePair })
    await copied.goto(`${fixture.frontend.baseUrl}/code`, { waitUntil: 'domcontentloaded' })
    await expect(copied.getByTestId('code-bootstrap-evidence')).toContainText('TAB_BINDING_COLLISION')
    await expect(copied.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
    await expect(copied.getByTestId('code-results-unselected')).toBeVisible()
    await expect(copied.locator('body')).not.toContainText(scenario.expectedSource)
    await recordTransition(copied, transitions, 'copied-state-collision')

    const popupPromise = page.waitForEvent('popup')
    await page.evaluate(() => { window.open('/code', '_blank') })
    const child = await popupPromise
    await child.waitForLoadState('domcontentloaded')
    await expect(child.getByTestId('code-bootstrap-evidence')).toContainText('Opener before')
    await expect(child.getByTestId('code-bootstrap-evidence')).toContainText('normalized to null')
    await expect(child.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
    await expect(child.locator('body')).not.toContainText(scenario.expectedSource)
    await recordTransition(child, transitions, 'fresh-opener')
    await child.close()
    await copied.close()

    expect(requestPaths).toEqual(expect.arrayContaining([
      'POST /api/code/tabs/handshake',
      'POST /api/code/contexts',
      'PUT /api/code/tabs/',
      'POST /api/code/status',
      'POST /api/code/search',
      'POST /api/code/graph',
      'POST /api/code/source',
      'POST /api/code/tabs/resume',
    ].map((path) => path === 'PUT /api/code/tabs/' ? expect.stringMatching(/^PUT \/api\/code\/tabs\/[^/]+\/context$/) : path)))
    expect(responseTraffic.filter((entry) => entry.status >= 400 && entry.path !== '/api/code/contexts')).toEqual([])
  } finally {
    const state = await appendBrowserTraffic(responseTraffic)
    const evidence = JSON.stringify({
      evidenceKind: 'real-authenticated-go-postgresql-browser',
      candidate: state.candidate,
      browser: { engine: browser.browserType().name(), version: browser.version() },
      transitions,
      traffic: state.traffic,
      liveFixtureProvisioned: scenario !== null,
    }, null, 2)
    await writeFile(testInfo.outputPath('s2-live-code-explorer.json'), evidence, 'utf8')
    await testInfo.attach('s2-live-code-explorer', { contentType: 'application/json', body: Buffer.from(evidence) })
    await context.close()
  }
})

async function requestJSON(page: Page, path: string, body?: Record<string, string>): Promise<BrowserApiResponse> {
  const response = await page.evaluate(async ({ requestBody, requestPath }) => {
    const result = await fetch(requestPath, requestBody === undefined
      ? undefined
      : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(requestBody) })
    return { status: result.status, text: await result.text() }
  }, { requestBody: body, requestPath: path })
  try {
    return { status: response.status, body: response.text === '' ? null : JSON.parse(response.text) }
  } catch {
    return { status: response.status, body: response.text }
  }
}

async function recordTransition(page: Page, transitions: Array<{ label: string; opener: boolean; navigationType: string; transition: string | null }>, label: string): Promise<void> {
  const signal = await page.evaluate(() => ({
    opener: window.opener !== null,
    navigationType: performance.getEntriesByType('navigation')[0] instanceof PerformanceNavigationTiming
      ? performance.getEntriesByType('navigation')[0].type
      : 'unknown',
  }))
  transitions.push({ label, opener: signal.opener, navigationType: signal.navigationType, transition: await page.getByTestId('code-bootstrap-evidence').locator('dd').last().textContent() })
}

