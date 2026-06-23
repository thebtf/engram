import type { ComputedRef, Ref } from 'vue'
import type { OperatorLoadState, OperatorMutationResult } from './useOperatorApi'
import {
  emptyState,
  endpointEvidence,
  errorState,
  gatedState,
  liveState,
  OperatorFetchError,
  operatorFetchJson,
  pendingState,
  runOperatorMutation,
  toOperatorSourceError,
} from './useOperatorApi'

const QUEUE_LIMIT = 100
const QUEUE_FLAG = 'ENGRAM_VNEXT_F_ENABLED'
const QUEUE_STATUS = 'pending'

interface ApiFlags {
  flags?: Record<string, boolean>
}

interface ApiCandidate {
  id: number | string
  status?: string
  proposed_content?: string
  proposed_promotion_target?: string
  proposed_tier?: string
  proposed_epistemic_type?: string
  source_session_id?: string
  confidence?: number
  recurrence_count?: number
  fingerprint?: string
  created_at?: string
  updated_at?: string
  review_after?: string
  evidence_handles?: string[]
  affected_projects?: string[]
  privacy_scope?: string
  promoted_memory_id?: number | string | null
}

interface ApiCandidateListResponse {
  candidates?: ApiCandidate[]
  count?: number
  project?: string
  status?: string
  limit?: number
}

export interface OperatorCandidate {
  reviewAfter?: string
  promotedMemoryId?: string
  id: string
  status: string
  content: string
  target: string
  tier: string
  epistemicType: string
  sourceSessionId: string
  confidence: number | null
  recurrenceCount: number
  fingerprint: string
  createdAt: string
  updatedAt: string
  evidenceHandles: string[]
  affectedProjects: string[]
  privacyScope: string
}

export interface CandidateActionReceipt {
  candidate_id: number
  candidate_status: string
  memory_id?: number
  promoted_memory_id?: number
  action: 'promote' | 'reject' | 'supersede'
}

function jsonInit(method: 'POST', body?: unknown): RequestInit {
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

function mapCandidate(row: ApiCandidate): OperatorCandidate {
  const confidence = typeof row.confidence === 'number' && Number.isFinite(row.confidence)
    ? row.confidence
    : null

  return {
    id: String(row.id),
    status: row.status || 'pending',
    content: row.proposed_content || '-',
    target: row.proposed_promotion_target || 'none',
    tier: row.proposed_tier || 'episodic',
    epistemicType: row.proposed_epistemic_type || 'observation',
    sourceSessionId: row.source_session_id || '-',
    confidence,
    recurrenceCount: typeof row.recurrence_count === 'number' ? row.recurrence_count : 0,
    fingerprint: row.fingerprint || '',
    createdAt: row.created_at || '',
    updatedAt: row.updated_at || '',
    reviewAfter: row.review_after || undefined,
    evidenceHandles: Array.isArray(row.evidence_handles) ? row.evidence_handles : [],
    affectedProjects: Array.isArray(row.affected_projects) ? row.affected_projects : [],
    privacyScope: row.privacy_scope || 'project',
    promotedMemoryId: row.promoted_memory_id === undefined || row.promoted_memory_id === null
      ? undefined
      : String(row.promoted_memory_id),
  }
}

function parseCandidatePayload(payload: ApiCandidateListResponse, path: string): OperatorCandidate[] {
  if (!payload || typeof payload !== 'object' || !Array.isArray(payload.candidates)) {
    throw new OperatorFetchError(`Invalid candidate payload from ${path}: expected { candidates: [] }`, {
      message: `Invalid candidate payload from ${path}: expected { candidates: [] }`,
      source: 'candidate-queue',
      path,
      method: 'GET',
      retryable: false,
    })
  }

  return payload.candidates.map(mapCandidate)
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorQueue] ${key} live load failed`, error)
      }
    })
  }
}

export function useOperatorQueue(): {
  rows: OperatorCandidate[]
  projects: string[]
  selectedProject: Ref<string>
  loadState: ComputedRef<OperatorLoadState<OperatorCandidate[]>>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  refresh: () => Promise<void>
  promoteCandidate: (id: string) => Promise<OperatorMutationResult<CandidateActionReceipt>>
  rejectCandidate: (id: string, reason?: string) => Promise<OperatorMutationResult<CandidateActionReceipt>>
  supersedeCandidate: (id: string) => Promise<OperatorMutationResult<CandidateActionReceipt>>
} {
  const evidence = endpointEvidence(`/api/memory/candidates?project={project}&status=${QUEUE_STATUS}&limit=${QUEUE_LIMIT}`, 'candidate-queue', {
    flag: QUEUE_FLAG,
  })
  const rowsState = useState<OperatorCandidate[]>('live:candidate-queue:rows', () => [])
  const projectsState = useState<string[]>('live:candidate-queue:projects', () => [])
  const selectedProject = useState<string>('live:candidate-queue:selected-project', () => '')
  const state = useState<OperatorLoadState<OperatorCandidate[]>>('live:candidate-queue:state', () => pendingState(evidence, rowsState.value))

  const loadState = computed(() => state.value)
  const pending = computed(() => state.value.kind === 'pending')
  const error = computed(() => state.value.kind === 'error' ? state.value.error.message : null)

  async function refresh() {
    state.value = pendingState(evidence, rowsState.value)
    try {
      const flags = await operatorFetchJson<ApiFlags>('/api/flags', undefined, 'candidate-flags')
      if (flags.flags?.[QUEUE_FLAG] !== true) {
        replaceArray(rowsState.value, [])
        state.value = gatedState(evidence, QUEUE_FLAG, 'Candidate review queue is disabled by the vNext-F feature flag.', rowsState.value)
        return
      }

      const projects = await operatorFetchJson<string[]>('/api/projects', undefined, 'candidate-projects')
      replaceArray(projectsState.value, Array.isArray(projects) ? projects.filter(Boolean).sort() : [])
      if (!selectedProject.value && projectsState.value.length) {
        selectedProject.value = projectsState.value[0]
      }

      if (!selectedProject.value) {
        replaceArray(rowsState.value, [])
        state.value = emptyState(evidence, rowsState.value)
        return
      }

      const path = `/api/memory/candidates?project=${encodeURIComponent(selectedProject.value)}&status=${QUEUE_STATUS}&limit=${QUEUE_LIMIT}`
      const payload = await operatorFetchJson<ApiCandidateListResponse>(path, undefined, 'candidate-queue')
      const rows = parseCandidatePayload(payload, path)
      replaceArray(rowsState.value, rows)
      state.value = rows.length
        ? liveState(evidence, rowsState.value)
        : emptyState(evidence, rowsState.value)
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'candidate-queue',
        path: evidence.endpoint,
        method: 'GET',
      })
      state.value = errorState(evidence, mapped, {
        source: 'candidate-queue',
        run: async () => {
          await refresh()
          return state.value
        },
      }, rowsState.value)
    }
  }

  function actionPath(id: string, action: CandidateActionReceipt['action']) {
    return `/api/memory/candidates/${encodeURIComponent(id)}/${action}`
  }

  function runCandidateAction(id: string, action: CandidateActionReceipt['action'], body?: unknown) {
    const path = actionPath(id, action)
    return runOperatorMutation<CandidateActionReceipt>({
      action: `candidate-${action}`,
      evidence: endpointEvidence(path, 'candidate-queue-action', { flag: QUEUE_FLAG }),
      snapshot: () => [...rowsState.value],
      optimistic: () => {
        replaceArray(rowsState.value, rowsState.value.filter((row) => row.id !== id))
      },
      run: () => operatorFetchJson<CandidateActionReceipt>(path, jsonInit('POST', body), 'candidate-queue-action'),
      rollback: (snapshot) => {
        replaceArray(rowsState.value, snapshot || [])
      },
      refresh,
    })
  }

  function promoteCandidate(id: string) {
    return runCandidateAction(id, 'promote')
  }

  function rejectCandidate(id: string, reason = 'operator rejected candidate') {
    return runCandidateAction(id, 'reject', { reason })
  }

  function supersedeCandidate(id: string) {
    return runCandidateAction(id, 'supersede')
  }

  startOnce('candidate-queue', refresh)

  return {
    rows: rowsState.value,
    projects: projectsState.value,
    selectedProject,
    loadState,
    pending,
    error,
    refresh,
    promoteCandidate,
    rejectCandidate,
    supersedeCandidate,
  }
}
