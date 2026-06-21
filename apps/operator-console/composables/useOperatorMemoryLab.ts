import type { ComputedRef } from 'vue'
import type { Memory } from './useMockData'
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
}

export interface StoreMemoryInput {
  project: string
  content: string
  tags?: string[]
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

function mapTier(value?: string): Memory['tier'] {
  if (value === 'semantic' || value === 'episodic' || value === 'procedural') {
    return value
  }

  return 'procedural'
}

function isNoiseStatus(value?: string): boolean {
  const normalized = String(value || '').trim().toLowerCase()
  return normalized === 'noise' || normalized === 'flagged' || normalized === 'suppressed'
}

function mapMemoryRow(row: ApiMemory): Memory {
  return {
    id: String(row.id),
    content: row.content || '-',
    tags: Array.isArray(row.tags) ? row.tags : [],
    tier: mapTier(row.tier),
    type: row.epistemic_type || 'note',
    project: row.project || 'global',
    conf: typeof row.confidence === 'number' ? row.confidence : 0,
    cite: typeof row.citation_count === 'number' ? row.citation_count : 0,
    inj: typeof row.injection_count === 'number' ? row.injection_count : 0,
    age: compactAge(row.updated_at || row.created_at),
    noise: isNoiseStatus(row.status),
  }
}

function parseMemoryArray(payload: unknown): ApiMemory[] {
  if (Array.isArray(payload)) {
    return payload as ApiMemory[]
  }

  if (typeof payload === 'string' && payload.trim()) {
    try {
      const parsed = JSON.parse(payload)
      return Array.isArray(parsed) ? parsed as ApiMemory[] : []
    } catch {
      return []
    }
  }

  return []
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
    empty: (rows) => !rows.length,
  })

  if (projectState.kind === 'error') {
    throw projectState.error
  }

  const projects = projectState.kind === 'live' || projectState.kind === 'empty'
    ? projectState.data
    : []
  const combined: Memory[] = []

  for (const project of projects) {
    const payload = await operatorFetchJson<unknown>(
      `/api/memories?project=${encodeURIComponent(project)}&limit=200`,
      undefined,
      'memory-list',
    )
    for (const row of parseMemoryArray(payload)) {
      combined.push(mapMemoryRow(row))
    }
  }

  const deduped = new Map<string, Memory>()
  for (const row of combined) {
    deduped.set(`${row.project}:${row.id}`, row)
  }

  return [...deduped.values()]
}

export function useOperatorMemoryLab(): {
  rows: Memory[]
  loadState: ComputedRef<OperatorLoadState<Memory[]>>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  refresh: () => Promise<void>
  storeMemory: (input: StoreMemoryInput) => Promise<unknown>
  deleteMemory: (id: string) => Promise<unknown>
  auditGap: ReturnType<typeof unsupportedOperatorAction>
  provenanceGap: ReturnType<typeof unsupportedOperatorAction>
} {
  const evidence = endpointEvidence('/api/memories?project={project}&limit=200', 'memory-list')
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

  const auditGap = unsupportedOperatorAction(
    'memory-audit',
    'GET /api/memories/{id}/audit',
    'Memory audit history is not exposed by the current server API.',
  )
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
    auditGap,
    provenanceGap,
  }
}
