import type { Observation, UserPrompt, SessionSummary, Stats, FeedItem, ObservationFeedItem, PromptFeedItem, SummaryFeedItem, RelationWithDetails, RelationGraph, RelationStats, GraphStats, VectorMetrics, ContextSearchResponse, DecisionSearchResponse, SDKSessionListResponse } from '@/types'

const API_BASE = '/api'
const DEFAULT_TIMEOUT = 10000 // 10 seconds
const MAX_RETRIES = 3
const RETRY_DELAY = 1000 // 1 second base delay

interface FetchOptions {
  timeout?: number
  signal?: AbortSignal
  retries?: number
}

async function fetchJson<T>(url: string, options: FetchOptions = {}): Promise<T> {
  const { timeout = DEFAULT_TIMEOUT, signal } = options

  // Create timeout abort controller
  const timeoutController = new AbortController()
  const timeoutId = setTimeout(() => timeoutController.abort(), timeout)

  // Combine signals if both provided
  const combinedSignal = signal
    ? combineAbortSignals(signal, timeoutController.signal)
    : timeoutController.signal

  try {
    const response = await fetch(url, { signal: combinedSignal })
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`)
    }
    return response.json()
  } catch (err) {
    // Re-throw abort errors (user cancellation)
    if (err instanceof Error && err.name === 'AbortError') {
      // Check if it was a timeout vs user abort
      if (signal?.aborted) {
        throw err // User cancelled
      }
      throw new Error('Request timed out')
    }
    throw err
  } finally {
    clearTimeout(timeoutId)
  }
}

// Helper to combine multiple abort signals
function combineAbortSignals(...signals: AbortSignal[]): AbortSignal {
  const controller = new AbortController()
  for (const signal of signals) {
    if (signal.aborted) {
      controller.abort()
      break
    }
    signal.addEventListener('abort', () => controller.abort(), { once: true })
  }
  return controller.signal
}

// Fetch with retry logic
async function fetchWithRetry<T>(url: string, options: FetchOptions = {}): Promise<T> {
  const { retries = MAX_RETRIES, ...fetchOptions } = options
  let lastError: Error | null = null

  for (let attempt = 0; attempt < retries; attempt++) {
    try {
      return await fetchJson<T>(url, fetchOptions)
    } catch (err) {
      lastError = err instanceof Error ? err : new Error(String(err))

      // Don't retry on user abort
      if (lastError.name === 'AbortError') {
        throw lastError
      }

      // Don't retry on client errors (4xx)
      if (lastError.message.includes('HTTP 4')) {
        throw lastError
      }

      // Wait before retry (exponential backoff)
      if (attempt < retries - 1) {
        const delay = Math.min(RETRY_DELAY * Math.pow(2, attempt), 5000)
        await new Promise(r => setTimeout(r, delay))
      }
    }
  }

  throw lastError!
}

interface ObservationsResponse {
  observations: Observation[]
  total: number
  limit: number
  offset: number
  hasMore: boolean
}

export async function fetchObservations(limit: number = 100, project?: string, signal?: AbortSignal): Promise<Observation[]> {
  const params = new URLSearchParams({ limit: String(limit) })
  if (project) params.append('project', project)
  const response = await fetchWithRetry<ObservationsResponse>(`${API_BASE}/observations?${params}`, { signal })
  return response.observations || []
}

export async function fetchPrompts(limit: number = 100, project?: string, signal?: AbortSignal): Promise<UserPrompt[]> {
  const params = new URLSearchParams({ limit: String(limit) })
  if (project) params.append('project', project)
  return fetchWithRetry<UserPrompt[]>(`${API_BASE}/prompts?${params}`, { signal })
}

export async function fetchSummaries(limit: number = 50, project?: string, signal?: AbortSignal): Promise<SessionSummary[]> {
  const params = new URLSearchParams({ limit: String(limit) })
  if (project) params.append('project', project)
  return fetchWithRetry<SessionSummary[]>(`${API_BASE}/summaries?${params}`, { signal })
}

export async function fetchStats(project?: string | null): Promise<Stats> {
  const params = new URLSearchParams()
  if (project) params.append('project', project)
  const query = params.toString()
  return fetchWithRetry<Stats>(`${API_BASE}/stats${query ? '?' + query : ''}`)
}

export async function fetchProjects(): Promise<string[]> {
  return fetchWithRetry<string[]>(`${API_BASE}/projects`)
}

/**
 * Combine and sort all feed items by timestamp
 */
export function combineTimeline(
  observations: Observation[],
  prompts: UserPrompt[],
  summaries: SessionSummary[]
): FeedItem[] {
  const obsItems: ObservationFeedItem[] = observations.map(o => ({
    ...o,
    itemType: 'observation' as const,
    timestamp: new Date(o.created_at)
  }))

  const promptItems: PromptFeedItem[] = prompts.map(p => ({
    ...p,
    itemType: 'prompt' as const,
    timestamp: new Date(p.created_at)
  }))

  const summaryItems: SummaryFeedItem[] = summaries.map(s => ({
    ...s,
    itemType: 'summary' as const,
    timestamp: new Date(s.created_at)
  }))

  return [...obsItems, ...promptItems, ...summaryItems]
    .sort((a, b) => b.timestamp.getTime() - a.timestamp.getTime())
}

// Relation API functions
export async function fetchObservationRelations(observationId: number, signal?: AbortSignal): Promise<RelationWithDetails[]> {
  return fetchWithRetry<RelationWithDetails[]>(`${API_BASE}/observations/${observationId}/relations`, { signal })
}

export async function fetchObservationGraph(observationId: number, depth: number = 2, signal?: AbortSignal): Promise<RelationGraph> {
  return fetchWithRetry<RelationGraph>(`${API_BASE}/observations/${observationId}/graph?depth=${depth}`, { signal })
}

export async function fetchRelatedObservations(observationId: number, minConfidence: number = 0.4, signal?: AbortSignal): Promise<Observation[]> {
  return fetchWithRetry<Observation[]>(`${API_BASE}/observations/${observationId}/related?min_confidence=${minConfidence}`, { signal })
}

export async function fetchRelationStats(signal?: AbortSignal): Promise<RelationStats> {
  return fetchWithRetry<RelationStats>(`${API_BASE}/relations/stats`, { signal })
}

// Scoring API functions
export interface ScoreBreakdown {
  observation: {
    id: number
    title: string
    type: string
    project: string
    created_at: number
  }
  scoring: {
    final_score: number
    type_weight: number
    recency_decay: number
    core_score: number
    feedback_contrib: number
    concept_contrib: number
    retrieval_contrib: number
    age_days: number
  }
  explanation: {
    type_impact: string
    recency_impact: string
    feedback_impact: string
    concept_impact: string
    retrieval_impact: string
  }
}

export async function fetchObservationScore(observationId: number, signal?: AbortSignal): Promise<ScoreBreakdown> {
  return fetchWithRetry<ScoreBreakdown>(`${API_BASE}/observations/${observationId}/score`, { signal })
}

export interface FeedbackStats {
  total: number
  positive: number
  negative: number
  neutral: number
  avg_score: number
  avg_retrieval: number
}

export interface TopObservation {
  id: number
  title: string
  type: string
  importance_score: number
  retrieval_count?: number
}

export async function fetchScoringStats(project?: string, signal?: AbortSignal): Promise<FeedbackStats> {
  const params = new URLSearchParams()
  if (project) params.append('project', project)
  const query = params.toString()
  return fetchWithRetry<FeedbackStats>(`${API_BASE}/scoring/stats${query ? '?' + query : ''}`, { signal })
}

export async function fetchTopObservations(project?: string, limit: number = 10, signal?: AbortSignal): Promise<Observation[]> {
  const params = new URLSearchParams({ limit: String(limit) })
  if (project) params.append('project', project)
  return fetchWithRetry<Observation[]>(`${API_BASE}/observations/top?${params}`, { signal })
}

export async function fetchMostRetrievedObservations(project?: string, limit: number = 10, signal?: AbortSignal): Promise<Observation[]> {
  const params = new URLSearchParams({ limit: String(limit) })
  if (project) params.append('project', project)
  return fetchWithRetry<Observation[]>(`${API_BASE}/observations/most-retrieved?${params}`, { signal })
}

// Search Analytics API functions
export interface RecentQuery {
  query: string
  project?: string
  type?: string
  count: number
  last_used: string
}

export interface SearchAnalytics {
  total_searches: number
  vector_searches: number
  filter_searches: number
  cache_hits: number
  coalesced_requests: number
  search_errors: number
  avg_latency_ms: number
  avg_vector_latency_ms: number
  avg_filter_latency_ms: number
}

export async function fetchSearchAnalytics(since?: string, signal?: AbortSignal): Promise<SearchAnalytics> {
  const params = new URLSearchParams()
  if (since) params.append('since', since)
  const query = params.toString()
  return fetchWithRetry<SearchAnalytics>(`${API_BASE}/search/analytics${query ? '?' + query : ''}`, { signal })
}

interface RecentSearchResponse {
  queries: Array<{
    timestamp: string
    query: string
    project?: string
    type?: string
    results: number
    used_vector: boolean
  }>
  count: number
}

export async function fetchRecentSearches(limit: number = 20, since?: string, signal?: AbortSignal): Promise<RecentQuery[]> {
  const params = new URLSearchParams({ limit: String(limit) })
  if (since) params.append('since', since)
  const response = await fetchWithRetry<RecentSearchResponse>(`${API_BASE}/search/recent?${params}`, { signal })
  return (response.queries || []).map(q => ({
    query: q.query,
    project: q.project,
    type: q.type,
    count: q.results,
    last_used: q.timestamp,
  }))
}

// System health API
export interface ComponentHealth {
  name: string
  status: 'healthy' | 'degraded' | 'unhealthy'
  message?: string
  latency_ms?: number
}

export interface SystemHealth {
  status: 'healthy' | 'degraded' | 'unhealthy'
  version: string
  components: ComponentHealth[]
  warnings?: string[]
}

export async function fetchSystemHealth(signal?: AbortSignal): Promise<SystemHealth> {
  return fetchWithRetry<SystemHealth>(`${API_BASE}/selfcheck`, { signal })
}

export async function fetchGraphStats(signal?: AbortSignal): Promise<GraphStats> {
  return fetchWithRetry<GraphStats>(`${API_BASE}/graph/stats`, { signal })
}

export async function fetchVectorMetrics(signal?: AbortSignal): Promise<VectorMetrics> {
  return fetchWithRetry<VectorMetrics>(`${API_BASE}/vector/metrics`, { signal })
}

// POST JSON helper with retry logic
async function postJson<T>(url: string, body: unknown, options: FetchOptions = {}): Promise<T> {
  const { timeout = DEFAULT_TIMEOUT, signal, retries = MAX_RETRIES } = options
  let lastError: Error | null = null

  for (let attempt = 0; attempt < retries; attempt++) {
    const timeoutController = new AbortController()
    const timeoutId = setTimeout(() => timeoutController.abort(), timeout)
    const combinedSignal = signal
      ? combineAbortSignals(signal, timeoutController.signal)
      : timeoutController.signal

    try {
      const response = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
        signal: combinedSignal,
      })
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`)
      }
      return response.json()
    } catch (err) {
      lastError = err instanceof Error ? err : new Error(String(err))
      if (lastError.name === 'AbortError') {
        if (signal?.aborted) throw lastError
        throw new Error('Request timed out')
      }
      if (lastError.message.includes('HTTP 4')) throw lastError
      if (attempt < retries - 1) {
        const delay = Math.min(RETRY_DELAY * Math.pow(2, attempt), 5000)
        await new Promise(r => setTimeout(r, delay))
      }
    } finally {
      clearTimeout(timeoutId)
    }
  }
  throw lastError!
}

// PUT JSON helper (single attempt, no retry for mutations)
async function putJson<T>(url: string, body: unknown, options: FetchOptions = {}): Promise<T> {
  const { timeout = DEFAULT_TIMEOUT, signal } = options
  const timeoutController = new AbortController()
  const timeoutId = setTimeout(() => timeoutController.abort(), timeout)
  const combinedSignal = signal
    ? combineAbortSignals(signal, timeoutController.signal)
    : timeoutController.signal

  try {
    const response = await fetch(url, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      signal: combinedSignal,
    })
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`)
    }
    return response.json()
  } catch (err) {
    if (err instanceof Error && err.name === 'AbortError') {
      if (signal?.aborted) throw err
      throw new Error('Request timed out')
    }
    throw err
  } finally {
    clearTimeout(timeoutId)
  }
}

// Observation CRUD
export async function fetchObservationById(id: number, signal?: AbortSignal): Promise<Observation> {
  return fetchWithRetry<Observation>(`${API_BASE}/observations/${id}`, { signal })
}

export async function fetchObservationsPaginated(
  params: { limit?: number; offset?: number; project?: string },
  signal?: AbortSignal
): Promise<ObservationsResponse> {
  const searchParams = new URLSearchParams()
  if (params.limit) searchParams.append('limit', String(params.limit))
  if (params.offset) searchParams.append('offset', String(params.offset))
  if (params.project) searchParams.append('project', params.project)
  return fetchWithRetry<ObservationsResponse>(`${API_BASE}/observations?${searchParams}`, { signal })
}

export async function updateObservation(
  id: number,
  updates: {
    title?: string
    subtitle?: string
    narrative?: string
    scope?: string
    facts?: string[]
    concepts?: string[]
  },
  signal?: AbortSignal
): Promise<{ observation: Observation; message: string }> {
  return putJson(`${API_BASE}/observations/${id}`, updates, { signal })
}

export async function archiveObservations(
  ids: number[],
  reason?: string,
  signal?: AbortSignal
): Promise<{ archived: number[]; failed: number[]; errors?: string[] }> {
  return postJson(`${API_BASE}/observations/archive`, { ids, reason }, { signal })
}

export async function submitObservationFeedback(
  id: number,
  feedback: number,
  signal?: AbortSignal
): Promise<{ score?: number }> {
  return postJson(`${API_BASE}/observations/${id}/feedback`, { feedback }, { signal })
}

// Search
export async function searchObservations(
  params: { query: string; project: string; limit?: number },
  signal?: AbortSignal
): Promise<ContextSearchResponse> {
  return postJson(`${API_BASE}/context/search`, params, { signal })
}

export async function searchDecisions(
  params: { query: string; project: string; limit?: number },
  signal?: AbortSignal
): Promise<DecisionSearchResponse> {
  return postJson(`${API_BASE}/decisions/search`, params, { signal })
}

// DELETE helper (single attempt, no retry for mutations)
async function deleteJson<T>(url: string, options: FetchOptions = {}): Promise<T> {
  const { timeout = DEFAULT_TIMEOUT, signal } = options
  const timeoutController = new AbortController()
  const timeoutId = setTimeout(() => timeoutController.abort(), timeout)
  const combinedSignal = signal
    ? combineAbortSignals(signal, timeoutController.signal)
    : timeoutController.signal

  try {
    const response = await fetch(url, {
      method: 'DELETE',
      signal: combinedSignal,
    })
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`)
    }
    const text = await response.text()
    return text ? JSON.parse(text) : ({} as T)
  } catch (err) {
    if (err instanceof Error && err.name === 'AbortError') {
      if (signal?.aborted) throw err
      throw new Error('Request timed out')
    }
    throw err
  } finally {
    clearTimeout(timeoutId)
  }
}

// ============================================================
// Vault API
// ============================================================

export interface VaultCredential {
  name: string
  scope: string
  created_at: string
  updated_at?: string
}

export interface VaultStatus {
  encrypted: boolean
  key_fingerprint?: string
  credential_count: number
}

export async function fetchVaultStatus(signal?: AbortSignal): Promise<VaultStatus> {
  const raw = await fetchWithRetry<any>(`${API_BASE}/vault/status`, { signal })
  return {
    encrypted: raw.key_configured ?? false,
    key_fingerprint: raw.fingerprint ?? raw.key_fingerprint,
    credential_count: raw.credential_count ?? 0,
  }
}

export async function fetchCredentials(signal?: AbortSignal): Promise<VaultCredential[]> {
  return fetchWithRetry<VaultCredential[]>(`${API_BASE}/vault/credentials`, { signal })
}

export async function fetchCredential(name: string, signal?: AbortSignal): Promise<{ name: string; value: string }> {
  return fetchWithRetry<{ name: string; value: string }>(`${API_BASE}/vault/credentials/${encodeURIComponent(name)}`, { signal })
}

export async function deleteCredential(name: string, signal?: AbortSignal): Promise<void> {
  await deleteJson<Record<string, unknown>>(`${API_BASE}/vault/credentials/${encodeURIComponent(name)}`, { signal })
}

// ============================================================
// Tokens API
// ============================================================

export interface ApiToken {
  id: string
  name: string
  token_prefix: string
  scope: string
  created_at: string
  last_used_at?: string
  request_count: number
  error_count?: number
  revoked: boolean
  revoked_at?: string
}

export interface CreateTokenResponse {
  token: string
  name: string
  prefix: string
}

export async function fetchTokens(signal?: AbortSignal): Promise<ApiToken[]> {
  const response = await fetchWithRetry<{ tokens: ApiToken[] }>(`${API_BASE}/auth/tokens`, { signal })
  return response.tokens
}

export async function createToken(
  params: { name: string; scope: string },
  signal?: AbortSignal
): Promise<CreateTokenResponse> {
  return postJson<CreateTokenResponse>(`${API_BASE}/auth/tokens`, params, { signal })
}

export async function revokeToken(id: string, signal?: AbortSignal): Promise<void> {
  await deleteJson<Record<string, unknown>>(`${API_BASE}/auth/tokens/${encodeURIComponent(id)}`, { signal })
}

// ============================================================
// Analytics API
// ============================================================

export interface SearchMiss {
  query: string
  project?: string
  frequency: number
  last_seen: string
}

export async function fetchSearchMisses(signal?: AbortSignal): Promise<SearchMiss[]> {
  return postJson<SearchMiss[]>(`${API_BASE}/analytics/search-misses`, {}, { signal })
}

export interface RetrievalStatsResponse {
  total_requests: number
  observations_served: number
  search_requests: number
  context_injections: number
  stale_excluded: number
  fresh_count: number
  duplicates_removed: number
}

export async function fetchRetrievalStats(project?: string, since?: string, signal?: AbortSignal): Promise<RetrievalStatsResponse> {
  const params = new URLSearchParams()
  if (project) params.append('project', project)
  if (since) params.append('since', since)
  const query = params.toString()
  return fetchWithRetry<RetrievalStatsResponse>(`${API_BASE}/stats/retrieval${query ? '?' + query : ''}`, { signal })
}

// ============================================================
// Patterns API
// ============================================================

export interface Pattern {
  id: number
  name: string
  type: string
  frequency: number
  confidence: number
  created_at: string
  last_seen_at: string
  last_seen_at_epoch: number
}

export interface PatternInsight {
  id: number
  insight: string
  examples?: string[]
}

export interface PatternsResponse {
  patterns: Pattern[]
  total: number
}

export async function fetchPatterns(
  params?: { limit?: number; offset?: number; sort?: string },
  signal?: AbortSignal
): Promise<PatternsResponse> {
  const searchParams = new URLSearchParams()
  if (params?.limit !== undefined) searchParams.append('limit', String(params.limit))
  if (params?.offset !== undefined) searchParams.append('offset', String(params.offset))
  if (params?.sort) searchParams.append('sort', params.sort)
  const query = searchParams.toString()
  return fetchWithRetry<PatternsResponse>(`${API_BASE}/patterns${query ? '?' + query : ''}`, { signal })
}

export async function fetchPatternInsight(id: number, signal?: AbortSignal): Promise<PatternInsight> {
  return fetchWithRetry<PatternInsight>(`${API_BASE}/patterns/${id}/insight`, { signal })
}

export async function deprecatePattern(id: number, signal?: AbortSignal): Promise<void> {
  await postJson<Record<string, unknown>>(`${API_BASE}/patterns/${id}/deprecate`, {}, { signal })
}

export async function deletePattern(id: number, signal?: AbortSignal): Promise<void> {
  await deleteJson<Record<string, unknown>>(`${API_BASE}/patterns/${id}`, { signal })
}

export async function mergePatterns(ids: number[], signal?: AbortSignal): Promise<void> {
  await postJson<Record<string, unknown>>(`${API_BASE}/patterns/merge`, { ids }, { signal })
}

// ============================================================
// Sessions Index API
// ============================================================

export interface IndexedSession {
  id: string
  workstation: string
  project: string
  date: string
  message_count: number
  created_at: string
}

// Raw shape returned by the Go handler
interface RawIndexedSession {
  id: string
  workstation_id: string
  project_id: string
  project_path?: string
  exchange_count: number
  git_branch?: string
  last_msg_at?: string
}

function mapRawSession(raw: RawIndexedSession): IndexedSession {
  const lastMsg = raw.last_msg_at ? new Date(raw.last_msg_at) : null
  return {
    id: raw.id,
    workstation: raw.workstation_id,
    project: raw.project_path || raw.project_id,
    date: lastMsg ? lastMsg.toISOString().slice(0, 10) : '',
    message_count: raw.exchange_count,
    created_at: raw.last_msg_at || '',
  }
}

export async function fetchIndexedSessions(
  params?: { project?: string; from?: string; to?: string },
  signal?: AbortSignal
): Promise<IndexedSession[]> {
  const searchParams = new URLSearchParams()
  if (params?.project) searchParams.append('project', params.project)
  if (params?.from) searchParams.append('from', params.from)
  if (params?.to) searchParams.append('to', params.to)
  const query = searchParams.toString()
  const raw = await fetchWithRetry<RawIndexedSession[]>(`${API_BASE}/sessions-index${query ? '?' + query : ''}`, { signal })
  return (raw || []).map(mapRawSession)
}

// Raw shape for search results (slightly different from list — includes rank/snippet)
interface RawSessionSearchResult {
  id: string
  workstation_id: string
  project_path?: string
  exchange_count: number
  rank: number
  snippet?: string
}

export async function searchIndexedSessions(
  query: string,
  signal?: AbortSignal
): Promise<IndexedSession[]> {
  const raw = await fetchWithRetry<RawSessionSearchResult[]>(`${API_BASE}/sessions-index/search?query=${encodeURIComponent(query)}`, { signal })
  return (raw || []).map(r => ({
    id: r.id,
    workstation: r.workstation_id,
    project: r.project_path || '',
    date: '',
    message_count: r.exchange_count,
    created_at: '',
  }))
}

export async function fetchSDKSessions(
  params?: { project?: string; limit?: number; offset?: number },
  signal?: AbortSignal
): Promise<SDKSessionListResponse> {
  const query = new URLSearchParams()
  if (params?.project) query.set('project', params.project)
  if (params?.limit) query.set('limit', String(params.limit))
  if (params?.offset) query.set('offset', String(params.offset))

  const url = `${API_BASE}/sessions/list${query.toString() ? '?' + query.toString() : ''}`
  return fetchWithRetry<SDKSessionListResponse>(url, { signal })
}

// ============================================================
// System / Maintenance API
// ============================================================

export async function triggerConsolidation(signal?: AbortSignal): Promise<{ message: string }> {
  return postJson<{ message: string }>(`${API_BASE}/maintenance/consolidate`, {}, { signal })
}

export async function triggerMaintenance(signal?: AbortSignal): Promise<{ message: string }> {
  return postJson<{ message: string }>(`${API_BASE}/maintenance/run`, {}, { signal })
}

export interface MaintenanceStats {
  last_consolidation?: string
  last_maintenance?: string
  observations_consolidated?: number
  stale_removed?: number
}

export async function fetchMaintenanceStats(signal?: AbortSignal): Promise<MaintenanceStats> {
  return fetchWithRetry<MaintenanceStats>(`${API_BASE}/maintenance/stats`, { signal })
}

export async function checkForUpdate(signal?: AbortSignal): Promise<{
  available: boolean
  current_version: string
  latest_version: string
  release_notes?: string
}> {
  return fetchWithRetry(`${API_BASE}/update/check`, { signal })
}

// Observation tag management
export async function updateObservationTags(
  id: number,
  action: 'add' | 'remove',
  tag: string,
  signal?: AbortSignal
): Promise<{ concepts: string[] }> {
  return postJson<{ concepts: string[] }>(`${API_BASE}/observations/${id}/tags`, { action, tags: [tag] }, { signal })
}
