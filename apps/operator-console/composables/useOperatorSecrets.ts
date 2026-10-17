import { computed, ref as sharedRef, type Ref as SharedRef } from 'vue'
import { startedLoads } from './useConsolePreferences'
import { isCatalogProject, useOperatorProjectContext } from './useOperatorProjectContext'

let sharedSecretsPageCreds: SharedRef<OperatorCredential[]> | undefined
let sharedSecretsPageVault: SharedRef<OperatorVaultStatus> | undefined
let sharedSecretsPageState: SharedRef<OperatorLoadState<OperatorCredential[]>> | undefined
let sharedSecretsPageVaultState: SharedRef<OperatorLoadState<OperatorVaultStatus>> | undefined

import type { ComputedRef } from 'vue'
import { executeMutation, type MutationCurrentStateParser, type MutationResult } from './useApi'
import {
  emptyState,
  endpointEvidence,
  errorState,
  liveState,
  loadOperatorJson,
  operatorFetchResponse,
  operatorFetchJson,
  pendingState,
  toOperatorSourceError,
  unsupportedOperatorAction,
  type OperatorLoadState,
} from './useOperatorApi'
function submitMutation<TIntent, TCurrent>(action: string, intent: TIntent, path: string, init: RequestInit, parseCurrentState: MutationCurrentStateParser<TCurrent>): Promise<MutationResult<TIntent, TCurrent>> {
  return executeMutation(
    { requestId: crypto.randomUUID(), action, intent },
    operatorFetchResponse(path, { ...init, credentials: 'include' }),
    parseCurrentState,
  )
}


export interface OperatorCredential {
  id: string
  name: string
  project: string
  scope: 'project' | 'global'
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
  project_id?: string | null
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
  project?: string
  project_id?: string | null
  id?: number
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
    const project = Reflect.get(value, 'project')
    const projectId = Reflect.get(value, 'project_id')
    if (typeof id !== 'number' || !Number.isSafeInteger(id) || id <= 0 || name !== input.name || scope !== input.scope || (project !== undefined && project !== input.project) || (projectId !== undefined && projectId !== input.project)) return undefined
    return { id, name, scope }
  }
}

export function deleteSecretCurrentStateParser(name: string, project: string): MutationCurrentStateParser<{ name: string }> {
  return (value) => {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined
    const echoedProject = Reflect.get(value, 'project')
    const projectId = Reflect.get(value, 'project_id')
    return Reflect.get(value, 'deleted') === true && Reflect.get(value, 'name') === name && (echoedProject === undefined || echoedProject === project) && (projectId === undefined || projectId === (project || null)) ? { name } : undefined
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
  const scope = row?.scope || 'project'
  const project = row?.project_id === undefined ? row?.project || '' : row.project_id || ''
  if (!row || (scope !== 'project' && scope !== 'global') || (scope === 'global' ? project !== '' : !isCatalogProject(project)) || (row.project !== undefined && row.project !== project) || typeof row.name !== 'string' || !row.name || (row.id !== undefined && (!Number.isSafeInteger(row.id) || row.id < 1))) throw new TypeError('Invalid credential project identity')
  return {
    id: row.id !== undefined ? String(row.id) : `${scope}:${project}:${row.name}`,
    name: row.name,
    project,
    scope,
    created: compactAge(row.created_at),
  }
}

function credentialUrl(cred: OperatorCredential): string {
  const base = `/api/vault/credentials/${encodeURIComponent(cred.name)}`
  if (cred.scope === 'global' ? cred.project !== '' : !isCatalogProject(cred.project)) throw new TypeError('An exact credential scope is required')

  const params = new URLSearchParams(cred.scope === 'global' ? { scope: 'global' } : { project: cred.project })
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
  const started = startedLoads.has(key)
  if (typeof window !== 'undefined' && !started) {
    startedLoads.add(key)
    void run().catch((error) => {
      if (import.meta.env.DEV) {
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
  createSecret: (input: StoreSecretInput) => Promise<MutationResult<Omit<StoreSecretInput, 'value'>, OperatorVaultCreateReceipt>>
  deleteSecret: (cred: OperatorCredential) => Promise<MutationResult<{ credentialID: string }, { name: string }>>
  cleanupOrphans: () => Promise<MutationResult<undefined, { deleted: number }>>
  rotationGap: ReturnType<typeof unsupportedOperatorAction>
} {
  const credsEvidence = endpointEvidence('/api/vault/credentials', 'vault-credentials')
  const vaultEvidence = endpointEvidence('/api/vault/status', 'vault-status')
  const context = useOperatorProjectContext()
  const credsState = (sharedSecretsPageCreds ??= sharedRef<OperatorCredential[]>((() => [])()))
  const vaultStatus = (sharedSecretsPageVault ??= sharedRef<OperatorVaultStatus>((() => ({
    encrypted: false,
    fingerprint: '-',
    source: '-',
    count: 0,
  }))()))
  const loadStateValue = (sharedSecretsPageState ??= sharedRef<OperatorLoadState<OperatorCredential[]>>((() => pendingState(credsEvidence, credsState.value))()))
  const vaultStateValue = (sharedSecretsPageVaultState ??= sharedRef<OperatorLoadState<OperatorVaultStatus>>((() => pendingState(vaultEvidence, vaultStatus.value))()))

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
      try {
        await context.refresh()
        if (context.error.value) throw context.error.value
        if (!Array.isArray(result.data)) throw new TypeError('Invalid credential metadata')
        const credentials = result.data.map(mapCredential)
        const identities = new Set(credentials.map((cred) => `${cred.project}\u0000${cred.name}`))
        if (identities.size !== credentials.length) throw new TypeError('Duplicate credential identity')
        replaceArray(credsState.value, credentials)
        loadStateValue.value = credsState.value.length ? liveState(credsEvidence, credsState.value) : emptyState(credsEvidence, credsState.value)
      } catch (cause) {
        replaceArray(credsState.value, [])
        const mapped = toOperatorSourceError(cause, { source: 'vault-credentials', path: '/api/vault/credentials', method: 'GET' })
        if (cause instanceof TypeError) mapped.category = 'invalid-response'
        loadStateValue.value = errorState(credsEvidence, mapped, { source: 'vault-credentials', run: async () => { await refreshCreds(); return loadStateValue.value } }, [])
      }
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
      if (payload.name !== cred.name || (payload.id !== undefined && String(payload.id) !== cred.id) || (payload.project !== undefined && payload.project !== cred.project) || (payload.project_id !== undefined && payload.project_id !== (cred.project || null)) || (payload.scope !== undefined && payload.scope !== cred.scope) || typeof payload.value !== 'string') throw new TypeError('Credential reveal identity mismatch')
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
    if (!isCatalogProject(input.project)) throw new TypeError('A catalog credential project is required')
    const intent = { name: input.name, project: input.project, scope: input.scope }
    const result = await submitMutation('vault-store', intent, '/api/vault/credentials', jsonInit('POST', {
      name: input.name,
      value: input.value,
      scope: input.scope,
      project: input.project,
    }), createSecretCurrentStateParser(input))
    if (result.kind !== 'committed_verified' || result.readback.kind !== 'current') return result
    try {
      const rows = await operatorFetchJson<ApiVaultCredential[]>('/api/vault/credentials', undefined, 'vault-store-readback')
      const id = result.readback.current.id
      if (Array.isArray(rows) && rows.map(mapCredential).some((row) => row.id === String(id) && row.name === input.name && row.project === input.project && row.scope === input.scope)) return result
    } catch { /* A failed metadata read never proves the credential identity. */ }
    return { kind: 'committed_verification_pending' as const, request: result.request, httpStatus: result.httpStatus, commitment: 'committed' as const, reason: 'readback_invalid' as const }
  }

  async function deleteSecret(cred: OperatorCredential) {
    const path = credentialUrl(cred)
    const result = await submitMutation('vault-delete', { credentialID: cred.id }, path, jsonInit('DELETE'), deleteSecretCurrentStateParser(cred.name, cred.project))
    if (result.kind !== 'committed_verified') return result
    try {
      const rows = await operatorFetchJson<ApiVaultCredential[]>('/api/vault/credentials', undefined, 'vault-delete-readback')
      if (Array.isArray(rows) && !rows.map(mapCredential).some((row) => row.scope === cred.scope && row.project === cred.project && (row.id === cred.id || row.name === cred.name))) return result
    } catch { /* A failed metadata read never proves scoped deletion. */ }
    return { kind: 'committed_verification_pending' as const, request: result.request, httpStatus: result.httpStatus, commitment: 'committed' as const, reason: 'readback_invalid' as const }
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
