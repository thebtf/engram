import { execFileSync } from 'node:child_process'
import { readFile, rename, rm, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
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

async function findSearchResult(page: Page, symbol: string): Promise<Locator> {
  const result = page.getByTestId('code-search-results').getByRole('listitem').filter({
    has: page.getByText(`go:fixture/func:${symbol}`, { exact: true }),
  })
  for (let offset = 0; offset < 8 && await result.count() === 0; offset += 1) {
    const next = page.getByTestId('code-search-next')
    await expect(next).toBeVisible()
    const response = page.waitForResponse((candidate) => candidate.request().method() === 'POST' && new URL(candidate.url()).pathname === '/api/code/search')
    await next.click()
    expect((await response).status()).toBe(200)
  }
  await expect(result).toHaveCount(1)
  return result.first()
}

async function pinAndRead(browser: Browser, fixture: LiveFixtureState, credential: BrowserCredential, scenario: OperatorCodeFixture, traffic: RouteTraffic[]): Promise<CodeTab> {
  const context = await browser.newContext()
  const page = await context.newPage()
  let proof: CodeProof | null = null
  let searchPayload: Record<string, unknown> | null = null
  let tab: CodeTab | null = null
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.origin !== fixture.frontend.baseUrl || !url.pathname.startsWith('/api/code/')) return
    const candidate = proofFromRequestPayload(request.postData())
    if (candidate !== null) {
      proof = candidate
      if (tab !== null) tab.proof = candidate
    }
    if (url.pathname === '/api/code/search' && request.postData() !== null) {
      const body: unknown = JSON.parse(request.postData() || '')
      if (body !== null && typeof body === 'object' && !Array.isArray(body)) {
        searchPayload = { ...body }
        if (tab !== null) tab.searchPayload = searchPayload
      }
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
  const searchResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/code/search')
  await page.getByTestId('code-search-submit').click()
  expect((await searchResponse).status()).toBe(200)
  const result = await findSearchResult(page, scenario.expectedSource)
  await result.getByTestId('code-search-explore').click()
  await expect(page.getByTestId('code-graph-results')).toContainText(scenario.expectedGraph)
  await result.getByTestId('code-search-source').click()
  await expect(page.getByTestId('code-source-result')).toContainText(scenario.expectedMarker)
  if (proof === null || searchPayload === null) {
    throw new Error('live Code Explorer did not send a binding-bound search request')
  }
  tab = { context, page, proof, searchPayload }
  return tab
}

test('S2 live topology: linked A/B browser contexts retain pins and close without cross-disclosure', async ({ browser }, testInfo) => {
  test.setTimeout(300_000)
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
  let dirtyA: MCPStdioClient | undefined
  let dirtyB: MCPStdioClient | undefined
  let reconnected: MCPStdioClient | undefined

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
    await a.page.getByTestId('code-grant-chooser').locator('summary').click()
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
        const searchResponse = tabA.page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/code/search')
        await tabA.page.getByTestId('code-search-submit').click()
        expect((await searchResponse).status()).toBe(200)
        const result = await findSearchResult(tabA.page, aScenario.expectedSource)
        await result.getByTestId('code-search-source').click()
        await expect(tabA.page.getByTestId('code-source-result')).toContainText(aScenario.expectedMarker)
        await expect(tabA.page.getByTestId('code-source-result')).not.toContainText(bScenario.expectedMarker)
      }),
      observeOperation(async () => {
        await tabB.page.getByTestId('code-query-input').fill(bScenario.expectedSearch)
        const searchResponse = tabB.page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/code/search')
        await tabB.page.getByTestId('code-search-submit').click()
        expect((await searchResponse).status()).toBe(200)
        const result = await findSearchResult(tabB.page, bScenario.expectedSource)
        await result.getByTestId('code-search-source').click()
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

    const ownerID = await browserUserID(a.page)
    const ownerKeycard = await a.page.evaluate(async (principal) => {
      const response = await fetch('/api/auth/tokens', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: crypto.randomUUID(), principal: `browser-user/${principal}`, principal_kind: 'human', scope: 'read-write' }) })
      const body = await response.json() as { token: string }
      return { status: response.status, token: body.token }
    }, ownerID)
    expect(ownerKeycard.status).toBe(200)
    expect(ownerKeycard.token).not.toBe('')
    const dirtyConfig = { parserBundleDigest: fixture.mcp.firstIndex.parserBundleDigest, parserExecutable: fixture.mcp.firstIndex.parserExecutable }
    dirtyA = await MCPStdioClient.start({ clientRoot: fixture.dirty.a.root, codeIndex: dirtyConfig, executable: fixture.mcp.clientBinary, serverURL: fixture.backend.baseUrl, token: ownerKeycard.token })
    dirtyB = await MCPStdioClient.start({ clientRoot: fixture.dirty.b.root, codeIndex: dirtyConfig, executable: fixture.mcp.clientBinary, serverURL: fixture.backend.baseUrl, token: ownerKeycard.token })
    const aHEAD = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: fixture.dirty.a.root, encoding: 'utf8' }).trim()
    const bHEAD = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: fixture.dirty.b.root, encoding: 'utf8' }).trim()
    expect(aHEAD).toBe(bHEAD)
    expect(execFileSync('git', ['status', '--porcelain'], { cwd: fixture.dirty.a.root, encoding: 'utf8' })).toContain('workspace-dirty.go')
    expect(execFileSync('git', ['status', '--porcelain'], { cwd: fixture.dirty.b.root, encoding: 'utf8' })).toContain('workspace-dirty.go')
    await Promise.all([dirtyA.initializeAndList(), dirtyB.initializeAndList()])
    const source = await dirtyA.registerDirtyCheckout({ root: fixture.dirty.a.root, label: `${fixture.fixtureId}-dirty-source` })
    const registeredA = { sourceId: String(source.source_id), checkoutId: String(source.checkout_id), incarnationId: String(source.incarnation_id), analysisProfileId: String(source.analysis_profile_id) }
    const registeredBResponse = await dirtyB.registerDirtyCheckout({ root: fixture.dirty.b.root, id: registeredA.sourceId })
    const registeredB = { sourceId: String(registeredBResponse.source_id), checkoutId: String(registeredBResponse.checkout_id), incarnationId: String(registeredBResponse.incarnation_id), analysisProfileId: String(registeredBResponse.analysis_profile_id) }
    expect(registeredB.sourceId).toBe(registeredA.sourceId)
    expect(registeredB.checkoutId).not.toBe(registeredA.checkoutId)
    const [headA, headB] = await Promise.all([readFile(join(fixture.dirty.a.root, '.git'), 'utf8'), readFile(join(fixture.dirty.b.root, '.git'), 'utf8')])
    expect(headA).not.toBe(headB)
    const viewID = (status: Record<string, unknown>): string => {
      const context = status.context
      return context !== null && typeof context === 'object' && typeof Reflect.get(context, 'view_id') === 'string' ? Reflect.get(context, 'view_id') as string : ''
    }
    const indexAt = async (client: MCPStdioClient, handle: string): Promise<{ view: string; indexMs: number; embeddingMs: number; status: Record<string, unknown> }> => {
      const start = Date.now()
      const run = await client.indexDirtyCheckout(handle)
      const indexed = await client.dirtyIndexStatus(handle, run)
      expect(indexed.server_counts_available).toBe(true)
      const view = viewID(indexed)
      expect(view).not.toBe('')
      const indexMs = Date.now() - start
      await expect.poll(async () => {
        const current = await client.dirtyIndexStatus(handle)
        return current.total_chunks === current.embedded_chunks && typeof current.total_chunks === 'number' && current.total_chunks > 0
      }, { timeout: 90_000, intervals: [500, 1000, 2000] }).toBe(true)
      return { view, indexMs, embeddingMs: Date.now() - start - indexMs, status: indexed }
    }
    const initialA = await indexAt(dirtyA, String(source.context_handle))
    const initialB = await indexAt(dirtyB, String(registeredBResponse.context_handle))
    expect(initialA.view).not.toBe(initialB.view)
    const initialQueryA = await dirtyA.dirtySearch('WorkspaceDirtyAlpha')
    const initialQueryB = await dirtyB.dirtySearch('WorkspaceDirtyBeta')
    expect(JSON.stringify(initialQueryA)).toContain(`${fixture.dirty.a.marker}-initial`)
    expect(JSON.stringify(initialQueryA)).not.toContain(fixture.dirty.b.marker)
    expect(JSON.stringify(initialQueryB)).toContain(`${fixture.dirty.b.marker}-initial`)
    expect(JSON.stringify(initialQueryB)).not.toContain(fixture.dirty.a.marker)

    const choices = await a.page.evaluate(async () => { const response = await fetch('/api/code/grants/choices', { headers: { 'X-Engram-Request-ID': crypto.randomUUID() } }); return { status: response.status, text: await response.text() } })
    expect(choices.status).toBe(200)
    const choiceBody = JSON.parse(choices.text) as { choices: Array<{ choice_ref: string; repository: string }>; targets: Array<{ target_ref: string; label: string }> }
    const owned = choiceBody.choices.filter((item) => item.repository === `${fixture.fixtureId}-dirty-source`)
    expect(owned).toHaveLength(2)
    for (const choice of owned) for (const email of [fixture.browserCredential.email, fixture.browserCredentialB.email]) {
      const target = choiceBody.targets.find((entry) => entry.label === email)
      expect(target).toBeDefined()
      const result = await a.page.evaluate(async ({ choiceRef, targetRef }) => { const response = await fetch('/api/code/grants', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Engram-Request-ID': crypto.randomUUID() }, body: JSON.stringify({ choice_ref: choiceRef, target_ref: targetRef }) }); return response.status }, { choiceRef: choice.choice_ref, targetRef: target!.target_ref })
      expect(result).toBe(200)
    }
    const pinDirty = async (page: Page, wanted: string): Promise<void> => {
      await expect(page.getByTestId('code-context-repository').locator('option').filter({ hasText: `${fixture.fixtureId}-dirty-source` })).toHaveCount(1)
      await page.getByTestId('code-context-repository').selectOption({ label: `${fixture.fixtureId}-dirty-source` })
      const copies = page.getByTestId('code-context-working-copy').locator('option:not([disabled])')
      await expect(copies).toHaveCount(2)
      for (let index = 0; index < 2; index += 1) {
        const value = await copies.nth(index).getAttribute('value')
        if (value === null) throw new Error('registered checkout has no selection')
        await page.getByTestId('code-context-working-copy').selectOption(value)
        const snapshot = page.getByTestId('code-context-snapshot').locator('option:not([disabled])')
        await expect(snapshot).toHaveCount(1)
        const ref = await snapshot.getAttribute('value')
        if (ref === null) throw new Error('registered checkout has no published View')
        await page.getByTestId('code-context-snapshot').selectOption(ref)
        await page.getByTestId('code-pin-context').click()
        await page.getByTestId('code-query-input').fill(wanted)
        const response = page.waitForResponse((entry) => new URL(entry.url()).pathname === '/api/code/search' && entry.request().method() === 'POST')
        await page.getByTestId('code-search-submit').click()
        expect((await response).status()).toBe(200)
        const results = page.getByTestId('code-search-results')
        if (await results.getByText(`go:fixture/func:${wanted}`, { exact: true }).count() > 0) {
          await findSearchResult(page, wanted)
          return
        }
      }
      throw new Error(`saved checkout ${wanted} not found in either published View`)
    }
    await Promise.all([a.page.reload({ waitUntil: 'domcontentloaded' }), b.page.reload({ waitUntil: 'domcontentloaded' })])
    await pinDirty(a.page, 'WorkspaceDirtyAlpha')
    const dirtyACheckoutOption = await a.page.getByTestId('code-context-working-copy').inputValue()
    expect(dirtyACheckoutOption).not.toBe('')
    const originalSnapshotOption = await a.page.getByTestId('code-context-snapshot').inputValue()
    expect(originalSnapshotOption).not.toBe('')
    await pinDirty(b.page, 'WorkspaceDirtyBeta')
    const oldA = await findSearchResult(a.page, 'WorkspaceDirtyAlpha')
    await oldA.getByTestId('code-search-source').click()
    await expect(a.page.getByTestId('code-source-result')).toContainText(`${fixture.dirty.a.marker}-initial`)

    const transitions: Array<{ action: string; view: string; publicationMs: number; embeddingMs: number }> = []
    let previousView = initialA.view
    const savedA = join(fixture.dirty.a.root, 'workspace-dirty.go')
    const renamedA = join(fixture.dirty.a.root, 'workspace-renamed.go')
    for (const [action, change] of [
      ['save', async () => writeFile(savedA, `package fixture\nfunc WorkspaceDirtyAlpha() string { return "${fixture.dirty.a.marker}-saved" }\n`)],
      ['rename', async () => rename(savedA, renamedA)],
      ['delete', async () => rm(renamedA)],
    ] as const) {
      const start = Date.now()
      await change()
      let nextView = ''
      await expect.poll(async () => { const status = await dirtyA!.dirtyIndexStatus(String(source.context_handle)); nextView = viewID(status); return nextView !== '' && nextView !== previousView }, { timeout: 90_000, intervals: [500, 1000, 2000] }).toBe(true)
      const publicationMs = Date.now() - start
      await expect.poll(async () => { const status = await dirtyA!.dirtyIndexStatus(String(source.context_handle)); return typeof status.total_chunks === 'number' && status.total_chunks > 0 && status.total_chunks === status.embedded_chunks }, { timeout: 90_000, intervals: [500, 1000, 2000] }).toBe(true)
      transitions.push({ action, view: nextView, publicationMs, embeddingMs: Date.now() - start - publicationMs })
      previousView = nextView
      const unchanged = await dirtyB.dirtyIndexStatus(String(registeredBResponse.context_handle))
      expect(viewID(unchanged)).toBe(initialB.view)
      expect(JSON.stringify(await dirtyB.dirtySearch('WorkspaceDirtyBeta'))).toContain(`${fixture.dirty.b.marker}-initial`)
      await oldA.getByTestId('code-search-source').click()
      await expect(a.page.getByTestId('code-source-result')).toContainText(`${fixture.dirty.a.marker}-initial`)
    }
    await a.page.locator('.context-picker .actions button').first().click()
    await a.page.getByTestId('code-context-repository').selectOption({ label: `${fixture.fixtureId}-dirty-source` })
    await a.page.getByTestId('code-context-working-copy').selectOption(dirtyACheckoutOption)
    const snapshot = a.page.getByTestId('code-context-snapshot').locator('option:not([disabled])')
    await expect(snapshot.first()).not.toHaveAttribute('value', originalSnapshotOption)
    await a.page.getByTestId('code-context-snapshot').selectOption((await snapshot.first().getAttribute('value'))!)
    await a.page.getByTestId('code-pin-context').click()
    await a.page.getByTestId('code-query-input').fill('WorkspaceDirtyAlpha')
    const deletedSearch = a.page.waitForResponse((entry) => new URL(entry.url()).pathname === '/api/code/search' && entry.request().method() === 'POST')
    await a.page.getByTestId('code-search-submit').click()
    expect((await deletedSearch).status()).toBe(200)
    await expect(a.page.getByTestId('code-search-results').getByText('go:fixture/func:WorkspaceDirtyAlpha', { exact: true })).toHaveCount(0)
    await dirtyA.close()
    reconnected = await MCPStdioClient.start({ clientRoot: fixture.dirty.a.root, codeIndex: dirtyConfig, executable: fixture.mcp.clientBinary, serverURL: fixture.backend.baseUrl, token: ownerKeycard.token })
    await reconnected.initializeAndList()
    const rebound = await reconnected.selectDirtyCheckout(registeredA)
    expect(rebound.context).not.toBeNull()
    expect(rebound.checkout_id).toBe(registeredA.checkoutId)
    const reconnectView = viewID(await reconnected.dirtyIndexStatus(String(rebound.context_handle)))
    expect(reconnectView).not.toBe(initialA.view)
    expect(JSON.stringify(await reconnected.dirtySearch('WorkspaceDirtyAlpha'))).not.toContain(`${fixture.dirty.a.marker}-initial`)
    lifecycle.dirty = { source: registeredA.sourceId, a: registeredA.checkoutId, b: registeredB.checkoutId, initial: [initialA, initialB], transitions, oldPinRetained: true, switched: true, reconnectView }

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
    await Promise.all([registrationClient?.close(), mcpA?.close(), mcpB?.close(), dirtyA?.close(), dirtyB?.close(), reconnected?.close()])
    const externalMCP = [registrationClient, mcpA, mcpB, dirtyA, dirtyB, reconnected].flatMap((client) => client === undefined ? [] : [client.transcript()])
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
