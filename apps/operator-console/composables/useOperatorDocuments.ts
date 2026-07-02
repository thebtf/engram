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

const DEFAULT_DOCUMENT_PROJECT = 'engram'
const DOCUMENT_LIST_LIMIT = 100

type ApiProjectList = string[]

interface ApiDocumentListItem {
  id?: number | string
  path?: string
  project?: string
  doc_type?: string
  author?: string
  version?: number
  created_at?: string
}

interface ApiDocumentListResponse {
  documents?: ApiDocumentListItem[]
  count?: number
  project?: string
  doc_type?: string
  path_prefix?: string
  limit?: number
}

interface ApiDocumentHistoryItem {
  id?: number | string
  version?: number
  content_hash?: string
  author?: string
  created_at?: string
}

interface ApiDocumentHistoryResponse {
  path?: string
  project?: string
  versions?: ApiDocumentHistoryItem[]
  count?: number
}

interface ApiDocumentReadResponse {
  id?: number | string
  path?: string
  project?: string
  version?: number
  content?: string
  content_hash?: string
  doc_type?: string
  metadata?: string
  author?: string
  created_at?: string
}

interface ApiDocumentCommentItem {
  id?: number | string
  document_id?: number | string
  author?: string
  content?: string
  status?: string
  line_start?: number | null
  line_end?: number | null
  created_at?: string
}

interface ApiDocumentCommentsResponse {
  comments?: ApiDocumentCommentItem[]
  count?: number
  document_id?: number | string
}

interface ApiDocumentCommentReceipt {
  comment_id?: number | string
  document_id?: number | string
  author?: string
}

export interface OperatorDocumentSummary {
  id: string
  path: string
  project: string
  docType: string
  author: string
  createdAt: string
  age: string
  version: number
}

export interface OperatorDocumentVersion {
  id: string
  contentHash: string
  author: string
  createdAt: string
  age: string
  version: number
}

export interface OperatorDocument {
  id: string
  path: string
  project: string
  content: string
  contentHash: string
  docType: string
  metadata: string
  author: string
  createdAt: string
  age: string
  version: number
}

export interface OperatorDocumentComment {
  id: string
  documentId: string
  author: string
  content: string
  status: string
  createdAt: string
  age: string
  lineStart?: number
  lineEnd?: number
}

export interface DocumentCommentInput {
  content: string
  author?: string
  lineStart?: number
  lineEnd?: number
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

function compactAge(timestamp?: string) {
  if (!timestamp) return '—'

  const value = Date.parse(timestamp)
  if (Number.isNaN(value)) return '—'

  const seconds = Math.max(0, Math.floor((Date.now() - value) / 1000))
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`
  return `${Math.floor(seconds / 86400)}d`
}

function normalizeProjectList(payload: ApiProjectList): string[] {
  if (!Array.isArray(payload)) {
    return []
  }
  return [...new Set(payload.map((row) => String(row || '').trim()).filter(Boolean))].sort((left, right) => left.localeCompare(right))
}

function mapDocumentSummary(row: ApiDocumentListItem): OperatorDocumentSummary {
  return {
    id: String(row.id ?? ''),
    path: row.path || '',
    project: row.project || DEFAULT_DOCUMENT_PROJECT,
    docType: row.doc_type || 'markdown',
    author: row.author || 'operator',
    createdAt: row.created_at || '',
    age: compactAge(row.created_at),
    version: typeof row.version === 'number' ? row.version : 0,
  }
}

function mapDocumentVersion(row: ApiDocumentHistoryItem): OperatorDocumentVersion {
  return {
    id: String(row.id ?? ''),
    contentHash: row.content_hash || '',
    author: row.author || 'operator',
    createdAt: row.created_at || '',
    age: compactAge(row.created_at),
    version: typeof row.version === 'number' ? row.version : 0,
  }
}

function mapDocument(row: ApiDocumentReadResponse): OperatorDocument {
  return {
    id: String(row.id ?? ''),
    path: row.path || '',
    project: row.project || DEFAULT_DOCUMENT_PROJECT,
    content: row.content || '',
    contentHash: row.content_hash || '',
    docType: row.doc_type || 'markdown',
    metadata: row.metadata || '{}',
    author: row.author || 'operator',
    createdAt: row.created_at || '',
    age: compactAge(row.created_at),
    version: typeof row.version === 'number' ? row.version : 0,
  }
}

function mapDocumentComment(row: ApiDocumentCommentItem): OperatorDocumentComment {
  const lineStart = typeof row.line_start === 'number' && row.line_start > 0 ? row.line_start : undefined
  const lineEnd = typeof row.line_end === 'number' && row.line_end > 0 ? row.line_end : undefined

  return {
    id: String(row.id ?? ''),
    documentId: String(row.document_id ?? ''),
    author: row.author || 'operator',
    content: row.content || '',
    status: row.status || 'open',
    createdAt: row.created_at || '',
    age: compactAge(row.created_at),
    lineStart,
    lineEnd,
  }
}

function parseDocumentListPayload(payload: ApiDocumentListResponse, path: string): OperatorDocumentSummary[] {
  if (!payload || typeof payload !== 'object' || !Array.isArray(payload.documents)) {
    throw new OperatorFetchError(`Invalid document payload from ${path}: expected { documents: [] }`, {
      message: `Invalid document payload from ${path}: expected { documents: [] }`,
      source: 'documents-list',
      path,
      method: 'GET',
      retryable: false,
    })
  }

  return payload.documents.map(mapDocumentSummary)
}

function parseDocumentHistoryPayload(payload: ApiDocumentHistoryResponse, path: string): OperatorDocumentVersion[] {
  if (!payload || typeof payload !== 'object' || !Array.isArray(payload.versions)) {
    throw new OperatorFetchError(`Invalid document history payload from ${path}: expected { versions: [] }`, {
      message: `Invalid document history payload from ${path}: expected { versions: [] }`,
      source: 'documents-history',
      path,
      method: 'GET',
      retryable: false,
    })
  }

  return payload.versions.map(mapDocumentVersion)
}

function parseDocumentReadPayload(payload: ApiDocumentReadResponse, path: string): OperatorDocument {
  if (!payload || typeof payload !== 'object' || typeof payload.path !== 'string') {
    throw new OperatorFetchError(`Invalid document read payload from ${path}: expected document fields`, {
      message: `Invalid document read payload from ${path}: expected document fields`,
      source: 'documents-read',
      path,
      method: 'GET',
      retryable: false,
    })
  }

  return mapDocument(payload)
}

function parseDocumentCommentsPayload(payload: ApiDocumentCommentsResponse, path: string): OperatorDocumentComment[] {
  if (!payload || typeof payload !== 'object' || !Array.isArray(payload.comments)) {
    throw new OperatorFetchError(`Invalid document comments payload from ${path}: expected { comments: [] }`, {
      message: `Invalid document comments payload from ${path}: expected { comments: [] }`,
      source: 'documents-comments',
      path,
      method: 'GET',
      retryable: false,
    })
  }

  return payload.comments.map(mapDocumentComment)
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorDocuments] ${key} live load failed`, error)
      }
    })
  }
}

export function useOperatorDocuments(): {
  projects: string[]
  documents: OperatorDocumentSummary[]
  history: OperatorDocumentVersion[]
  comments: OperatorDocumentComment[]
  selectedProject: Ref<string>
  selectedPath: Ref<string>
  primaryVersion: Ref<number | null>
  secondaryVersion: Ref<number | null>
  activeDocument: ComputedRef<OperatorDocumentSummary | null>
  primaryDocument: Ref<OperatorDocument | null>
  secondaryDocument: Ref<OperatorDocument | null>
  documentsState: ComputedRef<OperatorLoadState<OperatorDocumentSummary[]>>
  historyState: ComputedRef<OperatorLoadState<OperatorDocumentVersion[]>>
  primaryState: ComputedRef<OperatorLoadState<OperatorDocument | null>>
  secondaryState: ComputedRef<OperatorLoadState<OperatorDocument | null>>
  commentsState: ComputedRef<OperatorLoadState<OperatorDocumentComment[]>>
  pending: ComputedRef<boolean>
  error: ComputedRef<string | null>
  refresh: () => Promise<void>
  openDocument: (doc: OperatorDocumentSummary) => Promise<void>
  selectPrimaryVersion: (version: number) => Promise<void>
  selectSecondaryVersion: (version: number) => Promise<void>
  addComment: (input: DocumentCommentInput) => Promise<OperatorMutationResult<ApiDocumentCommentReceipt>>
} {
  const listEvidence = endpointEvidence(`/api/documents?project={project}&limit=${DOCUMENT_LIST_LIMIT}`, 'documents-list')
  const historyEvidence = endpointEvidence('/api/documents/history?path={path}&project={project}', 'documents-history')
  const primaryEvidence = endpointEvidence('/api/documents/read?path={path}&project={project}&version={version}', 'documents-read-primary')
  const secondaryEvidence = endpointEvidence('/api/documents/read?path={path}&project={project}&version={version}', 'documents-read-secondary')
  const commentsEvidence = endpointEvidence('/api/documents/comments?document_id={id}', 'documents-comments')
  const route = useRoute()

  const projects = useState<string[]>('live:documents-page:projects', () => [DEFAULT_DOCUMENT_PROJECT])
  const documents = useState<OperatorDocumentSummary[]>('live:documents-page:documents', () => [])
  const history = useState<OperatorDocumentVersion[]>('live:documents-page:history', () => [])
  const comments = useState<OperatorDocumentComment[]>('live:documents-page:comments', () => [])
  const selectedProject = useState<string>('live:documents-page:selected-project', () => DEFAULT_DOCUMENT_PROJECT)
  const selectedPath = useState<string>('live:documents-page:selected-path', () => '')
  const primaryVersion = useState<number | null>('live:documents-page:primary-version', () => null)
  const secondaryVersion = useState<number | null>('live:documents-page:secondary-version', () => null)
  const primaryDocumentValue = useState<OperatorDocument | null>('live:documents-page:primary-document', () => null)
  const secondaryDocumentValue = useState<OperatorDocument | null>('live:documents-page:secondary-document', () => null)

  const documentsStateValue = useState<OperatorLoadState<OperatorDocumentSummary[]>>('live:documents-page:documents-state', () => pendingState(listEvidence, documents.value))
  const historyStateValue = useState<OperatorLoadState<OperatorDocumentVersion[]>>('live:documents-page:history-state', () => emptyState(historyEvidence, history.value))
  const primaryStateValue = useState<OperatorLoadState<OperatorDocument | null>>('live:documents-page:primary-state', () => emptyState(primaryEvidence, null))
  const secondaryStateValue = useState<OperatorLoadState<OperatorDocument | null>>('live:documents-page:secondary-state', () => emptyState(secondaryEvidence, null))
  const commentsStateValue = useState<OperatorLoadState<OperatorDocumentComment[]>>('live:documents-page:comments-state', () => emptyState(commentsEvidence, comments.value))

  const activeDocument = computed(() => documents.value.find((row) => row.path === selectedPath.value) || null)
  const documentsState = computed(() => documentsStateValue.value)
  const historyState = computed(() => historyStateValue.value)
  const primaryState = computed(() => primaryStateValue.value)
  const secondaryState = computed(() => secondaryStateValue.value)
  const commentsState = computed(() => commentsStateValue.value)
  const pending = computed(() => documentsStateValue.value.kind === 'pending' || historyStateValue.value.kind === 'pending' || primaryStateValue.value.kind === 'pending' || secondaryStateValue.value.kind === 'pending' || commentsStateValue.value.kind === 'pending')
  const error = computed(() => {
    if (documentsStateValue.value.kind === 'error') return documentsStateValue.value.error.message
    if (historyStateValue.value.kind === 'error') return historyStateValue.value.error.message
    if (primaryStateValue.value.kind === 'error') return primaryStateValue.value.error.message
    if (secondaryStateValue.value.kind === 'error') return secondaryStateValue.value.error.message
    if (commentsStateValue.value.kind === 'error') return commentsStateValue.value.error.message
    return null
  })


  function clearDocumentDetail() {
    selectedPath.value = ''
    primaryVersion.value = null
    secondaryVersion.value = null
    primaryDocumentValue.value = null
    secondaryDocumentValue.value = null
    replaceArray(history.value, [])
    replaceArray(comments.value, [])
    historyStateValue.value = emptyState(historyEvidence, history.value)
    primaryStateValue.value = emptyState(primaryEvidence, null)
    secondaryStateValue.value = emptyState(secondaryEvidence, null)
    commentsStateValue.value = emptyState(commentsEvidence, comments.value)
  }

  async function refreshProjects() {
    try {
      const payload = await operatorFetchJson<ApiProjectList>('/api/projects', undefined, 'documents-projects')
      const rows = normalizeProjectList(payload)
      if (rows.length) {
        replaceArray(projects.value, rows)
        if (!projects.value.includes(selectedProject.value)) {
          selectedProject.value = projects.value[0]
        }
      } else if (!selectedProject.value) {
        selectedProject.value = DEFAULT_DOCUMENT_PROJECT
      }
    } catch (nextError) {
      if (import.meta.dev) {
        console.warn('[useOperatorDocuments] project list load failed', nextError)
      }
      if (!projects.value.length) {
        replaceArray(projects.value, [selectedProject.value || DEFAULT_DOCUMENT_PROJECT])
      }
    }
  }

  async function loadVersionDocument(
    path: string,
    project: string,
    version: number | null,
    stateRef: Ref<OperatorLoadState<OperatorDocument | null>>,
    valueRef: Ref<OperatorDocument | null>,
    source: 'documents-read-primary' | 'documents-read-secondary',
  ) {
    const endpoint = version === null
      ? `/api/documents/read?path=${encodeURIComponent(path)}&project=${encodeURIComponent(project)}`
      : `/api/documents/read?path=${encodeURIComponent(path)}&project=${encodeURIComponent(project)}&version=${version}`
    const evidence = endpointEvidence(endpoint, source)
    if (version === null) {
      valueRef.value = null
      stateRef.value = emptyState(evidence, null)
      return
    }

    stateRef.value = pendingState(evidence, valueRef.value)
    try {
      const payload = await operatorFetchJson<ApiDocumentReadResponse>(endpoint, undefined, source)
      valueRef.value = parseDocumentReadPayload(payload, endpoint)
      stateRef.value = liveState(evidence, valueRef.value)
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source,
        path: endpoint,
        method: 'GET',
      })
      stateRef.value = errorState(evidence, mapped, {
        source,
        run: async () => {
          await loadVersionDocument(path, project, version, stateRef, valueRef, source)
          return stateRef.value
        },
      }, valueRef.value)
    }
  }

  async function refreshCommentsForVersion(version: number | null) {
    const versionEntry = history.value.find((row) => row.version === version)
    if (!versionEntry?.id) {
      replaceArray(comments.value, [])
      commentsStateValue.value = emptyState(commentsEvidence, comments.value)
      return
    }

    const endpoint = `/api/documents/comments?document_id=${encodeURIComponent(versionEntry.id)}`
    const evidence = endpointEvidence(endpoint, 'documents-comments')
    commentsStateValue.value = pendingState(evidence, comments.value)
    try {
      const payload = await operatorFetchJson<ApiDocumentCommentsResponse>(endpoint, undefined, 'documents-comments')
      const rows = parseDocumentCommentsPayload(payload, endpoint)
      replaceArray(comments.value, rows)
      commentsStateValue.value = rows.length ? liveState(evidence, comments.value) : emptyState(evidence, comments.value)
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'documents-comments',
        path: endpoint,
        method: 'GET',
      })
      commentsStateValue.value = errorState(evidence, mapped, {
        source: 'documents-comments',
        run: async () => {
          await refreshCommentsForVersion(version)
          return commentsStateValue.value
        },
      }, comments.value)
    }
  }

  async function refreshHistory(path = selectedPath.value, project = selectedProject.value) {
    if (!path || !project) {
      clearDocumentDetail()
      return
    }

    selectedPath.value = path
    const endpoint = `/api/documents/history?path=${encodeURIComponent(path)}&project=${encodeURIComponent(project)}`
    const evidence = endpointEvidence(endpoint, 'documents-history')
    historyStateValue.value = pendingState(evidence, history.value)

    try {
      const payload = await operatorFetchJson<ApiDocumentHistoryResponse>(endpoint, undefined, 'documents-history')
      const rows = parseDocumentHistoryPayload(payload, endpoint)
      replaceArray(history.value, rows)
      historyStateValue.value = rows.length ? liveState(evidence, history.value) : emptyState(evidence, history.value)

      if (!rows.length) {
        primaryVersion.value = null
        secondaryVersion.value = null
        primaryDocumentValue.value = null
        secondaryDocumentValue.value = null
        replaceArray(comments.value, [])
        primaryStateValue.value = emptyState(primaryEvidence, null)
        secondaryStateValue.value = emptyState(secondaryEvidence, null)
        commentsStateValue.value = emptyState(commentsEvidence, comments.value)
        return
      }

      const nextPrimary = rows[0].version
      const nextSecondary = rows[1]?.version ?? rows[0].version

      primaryVersion.value = nextPrimary
      secondaryVersion.value = nextSecondary

      await Promise.all([
        loadVersionDocument(path, project, nextPrimary, primaryStateValue, primaryDocumentValue, 'documents-read-primary'),
        loadVersionDocument(path, project, nextSecondary, secondaryStateValue, secondaryDocumentValue, 'documents-read-secondary'),
        refreshCommentsForVersion(rows[0].version),
      ])
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'documents-history',
        path: endpoint,
        method: 'GET',
      })
      historyStateValue.value = errorState(evidence, mapped, {
        source: 'documents-history',
        run: async () => {
          await refreshHistory(path, project)
          return historyStateValue.value
        },
      }, history.value)
    }
  }

  async function openDocument(doc: OperatorDocumentSummary) {
    selectedPath.value = doc.path
    primaryVersion.value = null
    secondaryVersion.value = null
    await refreshHistory(doc.path, doc.project)
  }

  async function refreshDocuments() {
    const routeProject = typeof route.query.project === 'string' ? route.query.project.trim() : ''
    const project = (routeProject || selectedProject.value || DEFAULT_DOCUMENT_PROJECT).trim() || DEFAULT_DOCUMENT_PROJECT
    if (project !== selectedProject.value) {
      selectedProject.value = project
    }
    const routePathPrefix = typeof route.query.pathPrefix === 'string'
      ? route.query.pathPrefix.trim()
      : typeof route.query.path_prefix === 'string'
        ? route.query.path_prefix.trim()
        : ''
    const params = new URLSearchParams({
      project,
      limit: String(DOCUMENT_LIST_LIMIT),
    })
    if (routePathPrefix) {
      params.set('path_prefix', routePathPrefix)
    }

    const endpoint = `/api/documents?${params.toString()}`
    const evidence = endpointEvidence(endpoint, 'documents-list')
    documentsStateValue.value = pendingState(evidence, documents.value)

    try {
      const payload = await operatorFetchJson<ApiDocumentListResponse>(endpoint, undefined, 'documents-list')
      const rows = parseDocumentListPayload(payload, endpoint)
      replaceArray(documents.value, rows)
      documentsStateValue.value = rows.length ? liveState(evidence, documents.value) : emptyState(evidence, documents.value)

      if (!rows.length) {
        clearDocumentDetail()
        return
      }

      const nextDocument = rows.find((row) => row.path === selectedPath.value) || rows[0]
      await openDocument(nextDocument)
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'documents-list',
        path: endpoint,
        method: 'GET',
      })
      documentsStateValue.value = errorState(evidence, mapped, {
        source: 'documents-list',
        run: async () => {
          await refreshDocuments()
          return documentsStateValue.value
        },
      }, documents.value)
    }
  }

  async function refresh() {
    await refreshProjects()
    await refreshDocuments()
  }

  async function selectPrimaryVersion(version: number) {
    if (!selectedPath.value || !selectedProject.value || version <= 0) return
    if (primaryVersion.value === version && primaryDocumentValue.value?.version === version && primaryStateValue.value.kind !== 'error') {
      return
    }
    primaryVersion.value = version
    await loadVersionDocument(selectedPath.value, selectedProject.value, version, primaryStateValue, primaryDocumentValue, 'documents-read-primary')
  }

  async function selectSecondaryVersion(version: number) {
    if (!selectedPath.value || !selectedProject.value || version <= 0) return
    if (secondaryVersion.value === version && secondaryDocumentValue.value?.version === version && secondaryStateValue.value.kind !== 'error') {
      return
    }
    secondaryVersion.value = version
    await loadVersionDocument(selectedPath.value, selectedProject.value, version, secondaryStateValue, secondaryDocumentValue, 'documents-read-secondary')
  }

  async function addComment(input: DocumentCommentInput) {
    const currentVersionEntry = history.value[0]
    if (!currentVersionEntry?.id) {
      throw new Error('No current document version selected for comments')
    }

    return runOperatorMutation<ApiDocumentCommentReceipt>({
      action: 'document-comment',
      evidence: endpointEvidence('/api/documents/comment', 'documents-comment'),
      snapshot: () => [...comments.value],
      run: () => operatorFetchJson<ApiDocumentCommentReceipt>('/api/documents/comment', jsonInit('POST', {
        document_id: Number(currentVersionEntry.id),
        author: input.author || 'operator-console',
        content: input.content,
        line_start: input.lineStart,
        line_end: input.lineEnd,
      }), 'documents-comment'),
      rollback: (snapshot) => {
        replaceArray(comments.value, snapshot || [])
      },
      refresh: () => refreshCommentsForVersion(currentVersionEntry.version),
    })
  }

  startOnce('documents-page', refresh)

  return {
    projects: projects.value,
    documents: documents.value,
    history: history.value,
    comments: comments.value,
    selectedProject,
    selectedPath,
    primaryVersion,
    secondaryVersion,
    activeDocument,
    primaryDocument: primaryDocumentValue,
    secondaryDocument: secondaryDocumentValue,
    documentsState,
    historyState,
    primaryState,
    secondaryState,
    commentsState,
    pending,
    error,
    refresh,
    openDocument,
    selectPrimaryVersion,
    selectSecondaryVersion,
    addComment,
  }
}
