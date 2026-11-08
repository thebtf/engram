import { parseProjectCatalog, parseProjectResolution, parseRetainedPage, parseReaderPage, credentialBinding, assertCredentialMetadata, UnresolvedProjectError, type ProjectCatalog, type RetainedKind, type RetainedPage, type ReaderPage, type MemoryReaderQuery } from '../../../../ui/src/utils/projectCatalog'

export interface NullableStringValue {
  String: string
  Valid: boolean
}

export interface SDKSession {
  id: number
  claude_session_id: string
  project: string
  status: string
  started_at: string
  started_at_epoch: number
  prompt_counter: number
  user_prompt: NullableStringValue
}

export interface SessionListResponse {
  sessions: SDKSession[]
  total: number
  limit: number
  offset: number
}

export interface Rule {
  id: number
  project?: string | null
  content: string
  edited_by?: string
  priority: number
  version: number
  created_at: string
  updated_at: string
}

export interface RuleListResponse {
  rules: Rule[]
  total: number
}

export type IssueRevision = string
export type IssueJsonValue = null | boolean | string | number | readonly IssueJsonValue[] | IssueMetadata
export interface IssueMetadata {
  readonly [key: string]: IssueJsonValue
}

export interface IssueActor {
  kind: 'client' | 'operator' | 'system' | 'unknown'
  reference: string | null
}

export interface IssueCurrentActor {
  kind: 'client' | 'operator'
  reference: string
}

export interface IssueExpectedActor {
  readonly kind: 'client' | 'operator' | 'unknown'
  readonly reference: string | null
}

export interface Issue {
  id: number
  revision: IssueRevision
  title: string
  body?: string
  status: 'open' | 'acknowledged' | 'resolved' | 'reopened' | 'closed' | 'rejected'
  priority: 'critical' | 'high' | 'medium' | 'low'
  type: 'bug' | 'feature' | 'improvement' | 'task'
  source_project: string
  target_project: string
  source_agent: string
  source_project_id?: string | null
  target_project_id?: string | null
  project_origin?: { source_project: string; target_project: string }
  created_by_session?: string | null
  target_session?: string | null
  owner_label?: string | null
  recipient_label?: string | null
  metadata?: IssueMetadata
  actor?: IssueActor | null
  labels: string[]
  comment_count?: number
  created_at: string
  updated_at: string
}

export interface IssueComment {
  id: number
  issue_id: number
  author_project: string
  author_agent: string
  body: string
  metadata: IssueMetadata
  actor?: IssueActor | null
  created_at: string
}

export interface IssueHistory {
  revision: IssueRevision
  previous_revision: IssueRevision | null
  action: string
  changed_fields: string[]
  actor: IssueActor
  attribution: {
    source_project: string | null
    source_agent: string | null
    session: string | null
  }
  created_at: string
  changes: IssueMetadata
}

export interface IssueListOptions {
  project?: string
  status?: string
  limit?: number
  offset?: number
  type?: string
  sourceProject?: string
  sourceAgent?: string
  q?: string
  ownerLabel?: string
  recipientLabel?: string
  sourceSession?: string
  participantSession?: string
  targetSession?: string
}

export interface IssueListResponse {
  issues: Issue[]
  total: number
  limit: number
  offset: number
  next_offset: number | null
  project_names?: Record<string, string>
}

export interface IssueDetailPageOptions {
  comments_offset?: number
  comments_limit?: number
  history_offset?: number
  history_limit?: number
}

export interface IssueDetailResponse {
  issue: Issue & { body: string; metadata: IssueMetadata }
  comments: IssueComment[]
  history: IssueHistory[]
  comment_count: number
  history_count: number
  comments_offset: number
  comments_limit: number
  history_offset: number
  history_limit: number
  comments_next_offset: number | null
  history_next_offset: number | null
  comments_complete: boolean
  history_complete: boolean
  comments_oversized: boolean
  history_oversized: boolean
  source_project_display_name?: string
  target_project_display_name?: string
}

export interface CreateIssueInput {
  readonly title: string
  readonly body?: string
  readonly type?: Issue['type']
  readonly priority?: Issue['priority']
  readonly target_project: string
  readonly source_project: string
  readonly source_agent?: string
  readonly created_by_session?: string
  readonly target_session?: string
  readonly labels?: readonly string[]
  readonly metadata?: IssueMetadata
  readonly metadataRaw?: string
  readonly owner_label?: string | null
  readonly recipient_label?: string | null
  readonly request_key?: string
  readonly expected_actor?: IssueExpectedActor
}

export type IssueCreateIntent = Readonly<CreateIssueInput> & { readonly request_key: string }

export interface IssueCreateResponse {
  id: number
  revision: IssueRevision
  replayed: boolean
  message: string
}

export interface IssueUpdateResponse {
  id: number
  revision: IssueRevision
  changed: boolean
  comment_id: number | null
  message: string
}

const issueErrorMessages: Record<string, string> = {
  INVALID_ARGUMENT: 'Invalid issue field, metadata, number or paging argument. Correct the input before submitting.',
  PAYLOAD_TOO_LARGE: 'The issue request exceeds an admitted limit. Nothing was truncated.',
  UNAUTHENTICATED: 'Authenticate before reading or acting on an issue.',
  FORBIDDEN: 'The current actor is not allowed to perform this issue action.',
  ISSUE_NOT_FOUND: 'The issue is missing or unavailable to the current actor.',
  ACTOR_CHANGED: 'The saved actor expectation did not match. Preserve the original actor, key, source and payload and reconcile explicitly before any new dispatch.',
  IDEMPOTENCY_CONFLICT: 'Reconcile the original create intent with its original actor, source, key and payload. Do not create a replacement.',
  REVISION_CONFLICT: 'Keep the original base and draft. Read the current issue and explicitly reconcile before submitting again.',
  INVALID_TRANSITION: 'Read the current issue and choose a permitted transition.',
  REVISION_REQUIRED: 'Keep the draft and explicitly reconcile it with a read revision before submitting a material change.',
  ISSUE_GONE: 'The original issue was deleted. Its retained intent must not create a replacement.',
  DETAIL_PAGING_REQUIRED: 'Read detail_mode=paged and follow each collection continuation explicitly.',
  PRIVACY_REJECTED: 'The issue write was rejected by the applicable privacy policy. No payload was disclosed or silently redacted.',
  TRANSPORT_RECORD_TOO_LARGE: 'Read the intact record through authorized REST detail paging.',
  UNAVAILABLE: 'Outcome unknown. Retain the original intent or base and draft; reconcile explicitly after recovery.',
  UNKNOWN_OUTCOME: 'Outcome unknown. Retain the original intent or base and draft; reconcile explicitly before any new write.',
  INVALID_RESPONSE: 'The issue response could not be confirmed. Retain the original intent or base and draft; reconcile explicitly.',
}

export class IssueApiError extends Error {
  readonly code: string
  readonly status?: number
  readonly current_revision?: IssueRevision
  readonly outcome: 'rejected' | 'unknown'

  constructor(code: string, outcome: 'rejected' | 'unknown' = 'rejected', status?: number, currentRevision?: IssueRevision) {
    const message = Object.hasOwn(issueErrorMessages, code)
      ? issueErrorMessages[code]
      : code.startsWith('PROJECT_')
        ? 'Restore the existing canonical project identity before acting. Do not substitute a source or target.'
        : 'The issue request failed. Retain the original intent or base and draft and reconcile explicitly.'
    super(`${code}: ${message}`)
    this.name = 'IssueApiError'
    this.code = code
    this.outcome = outcome
    this.status = status
    this.current_revision = currentRevision
  }
}

function isIssueRevision(value: unknown): value is IssueRevision {
  return typeof value === 'string' && /^[1-9][0-9]{0,18}$/.test(value)
    && (value.length < 19 || value <= '9223372036854775807')
}

export function assertIssueRevision(value: unknown): asserts value is IssueRevision {
  if (!isIssueRevision(value)) throw new IssueApiError('INVALID_ARGUMENT')
}

export function assertIssueRequestKey(value: unknown): asserts value is string {
  if (typeof value !== 'string' || value.length === 0 || value.includes('\0')) throw new IssueApiError('INVALID_ARGUMENT')
  for (let i = 0; i < value.length; i++) {
    const unit = value.charCodeAt(i)
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const next = value.charCodeAt(++i)
      if (!(next >= 0xdc00 && next <= 0xdfff)) throw new IssueApiError('INVALID_ARGUMENT')
    } else if (unit >= 0xdc00 && unit <= 0xdfff) {
      throw new IssueApiError('INVALID_ARGUMENT')
    }
  }
  if (new TextEncoder().encode(value).byteLength > 512) throw new IssueApiError('PAYLOAD_TOO_LARGE')
}

function copyIssueJson(value: unknown, depth: number, ancestors: Set<object>): IssueJsonValue {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return value
  if (typeof value === 'number') {
    // Parsed JS cannot recover rounded raw tokens or duplicate JSON members; the server checks those.
    if (!Number.isFinite(value) || (Number.isInteger(value) && !Number.isSafeInteger(value))) throw new IssueApiError('INVALID_ARGUMENT')
    return value
  }
  if (typeof value !== 'object') throw new IssueApiError('INVALID_ARGUMENT')
  if (depth > 256) throw new IssueApiError('PAYLOAD_TOO_LARGE')
  if (ancestors.has(value) || Object.getOwnPropertySymbols(value).length) throw new IssueApiError('INVALID_ARGUMENT')
  ancestors.add(value)
  try {
    if (Array.isArray(value)) {
      const result: IssueJsonValue[] = []
      if (Object.keys(value).length !== value.length) throw new IssueApiError('INVALID_ARGUMENT')
      for (let index = 0; index < value.length; index++) {
        const descriptor = Object.getOwnPropertyDescriptor(value, String(index))
        if (!descriptor || !('value' in descriptor)) throw new IssueApiError('INVALID_ARGUMENT')
        result.push(copyIssueJson(descriptor.value, depth + 1, ancestors))
      }
      return Object.freeze(result)
    }
    const prototype = Object.getPrototypeOf(value)
    if (prototype !== Object.prototype && prototype !== null) throw new IssueApiError('INVALID_ARGUMENT')
    const entries: [string, IssueJsonValue][] = []
    for (const [key, descriptor] of Object.entries(Object.getOwnPropertyDescriptors(value))) {
      if (!descriptor.enumerable || !('value' in descriptor)) throw new IssueApiError('INVALID_ARGUMENT')
      entries.push([key, copyIssueJson(descriptor.value, depth + 1, ancestors)])
    }
    return Object.freeze(Object.fromEntries(entries))
  } finally {
    ancestors.delete(value)
  }
}

export function prepareIssueMetadata(value: unknown): IssueMetadata {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) throw new IssueApiError('INVALID_ARGUMENT')
  const metadata = copyIssueJson(value, 1, new Set()) as IssueMetadata
  if (new TextEncoder().encode(JSON.stringify(metadata)).byteLength > 1024 * 1024) throw new IssueApiError('PAYLOAD_TOO_LARGE')
  return metadata
}

export function prepareIssueMetadataRaw(value: unknown): string {
  if (typeof value !== 'string') throw new IssueApiError('INVALID_ARGUMENT')
  let parsed: unknown
  try {
    parsed = JSON.parse(value)
  } catch {
    throw new IssueApiError('INVALID_ARGUMENT')
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) throw new IssueApiError('INVALID_ARGUMENT')
  return value
}

function issueErrorRecord(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object' ? value as Record<string, unknown> : {}
}

export function toIssueApiError(error: unknown): IssueApiError {
  if (error instanceof IssueApiError) return error
  const failure = issueErrorRecord(error)
  const response = issueErrorRecord(failure.response)
  const data = issueErrorRecord(failure.data ?? response._data)
  const detail = issueErrorRecord(data.error)
  const rawStatus = failure.statusCode ?? failure.status ?? response.status
  const status = typeof rawStatus === 'number' && Number.isInteger(rawStatus) && rawStatus >= 400 && rawStatus <= 599 ? rawStatus : undefined
  const suppliedCode = typeof detail.code === 'string' ? detail.code : ''
  const code = Object.hasOwn(issueErrorMessages, suppliedCode) || (suppliedCode.length <= 96 && /^PROJECT_[A-Z0-9_]+$/.test(suppliedCode))
    ? suppliedCode : status !== undefined ? `HTTP_${status}` : 'UNKNOWN_OUTCOME'
  const currentRevision = status === 409 && code === 'REVISION_CONFLICT' && isIssueRevision(detail.current_revision) ? detail.current_revision : undefined
  return new IssueApiError(code, status !== undefined && status < 500 ? 'rejected' : 'unknown', status, currentRevision)
}

function issueRequestBody(body: object | undefined): Record<string, unknown> | string | undefined {
  if (body === undefined) return undefined
  const { metadataRaw, commentMetadataRaw, ...wire } = issueErrorRecord(body)
  if ((metadataRaw !== undefined && wire.metadata !== undefined)
    || (commentMetadataRaw !== undefined && wire.comment_metadata !== undefined)) throw new IssueApiError('INVALID_ARGUMENT')
  if (metadataRaw === undefined && commentMetadataRaw === undefined) return wire
  const metadata = metadataRaw === undefined ? undefined : prepareIssueMetadataRaw(metadataRaw)
  const commentMetadata = commentMetadataRaw === undefined ? undefined : prepareIssueMetadataRaw(commentMetadataRaw)
  const members: string[] = []
  try {
    for (const [name, value] of Object.entries(wire)) {
      const encoded = JSON.stringify(value, (_key, item: unknown) => {
        if (typeof item === 'number' && (!Number.isFinite(item) || (Number.isInteger(item) && !Number.isSafeInteger(item)))) throw new IssueApiError('INVALID_ARGUMENT')
        return item
      })
      if (encoded !== undefined) members.push(`${JSON.stringify(name)}:${encoded}`)
    }
  } catch {
    throw new IssueApiError('INVALID_ARGUMENT')
  }
  // Whole-object validation above prevents escaping this field; retain raw tokens for server admission.
  if (metadata !== undefined) members.push(`"metadata":${metadata}`)
  if (commentMetadata !== undefined) members.push(`"comment_metadata":${commentMetadata}`)
  return `{${members.join(',')}}`
}

export async function issueApiRequest<T>(path: string, method: 'GET' | 'POST' | 'PATCH' | 'DELETE', body?: object): Promise<T> {
  const wireBody = issueRequestBody(body)
  try {
    return await $fetch<T>(`${apiBase()}${path}`, {
      method,
      body: wireBody,
      ...(wireBody === undefined ? {} : { headers: { 'Content-Type': 'application/json' } }),
      credentials: 'include',
      retry: 0,
    })
  } catch (error) {
    throw toIssueApiError(error)
  }
}

export async function fetchIssueActor(): Promise<IssueCurrentActor | null> {
  try {
    const response = await apiGet<unknown>('/auth/me')
    if (response === null || typeof response !== 'object' || Array.isArray(response)) throw new IssueApiError('INVALID_RESPONSE', 'unknown')
    const actor = issueErrorRecord(response).actor
    if (actor === undefined || actor === null) return null
    if (typeof actor !== 'object' || Array.isArray(actor)) throw new IssueApiError('INVALID_RESPONSE', 'unknown')
    const descriptor = issueErrorRecord(actor)
    if ((descriptor.kind !== 'client' && descriptor.kind !== 'operator' && descriptor.kind !== 'unknown')
      || (descriptor.reference !== null && typeof descriptor.reference !== 'string')) {
      throw new IssueApiError('INVALID_RESPONSE', 'unknown')
    }
    if (descriptor.kind === 'unknown' || descriptor.reference === null || descriptor.reference.trim() === '') return null
    return Object.freeze({ kind: descriptor.kind, reference: descriptor.reference })
  } catch (error) {
    throw toIssueApiError(error)
  }
}

function issuePageBound(value: number, limit: boolean): number {
  if (!Number.isSafeInteger(value) || value < (limit ? 1 : 0) || (limit && value > 200)) throw new IssueApiError('INVALID_ARGUMENT')
  return value
}

export function normalizeIssueDetailPage(options: IssueDetailPageOptions = {}): Required<IssueDetailPageOptions> {
  return {
    comments_offset: issuePageBound(options.comments_offset === undefined ? 0 : options.comments_offset, false),
    comments_limit: issuePageBound(options.comments_limit === undefined ? 50 : options.comments_limit, true),
    history_offset: issuePageBound(options.history_offset === undefined ? 0 : options.history_offset, false),
    history_limit: issuePageBound(options.history_limit === undefined ? 50 : options.history_limit, true),
  }
}

function assertIssueCollectionPage(items: unknown[], total: number, offset: number, limit: number, next: number | null, complete: boolean, oversized: boolean): void {
  const end = offset + items.length
  if (!Number.isSafeInteger(total) || total < 0 || !Number.isSafeInteger(end) || items.length > limit
    || items.length > Math.max(0, total - offset) || typeof complete !== 'boolean' || typeof oversized !== 'boolean'
    || (oversized && items.length !== 1) || complete !== (next === null)
    || (next !== null && (next !== end || items.length === 0 || end >= total))
    || (next === null && end < total)) throw new IssueApiError('INVALID_RESPONSE', 'unknown')
}

function copyIssueExpectedActor(value: unknown): IssueExpectedActor {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) throw new IssueApiError('INVALID_ARGUMENT')
  const { kind, reference } = issueErrorRecord(value)
  if ((kind !== 'client' && kind !== 'operator' && kind !== 'unknown') || (reference !== null && typeof reference !== 'string')) {
    throw new IssueApiError('INVALID_ARGUMENT')
  }
  return Object.freeze({ kind, reference })
}

function copyIssueCreateInput(data: CreateIssueInput): CreateIssueInput {
  for (const name of ['title', 'target_project', 'source_project'] as const) {
    if (typeof data[name] !== 'string') throw new IssueApiError('INVALID_ARGUMENT')
  }
  for (const name of ['body', 'type', 'priority', 'source_agent', 'created_by_session', 'target_session'] as const) {
    if (data[name] !== undefined && typeof data[name] !== 'string') throw new IssueApiError('INVALID_ARGUMENT')
  }
  for (const name of ['owner_label', 'recipient_label'] as const) {
    if (data[name] !== undefined && data[name] !== null && (typeof data[name] !== 'string' || data[name] === '')) throw new IssueApiError('INVALID_ARGUMENT')
  }
  if (data.labels !== undefined && (!Array.isArray(data.labels) || data.labels.some(label => typeof label !== 'string'))) throw new IssueApiError('INVALID_ARGUMENT')
  if (data.request_key !== undefined) assertIssueRequestKey(data.request_key)
  if (data.metadataRaw !== undefined && data.metadata !== undefined) throw new IssueApiError('INVALID_ARGUMENT')
  const snapshot = { ...data }
  if (data.labels !== undefined) snapshot.labels = Object.freeze([...data.labels])
  if (data.metadata !== undefined) snapshot.metadata = prepareIssueMetadata(data.metadata)
  if (data.metadataRaw !== undefined) snapshot.metadataRaw = prepareIssueMetadataRaw(data.metadataRaw)
  if (data.expected_actor !== undefined) snapshot.expected_actor = copyIssueExpectedActor(data.expected_actor)
  return Object.freeze(snapshot)
}

async function prepareIssueCreateInput(data: CreateIssueInput): Promise<CreateIssueInput> {
  const snapshot = copyIssueCreateInput(data)
  const target_project = await resolveProject(snapshot.target_project)
  const source_project = await resolveProject(snapshot.source_project)
  return Object.freeze({ ...snapshot, target_project, source_project, source_agent: snapshot.source_agent === undefined ? 'human' : snapshot.source_agent })
}

export async function prepareIssueCreateIntent(data: CreateIssueInput & { readonly request_key: string }): Promise<IssueCreateIntent> {
  const request_key = data.request_key
  assertIssueRequestKey(request_key)
  const snapshot = await prepareIssueCreateInput(data)
  return Object.freeze({ ...snapshot, request_key })
}

export interface VaultCredential {
  id: number
  name: string
  scope: string
  project?: string
  project_id?: string | null
  unresolved_reference?: string
  binding_error?: string
  origin_project?: string
  created_at: string
  updated_at?: string
}

export interface VaultStatus {
  encrypted: boolean
  key_fingerprint?: string
  credential_count: number
}

export interface RetrievalStats {
  total_requests: number
  observations_served: number
  search_requests: number
  context_injections: number
  stale_excluded?: number
  fresh_count?: number
  duplicates_removed?: number
}

export interface Stats {
  uptime: string
  activeSessions: number
  queueDepth: number
  isProcessing: boolean
  connectedClients: number
  sessionsToday: number
  retrieval: RetrievalStats
}

export interface ComponentHealth {
  name: string
  status: 'healthy' | 'degraded' | 'unhealthy'
  message?: string
}

export interface SelfCheckResponse {
  overall: 'healthy' | 'degraded' | 'unhealthy'
  version: string
  uptime: string
  components: ComponentHealth[]
}

export interface MaintenanceStats {
  last_consolidation?: string
  last_maintenance?: string
  observations_consolidated?: number
  stale_removed?: number
}

export interface UpdateInfo {
  available: boolean
  current_version: string
  latest_version: string
  release_notes?: string
  published_at?: string
}

export interface Memory {
  id: number
  project: string
  content: string
  tags: string[]
  source_agent?: string
  edited_by?: string
  status?: string
  tier?: string
  epistemic_type?: string
  privacy_scope?: string
  confidence?: number
  citation_count?: number
  injection_count?: number
  access_count?: number
  created_at: string
  updated_at: string
}

function apiBase(): string {
  return useRuntimeConfig().public.apiBase
}

async function apiGet<T>(path: string): Promise<T> {
  return $fetch<T>(`${apiBase()}${path}`, {
    credentials: 'include',
  })
}

async function apiPost<T>(path: string, body: unknown): Promise<T> {
  return $fetch<T>(`${apiBase()}${path}`, {
    method: 'POST',
    body,
    credentials: 'include',
  })
}

async function apiDelete<T>(path: string): Promise<T> {
  return $fetch<T>(`${apiBase()}${path}`, {
    method: 'DELETE',
    credentials: 'include',
  })
}

let projectCatalog: ProjectCatalog | null = null

export async function fetchProjects(): Promise<ProjectCatalog> {
  const catalog = parseProjectCatalog(await apiGet<unknown>('/projects'))
  projectCatalog = catalog
  return catalog
}

export async function resolveProject(project: string): Promise<string> {
  if (!project || project === 'all' || project === 'global') throw new Error('A concrete project is required')
  const catalog = projectCatalog ?? await fetchProjects()
  if (catalog.items.some(item => item.project_id === project)) return project
  const result = parseProjectResolution(await apiGet<unknown>(`/projects/resolve?${new URLSearchParams({ legacy_id: project })}`))
  if (result.status === 'unresolved') throw new UnresolvedProjectError(result.reference, result.reason)
  const current = catalog.items.some(item => item.project_id === result.project_id) ? catalog : await fetchProjects()
  if (!current.items.some(item => item.project_id === result.project_id)) throw new Error('Resolved project is absent from the catalog')
  return result.project_id
}

export async function fetchRetainedContent(reference: string, kind: RetainedKind, id?: string | number, cursor?: string, q?: string): Promise<RetainedPage> {
  const params = new URLSearchParams({ limit: '50' })
  if (kind === 'code' && id !== undefined) throw new Error('Retained code has no content history reader')
  if (kind !== 'code') params.set('kind', kind)
  if (id !== undefined) params.set('id', String(id))
  if (cursor) params.set('cursor', cursor)
  if (q && id === undefined && kind !== 'code') params.set('q', q)
  const data = await apiGet<unknown>(`/projects/unresolved/${encodeURIComponent(reference)}/${kind === 'code' ? 'code' : id === undefined ? 'content' : 'history'}?${params}`)
  return parseRetainedPage(data, reference, kind, id)
}

export async function fetchStats(project?: string | null): Promise<Stats> {
  const params = new URLSearchParams()
  if (project) params.append('project', await resolveProject(project))
  const query = params.toString()
  return apiGet<Stats>(`/stats${query ? `?${query}` : ''}`)
}

export async function fetchSessions(params: {
  project?: string
  limit?: number
  offset?: number
} = {}): Promise<SessionListResponse> {
  const searchParams = new URLSearchParams()
  if (params.project) searchParams.set('project', await resolveProject(params.project))
  if (params.limit !== undefined) searchParams.set('limit', String(params.limit))
  if (params.offset !== undefined) searchParams.set('offset', String(params.offset))
  const query = searchParams.toString()
  return apiGet<SessionListResponse>(`/sessions/list${query ? `?${query}` : ''}`)
}

export async function fetchRules(params: {
  project?: string
  limit?: number
} = {}): Promise<RuleListResponse> {
  const searchParams = new URLSearchParams()
  if (params.project) searchParams.set('project', await resolveProject(params.project))
  if (params.limit !== undefined) searchParams.set('limit', String(params.limit))
  const query = searchParams.toString()
  return apiGet<RuleListResponse>(`/rules${query ? `?${query}` : ''}`)
}

export async function fetchIssues(params: IssueListOptions = {}): Promise<IssueListResponse> {
  const limit = issuePageBound(params.limit === undefined ? 50 : params.limit, true)
  const offset = issuePageBound(params.offset === undefined ? 0 : params.offset, false)
  const searchParams = new URLSearchParams({ limit: String(limit), offset: String(offset) })
  if (params.project) searchParams.set('project', await resolveProject(params.project))
  if (params.sourceProject) searchParams.set('source_project', await resolveProject(params.sourceProject))
  if (params.sourceAgent) searchParams.set('source_agent', params.sourceAgent)
  if (params.status) searchParams.set('status', params.status)
  if (params.type) searchParams.set('type', params.type)
  if (params.q !== undefined) searchParams.set('q', params.q)
  if (params.ownerLabel !== undefined) searchParams.set('owner_label', params.ownerLabel)
  if (params.recipientLabel !== undefined) searchParams.set('recipient_label', params.recipientLabel)
  if (params.sourceSession !== undefined) searchParams.set('source_session', params.sourceSession)
  if (params.participantSession !== undefined) searchParams.set('participant_session', params.participantSession)
  if (params.targetSession !== undefined) searchParams.set('target_session', params.targetSession)
  const response = await issueApiRequest<IssueListResponse>(`/issues?${searchParams}`, 'GET')
  if (!Array.isArray(response?.issues) || response.issues.some(issue => !isIssueRevision(issue?.revision))
    || !Number.isSafeInteger(response.total) || response.total < 0
    || response.limit !== limit || response.offset !== offset
    || response.issues.length > limit || !Number.isSafeInteger(offset + response.issues.length)
    || (response.issues.length > 0 && offset + response.issues.length > response.total)
    || (response.next_offset !== null && (!Number.isSafeInteger(response.next_offset)
      || response.issues.length === 0 || response.next_offset !== offset + response.issues.length
      || response.next_offset >= response.total))
    || (response.next_offset === null && offset + response.issues.length < response.total)) {
    throw new IssueApiError('INVALID_RESPONSE', 'unknown')
  }
  return response
}

export async function fetchIssue(id: number, options: IssueDetailPageOptions = {}): Promise<IssueDetailResponse> {
  if (!Number.isSafeInteger(id) || id <= 0) throw new IssueApiError('INVALID_ARGUMENT')
  const page = normalizeIssueDetailPage(options)
  const params = new URLSearchParams({ detail_mode: 'paged' })
  for (const [name, value] of Object.entries(page)) params.set(name, String(value))
  const response = await issueApiRequest<IssueDetailResponse>(`/issues/${id}?${params}`, 'GET')
  if (!response?.issue || response.issue.id !== id || !isIssueRevision(response.issue.revision)
    || !Array.isArray(response.comments) || !Array.isArray(response.history)
    || Object.entries(page).some(([name, value]) => response[name as keyof IssueDetailPageOptions] !== value)
    || response.history.some(entry => !isIssueRevision(entry.revision) || (entry.previous_revision !== null && !isIssueRevision(entry.previous_revision)))) {
    throw new IssueApiError('INVALID_RESPONSE', 'unknown')
  }
  assertIssueCollectionPage(response.comments, response.comment_count, response.comments_offset, response.comments_limit, response.comments_next_offset, response.comments_complete, response.comments_oversized)
  assertIssueCollectionPage(response.history, response.history_count, response.history_offset, response.history_limit, response.history_next_offset, response.history_complete, response.history_oversized)
  return response
}


export async function createIssue(data: CreateIssueInput): Promise<IssueCreateResponse> {
  const snapshot = await prepareIssueCreateInput(data)
  const response = await issueApiRequest<IssueCreateResponse>('/issues', 'POST', snapshot)
  if (!Number.isSafeInteger(response?.id) || response.id <= 0 || !isIssueRevision(response.revision) || typeof response.replayed !== 'boolean') {
    throw new IssueApiError('INVALID_RESPONSE', 'unknown')
  }
  return response
}

export async function fetchVaultStatus(): Promise<VaultStatus> {
  const raw = await apiGet<any>('/vault/status')
  return {
    encrypted: raw.key_configured ?? false,
    key_fingerprint: raw.fingerprint ?? raw.key_fingerprint,
    credential_count: raw.credential_count ?? 0,
  }
}

export async function fetchCredentials(): Promise<VaultCredential[]> {
  return apiGet<VaultCredential[]>('/vault/credentials')
}

export async function fetchCredential(credential: VaultCredential): Promise<{ name: string; value: string }> {
  const binding = credentialBinding(credential)
  if (binding.binding_error !== undefined) throw new Error(`Credential unavailable: ${binding.binding_error}`)
  const params = new URLSearchParams(binding.unresolved_reference !== undefined
    ? { id: String(binding.id), unresolved_reference: binding.unresolved_reference }
    : binding.project_id === null ? { scope: 'global' } : { project: binding.project_id })
  const result = await apiGet<{ name: string; value: string }>(`/vault/credentials/${encodeURIComponent(binding.name)}?${params}`)
  assertCredentialMetadata(result, binding)
  const current = (await fetchCredentials()).find(row => row.id === binding.id)
  if (!current) throw new Error('Credential metadata readback missing')
  assertCredentialMetadata(credentialBinding(current), binding)
  return result
}

export async function deleteCredential(credential: VaultCredential): Promise<void> {
  const binding = credentialBinding(credential)
  if (binding.binding_error !== undefined) throw new Error(`Credential unavailable: ${binding.binding_error}`)
  if (binding.unresolved_reference !== undefined) throw new Error('Retained credentials are read-only; Delete is unsupported')
  const current = (await fetchCredentials()).find(row => row.id === binding.id)
  if (!current) throw new Error('Credential metadata readback missing')
  assertCredentialMetadata(credentialBinding(current), binding)
  const params = new URLSearchParams(binding.project_id === null ? { scope: 'global' } : { project: binding.project_id })
  const result = await apiDelete<{ current_state?: unknown }>(`/vault/credentials/${encodeURIComponent(binding.name)}?${params}`)
  if (result.current_state !== undefined) assertCredentialMetadata(result.current_state, binding)
  if ((await fetchCredentials()).some(row => row.id === binding.id)) throw new Error('Credential deletion readback failed')
}

export async function fetchConfig(): Promise<Record<string, Record<string, unknown>>> {
  return apiGet<Record<string, Record<string, unknown>>>('/config')
}

export async function fetchMaintenanceStats(): Promise<MaintenanceStats> {
  return apiGet<MaintenanceStats>('/maintenance/stats')
}

export async function fetchSelfCheck(): Promise<SelfCheckResponse> {
  return apiGet<SelfCheckResponse>('/selfcheck')
}

export async function checkForUpdate(): Promise<UpdateInfo> {
  return apiGet<UpdateInfo>('/update/check')
}

export async function fetchMemories(query: MemoryReaderQuery): Promise<ReaderPage<Memory>> {
  const project = await resolveProject(query.project)
  const offset = query.offset ?? 0
  const limit = query.limit ?? 100
  const params = new URLSearchParams({ project, offset: String(offset), limit: String(limit) })
  if (query.query) params.set('query', query.query)
  for (const filter of query.filters ?? []) params.append('filter', filter)
  return parseReaderPage(await apiGet<ReaderPage<Memory>>(`/memories?${params}`), project, offset, limit)
}
