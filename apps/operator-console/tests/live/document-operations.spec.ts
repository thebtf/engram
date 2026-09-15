import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { parseMutationResponse } from '../../composables/useApi'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { RouteTraffic } from './fixture-bootstrap'

type BrowserResponse = {
  status: number
  body: unknown
}

type DocumentTarget = {
  document_id: number
  path: string
  project: string
  version: number
}

function record(value: unknown, label: string): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`${label} must be a JSON object`)
  }
  return value as Record<string, unknown>
}

async function requestJSON(page: Page, path: string, body: unknown, requestId?: string): Promise<BrowserResponse> {
  const response = await page.evaluate(async ({ requestBody, requestId, requestPath }) => {
    const result = await fetch(requestPath, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(requestId ? { 'X-Engram-Request-ID': requestId } : {}),
      },
      body: JSON.stringify(requestBody),
    })
    return { status: result.status, text: await result.text() }
  }, { requestBody: body, requestId, requestPath: path })

  try {
    return { status: response.status, body: response.text === '' ? null : JSON.parse(response.text) }
  } catch {
    return { status: response.status, body: response.text }
  }
}

function responseFrom(response: BrowserResponse): Response {
  return new Response(JSON.stringify(response.body), {
    status: response.status,
    headers: { 'content-type': 'application/json' },
  })
}

function operationSelection(snapshot: Record<string, unknown>) {
  const kind = snapshot.kind
  const version = snapshot.selection_version
  if ((kind !== 'explicit' && kind !== 'page' && kind !== 'frozen_filter') || typeof version !== 'number') {
    throw new Error('Documents selection snapshot is invalid')
  }
  if (kind === 'frozen_filter') {
    const token = snapshot.selection_token
    if (typeof token !== 'string') throw new Error('Documents frozen selection token is missing')
    return { kind, selection_version: version, selection_token: token }
  }
  return { kind, selection_version: version }
}

function selectionTargets(snapshot: Record<string, unknown>): DocumentTarget[] {
  const targets = snapshot.targets
  if (!Array.isArray(targets)) throw new Error('Documents selection membership is missing')
  const excluded = new Set(Array.isArray(snapshot.excluded_ids) ? snapshot.excluded_ids : [])
  return targets.flatMap((target) => {
    const row = record(target, 'Documents selection target')
    if (typeof row.id !== 'string' || typeof row.expected_version !== 'number' || excluded.has(row.id)) return []
    return [{ document_id: Number(row.id), path: '', project: '', version: row.expected_version }]
  })
}

async function saveDocumentSelection(page: Page, requestId: string, selection: Record<string, unknown>): Promise<Record<string, unknown>> {
  const saved = await requestJSON(page, '/api/documents/selection', { domain: 'documents', selection }, requestId)
  expect(saved.status).toBe(200)
  return record(record(saved.body, 'Documents selection response').selection, 'Documents selection')
}

function exportRequest(requestId: string, selection: Record<string, unknown>) {
  return { request_id: requestId, action: 'export', selection: operationSelection(selection) }
}

async function createDocument(page: Page, project: string, path: string, content: string): Promise<DocumentTarget> {
  const created = await requestJSON(page, '/api/documents', { path, project, content, author: 'operator-console' })
  expect(created.status).toBe(201)
  const body = record(created.body, 'document create response')
  expect(body.id).toEqual(expect.any(Number))
  return { document_id: body.id as number, path, project, version: 1 }
}

test('T030 live Documents export resolves server selection and downloads a non-secret artifact', async ({ page }, testInfo) => {
  const fixture = await readLiveFixture()
  const traffic: RouteTraffic[] = []
  const marker = `t030-${fixture.fixtureId}`
  const project = `${marker}-project`
  const privateContent = `${marker} document body must not be reported as operation status or artifact`

  page.on('response', (response) => {
    const url = new URL(response.url())
    if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/')) {
      traffic.push({ origin: 'browser', method: response.request().method(), path: url.pathname, status: response.status(), at: new Date().toISOString() })
    }
  })

  try {
    expect(fixture.candidate.commit).toBe(fixture.backend.sourceCommit)
    expect(fixture.mock.prohibited).toBe(true)
    const shell = await page.goto(`${fixture.frontend.baseUrl}/documents`, { waitUntil: 'domcontentloaded' })
    expect(shell?.status()).toBe(200)

    const unauthenticated = await requestJSON(page, '/api/documents', exportRequest(`${marker}-denied`, {
      kind: 'frozen_filter', selection_version: 1, selection_token: 'b857ebf7-c1cf-4a1f-a733-465ee492d3cb',
    }), `${marker}-denied`)
    expect(unauthenticated.status).toBe(401)

    const login = await requestJSON(page, '/api/auth/user-login', { email: fixture.browserCredential.email, password: fixture.browserCredential.password })
    expect(login.status).toBe(200)

    const primary = await createDocument(page, project, `${marker}/primary.md`, privateContent)
    const secondary = await createDocument(page, project, `${marker}/secondary.md`, `${marker} second document body`)
    const pageResponse = await requestJSON(page, '/api/documents/selection/page', { project, limit: 1 }, `${marker}-page`)
    expect(pageResponse.status).toBe(200)
    const pageSnapshot = record(pageResponse.body, 'Documents page')
    const pageCursor = pageSnapshot.cursor
    if (typeof pageCursor !== 'string') throw new Error('Documents page cursor is missing')

    const selectionInputs = [
      { name: 'explicit', selection: { kind: 'explicit', targets: [{ id: String(primary.document_id), expected_version: primary.version }] } },
      { name: 'page', selection: { kind: 'page', cursor: pageCursor } },
      { name: 'frozen', selection: { kind: 'frozen_filter', filter: { scope: project }, excluded_ids: [String(secondary.document_id)] } },
    ]
    let firstSelection: Record<string, unknown> | undefined
    for (const input of selectionInputs) {
      const selection = await saveDocumentSelection(page, `${marker}-${input.name}`, input.selection)
      if (firstSelection === undefined) firstSelection = selection
      const requestId = `${marker}-${input.name}-export`
      const expected = selectionTargets(selection)
      const exported = await requestJSON(page, '/api/documents', exportRequest(requestId, selection), requestId)
      expect(exported.status).toBe(200)
      const body = record(exported.body, 'Documents export response')
      expect(body.operation_state).toBe('completed')
      expect(body.item_results).toEqual(expected.map((target) => ({ target_id: target.document_id, outcome: 'committed', observed_version: target.version })))
      expect(body.readback).toEqual({ authoritative: true, kind: 'non_disclosing', operation_status: 'export_ready' })
      const artifact = record(body.artifact, 'Documents export artifact')
      expect(artifact.filename).toEqual(expect.stringMatching(/^documents-export-[0-9a-f-]+\.json$/))
      expect(JSON.stringify(body)).not.toContain(privateContent)

      const downloaded = await page.evaluate(async (downloadURL) => {
        const response = await fetch(downloadURL)
        return { status: response.status, type: response.headers.get('content-type'), disposition: response.headers.get('content-disposition'), text: await response.text() }
      }, artifact.download_url)
      expect(downloaded.status).toBe(200)
      expect(downloaded.type).toContain('application/json')
      expect(downloaded.disposition).toContain('attachment')
      const payload = record(JSON.parse(downloaded.text), 'Documents export file')
      expect(payload.schema_version).toBe('engram.documents.export/v1')
      expect(payload.documents).toHaveLength(expected.length)
      expect(downloaded.text).not.toContain(privateContent)

      const outcome = await parseMutationResponse({ requestId, action: 'document-export', intent: { kind: selection.kind } }, responseFrom(exported), () => undefined)
      expect(outcome.kind).toBe('committed_verified')
    }

    if (firstSelection === undefined) throw new Error('Documents explicit selection was not saved')
    const staleRequestId = `${marker}-stale`
    await saveDocumentSelection(page, `${marker}-replace`, { kind: 'explicit', targets: [{ id: String(secondary.document_id), expected_version: secondary.version }] })
    const stale = await requestJSON(page, '/api/documents', exportRequest(staleRequestId, firstSelection), staleRequestId)
    expect(stale.status).toBe(409)
    expect(record(stale.body, 'stale export response').operation_state).toBe('failed')
    expect(JSON.stringify(stale.body)).not.toContain(privateContent)
  } finally {
    const state = await appendBrowserTraffic(traffic)
    await testInfo.attach('t030-documents-selection-operation-live', {
      contentType: 'application/json',
      body: Buffer.from(JSON.stringify({
        evidenceKind: 'real-authenticated-go-postgresql-browser',
        candidate: state.candidate,
        backend: { sourceCommit: state.backend.sourceCommit, binarySha256: state.backend.binarySha256 },
        frontend: { buildEntry: state.frontend.buildEntry, buildEntrySha256: state.frontend.buildEntrySha256 },
        assertedSafetyBranches: ['unauthenticated selection does not authorize export', 'explicit page and frozen selection modes resolve server membership', 'completed export carries a user-receivable metadata artifact', 'replaced selection version is rejected'],
        traffic: state.traffic,
      }, null, 2)),
    })
  }
})
