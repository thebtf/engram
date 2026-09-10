import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { RouteTraffic } from './fixture-bootstrap'

type BrowserResponse = {
  status: number
  text: string
}

type MemoryState = {
  id: number
  version: number
  status: string
}

type Selection = {
  kind: 'explicit' | 'page' | 'frozen_filter'
  version: number
  token?: string
}

async function requestJSON(page: Page, path: string, method: 'POST', body: unknown, requestID?: string): Promise<BrowserResponse> {
  return page.evaluate(async ({ requestBody, requestID, requestMethod, requestPath }) => {
    const response = await fetch(requestPath, {
      method: requestMethod,
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
        ...(requestID === undefined ? {} : { 'X-Engram-Request-ID': requestID }),
      },
      body: JSON.stringify(requestBody),
    })
    return { status: response.status, text: await response.text() }
  }, { requestBody: body, requestID, requestMethod: method, requestPath: path })
}

function parseJSON<T>(response: BrowserResponse): T {
  return JSON.parse(response.text) as T
}

async function createMemory(page: Page, project: string, content: string): Promise<MemoryState> {
  const response = await requestJSON(page, '/api/memories', 'POST', { project, content, source_agent: 'operator-console-live' })
  expect(response.status).toBe(201)
  const memory = parseJSON<MemoryState>(response)
  expect(memory.id).toBeGreaterThan(0)
  expect(memory.version).toBeGreaterThan(0)
  expect(memory.status).toBe('active')
  return memory
}

async function selectExplicit(page: Page, target: MemoryState): Promise<Selection> {
  const response = await requestJSON(page, '/api/memories/selection', 'POST', {
    selection: {
      kind: 'explicit',
      targets: [{ id: String(target.id), expected_version: target.version }],
    },
  })
  expect(response.status).toBe(200)
  const body = parseJSON<{ selection: { kind: Selection['kind']; selection_version: number; selection_token?: string } }>(response)
  expect(body.selection.kind).toBe('explicit')
  expect(body.selection.selection_version).toBeGreaterThan(0)
  return { kind: body.selection.kind, version: body.selection.selection_version }
}

async function selectCurrentPage(page: Page, project: string): Promise<Selection> {
  const pageResponse = await requestJSON(page, '/api/memories/selection/page', 'POST', { project, limit: 1 })
  expect(pageResponse.status).toBe(200)
  const pageBody = parseJSON<{ cursor: string; targets: Array<{ id: string; expected_version: number }>; total: number }>(pageResponse)
  expect(pageBody.total).toBe(1)
  expect(pageBody.targets).toHaveLength(1)

  const response = await requestJSON(page, '/api/memories/selection', 'POST', {
    selection: { kind: 'page', cursor: pageBody.cursor },
  })
  expect(response.status).toBe(200)
  const body = parseJSON<{ selection: { kind: 'page'; selection_version: number } }>(response)
  expect(body.selection.selection_version).toBeGreaterThan(0)
  return { kind: body.selection.kind, version: body.selection.selection_version }
}

async function selectFrozenProject(page: Page, project: string): Promise<Selection> {
  const response = await requestJSON(page, '/api/memories/selection', 'POST', {
    selection: { kind: 'frozen_filter', project },
  })
  expect(response.status).toBe(200)
  const body = parseJSON<{ selection: { kind: 'frozen_filter'; selection_version: number; selection_token: string } }>(response)
  expect(body.selection.selection_token).not.toBe('')
  return { kind: body.selection.kind, version: body.selection.selection_version, token: body.selection.selection_token }
}

async function operate(page: Page, action: 'suppress' | 'unsuppress' | 'archive', selection: Selection): Promise<BrowserResponse> {
  const requestID = `t028-${action}-${crypto.randomUUID()}`
  return requestJSON(page, '/api/memories/operations', 'POST', {
    request_id: requestID,
    action,
    selection: {
      kind: selection.kind,
      selection_version: selection.version,
      ...(selection.token === undefined ? {} : { selection_token: selection.token }),
    },
  }, requestID)
}

test('T028 live Memory selection actions preserve privacy and mutation truth', async ({ page }, testInfo) => {
  const fixture = await readLiveFixture()
  const traffic: RouteTraffic[] = []
  const marker = `t028-${fixture.fixtureId}`
  const project = `${marker}-memory`
  const privateContent = `${marker} must never be disclosed after selection access loss`

  page.on('response', (response) => {
    const url = new URL(response.url())
    if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/')) {
      traffic.push({ origin: 'browser', method: response.request().method(), path: url.pathname, status: response.status(), at: new Date().toISOString() })
    }
  })

  try {
    expect(fixture.candidate.commit).toBe(fixture.backend.sourceCommit)
    expect((await page.goto(`${fixture.frontend.baseUrl}/memory`, { waitUntil: 'domcontentloaded' }))?.status()).toBe(200)
    expect((await requestJSON(page, '/api/auth/user-login', 'POST', fixture.browserCredential)).status).toBe(200)

    const suppressTarget = await createMemory(page, project, `${marker} suppress target`)
    const suppressSelection = await selectExplicit(page, suppressTarget)
    const suppressed = await operate(page, 'suppress', suppressSelection)
    expect(suppressed.status).toBe(200)
    expect(suppressed.text).not.toContain(`${marker} suppress target`)
    const suppressResult = parseJSON<{ operation_state: string; readback: { kind: string; current_state: Array<{ id: number; status: string; version: number }> } }>(suppressed)
    expect(suppressResult.operation_state).toBe('completed')
    expect(suppressResult.readback.kind).toBe('current')
    expect(suppressResult.readback.current_state).toEqual([{ id: suppressTarget.id, status: 'flagged', version: suppressTarget.version + 1 }])

    const unsuppressTarget = { ...suppressTarget, status: 'flagged', version: suppressTarget.version + 1 }
    const unsuppressSelection = await selectExplicit(page, unsuppressTarget)
    const unsuppressed = await operate(page, 'unsuppress', unsuppressSelection)
    expect(unsuppressed.status).toBe(200)
    expect(parseJSON<{ readback: { current_state: Array<{ status: string }> } }>(unsuppressed).readback.current_state[0]?.status).toBe('active')

    const archiveTarget = { ...suppressTarget, status: 'active', version: suppressTarget.version + 2 }
    const archiveSelection = await selectExplicit(page, archiveTarget)
    const archived = await operate(page, 'archive', archiveSelection)
    expect(archived.status).toBe(200)
    expect(parseJSON<{ readback: { current_state: Array<{ status: string }> } }>(archived).readback.current_state[0]?.status).toBe('archived')

    const pageTarget = await createMemory(page, project, `${marker} current page target`)
    const pageSelection = await selectCurrentPage(page, project)
    const pageSuppressed = await operate(page, 'suppress', pageSelection)
    expect(pageSuppressed.status).toBe(200)
    expect(parseJSON<{ readback: { current_state: Array<{ id: number; status: string; version: number }> } }>(pageSuppressed).readback.current_state).toEqual([
      { id: pageTarget.id, status: 'flagged', version: pageTarget.version + 1 },
    ])

    const conflictTarget = await createMemory(page, project, `${marker} conflict target`)
    const conflictSelection = await selectExplicit(page, conflictTarget)
    expect((await operate(page, 'suppress', conflictSelection)).status).toBe(200)
    const staleConflict = await operate(page, 'suppress', conflictSelection)
    expect(staleConflict.status).toBe(207)
    expect(staleConflict.text).not.toContain(String(conflictTarget.id))
    expect(parseJSON<{ operation_state: string; item_results: Array<{ target_id: string; outcome: string }> }>(staleConflict)).toEqual({
      operation_state: 'partial',
      item_results: [{ target_id: 'redacted', outcome: 'conflict' }],
    })

    const accessLossTarget = await createMemory(page, project, privateContent)
    const frozenSelection = await selectFrozenProject(page, project)
    expect((await requestJSON(page, '/api/auth/user-logout', 'POST', {})).status).toBe(200)
    expect((await requestJSON(page, '/api/auth/user-login', 'POST', fixture.browserCredentialB)).status).toBe(200)
    const denied = await operate(page, 'archive', frozenSelection)
    expect(denied.status).toBe(403)
    expect(denied.text).not.toContain(String(accessLossTarget.id))
    expect(denied.text).not.toContain(privateContent)

    expect((await operate(page, 'suppress', frozenSelection)).status).toBe(403)
    expect(traffic.some((entry) => entry.path.toLowerCase().includes('book'))).toBe(false)
  } finally {
    const state = await appendBrowserTraffic(traffic)
    await testInfo.attach('t028-memory-selection-live', {
      contentType: 'application/json',
      body: Buffer.from(JSON.stringify({
        evidenceKind: 'real-authenticated-go-postgresql-browser',
        candidate: state.candidate,
        backend: { sourceCommit: state.backend.sourceCommit, binarySha256: state.backend.binarySha256 },
        frontend: { buildEntry: state.frontend.buildEntry, buildEntrySha256: state.frontend.buildEntrySha256 },
        assertedSafetyBranches: [
          'selected suppress, unsuppress, and archive use current status/version readbacks',
          'current-page selection resolves server-issued membership before suppressing its selected version',
          'frozen selection token is non-authorizing after browser-subject access loss',
          'denied operation returns no selected row identifier or content',
          'no Book endpoint traffic',
        ],
        traffic: state.traffic,
      }, null, 2)),
    })
  }
})
