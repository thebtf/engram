import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { RouteTraffic } from './fixture-bootstrap'

type BrowserResponse = {
  status: number
  body: unknown
}

type Candidate = {
  id: number
  status: string
}

type Selection = {
  kind: 'explicit' | 'page' | 'frozen_filter'
  selection_version: number
  selection_token?: string
}

function object(value: unknown, label: string): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error(`${label} must be an object`)
  }
  return value as Record<string, unknown>
}

async function requestJSON(page: Page, path: string, body: unknown, requestId: string): Promise<BrowserResponse> {
  return page.evaluate(async ({ requestBody, requestId: operationRequestID, requestPath }) => {
    const response = await fetch(requestPath, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Engram-Request-ID': operationRequestID,
      },
      body: JSON.stringify(requestBody),
    })
    const text = await response.text()
    let responseBody: unknown = text
    try {
      responseBody = text === '' ? null : JSON.parse(text)
    } catch {
      // The test asserts only the public status for rejected operations.
    }
    return { status: response.status, body: responseBody }
  }, { requestBody: body, requestId, requestPath: path })
}

async function listPendingCandidates(page: Page): Promise<Candidate[]> {
  const response = await page.evaluate(async () => {
    const result = await fetch('/api/memory/candidates?project=all&status=pending&limit=100')
    return { status: result.status, body: await result.json() }
  })
  expect(response.status).toBe(200)
  const candidates = object(response.body, 'candidate list').candidates
  expect(Array.isArray(candidates)).toBe(true)
  return (candidates as unknown[]).map((candidate) => {
    const row = object(candidate, 'candidate row')
    expect(typeof row.id).toBe('number')
    expect(row.status).toBe('pending')
    return { id: row.id as number, status: row.status as string }
  })
}

async function saveSelection(page: Page, ids: number[], requestId: string): Promise<Selection> {
  const response = await requestJSON(page, '/api/collections/selection', {
    domain: 'queue',
    selection: {
      kind: 'explicit',
      targets: ids.map((id) => ({ id: String(id) })),
    },
  }, requestId)
  expect(response.status).toBe(200)
  const selection = object(response.body, 'selection response').selection
  const parsed = object(selection, 'selection')
  expect(parsed.kind).toBe('explicit')
  expect(typeof parsed.selection_version).toBe('number')
  return {
    kind: parsed.kind as Selection['kind'],
    selection_version: parsed.selection_version as number,
  }
}

async function operate(page: Page, action: 'promote' | 'reject' | 'supersede', selection: Selection, requestId: string): Promise<BrowserResponse> {
  return requestJSON(page, '/api/memory/candidates/operations', {
    request_id: requestId,
    action,
    selection,
    ...(action === 'reject' ? { reason: 'fixture rejection' } : {}),
  }, requestId)
}

test('T029 live Queue actions re-resolve selected candidates without review controls', async ({ page }, testInfo) => {
  const fixture = await readLiveFixture()
  const traffic: RouteTraffic[] = []
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
    const queue = await page.goto(`${fixture.frontend.baseUrl}/queue`, { waitUntil: 'domcontentloaded' })
    expect(queue?.status()).toBe(200)
    expect((await requestJSON(page, '/api/auth/user-login', fixture.browserCredential, 'queue-login')).status).toBe(200)

    const candidates = await listPendingCandidates(page)
    expect(candidates.length).toBeGreaterThanOrEqual(5)

    for (const [offset, action] of (['promote', 'reject', 'supersede'] as const).entries()) {
      const selection = await saveSelection(page, [candidates[offset].id], `queue-${action}-selection`)
      const response = await operate(page, action, selection, `queue-${action}-operation`)
      expect(response.status).toBe(200)
      const result = object(response.body, `${action} result`)
      expect(result.operation_state).toBe('completed')
      const items = result.item_results
      expect(items).toEqual([{
        target_id: candidates[offset].id,
        outcome: 'committed',
        candidate_status: action === 'promote' ? 'promoted' : action === 'reject' ? 'rejected' : 'superseded',
      }])
      expect(result).not.toHaveProperty('review_packet')
      expect(result).not.toHaveProperty('proposed_content')
      expect(result).not.toHaveProperty('lifecycle')
      expect(result).not.toHaveProperty('ingestion')
    }

    const committed = await saveSelection(page, [candidates[4].id], 'queue-conflict-commit-selection')
    expect((await operate(page, 'promote', committed, 'queue-conflict-commit-operation')).status).toBe(200)
    const mixedSelection = await saveSelection(page, [candidates[3].id, candidates[4].id], 'queue-mixed-selection')
    const mixed = await operate(page, 'promote', mixedSelection, 'queue-mixed-operation')
    expect(mixed.status).toBe(207)
    const mixedResult = object(mixed.body, 'mixed Queue result')
    expect(mixedResult.operation_state).toBe('partial')
    expect(mixedResult.item_results).toEqual([
      { target_id: candidates[3].id, outcome: 'committed', candidate_status: 'promoted' },
      { target_id: candidates[4].id, outcome: 'conflict' },
    ])
    expect(JSON.stringify(mixedResult)).not.toContain('review_packet')
    expect(JSON.stringify(mixedResult)).not.toContain('proposed_content')

    expect(traffic.some((entry) => entry.path.includes('/review-packets/'))).toBe(false)
    expect(traffic.some((entry) => entry.path.includes('lifecycle') || entry.path.includes('ingestion'))).toBe(false)
  } finally {
    const state = await appendBrowserTraffic(traffic)
    await testInfo.attach('t029-queue-selection-actions-live', {
      contentType: 'application/json',
      body: Buffer.from(JSON.stringify({
        evidenceKind: 'real-authenticated-go-postgresql-browser',
        candidate: state.candidate,
        backend: { sourceCommit: state.backend.sourceCommit, binarySha256: state.backend.binarySha256 },
        assertedBranches: [
          'candidate promote reject supersede',
          'selection re-resolution conflict after terminal transition',
          'per-item status without candidate content',
          'no review-packet or lifecycle operator control traffic',
        ],
        traffic: state.traffic,
      }, null, 2)),
    })
  }
})
