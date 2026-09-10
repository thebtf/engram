import type { ComputedRef, Ref } from 'vue'
import type { OperatorLoadState } from './useOperatorApi'
import { executeMutation, type MutationResult } from './useApi'
import type { OperatorSelection } from './useOperatorSelection'
import { parseOperatorSelectionSnapshot } from './useOperatorSelection'
import {
  emptyState,
  endpointEvidence,
  errorState,
  gatedState,
  liveState,
  OperatorFetchError,
  operatorApiUrl,
  operatorFetchJson,
  pendingState,
  toOperatorSourceError,
} from './useOperatorApi'

const QUEUE_LIMIT = 100
const QUEUE_FLAG = 'ENGRAM_VNEXT_F_ENABLED'
const QUEUE_STATUS = 'pending'
const QUEUE_ALL_PROJECTS_API = 'all'

export const QUEUE_ALL_PROJECTS = '__all__'

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

export type CandidateAction = 'promote' | 'reject' | 'supersede'

export interface CandidateActionIntent {
  id: string
  action: CandidateAction
  reason?: string
}

interface CandidateActionStatus {
  candidateId: string
  candidateStatus: 'promoted' | 'rejected' | 'superseded'
}

type CandidateActionCurrentState = CandidateActionStatus | CandidateActionStatus[]

interface SelectionEnvelope {
  selection: unknown
}

function jsonInit(method: 'POST', body?: unknown, requestId?: string): RequestInit {
  const init: RequestInit = { method }
  if (body !== undefined) {
    init.headers = {
      'Content-Type': 'application/json',
      ...(requestId === undefined ? {} : { 'X-Engram-Request-ID': requestId }),
    }
    init.body = JSON.stringify(body)
  }
  return init
}

function candidateStatusForAction(action: CandidateAction): CandidateActionStatus['candidateStatus'] {
  switch (action) {
    case 'promote': return 'promoted'
    case 'reject': return 'rejected'
    case 'supersede': return 'superseded'
  }
}

function parseCandidateActionStatus(value: unknown, action: CandidateAction): CandidateActionStatus | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined
  const candidateId = Reflect.get(value, 'candidate_id')
  const candidateStatus = Reflect.get(value, 'candidate_status')
  if ((typeof candidateId !== 'string' && typeof candidateId !== 'number') || candidateStatus !== candidateStatusForAction(action)) return undefined
  return { candidateId: String(candidateId), candidateStatus }
}

function candidateActionCurrentStateParser(action: CandidateAction) {
  return (value: unknown): CandidateActionCurrentState | undefined => {
    if (!Array.isArray(value)) return parseCandidateActionStatus(value, action)
    if (!value.length) return undefined
    const statuses: CandidateActionStatus[] = []
    for (const entry of value) {
      const status = parseCandidateActionStatus(entry, action)
      if (status === undefined) return undefined
      statuses.push(status)
    }
    return statuses
  }
}

function operationSelectionPayload(selection: OperatorSelection): Record<string, unknown> {
  if (selection.version < 1) throw new TypeError('the selected Queue snapshot has no server version')
  switch (selection.kind) {
    case 'explicit':
    case 'page':
      return { kind: selection.kind, selection_version: selection.version }
    case 'frozen_filter':
      return { kind: 'frozen_filter', selection_version: selection.version, selection_token: selection.selectionToken }
    case 'none':
      throw new TypeError('a Queue action requires a selected candidate')
  }
}

function parseQueueSelection(value: unknown): OperatorSelection {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new TypeError('Queue selection response is invalid')
  }
  return parseOperatorSelectionSnapshot(Reflect.get(value, 'selection'))
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
  promoteCandidate: (id: string) => Promise<MutationResult<CandidateActionIntent, CandidateActionCurrentState>>
  rejectCandidate: (id: string, reason?: string) => Promise<MutationResult<CandidateActionIntent, CandidateActionCurrentState>>
  supersedeCandidate: (id: string) => Promise<MutationResult<CandidateActionIntent, CandidateActionCurrentState>>
} {
  const evidence = endpointEvidence(`/api/memory/candidates?project={project}&status=${QUEUE_STATUS}&limit=${QUEUE_LIMIT}`, 'candidate-queue', {
    flag: QUEUE_FLAG,
  })
  const rowsState = useState<OperatorCandidate[]>('live:candidate-queue:rows', () => [])
  const projectsState = useState<string[]>('live:candidate-queue:projects', () => [])
  const selectedProject = useState<string>('live:candidate-queue:selected-project', () => QUEUE_ALL_PROJECTS)
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
      const nextProjects = Array.isArray(projects) ? projects.filter(Boolean).sort() : []
      replaceArray(projectsState.value, [QUEUE_ALL_PROJECTS, ...nextProjects])
      if (!selectedProject.value || !projectsState.value.includes(selectedProject.value)) {
        selectedProject.value = QUEUE_ALL_PROJECTS
      }

      const apiProject = selectedProject.value === QUEUE_ALL_PROJECTS ? QUEUE_ALL_PROJECTS_API : selectedProject.value
      const path = `/api/memory/candidates?project=${encodeURIComponent(apiProject)}&status=${QUEUE_STATUS}&limit=${QUEUE_LIMIT}`
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

  async function runCandidateAction(id: string, action: CandidateAction, reason?: string): Promise<MutationResult<CandidateActionIntent, CandidateActionCurrentState>> {
    const requestId = crypto.randomUUID()
    const intent = { id, action, ...(reason === undefined ? {} : { reason }) }
    const request = { requestId, action: `candidate-${action}`, intent }
    let selectionResponse: Response
    try {
      selectionResponse = await fetch(operatorApiUrl('/api/collections/selection'), {
        ...jsonInit('POST', {
          domain: 'queue',
          selection: { kind: 'explicit', targets: [{ id }] },
        }, requestId),
        credentials: 'include',
      })
    } catch {
      return { kind: 'network', request }
    }
    if (!selectionResponse.ok) {
      return executeMutation(request, Promise.resolve(selectionResponse), candidateActionCurrentStateParser(action))
    }

    let selection: OperatorSelection
    try {
      selection = parseQueueSelection(await selectionResponse.json() as SelectionEnvelope)
      if (selection.kind !== 'explicit' || selection.targets.length !== 1 || selection.targets[0].id !== id) {
        throw new TypeError('Queue selection did not retain the requested candidate')
      }
    } catch {
      return { kind: 'failed', request, httpStatus: selectionResponse.status, code: 'invalid_queue_selection_snapshot' }
    }

    const body = {
      request_id: requestId,
      action,
      selection: operationSelectionPayload(selection),
      ...(reason === undefined ? {} : { reason }),
    }
    return executeMutation(
      request,
      fetch(operatorApiUrl('/api/memory/candidates/operations'), {
        ...jsonInit('POST', body, requestId),
        credentials: 'include',
      }),
      candidateActionCurrentStateParser(action),
    )
  }

  function promoteCandidate(id: string) {
    return runCandidateAction(id, 'promote')
  }

  function rejectCandidate(id: string, reason = 'operator rejected candidate') {
    return runCandidateAction(id, 'reject', reason)
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
