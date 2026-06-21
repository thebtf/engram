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
  unsupportedOperatorAction,
  type OperatorUnsupportedAction,
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
  sourceDisplayName: string
  targetDisplayName: string
  labels: string[]
  createdAt: string
  updatedAt: string
}

export interface OperatorIssueComment {
  id: number
  issueId: number
  authorProject: string
  authorAgent: string
  body: string
  createdAt: string
  age: string
}

export interface IssueCreateInput {
  title: string
  body?: string
  priority: OperatorIssuePriority
  type: OperatorIssueType
  sourceProject?: string
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
  labels?: string[]
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
  source_project_display_name?: string
  target_project_display_name?: string
  labels?: string[]
  comment_count?: number
  created_at?: string
  updated_at?: string
}

interface ApiIssueComment {
  id?: number
  issue_id?: number
  author_project?: string
  author_agent?: string
  body?: string
  created_at?: string
}

interface ApiIssueList {
  issues?: ApiIssueRow[]
  total?: number
  project_names?: Record<string, string>
}

interface ApiIssueDetail {
  issue?: ApiIssueRow
  comments?: ApiIssueComment[]
  comment_count?: number
  source_project_display_name?: string
  target_project_display_name?: string
}

interface ApiIssueCreateReceipt {
  id: number
  message?: string
}

interface ApiIssueAcknowledgeReceipt {
  acknowledged: number
}

interface ApiTrackedProjects {
  projects?: string[]
  count?: number
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

function replaceRecord<T>(target: Record<string, T>, next: Record<string, T>) {
  for (const key of Object.keys(target)) {
    delete target[key]
  }
  Object.assign(target, next)
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

function displayName(project: string, projectNames: Record<string, string>, fallback?: string) {
  return fallback || projectNames[project] || project
}

function mapIssue(
  row: ApiIssueRow,
  commentCount = row.comment_count,
  projectNames: Record<string, string> = {},
): OperatorIssue {
  const sourceProject = row.source_project || 'operator-console'
  const targetProject = row.target_project || 'engram'
  const createdAt = row.created_at || ''
  const updatedAt = row.updated_at || createdAt
  return {
    id: row.id,
    title: row.title || '-',
    body: row.body || '',
    status: normalizeStatus(row.status),
    priority: normalizePriority(row.priority),
    type: normalizeType(row.type),
    age: compactAge(updatedAt || createdAt),
    comments: typeof commentCount === 'number' ? commentCount : 0,
    sourceProject,
    targetProject,
    sourceDisplayName: displayName(sourceProject, projectNames, row.source_project_display_name),
    targetDisplayName: displayName(targetProject, projectNames, row.target_project_display_name),
    labels: Array.isArray(row.labels) ? row.labels : [],
    createdAt,
    updatedAt,
  }
}

function mapComment(row: ApiIssueComment): OperatorIssueComment {
  const createdAt = row.created_at || ''
  return {
    id: row.id || 0,
    issueId: row.issue_id || 0,
    authorProject: row.author_project || 'operator-console',
    authorAgent: row.author_agent || 'operator',
    body: row.body || '',
    createdAt,
    age: compactAge(createdAt),
  }
}

function sortIssues(rows: OperatorIssue[]) {
  const priorityRank: Record<OperatorIssuePriority, number> = { critical: 0, high: 1, medium: 2, low: 3 }
  return [...rows].sort((left, right) => {
    const rank = priorityRank[left.priority] - priorityRank[right.priority]
    if (rank !== 0) return rank
    return right.id - left.id
  })
}

function uniqueProjects(rows: OperatorIssue[], tracked: string[]) {
  return [...new Set([
    'engram',
    ...tracked,
    ...rows.map((row) => row.sourceProject),
    ...rows.map((row) => row.targetProject),
  ].filter(Boolean))].sort((left, right) => left.localeCompare(right))
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
  comments: OperatorIssueComment[]
  projectNames: Record<string, string>
  trackedProjects: ComputedRef<string[]>
  detail: ComputedRef<OperatorIssue | null>
  loadState: ComputedRef<OperatorLoadState<OperatorIssue[]>>
  detailState: ComputedRef<OperatorLoadState<OperatorIssue | null>>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  routeChangeAction: OperatorUnsupportedAction
  refresh: () => Promise<void>
  openIssue: (id: number) => Promise<void>
  createIssue: (input: IssueCreateInput) => Promise<unknown>
  updateIssue: (id: number, input: IssueUpdateInput) => Promise<unknown>
  commentIssue: (id: number, body: string) => Promise<unknown>
  rejectIssue: (id: number, comment: string) => Promise<unknown>
  acknowledgeIssue: (id: number) => Promise<unknown>
  bulkAcknowledgeIssues: (ids: number[]) => Promise<unknown>
  bulkUpdateIssues: (ids: number[], input: IssueUpdateInput) => Promise<unknown>
  deleteIssue: (id: number) => Promise<unknown>
} {
  const listEvidence = endpointEvidence('/api/issues?limit=100', 'issues-list')
  const detailEvidence = endpointEvidence('/api/issues/{id}', 'issues-detail')
  const rowsState = useState<OperatorIssue[]>('live:issues-page:rows', () => [])
  const commentsState = useState<OperatorIssueComment[]>('live:issues-page:comments', () => [])
  const projectNamesState = useState<Record<string, string>>('live:issues-page:project-names', () => ({}))
  const trackedProjectsState = useState<string[]>('live:issues-page:tracked-projects', () => ['engram'])
  const detailStateValue = useState<OperatorIssue | null>('live:issues-page:detail', () => null)
  const state = useState<OperatorLoadState<OperatorIssue[]>>('live:issues-page:state', () => pendingState(listEvidence, rowsState.value))
  const detailLoadState = useState<OperatorLoadState<OperatorIssue | null>>('live:issues-page:detail-state', () => emptyState(detailEvidence, null))

  const loadState = computed(() => state.value)
  const detailState = computed(() => detailLoadState.value)
  const detail = computed(() => detailStateValue.value)
  const trackedProjects = computed(() => uniqueProjects(rowsState.value, trackedProjectsState.value))
  const pending = computed(() => state.value.kind === 'pending')
  const error = computed(() => state.value.kind === 'error' ? state.value.error.message : null)
  const routeChangeAction = unsupportedOperatorAction(
    'issue-route-change',
    'PATCH /api/issues/{id} target_project',
    'Changing target_project is not exposed by the current issue update endpoint.',
  )

  async function loadTrackedProjects() {
    try {
      const payload = await operatorFetchJson<ApiTrackedProjects>('/api/issues/tracked-projects', undefined, 'issues-tracked-projects')
      replaceArray(trackedProjectsState.value, payload.projects?.length ? payload.projects : ['engram'])
    } catch (error) {
      if (import.meta.dev) {
        console.warn('[useOperatorIssues] tracked projects live load failed', error)
      }
    }
  }

  async function refresh() {
    state.value = pendingState(listEvidence, rowsState.value)
    const result = await loadOperatorJson<ApiIssueList>('/api/issues?limit=100', {
      source: 'issues-list',
      empty: (payload) => !(payload.issues || []).length,
    })

    if (result.kind === 'live' || result.kind === 'empty') {
      replaceRecord(projectNamesState.value, result.data.project_names || {})
      const nextRows = sortIssues((result.data.issues || []).map((row) => mapIssue(row, row.comment_count, projectNamesState.value)))
      replaceArray(rowsState.value, nextRows)
      await loadTrackedProjects()
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
    replaceArray(commentsState.value, [])
    try {
      const payload = await operatorFetchJson<ApiIssueDetail>(`/api/issues/${id}`, undefined, 'issues-detail')
      const detailProjectNames = {
        ...projectNamesState.value,
        ...(payload.issue?.source_project ? { [payload.issue.source_project]: payload.source_project_display_name || payload.issue.source_project } : {}),
        ...(payload.issue?.target_project ? { [payload.issue.target_project]: payload.target_project_display_name || payload.issue.target_project } : {}),
      }
      replaceRecord(projectNamesState.value, detailProjectNames)
      const next = payload.issue
        ? mapIssue({
            ...payload.issue,
            source_project_display_name: payload.source_project_display_name,
            target_project_display_name: payload.target_project_display_name,
          }, payload.comment_count ?? payload.comments?.length, projectNamesState.value)
        : null
      detailStateValue.value = next
      replaceArray(commentsState.value, (payload.comments || []).map((row) => mapComment(row)))
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
        source_project: input.sourceProject || 'operator-console',
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
      snapshot: () => ({
        rows: [...rowsState.value],
        detail: detailStateValue.value ? { ...detailStateValue.value } : null,
        comments: [...commentsState.value],
      }),
      optimistic: () => {
        const patch = {
          ...(input.title !== undefined ? { title: input.title } : {}),
          ...(input.body !== undefined ? { body: input.body } : {}),
          ...(input.priority !== undefined ? { priority: input.priority } : {}),
          ...(input.type !== undefined ? { type: input.type } : {}),
          ...(input.status !== undefined ? { status: input.status } : {}),
          ...(input.labels !== undefined ? { labels: input.labels } : {}),
        }
        replaceArray(rowsState.value, rowsState.value.map((row) => row.id === id ? { ...row, ...patch } : row))
        if (detailStateValue.value?.id === id) {
          detailStateValue.value = { ...detailStateValue.value, ...patch }
        }
      },
      run: () => operatorFetchJson(`/api/issues/${id}`, jsonInit('PATCH', {
        ...(input.title !== undefined ? { title: input.title } : {}),
        ...(input.body !== undefined ? { body: input.body } : {}),
        ...(input.priority !== undefined ? { priority: input.priority } : {}),
        ...(input.type !== undefined ? { type: input.type } : {}),
        ...(input.status !== undefined ? { status: input.status } : {}),
        ...(input.comment !== undefined ? { comment: input.comment } : {}),
        ...(input.labels !== undefined ? { labels: input.labels } : {}),
        source_project: 'dashboard',
        source_agent: 'operator-console',
      }), 'issues-update'),
      rollback: (snapshot) => {
        replaceArray(rowsState.value, snapshot?.rows || [])
        detailStateValue.value = snapshot?.detail || null
        replaceArray(commentsState.value, snapshot?.comments || [])
      },
      refresh: async () => {
        await refresh()
        if (detailStateValue.value?.id === id) {
          await openIssue(id)
        }
      },
    })
  }

  async function commentIssue(id: number, body: string) {
    return updateIssue(id, { comment: body })
  }

  async function rejectIssue(id: number, comment: string) {
    return updateIssue(id, { status: 'rejected', comment })
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

  async function bulkAcknowledgeIssues(ids: number[]) {
    return runOperatorMutation({
      action: 'issues-bulk-acknowledge',
      evidence: endpointEvidence('/api/issues/acknowledge', 'issues-bulk-acknowledge'),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        const set = new Set(ids)
        replaceArray(rowsState.value, rowsState.value.map((row) => set.has(row.id) ? { ...row, status: 'acknowledged' } : row))
      },
      run: () => operatorFetchJson<ApiIssueAcknowledgeReceipt>('/api/issues/acknowledge', jsonInit('POST', { ids }), 'issues-bulk-acknowledge'),
      rollback: (snapshot) => replaceArray(rowsState.value, snapshot || []),
      refresh,
    })
  }

  async function bulkUpdateIssues(ids: number[], input: IssueUpdateInput) {
    return runOperatorMutation({
      action: 'issues-bulk-update',
      evidence: endpointEvidence('/api/issues/{id}', 'issues-bulk-update'),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        const set = new Set(ids)
        replaceArray(rowsState.value, rowsState.value.map((row) => set.has(row.id)
          ? {
              ...row,
              ...(input.priority !== undefined ? { priority: input.priority } : {}),
              ...(input.type !== undefined ? { type: input.type } : {}),
              ...(input.status !== undefined ? { status: input.status } : {}),
              ...(input.labels !== undefined ? { labels: input.labels } : {}),
            }
          : row))
      },
      run: async () => Promise.all(ids.map((id) => operatorFetchJson(`/api/issues/${id}`, jsonInit('PATCH', {
        ...(input.priority !== undefined ? { priority: input.priority } : {}),
        ...(input.type !== undefined ? { type: input.type } : {}),
        ...(input.status !== undefined ? { status: input.status } : {}),
        ...(input.comment !== undefined ? { comment: input.comment } : {}),
        ...(input.labels !== undefined ? { labels: input.labels } : {}),
        source_project: 'dashboard',
        source_agent: 'operator-console',
      }), 'issues-bulk-update'))),
      rollback: (snapshot) => replaceArray(rowsState.value, snapshot || []),
      refresh,
    })
  }

  async function deleteIssue(id: number) {
    return runOperatorMutation({
      action: 'issue-delete',
      evidence: endpointEvidence(`/api/issues/${id}`, 'issues-delete'),
      snapshot: () => ({
        rows: [...rowsState.value],
        detail: detailStateValue.value ? { ...detailStateValue.value } : null,
        comments: [...commentsState.value],
      }),
      optimistic: () => {
        replaceArray(rowsState.value, rowsState.value.filter((row) => row.id !== id))
        if (detailStateValue.value?.id === id) {
          detailStateValue.value = null
          replaceArray(commentsState.value, [])
        }
      },
      run: () => operatorFetchJson(`/api/issues/${id}`, jsonInit('DELETE'), 'issues-delete'),
      rollback: (snapshot) => {
        replaceArray(rowsState.value, snapshot?.rows || [])
        detailStateValue.value = snapshot?.detail || null
        replaceArray(commentsState.value, snapshot?.comments || [])
      },
      refresh,
    })
  }

  startOnce('issues-page', refresh)

  return {
    rows: rowsState.value,
    comments: commentsState.value,
    projectNames: projectNamesState.value,
    trackedProjects,
    detail,
    loadState,
    detailState,
    pending,
    error,
    routeChangeAction,
    refresh,
    openIssue,
    createIssue,
    updateIssue,
    commentIssue,
    rejectIssue,
    acknowledgeIssue,
    bulkAcknowledgeIssues,
    bulkUpdateIssues,
    deleteIssue,
  }
}
