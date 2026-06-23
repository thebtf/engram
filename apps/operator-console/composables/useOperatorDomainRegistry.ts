import type { ComputedRef } from 'vue'
import type { OperatorLoadState, OperatorMutationResult } from './useOperatorApi'
import {
  endpointEvidence,
  loadOperatorJson,
  operatorFetchJson,
  pendingState,
  runOperatorMutation,
} from './useOperatorApi'

export const DOMAIN_OWNER_KINDS = ['human', 'agent', 'service'] as const
export const DOMAIN_OWNER_MODES = ['off', 'warn', 'reject'] as const

export type DomainOwnerKind = typeof DOMAIN_OWNER_KINDS[number]
export type DomainOwnerMode = typeof DOMAIN_OWNER_MODES[number]

interface ApiMemoryDomain {
  created_at?: string
  updated_at?: string
  domain?: string
  owner_principal?: string
  owner_principal_kind?: string
  mode?: string
}

interface ApiMemoryDomainsListResponse {
  domains?: ApiMemoryDomain[]
}

interface ApiMemoryDomainUpsertRequest {
  owner_principal: string
  owner_principal_kind: DomainOwnerKind
  mode: DomainOwnerMode
}

interface ApiMemoryDomainDeleteReceipt {
  deleted?: boolean
  domain?: string
}

export interface OperatorMemoryDomain {
  createdAt: string
  updatedAt: string
  domain: string
  ownerPrincipal: string
  ownerPrincipalKind: DomainOwnerKind
  mode: DomainOwnerMode
}

export interface DomainRegistryDraft {
  domain: string
  ownerPrincipal: string
  ownerPrincipalKind: DomainOwnerKind
  mode: DomainOwnerMode
}

function nowIso() {
  return new Date().toISOString()
}

function normalizeKind(value?: string): DomainOwnerKind {
  return DOMAIN_OWNER_KINDS.includes(value as DomainOwnerKind) ? value as DomainOwnerKind : 'agent'
}

function normalizeMode(value?: string): DomainOwnerMode {
  return DOMAIN_OWNER_MODES.includes(value as DomainOwnerMode) ? value as DomainOwnerMode : 'warn'
}

function mapDomain(row: ApiMemoryDomain): OperatorMemoryDomain {
  return {
    createdAt: row.created_at || nowIso(),
    updatedAt: row.updated_at || row.created_at || nowIso(),
    domain: String(row.domain || '').trim(),
    ownerPrincipal: String(row.owner_principal || '').trim(),
    ownerPrincipalKind: normalizeKind(row.owner_principal_kind),
    mode: normalizeMode(row.mode),
  }
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorDomainRegistry] ${key} live load failed`, error)
      }
    })
  }
}

export function useOperatorDomainRegistry(): {
  domainState: ComputedRef<OperatorLoadState<OperatorMemoryDomain[]>>
  domains: ComputedRef<OperatorMemoryDomain[]>
  count: ComputedRef<number>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  refreshDomains: () => Promise<void>
  upsertDomain: (draft: DomainRegistryDraft) => Promise<OperatorMutationResult<OperatorMemoryDomain>>
  deleteDomain: (domain: string) => Promise<OperatorMutationResult<ApiMemoryDomainDeleteReceipt>>
  listEvidence: ReturnType<typeof endpointEvidence>
} {
  const listEvidence = endpointEvidence('/api/memory-domains', 'memory-domain-registry')
  const domainStateRef = useState<OperatorLoadState<OperatorMemoryDomain[]>>(
    'live:domain-registry:domains',
    () => pendingState(listEvidence, []),
  )

  const domainState = computed(() => domainStateRef.value)
  const domains = computed(() => domainStateRef.value.data || [])
  const count = computed(() => domains.value.length)
  const pending = computed(() => domainStateRef.value.kind === 'pending')
  const error = computed(() => domainStateRef.value.kind === 'error' ? domainStateRef.value.error.message : null)

  async function refreshDomains() {
    const state = await loadOperatorJson<ApiMemoryDomainsListResponse>('/api/memory-domains', {
      source: 'memory-domain-registry',
      empty: (data) => !data.domains || data.domains.length === 0,
    })

    if (state.kind === 'live' || state.kind === 'empty') {
      domainStateRef.value = {
        ...state,
        data: (state.data.domains || []).map(mapDomain),
      }
      return
    }

    domainStateRef.value = { ...state, data: domains.value }
  }

  async function upsertDomain(draft: DomainRegistryDraft) {
    const domain = draft.domain.trim()
    const payload: ApiMemoryDomainUpsertRequest = {
      owner_principal: draft.ownerPrincipal.trim(),
      owner_principal_kind: draft.ownerPrincipalKind,
      mode: draft.mode,
    }
    const endpoint = `/api/memory-domains/${encodeURIComponent(domain)}`

    return runOperatorMutation({
      action: 'memory-domain-upsert',
      evidence: endpointEvidence(endpoint, 'memory-domain-upsert'),
      run: async () => mapDomain(await operatorFetchJson<ApiMemoryDomain>(endpoint, {
        method: 'PUT',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(payload),
      }, 'memory-domain-upsert')),
      refresh: refreshDomains,
    })
  }

  async function deleteDomain(domain: string) {
    const endpoint = `/api/memory-domains/${encodeURIComponent(domain.trim())}`
    return runOperatorMutation({
      action: 'memory-domain-delete',
      evidence: endpointEvidence(endpoint, 'memory-domain-delete'),
      run: () => operatorFetchJson<ApiMemoryDomainDeleteReceipt>(endpoint, { method: 'DELETE' }, 'memory-domain-delete'),
      refresh: refreshDomains,
    })
  }

  startOnce('domain-registry', refreshDomains)

  return {
    domainState,
    domains,
    count,
    pending,
    error,
    refreshDomains,
    upsertDomain,
    deleteDomain,
    listEvidence,
  }
}
