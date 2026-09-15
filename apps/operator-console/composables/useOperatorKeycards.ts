import { computed, type ComputedRef } from 'vue'
import { executeMutation, type MutationCurrentStateParser, type MutationResult } from './useApi'
import {
  endpointEvidence,
  errorState,
  liveState,
  operatorApiUrl,
  operatorFetchJson,
  pendingState,
  toOperatorSourceError,
  type OperatorLoadState,
  type OperatorSourceError,
} from './useOperatorApi'

const KEYCARDS_ENDPOINT = '/api/auth/tokens'


export type OperatorKeycardScope = 'read-write' | 'read-only'
export type OperatorKeycardPrincipalKind = 'human' | 'agent' | 'service'

export interface OperatorKeycard {
  id: string
  name: string
  tokenPrefix: string
  scope: string
  principal: string
  principalKind: string
  expiresAt: string | null
  createdAt: string | null
  lastUsedAt: string | null
  requestCount: number
  errorCount: number
  revoked: boolean
  revokedAt: string | null
}

export interface OperatorKeycardCreateInput {
  name: string
  scope: OperatorKeycardScope
  principal: string
  principalKind: OperatorKeycardPrincipalKind
  expiresAt: string | null
}


export interface OperatorKeycardsComposable {
  keycards: OperatorKeycard[]
  loadState: ComputedRef<OperatorLoadState<OperatorKeycard[]>>
  error: ComputedRef<OperatorSourceError | null>
  refresh: () => Promise<void>
  createKeycard: (input: OperatorKeycardCreateInput) => Promise<MutationResult<OperatorKeycardCreateInput, OperatorKeycardCreateReceipt>>
  revokeKeycard: (keycardID: string) => Promise<MutationResult<{ keycardID: string }>>
}

export interface OperatorKeycardCreateReceipt {
  token: string
}

function parseKeycard(value: unknown): OperatorKeycard {
  if (
    value === null ||
    typeof value !== 'object' ||
    Array.isArray(value) ||
    !('id' in value) ||
    typeof value.id !== 'string' ||
    value.id.length === 0
  ) {
    throw new Error('Invalid keycard response')
  }

  const name = 'name' in value && typeof value.name === 'string' ? value.name : ''
  const tokenPrefix = 'token_prefix' in value && typeof value.token_prefix === 'string'
    ? value.token_prefix
    : 'prefix' in value && typeof value.prefix === 'string' ? value.prefix : ''
  const scope = 'scope' in value && typeof value.scope === 'string' ? value.scope : ''
  const principal = 'principal' in value && typeof value.principal === 'string' ? value.principal : ''
  const principalKind = 'principal_kind' in value && typeof value.principal_kind === 'string' ? value.principal_kind : ''
  const expiresAt = 'expires_at' in value && typeof value.expires_at === 'string' && value.expires_at ? value.expires_at : null
  const createdAt = 'created_at' in value && typeof value.created_at === 'string' && value.created_at ? value.created_at : null
  const lastUsedAt = 'last_used_at' in value && typeof value.last_used_at === 'string' && value.last_used_at ? value.last_used_at : null
  const requestCount = 'request_count' in value && typeof value.request_count === 'number' && Number.isFinite(value.request_count)
    ? Math.trunc(value.request_count)
    : 0
  const errorCount = 'error_count' in value && typeof value.error_count === 'number' && Number.isFinite(value.error_count)
    ? Math.trunc(value.error_count)
    : 0
  const revoked = 'revoked' in value && value.revoked === true
  const revokedAt = 'revoked_at' in value && typeof value.revoked_at === 'string' && value.revoked_at ? value.revoked_at : null

  return {
    id: value.id,
    name,
    tokenPrefix,
    scope,
    principal,
    principalKind,
    expiresAt,
    createdAt,
    lastUsedAt,
    requestCount,
    errorCount,
    revoked,
    revokedAt,
  }
}

function parseKeycardList(value: unknown): OperatorKeycard[] {
  if (
    value === null ||
    typeof value !== 'object' ||
    Array.isArray(value) ||
    !('tokens' in value) ||
    !Array.isArray(value.tokens)
  ) {
    throw new Error('Invalid keycard list response')
  }
  return value.tokens.map(parseKeycard)
}

export function createKeycardCurrentStateParser(input: OperatorKeycardCreateInput): MutationCurrentStateParser<OperatorKeycardCreateReceipt> {
  return (value) => {
    try {
      const keycard = parseKeycard(value)
      const token = typeof value === 'object' && value !== null && !Array.isArray(value)
        ? Reflect.get(value, 'token')
        : undefined
      if (
        keycard.name !== input.name
        || keycard.scope !== input.scope
        || keycard.principal !== input.principal
        || keycard.principalKind !== input.principalKind
        || typeof token !== 'string'
        || !/^engram_[a-f0-9]{32}$/.test(token)
      ) return undefined
      return { token }
    } catch {
      return undefined
    }
  }
}


function keycardCreateInit(input: OperatorKeycardCreateInput): RequestInit {
  const body: Record<string, string> = {
    name: input.name,
    scope: input.scope,
    principal: input.principal,
    principal_kind: input.principalKind,
  }
  if (input.expiresAt) {
    body.expires_at = input.expiresAt
  }

  return {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }
}


export function useOperatorKeycards(): OperatorKeycardsComposable {
  const evidence = endpointEvidence(KEYCARDS_ENDPOINT, 'access-keycards')
  const keycardsState = useState<OperatorKeycard[]>('live:access:keycards', () => [])
  const loadStateValue = useState<OperatorLoadState<OperatorKeycard[]>>(
    'live:access:keycards-state',
    () => pendingState(evidence, [...keycardsState.value]),
  )
  const started = useState<boolean>('live:access:keycards-started', () => false)

  const loadState = computed(() => loadStateValue.value)
  const error = computed(() => loadStateValue.value.kind === 'error' ? loadStateValue.value.error : null)

  function currentSnapshot(): OperatorKeycard[] {
    return keycardsState.value.map((keycard) => ({ ...keycard }))
  }

  async function refresh() {
    loadStateValue.value = pendingState(evidence, currentSnapshot())
    try {
      const payload = await operatorFetchJson<unknown>(KEYCARDS_ENDPOINT, undefined, 'access-keycards-list')
      keycardsState.value.splice(0, keycardsState.value.length, ...parseKeycardList(payload))
      loadStateValue.value = liveState(evidence, currentSnapshot())
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'access-keycards-list',
        path: KEYCARDS_ENDPOINT,
        method: 'GET',
      })
      loadStateValue.value = errorState(evidence, mapped, {
        source: 'access-keycards-list',
        run: async () => {
          await refresh()
          return loadStateValue.value
        },
      }, currentSnapshot())
    }
  }

  function createKeycard(input: OperatorKeycardCreateInput) {
    return executeMutation(
      { requestId: crypto.randomUUID(), action: 'access-create-keycard', intent: input },
      fetch(operatorApiUrl(KEYCARDS_ENDPOINT), { ...keycardCreateInit(input), credentials: 'include' }),
      createKeycardCurrentStateParser(input),
    )
  }

  function revokeKeycard(keycardID: string) {
    const path = `${KEYCARDS_ENDPOINT}/${encodeURIComponent(keycardID)}`
    return executeMutation(
      { requestId: crypto.randomUUID(), action: 'access-revoke-keycard', intent: { keycardID } },
      fetch(operatorApiUrl(path), { method: 'DELETE', credentials: 'include' }),
      () => undefined,
    )
  }

  if (import.meta.client && !started.value) {
    started.value = true
    void refresh()
  }

  return {
    keycards: keycardsState.value,
    loadState,
    error,
    refresh,
    createKeycard,
    revokeKeycard,
  }
}
