import type { ComputedRef } from 'vue'
import type { OperatorLoadState } from './useOperatorApi'
import {
  emptyState,
  endpointEvidence,
  errorState,
  liveState,
  loadOperatorJson,
  operatorFetchJson,
  pendingState,
  runOperatorMutation,
  toOperatorSourceError,
  unsupportedOperatorAction,
} from './useOperatorApi'

export interface OperatorCredential {
  id: string
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

interface ApiVaultStoreReceipt {
  id: number
  name: string
  scope: string
  message?: string
}

interface ApiVaultOrphanReceipt {
  status: string
  deleted: number
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
  return {
    id: row.name,
    project: row.project || 'global',
    scope: row.scope || 'project',
    created: compactAge(row.created_at),
  }
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
  revealSecret: (name: string) => Promise<string>
  createSecret: (input: StoreSecretInput) => Promise<unknown>
  deleteSecret: (name: string) => Promise<unknown>
  cleanupOrphans: () => Promise<unknown>
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

  async function revealSecret(name: string): Promise<string> {
    try {
      const payload = await operatorFetchJson<ApiVaultReveal>(
        `/api/vault/credentials/${encodeURIComponent(name)}`,
        undefined,
        'vault-reveal',
      )
      return payload.value
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'vault-reveal',
        path: `/api/vault/credentials/${encodeURIComponent(name)}`,
        method: 'GET',
      })
      throw new Error(mapped.message)
    }
  }

  async function createSecret(input: StoreSecretInput) {
    return runOperatorMutation({
      action: 'vault-store',
      evidence: endpointEvidence('/api/vault/credentials', 'vault-store'),
      snapshot: () => [...credsState.value],
      run: () => operatorFetchJson<ApiVaultStoreReceipt>('/api/vault/credentials', jsonInit('POST', {
        name: input.name,
        value: input.value,
        scope: input.scope,
        project: input.project,
      }), 'vault-store'),
      rollback: (snapshot) => replaceArray(credsState.value, snapshot || []),
      refresh,
    })
  }

  async function deleteSecret(name: string) {
    return runOperatorMutation({
      action: 'vault-delete',
      evidence: endpointEvidence(`/api/vault/credentials/${name}`, 'vault-delete'),
      snapshot: () => [...credsState.value],
      optimistic: () => {
        replaceArray(credsState.value, credsState.value.filter((row) => row.id !== name))
      },
      run: () => operatorFetchJson(`/api/vault/credentials/${encodeURIComponent(name)}`, jsonInit('DELETE'), 'vault-delete'),
      rollback: (snapshot) => replaceArray(credsState.value, snapshot || []),
      refresh,
    })
  }

  async function cleanupOrphans() {
    return runOperatorMutation({
      action: 'vault-orphan-cleanup',
      evidence: endpointEvidence('/api/vault/orphaned-credentials', 'vault-orphan-cleanup'),
      snapshot: () => [...credsState.value],
      run: () => operatorFetchJson<ApiVaultOrphanReceipt>('/api/vault/orphaned-credentials', jsonInit('DELETE'), 'vault-orphan-cleanup'),
      rollback: (snapshot) => replaceArray(credsState.value, snapshot || []),
      refresh,
    })
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
