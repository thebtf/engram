import type { ComputedRef } from 'vue'
import type { RuleCreateInput, RuleRow, RuleUpdateInput } from './useMockData'
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
  unsupportedOperatorAction,
} from './useOperatorApi'

interface ApiRuleRow {
  id: number
  project?: string
  content?: string
  priority?: number
  version?: number
  enabled?: boolean
  edited_by?: string
  created_at?: string
  updated_at?: string
}

function jsonInit(method: 'POST' | 'PATCH' | 'DELETE', body?: unknown): RequestInit {
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

function normalizeProject(value?: string): string {
  return value && value.trim() ? value.trim() : 'global'
}

function mapRuleRow(row: ApiRuleRow): RuleRow {
  return {
    id: row.id,
    content: row.content || '-',
    project: normalizeProject(row.project),
    priority: typeof row.priority === 'number' ? row.priority : 0,
    version: typeof row.version === 'number' ? row.version : 1,
    updated: compactAge(row.updated_at || row.created_at),
    enabled: row.enabled !== false,
  }
}

function sortRules(rows: RuleRow[]): RuleRow[] {
  return [...rows].sort((left, right) => {
    if (left.priority !== right.priority) {
      return right.priority - left.priority
    }
    return left.content.localeCompare(right.content)
  })
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorRules] ${key} live load failed`, error)
      }
    })
  }
}

export function useOperatorRules(): {
  rows: RuleRow[]
  scopeOptions: ComputedRef<string[]>
  loadState: ComputedRef<OperatorLoadState<RuleRow[]>>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  refresh: () => Promise<void>
  createRule: (input: RuleCreateInput) => Promise<unknown>
  updateRule: (id: number, input: RuleUpdateInput) => Promise<unknown>
  toggleRuleEnabled: (id: number, enabled: boolean) => Promise<unknown>
  reorderRules: (orderedRows: RuleRow[]) => Promise<unknown>
  deleteRule: (id: number) => Promise<unknown>
  scopeChangeGap: ReturnType<typeof unsupportedOperatorAction>
} {
  const evidence = endpointEvidence('/api/rules?all=true&limit=200', 'rules-list')
  const rowsState = useState<RuleRow[]>('live:rules-page:rows', () => [])
  const projectOptions = useState<string[]>('live:rules-page:project-options', () => [])
  const state = useState<OperatorLoadState<RuleRow[]>>('live:rules-page:state', () => pendingState(evidence, rowsState.value))

  const loadState = computed(() => state.value)
  const scopeOptions = computed(() => ['global', ...projectOptions.value])
  const pending = computed(() => state.value.kind === 'pending')
  const error = computed(() => state.value.kind === 'error' ? state.value.error.message : null)

  async function refreshProjects() {
    const result = await loadOperatorJson<string[]>('/api/projects', {
      source: 'rules-projects',
      empty: (rows) => !rows.length,
    })
    if (result.kind === 'live' || result.kind === 'empty') {
      replaceArray(projectOptions.value, [...new Set(result.data.filter((project) => project.trim()))].sort())
    } else if (result.kind === 'error' && import.meta.dev) {
      console.warn('[useOperatorRules] project options unavailable', result.error.message)
    }
  }

  async function refresh() {
    state.value = pendingState(evidence, rowsState.value)
    await refreshProjects()
    const result = await loadOperatorJson<ApiRuleRow[]>('/api/rules?all=true&limit=200', {
      source: 'rules-list',
      empty: (rows) => !rows.length,
    })

    if (result.kind === 'live' || result.kind === 'empty') {
      replaceArray(rowsState.value, sortRules(result.data.map(mapRuleRow)))
      state.value = rowsState.value.length
        ? liveState(evidence, rowsState.value)
        : emptyState(evidence, rowsState.value)
      return
    }

    if (result.kind === 'error') {
      state.value = errorState(evidence, result.error, {
        source: 'rules-list',
        run: async () => {
          await refresh()
          return state.value
        },
      }, rowsState.value)
    } else {
      state.value = result as OperatorLoadState<RuleRow[]>
    }
  }

  async function createRule(input: RuleCreateInput) {
    return runOperatorMutation({
      action: 'rule-create',
      evidence: endpointEvidence('/api/rules', 'rules-create'),
      snapshot: () => [...rowsState.value],
      run: async () => {
        const row = await operatorFetchJson<ApiRuleRow>('/api/rules', jsonInit('POST', {
          content: input.content,
          priority: input.priority ?? 0,
          edited_by: input.editedBy || 'operator-console',
          ...(input.project ? { project: input.project } : {}),
        }), 'rules-create')
        return mapRuleRow(row)
      },
      rollback: (snapshot) => replaceArray(rowsState.value, snapshot || []),
      refresh,
    })
  }

  async function updateRule(id: number, input: RuleUpdateInput) {
    return runOperatorMutation({
      action: 'rule-update',
      evidence: endpointEvidence(`/api/rules/${id}`, 'rules-update'),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        replaceArray(rowsState.value, rowsState.value.map((row) => row.id === id
          ? {
              ...row,
              ...(input.content !== undefined ? { content: input.content } : {}),
              ...(input.priority !== undefined ? { priority: input.priority } : {}),
            }
          : row))
      },
      run: async () => {
        const row = await operatorFetchJson<ApiRuleRow>(`/api/rules/${id}`, jsonInit('PATCH', {
          ...(input.content !== undefined ? { content: input.content } : {}),
          ...(input.priority !== undefined ? { priority: input.priority } : {}),
          ...(input.editedBy !== undefined ? { edited_by: input.editedBy } : { edited_by: 'operator-console' }),
        }), 'rules-update')
        return mapRuleRow(row)
      },
      rollback: (snapshot) => replaceArray(rowsState.value, snapshot || []),
      refresh,
    })
  }

  async function toggleRuleEnabled(id: number, enabled: boolean) {
    return runOperatorMutation({
      action: 'rule-enable-toggle',
      evidence: endpointEvidence(`/api/rules/${id}/enabled`, 'rule-enable-toggle'),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        replaceArray(rowsState.value, rowsState.value.map((row) => row.id === id ? { ...row, enabled } : row))
      },
      run: async () => {
        const row = await operatorFetchJson<ApiRuleRow>(`/api/rules/${id}/enabled`, jsonInit('PATCH', {
          enabled,
          edited_by: 'operator-console',
        }), 'rule-enable-toggle')
        return mapRuleRow(row)
      },
      rollback: (snapshot) => replaceArray(rowsState.value, snapshot || []),
      refresh,
    })
  }

  async function reorderRules(orderedRows: RuleRow[]) {
    const nextRows = orderedRows.map((row, index) => ({
      ...row,
      priority: (orderedRows.length - index) * 10,
    }))
    const changed = nextRows.filter((row) => rowsState.value.find((current) => current.id === row.id)?.priority !== row.priority)

    return runOperatorMutation({
      action: 'rule-reorder',
      evidence: endpointEvidence('/api/rules/{id}', 'rules-reorder'),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        const byId = new Map(nextRows.map((row) => [row.id, row]))
        const untouched = rowsState.value.filter((row) => !byId.has(row.id))
        replaceArray(rowsState.value, sortRules([...nextRows, ...untouched]))
      },
      run: async () => {
        await Promise.all(changed.map((row) => operatorFetchJson<ApiRuleRow>(`/api/rules/${row.id}`, jsonInit('PATCH', {
          priority: row.priority,
          edited_by: 'operator-console',
        }), 'rules-reorder')))
        return nextRows
      },
      rollback: (snapshot) => replaceArray(rowsState.value, snapshot || []),
      refresh,
    })
  }

  async function deleteRule(id: number) {
    return runOperatorMutation({
      action: 'rule-delete',
      evidence: endpointEvidence(`/api/rules/${id}`, 'rules-delete'),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        replaceArray(rowsState.value, rowsState.value.filter((row) => row.id !== id))
      },
      run: () => operatorFetchJson(`/api/rules/${id}`, jsonInit('DELETE'), 'rules-delete'),
      rollback: (snapshot) => replaceArray(rowsState.value, snapshot || []),
      refresh,
    })
  }

  const scopeChangeGap = unsupportedOperatorAction(
    'rule-scope-change',
    'PATCH /api/rules/{id}/project',
    'Behavioral rule scope changes are intentionally not accepted by the current update endpoint.',
  )

  startOnce('rules-page', refresh)

  return {
    rows: rowsState.value,
    scopeOptions,
    loadState,
    pending,
    error,
    refresh,
    createRule,
    updateRule,
    toggleRuleEnabled,
    reorderRules,
    deleteRule,
    scopeChangeGap,
  }
}
