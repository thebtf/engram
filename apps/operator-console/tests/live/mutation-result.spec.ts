import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { parseMutationResponse } from '../../composables/useApi'
import { createRuleCurrentStateParser } from '../../composables/useOperatorRules'
import { storeMemoryCurrentStateParser } from '../../composables/useOperatorMemoryLab'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { RouteTraffic } from './fixture-bootstrap'

interface BrowserApiResponse {
  status: number
  body: unknown
}

type JsonRecord = Record<string, unknown>

function record(value: unknown, label: string): JsonRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error(`${label} must be a JSON object: ${JSON.stringify(value)}`)
  }
  return value as JsonRecord
}

function expectRFC3339(value: unknown, label: string): void {
  expect(typeof value, label).toBe('string')
  expect(value as string, label).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/)
  expect(Number.isNaN(Date.parse(value as string)), label).toBe(false)
}

function expectDirectRule(value: unknown, content: string, priority: number): JsonRecord {
  const body = record(value, 'rule response')
  expect(body.id).toEqual(expect.any(Number))
  expect(Number.isSafeInteger(body.id as number)).toBe(true)
  expect(body.id as number).toBeGreaterThan(0)
  expect(body.project === undefined || body.project === null).toBe(true)
  expect(body.content).toBe(content)
  expect(body.priority).toBe(priority)
  expect(body.version).toEqual(expect.any(Number))
  expect(body.version as number).toBeGreaterThan(0)
  expect(body.enabled).toBe(true)
  expectRFC3339(body.created_at, 'rule created_at')
  expectRFC3339(body.updated_at, 'rule updated_at')
  return body
}

function expectDirectMemory(value: unknown, project: string, content: string): JsonRecord {
  const body = record(value, 'memory response')
  expect(body.id).toEqual(expect.any(Number))
  expect(Number.isSafeInteger(body.id as number)).toBe(true)
  expect(body.id as number).toBeGreaterThan(0)
  expect(body.project).toBe(project)
  expect(body.content).toBe(content)
  expect(body.version).toEqual(expect.any(Number))
  expect(body.version as number).toBeGreaterThan(0)
  expectRFC3339(body.created_at, 'memory created_at')
  expectRFC3339(body.updated_at, 'memory updated_at')
  return body
}

async function requestJSON(page: Page, path: string, method: 'GET' | 'POST', body?: unknown): Promise<BrowserApiResponse> {
  const response = await page.evaluate(async ({ requestBody, requestMethod, requestPath }) => {
    const init: RequestInit = requestBody === undefined
      ? { method: requestMethod }
      : {
        method: requestMethod,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(requestBody),
      }
    const result = await fetch(requestPath, init)
    return { status: result.status, text: await result.text() }
  }, { requestBody: body, requestMethod: method, requestPath: path })

  try {
    return { status: response.status, body: response.text === '' ? null : JSON.parse(response.text) }
  } catch {
    return { status: response.status, body: response.text }
  }
}

function responseFrom(result: BrowserApiResponse): Response {
  return new Response(JSON.stringify(result.body), {
    status: result.status,
    headers: { 'content-type': 'application/json' },
  })
}

test('T011 live acceptance: current Rule and Memory DTOs classify as verified', async ({ page }, testInfo) => {
  const fixture = await readLiveFixture()
  const traffic: RouteTraffic[] = []
  const marker = `t011-${fixture.fixtureId}`
  const evidence: Record<string, unknown> = {}

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

    const shell = await page.goto(`${fixture.frontend.baseUrl}/rules`, { waitUntil: 'domcontentloaded' })
    expect(shell?.status()).toBe(200)

    const login = await requestJSON(page, '/api/auth/user-login', 'POST', {
      email: fixture.browserCredential.email,
      password: fixture.browserCredential.password,
    })
    expect(login.status).toBe(200)

    const rule = {
      content: `T011 verified rule ${marker}`,
      priority: 37,
    }
    const ruleResponse = await requestJSON(page, '/api/rules', 'POST', {
      ...rule,
      edited_by: 'operator-console',
    })
    expect(ruleResponse.status).toBe(201)
    const ruleBody = expectDirectRule(ruleResponse.body, rule.content, rule.priority)
    const ruleOutcome = await parseMutationResponse(
      { requestId: `${marker}-rule`, action: 'rule-create', intent: rule },
      responseFrom(ruleResponse),
      createRuleCurrentStateParser(rule),
    )
    expect(ruleOutcome.kind).toBe('committed_verified')
    if (ruleOutcome.kind !== 'committed_verified' || ruleOutcome.readback.kind !== 'current') {
      throw new Error(`rule outcome was ${ruleOutcome.kind}`)
    }
    expect(ruleOutcome.readback.current).not.toBe(ruleOutcome.request.intent)

    const memory = {
      project: `t011-live-${fixture.fixtureId}`,
      content: `T011 verified memory ${marker}`,
      tags: ['t011'],
    }
    const memoryResponse = await requestJSON(page, '/api/memories', 'POST', {
      ...memory,
      source_agent: 'operator-console',
    })
    expect(memoryResponse.status).toBe(201)
    const memoryBody = expectDirectMemory(memoryResponse.body, memory.project, memory.content)
    const memoryOutcome = await parseMutationResponse(
      { requestId: `${marker}-memory`, action: 'memory-store', intent: memory },
      responseFrom(memoryResponse),
      storeMemoryCurrentStateParser(memory),
    )
    expect(memoryOutcome.kind).toBe('committed_verified')
    if (memoryOutcome.kind !== 'committed_verified' || memoryOutcome.readback.kind !== 'current') {
      throw new Error(`memory outcome was ${memoryOutcome.kind}`)
    }
    expect(memoryOutcome.readback.current).not.toBe(memoryOutcome.request.intent)

    evidence.rule = { status: ruleResponse.status, fields: Object.keys(ruleBody).sort() }
    evidence.memory = { status: memoryResponse.status, fields: Object.keys(memoryBody).sort() }
  } finally {
    const state = await appendBrowserTraffic(traffic)
    await testInfo.attach('t011-live-mutation-result', {
      contentType: 'application/json',
      body: Buffer.from(JSON.stringify({
        candidate: state.candidate,
        backend: { sourceCommit: state.backend.sourceCommit, binarySha256: state.backend.binarySha256 },
        frontend: { buildEntry: state.frontend.buildEntry, buildEntrySha256: state.frontend.buildEntrySha256 },
        traffic: state.traffic,
        evidence,
      }, null, 2)),
    })
  }
})
