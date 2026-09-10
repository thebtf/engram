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

function exportRequest(requestId: string, kind: 'explicit' | 'page' | 'frozen_filter', targets: DocumentTarget[]) {
  return {
    request_id: requestId,
    action: 'export',
    selection: {
      kind,
      selection_version: 1,
      ...(kind === 'frozen_filter' ? { selection_token: 'opaque-selection-token-not-authority' } : {}),
    },
    targets,
  }
}

async function createDocument(page: Page, project: string, path: string, content: string): Promise<DocumentTarget> {
  const created = await requestJSON(page, '/api/documents', { path, project, content, author: 'operator-console' })
  expect(created.status).toBe(201)
  const body = record(created.body, 'document create response')
  expect(body.id).toEqual(expect.any(Number))
  return {
    document_id: body.id as number,
    path,
    project,
    version: 1,
  }
}

test('T030 live Documents export rechecks selected versions with safe result status', async ({ page }, testInfo) => {
  const fixture = await readLiveFixture()
  const traffic: RouteTraffic[] = []
  const marker = `t030-${fixture.fixtureId}`
  const project = `${marker}-project`
  const privateContent = `${marker} document body must not be reported as operation status`

  page.on('response', (response) => {
    const url = new URL(response.url())
    if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/')) {
      traffic.push({
        origin: 'browser',
        method: response.request().method(),
        path: url.pathname,
        status: response.status(),
        at: new Date().toISOString(),
      })
    }
  })

  try {
    expect(fixture.candidate.commit).toBe(fixture.backend.sourceCommit)
    expect(fixture.mock.prohibited).toBe(true)
    const shell = await page.goto(`${fixture.frontend.baseUrl}/documents`, { waitUntil: 'domcontentloaded' })
    expect(shell?.status()).toBe(200)

    const unauthenticated = await requestJSON(page, '/api/documents', exportRequest(`${marker}-denied`, 'frozen_filter', [{
      document_id: 1,
      path: 'forbidden.md',
      project,
      version: 1,
    }]), `${marker}-denied`)
    expect(unauthenticated.status).toBe(401)
    expect(JSON.stringify(unauthenticated.body)).not.toContain('forbidden.md')

    const login = await requestJSON(page, '/api/auth/user-login', {
      email: fixture.browserCredential.email,
      password: fixture.browserCredential.password,
    })
    expect(login.status).toBe(200)

    const primary = await createDocument(page, project, `${marker}/primary.md`, privateContent)
    const secondary = await createDocument(page, project, `${marker}/secondary.md`, `${marker} second document body`)

    for (const kind of ['explicit', 'page', 'frozen_filter'] as const) {
      const requestId = `${marker}-${kind}`
      const exported = await requestJSON(page, '/api/documents', exportRequest(requestId, kind, [primary]), requestId)
      expect(exported.status).toBe(200)
      const body = record(exported.body, `${kind} export response`)
      expect(body.operation_state).toBe('completed')
      expect(body.request_id).toBe(requestId)
      expect(body.item_results).toEqual([{ target_id: primary.document_id, outcome: 'committed', observed_version: primary.version }])
      expect(body.readback).toEqual({ authoritative: true, kind: 'non_disclosing', operation_status: 'export_ready' })
      expect(JSON.stringify(body)).not.toContain(privateContent)
      expect(body).not.toHaveProperty('content')

      const outcome = await parseMutationResponse(
        { requestId, action: 'document-export', intent: { kind, target: primary.document_id } },
        responseFrom(exported),
        () => undefined,
      )
      expect(outcome.kind).toBe('committed_verified')
      if (outcome.kind !== 'committed_verified' || outcome.readback.kind !== 'non_disclosing') {
        throw new Error(`Documents ${kind} export was not verified non-disclosing status`)
      }
      expect(outcome.readback.operationStatus).toBe('export_ready')
    }

    const missing: DocumentTarget = {
      document_id: secondary.document_id + 10_000,
      path: `${marker}/missing.md`,
      project,
      version: 1,
    }
    const partialRequestId = `${marker}-partial`
    const partial = await requestJSON(page, '/api/documents', exportRequest(partialRequestId, 'explicit', [secondary, missing]), partialRequestId)
    expect(partial.status).toBe(207)
    const partialBody = record(partial.body, 'partial export response')
    expect(partialBody.operation_state).toBe('partial')
    expect(partialBody.item_results).toEqual([
      { target_id: secondary.document_id, outcome: 'committed', observed_version: secondary.version },
      { target_id: missing.document_id, outcome: 'conflict' },
    ])
    expect(JSON.stringify(partialBody)).not.toContain(privateContent)
    const partialOutcome = await parseMutationResponse(
      { requestId: partialRequestId, action: 'document-export', intent: { targetIds: [secondary.document_id, missing.document_id] } },
      responseFrom(partial),
      () => undefined,
    )
    expect(partialOutcome.kind).toBe('partial')
    if (partialOutcome.kind === 'partial') {
      expect(partialOutcome.items).toEqual([
        { targetId: secondary.document_id, outcome: 'committed', observedVersion: secondary.version },
        { targetId: missing.document_id, outcome: 'conflict' },
      ])
    }

    const staleRequestId = `${marker}-version-conflict`
    const stale = await requestJSON(page, '/api/documents', exportRequest(staleRequestId, 'page', [{ ...primary, version: primary.version + 1 }]), staleRequestId)
    expect(stale.status).toBe(409)
    const staleBody = record(stale.body, 'stale export response')
    expect(staleBody.operation_state).toBe('failed')
    expect(staleBody.item_results).toEqual([{ target_id: primary.document_id, outcome: 'conflict' }])
    expect(JSON.stringify(staleBody)).not.toContain(privateContent)
  } finally {
    const state = await appendBrowserTraffic(traffic)
    await testInfo.attach('t030-documents-selection-operation-live', {
      contentType: 'application/json',
      body: Buffer.from(JSON.stringify({
        evidenceKind: 'real-authenticated-go-postgresql-browser',
        candidate: state.candidate,
        backend: { sourceCommit: state.backend.sourceCommit, binarySha256: state.backend.binarySha256 },
        frontend: { buildEntry: state.frontend.buildEntry, buildEntrySha256: state.frontend.buildEntrySha256 },
        assertedSafetyBranches: [
          'unauthenticated frozen token does not authorize export',
          'explicit page and frozen selection modes recheck exact document versions',
          'safe non-disclosing completed status',
          'partial committed and conflict item outcomes',
          'version conflict after selected version is unavailable',
        ],
        traffic: state.traffic,
      }, null, 2)),
    })
  }
})
