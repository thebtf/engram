import { spawn } from 'node:child_process'
import { writeFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { LiveFixtureState, RouteTraffic } from './fixture-bootstrap'

const RESUME_STORAGE_KEY = 'engram.operator-code.resume.v1'

interface OperatorCodeLiveFixture {
  query: string
  expectedSearch: string
  expectedGraph: string
  expectedSource: string
  expectedMarker: string
}

interface BrowserApiResponse {
  status: number
  body: unknown
}

function provisionedOperatorCodeFixture(state: LiveFixtureState, key: 'operatorCode' | 'operatorCodeAlternate'): OperatorCodeLiveFixture | null {
  const raw = state[key]
  if (
    typeof raw.query !== 'string' || raw.query.trim() === ''
    || typeof raw.expectedSearch !== 'string' || raw.expectedSearch.trim() === ''
    || typeof raw.expectedGraph !== 'string' || raw.expectedGraph.trim() === ''
    || typeof raw.expectedSource !== 'string' || raw.expectedSource.trim() === ''
    || typeof raw.expectedMarker !== 'string' || raw.expectedMarker.trim() === ''
  ) return null
  return raw
}


async function fixtureSQL(fixture: LiveFixtureState, statement: string): Promise<void> {
  const child = spawn('docker', [
    'exec', fixture.postgres.container,
    'psql', '--username=engram', '--dbname=engram', '--command', statement,
  ], { windowsHide: true })
  child.stdout?.resume()
  child.stderr?.resume()
  const code = await new Promise<number | null>((resolve, reject) => {
    child.once('error', reject)
    child.once('close', resolve)
  })
  if (code !== 0) throw new Error('disposable fixture lifecycle control failed')
}

async function selectFixtureContext(page: Page, fixture: LiveFixtureState, variant: 'a' | 'd'): Promise<void> {
  const select = page.getByTestId('code-context-select')
  const option = select.locator('option').filter({ hasText: `${fixture.fixtureId}-${variant}` })
  await expect(option).toHaveCount(1)
  const value = await option.getAttribute('value')
  if (value === null) throw new Error('fixture catalog did not expose a selectable View')
  await select.selectOption(value)
}

test('S2 live acceptance: explicit catalog preserves View-bound pagination and graph-source limits', async ({ browser }, testInfo) => {
  const fixture = await readLiveFixture()
  const scenario = provisionedOperatorCodeFixture(fixture, 'operatorCode')
  const alternate = provisionedOperatorCodeFixture(fixture, 'operatorCodeAlternate')
  test.skip(scenario === null || alternate === null, 'requires the server-owned CodeGrantApplication/UCI live fixture provisioner')
  if (scenario === null || alternate === null) return

  const requestPaths: string[] = []
  const responseTraffic: RouteTraffic[] = []
  const searchRequests: Array<Record<string, unknown>> = []
  const context = await browser.newContext()
  const page = await context.newPage()
  const transitions: Array<{ label: string; opener: boolean; navigationType: string; transition: string | null }> = []

  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.origin !== fixture.frontend.baseUrl || !url.pathname.startsWith('/api/code/')) return
    requestPaths.push(`${request.method()} ${url.pathname}`)
    if (url.pathname !== '/api/code/search' || request.postData() === null) return
    const body: unknown = JSON.parse(request.postData() || '')
    if (body !== null && typeof body === 'object' && !Array.isArray(body)) searchRequests.push({ ...body })
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
    const contextSelect = page.getByTestId('code-context-select')
    await expect(contextSelect.locator('option')).toHaveCount(3)
    await expect(page.getByTestId('code-context-index-affordance')).toHaveCount(1)
    await expect(page.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
    await expect(page.getByTestId('code-results-unselected')).toBeVisible()
    await recordTransition(page, transitions, 'fresh')

    await selectFixtureContext(page, fixture, 'a')
    await expect(page.getByTestId('code-context-candidate')).toBeVisible()
    await page.getByTestId('code-pin-context').focus()
    await page.keyboard.press('Enter')
    await expect(page.getByTestId('code-context-pinned')).toBeVisible()
    await expect(page.getByTestId('code-status')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Graph evidence' })).toBeVisible()
    await expect(page.getByTestId('code-status')).toContainText('unavailable')

    const pinnedA = await page.getByTestId('code-context-pinned').textContent()
    await page.getByTestId('code-query-input').fill(scenario.query)
    await page.keyboard.press('Enter')
    await expect(page.getByTestId('code-search-results')).toContainText(scenario.expectedSearch)
    await expect(page.getByTestId('code-search-next')).toBeVisible()
    await page.getByTestId('code-search-next').click()
    await expect.poll(() => searchRequests.length).toBe(2)
    expect(searchRequests[1]).toMatchObject({ query: scenario.query, limit: 10, path_prefix: '', languages: [] })
    expect(typeof searchRequests[1].continuation).toBe('string')
    expect(await page.getByTestId('code-context-pinned').textContent()).toBe(pinnedA)

    await page.getByTestId('code-query-input').fill(scenario.query)
    await page.keyboard.press('Enter')
    const functionResult = page.getByTestId('code-search-results').getByRole('listitem').filter({
      has: page.getByText(`go:fixture/func:${scenario.expectedSource}`, { exact: true }),
    })
    await expect(functionResult).toHaveCount(1)
    await functionResult.getByRole('button', { name: 'Explore graph' }).click()
    await expect(page.getByTestId('code-graph-results')).toContainText(scenario.expectedGraph)
    await page.getByRole('button', { name: `Select graph node go:fixture/func:${scenario.expectedGraph}` }).click()
    const inspectSource = page.getByRole('button', { name: 'Inspect exact source' })
    await expect(inspectSource).toBeVisible()
    await inspectSource.click()
    await expect(page.getByTestId('code-source-result')).toContainText(scenario.expectedMarker)

    await fixtureSQL(fixture, `
      UPDATE ci_blobs
      SET storage_state = 'metadata_only', safe_content = NULL
      WHERE source_id IN (
        SELECT source_id FROM sources
        WHERE display_name = 'Operator Code Fixture ${fixture.fixtureId.split("'").join("''")}-d'
      );
    `)
    await selectFixtureContext(page, fixture, 'd')
    await page.getByTestId('code-pin-context').click()
    await expect(page.getByTestId('code-context-pinned')).toContainText(`${fixture.fixtureId}-d`)
    await page.getByTestId('code-query-input').fill(alternate.query)
    await page.keyboard.press('Enter')
    const alternateResult = page.getByTestId('code-search-results').getByRole('listitem').filter({
      has: page.getByText(`go:fixture/func:${alternate.expectedSource}`, { exact: true }),
    })
    await expect(alternateResult).toHaveCount(1)
    await alternateResult.getByRole('button', { name: 'Explore graph' }).click()
    await page.getByRole('button', { name: `Select graph node go:fixture/func:${alternate.expectedGraph}` }).click()
    await expect(page.getByText('No source request was made.', { exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Inspect exact source' })).toHaveCount(0)

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
      backend: { sourceCommit: state.backend.sourceCommit, binarySha256: state.backend.binarySha256 },
      browser: { engine: browser.browserType().name(), version: browser.version() },
      transitions,
      traffic: state.traffic,
      liveFixtureProvisioned: scenario !== null && alternate !== null,
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
  const signal = await page.evaluate(() => {
    const navigation = performance.getEntriesByType('navigation')[0]
    return {
      opener: window.opener !== null,
      navigationType: navigation instanceof PerformanceNavigationTiming ? navigation.type : 'unknown',
    }
  })
  transitions.push({ label, opener: signal.opener, navigationType: signal.navigationType, transition: await page.getByTestId('code-bootstrap-evidence').locator('dd').last().textContent() })
}

