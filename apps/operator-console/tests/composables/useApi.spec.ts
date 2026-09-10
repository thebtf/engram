import assert from 'node:assert/strict'
import test from 'node:test'
import {
 executeMutation,
 parseMutationResponse,
 parseMutationTransportFailure,
 type MutationRequest,
 type MutationResult,
} from '../../composables/useApi.ts'
import {
 createRuleCurrentStateParser,
 toggleRuleCurrentStateParser,
 updateRuleCurrentStateParser,
} from '../../composables/useOperatorRules.ts'
import { storeMemoryCurrentStateParser } from '../../composables/useOperatorMemoryLab.ts'

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

function assertMutationKind<
 TIntent,
 TCurrent,
 TKind extends MutationResult<TIntent, TCurrent>['kind'],
>(
 result: MutationResult<TIntent, TCurrent>,
 kind: TKind,
): asserts result is Extract<MutationResult<TIntent, TCurrent>, { kind: TKind }> {
 assert.equal(result.kind, kind)
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

 assertMutationKind(result, 'committed_verified')
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

 assertMutationKind(result, 'committed_verification_pending')
 assert.equal(result.commitment, 'accepted')
 assert.equal(result.operationId, 'operation-10')
})

test('bare successful receipts remain pending until authoritative readback', async () => {
 const result = await parseMutationResponse(request, jsonResponse(200, {
  id: 'rule-1',
 }), parseRuleState)

 assertMutationKind(result, 'committed_verification_pending')
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
 assertMutationKind(result, 'committed_verification_pending')
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

 assertMutationKind(result, 'partial')
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

 assertMutationKind(result, 'timeout')
 assert.strictEqual(result.request, request)
})

test('a rejected already-dispatched mutation has manual retry intent and unknown commitment', async () => {
 const result = await executeMutation(
  request,
  Promise.reject(new TypeError('Failed to fetch')),
  parseRuleState,
 )

 assertMutationKind(result, 'outcome_unknown')
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
 assertMutationKind(result, 'outcome_unknown')
 assert.strictEqual(result.request, request)
 assert.equal(result.retry, 'manual')
})

test('invalid successful mutation bodies leave commitment unknown', async () => {
 const result = await parseMutationResponse(request, new Response('{'), parseRuleState)

 assertMutationKind(result, 'outcome_unknown')
 assert.strictEqual(result.request, request)
 assert.equal(result.retry, 'manual')
})

function directRule(overrides: Record<string, unknown> = {}) {
 return {
  id: 7,
  project: 'engram',
  content: 'Only write through reviewed paths.',
  priority: 10,
  version: 2,
  enabled: true,
  created_at: '2026-09-10T12:00:00.000Z',
  updated_at: '2026-09-10T12:01:00.000Z',
  ...overrides,
 }
}

function directMemory(overrides: Record<string, unknown> = {}) {
 return {
  id: 19,
  project: 'engram',
  content: 'Mutation response proof belongs to the current DTO.',
  tags: ['operator'],
  version: 1,
  created_at: '2026-09-10T12:00:00.000Z',
  updated_at: '2026-09-10T12:01:00.000Z',
  ...overrides,
 }
}

test('strict endpoint parsers verify matching direct current DTOs separately from submitted intent', async () => {
 const createdRule = await parseMutationResponse(
  { requestId: 'rule-create-1', action: 'rule-create', intent: { content: 'Only write through reviewed paths.', priority: 10, project: 'engram' } },
  jsonResponse(201, directRule()),
  createRuleCurrentStateParser({ content: 'Only write through reviewed paths.', priority: 10, project: 'engram' }),
 )
 const updatedRule = await parseMutationResponse(
  { requestId: 'rule-update-1', action: 'rule-update', intent: { id: 7, input: { priority: 10 } } },
  jsonResponse(200, directRule()),
  updateRuleCurrentStateParser(7, { priority: 10 }),
 )
 const toggledRule = await parseMutationResponse(
  { requestId: 'rule-toggle-1', action: 'rule-enable-toggle', intent: { id: 7, enabled: true } },
  jsonResponse(200, directRule()),
  toggleRuleCurrentStateParser(7, true),
 )
 const storedMemory = await parseMutationResponse(
  { requestId: 'memory-store-1', action: 'memory-store', intent: { project: 'engram', content: 'Mutation response proof belongs to the current DTO.', tags: ['operator'] } },
  jsonResponse(201, directMemory()),
  storeMemoryCurrentStateParser({ project: 'engram', content: 'Mutation response proof belongs to the current DTO.', tags: ['operator'] }),
 )

 for (const result of [createdRule, updatedRule, toggledRule, storedMemory]) {
  assertMutationKind(result, 'committed_verified')
  assert.equal(result.readback.kind, 'current')
  assert.notStrictEqual(result.readback.current, result.request.intent)
 }
})

test('direct current DTO identity, domain, and version mismatches remain pending', async () => {
 const results = await Promise.all([
  parseMutationResponse(
   { requestId: 'rule-id-mismatch', action: 'rule-update', intent: { id: 7, input: {} } },
   jsonResponse(200, directRule({ id: 8 })),
   updateRuleCurrentStateParser(7, {}),
  ),
  parseMutationResponse(
   { requestId: 'rule-domain-mismatch', action: 'rule-create', intent: { content: 'Only write through reviewed paths.', priority: 10, project: 'engram' } },
   jsonResponse(201, directRule({ project: 'other-project' })),
   createRuleCurrentStateParser({ content: 'Only write through reviewed paths.', priority: 10, project: 'engram' }),
  ),
  parseMutationResponse(
   { requestId: 'memory-version-mismatch', action: 'memory-store', intent: { project: 'engram', content: 'Mutation response proof belongs to the current DTO.', tags: ['operator'] } },
   jsonResponse(201, directMemory({ version: 0 })),
   storeMemoryCurrentStateParser({ project: 'engram', content: 'Mutation response proof belongs to the current DTO.', tags: ['operator'] }),
  ),
 ])

 for (const result of results) {
  assertMutationKind(result, 'committed_verification_pending')
  assert.equal(result.commitment, 'committed')
 }
})

test('bare, delete, action, and 202 responses remain pending despite direct parser support', async () => {
 const parser = updateRuleCurrentStateParser(7, {})
 const results = await Promise.all([
  parseMutationResponse({ requestId: 'bare', action: 'rule-update', intent: { id: 7, input: {} } }, jsonResponse(200, { id: 7 }), parser),
  parseMutationResponse({ requestId: 'delete', action: 'rule-delete', intent: { id: 7 } }, jsonResponse(200, { deleted: 7 }), parser),
  parseMutationResponse({ requestId: 'action', action: 'memory-suppress', intent: { id: '7' } }, jsonResponse(200, { status: 'suppressed', action: 'suppress', id: 7 }), parser),
  parseMutationResponse({ requestId: 'accepted', action: 'rule-update', intent: { id: 7, input: {} } }, jsonResponse(202, directRule()), parser),
 ])

 for (const result of results) {
  assertMutationKind(result, 'committed_verification_pending')
 }
 assertMutationKind(results[3], 'committed_verification_pending')
})
