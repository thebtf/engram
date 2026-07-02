import type { ComputedRef, Ref } from 'vue'
import type { OperatorLoadState, OperatorMutationResult } from './useOperatorApi'
import {
  emptyState,
  endpointEvidence,
  errorState,
  liveState,
  OperatorFetchError,
  operatorFetchJson,
  pendingState,
  runOperatorMutation,
  toOperatorSourceError,
} from './useOperatorApi'

const BOOKS_CREATE_ENDPOINT = '/api/books'
const DEFAULT_BOOKS_PROJECT = 'engram'
const BOOKS_POLL_INTERVAL_MS = 3000

interface ApiBookJobResponse {
  id?: number | string
  status?: string
  source_ref?: string
  error?: string
  created_at?: string
  updated_at?: string
  documents_path_prefix?: string
  documents_link?: string
}

function bookStatusEndpoint(jobID: string | number): string {
  return `/api/books/${encodeURIComponent(String(jobID))}/status`
}

function jsonInit(method: 'POST', body?: unknown): RequestInit {
  const init: RequestInit = { method }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  return init
}

function parseBookJobPayload(
  payload: ApiBookJobResponse,
  path: string,
  source: string,
  method: 'GET' | 'POST',
): OperatorBookJob {
  if (!payload || typeof payload !== 'object' || (payload.id === undefined || payload.id === null)) {
    throw new OperatorFetchError(`Invalid books payload from ${path}: expected job fields`, {
      message: `Invalid books payload from ${path}: expected job fields`,
      path,
      method,
      source,
      retryable: false,
    })
  }
  return {
    id: String(payload.id),
    status: typeof payload.status === 'string' ? payload.status : 'pending',
    sourceRef: typeof payload.source_ref === 'string' ? payload.source_ref : '',
    error: typeof payload.error === 'string' ? payload.error : '',
    createdAt: typeof payload.created_at === 'string' ? payload.created_at : '',
    updatedAt: typeof payload.updated_at === 'string' ? payload.updated_at : '',
    documentsPathPrefix: typeof payload.documents_path_prefix === 'string' ? payload.documents_path_prefix : '',
    documentsLink: typeof payload.documents_link === 'string' && payload.documents_link ? payload.documents_link : '/documents',
  }
}

export interface CreateBookJobInput {
  sourceRef: string
  content: string
  project?: string
  author?: string
}

export interface OperatorBookJob {
  id: string
  status: string
  sourceRef: string
  error: string
  createdAt: string
  updatedAt: string
  documentsPathPrefix: string
  documentsLink: string
}

export interface OperatorBooksComposable {
  currentJob: Ref<OperatorBookJob | null>
  currentProject: Ref<string>
  jobState: ComputedRef<OperatorLoadState<OperatorBookJob | null>>
  submitting: ComputedRef<boolean>
  error: ComputedRef<string | null>
  documentsHref: ComputedRef<string>
  refreshJobStatus: () => Promise<void>
  ingestBook: (input: CreateBookJobInput) => Promise<OperatorMutationResult<OperatorBookJob>>
}
export function useOperatorBooks(): OperatorBooksComposable {
  let booksPollTimer: number | null = null

  function clearBooksPollTimer() {
    if (!import.meta.client) return
    if (booksPollTimer !== null) {
      window.clearTimeout(booksPollTimer)
      booksPollTimer = null
    }
  }

  const createEvidence = endpointEvidence(BOOKS_CREATE_ENDPOINT, 'books-create')
  const currentJob = useState<OperatorBookJob | null>('live:books:current-job', () => null)
  const currentProject = useState<string>('live:books:current-project', () => DEFAULT_BOOKS_PROJECT)
  const jobStateValue = useState<OperatorLoadState<OperatorBookJob | null>>(
    'live:books:job-state',
    () => emptyState(endpointEvidence('/api/books/{id}/status', 'books-status'), null),
  )
  const submittingValue = useState<boolean>('live:books:submitting', () => false)

  const jobState = computed(() => jobStateValue.value)
  const submitting = computed(() => submittingValue.value)
  const error = computed(() => jobStateValue.value.kind === 'error' ? jobStateValue.value.error.message : null)
  const documentsHref = computed(() => {
    const job = currentJob.value
    if (!job) {
      return '/documents'
    }
    const base = job.documentsLink || '/documents'
    const params = new URLSearchParams()
    if (currentProject.value) {
      params.set('project', currentProject.value)
    }
    if (job.documentsPathPrefix) {
      params.set('pathPrefix', job.documentsPathPrefix)
    }
    const query = params.toString()
    return query ? `${base}?${query}` : base
  })


  function statusEvidence(jobID: string) {
    return endpointEvidence(bookStatusEndpoint(jobID), 'books-status')
  }

  function schedulePolling() {
    if (!import.meta.client) return
    clearBooksPollTimer()
    const job = currentJob.value
    if (!job || (job.status !== 'pending' && job.status !== 'processing')) {
      return
    }
    booksPollTimer = window.setTimeout(() => {
      void refreshJobStatus()
    }, BOOKS_POLL_INTERVAL_MS)
  }

  function reconcilePolling() {
    if (currentJob.value?.status === 'pending' || currentJob.value?.status === 'processing') {
      schedulePolling()
      return
    }
    clearBooksPollTimer()
  }

  async function refreshJobStatus() {
    const job = currentJob.value
    if (!job?.id) {
      clearBooksPollTimer()
      jobStateValue.value = emptyState(endpointEvidence('/api/books/{id}/status', 'books-status'), null)
      return
    }

    const path = bookStatusEndpoint(job.id)
    const evidence = statusEvidence(job.id)
    jobStateValue.value = pendingState(evidence, currentJob.value)
    try {
      const payload = await operatorFetchJson<ApiBookJobResponse>(path, undefined, 'books-status')
      const nextJob = parseBookJobPayload(payload, path, 'books-status', 'GET')
      currentJob.value = {
        ...nextJob,
        documentsLink: nextJob.documentsLink || currentJob.value?.documentsLink || '/documents',
      }
      jobStateValue.value = liveState(evidence, currentJob.value)
      reconcilePolling()
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'books-status',
        path,
        method: 'GET',
      })
      jobStateValue.value = errorState(evidence, mapped, {
        source: 'books-status',
        run: async () => {
          await refreshJobStatus()
          return jobStateValue.value
        },
      }, currentJob.value)
      clearBooksPollTimer()
    }
  }

  async function ingestBook(input: CreateBookJobInput): Promise<OperatorMutationResult<OperatorBookJob>> {
    const sourceRef = input.sourceRef.trim()
    const content = input.content.trim()
    const project = (input.project || DEFAULT_BOOKS_PROJECT).trim() || DEFAULT_BOOKS_PROJECT
    const author = (input.author || 'operator-console').trim() || 'operator-console'

    currentProject.value = project
    submittingValue.value = true
    jobStateValue.value = pendingState(createEvidence, currentJob.value)

    try {
      const result = await runOperatorMutation<OperatorBookJob>({
        action: 'book-ingest',
        evidence: createEvidence,
        snapshot: () => currentJob.value ? { ...currentJob.value } : null,
        run: async () => {
          const payload = await operatorFetchJson<ApiBookJobResponse>(BOOKS_CREATE_ENDPOINT, jsonInit('POST', {
            source_ref: sourceRef,
            content,
            project,
            author,
          }), 'books-create')
          const job = parseBookJobPayload(payload, BOOKS_CREATE_ENDPOINT, 'books-create', 'POST')
          currentJob.value = job
          jobStateValue.value = liveState(statusEvidence(job.id), currentJob.value)
          reconcilePolling()
          return job
        },
        rollback: (snapshot) => {
          currentJob.value = snapshot ? { ...snapshot } : null
          if (currentJob.value) {
            jobStateValue.value = liveState(statusEvidence(currentJob.value.id), currentJob.value)
          } else {
            jobStateValue.value = emptyState(endpointEvidence('/api/books/{id}/status', 'books-status'), null)
          }
          reconcilePolling()
        },
        refresh: refreshJobStatus,
      })
      return result
    } finally {
      submittingValue.value = false
    }
  }

  onMounted(() => {
    if (currentJob.value?.id) {
      void refreshJobStatus()
    }
  })

  onBeforeUnmount(() => {
    clearBooksPollTimer()
  })

  return {
    currentJob,
    currentProject,
    jobState,
    submitting,
    error,
    documentsHref,
    refreshJobStatus,
    ingestBook,
  }
}
