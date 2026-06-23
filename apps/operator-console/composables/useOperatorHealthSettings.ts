import type { ComputedRef } from 'vue'
import type { OperatorLoadState } from './useOperatorApi'
import {
  endpointEvidence,
  loadOperatorJson,
  operatorFetchJson,
  pendingState,
  runOperatorMutation,
  unsupportedOperatorAction,
} from './useOperatorApi'

interface ApiComponentHealth {
  name?: string
  status?: string
}

interface ApiSelfcheck {
  overall?: string
  version?: string
  uptime?: string
  components?: ApiComponentHealth[]
}

interface ApiReady {
  status?: string
  ready?: boolean
  error?: string
}

interface ApiConfig {
  context?: {
    observations?: number
    max_tokens?: number
    session_count?: number
    relevance_threshold?: number
    obs_types?: string[]
    obs_concepts?: string[]
  }
  memory?: {
    inject_unified?: boolean
    always_inject_limit?: number
    project_inject_limit?: number
  }
  storage?: {
    vector_strategy?: string
    database_max_conns?: number
    log_buffer_size?: number
  }
  features?: {
    telemetry_enabled?: boolean
    enforce_source_project?: boolean
  }
}

interface ApiFlagItem {
  name: string
  enabled: boolean
  source: string
  category: string
  restart_required_to_change?: boolean
}

interface ApiFlags {
  flags?: Record<string, boolean>
  items?: ApiFlagItem[]
  summary?: {
    total?: number
    enabled?: number
    disabled?: number
  }
  read_only?: boolean
  apply?: {
    supported?: boolean
    endpoint?: string
    reason?: string
  }
}

interface ApiStatsVNext {
  injection_count?: number
  citation_count?: number
  uncited_count?: number
  noise_ratio?: number
  outcomes?: {
    total_sessions?: number
    unrecorded_sessions?: number
    unrecorded_fraction?: number
  }
  embedding?: {
    chunk_count?: number
    memories_with_chunks?: number
    active_memory_count?: number
    dimension?: number
    embedding_coverage?: number
    model?: string
  }
}

interface ApiVectorMetrics {
  enabled?: boolean
  message?: string
  stats?: {
    chunk_count?: number
    memories_with_chunks?: number
    dimension?: number
    model?: string
  }
}

interface ApiUpdateStatus {
  state?: string
  progress?: number
  message?: string
  version?: string
}

interface ApiUpdateCheck {
  current_version?: string
  latest_version?: string
  version?: string
  available?: boolean
  message?: string
}

export interface OperatorHealthMetric {
  label: string
  value: string
}

function display(value: unknown): string {
  if (value === undefined || value === null || value === '') return '-'
  if (typeof value === 'number') return Number.isInteger(value) ? String(value) : value.toFixed(3)
  if (typeof value === 'boolean') return String(value)
  return String(value)
}

function percent(value?: number): string {
  return typeof value === 'number' ? `${Math.round(value * 100)}%` : '-'
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorHealthSettings] ${key} live load failed`, error)
      }
    })
  }
}

export function useOperatorHealthSettings(): {
  selfcheckState: ComputedRef<OperatorLoadState<ApiSelfcheck>>
  readyState: ComputedRef<OperatorLoadState<ApiReady>>
  configState: ComputedRef<OperatorLoadState<ApiConfig>>
  flagsState: ComputedRef<OperatorLoadState<ApiFlags>>
  vnextState: ComputedRef<OperatorLoadState<ApiStatsVNext>>
  vectorState: ComputedRef<OperatorLoadState<ApiVectorMetrics>>
  updateStatusState: ComputedRef<OperatorLoadState<ApiUpdateStatus>>
  updateCheckState: ComputedRef<OperatorLoadState<ApiUpdateCheck>>
  components: ComputedRef<ApiComponentHealth[]>
  flagItems: ComputedRef<ApiFlagItem[]>
  embeddingMetrics: ComputedRef<OperatorHealthMetric[]>
  configMetrics: ComputedRef<OperatorHealthMetric[]>
  restartRequired: ComputedRef<boolean>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  refresh: () => Promise<void>
  restartServer: () => Promise<unknown>
  restartAfterUpdate: () => Promise<unknown>
  configSaveGap: ReturnType<typeof unsupportedOperatorAction>
} {
  const selfcheckEvidence = endpointEvidence('/api/selfcheck', 'selfcheck')
  const readyEvidence = endpointEvidence('/api/ready', 'ready')
  const configEvidence = endpointEvidence('/api/config', 'config')
  const flagsEvidence = endpointEvidence('/api/flags', 'flags')
  const vnextEvidence = endpointEvidence('/api/stats/vnext', 'stats-vnext')
  const vectorEvidence = endpointEvidence('/api/vector/metrics', 'vector-metrics')
  const updateStatusEvidence = endpointEvidence('/api/update/status', 'update-status')
  const updateCheckEvidence = endpointEvidence('/api/update/check', 'update-check')

  const selfcheck = useState<OperatorLoadState<ApiSelfcheck>>('live:health-settings:selfcheck', () => pendingState(selfcheckEvidence))
  const ready = useState<OperatorLoadState<ApiReady>>('live:health-settings:ready', () => pendingState(readyEvidence))
  const config = useState<OperatorLoadState<ApiConfig>>('live:health-settings:config', () => pendingState(configEvidence))
  const flags = useState<OperatorLoadState<ApiFlags>>('live:health-settings:flags', () => pendingState(flagsEvidence))
  const vnext = useState<OperatorLoadState<ApiStatsVNext>>('live:health-settings:vnext', () => pendingState(vnextEvidence))
  const vector = useState<OperatorLoadState<ApiVectorMetrics>>('live:health-settings:vector', () => pendingState(vectorEvidence))
  const updateStatus = useState<OperatorLoadState<ApiUpdateStatus>>('live:health-settings:update-status', () => pendingState(updateStatusEvidence))
  const updateCheck = useState<OperatorLoadState<ApiUpdateCheck>>('live:health-settings:update-check', () => pendingState(updateCheckEvidence))

  const selfcheckState = computed(() => selfcheck.value)
  const readyState = computed(() => ready.value)
  const configState = computed(() => config.value)
  const flagsState = computed(() => flags.value)
  const vnextState = computed(() => vnext.value)
  const vectorState = computed(() => vector.value)
  const updateStatusState = computed(() => updateStatus.value)
  const updateCheckState = computed(() => updateCheck.value)

  const components = computed(() => selfcheck.value.kind === 'live' ? selfcheck.value.data.components || [] : [])
  const flagItems = computed(() => flags.value.kind === 'live' ? flags.value.data.items || [] : [])
  const embeddingMetrics = computed(() => {
    const embedding = vnext.value.kind === 'live' ? vnext.value.data.embedding : undefined
    const vectorStats = vector.value.kind === 'live' ? vector.value.data.stats : undefined
    return [
      { label: 'chunk_count', value: display(embedding?.chunk_count ?? vectorStats?.chunk_count) },
      { label: 'with_vectors', value: display(embedding?.memories_with_chunks ?? vectorStats?.memories_with_chunks) },
      { label: 'dimension', value: display(embedding?.dimension ?? vectorStats?.dimension) },
      { label: 'coverage', value: percent(embedding?.embedding_coverage) },
      { label: 'noise_ratio', value: display(vnext.value.kind === 'live' ? vnext.value.data.noise_ratio : undefined) },
      { label: 'model', value: display(embedding?.model ?? vectorStats?.model) },
    ]
  })
  const configMetrics = computed(() => {
    const data = config.value.kind === 'live' ? config.value.data : {}
    return [
      { label: 'context.observations', value: display(data.context?.observations) },
      { label: 'context.max_tokens', value: display(data.context?.max_tokens) },
      { label: 'context.session_count', value: display(data.context?.session_count) },
      { label: 'memory.inject_unified', value: display(data.memory?.inject_unified) },
      { label: 'memory.always_inject_limit', value: display(data.memory?.always_inject_limit) },
      { label: 'memory.project_inject_limit', value: display(data.memory?.project_inject_limit) },
      { label: 'storage.vector_strategy', value: display(data.storage?.vector_strategy) },
      { label: 'storage.database_max_conns', value: display(data.storage?.database_max_conns) },
      { label: 'features.telemetry_enabled', value: display(data.features?.telemetry_enabled) },
      { label: 'features.enforce_source_project', value: display(data.features?.enforce_source_project) },
    ]
  })
  const restartRequired = computed(() => updateStatus.value.kind === 'live' && updateStatus.value.data.state === 'done')
  const pending = computed(() => [
    selfcheck.value,
    ready.value,
    config.value,
    flags.value,
    vnext.value,
    vector.value,
    updateStatus.value,
    updateCheck.value,
  ].some((state) => state.kind === 'pending'))
  const error = computed(() => {
    for (const state of [selfcheck.value, ready.value, config.value, flags.value, vnext.value, vector.value, updateStatus.value, updateCheck.value]) {
      if (state.kind === 'error') return state.error.message
    }
    return null
  })

  async function refresh() {
    selfcheck.value = await loadOperatorJson<ApiSelfcheck>('/api/selfcheck', { source: 'selfcheck' })
    ready.value = await loadOperatorJson<ApiReady>('/api/ready', { source: 'ready' })
    config.value = await loadOperatorJson<ApiConfig>('/api/config', { source: 'config' })
    flags.value = await loadOperatorJson<ApiFlags>('/api/flags', { source: 'flags' })
    vnext.value = await loadOperatorJson<ApiStatsVNext>('/api/stats/vnext', { source: 'stats-vnext' })
    vector.value = await loadOperatorJson<ApiVectorMetrics>('/api/vector/metrics', { source: 'vector-metrics' })
    updateStatus.value = await loadOperatorJson<ApiUpdateStatus>('/api/update/status', { source: 'update-status' })
    updateCheck.value = await loadOperatorJson<ApiUpdateCheck>('/api/update/check', { source: 'update-check' })
  }

  async function restartServer() {
    return runOperatorMutation({
      action: 'server-restart',
      evidence: endpointEvidence('/api/restart', 'restart'),
      run: () => operatorFetchJson('/api/restart', { method: 'POST' }, 'restart'),
    })
  }

  async function restartAfterUpdate() {
    return runOperatorMutation({
      action: 'update-restart',
      evidence: endpointEvidence('/api/update/restart', 'update-restart'),
      run: () => operatorFetchJson('/api/update/restart', { method: 'POST' }, 'update-restart'),
      refresh,
    })
  }

  const configSaveGap = unsupportedOperatorAction(
    'config-save',
    'PATCH /api/config',
    'Runtime config is exposed as a read model only; saving settings needs a server endpoint and restart-required receipt.',
  )

  startOnce('health-settings', refresh)

  return {
    selfcheckState,
    readyState,
    configState,
    flagsState,
    vnextState,
    vectorState,
    updateStatusState,
    updateCheckState,
    components,
    flagItems,
    embeddingMetrics,
    configMetrics,
    restartRequired,
    pending,
    error,
    refresh,
    restartServer,
    restartAfterUpdate,
    configSaveGap,
  }
}
