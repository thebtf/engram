import type { ComputedRef, Ref } from 'vue'
import type { OperatorLoadState } from './useOperatorApi'
import {
  emptyState,
  endpointEvidence,
  errorState,
  liveState,
  loadOperatorJson,
  operatorFetchJson,
  pendingState,
  staleState,
  toOperatorSourceError,
  unsupportedOperatorAction,
} from './useOperatorApi'

interface ApiObservation {
  id?: number | string
  project?: string
  type?: string
  memory_type?: string
  title?: unknown
  subtitle?: unknown
  narrative?: unknown
  content?: unknown
  similarity?: number
  created_at?: string
  concepts?: unknown
}

interface ApiContextSearch {
  project?: string
  query?: string
  intent?: string
  observations?: ApiObservation[]
  results?: ApiObservation[]
  memories?: ApiObservation[]
  relevant?: ApiObservation[]
  always_inject?: ApiObservation[]
  threshold?: number
  max_results?: number
  total_results?: number
  deprecated?: boolean
  message?: string
}

interface ApiSearchQuery {
  query?: string
  project?: string
  scope?: string
  result_count?: number
  latency_ms?: number
  created_at?: string
}

interface ApiSearchRecent {
  queries?: ApiSearchQuery[]
  count?: number
  project?: string
}

interface ApiSearchAnalytics {
  total_searches?: number
  searches_today?: number
  avg_latency_ms?: number
  zero_result_rate?: number
  vector_searches?: number
  filter_searches?: number
  cache_hits?: number
  search_errors?: number
}

interface ApiRetrievalStats {
  total_requests?: number
  observations_served?: number
  search_requests?: number
  context_injections?: number
  stale_excluded?: number
  fresh_count?: number
  duplicates_removed?: number
  verified_stale?: number
  deleted_invalid?: number
  last_updated?: number
}

interface ApiStatsVNext {
  injection_count?: number
  citation_count?: number
  uncited_count?: number
  noise_ratio?: number
  generated_at?: string
  outcomes?: {
    total_sessions?: number
    unrecorded_sessions?: number
    unrecorded_fraction?: number
    by_outcome?: Record<string, number>
  }
}

export interface OperatorSearchResult {
  id: string
  title: string
  body: string
  project: string
  type: string
  score: string
  createdAt: string
  memoryBacked: boolean
}

export interface OperatorRecentSearch {
  id: string
  query: string
  project: string
  resultCount: string
  latency: string
  createdAt: string
}

export interface OperatorNoiseMetric {
  label: string
  value: string
  endpoint: string
}

function replaceArray<T>(target: T[], next: readonly T[]) {
  target.splice(0, target.length, ...next)
}

function display(value: unknown): string {
  if (value === undefined || value === null || value === '') return '-'
  if (typeof value === 'number') return Number.isInteger(value) ? String(value) : value.toFixed(3)
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  return String(value)
}

function percent(value?: number): string {
  return typeof value === 'number' ? `${Math.round(value * 100)}%` : '-'
}

function textField(value: unknown): string {
  if (value === undefined || value === null) return ''
  if (typeof value === 'string') return value
  if (typeof value === 'object' && 'String' in value) {
    return String((value as { String?: string }).String || '')
  }
  return String(value)
}

function normalizeObservation(row: ApiObservation, index: number): OperatorSearchResult {
  const title = textField(row.title) || textField(row.subtitle) || textField(row.content) || `#${row.id ?? index + 1}`
  const body = textField(row.narrative) || textField(row.content) || textField(row.subtitle)
  return {
    id: String(row.id ?? `${row.project || 'result'}-${index}`),
    title,
    body,
    project: row.project || '-',
    type: row.type || row.memory_type || '-',
    score: typeof row.similarity === 'number' ? row.similarity.toFixed(3) : '-',
    createdAt: row.created_at || '-',
    memoryBacked: row.type === 'discovery' && row.memory_type === 'context',
  }
}

function normalizeRecent(row: ApiSearchQuery, index: number): OperatorRecentSearch {
  return {
    id: `${row.project || 'all'}-${row.created_at || index}-${row.query || index}`,
    query: row.query || '-',
    project: row.project || '-',
    resultCount: display(row.result_count),
    latency: row.latency_ms === undefined ? '-' : `${display(row.latency_ms)}ms`,
    createdAt: row.created_at || '-',
  }
}

function contextRows(payload: ApiContextSearch): OperatorSearchResult[] {
  const rows = payload.observations || payload.results || payload.memories || payload.relevant || []
  return rows.map(normalizeObservation)
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorSearchNoise] ${key} live load failed`, error)
      }
    })
  }
}

export function useOperatorSearchNoise(): {
  projects: Ref<string[]>
  selectedProject: Ref<string>
  searchResults: OperatorSearchResult[]
  recentRows: ComputedRef<OperatorRecentSearch[]>
  searchState: ComputedRef<OperatorLoadState<OperatorSearchResult[]>>
  recentState: ComputedRef<OperatorLoadState<ApiSearchRecent>>
  analyticsState: ComputedRef<OperatorLoadState<ApiSearchAnalytics>>
  retrievalState: ComputedRef<OperatorLoadState<ApiRetrievalStats>>
  vnextState: ComputedRef<OperatorLoadState<ApiStatsVNext>>
  noiseMetrics: ComputedRef<OperatorNoiseMetric[]>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  refresh: () => Promise<void>
  runSearch: (query: string, project?: string) => Promise<void>
  tombstoneGap: ReturnType<typeof unsupportedOperatorAction>
  noiseTrendGap: ReturnType<typeof unsupportedOperatorAction>
} {
  const projectsEvidence = endpointEvidence('/api/projects', 'search-projects')
  const searchEvidence = endpointEvidence('/api/context/search?project={project}&query={query}&limit=20', 'context-search')
  const recentEvidence = endpointEvidence('/api/search/recent', 'search-recent')
  const analyticsEvidence = endpointEvidence('/api/search/analytics', 'search-analytics')
  const retrievalEvidence = endpointEvidence('/api/stats/retrieval', 'retrieval-stats')
  const vnextEvidence = endpointEvidence('/api/stats/vnext', 'stats-vnext')

  const projects = useState<string[]>('live:search-noise:projects', () => [])
  const selectedProject = useState<string>('live:search-noise:selected-project', () => '')
  const searchResults = useState<OperatorSearchResult[]>('live:search-noise:results', () => [])

  const projectsStateValue = useState<OperatorLoadState<string[]>>('live:search-noise:projects-state', () => pendingState(projectsEvidence, projects.value))
  const searchStateValue = useState<OperatorLoadState<OperatorSearchResult[]>>('live:search-noise:search-state', () => emptyState(searchEvidence, searchResults.value))
  const recentStateValue = useState<OperatorLoadState<ApiSearchRecent>>('live:search-noise:recent-state', () => pendingState(recentEvidence))
  const analyticsStateValue = useState<OperatorLoadState<ApiSearchAnalytics>>('live:search-noise:analytics-state', () => pendingState(analyticsEvidence))
  const retrievalStateValue = useState<OperatorLoadState<ApiRetrievalStats>>('live:search-noise:retrieval-state', () => pendingState(retrievalEvidence))
  const vnextStateValue = useState<OperatorLoadState<ApiStatsVNext>>('live:search-noise:vnext-state', () => pendingState(vnextEvidence))
  let searchGeneration = 0

  const searchState = computed(() => searchStateValue.value)
  const recentState = computed(() => recentStateValue.value)
  const analyticsState = computed(() => analyticsStateValue.value)
  const retrievalState = computed(() => retrievalStateValue.value)
  const vnextState = computed(() => vnextStateValue.value)

  const recentRows = computed(() => {
    if (recentStateValue.value.kind !== 'live' && recentStateValue.value.kind !== 'empty') return []
    return (recentStateValue.value.data.queries || []).map(normalizeRecent)
  })

  const noiseMetrics = computed(() => {
    const retrieval = retrievalStateValue.value.kind === 'live' ? retrievalStateValue.value.data : {}
    const vnext = vnextStateValue.value.kind === 'live' ? vnextStateValue.value.data : {}
    const outcomes = vnext.outcomes || {}
    return [
      { label: 'noiseRatio', value: display(vnext.noise_ratio), endpoint: '/api/stats/vnext' },
      { label: 'shown', value: display(retrieval.observations_served), endpoint: '/api/stats/retrieval' },
      { label: 'used', value: display(vnext.citation_count), endpoint: '/api/stats/vnext' },
      { label: 'unused', value: display(vnext.uncited_count), endpoint: '/api/stats/vnext' },
      { label: 'searchRequests', value: display(retrieval.search_requests), endpoint: '/api/stats/retrieval' },
      { label: 'unrecorded', value: percent(outcomes.unrecorded_fraction), endpoint: '/api/stats/vnext' },
    ]
  })

  const pending = computed(() => [
    projectsStateValue.value,
    recentStateValue.value,
    analyticsStateValue.value,
    retrievalStateValue.value,
    vnextStateValue.value,
  ].some((state) => state.kind === 'pending'))

  const error = computed(() => {
    for (const state of [projectsStateValue.value, searchStateValue.value, recentStateValue.value, analyticsStateValue.value, retrievalStateValue.value, vnextStateValue.value]) {
      if (state.kind === 'error') return state.error.message
    }
    return null
  })

  async function refreshProjects() {
    projectsStateValue.value = pendingState(projectsEvidence, projects.value)
    const result = await loadOperatorJson<string[]>('/api/projects', {
      source: 'search-projects',
      empty: (rows) => !rows.length,
    })

    if (result.kind === 'live' || result.kind === 'empty') {
      replaceArray(projects.value, result.data)
      projectsStateValue.value = projects.value.length
        ? liveState(projectsEvidence, projects.value)
        : emptyState(projectsEvidence, projects.value)
      if (!selectedProject.value && projects.value.length) {
        selectedProject.value = projects.value[0]
      }
      return
    }

    projectsStateValue.value = result
  }

  async function refreshRecent() {
    recentStateValue.value = await loadOperatorJson<ApiSearchRecent>('/api/search/recent', {
      source: 'search-recent',
      empty: (payload) => !(payload.queries || []).length,
    })
  }

  async function refreshAnalytics() {
    analyticsStateValue.value = await loadOperatorJson<ApiSearchAnalytics>('/api/search/analytics', {
      source: 'search-analytics',
    })
  }

  async function refreshRetrieval() {
    retrievalStateValue.value = await loadOperatorJson<ApiRetrievalStats>('/api/stats/retrieval', {
      source: 'retrieval-stats',
    })
  }

  async function refreshVNext() {
    vnextStateValue.value = await loadOperatorJson<ApiStatsVNext>('/api/stats/vnext', {
      source: 'stats-vnext',
    })
  }

  async function refresh() {
    await refreshProjects()
    await Promise.all([
      refreshRecent(),
      refreshAnalytics(),
      refreshRetrieval(),
      refreshVNext(),
    ])
  }

  async function runSearch(query: string, project = selectedProject.value) {
    const run = ++searchGeneration
    const trimmedQuery = query.trim()
    const trimmedProject = project.trim()
    if (!trimmedQuery || !trimmedProject) {
      replaceArray(searchResults.value, [])
      searchStateValue.value = emptyState(searchEvidence, searchResults.value)
      return
    }

    const path = '/api/context/search?' +
      `project=${encodeURIComponent(trimmedProject)}` +
      `&query=${encodeURIComponent(trimmedQuery)}` +
      '&limit=20'
    const evidence = endpointEvidence(path, 'context-search')
    searchStateValue.value = pendingState(evidence, searchResults.value)

    try {
      const payload = await operatorFetchJson<ApiContextSearch>(path, undefined, 'context-search')
      if (run !== searchGeneration) return
      if (typeof payload === 'string' || payload.deprecated) {
        replaceArray(searchResults.value, [])
        searchStateValue.value = staleState(evidence, payload.message || 'Deprecated search response; not a live result set.', searchResults.value)
        return
      }

      const nextRows = contextRows(payload)
      replaceArray(searchResults.value, nextRows)
      searchStateValue.value = nextRows.length
        ? liveState(evidence, searchResults.value)
        : emptyState(evidence, searchResults.value)
      await Promise.all([refreshRecent(), refreshAnalytics(), refreshRetrieval(), refreshVNext()])
    } catch (nextError) {
      if (run !== searchGeneration) return
      const mapped = toOperatorSourceError(nextError, {
        source: 'context-search',
        path,
        method: 'GET',
      })
      searchStateValue.value = errorState(evidence, mapped, {
        source: 'context-search',
        run: async () => {
          await runSearch(trimmedQuery, trimmedProject)
          return searchStateValue.value
        },
      }, searchResults.value)
    }
  }

  const tombstoneGap = unsupportedOperatorAction(
    'search-collection-tombstone',
    'search_collection',
    'Legacy collection search is not a live operator-console result source; keep it as stale/tombstone evidence.',
  )

  const noiseTrendGap = unsupportedOperatorAction(
    'noise-trend',
    'GET /api/stats/noise-trend',
    'Only snapshot retrieval/vNext metrics exist today; time-series trend needs a server endpoint.',
  )

  startOnce('search-noise', refresh)

  return {
    projects,
    selectedProject,
    searchResults: searchResults.value,
    recentRows,
    searchState,
    recentState,
    analyticsState,
    retrievalState,
    vnextState,
    noiseMetrics,
    pending,
    error,
    refresh,
    runSearch,
    tombstoneGap,
    noiseTrendGap,
  }
}
