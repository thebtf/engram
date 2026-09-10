import assert from 'node:assert/strict'
import test from 'node:test'
import {
 executeMutation,
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

test('bare successful receipts remain pending until authoritative readback', async () => {
 const result = await parseMutationResponse(request, jsonResponse(200, {
  id: 'rule-1',
 }), parseRuleState)

 assert.equal(result.kind, 'committed_verification_pending')
 assert.equal(result.commitment, 'committed')
 assert.equal(result.reason, 'readback_missing')
 assert.deepEqual(result.request, request)
})

test('unverified receipts retain the request without replaying or fabricating completed state', async () => {
 let transportAttempts = 0
 const response = new Promise<Response>((resolve) => {
  transportAttempts += 1
  resolve(jsonResponse(200, { id: 'rule-1' }))
 })

 const result = await executeMutation(request, response, parseRuleState)

 assert.equal(transportAttempts, 1)
 assert.equal(result.kind, 'committed_verification_pending')
 assert.deepEqual(result.request, request)
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
  [500, 'failed'],
 ] as const

 for (const [status, expectedKind] of scenarios) {
  const result = await parseMutationResponse(request, jsonResponse(status, { code: expectedKind }), parseRuleState)
  assert.equal(result.kind, expectedKind, `HTTP ${status} must remain ${expectedKind}`)
 }
})

test('pre-dispatch failures remain known network or offline outcomes', () => {
 const timeout = parseMutationTransportFailure(request, new DOMException('Request timed out', 'AbortError'))
 const network = parseMutationTransportFailure(request, new TypeError('Failed to construct request'))
 const offline = parseMutationTransportFailure(request, new TypeError('Offline'), {
  stage: 'pre_dispatch',
  failure: 'offline',
 })

 assert.equal(timeout.kind, 'timeout')
 assert.equal(network.kind, 'network')
 assert.equal(offline.kind, 'offline')
 assert.strictEqual(network.request, request)
})

test('an explicit timeout before a response remains timeout after dispatch', async () => {
 const result = await executeMutation(
  request,
  Promise.reject(new DOMException('Request timed out', 'AbortError')),
  parseRuleState,
 )

 assert.equal(result.kind, 'timeout')
 assert.strictEqual(result.request, request)
})

test('a rejected already-dispatched mutation has manual retry intent and unknown commitment', async () => {
 const result = await executeMutation(
  request,
  Promise.reject(new TypeError('Failed to fetch')),
  parseRuleState,
 )

 assert.equal(result.kind, 'outcome_unknown')
 assert.strictEqual(result.request, request)
 assert.strictEqual(result.request.intent, request.intent)
 assert.equal(result.retry, 'manual')
 assert.equal('readback' in result, false)
})

test('post-header body loss leaves commitment unknown without fabricating completion', async () => {
 let bodyReads = 0
 const response = {
  ok: true,
  status: 200,
  text: async () => {
   bodyReads += 1
   throw new TypeError('Response body lost')
  },
 } as unknown as Response

 const result = await executeMutation(request, Promise.resolve(response), parseRuleState)

 assert.equal(bodyReads, 1)
 assert.equal(result.kind, 'outcome_unknown')
 assert.strictEqual(result.request, request)
 assert.equal(result.retry, 'manual')
})

test('invalid successful mutation bodies leave commitment unknown', async () => {
 const result = await parseMutationResponse(request, new Response('{'), parseRuleState)

 assert.equal(result.kind, 'outcome_unknown')
 assert.strictEqual(result.request, request)
 assert.equal(result.retry, 'manual')
})
