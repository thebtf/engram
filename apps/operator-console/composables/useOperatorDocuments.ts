import { computed, onBeforeUnmount, ref as sharedRef, type Ref as SharedRef } from 'vue'
import { startedLoads } from './useConsolePreferences'
import { loadKnowledgePage, type OperatorKnowledgePage } from './useOperatorProjectContext'
import { useRoute } from 'vue-router'

let sharedDocumentsPageDocuments: SharedRef<OperatorDocumentSummary[]> | undefined
let sharedDocumentsPageHistory: SharedRef<OperatorDocumentVersion[]> | undefined
let sharedDocumentsPageComments: SharedRef<OperatorDocumentComment[]> | undefined
let sharedDocumentsPageSelectedProject: SharedRef<string> | undefined
let sharedDocumentsPageSelectedPath: SharedRef<string> | undefined
let sharedDocumentsPagePrimaryVersion: SharedRef<number | null> | undefined
let sharedDocumentsPageSecondaryVersion: SharedRef<number | null> | undefined
let sharedDocumentsPagePrimaryDocument: SharedRef<OperatorDocument | null> | undefined
let sharedDocumentsPageSecondaryDocument: SharedRef<OperatorDocument | null> | undefined
let sharedDocumentsPageDocumentsState: SharedRef<OperatorLoadState<OperatorDocumentSummary[]>> | undefined
let sharedDocumentsPageHistoryState: SharedRef<OperatorLoadState<OperatorDocumentVersion[]>> | undefined
let sharedDocumentsPagePrimaryState: SharedRef<OperatorLoadState<OperatorDocument | null>> | undefined
let sharedDocumentsPageSecondaryState: SharedRef<OperatorLoadState<OperatorDocument | null>> | undefined
let sharedDocumentsPageCommentsState: SharedRef<OperatorLoadState<OperatorDocumentComment[]>> | undefined

import type { ComputedRef, Ref } from 'vue'
import type { OperatorLoadState } from './useOperatorApi'
import { parseOperatorSelectionSnapshot, type OperatorSelection } from './useOperatorSelection'
import { executeMutation, type MutationResult } from './useApi'
import {
  emptyState,
  endpointEvidence,
  errorState,
  liveState,
  OperatorFetchError,
  operatorApiUrl,
  operatorFetchJson,
  pendingState,
  toOperatorSourceError,
} from './useOperatorApi'

const DEFAULT_DOCUMENT_PROJECT = ''
const DOCUMENT_LIST_LIMIT = 20



interface ApiDocumentListItem {
  id?: number | string
  path?: string
  project?: string
  doc_type?: string
  author?: string
  version?: number
  created_at?: string
}

type ApiDocumentListResponse = OperatorKnowledgePage<ApiDocumentListItem>

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

export type DocumentSelectionOperationAction = 'export'

export interface DocumentSelectionOperationTarget {
  documentId: number
  version: number
}

export interface DocumentSelectionOperationInput {
  action: DocumentSelectionOperationAction
  target: DocumentSelectionOperationTarget
}

export interface DocumentExportArtifact {
  downloadUrl: string
  filename: string
  contentType: string
  byteLength: number
}

export interface DocumentSelectionOperationResult {
  mutation: MutationResult<DocumentSelectionOperationInput>
  artifact?: DocumentExportArtifact
}

function documentOperationSelectionPayload(selection: Exclude<OperatorSelection, { kind: 'none' }>): Record<string, unknown> {
  if (selection.domain !== 'documents' || !Number.isSafeInteger(selection.version) || selection.version < 1 || (selection.kind === 'frozen_filter' && selection.reconfirmationRequired)) {
    throw new TypeError('the selected Documents snapshot is not server current')
  }

  switch (selection.kind) {
    case 'explicit':
    case 'page':
      return { kind: selection.kind, selection_version: selection.version }
    case 'frozen_filter':
      return { kind: selection.kind, selection_version: selection.version, selection_token: selection.selectionToken }
  }
}

function parseDocumentSelection(value: unknown): Exclude<OperatorSelection, { kind: 'none' }> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) throw new TypeError('Documents selection response is invalid')
  const selection = parseOperatorSelectionSnapshot(Reflect.get(value, 'selection'))
  if (selection.domain !== 'documents' || selection.kind === 'none') throw new TypeError('Documents selection response is invalid')
  return selection
}

function parseDocumentExportArtifact(value: unknown): DocumentExportArtifact | undefined {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return undefined
  const artifact = Reflect.get(value, 'artifact')
  if (artifact === null || typeof artifact !== 'object' || Array.isArray(artifact)) return undefined
  const downloadUrl = Reflect.get(artifact, 'download_url')
  const filename = Reflect.get(artifact, 'filename')
  const contentType = Reflect.get(artifact, 'content_type')
  const byteLength = Reflect.get(artifact, 'byte_length')
  if (typeof downloadUrl !== 'string' || !/^\/api\/documents\/exports\/[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/.test(downloadUrl) || typeof filename !== 'string' || !filename || contentType !== 'application/json' || !Number.isSafeInteger(byteLength) || byteLength < 1) return undefined
  return { downloadUrl, filename, contentType, byteLength }
}

function jsonInit(method: 'POST', body?: unknown): RequestInit {
  const init: RequestInit = { method }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  return init
}

function documentOperationInit(body: unknown, requestId: string): RequestInit {
  return {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Engram-Request-ID': requestId,
    },
    body: JSON.stringify(body),
  }
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


function mapDocumentSummary(row: ApiDocumentListItem): OperatorDocumentSummary {
  return {
    id: String(row.id ?? ''),
    path: row.path || '',
    project: row.project || DEFAULT_DOCUMENT_PROJECT,
    docType: row.doc_type || 'markdown',
    author: row.author || '',
    createdAt: row.created_at || '',
    age: compactAge(row.created_at),
    version: typeof row.version === 'number' ? row.version : 0,
  }
}

function mapDocumentVersion(row: ApiDocumentHistoryItem): OperatorDocumentVersion {
  return {
    id: String(row.id ?? ''),
    contentHash: row.content_hash || '',
    author: row.author || '',
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
    author: row.author || '',
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
    author: row.author || '',
    content: row.content || '',
    status: row.status || 'open',
    createdAt: row.created_at || '',
    age: compactAge(row.created_at),
    lineStart,
    lineEnd,
  }
}

function parseDocumentListPayload(payload: ApiDocumentListResponse): OperatorDocumentSummary[] {
  return payload.items.map(mapDocumentSummary)
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
  const started = startedLoads.has(key)
  if (typeof window !== 'undefined' && !started) {
    startedLoads.add(key)
    void run().catch((error) => {
      if (import.meta.env.DEV) {
        console.warn(`[useOperatorDocuments] ${key} live load failed`, error)
      }
    })
  }
}

export function useOperatorDocuments(projectContext?: Ref<string>): {
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
  addComment: (input: DocumentCommentInput) => Promise<MutationResult<DocumentCommentInput>>
  runDocumentSelectionOperation: (input: DocumentSelectionOperationInput) => Promise<DocumentSelectionOperationResult>
  clearDocumentDetail: () => void
  total: Ref<number | null>
  hasMore: Ref<boolean>
} {
  const listEvidence = endpointEvidence(`/api/documents?project={project}&limit=${DOCUMENT_LIST_LIMIT}`, 'documents-list')
  const historyEvidence = endpointEvidence('/api/documents/history?path={path}&project={project}', 'documents-history')
  const primaryEvidence = endpointEvidence('/api/documents/read?path={path}&project={project}&version={version}', 'documents-read-primary')
  const secondaryEvidence = endpointEvidence('/api/documents/read?path={path}&project={project}&version={version}', 'documents-read-secondary')
  const commentsEvidence = endpointEvidence('/api/documents/comments?document_id={id}', 'documents-comments')
  const route = useRoute()

  const documents = (sharedDocumentsPageDocuments ??= sharedRef<OperatorDocumentSummary[]>((() => [])()))
  const history = (sharedDocumentsPageHistory ??= sharedRef<OperatorDocumentVersion[]>((() => [])()))
  const comments = (sharedDocumentsPageComments ??= sharedRef<OperatorDocumentComment[]>((() => [])()))
  const selectedProject = projectContext || (sharedDocumentsPageSelectedProject ??= sharedRef<string>(''))
  const selectedPath = (sharedDocumentsPageSelectedPath ??= sharedRef<string>((() => '')()))
  const primaryVersion = (sharedDocumentsPagePrimaryVersion ??= sharedRef<number | null>((() => null)()))
  const secondaryVersion = (sharedDocumentsPageSecondaryVersion ??= sharedRef<number | null>((() => null)()))
  const primaryDocumentValue = (sharedDocumentsPagePrimaryDocument ??= sharedRef<OperatorDocument | null>((() => null)()))
  const secondaryDocumentValue = (sharedDocumentsPageSecondaryDocument ??= sharedRef<OperatorDocument | null>((() => null)()))

  const documentsStateValue = (sharedDocumentsPageDocumentsState ??= sharedRef<OperatorLoadState<OperatorDocumentSummary[]>>((() => pendingState(listEvidence, documents.value))()))
  const historyStateValue = (sharedDocumentsPageHistoryState ??= sharedRef<OperatorLoadState<OperatorDocumentVersion[]>>((() => emptyState(historyEvidence, history.value))()))
  const primaryStateValue = (sharedDocumentsPagePrimaryState ??= sharedRef<OperatorLoadState<OperatorDocument | null>>((() => emptyState(primaryEvidence, null))()))
  const secondaryStateValue = (sharedDocumentsPageSecondaryState ??= sharedRef<OperatorLoadState<OperatorDocument | null>>((() => emptyState(secondaryEvidence, null))()))
  const commentsStateValue = (sharedDocumentsPageCommentsState ??= sharedRef<OperatorLoadState<OperatorDocumentComment[]>>((() => emptyState(commentsEvidence, comments.value))()))

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

  let listGeneration = 0
  let detailGeneration = 0
  let primaryGeneration = 0
  let secondaryGeneration = 0
  let commentsGeneration = 0
  let documentProject = ''
  let listRequest: AbortController | undefined
  const total = sharedRef<number | null>(null)
  const hasMore = sharedRef(false)

  function clearDocumentDetail() {
    detailGeneration += 1
    primaryGeneration += 1
    secondaryGeneration += 1
    commentsGeneration += 1
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


  async function loadVersionDocument(
    path: string,
    project: string,
    version: number | null,
    stateRef: Ref<OperatorLoadState<OperatorDocument | null>>,
    valueRef: Ref<OperatorDocument | null>,
    source: 'documents-read-primary' | 'documents-read-secondary',
  ) {
    const generation = source === 'documents-read-primary' ? ++primaryGeneration : ++secondaryGeneration
    const detail = detailGeneration
    const isCurrent = () => detail === detailGeneration && project === documentProject && path === selectedPath.value
      && generation === (source === 'documents-read-primary' ? primaryGeneration : secondaryGeneration)
    const endpoint = version === null
      ? `/api/documents/read?path=${encodeURIComponent(path)}&project=${encodeURIComponent(project)}`
      : `/api/documents/read?path=${encodeURIComponent(path)}&project=${encodeURIComponent(project)}&version=${version}`
    const evidence = endpointEvidence(endpoint, source)
    if (version === null && source === 'documents-read-secondary') {
      valueRef.value = null
      stateRef.value = emptyState(evidence, null)
      return
    }

    valueRef.value = null
    stateRef.value = pendingState(evidence, null)
    try {
      const payload = await operatorFetchJson<ApiDocumentReadResponse>(endpoint, undefined, source)
      if (!isCurrent()) return
      if (payload.path !== path || (version !== null && payload.version !== version)) throw new TypeError('Document read scope mismatch')
      valueRef.value = { ...parseDocumentReadPayload(payload, endpoint), project }
      stateRef.value = liveState(evidence, valueRef.value)
    } catch (nextError) {
      if (!isCurrent()) return
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
    const generation = ++commentsGeneration
    const detail = detailGeneration
    const versionEntry = history.value.find((row) => row.version === version)
    if (!versionEntry?.id) {
      replaceArray(comments.value, [])
      commentsStateValue.value = emptyState(commentsEvidence, comments.value)
      return
    }

    const endpoint = `/api/documents/comments?document_id=${encodeURIComponent(versionEntry.id)}`
    const evidence = endpointEvidence(endpoint, 'documents-comments')
    replaceArray(comments.value, [])
    commentsStateValue.value = pendingState(evidence, [])
    try {
      const payload = await operatorFetchJson<ApiDocumentCommentsResponse>(endpoint, undefined, 'documents-comments')
      if (generation !== commentsGeneration || detail !== detailGeneration) return
      const rows = parseDocumentCommentsPayload(payload, endpoint)
      replaceArray(comments.value, rows)
      commentsStateValue.value = rows.length ? liveState(evidence, comments.value) : emptyState(evidence, comments.value)
    } catch (nextError) {
      if (generation !== commentsGeneration || detail !== detailGeneration) return
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

  async function refreshHistory(path = selectedPath.value, project = documentProject) {
    const generation = ++detailGeneration
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
      if (generation !== detailGeneration || project !== documentProject || path !== selectedPath.value) return
      if (payload.path !== path) throw new TypeError('Document history path mismatch')
      const rows = parseDocumentHistoryPayload(payload, endpoint).sort((a, b) => b.version - a.version)
      replaceArray(history.value, rows)
      historyStateValue.value = rows.length ? liveState(evidence, history.value) : emptyState(evidence, history.value)

      if (!rows.length) return
      const nextPrimary = rows[0].version
      primaryVersion.value = nextPrimary

      await Promise.all([
        primaryDocumentValue.value?.version === nextPrimary ? Promise.resolve() : loadVersionDocument(path, project, nextPrimary, primaryStateValue, primaryDocumentValue, 'documents-read-primary'),
        refreshCommentsForVersion(nextPrimary),
      ])
    } catch (nextError) {
      if (generation !== detailGeneration || project !== documentProject || path !== selectedPath.value) return
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
    clearDocumentDetail()
    const project = selectedProject.value === 'all' ? String(route.query.docProject || '') : selectedProject.value
    if (!project || project === 'all') return
    documentProject = project
    selectedPath.value = doc.path
    const generation = detailGeneration
    await loadVersionDocument(doc.path, project, null, primaryStateValue, primaryDocumentValue, 'documents-read-primary')
    if (generation !== detailGeneration || project !== documentProject || doc.path !== selectedPath.value) return
    primaryVersion.value = primaryDocumentValue.value?.version || null
    await refreshHistory(doc.path, project)
  }

  async function refreshDocuments() {
    const generation = ++listGeneration
    listRequest?.abort()
    const controller = new AbortController()
    listRequest = controller
    const project = selectedProject.value
    replaceArray(documents.value, [])
    total.value = null
    hasMore.value = false
    if (project === 'all' && route.query.allRead !== '1') {
      documentsStateValue.value = { kind: 'gated', evidence: listEvidence, flag: 'explicit-all-read', reason: 'Choose a project or submit an explicit all-projects search.', updatedAt: new Date().toISOString(), data: [] }
      return
    }
    const page = Math.max(1, Number.parseInt(String(route.query.page || '1'), 10) || 1)
    const limit = [10, 20, 50].includes(Number(route.query.size)) ? Number(route.query.size) : DOCUMENT_LIST_LIMIT
    const params = new URLSearchParams({ offset: String((page - 1) * limit), limit: String(limit) })
    const prefix = typeof route.query.pathPrefix === 'string' ? route.query.pathPrefix : typeof route.query.path_prefix === 'string' ? route.query.path_prefix : ''
    if (prefix) params.set('path_prefix', prefix)
    if (typeof route.query.q === 'string' && route.query.q.trim()) params.set('query', route.query.q.trim())
    if (typeof route.query.docType === 'string' && route.query.docType) params.set('doc_type', route.query.docType)
    const endpoint = `/api/documents?${params}`
    const evidence = endpointEvidence(endpoint, 'documents-list')
    documentsStateValue.value = pendingState(evidence, [])
    try {
      const payload = await loadKnowledgePage<ApiDocumentListItem>('/api/documents', project, params, controller.signal)
      if (generation !== listGeneration || project !== selectedProject.value) return
      const rows = parseDocumentListPayload(payload)
      if (project !== 'all' && rows.some((row) => row.project !== project)) throw new TypeError('Document page scope mismatch')
      replaceArray(documents.value, rows)
      total.value = payload.total
      hasMore.value = payload.has_more
      documentsStateValue.value = rows.length ? liveState(evidence, documents.value) : emptyState(evidence, documents.value)
    } catch (nextError) {
      if (generation !== listGeneration || project !== selectedProject.value || controller.signal.aborted) return
      const mapped = toOperatorSourceError(nextError, { source: 'documents-list', path: endpoint, method: 'GET' })
      if (nextError instanceof TypeError) mapped.category = 'invalid-response'
      documentsStateValue.value = errorState(evidence, mapped, { source: 'documents-list', run: async () => { await refreshDocuments(); return documentsStateValue.value } }, [])
    }
  }

  async function refresh() {
    await refreshDocuments()
  }

  async function selectPrimaryVersion(version: number) {
    if (!Number.isSafeInteger(version) || !selectedPath.value || !documentProject || version <= 0) return
    if (primaryVersion.value === version && primaryDocumentValue.value?.version === version && primaryStateValue.value.kind !== 'error') {
      await refreshCommentsForVersion(version)
      return
    }
    const detail = detailGeneration
    primaryVersion.value = version
    await loadVersionDocument(selectedPath.value, documentProject, version, primaryStateValue, primaryDocumentValue, 'documents-read-primary')
    if (detail === detailGeneration && primaryVersion.value === version) await refreshCommentsForVersion(version)
  }

  async function selectSecondaryVersion(version: number) {
    if (!selectedPath.value || !documentProject || version <= 0) return
    if (secondaryVersion.value === version && secondaryDocumentValue.value?.version === version && secondaryStateValue.value.kind !== 'error') {
      return
    }
    secondaryVersion.value = version
    await loadVersionDocument(selectedPath.value, documentProject, version, secondaryStateValue, secondaryDocumentValue, 'documents-read-secondary')
  }

  async function addComment(input: DocumentCommentInput) {
    const currentVersionEntry = history.value.find((entry) => entry.version === primaryVersion.value)
    if (!currentVersionEntry?.id) {
      throw new Error('No document version selected for comments')
    }

    return executeMutation(
      { requestId: crypto.randomUUID(), action: 'document-comment', intent: input },
      fetch(operatorApiUrl('/api/documents/comment'), {
        ...jsonInit('POST', {
          document_id: Number(currentVersionEntry.id),
          author: input.author || 'operator-console',
          content: input.content,
          line_start: input.lineStart,
          line_end: input.lineEnd,
        }),
        credentials: 'include',
      }),
      () => undefined,
    )
  }

  async function runDocumentSelectionOperation(input: DocumentSelectionOperationInput): Promise<DocumentSelectionOperationResult> {
    if (!Number.isSafeInteger(input.target.documentId) || input.target.documentId < 1 || !Number.isSafeInteger(input.target.version) || input.target.version < 1) {
      throw new TypeError('Documents export target is invalid')
    }
    const selectionRequestId = crypto.randomUUID()
    const selection = parseDocumentSelection(await operatorFetchJson<unknown>('/api/documents/selection', documentOperationInit({
      domain: 'documents',
      selection: { kind: 'explicit', targets: [{ id: String(input.target.documentId), expected_version: input.target.version }] },
    }, selectionRequestId), 'documents-selection'))
    const requestId = crypto.randomUUID()
    const request = { requestId, action: `document-${input.action}`, intent: input }
    const body = { request_id: requestId, action: input.action, selection: documentOperationSelectionPayload(selection) }
    let response: Response
    try {
      response = await fetch(operatorApiUrl('/api/documents'), { ...documentOperationInit(body, requestId), credentials: 'include' })
    } catch (error) {
      return { mutation: await executeMutation(request, Promise.reject(error), () => undefined) }
    }
    const artifact = response.ok
      ? await response.clone().json().then(parseDocumentExportArtifact).catch(() => undefined)
      : undefined
    return { mutation: await executeMutation(request, Promise.resolve(response), () => undefined), ...(artifact === undefined ? {} : { artifact }) }
  }

  if (!projectContext) startOnce('documents-page', refresh)
  onBeforeUnmount(() => { listGeneration += 1; listRequest?.abort(); clearDocumentDetail() })

  return {
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
    runDocumentSelectionOperation,
    clearDocumentDetail,
    total,
    hasMore,
  }
}
