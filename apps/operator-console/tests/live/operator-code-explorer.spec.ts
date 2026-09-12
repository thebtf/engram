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

function provisionedOperatorCodeFixture(state: LiveFixtureState, key: 'operatorCode' | 'operatorCodeAlternate'): OperatorCodeLiveFixture {
  const raw = state[key]
  if (
    typeof raw.query !== 'string' || raw.query.trim() === ''
    || typeof raw.expectedSearch !== 'string' || raw.expectedSearch.trim() === ''
    || typeof raw.expectedGraph !== 'string' || raw.expectedGraph.trim() === ''
    || typeof raw.expectedSource !== 'string' || raw.expectedSource.trim() === ''
    || typeof raw.expectedMarker !== 'string' || raw.expectedMarker.trim() === ''
  ) throw new Error(`live fixture ${key} is incomplete`)
  return raw
}

function record(value: unknown, label: string): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${label} was not an object`)
  return value
}

function oneContext(value: unknown): Record<string, unknown> {
  const contexts = record(value, 'Code response').contexts
  if (!Array.isArray(contexts) || contexts.length !== 1) throw new Error('Code response did not contain exactly one ContextRef')
  return record(contexts[0], 'Code response ContextRef')
}

function scenarioEntityKeys(value: unknown, expectedA: string, expectedB: string): string[] {
  const items = record(value, 'Search response').items
  if (!Array.isArray(items)) throw new Error('Search response did not contain items')
  return items.map((item) => requiredText(record(record(item, 'Search item').ref, 'Search item ref').entity_key, 'Search item entity key'))
    .filter((entityKey) => entityKey === expectedA || entityKey === expectedB)
}

function graphSourceDescriptor(value: unknown, entityKey: string): Record<string, unknown> {
  const navigation = record(record(value, 'Graph response').navigation, 'Graph navigation')
  if (!Array.isArray(navigation.nodes)) throw new Error('Graph navigation did not contain nodes')
  for (const rawNode of navigation.nodes) {
    const node = record(rawNode, 'Graph navigation node')
    const entity = record(node.entity, 'Graph navigation entity')
    if (entity.entity_key !== entityKey) continue
    if (node.source_state !== 'available') throw new Error(`Graph node ${entityKey} did not publish a source descriptor`)
    return record(node.source_read, `Graph node ${entityKey} source descriptor`)
  }
  throw new Error(`Graph navigation omitted ${entityKey}`)
}

function requiredText(value: unknown, label: string): string {
  if (typeof value !== 'string' || value === '') throw new Error(`${label} was not text`)
  return value
}

function requiredInteger(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value)) throw new Error(`${label} was not an integer`)
  return value
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

  const requestPaths: string[] = []
  const responseTraffic: RouteTraffic[] = []
  const searchRequests: Array<Record<string, unknown>> = []
  const graphRequests: Array<Record<string, unknown>> = []
  const sourceRequests: Array<Record<string, unknown>> = []
  const context = await browser.newContext()
  const page = await context.newPage()
  const transitions: Array<{ label: string; opener: boolean; navigationType: string; transition: string | null }> = []

  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.origin !== fixture.frontend.baseUrl || !url.pathname.startsWith('/api/code/')) return
    requestPaths.push(`${request.method()} ${url.pathname}`)
    if ((url.pathname !== '/api/code/search' && url.pathname !== '/api/code/graph' && url.pathname !== '/api/code/source') || request.postData() === null) return
    const body: unknown = JSON.parse(request.postData() || '')
    if (body === null || typeof body !== 'object' || Array.isArray(body)) return
    if (url.pathname === '/api/code/search') searchRequests.push({ ...body })
    if (url.pathname === '/api/code/graph') graphRequests.push({ ...body })
    if (url.pathname === '/api/code/source') sourceRequests.push({ ...body })
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
    await expect(page.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
    await expect(page.getByTestId('code-results-unselected')).toBeVisible()
    await recordTransition(page, transitions, 'fresh')

    await selectFixtureContext(page, fixture, 'a')
    await expect(page.getByTestId('code-context-candidate')).toBeVisible()
    await page.getByTestId('code-pin-context').focus()
    await page.keyboard.press('Enter')
    await expect(page.getByTestId('code-context-pinned')).toBeVisible()
    await expect(page.getByTestId('code-status')).toBeVisible()
    await expect(page.getByTestId('code-graph-heading')).toBeVisible()
    await expect(page.getByTestId('code-status')).toContainText('unavailable')

    const pinnedA = await page.getByTestId('code-context-pinned').textContent()
    const initialSearchResponse = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/code/search' && response.request().method() === 'POST')
    await page.getByTestId('code-query-input').fill(scenario.query)
    await page.keyboard.press('Enter')
    const initialSearch = await (await initialSearchResponse).json()
    const initialSearchContext = oneContext(initialSearch)
    const searchEntityKey = `go:fixture/func:${scenario.expectedSource}`
    const graphEntityKey = `go:fixture/func:${scenario.expectedGraph}`
    expect(scenarioEntityKeys(initialSearch, searchEntityKey, graphEntityKey)).toEqual([searchEntityKey])
    await expect(page.getByTestId('code-search-results')).toContainText(scenario.expectedSearch)
    await expect(page.getByTestId('code-search-next')).toBeVisible()
    const continuedSearchResponse = page.waitForResponse((response) => {
      if (new URL(response.url()).pathname !== '/api/code/search' || response.request().method() !== 'POST') return false
      const body = response.request().postData()
      return body !== null && Object.hasOwn(record(JSON.parse(body), 'Search request'), 'continuation')
    })
    await page.getByTestId('code-search-next').click()
    const continuedSearch = await (await continuedSearchResponse).json()
    expect(oneContext(continuedSearch)).toEqual(initialSearchContext)
    await expect.poll(() => searchRequests.length).toBe(2)
    expect(searchRequests[1]).toMatchObject({ query: scenario.query, limit: 10, path_prefix: '', languages: [] })
    expect(typeof searchRequests[1].continuation).toBe('string')
    expect(await page.getByTestId('code-context-pinned').textContent()).toBe(pinnedA)

    const resetSearchResponse = page.waitForResponse((response) => {
      if (new URL(response.url()).pathname !== '/api/code/search' || response.request().method() !== 'POST') return false
      const body = response.request().postData()
      return body !== null && !Object.hasOwn(record(JSON.parse(body), 'Search request'), 'continuation')
    })
    await page.getByTestId('code-query-input').fill(scenario.query)
    await page.keyboard.press('Enter')
    const resetSearch = await (await resetSearchResponse).json()
    await expect.poll(() => searchRequests.length).toBe(3)
    expect(searchRequests[2]).toMatchObject({ query: scenario.query, limit: 10, path_prefix: '', languages: [] })
    expect(searchRequests[2]).not.toHaveProperty('continuation')
    expect(oneContext(resetSearch)).toEqual(initialSearchContext)
    expect(scenarioEntityKeys(resetSearch, searchEntityKey, graphEntityKey)).toEqual([searchEntityKey])
    const functionResult = page.getByTestId('code-search-results').getByRole('listitem').filter({
      has: page.getByText(searchEntityKey, { exact: true }),
    })
    const offPageResult = page.getByTestId('code-search-results').getByRole('listitem').filter({
      has: page.getByText(graphEntityKey, { exact: true }),
    })
    await expect(functionResult).toHaveCount(1)
    await expect(offPageResult).toHaveCount(0)
    const firstGraphResponse = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/code/graph' && response.request().method() === 'POST')
    await functionResult.getByTestId('code-search-explore').click()
    const firstGraph = await (await firstGraphResponse).json()
    const revealedDescriptor = graphSourceDescriptor(firstGraph, graphEntityKey)
    expect(oneContext(firstGraph)).toEqual(initialSearchContext)
    await expect(page.getByTestId('code-graph-results')).toContainText(scenario.expectedGraph)
    await page.getByTestId('code-graph-node').filter({ hasText: graphEntityKey }).click()
    const continuedGraphResponse = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/code/graph' && response.request().method() === 'POST')
    await page.getByTestId('code-graph-continue').click()
    const continuedGraph = await (await continuedGraphResponse).json()
    expect(oneContext(continuedGraph)).toEqual(initialSearchContext)
    await expect.poll(() => graphRequests.length).toBe(2)
    expect(graphRequests[1]).toMatchObject({
      action: graphRequests[0]?.action,
      target: { entity_key: graphEntityKey },
      direction: graphRequests[0]?.direction,
      relations: graphRequests[0]?.relations,
      max_depth: graphRequests[0]?.max_depth,
      max_nodes: graphRequests[0]?.max_nodes,
      max_edges: graphRequests[0]?.max_edges,
    })
    expect(graphRequests[1]).not.toHaveProperty('continuation')
    const descriptor = graphSourceDescriptor(continuedGraph, graphEntityKey)
    expect(descriptor).toEqual(revealedDescriptor)
    const descriptorSpan = record(descriptor.span, 'Graph source span')
    const descriptorDigest = requiredText(descriptor.content_digest, 'Graph source digest')
    const lineStart = requiredInteger(descriptorSpan.line_start, 'Graph source start line')
    const lineEnd = requiredInteger(descriptorSpan.line_end, 'Graph source end line')
    const byteStart = requiredInteger(descriptorSpan.byte_start, 'Graph source start byte')
    const byteEnd = requiredInteger(descriptorSpan.byte_end, 'Graph source end byte')
    const sourceResponse = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/code/source' && response.request().method() === 'POST')
    const inspectSource = page.getByTestId('code-graph-source')
    await expect(inspectSource).toBeVisible()
    await inspectSource.click()
    const source = await (await sourceResponse).json()
    expect(oneContext(source)).toEqual(initialSearchContext)
    await expect.poll(() => sourceRequests.length).toBe(1)
    expect(sourceRequests[0]).toMatchObject(descriptor)
    await expect(page.getByTestId('code-source-result')).toContainText(`func ${scenario.expectedGraph}`)
    const sourceMeta = page.locator('.source-meta').first()
    await expect(sourceMeta).toContainText(`${lineStart}–${lineEnd}`)
    await expect(sourceMeta).toContainText(`${byteStart}–${byteEnd}`)
    await expect(sourceMeta).toContainText(descriptorDigest)

    await selectFixtureContext(page, fixture, 'd')
    await expect(page.getByTestId('code-context-candidate')).toContainText(`${fixture.fixtureId}-d`)
    await expect(page.getByTestId('code-context-pinned')).toContainText(`${fixture.fixtureId}-a`)
    await page.reload({ waitUntil: 'domcontentloaded' })
    if (await page.getByTestId('code-retry-reload').isVisible().catch(() => false)) {
      await page.getByTestId('code-retry-reload').click()
    }
    await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
    await expect(page.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
    await expect(page.getByTestId('code-results-unselected')).toBeVisible()
    await recordTransition(page, transitions, 'unproven-pin-reload')

    await selectFixtureContext(page, fixture, 'd')
    await page.getByTestId('code-pin-context').click()
    await expect(page.getByTestId('code-context-pinned')).toContainText(`${fixture.fixtureId}-d`)
    await page.getByTestId('code-query-input').fill(alternate.query)
    await page.keyboard.press('Enter')
    const alternateResult = page.getByTestId('code-search-results').getByRole('listitem').filter({
      has: page.getByText(`go:fixture/func:${alternate.expectedSource}`, { exact: true }),
    })
    await expect(alternateResult).toHaveCount(1)
    await alternateResult.getByTestId('code-search-source').click()
    await expect(page.getByTestId('code-source-result')).toContainText(alternate.expectedMarker)


    const resumePair = await page.evaluate((key) => sessionStorage.getItem(key), RESUME_STORAGE_KEY)
    expect(resumePair).not.toBeNull()

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
    await expect(child.getByTestId('code-bootstrap-evidence')).toContainText('Opener до')
    await expect(child.getByTestId('code-bootstrap-evidence')).toContainText('нормализован в null')
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
      liveFixtureProvisioned: true,
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

