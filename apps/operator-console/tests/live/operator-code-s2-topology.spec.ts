import { readFile, writeFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'
import type { Browser, BrowserContext, Locator, Page } from '@playwright/test'
import { browserUserID, intervalsOverlap, issueReadOnlyKeycard, observeOperation } from './agent-topology'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { LiveFixtureState, RouteTraffic } from './fixture-bootstrap'
import { MCPStdioClient } from './mcp-stdio'

const RESUME_STORAGE_KEY = 'engram.operator-code.resume.v1'

interface BrowserCredential {
  email: string
  password: string
}

interface OperatorCodeFixture {
  query: string
  expectedSearch: string
  expectedGraph: string
  expectedSource: string
  expectedMarker: string
}

interface CodeProof {
  tabBindingId: string
  documentProof: string
}

interface CodeTab {
  context: BrowserContext
  page: Page
  proof: CodeProof
  searchPayload: Record<string, unknown>
}

function operatorCodeFixture(value: LiveFixtureState, key: 'operatorCode' | 'operatorCodeB'): OperatorCodeFixture {
  const fixture = value[key]
  if (
    typeof fixture.query !== 'string' || fixture.query === ''
    || typeof fixture.expectedSearch !== 'string' || fixture.expectedSearch === ''
    || typeof fixture.expectedGraph !== 'string' || fixture.expectedGraph === ''
    || typeof fixture.expectedSource !== 'string' || fixture.expectedSource === ''
    || typeof fixture.expectedMarker !== 'string' || fixture.expectedMarker === ''
  ) {
    throw new Error(`live fixture ${key} is incomplete`)
  }
  return fixture
}

function proofFromRequestPayload(raw: string | null): CodeProof | null {
  if (raw === null || raw === '') return null
  let value: unknown
  try {
    value = JSON.parse(raw)
  } catch {
    return null
  }
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const tabBindingId = Reflect.get(value, 'tab_binding_id')
  const documentProof = Reflect.get(value, 'document_proof')
  if (typeof tabBindingId !== 'string' || tabBindingId === '' || typeof documentProof !== 'string' || documentProof === '') return null
  return { tabBindingId, documentProof }
}

async function requestJSON(page: Page, path: string, body: BrowserCredential): Promise<{ status: number; body: unknown }> {
  const response = await page.evaluate(async ({ requestBody, requestPath }) => {
    const result = await fetch(requestPath, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(requestBody),
    })
    return { status: result.status, text: await result.text() }
  }, { requestBody: body, requestPath: path })
  try {
    return { status: response.status, body: response.text === '' ? null : JSON.parse(response.text) }
  } catch {
    return { status: response.status, body: response.text }
  }
}

async function selectFixtureContext(page: Page, fixture: LiveFixtureState, variant: 'a' | 'b'): Promise<void> {
  const fixtureLabel = `${fixture.fixtureId}-${variant}`
  const choose = async (select: Locator) => {
    const option = select.locator('option').filter({ hasText: fixtureLabel })
    await expect(option).toHaveCount(1)
    const value = await option.getAttribute('value')
    if (value === null) throw new Error('fixture catalog did not expose a readable selection')
    await select.selectOption(value)
  }
  await choose(page.getByTestId('code-context-repository'))
  await choose(page.getByTestId('code-context-working-copy'))
  const snapshot = page.getByTestId('code-context-snapshot')
  await expect(snapshot).toBeEnabled()
  const options = snapshot.locator('option:not([disabled])')
  await expect(options).toHaveCount(1)
  const value = await options.getAttribute('value')
  if (value === null) throw new Error('fixture catalog did not expose an indexed snapshot')
  await snapshot.selectOption(value)
  await expect(snapshot).toHaveValue(value)
  await expect(page.getByTestId('code-context-candidate')).toContainText(fixtureLabel)
}

async function pinAndRead(browser: Browser, fixture: LiveFixtureState, credential: BrowserCredential, scenario: OperatorCodeFixture, traffic: RouteTraffic[]): Promise<CodeTab> {
  const context = await browser.newContext()
  const page = await context.newPage()
  let proof: CodeProof | null = null
  let searchPayload: Record<string, unknown> | null = null
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.origin !== fixture.frontend.baseUrl || !url.pathname.startsWith('/api/code/')) return
    const candidate = proofFromRequestPayload(request.postData())
    if (candidate !== null) proof = candidate
    if (url.pathname === '/api/code/search' && request.postData() !== null) {
      const body: unknown = JSON.parse(request.postData() || '')
      if (body !== null && typeof body === 'object' && !Array.isArray(body)) searchPayload = { ...body }
    }
  })
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/code/')) {
      traffic.push({ origin: 'browser', method: response.request().method(), path: url.pathname, status: response.status(), at: new Date().toISOString() })
    }
  })

  const shell = await page.goto(`${fixture.frontend.baseUrl}/`, { waitUntil: 'domcontentloaded' })
  expect(shell?.status()).toBe(200)
  expect((await requestJSON(page, '/api/auth/user-login', credential)).status).toBe(200)
  await page.getByTestId('overview-workspace-entry').click()
  await expect(page).toHaveURL(/\/code$/)
  const variant = credential.email === fixture.browserCredential.email ? 'a' : 'b'
  await selectFixtureContext(page, fixture, variant)
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-context-pinned')).toBeVisible()
  await page.getByTestId('code-query-input').fill(scenario.expectedSearch)
  await page.getByTestId('code-search-submit').click()
  const results = page.getByTestId('code-search-results').getByRole('listitem').filter({
    has: page.getByText(`go:fixture/func:${scenario.expectedSource}`, { exact: true }),
  })
  await expect(results).not.toHaveCount(0)
  await results.first().getByTestId('code-search-explore').click()
  await expect(page.getByTestId('code-graph-results')).toContainText(scenario.expectedGraph)
  await results.first().getByTestId('code-search-source').click()
  await expect(page.getByTestId('code-source-result')).toContainText(scenario.expectedMarker)
  if (proof === null || searchPayload === null) {
    throw new Error('live Code Explorer did not send a binding-bound search request')
  }
  return { context, page, proof, searchPayload }
}

test('S2 live topology: linked A/B browser contexts retain pins and close without cross-disclosure', async ({ browser }, testInfo) => {
  const fixture = await readLiveFixture()
  const aScenario = operatorCodeFixture(fixture, 'operatorCode')
  const bScenario = operatorCodeFixture(fixture, 'operatorCodeB')
  const traffic: RouteTraffic[] = []
  const lifecycle: Record<string, unknown> = {
    worktrees: fixture.worktrees,
    candidate: fixture.candidate,
  }
  let a: CodeTab | undefined
  let b: CodeTab | undefined
  let registrationClient: MCPStdioClient | undefined
  let mcpA: MCPStdioClient | undefined
  let mcpB: MCPStdioClient | undefined

  try {
    expect(fixture.mock.prohibited).toBe(true)
    expect(fixture.candidate.commit).toBe(fixture.backend.sourceCommit)
    expect(fixture.worktrees.a.head).not.toBe(fixture.worktrees.b.head)
    expect(fixture.worktrees.a.fixtureSourceSha256).not.toBe(fixture.worktrees.b.fixtureSourceSha256)
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
    lifecycle.projectIdentityRegistration = registrationClient.transcript()

    const tabA = await pinAndRead(browser, fixture, fixture.browserCredential, aScenario, traffic)
    const tabB = await pinAndRead(browser, fixture, fixture.browserCredentialB, bScenario, traffic)
    a = tabA
    b = tabB
    const ownerReader = a.page.getByTestId('code-grant-chooser').getByRole('combobox').nth(1)
    await expect(ownerReader.getByRole('option', { name: fixture.browserCredential.email, exact: true })).toHaveCount(1)
    await ownerReader.selectOption({ label: fixture.browserCredential.email })
    await expect(ownerReader.locator('option:checked')).toHaveText(fixture.browserCredential.email)
    await expect(a.page.getByTestId('code-source-result')).toContainText(aScenario.expectedMarker)
    await expect(a.page.getByTestId('code-source-result')).not.toContainText(bScenario.expectedMarker)
    await expect(b.page.getByTestId('code-source-result')).toContainText(bScenario.expectedMarker)
    await expect(b.page.getByTestId('code-source-result')).not.toContainText(aScenario.expectedMarker)

    const [aUserID, bUserID] = await Promise.all([browserUserID(tabA.page), browserUserID(tabB.page)])
    expect(aUserID).not.toBe(bUserID)
    const [aKeycard, bKeycard] = await Promise.all([
      issueReadOnlyKeycard(tabA.page, `${fixture.fixtureId}-mcp-a`, aUserID),
      issueReadOnlyKeycard(tabA.page, `${fixture.fixtureId}-mcp-b`, bUserID),
    ])
    const externalClientA = await MCPStdioClient.start({
      clientRoot: fixture.mcp.clientRoots.a,
      executable: fixture.mcp.clientBinary,
      serverURL: fixture.backend.baseUrl,
      token: aKeycard,
    })
    mcpA = externalClientA
    const externalClientB = await MCPStdioClient.start({
      clientRoot: fixture.mcp.clientRoots.b,
      executable: fixture.mcp.clientBinary,
      serverURL: fixture.backend.baseUrl,
      token: bKeycard,
    })
    mcpB = externalClientB
    const [browserA, browserB, externalA, externalB] = await Promise.all([
      observeOperation(async () => {
        await tabA.page.getByTestId('code-query-input').fill(aScenario.expectedSearch)
        await tabA.page.getByTestId('code-search-submit').click()
        const results = tabA.page.getByTestId('code-search-results').getByRole('listitem').filter({
          has: tabA.page.getByText(`go:fixture/func:${aScenario.expectedSource}`, { exact: true }),
        })
        await expect(results).not.toHaveCount(0)
        await results.first().getByTestId('code-search-source').click()
        await expect(tabA.page.getByTestId('code-source-result')).toContainText(aScenario.expectedMarker)
        await expect(tabA.page.getByTestId('code-source-result')).not.toContainText(bScenario.expectedMarker)
      }),
      observeOperation(async () => {
        await tabB.page.getByTestId('code-query-input').fill(bScenario.expectedSearch)
        await tabB.page.getByTestId('code-search-submit').click()
        const results = tabB.page.getByTestId('code-search-results').getByRole('listitem').filter({
          has: tabB.page.getByText(`go:fixture/func:${bScenario.expectedSource}`, { exact: true }),
        })
        await expect(results).not.toHaveCount(0)
        await results.first().getByTestId('code-search-source').click()
        await expect(tabB.page.getByTestId('code-source-result')).toContainText(bScenario.expectedMarker)
        await expect(tabB.page.getByTestId('code-source-result')).not.toContainText(aScenario.expectedMarker)
      }),
      observeOperation(async () => {
        await externalClientA.initializeAndList()
        await externalClientA.readOnlySearch(fixture.fixtureId)
        return externalClientA.transcript()
      }),
      observeOperation(async () => {
        await externalClientB.initializeAndList()
        await externalClientB.readOnlySearch(fixture.fixtureId)
        return externalClientB.transcript()
      }),
    ])
    expect(externalA.value.usedStdio).toBe(true)
    expect(externalB.value.usedStdio).toBe(true)
    expect(externalA.value.externalPID).not.toBe(externalB.value.externalPID)
    expect(externalA.value.daemonPID).toBeGreaterThan(0)
    expect(externalB.value.daemonPID).toBeGreaterThan(0)
    expect(externalA.value.daemonPID).not.toBe(externalB.value.daemonPID)
    expect(externalA.value.daemonExecutable).toBe(fixture.mcp.clientBinary)
    expect(externalB.value.daemonExecutable).toBe(fixture.mcp.clientBinary)
    expect(externalA.value.daemonExecutableSha256).toBe(fixture.mcp.clientBinarySha256)
    expect(externalB.value.daemonExecutableSha256).toBe(fixture.mcp.clientBinarySha256)
    expect(externalA.value.daemonGeneration).not.toBe('')
    expect(externalB.value.daemonGeneration).not.toBe('')
    expect(externalA.value.sessionScoped).toBe(true)
    expect(externalB.value.sessionScoped).toBe(true)
    expect(externalA.value.rootLabel).toBe('A')
    expect(externalB.value.rootLabel).toBe('B')
    for (const transcript of [externalA.value, externalB.value]) {
      expect(transcript.methods).toEqual(expect.arrayContaining(['initialize', 'notifications/initialized', 'tools/list', 'tools/call']))
      expect(transcript.tools).toContain('recall')
    }
    const browserMCPOverlap = intervalsOverlap(browserA, externalA) || intervalsOverlap(browserA, externalB) || intervalsOverlap(browserB, externalA) || intervalsOverlap(browserB, externalB)
    expect(browserMCPOverlap).toBe(true)
    lifecycle.concurrentBrowserMCP = {
      browserMCPOverlap,
      daemonPIDsDistinct: true,
      externalPIDsDistinct: true,
      readOnlyCalls: ['recall.search'],
      sessionScoped: true,
      usedStdio: true,
    }

    const pending = await a.page.evaluate(async () => {
      const raw = sessionStorage.getItem('engram.operator-code.resume.v1')
      if (raw === null) return null
      const pair: unknown = JSON.parse(raw)
      if (pair === null || typeof pair !== 'object' || Array.isArray(pair)) return null
      const tabBindingId = Reflect.get(pair, 'tabBindingId')
      const resumeNonce = Reflect.get(pair, 'resumeNonce')
      const reloadToken = Reflect.get(pair, 'reloadToken')
      if (typeof tabBindingId !== 'string' || typeof resumeNonce !== 'string' || typeof reloadToken !== 'string') return null
      const response = await fetch('/api/code/tabs/resume', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Engram-Request-ID': crypto.randomUUID() },
        body: JSON.stringify({ tab_binding_id: tabBindingId, resume_nonce: resumeNonce, reload_token: reloadToken, document_nonce: crypto.randomUUID() }),
      })
      const body: unknown = await response.json()
      return { status: response.status, body }
    })
    expect(pending).not.toBeNull()
    expect(pending?.status).toBe(200)
    expect(pending?.body).toEqual({ state: 'RELOAD_PENDING' })
    lifecycle.delayedReload = 'RELOAD_PENDING'

    const pair = await a.page.evaluate((key) => sessionStorage.getItem(key), RESUME_STORAGE_KEY)
    expect(pair).not.toBeNull()
    const copied = await a.context.newPage()
    await copied.addInitScript(({ key, value }) => {
      if (value !== null) sessionStorage.setItem(key, value)
    }, { key: RESUME_STORAGE_KEY, value: pair })
    await copied.goto(`${fixture.frontend.baseUrl}/code`, { waitUntil: 'domcontentloaded' })
    await expect(copied.getByTestId('code-bootstrap-evidence')).toContainText('TAB_BINDING_COLLISION')
    await expect(copied.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
    await copied.close()


    const popupPromise = a.page.waitForEvent('popup')
    await a.page.evaluate(() => { window.open('/code', '_blank') })
    const child = await popupPromise
    await child.waitForLoadState('domcontentloaded')
    await expect(child.getByTestId('code-bootstrap-evidence')).toContainText('TAB_BINDING_READY')
    await expect(child.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
    await selectFixtureContext(child, fixture, 'a')
    await child.getByTestId('code-pin-context').click()
    await expect(child.getByTestId('code-context-pinned')).toBeVisible()
    await child.reload({ waitUntil: 'domcontentloaded' })
    await expect(child.getByTestId('code-context-pinned')).toHaveCount(0)
    await expect(child.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
    await expect(child.getByTestId('code-pin-context')).toBeEnabled()
    await child.close()

    const beforeReloadPayload = { ...a.searchPayload }
    const closeA = await a.page.evaluate(async ({ documentProof, tabBindingId }) => {
      const response = await fetch(`/api/code/tabs/${encodeURIComponent(tabBindingId)}`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json', 'X-Engram-Request-ID': crypto.randomUUID() },
        body: JSON.stringify({ document_proof: documentProof }),
      })
      return { status: response.status, text: await response.text() }
    }, a.proof)
    expect(closeA).toEqual({ status: 204, text: '' })
    await a.page.reload({ waitUntil: 'domcontentloaded' })
    await expect(a.page.getByTestId('code-context-pinned')).toHaveCount(0)
    await expect(a.page.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
    await expect(a.page.getByTestId('code-pin-context')).toBeEnabled()
    const staleReplay = await a.page.evaluate(async (body) => {
      const response = await fetch('/api/code/search', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Engram-Request-ID': crypto.randomUUID() },
        body: JSON.stringify(body),
      })
      return { status: response.status, text: await response.text() }
    }, beforeReloadPayload)
    expect(staleReplay.status).toBe(403)
    expect(staleReplay.text).toBe('')

    const closeB = await b.page.evaluate(async ({ documentProof, tabBindingId }) => {
      const response = await fetch(`/api/code/tabs/${encodeURIComponent(tabBindingId)}`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json', 'X-Engram-Request-ID': crypto.randomUUID() },
        body: JSON.stringify({ document_proof: documentProof }),
      })
      return { status: response.status, text: await response.text() }
    }, b.proof)
    expect(closeB).toEqual({ status: 204, text: '' })
    lifecycle.acknowledgedClose = ['A', 'B']
    lifecycle.replayedProof = 'denied_without_body'
  } finally {
    await Promise.all([registrationClient?.close(), mcpA?.close(), mcpB?.close()])
    const externalMCP = [registrationClient, mcpA, mcpB].flatMap((client) => client === undefined ? [] : [client.transcript()])
    for (const transcript of externalMCP) {
      expect(transcript.daemonExecutable).toBe(fixture.mcp.clientBinary)
      expect(transcript.daemonExecutableSha256).toBe(fixture.mcp.clientBinarySha256)
      expect(transcript.daemonGeneration).not.toBe('')
      expect(transcript.processTreeStopped).toBe(true)
      expect(transcript.daemonPID).toBeGreaterThan(0)
      expect(transcript.stateRootRemoved).toBe(true)
    }
    lifecycle.externalMCP = externalMCP
    const state = await appendBrowserTraffic(traffic)
    const evidence = JSON.stringify({
      evidenceKind: 'real-authenticated-go-postgresql-browser-linked-worktrees',
      candidate: state.candidate,
      backend: { sourceCommit: state.backend.sourceCommit, binarySha256: state.backend.binarySha256 },
      worktrees: state.worktrees,
      lifecycle,
      traffic: state.traffic,
    }, null, 2)
    await writeFile(testInfo.outputPath('s2-live-code-topology.json'), evidence, 'utf8')
    await testInfo.attach('s2-linked-worktree-browser-topology', {
      contentType: 'application/json',
      body: new TextEncoder().encode(evidence),
    })
    await Promise.all([a?.context.close(), b?.context.close()])
  }
})
