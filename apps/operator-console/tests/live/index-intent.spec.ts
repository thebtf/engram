import { readFile, writeFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { LiveFixtureState, RouteTraffic } from './fixture-bootstrap'
import { MCPStdioClient } from './mcp-stdio'
import type { MCPStdioTranscript } from './mcp-stdio'

type CatalogEntry = {
  source: { id: string; label: string }
  checkout: { id: string; label: string }
  view: null
  indexIntentAvailable: boolean
  analysisProfileId: string | null
} | null

type PublishedCatalogEntry = {
  sourceId: string
  checkoutId: string
  viewId: string
  profileId: string
} | null

function catalogEntry(value: unknown): CatalogEntry {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const source = Reflect.get(value, 'source')
  const checkout = Reflect.get(value, 'checkout')
  const view = Reflect.get(value, 'view')
  const indexIntentAvailable = Reflect.get(value, 'index_intent_available')
  const profileValue = Reflect.get(value, 'analysis_profile_id')
  if (
    source === null || typeof source !== 'object' || Array.isArray(source)
    || checkout === null || typeof checkout !== 'object' || Array.isArray(checkout)
    || view !== null || typeof indexIntentAvailable !== 'boolean'
  ) return null
  const sourceID = Reflect.get(source, 'id')
  const sourceLabel = Reflect.get(source, 'label')
  const checkoutID = Reflect.get(checkout, 'id')
  const checkoutLabel = Reflect.get(checkout, 'label')
  const analysisProfileId = profileValue === undefined ? null : typeof profileValue === 'string' && profileValue !== '' ? profileValue : null
  if (profileValue !== undefined && analysisProfileId === null || indexIntentAvailable !== (analysisProfileId !== null)) return null
  return typeof sourceID === 'string' && sourceID !== '' && typeof sourceLabel === 'string' && sourceLabel !== '' && typeof checkoutID === 'string' && checkoutID !== '' && typeof checkoutLabel === 'string' && checkoutLabel !== ''
    ? { source: { id: sourceID, label: sourceLabel }, checkout: { id: checkoutID, label: checkoutLabel }, view, indexIntentAvailable, analysisProfileId }
    : null
}

function publishedCatalogEntry(value: unknown): PublishedCatalogEntry {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const source = Reflect.get(value, 'source')
  const checkout = Reflect.get(value, 'checkout')
  const view = Reflect.get(value, 'view')
  if (source === null || typeof source !== 'object' || Array.isArray(source) || checkout === null || typeof checkout !== 'object' || Array.isArray(checkout) || view === null || typeof view !== 'object' || Array.isArray(view)) return null
  const context = Reflect.get(view, 'context_ref')
  if (context === null || typeof context !== 'object' || Array.isArray(context)) return null
  const sourceId = Reflect.get(source, 'id')
  const checkoutId = Reflect.get(checkout, 'id')
  const viewSourceId = Reflect.get(context, 'source_id')
  const viewCheckoutId = Reflect.get(context, 'checkout_id')
  const viewId = Reflect.get(context, 'view_id')
  const profileId = Reflect.get(context, 'analysis_profile_id')
  return typeof sourceId === 'string' && sourceId !== '' && typeof checkoutId === 'string' && checkoutId !== '' && viewSourceId === sourceId && viewCheckoutId === checkoutId && typeof viewId === 'string' && viewId !== '' && typeof profileId === 'string' && profileId !== ''
    ? { sourceId, checkoutId, viewId, profileId }
    : null
}

function parseFirstIndexTarget(value: string | null): { sourceId: string; checkoutId: string } | null {
  if (value === null || value === '') return null
  let request: unknown
  try {
    request = JSON.parse(value)
  } catch {
    return null
  }
  if (request === null || typeof request !== 'object' || Array.isArray(request)) return null
  const target = Reflect.get(request, 'target')
  if (target === null || typeof target !== 'object' || Array.isArray(target)) return null
  const keys = Object.keys(target).sort()
  const sourceId = Reflect.get(target, 'source_id')
  const checkoutId = Reflect.get(target, 'checkout_id')
  return keys.join(',') === 'checkout_id,source_id' && typeof sourceId === 'string' && sourceId !== '' && typeof checkoutId === 'string' && checkoutId !== ''
    ? { sourceId, checkoutId }
    : null
}

function parseIntentRef(value: unknown): string | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const intentRef = Reflect.get(value, 'intent_ref')
  return typeof intentRef === 'string' && intentRef !== '' ? intentRef : null
}

async function login(page: Page, fixture: LiveFixtureState): Promise<void> {
  await page.goto(`${fixture.frontend.baseUrl}/`, { waitUntil: 'domcontentloaded' })
  const status = await page.evaluate(async (credential) => {
    const response = await fetch('/api/auth/user-login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(credential),
    })
    return response.status
  }, fixture.browserCredential)
  expect(status).toBe(200)
}

test('S4 live first-index uses the real C-worktree daemon pump', async ({ browser }, testInfo) => {
  const fixture = await readLiveFixture()
  const context = await browser.newContext()
  const page = await context.newPage()
  const traffic: RouteTraffic[] = []
  const statusHeaderChecks: boolean[] = []
  const statusCodes: number[] = []
  const transcripts: MCPStdioTranscript[] = []
  let catalogRefreshes = 0
  let firstIndexSubmitted = false
  let intentRef: string | null = null
  let resultViewId: string | null = null
  let offlineClient: MCPStdioClient | undefined
  let liveClient: MCPStdioClient | undefined

  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.origin !== fixture.frontend.baseUrl || !url.pathname.startsWith('/api/code/index-intents')) return
    if (request.method() === 'GET') {
      const headers = request.headers()
      statusHeaderChecks.push(
        typeof headers['x-engram-tab-binding-id'] === 'string' && headers['x-engram-tab-binding-id'] !== ''
        && typeof headers['x-engram-document-proof'] === 'string' && headers['x-engram-document-proof'] !== ''
        && request.postData() === null,
      )
    }
  })
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (url.origin !== fixture.frontend.baseUrl) return
    if (response.request().method() === 'POST' && url.pathname === '/api/code/contexts') catalogRefreshes += 1
    if (!url.pathname.startsWith('/api/code/index-intents')) return
    traffic.push({ origin: 'browser', method: response.request().method(), path: url.pathname, status: response.status(), at: new Date().toISOString() })
    if (response.request().method() === 'GET') statusCodes.push(response.status())
  })

  try {
    expect(fixture.mock.prohibited).toBe(true)
    expect(fixture.mock.liveConfigContainsMockCommand).toBe(false)
    expect(fixture.candidate.commit).toBe(fixture.backend.sourceCommit)
    expect(fixture.worktrees.c.head).not.toBe(fixture.worktrees.a.head)
    expect(fixture.worktrees.c.head).not.toBe(fixture.worktrees.b.head)
    expect(fixture.worktrees.c.fixtureSourceSha256).not.toBe(fixture.worktrees.a.fixtureSourceSha256)

    await login(page, fixture)
    const keycard = (await readFile(fixture.mcp.firstIndex.keycardFile, 'utf8')).trim()
    expect(keycard).not.toBe('')

    offlineClient = await MCPStdioClient.start({
      clientRoot: fixture.mcp.firstIndex.clientRoot,
      codeIndex: { parserBundleDigest: fixture.mcp.firstIndex.parserBundleDigest, parserExecutable: fixture.mcp.firstIndex.parserExecutable },
      executable: fixture.mcp.clientBinary,
      serverURL: fixture.backend.baseUrl,
      token: keycard,
    })
    await offlineClient.initializeAndList()
    await offlineClient.close()
    const offline = offlineClient.transcript()
    expect(offline.rootLabel).toBe('C')
    expect(offline.daemonPID).toBeGreaterThan(0)
    expect(offline.externalPID).toBeGreaterThan(0)
    expect(offline.processTreeStopped).toBe(true)
    expect(offline.stateRootRemoved).toBe(true)
    transcripts.push(offline)

    const offlineCatalogResponse = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return response.request().method() === 'POST' && url.pathname === '/api/code/contexts'
    })
    await page.goto(`${fixture.frontend.baseUrl}/code`, { waitUntil: 'domcontentloaded' })
    const offlineCatalog = await offlineCatalogResponse
    expect(offlineCatalog.status()).toBe(200)
    const offlineBody: unknown = await offlineCatalog.json()
    const offlineEntries = offlineBody !== null && typeof offlineBody === 'object' && !Array.isArray(offlineBody) ? Reflect.get(offlineBody, 'contexts') : null
    const offlineNoView = Array.isArray(offlineEntries)
      ? offlineEntries.map(catalogEntry).find((entry) => entry !== null && entry.source.id === fixture.operatorCodeFirstIndex.sourceId && entry.checkout.id === fixture.operatorCodeFirstIndex.checkoutId) ?? null
      : null
    expect(offlineNoView).not.toBeNull()
    expect(offlineNoView?.indexIntentAvailable).toBe(false)
    await expect(page.getByTestId('code-request-first-index')).toHaveCount(0)
    await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'idle')
    expect(traffic).toHaveLength(0)

    liveClient = await MCPStdioClient.start({
      clientRoot: fixture.mcp.firstIndex.clientRoot,
      codeIndex: { parserBundleDigest: fixture.mcp.firstIndex.parserBundleDigest, parserExecutable: fixture.mcp.firstIndex.parserExecutable },
      executable: fixture.mcp.clientBinary,
      serverURL: fixture.backend.baseUrl,
      token: keycard,
    })
    await liveClient.initializeAndList()
    await liveClient.prepareNoViewIndexTarget(fixture.operatorCodeFirstIndex)
    const live = liveClient.transcript()
    expect(live.rootLabel).toBe('C')
    expect(live.daemonPID).toBeGreaterThan(0)
    expect(live.externalPID).toBeGreaterThan(0)
    expect(live.tools).toEqual(expect.arrayContaining(['codebase_context', 'codebase_index', 'codebase_status']))

    const liveCatalogResponse = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return response.request().method() === 'POST' && url.pathname === '/api/code/contexts'
    })
    await page.reload({ waitUntil: 'domcontentloaded' })
    const liveCatalog = await liveCatalogResponse
    expect(liveCatalog.status()).toBe(200)
    const liveBody: unknown = await liveCatalog.json()
    const liveEntries = liveBody !== null && typeof liveBody === 'object' && !Array.isArray(liveBody) ? Reflect.get(liveBody, 'contexts') : null
    const noView = Array.isArray(liveEntries)
      ? liveEntries.map(catalogEntry).find((entry) => entry !== null && entry.source.id === fixture.operatorCodeFirstIndex.sourceId && entry.checkout.id === fixture.operatorCodeFirstIndex.checkoutId) ?? null
      : null
    expect(noView).not.toBeNull()
    expect(noView?.indexIntentAvailable).toBe(true)
    expect(noView?.analysisProfileId).toBe(fixture.operatorCodeFirstIndex.analysisProfileId)

    const firstIndexAction = page.getByTestId('code-request-first-index')
    await expect(firstIndexAction).toHaveCount(1)
    const submitResponse = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return response.request().method() === 'POST' && url.pathname === '/api/code/index-intents'
    })
    await firstIndexAction.click()
    const response = await submitResponse
    expect(response.status()).toBe(202)
    expect(parseFirstIndexTarget(response.request().postData())).toEqual({ sourceId: fixture.operatorCodeFirstIndex.sourceId, checkoutId: fixture.operatorCodeFirstIndex.checkoutId })
    expect(response.request().postData()).not.toContain('analysis_profile_id')
    intentRef = parseIntentRef(await response.json())
    expect(intentRef).not.toBeNull()
    expect(intentRef).not.toBe('')
    await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
    await expect(page.getByTestId('index-intent-check-status')).toBeVisible()
    await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'completed', { timeout: 90_000 })
    expect(statusHeaderChecks.length).toBeGreaterThan(0)
    expect(statusHeaderChecks.every(Boolean)).toBe(true)
    expect(statusCodes.length).toBeGreaterThan(0)
    expect(statusCodes.every((status) => status === 200)).toBe(true)
    firstIndexSubmitted = true

    const refreshedCatalogResponse = page.waitForResponse((candidate) => {
      const url = new URL(candidate.url())
      return candidate.request().method() === 'POST' && url.pathname === '/api/code/contexts'
    })
    await page.reload({ waitUntil: 'domcontentloaded' })
    const refreshedCatalog = await refreshedCatalogResponse
    expect(refreshedCatalog.status()).toBe(200)
    const refreshedBody: unknown = await refreshedCatalog.json()
    const refreshedEntries = refreshedBody !== null && typeof refreshedBody === 'object' && !Array.isArray(refreshedBody) ? Reflect.get(refreshedBody, 'contexts') : null
    const published = Array.isArray(refreshedEntries)
      ? refreshedEntries.map(publishedCatalogEntry).find((entry) => entry !== null && entry.sourceId === fixture.operatorCodeFirstIndex.sourceId && entry.checkoutId === fixture.operatorCodeFirstIndex.checkoutId) ?? null
      : null
    expect(published).not.toBeNull()
    expect(published?.profileId).toBe(fixture.operatorCodeFirstIndex.analysisProfileId)
    resultViewId = published?.viewId ?? null
    expect(resultViewId).not.toBeNull()
    await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
    expect(catalogRefreshes).toBeGreaterThanOrEqual(3)

    const select = page.getByTestId('code-context-select')
    const option = select.locator('option').filter({ hasText: `${fixture.fixtureId}-c` })
    const value = await option.getAttribute('value')
    if (value === null) throw new Error('first-index fixture did not expose its completed View for manual selection')
    await select.selectOption(value)
    await expect(page.getByTestId('code-context-candidate')).toBeVisible()
    await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
    await page.getByTestId('code-pin-context').click()
    await expect(page.getByTestId('code-context-pinned')).toBeVisible()

    await page.getByTestId('code-query-input').fill(fixture.operatorCodeFirstIndex.query)
    await page.getByTestId('code-search-submit').click()
    const result = page.getByTestId('code-search-results').getByRole('listitem').filter({
      has: page.getByText(`go:fixture/func:${fixture.operatorCodeFirstIndex.expectedSource}`, { exact: true }),
    })
    await expect(result).toHaveCount(1)
    await result.getByRole('button', { name: 'Read source' }).click()
    await expect(page.getByTestId('code-source-result')).toContainText(fixture.operatorCodeFirstIndex.expectedMarker)
  } finally {
    await Promise.all([offlineClient?.close(), liveClient?.close()])
    for (const client of [offlineClient, liveClient]) {
      if (client === undefined) continue
      const transcript = client.transcript()
      if (!transcripts.some((candidate) => candidate.externalPID === transcript.externalPID)) transcripts.push(transcript)
    }
    for (const transcript of transcripts) {
      expect(transcript.processTreeStopped).toBe(true)
      expect(transcript.daemonPID).toBeGreaterThan(0)
      expect(transcript.externalPID).toBeGreaterThan(0)
      expect(transcript.stateRootRemoved).toBe(true)
    }
    const state = await appendBrowserTraffic(traffic)
    const evidence = JSON.stringify({
      evidenceKind: 'real-authenticated-go-postgresql-browser-first-index',
      candidate: state.candidate,
      backend: { sourceCommit: state.backend.sourceCommit, binarySha256: state.backend.binarySha256 },
      browser: { engine: browser.browserType().name(), version: browser.version() },
      browserHTTP: 'real registered Go routes; no page-level API mock or SQL lifecycle fabrication',
      indexIntent: { intentRef, resultViewId, firstIndexSubmitted, catalogRefreshes, noAutoPin: true },
      mcp: transcripts,
      offline: { stoppedOwnedDaemon: true, noCompletionBeforeLiveDaemon: true },
      traffic: state.traffic,
    }, null, 2)
    await writeFile(testInfo.outputPath('s4-live-index-intent.json'), evidence, 'utf8')
    await testInfo.attach('s4-live-index-intent', { contentType: 'application/json', body: Buffer.from(evidence) })
    await context.close()
  }
})
