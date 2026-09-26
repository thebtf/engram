import { readFile, writeFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { LiveFixtureState, RouteTraffic } from './fixture-bootstrap'
import { MCPStdioClient } from './mcp-stdio'
import type { MCPStdioTranscript } from './mcp-stdio'

type CatalogEntry = {
  sourceRef: string
  checkoutRef: string
  repository: string
  workingCopy: string
  indexIntentAvailable: boolean
  indexSelectionRef: string | null
}

type PublishedCatalogEntry = {
  sourceRef: string
  checkoutRef: string
  selectionRef: string
}

function catalogEntry(value: unknown): CatalogEntry | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const sourceRef = Reflect.get(value, 'source_ref')
  const checkoutRef = Reflect.get(value, 'checkout_ref')
  const repository = Reflect.get(value, 'repository')
  const workingCopy = Reflect.get(value, 'working_copy')
  const snapshot = Reflect.get(value, 'indexed_snapshot')
  const indexIntentAvailable = Reflect.get(value, 'index_intent_available')
  const indexSelectionValue = Reflect.get(value, 'index_intent_selection_ref')
  const indexSelectionRef = indexSelectionValue === undefined || indexSelectionValue === null ? null : indexSelectionValue
  if (typeof sourceRef !== 'string' || sourceRef === '' || typeof checkoutRef !== 'string' || checkoutRef === '' || typeof repository !== 'string' || repository === '' || typeof workingCopy !== 'string' || snapshot !== undefined && snapshot !== null || typeof indexIntentAvailable !== 'boolean' || indexIntentAvailable !== (typeof indexSelectionRef === 'string' && indexSelectionRef !== '')) return null
  return { sourceRef, checkoutRef, repository, workingCopy, indexIntentAvailable, indexSelectionRef }
}

function publishedCatalogEntry(value: unknown): PublishedCatalogEntry | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const sourceRef = Reflect.get(value, 'source_ref')
  const checkoutRef = Reflect.get(value, 'checkout_ref')
  const selectionRef = Reflect.get(value, 'selection_ref')
  const snapshot = Reflect.get(value, 'indexed_snapshot')
  return typeof sourceRef === 'string' && sourceRef !== '' && typeof checkoutRef === 'string' && checkoutRef !== '' && typeof selectionRef === 'string' && selectionRef !== '' && snapshot !== null && typeof snapshot === 'object' && !Array.isArray(snapshot)
    ? { sourceRef, checkoutRef, selectionRef }
    : null
}

function parseFirstIndexTarget(value: string | null): string | null {
  if (value === null || value === '') return null
  let request: unknown
  try {
    request = JSON.parse(value)
  } catch {
    return null
  }
  if (request === null || typeof request !== 'object' || Array.isArray(request)) return null
  const target = Reflect.get(request, 'target')
  if (target === null || typeof target !== 'object' || Array.isArray(target) || Object.keys(target).join(',') !== 'selection_ref') return null
  const selectionRef = Reflect.get(target, 'selection_ref')
  return typeof selectionRef === 'string' && selectionRef !== '' ? selectionRef : null
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
  const advertisementChecks: Array<{ available: boolean; selectionRef: string | null }> = []
  let catalogRefreshes = 0
  let firstIndexSubmitted = false
  let intentRef: string | null = null
  let resultViewId: string | null = null
  let registrationClient: MCPStdioClient | undefined
  let offlineClient: MCPStdioClient | undefined
  let liveClient: MCPStdioClient | undefined
  let primaryFailure: unknown

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
    const registrationKeycard = (await readFile(fixture.mcp.registration.keycardFile, 'utf8')).trim()
    expect(registrationKeycard).not.toBe('')
    registrationClient = await MCPStdioClient.start({
      clientRoot: fixture.mcp.registration.clientRoot,
      executable: fixture.mcp.clientBinary,
      serverURL: fixture.backend.baseUrl,
      token: registrationKeycard,
    })
    await registrationClient.initializeAndList()
    await registrationClient.registerProjectIdentity()
    await registrationClient.close()
    transcripts.push(registrationClient.transcript())

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
    expect(offline.daemonExecutable).toBe(fixture.mcp.clientBinary)
    expect(offline.daemonExecutableSha256).toBe(fixture.mcp.clientBinarySha256)
    expect(offline.daemonGeneration).not.toBe('')
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
    const offlineNoViews = Array.isArray(offlineEntries)
      ? offlineEntries.map(catalogEntry).filter((entry) => entry !== null)
      : []
    expect(offlineNoViews).toHaveLength(1)
    const offlineNoView = offlineNoViews[0]
    if (offlineNoView === null || offlineNoView === undefined) throw new Error('first-index fixture did not expose the sole no-view checkout')
    expect(offlineNoView.indexIntentAvailable).toBe(false)
    expect(offlineNoView.indexSelectionRef).toBeNull()
    await page.getByTestId('code-context-repository').selectOption(offlineNoView.sourceRef)
    await page.getByTestId('code-context-working-copy').selectOption(offlineNoView.checkoutRef)
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
    expect(live.daemonExecutable).toBe(fixture.mcp.clientBinary)
    expect(live.daemonExecutableSha256).toBe(fixture.mcp.clientBinarySha256)
    expect(live.daemonGeneration).not.toBe('')
    expect(live.daemonPID).toBeGreaterThan(0)
    expect(live.externalPID).toBeGreaterThan(0)
    expect(live.tools).toEqual(expect.arrayContaining(['codebase_context', 'codebase_index', 'codebase_status']))

    let noView: CatalogEntry | null = null
    let liveCatalogStatus = 0
    const advertisementDeadline = Date.now() + 15_000
    do {
      const liveCatalogResponse = page.waitForResponse((response) => {
        const url = new URL(response.url())
        return response.request().method() === 'POST' && url.pathname === '/api/code/contexts'
      })
      await page.reload({ waitUntil: 'domcontentloaded' })
      const liveCatalog = await liveCatalogResponse
      liveCatalogStatus = liveCatalog.status()
      const liveBody: unknown = await liveCatalog.json()
      const liveEntries = liveBody !== null && typeof liveBody === 'object' && !Array.isArray(liveBody) ? Reflect.get(liveBody, 'contexts') : null
      noView = Array.isArray(liveEntries)
        ? liveEntries.map(catalogEntry).find((entry) => entry !== null && entry.sourceRef === offlineNoView?.sourceRef && entry.checkoutRef === offlineNoView?.checkoutRef) ?? null
        : null
      advertisementChecks.push({ available: noView?.indexIntentAvailable ?? false, selectionRef: noView?.indexSelectionRef ?? null })
      if (liveCatalogStatus === 200 && noView?.indexIntentAvailable === true) break
      await page.waitForTimeout(250)
    } while (Date.now() < advertisementDeadline)
    expect(liveCatalogStatus).toBe(200)
    if (noView === null) throw new Error('live first-index catalog did not expose the registered checkout')
    expect(noView.indexIntentAvailable).toBe(true)
    expect(noView.indexSelectionRef).not.toBeNull()

    await page.getByTestId('code-context-repository').selectOption(offlineNoView.sourceRef)
    await page.getByTestId('code-context-working-copy').selectOption(offlineNoView.checkoutRef)
    const firstIndexAction = page.getByTestId('code-request-first-index')
    await expect(firstIndexAction).toHaveCount(1)
    const submitResponse = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return response.request().method() === 'POST' && url.pathname === '/api/code/index-intents'
    })
    await firstIndexAction.click()
    const response = await submitResponse
    expect(response.status()).toBe(202)
    expect(parseFirstIndexTarget(response.request().postData())).toBe(noView?.indexSelectionRef)
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
      ? refreshedEntries.map(publishedCatalogEntry).find((entry) => entry !== null && entry.sourceRef === noView?.sourceRef && entry.checkoutRef === noView?.checkoutRef) ?? null
      : null
    if (published === null) throw new Error('completed first-index catalog did not expose a View for the registered checkout')
    resultViewId = published.selectionRef
    expect(resultViewId).not.toBeNull()
    await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
    expect(catalogRefreshes).toBeGreaterThanOrEqual(3)

    await page.getByTestId('code-context-repository').selectOption(published.sourceRef)
    await page.getByTestId('code-context-working-copy').selectOption(published.checkoutRef)
    await page.getByTestId('code-context-snapshot').selectOption(published.selectionRef)
    await expect(page.getByTestId('code-context-candidate')).toBeVisible()
    await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
    await page.getByTestId('code-pin-context').click()
    await expect(page.getByTestId('code-context-pinned')).toBeVisible()
    await expect.poll(async () => {
      await page.locator('main.code-page > header button').click()
      const summary = await page.getByTestId('code-status').textContent()
      const counts = summary?.match(/(\d+)\s*\/\s*(\d+)/)
      return counts !== null && counts !== undefined && Number(counts[2]) > 50 && counts[1] === counts[2]
    }, { timeout: 90_000 }).toBe(true)

    await page.getByTestId('code-query-input').fill(fixture.operatorCodeFirstIndex.query)
    const searchResponse = page.waitForResponse((candidate) => candidate.request().method() === 'POST' && new URL(candidate.url()).pathname === '/api/code/search')
    await page.getByTestId('code-search-submit').click()
    expect((await searchResponse).status()).toBe(200)
    const result = page.getByTestId('code-search-results').getByRole('listitem').filter({
      has: page.getByText(`go:fixture/func:${fixture.operatorCodeFirstIndex.expectedSource}`, { exact: true }),
    })
    await expect(result).toHaveCount(1)
    await result.getByTestId('code-search-source').click()
    await expect(page.getByTestId('code-source-result')).toContainText(fixture.operatorCodeFirstIndex.expectedMarker)
  } catch (error) {
    primaryFailure = error
    throw error
  } finally {
    const cleanup = await Promise.allSettled([registrationClient?.close(), offlineClient?.close(), liveClient?.close()])
    for (const client of [registrationClient, offlineClient, liveClient]) {
      if (client === undefined) continue
      const transcript = client.transcript()
      if (!transcripts.some((candidate) => candidate.externalPID === transcript.externalPID)) transcripts.push(transcript)
    }
    if (primaryFailure === undefined) {
      for (const result of cleanup) if (result.status === 'rejected') throw result.reason
      for (const transcript of transcripts) {
        expect(transcript.daemonExecutable).toBe(fixture.mcp.clientBinary)
        expect(transcript.daemonExecutableSha256).toBe(fixture.mcp.clientBinarySha256)
        expect(transcript.daemonGeneration).not.toBe('')
        expect(transcript.processTreeStopped).toBe(true)
        expect(transcript.daemonPID).toBeGreaterThan(0)
        expect(transcript.externalPID).toBeGreaterThan(0)
        expect(transcript.stateRootRemoved).toBe(true)
      }
    }
    const state = await appendBrowserTraffic(traffic)
    const evidence = JSON.stringify({
      evidenceKind: 'real-authenticated-go-postgresql-browser-first-index',
      candidate: state.candidate,
      backend: { sourceCommit: state.backend.sourceCommit, binarySha256: state.backend.binarySha256 },
      browser: { engine: browser.browserType().name(), version: browser.version() },
      browserHTTP: 'real registered Go routes; no page-level API mock or SQL lifecycle fabrication',
      indexIntent: { intentRef, resultViewId, firstIndexSubmitted, catalogRefreshes, noAutoPin: true, advertisementChecks },
      mcp: transcripts,
      offline: { stoppedOwnedDaemon: true, noCompletionBeforeLiveDaemon: true },
      traffic: state.traffic,
    }, null, 2)
    await writeFile(testInfo.outputPath('s4-live-index-intent.json'), evidence, 'utf8')
    await testInfo.attach('s4-live-index-intent', { contentType: 'application/json', body: Buffer.from(evidence) })
    await context.close()
  }
})
