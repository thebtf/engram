import type { ComputedRef } from 'vue'
import { executeMutation, type MutationCurrentStateParser, type MutationResult } from './useApi'
import {
  endpointEvidence,
  loadOperatorJson,
  operatorApiUrl,
  pendingState,
  type OperatorLoadState,
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

function assertDomain(value: string) {
  const domain = value.trim()
  if (!domain) {
    throw new Error('domain must not be empty')
  }
  return domain
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

export function upsertDomainCurrentStateParser(input: DomainRegistryDraft): MutationCurrentStateParser<{ domain: string }> {
  return (value) => {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined
    const domain = Reflect.get(value, 'domain')
    const ownerPrincipal = Reflect.get(value, 'owner_principal')
    const ownerPrincipalKind = Reflect.get(value, 'owner_principal_kind')
    const mode = Reflect.get(value, 'mode')
    const createdAt = Reflect.get(value, 'created_at')
    const updatedAt = Reflect.get(value, 'updated_at')
    if (
      domain !== input.domain.trim()
      || ownerPrincipal !== input.ownerPrincipal.trim()
      || ownerPrincipalKind !== input.ownerPrincipalKind
      || mode !== input.mode
      || typeof createdAt !== 'string' || Number.isNaN(Date.parse(createdAt))
      || typeof updatedAt !== 'string' || Number.isNaN(Date.parse(updatedAt))
    ) return undefined
    return { domain }
  }
}

export function deleteDomainCurrentStateParser(domain: string): MutationCurrentStateParser<{ domain: string }> {
  return (value) => {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined
    return Reflect.get(value, 'deleted') === true && Reflect.get(value, 'domain') === domain ? { domain } : undefined
  }
}

export function useOperatorDomainRegistry(): {
  domainState: ComputedRef<OperatorLoadState<OperatorMemoryDomain[]>>
  domains: ComputedRef<OperatorMemoryDomain[]>
  count: ComputedRef<number>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  refreshDomains: () => Promise<void>
  upsertDomain: (draft: DomainRegistryDraft) => Promise<MutationResult<DomainRegistryDraft, { domain: string }>>
  deleteDomain: (domain: string) => Promise<MutationResult<{ domain: string }, { domain: string }>>
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

    if (state.kind === 'error') {
      domainStateRef.value = {
        ...state,
        data: domains.value,
        retry: {
          source: 'memory-domain-registry',
          run: async () => {
            await refreshDomains()
            return domainStateRef.value
          },
        },
      }
      return
    }

    domainStateRef.value = { ...state, data: domains.value }
  }

  async function upsertDomain(draft: DomainRegistryDraft) {
    const domain = assertDomain(draft.domain)
    const payload: ApiMemoryDomainUpsertRequest = {
      owner_principal: draft.ownerPrincipal.trim(),
      owner_principal_kind: draft.ownerPrincipalKind,
      mode: draft.mode,
    }
    const endpoint = `/api/memory-domains/${encodeURIComponent(domain)}`

    return executeMutation(
      { requestId: crypto.randomUUID(), action: 'memory-domain-upsert', intent: draft },
      fetch(operatorApiUrl(endpoint), {
        method: 'PUT',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(payload),
        credentials: 'include',
      }),
      upsertDomainCurrentStateParser(draft),
    )
  }

  async function deleteDomain(domain: string) {
    const normalizedDomain = assertDomain(domain)
    const endpoint = `/api/memory-domains/${encodeURIComponent(normalizedDomain)}`
    return executeMutation(
      { requestId: crypto.randomUUID(), action: 'memory-domain-delete', intent: { domain: normalizedDomain } },
      fetch(operatorApiUrl(endpoint), { method: 'DELETE', credentials: 'include' }),
      deleteDomainCurrentStateParser(normalizedDomain),
    )
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
