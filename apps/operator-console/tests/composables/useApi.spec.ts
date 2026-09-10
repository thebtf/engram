import assert from 'node:assert/strict'
import test from 'node:test'
import {
  parseMutationResponse,
  parseMutationTransportFailure,
  type MutationRequest,
} from '../../composables/useApi.ts'

const request: MutationRequest<{ enabled: boolean; version: number }> = {
  requestId: 'request-42',
  action: 'rule-enable',
  intent: { enabled: true, version: 1 },
}

interface RuleState {
  enabled: boolean
}

function parseRuleState(value: unknown): RuleState | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined
  const state = value as { enabled?: unknown }
  return typeof state.enabled === 'boolean' ? { enabled: state.enabled } : undefined
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

test('completed mutations require an authoritative readback that remains separate from intent', async () => {
  const result = await parseMutationResponse(request, jsonResponse(200, {
    operation_state: 'completed',
    operation_id: 'operation-9',
    readback: {
      authoritative: true,
      kind: 'current',
      current_state: { enabled: false },
      current_version: 2,
      observed_at: '2026-09-10T12:00:00.000Z',
    },
  }), parseRuleState)

  assert.equal(result.kind, 'committed_verified')
  assert.deepEqual(result.request.intent, { enabled: true, version: 1 })
  assert.deepEqual(result.readback, {
    kind: 'current',
    current: { enabled: false },
    version: 2,
    observedAt: '2026-09-10T12:00:00.000Z',
  })
})

test('HTTP acceptance remains pending even when its payload overstates completion', async () => {
  const result = await parseMutationResponse(request, jsonResponse(202, {
    operation_state: 'completed',
    operation_id: 'operation-10',
    readback: {
      authoritative: true,
      kind: 'current',
      current_state: { enabled: true },
    },
  }), parseRuleState)

  assert.equal(result.kind, 'committed_verification_pending')
  assert.equal(result.commitment, 'accepted')
  assert.equal(result.operationId, 'operation-10')
})

test('partial mutation responses preserve each item outcome', async () => {
  const result = await parseMutationResponse(request, jsonResponse(207, {
    operation_state: 'partial',
    item_results: [
      { target_id: 'rule-1', outcome: 'committed' },
      { target_id: 'rule-2', outcome: 'conflict', observed_version: 3 },
    ],
  }), parseRuleState)

  assert.equal(result.kind, 'partial')
  assert.deepEqual(result.items, [
    { targetId: 'rule-1', outcome: 'committed' },
    { targetId: 'rule-2', outcome: 'conflict', observedVersion: 3 },
  ])
})

test('HTTP failure categories remain distinct', async () => {
  const scenarios = [
    [401, 'denied'],
    [409, 'conflict'],
    [422, 'validation_error'],
    [412, 'stale'],
    [501, 'unsupported'],
    [503, 'offline'],
    [408, 'timeout'],
  ] as const

  for (const [status, expectedKind] of scenarios) {
    const result = await parseMutationResponse(request, jsonResponse(status, { code: expectedKind }), parseRuleState)
    assert.equal(result.kind, expectedKind, `HTTP ${status} must remain ${expectedKind}`)
  }
})

test('transport timeout and network loss stay distinct from server failures', () => {
  const timeout = parseMutationTransportFailure(request, new DOMException('Request timed out', 'AbortError'))
  const network = parseMutationTransportFailure(request, new TypeError('Failed to fetch'))

  assert.equal(timeout.kind, 'timeout')
  assert.equal(network.kind, 'network')
})
