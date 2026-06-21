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
} from './useOperatorApi'

export type OperatorIssueStatus = 'open' | 'acknowledged' | 'reopened' | 'resolved' | 'closed' | 'rejected'
export type OperatorIssuePriority = 'critical' | 'high' | 'medium' | 'low'
export type OperatorIssueType = 'bug' | 'feature' | 'improvement' | 'task'

export interface OperatorIssue {
  id: number
  title: string
  body: string
  status: OperatorIssueStatus
  priority: OperatorIssuePriority
  type: OperatorIssueType
  age: string
  comments: number
  sourceProject: string
  targetProject: string
  labels: string[]
}

export interface IssueCreateInput {
  title: string
  body?: string
  priority: OperatorIssuePriority
  type: OperatorIssueType
  targetProject: string
  labels?: string[]
}

export interface IssueUpdateInput {
  title?: string
  body?: string
  priority?: OperatorIssuePriority
  type?: OperatorIssueType
  status?: OperatorIssueStatus
  comment?: string
}

interface ApiIssueRow {
  id: number
  title?: string
  body?: string
  status?: string
  priority?: string
  type?: string
  source_project?: string
  target_project?: string
  labels?: string[]
  comment_count?: number
  created_at?: string
  updated_at?: string
}

interface ApiIssueList {
  issues?: ApiIssueRow[]
  total?: number
}

interface ApiIssueDetail {
  issue?: ApiIssueRow
  comments?: unknown[]
  comment_count?: number
}

interface ApiIssueCreateReceipt {
  id: number
  message?: string
}

interface ApiIssueAcknowledgeReceipt {
  acknowledged: number
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

function normalizeStatus(value?: string): OperatorIssueStatus {
  const normalized = String(value || '').toLowerCase()
  if (['open', 'acknowledged', 'reopened', 'resolved', 'closed', 'rejected'].includes(normalized)) {
    return normalized as OperatorIssueStatus
  }
  return 'open'
}

function normalizePriority(value?: string): OperatorIssuePriority {
  const normalized = String(value || '').toLowerCase()
  if (['critical', 'high', 'medium', 'low'].includes(normalized)) {
    return normalized as OperatorIssuePriority
  }
  return 'medium'
}

function normalizeType(value?: string): OperatorIssueType {
  const normalized = String(value || '').toLowerCase()
  if (['bug', 'feature', 'improvement', 'task'].includes(normalized)) {
    return normalized as OperatorIssueType
  }
  return 'task'
}

function mapIssue(row: ApiIssueRow, commentCount = row.comment_count): OperatorIssue {
  return {
    id: row.id,
    title: row.title || '-',
    body: row.body || '',
    status: normalizeStatus(row.status),
    priority: normalizePriority(row.priority),
    type: normalizeType(row.type),
    age: compactAge(row.updated_at || row.created_at),
    comments: typeof commentCount === 'number' ? commentCount : 0,
    sourceProject: row.source_project || 'operator-console',
    targetProject: row.target_project || 'engram',
    labels: Array.isArray(row.labels) ? row.labels : [],
  }
}

function sortIssues(rows: OperatorIssue[]) {
  const priorityRank: Record<OperatorIssuePriority, number> = { critical: 0, high: 1, medium: 2, low: 3 }
  return [...rows].sort((left, right) => {
    const rank = priorityRank[left.priority] - priorityRank[right.priority]
    if (rank !== 0) return rank
    return left.id - right.id
  })
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorIssues] ${key} live load failed`, error)
      }
    })
  }
}

export function useOperatorIssues(): {
  rows: OperatorIssue[]
  detail: ComputedRef<OperatorIssue | null>
  loadState: ComputedRef<OperatorLoadState<OperatorIssue[]>>
  detailState: ComputedRef<OperatorLoadState<OperatorIssue | null>>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  refresh: () => Promise<void>
  openIssue: (id: number) => Promise<void>
  createIssue: (input: IssueCreateInput) => Promise<unknown>
  updateIssue: (id: number, input: IssueUpdateInput) => Promise<unknown>
  acknowledgeIssue: (id: number) => Promise<unknown>
  deleteIssue: (id: number) => Promise<unknown>
} {
  const listEvidence = endpointEvidence('/api/issues?limit=100', 'issues-list')
  const detailEvidence = endpointEvidence('/api/issues/{id}', 'issues-detail')
  const rowsState = useState<OperatorIssue[]>('live:issues-page:rows', () => [])
  const detailStateValue = useState<OperatorIssue | null>('live:issues-page:detail', () => null)
  const state = useState<OperatorLoadState<OperatorIssue[]>>('live:issues-page:state', () => pendingState(listEvidence, rowsState.value))
  const detailLoadState = useState<OperatorLoadState<OperatorIssue | null>>('live:issues-page:detail-state', () => emptyState(detailEvidence, null))

  const loadState = computed(() => state.value)
  const detailState = computed(() => detailLoadState.value)
  const detail = computed(() => detailStateValue.value)
  const pending = computed(() => state.value.kind === 'pending')
  const error = computed(() => state.value.kind === 'error' ? state.value.error.message : null)

  async function refresh() {
    state.value = pendingState(listEvidence, rowsState.value)
    const result = await loadOperatorJson<ApiIssueList>('/api/issues?limit=100', {
      source: 'issues-list',
      empty: (payload) => !(payload.issues || []).length,
    })

    if (result.kind === 'live' || result.kind === 'empty') {
      const nextRows = sortIssues((result.data.issues || []).map((row) => mapIssue(row)))
      replaceArray(rowsState.value, nextRows)
      state.value = nextRows.length
        ? liveState(listEvidence, rowsState.value)
        : emptyState(listEvidence, rowsState.value)
      return
    }

    if (result.kind === 'error') {
      state.value = errorState(listEvidence, result.error, {
        source: 'issues-list',
        run: async () => {
          await refresh()
          return state.value
        },
      }, rowsState.value)
    } else {
      state.value = result as OperatorLoadState<OperatorIssue[]>
    }
  }

  async function openIssue(id: number) {
    const evidence = endpointEvidence(`/api/issues/${id}`, 'issues-detail')
    detailLoadState.value = pendingState(evidence, detailStateValue.value)
    try {
      const payload = await operatorFetchJson<ApiIssueDetail>(`/api/issues/${id}`, undefined, 'issues-detail')
      const next = payload.issue ? mapIssue(payload.issue, payload.comment_count ?? payload.comments?.length) : null
      detailStateValue.value = next
      detailLoadState.value = next ? liveState(evidence, next) : emptyState(evidence, null)
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'issues-detail',
        path: `/api/issues/${id}`,
        method: 'GET',
      })
      detailLoadState.value = errorState(evidence, mapped, {
        source: 'issues-detail',
        run: async () => {
          await openIssue(id)
          return detailLoadState.value
        },
      }, detailStateValue.value)
    }
  }

  async function createIssue(input: IssueCreateInput) {
    return runOperatorMutation({
      action: 'issue-create',
      evidence: endpointEvidence('/api/issues', 'issues-create'),
      snapshot: () => [...rowsState.value],
      run: () => operatorFetchJson<ApiIssueCreateReceipt>('/api/issues', jsonInit('POST', {
        title: input.title,
        body: input.body || '',
        priority: input.priority,
        type: input.type,
        source_project: 'operator-console',
        target_project: input.targetProject,
        source_agent: 'operator-console',
        labels: input.labels || [],
      }), 'issues-create'),
      rollback: (snapshot) => replaceArray(rowsState.value, snapshot || []),
      refresh,
    })
  }

  async function updateIssue(id: number, input: IssueUpdateInput) {
    return runOperatorMutation({
      action: 'issue-update',
      evidence: endpointEvidence(`/api/issues/${id}`, 'issues-update'),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        replaceArray(rowsState.value, rowsState.value.map((row) => row.id === id
          ? {
              ...row,
              ...(input.title !== undefined ? { title: input.title } : {}),
              ...(input.body !== undefined ? { body: input.body } : {}),
              ...(input.priority !== undefined ? { priority: input.priority } : {}),
              ...(input.type !== undefined ? { type: input.type } : {}),
              ...(input.status !== undefined ? { status: input.status } : {}),
            }
          : row))
      },
      run: () => operatorFetchJson(`/api/issues/${id}`, jsonInit('PATCH', {
        ...(input.title !== undefined ? { title: input.title } : {}),
        ...(input.body !== undefined ? { body: input.body } : {}),
        ...(input.priority !== undefined ? { priority: input.priority } : {}),
        ...(input.type !== undefined ? { type: input.type } : {}),
        ...(input.status !== undefined ? { status: input.status } : {}),
        ...(input.comment !== undefined ? { comment: input.comment } : {}),
        source_project: 'dashboard',
        source_agent: 'operator-console',
      }), 'issues-update'),
      rollback: (snapshot) => replaceArray(rowsState.value, snapshot || []),
      refresh,
    })
  }

  async function acknowledgeIssue(id: number) {
    return runOperatorMutation({
      action: 'issue-acknowledge',
      evidence: endpointEvidence('/api/issues/acknowledge', 'issues-acknowledge'),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        replaceArray(rowsState.value, rowsState.value.map((row) => row.id === id ? { ...row, status: 'acknowledged' } : row))
      },
      run: () => operatorFetchJson<ApiIssueAcknowledgeReceipt>('/api/issues/acknowledge', jsonInit('POST', { ids: [id] }), 'issues-acknowledge'),
      rollback: (snapshot) => replaceArray(rowsState.value, snapshot || []),
      refresh,
    })
  }

  async function deleteIssue(id: number) {
    return runOperatorMutation({
      action: 'issue-delete',
      evidence: endpointEvidence(`/api/issues/${id}`, 'issues-delete'),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        replaceArray(rowsState.value, rowsState.value.filter((row) => row.id !== id))
        if (detailStateValue.value?.id === id) {
          detailStateValue.value = null
        }
      },
      run: () => operatorFetchJson(`/api/issues/${id}`, jsonInit('DELETE'), 'issues-delete'),
      rollback: (snapshot) => replaceArray(rowsState.value, snapshot || []),
      refresh,
    })
  }

  startOnce('issues-page', refresh)

  return {
    rows: rowsState.value,
    detail,
    loadState,
    detailState,
    pending,
    error,
    refresh,
    openIssue,
    createIssue,
    updateIssue,
    acknowledgeIssue,
    deleteIssue,
  }
}
