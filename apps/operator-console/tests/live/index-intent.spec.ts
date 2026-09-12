import { spawn } from 'node:child_process'
import { writeFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { LiveFixtureState, RouteTraffic } from './fixture-bootstrap'

const INDEX_INTENT_STORAGE_KEY = 'engram.operator-code.index-intent.v1'

type LifecycleState = 'submitted' | 'queued' | 'acknowledged' | 'running' | 'completed' | 'unavailable' | 'failed'

interface IntentResponse {
  intentRef: string
  state: LifecycleState
  result: { viewRef: string; generation: number } | null
}

interface SubmitRequest {
  requestRef: string
  kind: 'reindex' | 'reconcile'
}

interface FixtureIntentView {
  previousViewRef: string
  sourceRef: string
  checkoutRef: string
  incarnationRef: string
  profileRef: string
  generation: number
}

function parseIntentResponse(value: unknown): IntentResponse | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const intentRef = Reflect.get(value, 'intent_ref')
  const state = Reflect.get(value, 'state')
  const resultValue = Reflect.get(value, 'result')
  if (
    typeof intentRef !== 'string' || intentRef === ''
    || state !== 'submitted' && state !== 'queued' && state !== 'acknowledged' && state !== 'running'
    && state !== 'completed' && state !== 'unavailable' && state !== 'failed'
  ) return null
  if (resultValue === undefined || resultValue === null) return { intentRef, state, result: null }
  if (typeof resultValue !== 'object' || Array.isArray(resultValue)) return null
  const viewRef = Reflect.get(resultValue, 'view_ref')
  const generation = Reflect.get(resultValue, 'generation')
  if (typeof viewRef !== 'string' || viewRef === '' || !Number.isSafeInteger(generation) || generation < 1) return null
  return { intentRef, state, result: { viewRef, generation } }
}

function parseSubmitRequest(value: string | null): SubmitRequest | null {
  if (value === null || value === '') return null
  let parsed: unknown
  try {
    parsed = JSON.parse(value)
  } catch {
    return null
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) return null
  const requestRef = Reflect.get(parsed, 'request_ref')
  const kind = Reflect.get(parsed, 'kind')
  if (typeof requestRef !== 'string' || requestRef === '' || kind !== 'reindex' && kind !== 'reconcile') return null
  return { requestRef, kind }
}

function quoteSQL(value: string): string {
  return `'${value.replaceAll("'", "''")}'`
}

async function fixtureSQL(fixture: LiveFixtureState, statement: string): Promise<string> {
  const child = spawn('docker', [
    'exec', fixture.postgres.container,
    'psql', '--username=engram', '--dbname=engram', '--tuples-only', '--no-align', '--field-separator=\t', '--command', statement,
  ], { windowsHide: true })
  let output = ''
  child.stdout?.on('data', (chunk: Buffer) => { output += chunk.toString('utf8') })
  child.stderr?.resume()
  const code = await new Promise<number | null>((resolve, reject) => {
    child.once('error', reject)
    child.once('close', resolve)
  })
  if (code !== 0) throw new Error('disposable fixture lifecycle control failed')
  return output.trim()
}

function fixtureColumns(output: string, label: string): string[] {
  const rows = output.split(/\r?\n/).filter((row) => row !== '')
  if (rows.length !== 1) throw new Error(`disposable fixture did not return one ${label} row`)
  return rows[0].split('\t')
}

async function loadFixtureIntentView(fixture: LiveFixtureState, intentRef: string): Promise<FixtureIntentView> {
  const columns = fixtureColumns(await fixtureSQL(fixture, `
    SELECT intent.previous_view_id::text, intent.source_id::text, intent.checkout_id::text, intent.incarnation_id::text, intent.profile_id::text, intent.previous_generation::text
    FROM uci_index_intents AS intent
    JOIN ci_views AS previous
      ON previous.view_id = intent.previous_view_id
      AND previous.source_id = intent.source_id
      AND previous.checkout_id = intent.checkout_id
      AND previous.incarnation_id = intent.incarnation_id
      AND previous.profile_id = intent.profile_id
      AND previous.generation = intent.previous_generation
    WHERE intent.intent_id = ${quoteSQL(intentRef)};
  `), 'intent previous View')
  const [previousViewRef, sourceRef, checkoutRef, incarnationRef, profileRef, generationText] = columns
  const generation = Number(generationText)
  if (
    !previousViewRef || !sourceRef || !checkoutRef || !incarnationRef || !profileRef
    || !Number.isSafeInteger(generation) || generation < 1
  ) throw new Error('disposable fixture previous View is not a complete readable context')
  return { previousViewRef, sourceRef, checkoutRef, incarnationRef, profileRef, generation }
}

async function countFixtureIntents(fixture: LiveFixtureState, requestRef: string): Promise<number> {
  const output = await fixtureSQL(fixture, `SELECT count(*) FROM uci_index_intents WHERE request_ref = ${quoteSQL(requestRef)};`)
  const count = Number(output)
  if (!Number.isSafeInteger(count) || count < 0) throw new Error('disposable fixture returned an invalid intent count')
  return count
}

async function fixtureIntentState(fixture: LiveFixtureState, intentRef: string): Promise<LifecycleState> {
  const state = await fixtureSQL(fixture, `SELECT state FROM uci_index_intents WHERE intent_id = ${quoteSQL(intentRef)};`)
  if (state === 'submitted' || state === 'queued' || state === 'acknowledged' || state === 'running' || state === 'completed' || state === 'unavailable' || state === 'failed') return state
  throw new Error('disposable fixture returned an invalid intent state')
}

async function setFixtureState(fixture: LiveFixtureState, intentRef: string, state: Exclude<LifecycleState, 'completed'>): Promise<void> {
  const intent = quoteSQL(intentRef)
  const timestamp = 'GREATEST(now(), created_at)'
  switch (state) {
    case 'submitted':
    case 'queued':
    case 'unavailable':
      await fixtureSQL(fixture, `UPDATE uci_index_intents SET state = '${state}', attempt = 0, acknowledgement_epoch = 0, acknowledged_owner = NULL, acknowledged_at = NULL, updated_at = ${timestamp} WHERE intent_id = ${intent};`)
      return
    case 'acknowledged':
      await fixtureSQL(fixture, `UPDATE uci_index_intents SET state = 'acknowledged', attempt = 1, acknowledgement_epoch = 1, acknowledged_owner = 'fixture-index-owner', acknowledged_at = ${timestamp}, updated_at = ${timestamp} WHERE intent_id = ${intent};`)
      return
    case 'running':
    case 'failed':
      await fixtureSQL(fixture, `UPDATE uci_index_intents SET state = '${state}', updated_at = ${timestamp} WHERE intent_id = ${intent};`)
      return
    default: {
      const exhaustive: never = state
      throw new Error(`unsupported disposable fixture state ${exhaustive}`)
    }
  }
}

async function completeFixtureIntent(fixture: LiveFixtureState, intentRef: string): Promise<{ viewRef: string; generation: number }> {
  const previous = await loadFixtureIntentView(fixture, intentRef)
  const createdColumns = fixtureColumns(await fixtureSQL(fixture, `
    WITH inserted AS (
      INSERT INTO ci_views (
        view_id, checkout_id, source_id, incarnation_id, generation, profile_id, head_oid, object_format, ref_label, dirty, observed_fs_seq, scan_start, scan_end, manifest_digest, state, coverage_json, published_at, created_at, updated_at
      )
      SELECT gen_random_uuid(), view.checkout_id, view.source_id, view.incarnation_id, view.generation + 1, view.profile_id, view.head_oid, view.object_format, view.ref_label, view.dirty, view.observed_fs_seq, view.scan_start, now(), view.manifest_digest, 'published', view.coverage_json, now(), now(), now()
      FROM ci_views AS view
      WHERE view.view_id = ${quoteSQL(previous.previousViewRef)}
        AND view.source_id = ${quoteSQL(previous.sourceRef)}
        AND view.checkout_id = ${quoteSQL(previous.checkoutRef)}
        AND view.incarnation_id = ${quoteSQL(previous.incarnationRef)}
        AND view.profile_id = ${quoteSQL(previous.profileRef)}
        AND view.generation = ${previous.generation}
      RETURNING view_id, generation
    )
    SELECT view_id::text, generation::text FROM inserted;
  `), 'new published View')
  const [viewRef, generationText] = createdColumns
  const generation = Number(generationText)
  if (!viewRef || viewRef === previous.previousViewRef || !Number.isSafeInteger(generation) || generation <= previous.generation) {
    throw new Error('disposable fixture did not create a new published View')
  }
  const readableColumns = fixtureColumns(await fixtureSQL(fixture, `
    SELECT view.view_id::text, view.generation::text
    FROM ci_views AS view
    JOIN uci_index_intents AS intent
      ON intent.intent_id = ${quoteSQL(intentRef)}
      AND view.source_id = intent.source_id
      AND view.checkout_id = intent.checkout_id
      AND view.incarnation_id = intent.incarnation_id
      AND view.profile_id = intent.profile_id
    WHERE view.view_id = ${quoteSQL(viewRef)}
      AND view.state = 'published'
      AND view.generation > intent.previous_generation;
  `), 'published readable View')
  if (readableColumns[0] !== viewRef || Number(readableColumns[1]) !== generation) {
    throw new Error('disposable fixture new View is not readable in the exact intent context')
  }
  const completionColumns = fixtureColumns(await fixtureSQL(fixture, `
    WITH completed AS (
      UPDATE uci_index_intents
      SET state = 'completed', result_space_id = NULL, result_view_id = ${quoteSQL(viewRef)}, result_generation = ${generation}, updated_at = GREATEST(now(), created_at)
      WHERE intent_id = ${quoteSQL(intentRef)}
        AND state = 'running'
        AND source_id = ${quoteSQL(previous.sourceRef)}
        AND checkout_id = ${quoteSQL(previous.checkoutRef)}
        AND incarnation_id = ${quoteSQL(previous.incarnationRef)}
        AND profile_id = ${quoteSQL(previous.profileRef)}
        AND previous_view_id = ${quoteSQL(previous.previousViewRef)}
        AND previous_generation = ${previous.generation}
      RETURNING result_view_id, result_generation
    )
    SELECT result_view_id::text, result_generation::text FROM completed;
  `), 'completed intent')
  if (completionColumns[0] !== viewRef || Number(completionColumns[1]) !== generation) {
    throw new Error('disposable fixture did not complete the exact intent with its published View')
  }
  return { viewRef, generation }
}

function sanitizedIntentPath(path: string): string {
  return path.replace(/^\/api\/code\/index-intents\/[^/]+(?:\/retry)?$/, (match) => match.endsWith('/retry')
    ? '/api/code/index-intents/:intent_ref/retry'
    : '/api/code/index-intents/:intent_ref')
}

async function login(page: Page, fixture: LiveFixtureState): Promise<void> {
  await page.goto(`${fixture.frontend.baseUrl}/`, { waitUntil: 'domcontentloaded' })
  const login = await page.evaluate(async (credential) => {
    const response = await fetch('/api/auth/user-login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(credential),
    })
    return response.status
  }, fixture.browserCredential)
  expect(login).toBe(200)
}

async function submitIntent(page: Page, kind: 'reindex' | 'reconcile'): Promise<IntentResponse> {
  const responsePromise = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return response.request().method() === 'POST' && url.pathname === '/api/code/index-intents'
  })
  await page.getByTestId(`index-intent-${kind}`).click()
  const response = await responsePromise
  expect(response.status()).toBe(202)
  const intent = parseIntentResponse(await response.json())
  expect(intent).not.toBeNull()
  if (intent === null) throw new Error('real index-intent admission returned an invalid response')
  expect(intent.state).not.toBe('completed')
  expect(intent.result).toBeNull()
  return intent
}

async function checkRenderedState(page: Page, state: LifecycleState, renderedStates: LifecycleState[]): Promise<IntentResponse> {
  const responsePromise = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return response.request().method() === 'GET' && /^\/api\/code\/index-intents\/[^/]+$/.test(url.pathname)
  })
  await page.getByTestId('index-intent-check-status').click()
  const response = await responsePromise
  expect(response.status()).toBe(200)
  const intent = parseIntentResponse(await response.json())
  expect(intent).not.toBeNull()
  if (intent === null) throw new Error('real index-intent status returned an invalid response')
  expect(intent.state).toBe(state)
  await expect(page.getByTestId('index-intent-state')).toHaveText(state)
  renderedStates.push(state)
  return intent
}

test('S4 live acceptance: truthful index intent lifecycle uses real HTTP and fixture-only daemon control', async ({ browser }, testInfo) => {
  const fixture = await readLiveFixture()
  const traffic: RouteTraffic[] = []
  const submitRequests: SubmitRequest[] = []
  const retryProofBodies: boolean[] = []
  const statusHeaderChecks: boolean[] = []
  const renderedStates: LifecycleState[] = []
  const context = await browser.newContext()
  const page = await context.newPage()
  let durableIntentCount = 0
  let completedResultReleased = false
  let reloadRestored = false
  let terminalActionCreatedNewIntent = false

  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.origin !== fixture.frontend.baseUrl || !url.pathname.startsWith('/api/code/index-intents')) return
    if (request.method() === 'POST' && url.pathname === '/api/code/index-intents') {
      const submit = parseSubmitRequest(request.postData())
      if (submit !== null) submitRequests.push(submit)
    }
    if (request.method() === 'POST' && url.pathname.endsWith('/retry')) {
      const body = request.postData()
      let parsed: unknown = null
      try {
        parsed = body === null ? null : JSON.parse(body)
      } catch {
        parsed = null
      }
      const keys = parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed) ? Object.keys(parsed).sort() : []
      retryProofBodies.push(parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)
        && keys.join(',') === 'document_proof,tab_binding_id'
        && typeof Reflect.get(parsed, 'document_proof') === 'string'
        && typeof Reflect.get(parsed, 'tab_binding_id') === 'string'
        && Reflect.get(parsed, 'request_ref') === undefined
        && Reflect.get(parsed, 'kind') === undefined
        && Reflect.get(parsed, 'path') === undefined
        && Reflect.get(parsed, 'credential') === undefined
        && Reflect.get(parsed, 'daemon_owner') === undefined
        && Reflect.get(parsed, 'environment') === undefined)
    }
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
    if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/code/index-intents')) {
      traffic.push({ origin: 'browser', method: response.request().method(), path: sanitizedIntentPath(url.pathname), status: response.status(), at: new Date().toISOString() })
    }
  })

  try {
    expect(fixture.mock.prohibited).toBe(true)
    expect(fixture.mock.liveConfigContainsMockCommand).toBe(false)
    expect(fixture.candidate.commit).toBe(fixture.backend.sourceCommit)

    await login(page, fixture)
    await page.goto(`${fixture.frontend.baseUrl}/code`, { waitUntil: 'domcontentloaded' })
    const contextSelect = page.getByTestId('code-context-select')
    const contextOption = contextSelect.locator('option').filter({ hasText: `${fixture.fixtureId}-a` })
    const contextValue = await contextOption.getAttribute('value')
    if (contextValue === null) throw new Error('fixture catalog did not expose a selectable View')
    await contextSelect.selectOption(contextValue)
    await expect(page.getByTestId('code-context-candidate')).toBeVisible()
    await page.getByTestId('code-pin-context').click()
    await expect(page.getByTestId('code-context-pinned')).toBeVisible()
    const pinnedBeforeCompletion = await page.getByTestId('code-context-pinned').textContent()

    const durable = await submitIntent(page, 'reindex')
    expect(durable.state).toBe('queued')
    await expect(page.getByTestId('index-intent-state')).toHaveText('queued')
    renderedStates.push('queued')

    const lostResponse = await page.evaluate((storageKey) => {
      const raw = sessionStorage.getItem(storageKey)
      if (raw === null) return null
      const value: unknown = JSON.parse(raw)
      if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
      const requestRef = Reflect.get(value, 'requestRef')
      const kind = Reflect.get(value, 'kind')
      if (typeof requestRef !== 'string' || requestRef === '' || kind !== 'reindex' && kind !== 'reconcile') return null
      sessionStorage.setItem(storageKey, JSON.stringify({ requestRef, kind }))
      return { requestRef, kind }
    }, INDEX_INTENT_STORAGE_KEY)
    expect(lostResponse).not.toBeNull()
    if (lostResponse === null) throw new Error('browser did not persist a safe index intent reconciliation record')

    await page.reload({ waitUntil: 'domcontentloaded' })
    await expect(page.getByTestId('code-context-pinned')).toBeVisible()
    await expect(page.getByTestId('index-intent-state')).toHaveText('queued')
    await expect.poll(() => submitRequests.filter((request) => request.requestRef === lostResponse.requestRef).length).toBe(2)
    expect(submitRequests.filter((request) => request.requestRef === lostResponse.requestRef).every((request) => request.kind === lostResponse.kind)).toBe(true)
    durableIntentCount = await countFixtureIntents(fixture, lostResponse.requestRef)
    expect(durableIntentCount).toBe(1)
    reloadRestored = true

    await setFixtureState(fixture, durable.intentRef, 'submitted')
    await checkRenderedState(page, 'submitted', renderedStates)
    await setFixtureState(fixture, durable.intentRef, 'queued')
    await checkRenderedState(page, 'queued', renderedStates)
    await setFixtureState(fixture, durable.intentRef, 'acknowledged')
    await checkRenderedState(page, 'acknowledged', renderedStates)
    await setFixtureState(fixture, durable.intentRef, 'running')
    await checkRenderedState(page, 'running', renderedStates)

    const completed = await completeFixtureIntent(fixture, durable.intentRef)
    const completedStatus = await checkRenderedState(page, 'completed', renderedStates)
    expect(completedStatus.result).not.toBeNull()
    if (completedStatus.result === null) throw new Error('real completed status omitted its released result')
    expect(completedStatus.result).toEqual(completed)
    await expect(page.getByTestId('index-intent-status')).toContainText('New View available')
    await expect(page.getByTestId('index-intent-status')).toContainText('not been selected automatically')
    expect(await page.getByTestId('code-context-pinned').textContent()).toBe(pinnedBeforeCompletion)
    const bodyAfterCompletion = (await page.locator('body').textContent()) ?? ''
    const intentPanelText = (await page.getByTestId('index-intent-status').textContent()) ?? ''
    expect(bodyAfterCompletion).not.toContain(completed.viewRef)
    expect(bodyAfterCompletion).not.toContain('view_ref')
    expect(intentPanelText).not.toContain(completed.viewRef)
    expect(intentPanelText).not.toContain(fixture.backend.baseUrl)
    expect(intentPanelText).not.toContain(fixture.postgres.container)
    expect(intentPanelText).not.toContain(fixture.mcp.clientRoots.a)
    expect(intentPanelText).not.toContain(fixture.browserCredential.password)
    expect(intentPanelText).not.toMatch(/\b(path|credential|daemon owner|environment)\b/i)
    completedResultReleased = true

    const submitsBeforeUnavailable = submitRequests.length
    const unavailable = await submitIntent(page, 'reconcile')
    const unavailableSubmission = submitRequests[submitsBeforeUnavailable]
    if (unavailableSubmission === undefined) throw new Error('browser did not submit the unavailable intent request reference')
    await setFixtureState(fixture, unavailable.intentRef, 'unavailable')
    await checkRenderedState(page, 'unavailable', renderedStates)
    await expect(page.getByTestId('index-intent-retry')).toBeVisible()
    const retryResponsePromise = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return response.request().method() === 'POST' && url.pathname.endsWith(`/index-intents/${unavailable.intentRef}/retry`)
    })
    await page.getByTestId('index-intent-retry').click()
    const retryResponse = await retryResponsePromise
    expect(retryResponse.status()).toBe(202)
    const retry = parseIntentResponse(await retryResponse.json())
    expect(retry).not.toBeNull()
    expect(retry?.intentRef).toBe(unavailable.intentRef)
    expect(retry?.state).not.toBe('completed')
    expect(retry?.result).toBeNull()
    await expect(page.getByTestId('index-intent-state')).toHaveText('queued')
    await setFixtureState(fixture, unavailable.intentRef, 'acknowledged')
    await checkRenderedState(page, 'acknowledged', renderedStates)
    await setFixtureState(fixture, unavailable.intentRef, 'failed')
    await checkRenderedState(page, 'failed', renderedStates)
    await expect(page.getByTestId('index-intent-retry')).toHaveCount(0)
    const submitsBeforeTerminalAction = submitRequests.length
    const renewed = await submitIntent(page, 'reconcile')
    expect(renewed.state).toBe('queued')
    expect(submitRequests).toHaveLength(submitsBeforeTerminalAction + 1)
    const renewedSubmission = submitRequests[submitsBeforeTerminalAction]
    if (renewedSubmission === undefined) throw new Error('browser did not submit a fresh terminal-action request reference')
    expect(renewedSubmission.kind).toBe('reconcile')
    expect(renewedSubmission.requestRef).not.toBe(unavailableSubmission.requestRef)
    expect(await countFixtureIntents(fixture, renewedSubmission.requestRef)).toBe(1)
    expect(await fixtureIntentState(fixture, unavailable.intentRef)).toBe('failed')
    terminalActionCreatedNewIntent = true

    expect(renderedStates).toEqual(expect.arrayContaining([
      'submitted', 'queued', 'acknowledged', 'running', 'completed', 'unavailable', 'failed',
    ]))
    expect(statusHeaderChecks.length).toBeGreaterThan(0)
    expect(statusHeaderChecks.every(Boolean)).toBe(true)
    expect(retryProofBodies).toEqual([true])
    expect(traffic).toEqual(expect.arrayContaining([
      expect.objectContaining({ method: 'POST', path: '/api/code/index-intents', status: 202 }),
      expect.objectContaining({ method: 'GET', path: '/api/code/index-intents/:intent_ref', status: 200 }),
      expect.objectContaining({ method: 'POST', path: '/api/code/index-intents/:intent_ref/retry', status: 202 }),
    ]))
  } finally {
    const state = await appendBrowserTraffic(traffic)
    const evidence = JSON.stringify({
      evidenceKind: 'real-authenticated-go-postgresql-browser-index-intent',
      candidate: state.candidate,
      backend: { sourceCommit: state.backend.sourceCommit, binarySha256: state.backend.binarySha256 },
      browser: { engine: browser.browserType().name(), version: browser.version() },
      browserHTTP: 'real registered Go routes; no page-level API mock',
      fixtureLifecycleControl: 'test-runner-only disposable PostgreSQL fixture; never exposed to the browser',
      renderedStates: [...new Set(renderedStates)],
      responseLossReconciledByRequestRef: reloadRestored,
      durableIntentCount,
      retryOnlyFromUnavailable: retryProofBodies.length === 1 && retryProofBodies.every(Boolean),
      completedResult: completedResultReleased ? 'released by status, withheld from the UI' : 'not reached',
      terminalActionCreatedNewIntent,
      traffic: state.traffic,
    }, null, 2)
    await writeFile(testInfo.outputPath('s4-live-index-intent.json'), evidence, 'utf8')
    await testInfo.attach('s4-live-index-intent', { contentType: 'application/json', body: Buffer.from(evidence) })
    await context.close()
  }
})
