import assert from 'node:assert/strict'
import test from 'node:test'
import {
 executeMutation,
 parseMutationResponse,
 parseMutationTransportFailure,
 type MutationRequest,
 type MutationResult,
} from '../../composables/useApi.ts'
import { createRuleCurrentStateParser } from '../../composables/useOperatorRules.ts'
import { storeMemoryCurrentStateParser } from '../../composables/useOperatorMemoryLab.ts'
import { createInvitationCurrentStateParser } from '../../composables/useOperatorAccess.ts'
import { createKeycardCurrentStateParser } from '../../composables/useOperatorKeycards.ts'
import { upsertDomainCurrentStateParser, deleteDomainCurrentStateParser } from '../../composables/useOperatorDomainRegistry.ts'
import { projectArchiveCurrentStateParser } from '../../composables/useOperatorProjects.ts'
import { cleanupOrphansCurrentStateParser, createSecretCurrentStateParser, deleteSecretCurrentStateParser } from '../../composables/useOperatorSecrets.ts'

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

test('strict endpoint parsers verify matching direct Rules and Memory current DTOs separately from submitted intent', async () => {
 const createdRule = await parseMutationResponse(
  { requestId: 'rule-create-1', action: 'rule-create', intent: { content: 'Only write through reviewed paths.', priority: 10, project: 'engram' } },
  jsonResponse(201, directRule()),
  createRuleCurrentStateParser({ content: 'Only write through reviewed paths.', priority: 10, project: 'engram' }),
 )
 const storedMemory = await parseMutationResponse(
  { requestId: 'memory-store-1', action: 'memory-store', intent: { project: 'engram', content: 'Mutation response proof belongs to the current DTO.', tags: ['operator'] } },
  jsonResponse(201, directMemory()),
  storeMemoryCurrentStateParser({ project: 'engram', content: 'Mutation response proof belongs to the current DTO.', tags: ['operator'] }),
 )

 for (const result of [createdRule, storedMemory]) {
  assertMutationKind(result, 'committed_verified')
  assert.equal(result.readback.kind, 'current')
  assert.notStrictEqual(result.readback.current, result.request.intent)
 }
})

test('direct current DTO domain and version mismatches remain pending', async () => {
 const results = await Promise.all([
  parseMutationResponse(
   { requestId: 'rule-domain-mismatch', action: 'rule-create', intent: { content: 'Only write through reviewed paths.', priority: 10, project: 'engram' } },
   jsonResponse(201, directRule({ project: 'other-project' })),
   createRuleCurrentStateParser({ content: 'Only write through reviewed paths.', priority: 10, project: 'engram' }),
  ),
  parseMutationResponse(
   { requestId: 'rule-version-mismatch', action: 'rule-create', intent: { content: 'Only write through reviewed paths.', priority: 10, project: 'engram' } },
   jsonResponse(201, directRule({ version: 0 })),
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

test('bare, action, and 202 responses remain pending despite direct parser support', async () => {
 const parser = createRuleCurrentStateParser({ content: 'Only write through reviewed paths.', priority: 10, project: 'engram' })
 const results = await Promise.all([
  parseMutationResponse({ requestId: 'bare', action: 'rule-create', intent: { content: 'Only write through reviewed paths.', priority: 10, project: 'engram' } }, jsonResponse(200, { id: 7 }), parser),
  parseMutationResponse({ requestId: 'delete', action: 'rule-delete', intent: { id: 7 } }, jsonResponse(200, { deleted: 7 }), parser),
  parseMutationResponse({ requestId: 'action', action: 'memory-suppress', intent: { id: '7' } }, jsonResponse(200, { status: 'suppressed', action: 'suppress', id: 7 }), parser),
  parseMutationResponse({ requestId: 'accepted', action: 'rule-create', intent: { content: 'Only write through reviewed paths.', priority: 10, project: 'engram' } }, jsonResponse(202, directRule()), parser),
 ])

 for (const result of results) {
  assertMutationKind(result, 'committed_verification_pending')
 }
 assertMutationKind(results[3], 'committed_verification_pending')
})

test('one-time keycard and invitation receipts expose only validated create-response secrets', async () => {
 const keycardInput = { name: 'workstation', scope: 'read-write' as const, principal: 'operator/alice', principalKind: 'human' as const, expiresAt: null }
 const invitationInput = { email: 'operator@example.test', role: 'operator', expiresInHours: 72 }
 const keycard = await parseMutationResponse(
  { requestId: 'keycard-create', action: 'access-create-keycard', intent: keycardInput },
  jsonResponse(200, { id: 'keycard-1', name: keycardInput.name, token_prefix: '01234567', scope: keycardInput.scope, principal: keycardInput.principal, principal_kind: keycardInput.principalKind, token: 'engram_0123456789abcdef0123456789abcdef' }),
  createKeycardCurrentStateParser(keycardInput),
 )
 const invitation = await parseMutationResponse(
  { requestId: 'invitation-create', action: 'access-create-invitation', intent: invitationInput },
  jsonResponse(201, { invitation: { id: 7, code: 'a'.repeat(64), email: invitationInput.email, role: invitationInput.role, created_by: 1, created_by_email: 'admin@example.test', expires_at: '2030-01-01T00:00:00Z', revocation_reason: '', created_at: '2026-01-01T00:00:00Z', status: 'pending' } }),
  createInvitationCurrentStateParser(invitationInput),
 )

 assertMutationKind(keycard, 'committed_verified')
 assertMutationKind(invitation, 'committed_verified')
 assert.equal(keycard.readback.kind, 'current')
 assert.equal(invitation.readback.kind, 'current')
 if (keycard.readback.kind !== 'current' || invitation.readback.kind !== 'current') throw new Error('expected direct create receipts')
 assert.equal(keycard.readback.current.token, 'engram_0123456789abcdef0123456789abcdef')
 assert.equal(invitation.readback.current.code, 'a'.repeat(64))

 for (const result of await Promise.all([
  parseMutationResponse({ requestId: 'keycard-malformed', action: 'access-create-keycard', intent: keycardInput }, jsonResponse(200, { id: 'keycard-1', token: 'not-a-keycard' }), createKeycardCurrentStateParser(keycardInput)),
  parseMutationResponse({ requestId: 'invitation-pending', action: 'access-create-invitation', intent: invitationInput }, jsonResponse(200, { operation_state: 'pending', invitation: { code: 'a'.repeat(64) } }), createInvitationCurrentStateParser(invitationInput)),
 ])) {
  assertMutationKind(result, 'committed_verification_pending')
 }
})

test('project, vault, and domain receipts require validated synchronous evidence', async () => {
 const project = 'project-alpha'
 const secret = { name: 'API_KEY', value: 'secret', project: 'engram', scope: 'project' as const }
 const domain = { domain: 'memory-lab', ownerPrincipal: 'agent/alice', ownerPrincipalKind: 'agent' as const, mode: 'warn' as const }
 const validDomain = { domain: domain.domain, owner_principal: domain.ownerPrincipal, owner_principal_kind: domain.ownerPrincipalKind, mode: domain.mode, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' }
 const valid = await Promise.all([
  parseMutationResponse({ requestId: 'project', action: 'project-archive', intent: { project } }, jsonResponse(200, { id: project, removed_at: '2026-01-01T00:00:00Z' }), projectArchiveCurrentStateParser(project)),
  parseMutationResponse({ requestId: 'vault-create', action: 'vault-store', intent: secret }, jsonResponse(201, { id: 9, name: secret.name, scope: secret.scope, message: 'Credential stored successfully' }), createSecretCurrentStateParser(secret)),
  parseMutationResponse({ requestId: 'vault-delete', action: 'vault-delete', intent: { credentialID: '9' } }, jsonResponse(200, { deleted: true, name: secret.name }), deleteSecretCurrentStateParser(secret.name)),
  parseMutationResponse({ requestId: 'vault-cleanup', action: 'vault-orphan-cleanup', intent: undefined }, jsonResponse(200, { status: 'ok', deleted: 1 }), cleanupOrphansCurrentStateParser),
  parseMutationResponse({ requestId: 'domain-upsert', action: 'memory-domain-upsert', intent: domain }, jsonResponse(200, validDomain), upsertDomainCurrentStateParser(domain)),
  parseMutationResponse({ requestId: 'domain-delete', action: 'memory-domain-delete', intent: { domain: domain.domain } }, jsonResponse(200, { deleted: true, domain: domain.domain }), deleteDomainCurrentStateParser(domain.domain)),
 ])
 for (const result of valid) assertMutationKind(result, 'committed_verified')

 const rejected = await Promise.all([
  parseMutationResponse({ requestId: 'project-malformed', action: 'project-archive', intent: { project } }, jsonResponse(200, { id: project }), projectArchiveCurrentStateParser(project)),
  parseMutationResponse({ requestId: 'vault-malformed', action: 'vault-store', intent: secret }, jsonResponse(201, { id: '9', name: secret.name }), createSecretCurrentStateParser(secret)),
  parseMutationResponse({ requestId: 'domain-malformed', action: 'memory-domain-upsert', intent: domain }, jsonResponse(200, { ...validDomain, owner_principal: 'agent/bob' }), upsertDomainCurrentStateParser(domain)),
  parseMutationResponse({ requestId: 'project-pending', action: 'project-archive', intent: { project } }, jsonResponse(200, { operation_state: 'pending', id: project, removed_at: '2026-01-01T00:00:00Z' }), projectArchiveCurrentStateParser(project)),
  parseMutationResponse({ requestId: 'vault-pending', action: 'vault-delete', intent: { credentialID: '9' } }, jsonResponse(200, { operation_state: 'pending', deleted: true, name: secret.name }), deleteSecretCurrentStateParser(secret.name)),
  parseMutationResponse({ requestId: 'domain-pending', action: 'memory-domain-delete', intent: { domain: domain.domain } }, jsonResponse(200, { operation_state: 'pending', deleted: true, domain: domain.domain }), deleteDomainCurrentStateParser(domain.domain)),
 ])
 for (const result of rejected) assertMutationKind(result, 'committed_verification_pending')
})
