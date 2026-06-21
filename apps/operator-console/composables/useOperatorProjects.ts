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
  runOperatorMutation,
  toOperatorSourceError,
  unsupportedOperatorAction,
} from './useOperatorApi'

interface ApiSessionRow {
  id?: number | string
  claude_session_id?: string
  claudeSessionId?: string
  sdk_session_id?: string
  project?: string
  status?: string
  started_at?: string
  completed_at?: string
  prompt_counter?: number
  injection_strategy?: string
  outcome?: string
  outcome_reason?: string
  worker_port?: number | string
  user_prompt?: string
}

interface ApiSessionsList {
  sessions?: ApiSessionRow[]
  total?: number
  limit?: number
  offset?: number
}

export interface OperatorProjectRow {
  id: string
  sessionCount: number
  activeSessions: number
  lastActivity: string
}

export interface OperatorSessionRow {
  id: string
  claudeSessionId: string
  sdkSessionId: string
  project: string
  status: string
  startedAt: string
  completedAt: string
  promptCounter: number
  injectionStrategy: string
  outcome: string
  outcomeReason: string
  workerPort: string
  userPrompt: string
}

function jsonInit(method: 'DELETE'): RequestInit {
  return { method }
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

function mapSession(row: ApiSessionRow): OperatorSessionRow {
  const claudeSessionId = row.claude_session_id || row.claudeSessionId || ''
  return {
    id: String(row.id ?? (claudeSessionId || '-')),
    claudeSessionId,
    sdkSessionId: row.sdk_session_id || '-',
    project: row.project || 'unknown',
    status: row.status || 'unknown',
    startedAt: row.started_at || '',
    completedAt: row.completed_at || '',
    promptCounter: typeof row.prompt_counter === 'number' ? row.prompt_counter : 0,
    injectionStrategy: row.injection_strategy || '-',
    outcome: row.outcome || '-',
    outcomeReason: row.outcome_reason || '-',
    workerPort: row.worker_port === undefined ? '-' : String(row.worker_port),
    userPrompt: row.user_prompt || '',
  }
}

function sortSessions(rows: OperatorSessionRow[]) {
  return [...rows].sort((left, right) => {
    const l = Date.parse(left.startedAt)
    const r = Date.parse(right.startedAt)
    return (Number.isNaN(r) ? 0 : r) - (Number.isNaN(l) ? 0 : l)
  })
}

function summarizeProject(project: string, sessions: OperatorSessionRow[]): OperatorProjectRow {
  const scoped = sessions.filter((row) => row.project === project)
  const last = scoped[0]?.startedAt
  return {
    id: project,
    sessionCount: scoped.length,
    activeSessions: scoped.filter((row) => row.status === 'active').length,
    lastActivity: compactAge(last),
  }
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorProjects] ${key} live load failed`, error)
      }
    })
  }
}

export function useOperatorProjects(): {
  projectRows: ComputedRef<OperatorProjectRow[]>
  sessions: OperatorSessionRow[]
  selectedProject: Ref<string>
  selectedSession: Ref<OperatorSessionRow | null>
  projectsState: ComputedRef<OperatorLoadState<string[]>>
  sessionsState: ComputedRef<OperatorLoadState<OperatorSessionRow[]>>
  detailState: ComputedRef<OperatorLoadState<OperatorSessionRow | null>>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  refresh: () => Promise<void>
  openProject: (project: string) => Promise<void>
  openSession: (session: OperatorSessionRow) => Promise<void>
  deleteProject: (project: string) => Promise<unknown>
  sessionDetailGap: ReturnType<typeof unsupportedOperatorAction>
  sessionStrategyGap: ReturnType<typeof unsupportedOperatorAction>
  codeIntelGap: ReturnType<typeof unsupportedOperatorAction>
} {
  const projectsEvidence = endpointEvidence('/api/projects', 'projects-list')
  const sessionsEvidence = endpointEvidence('/api/sessions/list?project={project}&limit=50', 'sessions-list')
  const detailEvidence = endpointEvidence('/api/sessions?claudeSessionId={id}', 'sessions-detail')

  const projects = useState<string[]>('live:projects-page:projects', () => [])
  const recentSessions = useState<OperatorSessionRow[]>('live:projects-page:recent-sessions', () => [])
  const sessions = useState<OperatorSessionRow[]>('live:projects-page:sessions', () => [])
  const selectedProject = useState<string>('live:projects-page:selected-project', () => '')
  const selectedSession = useState<OperatorSessionRow | null>('live:projects-page:selected-session', () => null)

  const projectsStateValue = useState<OperatorLoadState<string[]>>('live:projects-page:projects-state', () => pendingState(projectsEvidence, projects.value))
  const sessionsStateValue = useState<OperatorLoadState<OperatorSessionRow[]>>('live:projects-page:sessions-state', () => pendingState(sessionsEvidence, sessions.value))
  const detailStateValue = useState<OperatorLoadState<OperatorSessionRow | null>>('live:projects-page:detail-state', () => emptyState(detailEvidence, null))

  const projectsState = computed(() => projectsStateValue.value)
  const sessionsState = computed(() => sessionsStateValue.value)
  const detailState = computed(() => detailStateValue.value)
  const projectRows = computed(() => projects.value.map((project) => summarizeProject(project, recentSessions.value)))
  const pending = computed(() => projectsStateValue.value.kind === 'pending' || sessionsStateValue.value.kind === 'pending')
  const error = computed(() => {
    if (projectsStateValue.value.kind === 'error') return projectsStateValue.value.error.message
    if (sessionsStateValue.value.kind === 'error') return sessionsStateValue.value.error.message
    if (detailStateValue.value.kind === 'error') return detailStateValue.value.error.message
    return null
  })

  async function refreshProjects() {
    projectsStateValue.value = pendingState(projectsEvidence, projects.value)
    const result = await loadOperatorJson<string[]>('/api/projects', {
      source: 'projects-list',
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

  async function loadSessions(project?: string) {
    const path = project
      ? `/api/sessions/list?project=${encodeURIComponent(project)}&limit=50`
      : '/api/sessions/list?limit=200'
    return loadOperatorJson<ApiSessionsList>(path, {
      source: project ? 'sessions-list' : 'sessions-recent',
      empty: (payload) => !(payload.sessions || []).length,
    })
  }

  async function refreshRecentSessions() {
    const result = await loadSessions()
    if (result.kind === 'live' || result.kind === 'empty') {
      replaceArray(recentSessions.value, sortSessions((result.data.sessions || []).map(mapSession)))
    }
  }

  async function refreshSessions(project = selectedProject.value) {
    sessionsStateValue.value = pendingState(sessionsEvidence, sessions.value)
    if (!project) {
      replaceArray(sessions.value, [])
      sessionsStateValue.value = emptyState(sessionsEvidence, sessions.value)
      return
    }

    const result = await loadSessions(project)
    if (result.kind === 'live' || result.kind === 'empty') {
      const nextRows = sortSessions((result.data.sessions || []).map(mapSession))
      replaceArray(sessions.value, nextRows)
      sessionsStateValue.value = nextRows.length
        ? liveState(sessionsEvidence, sessions.value)
        : emptyState(sessionsEvidence, sessions.value)
      return
    }

    if (result.kind === 'error') {
      sessionsStateValue.value = errorState(sessionsEvidence, result.error, {
        source: 'sessions-list',
        run: async () => {
          await refreshSessions(project)
          return sessionsStateValue.value
        },
      }, sessions.value)
    } else {
      sessionsStateValue.value = result as OperatorLoadState<OperatorSessionRow[]>
    }
  }

  async function refresh() {
    await refreshProjects()
    await refreshRecentSessions()
    await refreshSessions(selectedProject.value)
  }

  async function openProject(project: string) {
    selectedProject.value = project
    selectedSession.value = null
    detailStateValue.value = emptyState(detailEvidence, null)
    await refreshSessions(project)
  }

  async function openSession(session: OperatorSessionRow) {
    selectedSession.value = session
    if (!session.claudeSessionId) {
      detailStateValue.value = emptyState(detailEvidence, session)
      return
    }

    const path = `/api/sessions?claudeSessionId=${encodeURIComponent(session.claudeSessionId)}`
    const evidence = endpointEvidence(path, 'sessions-detail')
    detailStateValue.value = pendingState(evidence, selectedSession.value)
    try {
      const row = await operatorFetchJson<ApiSessionRow>(path, undefined, 'sessions-detail')
      selectedSession.value = mapSession(row)
      detailStateValue.value = liveState(evidence, selectedSession.value)
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'sessions-detail',
        path,
        method: 'GET',
      })
      detailStateValue.value = errorState(evidence, mapped, {
        source: 'sessions-detail',
        run: async () => {
          await openSession(session)
          return detailStateValue.value
        },
      }, selectedSession.value)
    }
  }

  async function deleteProject(project: string) {
    return runOperatorMutation({
      action: 'project-delete',
      evidence: endpointEvidence(`/api/projects/${project}`, 'projects-delete'),
      snapshot: () => ({
        projects: [...projects.value],
        recentSessions: [...recentSessions.value],
        sessions: [...sessions.value],
        selectedProject: selectedProject.value,
      }),
      optimistic: () => {
        replaceArray(projects.value, projects.value.filter((row) => row !== project))
        replaceArray(recentSessions.value, recentSessions.value.filter((row) => row.project !== project))
        replaceArray(sessions.value, sessions.value.filter((row) => row.project !== project))
        if (selectedProject.value === project) {
          selectedProject.value = projects.value[0] || ''
          selectedSession.value = null
        }
      },
      run: () => operatorFetchJson(`/api/projects/${encodeURIComponent(project)}`, jsonInit('DELETE'), 'projects-delete'),
      rollback: (snapshot) => {
        if (!snapshot) return
        replaceArray(projects.value, snapshot.projects)
        replaceArray(recentSessions.value, snapshot.recentSessions)
        replaceArray(sessions.value, snapshot.sessions)
        selectedProject.value = snapshot.selectedProject
      },
      refresh,
    })
  }

  const sessionDetailGap = unsupportedOperatorAction(
    'session-transcript',
    'GET /api/sessions/{id}/transcript',
    'Session transcript readback is not exposed by the current server API.',
  )
  const sessionStrategyGap = unsupportedOperatorAction(
    'session-strategy',
    'GET /api/sessions/{id}/strategy',
    'Session strategy and route decisions are not exposed as a REST endpoint yet.',
  )
  const codeIntelGap = unsupportedOperatorAction(
    'project-code-intel',
    'GET /api/code-intel/projects/{project}',
    'Code-intelligence project summaries are MCP/tool-only and have no operator-console REST endpoint yet.',
  )

  startOnce('projects-page', refresh)

  return {
    projectRows,
    sessions: sessions.value,
    selectedProject,
    selectedSession,
    projectsState,
    sessionsState,
    detailState,
    pending,
    error,
    refresh,
    openProject,
    openSession,
    deleteProject,
    sessionDetailGap,
    sessionStrategyGap,
    codeIntelGap,
  }
}
