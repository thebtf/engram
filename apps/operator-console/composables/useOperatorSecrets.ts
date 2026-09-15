import type { ComputedRef } from 'vue'
import { executeMutation, type MutationCurrentStateParser, type MutationResult } from './useApi'
import {
  emptyState,
  endpointEvidence,
  errorState,
  liveState,
  loadOperatorJson,
  operatorApiUrl,
  operatorFetchJson,
  pendingState,
  toOperatorSourceError,
  unsupportedOperatorAction,
  type OperatorLoadState,
} from './useOperatorApi'
function submitMutation<TIntent, TCurrent>(action: string, intent: TIntent, path: string, init: RequestInit, parseCurrentState: MutationCurrentStateParser<TCurrent>): Promise<MutationResult<TIntent, TCurrent>> {
  return executeMutation(
    { requestId: crypto.randomUUID(), action, intent },
    fetch(operatorApiUrl(path), { ...init, credentials: 'include' }),
    parseCurrentState,
  )
}


export interface OperatorCredential {
  id: string
  name: string
  project: string
  scope: string
  created: string
}

export interface OperatorVaultStatus {
  encrypted: boolean
  fingerprint: string
  source: string
  count: number
  mismatchWarning?: string
}

export interface StoreSecretInput {
  name: string
  value: string
  project: string
  scope: 'project'
}

interface ApiVaultCredential {
  id?: number
  name: string
  project?: string
  scope?: string
  created_at?: string
}

interface ApiVaultStatus {
  key_configured?: boolean
  fingerprint?: string
  key_source?: string
  credential_count?: number
  mismatch_warning?: string
}

interface ApiVaultReveal {
  name: string
  value: string
  scope?: string
}

export interface OperatorVaultCreateReceipt {
  id: number
  name: string
  scope: 'project'
}

export function createSecretCurrentStateParser(input: StoreSecretInput): MutationCurrentStateParser<OperatorVaultCreateReceipt> {
  return (value) => {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined
    const id = Reflect.get(value, 'id')
    const name = Reflect.get(value, 'name')
    const scope = Reflect.get(value, 'scope')
    if (typeof id !== 'number' || !Number.isSafeInteger(id) || id <= 0 || name !== input.name || scope !== input.scope) return undefined
    return { id, name, scope }
  }
}

export function deleteSecretCurrentStateParser(name: string): MutationCurrentStateParser<{ name: string }> {
  return (value) => {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined
    return Reflect.get(value, 'deleted') === true && Reflect.get(value, 'name') === name ? { name } : undefined
  }
}

export function cleanupOrphansCurrentStateParser(value: unknown): { deleted: number } | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined
  const deleted = Reflect.get(value, 'deleted')
  return Reflect.get(value, 'status') === 'ok' && typeof deleted === 'number' && Number.isSafeInteger(deleted) && deleted >= 0
    ? { deleted }
    : undefined
}


function jsonInit(method: 'POST' | 'DELETE', body?: unknown): RequestInit {
  const init: RequestInit = { method }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  return init
}

function replaceArray<T>(target: T[], next: readonly T[]) {
  target.splice(0, target.length, ...next)
}

function compactAge(timestamp?: string): string {
  if (!timestamp) return '-'

  const value = Date.parse(timestamp)
  if (Number.isNaN(value)) return '-'

  const seconds = Math.max(0, Math.floor((Date.now() - value) / 1000))
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`
  return `${Math.floor(seconds / 86400)}d`
}

function mapCredential(row: ApiVaultCredential): OperatorCredential {
  const project = row.project || ''
  return {
    id: row.id !== undefined ? String(row.id) : `${project || 'global'}:${row.name}`,
    name: row.name,
    project,
    scope: row.scope || 'project',
    created: compactAge(row.created_at),
  }
}

function credentialUrl(cred: OperatorCredential): string {
  const base = `/api/vault/credentials/${encodeURIComponent(cred.name)}`
  if (!cred.project) return base

  const params = new URLSearchParams({ project: cred.project })
  return `${base}?${params.toString()}`
}

function mapVaultStatus(row: ApiVaultStatus): OperatorVaultStatus {
  return {
    encrypted: Boolean(row.key_configured),
    fingerprint: row.fingerprint || '-',
    source: row.key_source || '-',
    count: typeof row.credential_count === 'number' ? row.credential_count : 0,
    mismatchWarning: row.mismatch_warning,
  }
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorSecrets] ${key} live load failed`, error)
      }
    })
  }
}

export function useOperatorSecrets(): {
  creds: OperatorCredential[]
  vault: ComputedRef<OperatorVaultStatus>
  loadState: ComputedRef<OperatorLoadState<OperatorCredential[]>>
  vaultState: ComputedRef<OperatorLoadState<OperatorVaultStatus>>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  refresh: () => Promise<void>
  revealSecret: (cred: OperatorCredential) => Promise<string>
  createSecret: (input: StoreSecretInput) => Promise<MutationResult<StoreSecretInput, OperatorVaultCreateReceipt>>
  deleteSecret: (cred: OperatorCredential) => Promise<MutationResult<{ credentialID: string }, { name: string }>>
  cleanupOrphans: () => Promise<MutationResult<undefined, { deleted: number }>>
  rotationGap: ReturnType<typeof unsupportedOperatorAction>
} {
  const credsEvidence = endpointEvidence('/api/vault/credentials', 'vault-credentials')
  const vaultEvidence = endpointEvidence('/api/vault/status', 'vault-status')
  const credsState = useState<OperatorCredential[]>('live:secrets-page:creds', () => [])
  const vaultStatus = useState<OperatorVaultStatus>('live:secrets-page:vault', () => ({
    encrypted: false,
    fingerprint: '-',
    source: '-',
    count: 0,
  }))
  const loadStateValue = useState<OperatorLoadState<OperatorCredential[]>>('live:secrets-page:state', () => pendingState(credsEvidence, credsState.value))
  const vaultStateValue = useState<OperatorLoadState<OperatorVaultStatus>>('live:secrets-page:vault-state', () => pendingState(vaultEvidence, vaultStatus.value))

  const loadState = computed(() => loadStateValue.value)
  const vaultState = computed(() => vaultStateValue.value)
  const vault = computed(() => vaultStatus.value)
  const pending = computed(() => loadStateValue.value.kind === 'pending' || vaultStateValue.value.kind === 'pending')
  const error = computed(() => {
    if (loadStateValue.value.kind === 'error') return loadStateValue.value.error.message
    if (vaultStateValue.value.kind === 'error') return vaultStateValue.value.error.message
    return null
  })

  async function refreshVault() {
    vaultStateValue.value = pendingState(vaultEvidence, vaultStatus.value)
    const result = await loadOperatorJson<ApiVaultStatus>('/api/vault/status', {
      source: 'vault-status',
    })

    if (result.kind === 'live' || result.kind === 'empty') {
      vaultStatus.value = mapVaultStatus(result.data)
      vaultStateValue.value = liveState(vaultEvidence, vaultStatus.value)
      return
    }

    if (result.kind === 'error') {
      vaultStateValue.value = errorState(vaultEvidence, result.error, {
        source: 'vault-status',
        run: async () => {
          await refreshVault()
          return vaultStateValue.value
        },
      }, vaultStatus.value)
    } else {
      vaultStateValue.value = result as OperatorLoadState<OperatorVaultStatus>
    }
  }

  async function refreshCreds() {
    loadStateValue.value = pendingState(credsEvidence, credsState.value)
    const result = await loadOperatorJson<ApiVaultCredential[]>('/api/vault/credentials', {
      source: 'vault-credentials',
      empty: (rows) => !rows.length,
    })

    if (result.kind === 'live' || result.kind === 'empty') {
      replaceArray(credsState.value, result.data.map(mapCredential))
      loadStateValue.value = credsState.value.length
        ? liveState(credsEvidence, credsState.value)
        : emptyState(credsEvidence, credsState.value)
      return
    }

    if (result.kind === 'error') {
      loadStateValue.value = errorState(credsEvidence, result.error, {
        source: 'vault-credentials',
        run: async () => {
          await refreshCreds()
          return loadStateValue.value
        },
      }, credsState.value)
    } else {
      loadStateValue.value = result as OperatorLoadState<OperatorCredential[]>
    }
  }

  async function refresh() {
    await Promise.all([refreshVault(), refreshCreds()])
  }

  async function revealSecret(cred: OperatorCredential): Promise<string> {
    const path = credentialUrl(cred)
    try {
      const payload = await operatorFetchJson<ApiVaultReveal>(
        path,
        undefined,
        'vault-reveal',
      )
      return payload.value
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'vault-reveal',
        path,
        method: 'GET',
      })
      throw new Error(mapped.message)
    }
  }

  async function createSecret(input: StoreSecretInput) {
    return submitMutation('vault-store', input, '/api/vault/credentials', jsonInit('POST', {
      name: input.name,
      value: input.value,
      scope: input.scope,
      project: input.project,
    }), createSecretCurrentStateParser(input))
  }

  async function deleteSecret(cred: OperatorCredential) {
    const path = credentialUrl(cred)
    return submitMutation('vault-delete', { credentialID: cred.id }, path, jsonInit('DELETE'), deleteSecretCurrentStateParser(cred.name))
  }

  async function cleanupOrphans() {
    return submitMutation('vault-orphan-cleanup', undefined, '/api/vault/orphaned-credentials', jsonInit('DELETE'), cleanupOrphansCurrentStateParser)
  }

  const rotationGap = unsupportedOperatorAction(
    'vault-rotate',
    'POST /api/vault/rotate',
    'The current server exposes status/list/reveal/store/delete/orphan cleanup, but no key-rotation route.',
  )

  startOnce('secrets-page', refresh)

  return {
    creds: credsState.value,
    vault,
    loadState,
    vaultState,
    pending,
    error,
    refresh,
    revealSecret,
    createSecret,
    deleteSecret,
    cleanupOrphans,
    rotationGap,
  }
}
