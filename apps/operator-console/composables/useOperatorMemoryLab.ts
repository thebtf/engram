import type { ComputedRef, Ref } from 'vue'
import type { Memory } from './useMockData'
import type { OperatorLoadState, OperatorMutationResult, OperatorUnsupportedAction } from './useOperatorApi'
import {
  emptyState,
  endpointEvidence,
  errorState,
  gatedState,
  liveState,
  loadOperatorJson,
  mustBuildState,
  OperatorFetchError,
  operatorFetchJson,
  pendingState,
  runOperatorMutation,
  staleState,
  toOperatorSourceError,
  unsupportedOperatorAction,
} from './useOperatorApi'

interface ApiMemory {
  id: number | string
  project?: string
  content?: string
  tags?: string[]
  tier?: string
  epistemic_type?: string
  confidence?: number
  citation_count?: number
  injection_count?: number
  updated_at?: string
  created_at?: string
  status?: string
  superseded_by?: number | string | null
  source_sessions?: string[]
}

interface ApiPrincipalMemoryAudit {
  durable?: boolean
  action?: string
}

interface ApiPrincipalMemoryItem {
  id?: number | string
  project?: string
  content?: string
  tags?: string[]
  owner_principal?: string
  owner_principal_kind?: string
  agent_visibility?: string
  domain?: string
  confidence?: number
  created_at?: string
}

interface ApiPrincipalMemoryQueryResponse {
  principal?: string
  principal_kind?: string
  project?: string
  domain?: string
  items?: ApiPrincipalMemoryItem[]
  hidden_count?: number
  audit?: ApiPrincipalMemoryAudit
  audit_status?: string
}

// Per-project fetch cap; matches the server-side /api/memories maximum limit.
const MEMORY_LIST_LIMIT = 500
const PRINCIPAL_MEMORY_LIMIT = 10
const PRINCIPAL_BRIEF_REASON = 'Principal-scoped brief exists in MCP get_memory_brief, but no browser REST bridge is available yet.'
export const PRINCIPAL_CURRENT_PROJECT = 'current'

export type PrincipalKind = 'human' | 'agent' | 'service'
export type PrincipalVisibility = 'all' | 'shared' | 'private'

export interface PrincipalMemoryScope {
  principal: string
  principalKind: PrincipalKind
  project: string
  domain: string
  visibility: PrincipalVisibility
  includePrivate: boolean
  limit: number
}

export interface OperatorPrincipalMemoryItem {
  id: string
  project: string
  content: string
  tags: string[]
  ownerPrincipal: string
  ownerPrincipalKind: string
  visibility: string
  domain: string
  confidence: number | null
  createdAt: string
}

export interface OperatorPrincipalMemorySummary {
  scope: PrincipalMemoryScope
  principal: string
  principalKind: string
  project: string
  domain: string
  items: OperatorPrincipalMemoryItem[]
  hiddenCount: number
  auditStatus: string
  audit: {
    durable: boolean
    action: string
  }
  source: string
  freshness: string
}

export interface OperatorPrincipalMemoryBrief {
  scope: PrincipalMemoryScope
  source: string
  freshness: string
  lines: string[]
}

export interface StoreMemoryInput {
  project: string
  content: string
  tags?: string[]
}

export interface MemoryActionReceipt {
  status: string
  action: 'suppress'
  id: number
  reason?: string
}

export interface MemoryAuditEntry {
  id: number
  memory_id: number
  action: string
  actor: string
  source_session_id?: string
  reason?: string
  before_state_present: boolean
  after_state_present: boolean
  created_at: string
}

export interface MemoryAuditResponse {
  memory_id: number
  entries: MemoryAuditEntry[]
}

export interface MemoryActionGap extends OperatorUnsupportedAction {
  labelKey: string
  badgeKey: string
}

function memoryActionGap(
  action: string,
  endpoint: string,
  reason: string,
  labelKey: string,
  badgeKey = 'overview.badges.mustBuild',
): MemoryActionGap {
  return {
    ...unsupportedOperatorAction(action, endpoint, reason),
    labelKey,
    badgeKey,
  }
}

export const memoryActionGaps: readonly MemoryActionGap[] = [
  memoryActionGap(
    'memory-edit-text',
    'MCP memory.edit',
    'Memory text editing is not exposed by the current browser REST API.',
    'memory.detail.actions.editText',
  ),
  memoryActionGap(
    'memory-replace',
    'MCP memory.supersede',
    'Memory replacement is not exposed by the current browser REST API.',
    'memory.detail.actions.replace',
  ),
  memoryActionGap(
    'memory-promote',
    'ENGRAM_LIFECYCLE_ENABLED',
    'Memory lifecycle promotion requires the lifecycle backend surface.',
    'memory.detail.actions.promote',
    'memory.detail.actions.lifecycleRequired',
  ),
  memoryActionGap(
    'memory-flag',
    'POST /api/memories/{id}/flag',
    'Memory flagging is not exposed by the current server API.',
    'memory.detail.actions.flag',
  ),
]

function blankPrincipalScope(): PrincipalMemoryScope {
  return {
    principal: '',
    principalKind: 'agent',
    project: 'all',
    domain: '',
    visibility: 'all',
    includePrivate: false,
    limit: PRINCIPAL_MEMORY_LIMIT,
  }
}

function clean(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function clampPrincipalLimit(limit: number): number {
  return Number.isInteger(limit) && limit > 0 ? Math.min(10, limit) : PRINCIPAL_MEMORY_LIMIT
}

function normalizePrincipalKind(value?: string): PrincipalKind {
  return value === 'human' || value === 'service' || value === 'agent' ? value : 'agent'
}

function normalizeVisibility(value?: string): PrincipalVisibility {
  return value === 'shared' || value === 'private' || value === 'all' ? value : 'all'
}

function clonePrincipalScope(scope: PrincipalMemoryScope): PrincipalMemoryScope {
  return {
    principal: scope.principal.trim(),
    principalKind: normalizePrincipalKind(scope.principalKind),
    project: scope.project.trim(),
    domain: scope.domain.trim(),
    visibility: normalizeVisibility(scope.visibility),
    includePrivate: Boolean(scope.includePrivate),
    limit: clampPrincipalLimit(scope.limit),
  }
}

function resolvePrincipalProject(project: string, currentProject = ''): string {
  const normalizedProject = project.trim()
  if (normalizedProject === PRINCIPAL_CURRENT_PROJECT) {
    const resolvedCurrentProject = clean(currentProject)
    return resolvedCurrentProject && resolvedCurrentProject !== 'all' ? resolvedCurrentProject : ''
  }
  return normalizedProject
}

function principalQueryScope(scope: PrincipalMemoryScope, currentProject = ''): PrincipalMemoryScope {
  const normalized = clonePrincipalScope(scope)
  return {
    ...normalized,
    project: resolvePrincipalProject(normalized.project, currentProject),
  }
}

export function principalMemoryQueryPath(scope: PrincipalMemoryScope, currentProject = ''): string {
  const normalized = principalQueryScope(scope, currentProject)
  const params = new URLSearchParams()
  params.set('principal', normalized.principal)
  params.set('principal_kind', normalized.principalKind)
  params.set('visibility', normalized.visibility)
  params.set('limit', String(normalized.limit))
  if (normalized.project && normalized.project !== 'all') params.set('project', normalized.project)
  if (normalized.domain && normalized.domain !== 'all') params.set('domain', normalized.domain)
  if (normalized.includePrivate || normalized.visibility === 'private') params.set('include_private', 'true')
  return `/api/memories/principal?${params.toString()}`
}

function emptyPrincipalSummary(scope: PrincipalMemoryScope): OperatorPrincipalMemorySummary {
  const normalized = clonePrincipalScope(scope)
  return {
    scope: normalized,
    principal: normalized.principal,
    principalKind: normalized.principalKind,
    project: normalized.project,
    domain: normalized.domain,
    items: [],
    hiddenCount: 0,
    auditStatus: 'not_required',
    audit: { durable: false, action: 'principal_memory_query' },
    source: 'principal-memory-query',
    freshness: 'live',
  }
}

function emptyPrincipalBrief(scope: PrincipalMemoryScope): OperatorPrincipalMemoryBrief {
  return {
    scope: clonePrincipalScope(scope),
    source: 'principal-scoped-brief',
    freshness: 'mustbuild',
    lines: [],
  }
}

function principalPayloadError(path: string, detail: string, payload: unknown): OperatorFetchError {
  const message = `Invalid principal memory payload from ${path}: ${detail}${payloadPreview(payload)}`
  return new OperatorFetchError(message, {
    message,
    source: 'principal-memory-query',
    path,
    method: 'GET',
    retryable: false,
  })
}

function mapPrincipalMemoryItem(row: ApiPrincipalMemoryItem): OperatorPrincipalMemoryItem {
  return {
    id: row.id === undefined || row.id === null ? '-' : String(row.id),
    project: clean(row.project) || 'global',
    content: clean(row.content) || '-',
    tags: Array.isArray(row.tags) ? row.tags.filter((tag): tag is string => typeof tag === 'string') : [],
    ownerPrincipal: clean(row.owner_principal),
    ownerPrincipalKind: clean(row.owner_principal_kind) || 'unknown',
    visibility: clean(row.agent_visibility) || 'shared',
    domain: clean(row.domain) || 'general',
    confidence: typeof row.confidence === 'number' ? row.confidence : null,
    createdAt: clean(row.created_at),
  }
}

function mapPrincipalMemoryResponse(
  payload: unknown,
  path: string,
  scope: PrincipalMemoryScope,
): OperatorPrincipalMemorySummary {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw principalPayloadError(path, 'expected an object response', payload)
  }

  const response = payload as ApiPrincipalMemoryQueryResponse
  if (!Array.isArray(response.items)) {
    throw principalPayloadError(path, 'expected items array', payload)
  }

  const normalizedScope = clonePrincipalScope({
    ...scope,
    principal: clean(response.principal) || scope.principal,
    principalKind: normalizePrincipalKind(clean(response.principal_kind) || scope.principalKind),
    project: clean(response.project) || scope.project,
    domain: clean(response.domain) || scope.domain,
  })

  return {
    scope: normalizedScope,
    principal: normalizedScope.principal,
    principalKind: normalizedScope.principalKind,
    project: normalizedScope.project,
    domain: normalizedScope.domain,
    items: response.items.map(mapPrincipalMemoryItem),
    hiddenCount: typeof response.hidden_count === 'number' ? response.hidden_count : 0,
    auditStatus: clean(response.audit_status) || 'not_required',
    audit: {
      durable: Boolean(response.audit?.durable),
      action: clean(response.audit?.action) || 'principal_memory_query',
    },
    source: 'principal-memory-query',
    freshness: 'live',
  }
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

function ageDays(timestamp?: string): number | null {
  if (!timestamp) return null

  const value = Date.parse(timestamp)
  if (Number.isNaN(value)) return null

  return Math.max(0, Math.floor((Date.now() - value) / 86_400_000))
}

function mapTier(value?: string): Memory['tier'] {
  if (value === 'semantic' || value === 'episodic' || value === 'procedural') {
    return value
  }

  return 'procedural'
}

function mapStatus(row: ApiMemory): Memory['status'] {
  const normalized = String(row.status || '').trim().toLowerCase()
  if (normalized === 'active' || normalized === 'flagged' || normalized === 'superseded' || normalized === 'archived') {
    return normalized
  }
  if (normalized === 'noise' || normalized === 'suppressed') {
    return 'flagged'
  }
  if (row.superseded_by !== undefined && row.superseded_by !== null && String(row.superseded_by).trim()) {
    return 'superseded'
  }

  return 'active'
}

function isNoiseStatus(value?: string): boolean {
  const normalized = String(value || '').trim().toLowerCase()
  return normalized === 'noise' || normalized === 'flagged' || normalized === 'suppressed'
}

function mapMemoryRow(row: ApiMemory): Memory {
  const updatedAt = row.updated_at || row.created_at
  const hasConfidence = typeof row.confidence === 'number'
  const hasCitationCount = Object.prototype.hasOwnProperty.call(row, 'citation_count')
  const hasInjectionCount = Object.prototype.hasOwnProperty.call(row, 'injection_count')

  return {
    id: String(row.id),
    content: row.content || '-',
    tags: Array.isArray(row.tags) ? row.tags : [],
    status: mapStatus(row),
    tier: mapTier(row.tier),
    type: row.epistemic_type || 'note',
    project: row.project || 'global',
    conf: hasConfidence ? row.confidence as number : 1,
    confidenceKnown: hasConfidence,
    cite: typeof row.citation_count === 'number' ? row.citation_count : 0,
    inj: typeof row.injection_count === 'number' ? row.injection_count : 0,
    utilityKnown: hasCitationCount || hasInjectionCount,
    age: compactAge(updatedAt),
    ageDays: ageDays(updatedAt),
    supersededBy: row.superseded_by === undefined || row.superseded_by === null ? undefined : String(row.superseded_by),
    sourceSessions: Array.isArray(row.source_sessions) ? row.source_sessions : [],
    noise: isNoiseStatus(row.status),
  }
}

function payloadPreview(payload: unknown): string {
  if (typeof payload === 'string') {
    const compact = payload.trim().replace(/\s+/g, ' ')
    return compact ? `: ${compact.slice(0, 120)}${compact.length > 120 ? '...' : ''}` : ''
  }
  if (payload === undefined || payload === null) {
    return ''
  }
  try {
    const compact = JSON.stringify(payload).replace(/\s+/g, ' ')
    return compact ? `: ${compact.slice(0, 120)}${compact.length > 120 ? '...' : ''}` : ''
  } catch {
    return `: [unserializable ${typeof payload}]`
  }
}

function memoryPayloadError(path: string, detail: string, payload: unknown): OperatorFetchError {
  const message = `Invalid memory payload from ${path}: ${detail}${payloadPreview(payload)}`
  return new OperatorFetchError(message, {
    message,
    source: 'memory-list',
    path,
    method: 'GET',
    retryable: false,
  })
}

function parseMemoryArray(payload: unknown, path: string): ApiMemory[] {
  if (Array.isArray(payload)) {
    return payload as ApiMemory[]
  }

  if (typeof payload === 'string' && payload.trim()) {
    let parsed: unknown
    try {
      parsed = JSON.parse(payload)
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error)
      throw memoryPayloadError(path, `invalid JSON (${detail})`, payload)
    }

    if (Array.isArray(parsed)) {
      return parsed as ApiMemory[]
    }

    throw memoryPayloadError(path, 'expected a JSON array', parsed)
  }

  throw memoryPayloadError(path, 'expected a JSON array', payload)
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorMemoryLab] ${key} live load failed`, error)
      }
    })
  }
}

async function loadMemoryRows(): Promise<Memory[]> {
  const projectState = await loadOperatorJson<string[]>('/api/projects', {
    source: 'memory-projects',
    empty: (rows) => !Array.isArray(rows) || !rows.length,
  })

  if (projectState.kind === 'error') {
    throw projectState.error
  }

  const projects = projectState.kind === 'live' || projectState.kind === 'empty'
    ? projectState.data || []
    : []
  const projectRows = await Promise.all(projects.map(async (project) => {
    const path = `/api/memories?project=${encodeURIComponent(project)}&limit=${MEMORY_LIST_LIMIT}`
    const payload = await operatorFetchJson<unknown>(path, undefined, 'memory-list')
    return parseMemoryArray(payload, path).map(mapMemoryRow)
  }))
  const combined = projectRows.flat()

  const deduped = new Map<string, Memory>()
  for (const row of combined) {
    deduped.set(`${row.project}:${row.id}`, row)
  }

  return [...deduped.values()]
}

export function useOperatorPrincipalMemorySurface(currentProject?: Ref<string>): {
  scope: Ref<PrincipalMemoryScope>
  loadState: ComputedRef<OperatorLoadState<OperatorPrincipalMemorySummary>>
  briefState: ComputedRef<OperatorLoadState<OperatorPrincipalMemoryBrief>>
  attributionVisible: Ref<boolean>
  riskyConfirmation: Ref<boolean>
  principalOptions: ComputedRef<string[]>
  domainOptions: ComputedRef<string[]>
  refresh: () => Promise<void>
  refreshBrief: () => void
} {
  const scope = useState<PrincipalMemoryScope>('live:principal-memory:scope', blankPrincipalScope)
  const queryEvidence = endpointEvidence('/api/memories/principal?principal={principal}', 'principal-memory-query')
  const state = useState<OperatorLoadState<OperatorPrincipalMemorySummary>>(
    'live:principal-memory:state',
    () => gatedState(queryEvidence, 'principal-select', 'Select a principal before issuing a scoped query.', emptyPrincipalSummary(scope.value)),
  )
  const briefEvidence = endpointEvidence('MCP get_memory_brief', 'principal-scoped-brief')
  const briefStateRef = useState<OperatorLoadState<OperatorPrincipalMemoryBrief>>(
    'live:principal-memory:brief-state',
    () => mustBuildState<OperatorPrincipalMemoryBrief>(briefEvidence, PRINCIPAL_BRIEF_REASON, emptyPrincipalBrief(scope.value)),
  )
  const attributionVisible = useState<boolean>('live:principal-memory:attribution-visible', () => true)
  const riskyConfirmation = useState<boolean>('live:principal-memory:risky-confirmation', () => false)
  const confirmedPrincipal = useState<string>('live:principal-memory:confirmed-principal', () => '')

  const loadState = computed(() => state.value)
  const briefState = computed(() => briefStateRef.value)
  const principalOptions = computed(() => {
    const values = new Set<string>()
    if (scope.value.principal.trim()) values.add(scope.value.principal.trim())
    for (const item of state.value.data?.items || []) {
      if (item.ownerPrincipal) values.add(item.ownerPrincipal)
    }
    return [...values].sort()
  })
  const domainOptions = computed(() => {
    const values = new Set<string>()
    if (scope.value.domain.trim()) values.add(scope.value.domain.trim())
    for (const item of state.value.data?.items || []) {
      if (item.domain) values.add(item.domain)
    }
    return [...values].sort()
  })

  function refreshBrief() {
    briefStateRef.value = mustBuildState<OperatorPrincipalMemoryBrief>(
      endpointEvidence('MCP get_memory_brief', 'principal-scoped-brief', {
        reason: PRINCIPAL_BRIEF_REASON,
      }),
      PRINCIPAL_BRIEF_REASON,
      emptyPrincipalBrief(scope.value),
    )
  }

  async function refresh(forceWiden = false) {
    const normalizedScope = clonePrincipalScope(scope.value)
    const currentProjectValue = currentProject?.value || ''
    const queryScope = principalQueryScope(normalizedScope, currentProjectValue)
    scope.value.limit = normalizedScope.limit
    scope.value.principalKind = normalizedScope.principalKind
    scope.value.visibility = normalizedScope.visibility
    scope.value.project = normalizedScope.project || PRINCIPAL_CURRENT_PROJECT

    if (!normalizedScope.principal) {
      riskyConfirmation.value = false
      state.value = gatedState(
        queryEvidence,
        'principal-select',
        'Select a principal before issuing a scoped query.',
        state.value.data || emptyPrincipalSummary(queryScope),
      )
      refreshBrief()
      return
    }

    if (normalizedScope.project === PRINCIPAL_CURRENT_PROJECT && !queryScope.project) {
      riskyConfirmation.value = false
      state.value = gatedState(
        endpointEvidence('/api/memories/principal', 'principal-memory-query', {
          reason: 'Current project scope needs a concrete project before querying.',
        }),
        'project-scope',
        'Select a concrete project before using current project scope.',
        state.value.data || emptyPrincipalSummary(queryScope),
      )
      refreshBrief()
      return
    }

    if (
      confirmedPrincipal.value &&
      confirmedPrincipal.value !== normalizedScope.principal &&
      !forceWiden &&
      !riskyConfirmation.value
    ) {
      riskyConfirmation.value = true
      state.value = staleState(
        endpointEvidence('/api/memories/principal', 'principal-memory-query', {
          reason: 'Changing principal scope needs explicit confirmation before a broader read.',
        }),
        'Changing principal scope needs explicit confirmation before a broader read.',
        state.value.data || emptyPrincipalSummary(queryScope),
      )
      refreshBrief()
      return
    }

    riskyConfirmation.value = false
    confirmedPrincipal.value = normalizedScope.principal

    const path = principalMemoryQueryPath(normalizedScope, currentProjectValue)
    const evidence = endpointEvidence(path, 'principal-memory-query')
    state.value = pendingState(evidence, state.value.data || emptyPrincipalSummary(queryScope))
    try {
      const payload = await operatorFetchJson<unknown>(path, undefined, 'principal-memory-query')
      const summary = mapPrincipalMemoryResponse(payload, path, queryScope)
      state.value = summary.items.length
        ? liveState(evidence, summary)
        : emptyState(evidence, summary)
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'principal-memory-query',
        path,
        method: 'GET',
      })
      state.value = errorState(evidence, mapped, {
        source: 'principal-memory-query',
        run: async () => {
          await refresh(true)
          return state.value
        },
      }, state.value.data || emptyPrincipalSummary(queryScope))
    } finally {
      refreshBrief()
    }
  }

  startOnce('principal-memory', refresh)

  return {
    scope,
    loadState,
    briefState,
    attributionVisible,
    riskyConfirmation,
    principalOptions,
    domainOptions,
    refresh,
    refreshBrief,
  }
}

export function useOperatorMemoryLab(): {
  rows: Memory[]
  loadState: ComputedRef<OperatorLoadState<Memory[]>>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  refresh: () => Promise<void>
  storeMemory: (input: StoreMemoryInput) => Promise<OperatorMutationResult<Memory>>
  deleteMemory: (id: string) => Promise<OperatorMutationResult<unknown>>
  suppressMemory: (id: string, reason?: string) => Promise<OperatorMutationResult<MemoryActionReceipt>>
  suppressMemories: (ids: string[], reason?: string) => Promise<OperatorMutationResult<MemoryActionReceipt[]>>
  auditMemory: (id: string, limit?: number) => Promise<OperatorLoadState<MemoryAuditResponse>>
  provenanceGap: ReturnType<typeof unsupportedOperatorAction>
  actionGaps: readonly MemoryActionGap[]
} {
  const evidence = endpointEvidence(`/api/memories?project={project}&limit=${MEMORY_LIST_LIMIT}`, 'memory-list')
  const rowsState = useState<Memory[]>('live:memory-lab:rows', () => [])
  const state = useState<OperatorLoadState<Memory[]>>('live:memory-lab:state', () => pendingState(evidence, rowsState.value))

  const loadState = computed(() => state.value)
  const pending = computed(() => state.value.kind === 'pending')
  const error = computed(() => state.value.kind === 'error' ? state.value.error.message : null)

  async function refresh() {
    state.value = pendingState(evidence, rowsState.value)
    try {
      const nextRows = await loadMemoryRows()
      replaceArray(rowsState.value, nextRows)
      state.value = nextRows.length
        ? liveState(evidence, rowsState.value)
        : emptyState(evidence, rowsState.value)
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'memory-list',
        path: evidence.endpoint,
        method: 'GET',
      })
      state.value = errorState(evidence, mapped, {
        source: 'memory-list',
        run: async () => {
          await refresh()
          return state.value
        },
      }, rowsState.value)
    }
  }

  async function storeMemory(input: StoreMemoryInput) {
    return runOperatorMutation({
      action: 'memory-store',
      evidence: endpointEvidence('/api/memories', 'memory-store'),
      snapshot: () => [...rowsState.value],
      run: async () => {
        const row = await operatorFetchJson<ApiMemory>('/api/memories', jsonInit('POST', {
          project: input.project,
          content: input.content,
          tags: input.tags || [],
          source_agent: 'operator-console',
        }), 'memory-store')
        return mapMemoryRow(row)
      },
      rollback: (snapshot) => {
        replaceArray(rowsState.value, snapshot || [])
      },
      refresh,
    })
  }

  async function deleteMemory(id: string) {
    return runOperatorMutation({
      action: 'memory-delete',
      evidence: endpointEvidence(`/api/memories/${id}`, 'memory-delete'),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        replaceArray(rowsState.value, rowsState.value.filter((row) => row.id !== id))
      },
      run: () => operatorFetchJson(`/api/memories/${id}`, jsonInit('DELETE'), 'memory-delete'),
      rollback: (snapshot) => {
        replaceArray(rowsState.value, snapshot || [])
      },
      refresh,
    })
  }

  async function suppressMemory(id: string, reason = 'operator marked as noise') {
    return runOperatorMutation({
      action: 'memory-suppress',
      evidence: endpointEvidence(`/api/memories/${id}/suppress`, 'memory-suppress'),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        replaceArray(rowsState.value, rowsState.value.filter((row) => row.id !== id))
      },
      run: () => operatorFetchJson<MemoryActionReceipt>(`/api/memories/${id}/suppress`, jsonInit('POST', { reason }), 'memory-suppress'),
      rollback: (snapshot) => {
        replaceArray(rowsState.value, snapshot || [])
      },
      refresh,
    })
  }

  async function suppressMemories(ids: string[], reason = 'operator bulk marked as noise') {
    const uniqueIds = [...new Set(ids)].filter(Boolean)
    return runOperatorMutation({
      action: 'memory-bulk-suppress',
      evidence: endpointEvidence('/api/memories/suppress', 'memory-bulk-suppress', {
        reason: 'Bulk suppression validates the selected memory IDs before applying soft-delete semantics.',
      }),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        const suppressed = new Set(uniqueIds)
        replaceArray(rowsState.value, rowsState.value.filter((row) => !suppressed.has(row.id)))
      },
      run: () => {
        const numericIds = uniqueIds.map((id) => Number.parseInt(id, 10))
        if (numericIds.some((id) => !Number.isInteger(id) || id <= 0)) {
          throw new OperatorFetchError('Bulk suppression requires numeric memory IDs', {
            message: 'Bulk suppression requires numeric memory IDs',
            source: 'memory-bulk-suppress',
            path: '/api/memories/suppress',
            method: 'POST',
            retryable: false,
          })
        }
        return operatorFetchJson<MemoryActionReceipt[]>('/api/memories/suppress', jsonInit('POST', { ids: numericIds, reason }), 'memory-bulk-suppress')
      },
      rollback: (snapshot) => {
        replaceArray(rowsState.value, snapshot || [])
      },
      refresh,
    })
  }

  async function auditMemory(id: string, limit = 10) {
    const normalizedLimit = Number.isInteger(limit) && limit > 0 ? Math.min(200, limit) : 10
    const path = `/api/memories/${encodeURIComponent(id)}/audit?limit=${normalizedLimit}`
    return loadOperatorJson<MemoryAuditResponse>(path, {
      source: 'memory-audit',
      empty: (data) => !data.entries.length,
    })
  }

  const provenanceGap = unsupportedOperatorAction(
    'memory-provenance',
    'GET /api/memories/{id}/provenance',
    'Memory provenance is not exposed by the current server API.',
  )

  startOnce('memory-lab', refresh)

  return {
    rows: rowsState.value,
    loadState,
    pending,
    error,
    refresh,
    storeMemory,
    deleteMemory,
    suppressMemory,
    suppressMemories,
    auditMemory,
    provenanceGap,
    actionGaps: memoryActionGaps,
  }
}
