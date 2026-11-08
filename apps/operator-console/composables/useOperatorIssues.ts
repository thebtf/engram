import { computed, ref as sharedRef, watch, onBeforeUnmount, type Ref as SharedRef } from 'vue'
import { isCatalogProject, isProjectUUID, projectName, useOperatorProjectContext } from './useOperatorProjectContext'

let sharedIssuesPageRows: SharedRef<OperatorIssue[]> | undefined
let sharedIssuesPageComments: SharedRef<OperatorIssueComment[]> | undefined
let sharedIssuesPageProjectNames: SharedRef<Record<string, string>> | undefined
let sharedIssuesPageTotal: SharedRef<number> | undefined
let sharedIssuesPageDetail: SharedRef<OperatorIssue | null> | undefined
let sharedIssuesPageState: SharedRef<OperatorLoadState<OperatorIssue[]>> | undefined
let sharedIssuesPageDetailState: SharedRef<OperatorLoadState<OperatorIssue | null>> | undefined
let sharedIssuesPageScope = ''

import type { OperatorSelection, OperatorSelectionTarget } from './useOperatorSelection'
import { parseOperatorSelectionSnapshot } from './useOperatorSelection'
import type { OperatorLoadState } from './useOperatorApi'
import { executeMutation, type MutationCurrentStateParser, type MutationResult } from './useApi'
import {
 emptyState,
 authGeneration,
 operatorUnauthorizedRevision,
 endpointEvidence,
 errorState,
 gatedState,
 liveState,
 loadOperatorJson,
 OperatorFetchError,
 operatorErrorDiagnosis,
 operatorFetchResponse,
 operatorFetchJson,
 pendingState,
 toOperatorSourceError,
 unsupportedOperatorAction,
 type OperatorUnsupportedAction,
} from './useOperatorApi'

export type OperatorIssueStatus = 'open' | 'acknowledged' | 'reopened' | 'resolved' | 'closed' | 'rejected'
export type OperatorIssuePriority = 'critical' | 'high' | 'medium' | 'low'
export type OperatorIssueType = 'bug' | 'feature' | 'improvement' | 'task'

export type IssueJsonValue = null | boolean | number | string | IssueJsonValue[] | { [key: string]: IssueJsonValue }
export type IssueMetadata = { [key: string]: IssueJsonValue }
export interface IssueActor { kind: 'client' | 'operator' | 'system' | 'unknown'; reference: string | null; label?: string }
export interface OperatorIssueHistory {
 revision: string
 previousRevision: string | null
 action: string
 changedFields: string[]
 actor: IssueActor | null
 attribution: { source_project: string | null; source_agent: string | null; session: string | null }
 createdAt: string
 changes: IssueMetadata
}
export interface IssueDetailPage {
 offset: number
 limit: number
 length: number
 total: number
 nextOffset: number | null
 complete: boolean
 oversized: boolean
}
type CreateActor = { kind: 'client' | 'operator'; reference: string }
interface StoredIssueCreate { version: 1; actor: CreateActor; input: IssueCreateInput; resolution: 'pending' | 'rejected' | 'gone' }
const CREATE_STORAGE_KEY = 'engram:issues:create-intent:v1'

function object(value: unknown): value is Record<string, unknown> {
 return value !== null && typeof value === 'object' && !Array.isArray(value)
}
function revision(value: unknown): string | null {
 return typeof value === 'string' && /^[1-9][0-9]{0,18}$/.test(value) && (value.length < 19 || value <= '9223372036854775807') ? value : null
}
function jsonValue(value: unknown, depth = 0, ancestors = new Set<object>(), admit = false): asserts value is IssueJsonValue {
 if (value === null || typeof value === 'string' || typeof value === 'boolean') return
 if (typeof value === 'number') {
  if (!Number.isFinite(value) || (admit && Number.isInteger(value) && !Number.isSafeInteger(value))) throw new TypeError('INVALID_ARGUMENT: metadata contains an unsupported number')
  return
 }
 if (typeof value !== 'object' || (!Array.isArray(value) && Object.getPrototypeOf(value) !== Object.prototype && Object.getPrototypeOf(value) !== null) || ancestors.has(value)) throw new TypeError('INVALID_ARGUMENT: metadata must contain only JSON values')
 if (admit && depth >= 256) throw new TypeError('PAYLOAD_TOO_LARGE: metadata exceeds 256 nested containers')
 if (admit && (Object.getOwnPropertySymbols(value).length || Object.values(Object.getOwnPropertyDescriptors(value)).some((descriptor) => descriptor.get || descriptor.set))) throw new TypeError('INVALID_ARGUMENT: metadata must contain plain JSON data')
 ancestors.add(value)
 for (const item of Array.isArray(value) ? value : Object.values(value)) jsonValue(item, depth + 1, ancestors, admit)
 ancestors.delete(value)
}
function metadata(value: unknown, admit = false): IssueMetadata {
 if (!object(value)) throw new TypeError('INVALID_ARGUMENT: metadata must be an object')
 jsonValue(value, 0, new Set(), admit)
 if (admit && new TextEncoder().encode(JSON.stringify(value)).byteLength > 1024 * 1024) throw new TypeError('PAYLOAD_TOO_LARGE: metadata exceeds 1 MiB')
 return value as IssueMetadata
}
function rawMetadata(value: unknown): string {
 if (typeof value !== 'string') throw new TypeError('INVALID_ARGUMENT: raw metadata must be exactly one JSON object')
 let parsed: unknown
 try { parsed = JSON.parse(value) } catch { throw new TypeError('INVALID_ARGUMENT: raw metadata must be exactly one JSON object') }
 if (!object(parsed)) throw new TypeError('INVALID_ARGUMENT: raw metadata must be exactly one non-null JSON object')
 return value
}
function metadataReadbackMatches(actual: IssueMetadata, expected: IssueMetadata | undefined, raw: string | undefined): boolean {
 if (expected === undefined && raw === undefined) return true
 try { return sameJson(actual, raw === undefined ? expected! : JSON.parse(raw) as IssueMetadata) } catch { return false }
}
function requestKey(value: unknown): string {
 if (typeof value !== 'string' || !value.length || value.includes('\0') || /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value) || new TextEncoder().encode(value).byteLength > 512) throw new TypeError('INVALID_ARGUMENT: request_key must be valid nonempty UTF-8 within 512 bytes')
 return value
}
function actor(value: unknown): IssueActor | null {
 if (value === undefined || value === null) return null
 if (!object(value) || !['client', 'operator', 'system', 'unknown'].includes(String(value.kind)) || (value.reference !== null && typeof value.reference !== 'string')) throw new TypeError('Invalid issue actor')
 return { kind: value.kind as IssueActor['kind'], reference: value.reference, ...(typeof value.label === 'string' ? { label: value.label } : {}) }
}
function nullableLabel(value: unknown): string | null {
 if (value === undefined || value === null) return null
 if (typeof value !== 'string' || !value.length) throw new TypeError('INVALID_ARGUMENT: owner and recipient labels must be nonempty or null')
 return value
}
function sameJson(left: IssueJsonValue, right: IssueJsonValue): boolean {
 if (left === right) return true
 if (Array.isArray(left) && Array.isArray(right)) return left.length === right.length && left.every((value, index) => sameJson(value, right[index]!))
 if (object(left) && object(right)) return Object.keys(left).length === Object.keys(right).length && Object.keys(left).every((key) => Object.hasOwn(right, key) && sameJson(left[key] as IssueJsonValue, right[key] as IssueJsonValue))
 return false
}
function detailPath(id: number, project = 'all', commentsOffset = 0, historyOffset = 0): string {
 const params = new URLSearchParams({ detail_mode: 'paged', comments_offset: String(commentsOffset), history_offset: String(historyOffset), comments_limit: '50', history_limit: '50' })
 if (project !== 'all') params.set('project', project)
 return `/api/issues/${id}?${params}`
}

export interface OperatorIssue {
 id: number
 revision: string | null
 metadata: IssueMetadata
 ownerLabel: string | null
 recipientLabel: string | null
 actor: IssueActor | null
 createdBySession: string | null
 targetSession: string | null
 title: string
 body: string
 status: OperatorIssueStatus
 priority: OperatorIssuePriority
 type: OperatorIssueType
 age: string
 comments: number
 sourceProject: string | null
 targetProject: string | null
 projectOrigin: { sourceProject: string; targetProject: string }
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
 metadata: IssueMetadata
 actor: IssueActor | null
 createdAt: string
 age: string
}

export interface IssueCreateInput {
 title: string
 body?: string
 priority: OperatorIssuePriority
 type: OperatorIssueType
 sourceProject: string
 targetProject: string
 labels?: string[]
 requestKey?: string
 metadata?: IssueMetadata
 rawMetadata?: string
 ownerLabel?: string | null
 recipientLabel?: string | null
 createdBySession?: string
 targetSession?: string
}

export interface IssueUpdateInput {
 title?: string
 body?: string
 priority?: OperatorIssuePriority
 type?: OperatorIssueType
 status?: OperatorIssueStatus
 comment?: string
 labels?: string[]
 expectedRevision?: string
 metadata?: IssueMetadata
 rawMetadata?: string
 commentMetadata?: IssueMetadata
 commentRawMetadata?: string
 ownerLabel?: string | null
 recipientLabel?: string | null
 sessionId?: string
 targetSession?: string | null
}

export type IssueSelectionAction =
 | { kind: 'acknowledge' }
 | { kind: 'status'; status: OperatorIssueStatus }
 | { kind: 'priority'; priority: OperatorIssuePriority }
 | { kind: 'labels'; labels: string[] }

export interface IssueSelectionFilter {
 project?: string
 sourceProject?: string
 statuses?: OperatorIssueStatus[]
 type?: OperatorIssueType
}

interface IssueListOptions {
 limit: number
 offset: number
 role: 'target' | 'source'
 statuses: OperatorIssueStatus[]
 q?: string
 ownerLabel?: string
 sourceSession?: string
 participantSession?: string
 targetSession?: string
}

export interface IssueSelectionPage {
 cursor: string
 nextCursor: string
 filterFingerprint: string
 targets: OperatorSelectionTarget[]
 total: number
}

export interface IssueSelectionCurrent {
 status: OperatorIssueStatus
 priority: OperatorIssuePriority
 labels: string[]
 updatedAt: string
}

interface SelectionEnvelope { selection?: unknown }

interface IssueSelectionPageEnvelope {
 filter_fingerprint?: unknown
 cursor?: unknown
 next_cursor?: unknown
 targets?: unknown
 total?: unknown
}

function selectionInit(requestId: string, body: unknown): RequestInit {
 return { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Engram-Request-ID': requestId }, body: JSON.stringify(body) }
}

function issueSelectionPayload(selection: Exclude<OperatorSelection, { kind: 'frozen_filter' }>): Record<string, unknown> {
 switch (selection.kind) {
  case 'none': return { kind: 'none' }
  case 'explicit': return { kind: 'explicit', targets: selection.targets.map((target) => ({ id: target.id })) }
  case 'page': return { kind: 'page', cursor: selection.cursor }
 }
}

function parseIssueSelection(value: unknown): OperatorSelection {
 if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new TypeError('Issues selection response is invalid')
 const selection = Reflect.get(value, 'selection')
 return parseOperatorSelectionSnapshot(selection)
}

function parseIssueSelectionPage(value: unknown): IssueSelectionPage {
 if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new TypeError('Issues page response is invalid')
 const body = value as IssueSelectionPageEnvelope
 const total = body.total
 if (typeof body.filter_fingerprint !== 'string' || !/^sha256:[a-f0-9]{64}$/.test(body.filter_fingerprint) || typeof body.cursor !== 'string' || typeof body.next_cursor !== 'string' || !Array.isArray(body.targets) || typeof total !== 'number' || !Number.isSafeInteger(total) || total < body.targets.length) throw new TypeError('Issues page response is invalid')
 const selection = parseOperatorSelectionSnapshot({ domain: 'issues', kind: 'page', cursor: body.cursor, targets: body.targets })
 if (selection.kind !== 'page') throw new TypeError('Issues page response is invalid')
 return { cursor: body.cursor, nextCursor: body.next_cursor, filterFingerprint: body.filter_fingerprint, targets: selection.targets, total }
}

const parseIssueSelectionCurrent: MutationCurrentStateParser<IssueSelectionCurrent[]> = (value) => {
 if (!Array.isArray(value) || !value.length) return undefined
 const current: IssueSelectionCurrent[] = []
 for (const item of value) {
  if (item === null || typeof item !== 'object' || Array.isArray(item)) return undefined
  const status = Reflect.get(item, 'status')
  const priority = Reflect.get(item, 'priority')
  const labels = Reflect.get(item, 'labels')
  const updatedAt = Reflect.get(item, 'updated_at')
  if (!['open', 'acknowledged', 'reopened', 'resolved', 'closed', 'rejected'].includes(String(status)) || !['critical', 'high', 'medium', 'low'].includes(String(priority)) || !Array.isArray(labels) || !labels.every((label) => typeof label === 'string') || typeof updatedAt !== 'string' || Number.isNaN(Date.parse(updatedAt))) return undefined
  current.push({ status: status as OperatorIssueStatus, priority: priority as OperatorIssuePriority, labels: [...labels], updatedAt })
 }
 return current
}

interface ApiIssueRow {
 id: number
 revision?: unknown
 metadata?: unknown
 owner_label?: unknown
 recipient_label?: unknown
 actor?: unknown
 created_by_session?: unknown
 target_session?: unknown
 title?: string
 body?: string
 status?: string
 priority?: string
 type?: string
 source_project_id: string | null
 target_project_id: string | null
 project_origin: { source_project: string; target_project: string }
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
 metadata?: unknown
 actor?: unknown
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
 history?: unknown[]
 history_count?: number
 comments_offset?: number
 comments_limit?: number
 comments_next_offset?: number | null
 comments_complete?: boolean
 comments_oversized?: boolean
 history_offset?: number
 history_limit?: number
 history_next_offset?: number | null
 history_complete?: boolean
 history_oversized?: boolean
}

async function readIssueDetail(path: string, source: string): Promise<ApiIssueDetail> {
 const response = await operatorFetchResponse(path, { cache: 'no-store' })
 let value: unknown
 try { value = await response.json() } catch { if (response.ok) throw new TypeError('Invalid issue detail JSON') }
 if (!response.ok) {
  const error = object(value) && object(value.error) ? value.error : undefined
  const code = error && typeof error.code === 'string' ? error.code : `HTTP_${response.status}`
  const message = error && typeof error.message === 'string' ? `${code}: ${error.message}` : code
  throw new OperatorFetchError(message, { message, category: operatorErrorDiagnosis(response.status), status: response.status, source, path, method: 'GET', retryable: response.status >= 500 })
 }
 if (!object(value)) throw new TypeError('Invalid issue detail response')
 return value as ApiIssueDetail
}

async function issueMutation<TIntent>(
 request: { requestId: string; action: string; intent: TIntent },
 path: string,
 init: RequestInit,
): Promise<{ mutation: MutationResult<TIntent, OperatorIssue>; body: unknown }> {
 let response: Response
 try {
  response = await operatorFetchResponse(path, { ...init, credentials: 'include' })
 } catch (error) {
  return { mutation: await executeMutation<TIntent, OperatorIssue>(request, Promise.reject(error), () => undefined), body: undefined }
 }

 let body: unknown
 try {
  body = await response.clone().json()
 } catch {
  body = undefined
 }
 let mutation = await executeMutation<TIntent, OperatorIssue>(request, Promise.resolve(response), () => undefined)
 const error = object(body) && object(body.error) ? body.error : undefined
 if (error && typeof error.code === 'string') mutation = { ...mutation, code: error.code }
 if (response.status === 428 || response.status === 413) mutation = { kind: 'validation_error', request, httpStatus: response.status, ...(error && typeof error.code === 'string' ? { code: error.code } : {}) }
 return { mutation, body }
}




function jsonInit(method: 'POST' | 'PATCH' | 'DELETE', body?: unknown, rawFields?: { metadata?: string; comment_metadata?: string }): RequestInit {
 const init: RequestInit = { method }
 if (body !== undefined) {
  init.headers = { 'Content-Type': 'application/json' }
  const encoded = JSON.stringify(body)
  const fields = Object.entries(rawFields ?? {}).filter(([, raw]) => raw !== undefined)
  if (fields.length) {
   if (!object(body) || typeof encoded !== 'string' || !encoded.startsWith('{') || !encoded.endsWith('}')) throw new TypeError('INVALID_ARGUMENT: raw metadata requires an object request')
   const fragments = fields.map(([field, raw]) => {
    if (Object.hasOwn(body, field)) throw new TypeError('INVALID_ARGUMENT: object and raw metadata are mutually exclusive')
    return `${JSON.stringify(field)}:${rawMetadata(raw)}`
   })
   init.body = `${encoded.slice(0, -1)}${encoded === '{}' ? '' : ','}${fragments.join(',')}}`
  } else init.body = encoded
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


function mapIssue(
 row: ApiIssueRow,
 commentCount = row.comment_count,
): OperatorIssue {
 if ((row.source_project_id !== null && !isProjectUUID(row.source_project_id)) || (row.target_project_id !== null && !isProjectUUID(row.target_project_id)) || !row.project_origin || typeof row.project_origin.source_project !== 'string' || typeof row.project_origin.target_project !== 'string') throw new TypeError('Invalid canonical issue project roles or origin')
 const sourceProject = row.source_project_id
 const targetProject = row.target_project_id
 const createdAt = row.created_at || ''
 const updatedAt = row.updated_at || createdAt
 return {
  id: row.id,
  revision: row.revision === undefined ? null : requiredRevision(row.revision),
  metadata: row.metadata === undefined ? {} : metadata(row.metadata),
  ownerLabel: nullableLabel(row.owner_label),
  recipientLabel: nullableLabel(row.recipient_label),
  actor: actor(row.actor),
  createdBySession: typeof row.created_by_session === 'string' && row.created_by_session ? row.created_by_session : null,
  targetSession: typeof row.target_session === 'string' && row.target_session ? row.target_session : null,
  title: row.title || '-',
  body: row.body || '',
  status: normalizeStatus(row.status),
  priority: normalizePriority(row.priority),
  type: normalizeType(row.type),
  age: compactAge(updatedAt || createdAt),
  comments: typeof commentCount === 'number' ? commentCount : 0,
  sourceProject,
  targetProject,
  projectOrigin: { sourceProject: row.project_origin.source_project, targetProject: row.project_origin.target_project },
  sourceDisplayName: sourceProject && isCatalogProject(sourceProject) ? projectName(sourceProject) : '',
  targetDisplayName: targetProject && isCatalogProject(targetProject) ? projectName(targetProject) : '',
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
  authorProject: row.author_project || '',
  authorAgent: row.author_agent || 'operator',
  body: row.body || '',
  metadata: row.metadata === undefined ? {} : metadata(row.metadata),
  actor: actor(row.actor),
  createdAt,
  age: compactAge(createdAt),
 }
}
async function verifyCurrentIssue<TIntent>(
 mutation: MutationResult<TIntent, OperatorIssue>,
 id: number,
 matches: (issue: OperatorIssue, comments: OperatorIssueComment[]) => boolean,
 committedCommentId?: number,
): Promise<MutationResult<TIntent, OperatorIssue>> {
 if (mutation.kind !== 'committed_verification_pending' || mutation.commitment !== 'committed') return mutation

 let issue: OperatorIssue | null = null
 let comments: OperatorIssueComment[] = []
 try {
  const payload = await readIssueDetail(detailPath(id), 'issues-mutation-readback')
  issue = payload.issue ? mapIssue(payload.issue, payload.comment_count ?? payload.comments?.length) : null
  comments = (payload.comments || []).map(mapComment)
  detailPage(payload, 'comments', comments.length)
  if (committedCommentId !== undefined && !comments.some((comment) => comment.id === committedCommentId) && typeof payload.comment_count === 'number' && payload.comment_count > 0) {
   const tail = await readIssueDetail(detailPath(id, 'all', payload.comment_count - 1), 'issues-comment-readback')
   if (tail.issue?.id !== id) throw new TypeError('Issue comment readback identity mismatch')
   const lastComments = (tail.comments || []).map(mapComment)
   detailPage(tail, 'comments', lastComments.length)
   comments = [...comments, ...lastComments]
  }
 } catch {
  issue = null
 }
 if (!issue || issue.id !== id || !matches(issue, comments)) return { ...mutation, reason: 'readback_invalid' }

 return {
  kind: 'committed_verified',
  request: mutation.request,
  httpStatus: mutation.httpStatus,
  ...(mutation.operationId === undefined ? {} : { operationId: mutation.operationId }),
  ...(mutation.code === undefined ? {} : { code: mutation.code }),
  readback: { kind: 'current', current: issue },
 }
}

async function verifyDeletedIssue<TIntent>(mutation: MutationResult<TIntent>, id: number): Promise<MutationResult<TIntent>> {
 if (mutation.kind !== 'committed_verification_pending' || mutation.commitment !== 'committed') return mutation
 try {
  const response = await operatorFetchResponse(detailPath(id), { credentials: 'include' })
  if (response.status === 404) {
   return {
    kind: 'committed_verified',
    request: mutation.request,
    httpStatus: mutation.httpStatus,
    ...(mutation.operationId === undefined ? {} : { operationId: mutation.operationId }),
    ...(mutation.code === undefined ? {} : { code: mutation.code }),
    readback: { kind: 'authorized_absence' },
   }
  }
 } catch {
  // A failed readback never proves deletion.
 }
 return { ...mutation, reason: 'readback_invalid' }
}

function requiredRevision(value: unknown): string {
 const result = revision(value)
 if (result === null) throw new TypeError('REVISION_REQUIRED: a canonical decimal issue revision is required')
 return result
}
function mapHistory(value: unknown): OperatorIssueHistory {
 if (!object(value) || typeof value.action !== 'string' || !Array.isArray(value.changed_fields) || !value.changed_fields.every((field) => typeof field === 'string') || !object(value.attribution) || typeof value.created_at !== 'string') throw new TypeError('Invalid issue history')
 const attribution = value.attribution
 for (const field of ['source_project', 'source_agent', 'session']) if (attribution[field] !== null && typeof attribution[field] !== 'string') throw new TypeError('Invalid issue history attribution')
 return { revision: requiredRevision(value.revision), previousRevision: value.previous_revision === null ? null : requiredRevision(value.previous_revision), action: value.action, changedFields: [...value.changed_fields], actor: actor(value.actor), attribution: { source_project: attribution.source_project as string | null, source_agent: attribution.source_agent as string | null, session: attribution.session as string | null }, createdAt: value.created_at, changes: metadata(value.changes) }
}
function detailPage(payload: ApiIssueDetail, kind: 'comments' | 'history', length: number): IssueDetailPage {
 const offset = payload[`${kind}_offset`], limit = payload[`${kind}_limit`], next = payload[`${kind}_next_offset`], complete = payload[`${kind}_complete`], oversized = payload[`${kind}_oversized`]
 const total = kind === 'comments' ? payload.comment_count : payload.history_count
 if (typeof offset !== 'number' || !Number.isSafeInteger(offset) || offset < 0 || !Number.isSafeInteger(length) || length < 0 || offset > Number.MAX_SAFE_INTEGER - length || typeof limit !== 'number' || !Number.isInteger(limit) || limit < 1 || limit > 200 || typeof total !== 'number' || !Number.isSafeInteger(total) || total < 0 || typeof complete !== 'boolean' || typeof oversized !== 'boolean') throw new TypeError('DETAIL_PAGING_REQUIRED: invalid bounded issue detail page')
 const end = offset + length
 if ((length > 0 && end > total) || (next === null && end < total) || (next !== null && (typeof next !== 'number' || !Number.isSafeInteger(next) || next !== end || length === 0 || next >= total)) || complete !== (next === null) || length > limit || (oversized && length !== 1)) throw new TypeError('DETAIL_PAGING_REQUIRED: invalid bounded issue detail page')
 return { offset, limit, length, total, nextOffset: next, complete, oversized }
}

function sameLabels(left: string[], right: string[]): boolean {
 const a = [...new Set(left)].sort(), b = [...new Set(right)].sort()
 return a.length === b.length && a.every((label, index) => label === b[index])
}
function validateCreate(value: unknown): IssueCreateInput {
 if (!object(value) || typeof value.title !== 'string' || !value.title.trim() || (value.body !== undefined && typeof value.body !== 'string') || typeof value.sourceProject !== 'string' || typeof value.targetProject !== 'string' || !['critical', 'high', 'medium', 'low'].includes(String(value.priority)) || !['bug', 'feature', 'improvement', 'task'].includes(String(value.type)) || (value.labels !== undefined && (!Array.isArray(value.labels) || !value.labels.every((label) => typeof label === 'string')))) throw new TypeError('INVALID_ARGUMENT: invalid create fields')
 if (value.rawMetadata !== undefined) {
  if (value.metadata !== undefined) throw new TypeError('INVALID_ARGUMENT: object and raw metadata are mutually exclusive')
  rawMetadata(value.rawMetadata)
 }
 if (value.metadata !== undefined) metadata(value.metadata, true)
 if (value.ownerLabel !== undefined) nullableLabel(value.ownerLabel)
 if (value.recipientLabel !== undefined) nullableLabel(value.recipientLabel)
 if (value.createdBySession !== undefined && typeof value.createdBySession !== 'string') throw new TypeError('INVALID_ARGUMENT: invalid create fields')
 if (value.targetSession !== undefined && typeof value.targetSession !== 'string') throw new TypeError('INVALID_ARGUMENT: invalid create fields')
 const input = { title: value.title, ...(value.body === undefined ? {} : { body: value.body }), priority: value.priority, type: value.type, sourceProject: value.sourceProject, targetProject: value.targetProject, labels: value.labels ?? [], ...(value.rawMetadata === undefined ? { metadata: value.metadata ?? {} } : { rawMetadata: value.rawMetadata }), ownerLabel: value.ownerLabel ?? null, recipientLabel: value.recipientLabel ?? null, ...(value.createdBySession ? { createdBySession: value.createdBySession } : {}), ...(value.targetSession ? { targetSession: value.targetSession } : {}), requestKey: requestKey(value.requestKey ?? crypto.randomUUID()) }
 return JSON.parse(JSON.stringify(input)) as IssueCreateInput
}
function createPayload(input: IssueCreateInput) {
 return { title: input.title, body: input.body ?? '', priority: input.priority, type: input.type, source_project: input.sourceProject, target_project: input.targetProject, source_agent: 'human', labels: input.labels ?? [], ...(input.rawMetadata === undefined ? { metadata: input.metadata ?? {} } : {}), owner_label: input.ownerLabel ?? null, recipient_label: input.recipientLabel ?? null, ...(input.createdBySession ? { created_by_session: input.createdBySession } : {}), ...(input.targetSession ? { target_session: input.targetSession } : {}), request_key: requestKey(input.requestKey) }
}



export function useOperatorIssues(options?: Readonly<SharedRef<IssueListOptions>>) {
 const listEvidence = endpointEvidence('/api/issues?limit=200', 'issues-list')
 const detailEvidence = endpointEvidence('/api/issues/{id}', 'issues-detail')
 const rowsState = (sharedIssuesPageRows ??= sharedRef<OperatorIssue[]>((() => [])()))
 const commentsState = (sharedIssuesPageComments ??= sharedRef<OperatorIssueComment[]>((() => [])()))
 const projectNamesState = (sharedIssuesPageProjectNames ??= sharedRef<Record<string, string>>((() => ({}))()))
 const context = useOperatorProjectContext()
 const totalCountState = (sharedIssuesPageTotal ??= sharedRef<number>((() => 0)()))
 const detailStateValue = (sharedIssuesPageDetail ??= sharedRef<OperatorIssue | null>((() => null)()))
 const state = (sharedIssuesPageState ??= sharedRef<OperatorLoadState<OperatorIssue[]>>((() => pendingState(listEvidence, rowsState.value))()))
 const detailLoadState = (sharedIssuesPageDetailState ??= sharedRef<OperatorLoadState<OperatorIssue | null>>((() => emptyState(detailEvidence, null))()))
 const history = sharedRef<OperatorIssueHistory[]>([])
 const commentsPage = sharedRef<IssueDetailPage | null>(null)
 const historyPage = sharedRef<IssueDetailPage | null>(null)
 const pageLoading = sharedRef<'comments' | 'history' | null>(null)
 const pageError = sharedRef('')
 const pendingCreate = sharedRef<IssueCreateInput | null>(null)
 const hasCreateIntent = sharedRef(false)
 const createRecoveryError = sharedRef('')
 const createResolution = sharedRef<StoredIssueCreate['resolution']>('pending')
 let createBusy = false
 const createPending = sharedRef(false)

 function storedCreate(): StoredIssueCreate | null {
  let raw: string | null
  try { raw = sessionStorage.getItem(CREATE_STORAGE_KEY) } catch { throw new TypeError('CREATE_STORAGE_UNAVAILABLE: tab storage is unavailable; no create was dispatched') }
  if (!raw || raw === 'null') return null
  hasCreateIntent.value = true
  let value: unknown
  try { value = JSON.parse(raw) } catch { throw new TypeError('CREATE_INTENT_INVALID: retained intent is unreadable; no replacement task was created') }
  if (!object(value) || value.version !== 1 || !object(value.actor) || !['client', 'operator'].includes(String(value.actor.kind)) || typeof value.actor.reference !== 'string' || !value.actor.reference.length || !object(value.input) || typeof value.input.requestKey !== 'string' || !['pending', 'rejected', 'gone'].includes(String(value.resolution))) throw new TypeError('CREATE_INTENT_INVALID: retained intent needs recovery; no replacement task was created')
  const input = validateCreate(value.input)
  return { version: 1, actor: { kind: value.actor.kind as CreateActor['kind'], reference: value.actor.reference }, input, resolution: value.resolution as StoredIssueCreate['resolution'] }
 }
 function persistCreate(value: StoredIssueCreate | null, publishPayload = true) {
  const encoded = JSON.stringify(value)
  try {
   sessionStorage.setItem(CREATE_STORAGE_KEY, encoded)
   if (sessionStorage.getItem(CREATE_STORAGE_KEY) !== encoded) throw new Error('Storage did not retain the intent')
  } catch {
   hasCreateIntent.value = true
   throw new TypeError('CREATE_STORAGE_UNAVAILABLE: intent could not be retained in this tab; do not create a replacement task')
  }
  hasCreateIntent.value = value !== null
  createResolution.value = value?.resolution ?? 'pending'
  if (value === null) pendingCreate.value = null
  if (publishPayload) {
   pendingCreate.value = value ? JSON.parse(JSON.stringify(value.input)) as IssueCreateInput : null
   createRecoveryError.value = ''
  }
 }
 async function currentActor(): Promise<CreateActor> {
  let value: unknown
  try { value = await operatorFetchJson<unknown>('/api/auth/me', { cache: 'no-store' }, 'issues-create-identity') } catch { throw new TypeError('CREATE_IDENTITY_UNAVAILABLE: confirm the original authenticated actor before creating or reconciling') }
  if (object(value) && (value.auth_disabled === true || (value.auth_disabled === false && value.authenticated === true)) && object(value.actor) && (value.actor.kind === 'client' || value.actor.kind === 'operator') && typeof value.actor.reference === 'string' && value.actor.reference.length) return { kind: value.actor.kind, reference: value.actor.reference }
  throw new TypeError('CREATE_IDENTITY_UNAVAILABLE: the server must provide a trusted non-secret actor projection before this intent can be created or resumed')
 }
 function createScope(input: IssueCreateInput) {
  if (context.selectionPending.value || context.selectionError.value || context.error.value || !isCatalogProject(input.sourceProject) || !isCatalogProject(input.targetProject) || (context.project.value !== 'all' && input.sourceProject !== context.project.value)) throw new TypeError('CREATE_SOURCE_MISMATCH: select the original catalog source and target before dispatch')
 }
 async function restoreCreateIntent() {
  const identityGeneration = authGeneration.value, unauthorizedGeneration = operatorUnauthorizedRevision.value
  try {
   const stored = storedCreate()
   if (!stored) { hasCreateIntent.value = false; pendingCreate.value = null; createRecoveryError.value = ''; return }
   const current = await currentActor()
   if (identityGeneration !== authGeneration.value || unauthorizedGeneration !== operatorUnauthorizedRevision.value) return
   if (stored.actor.kind !== current.kind || stored.actor.reference !== current.reference) { pendingCreate.value = null; throw new TypeError('CREATE_ACTOR_MISMATCH: return to the original authenticated actor; retained payload is not displayed or replayed') }
   pendingCreate.value = JSON.parse(JSON.stringify(stored.input)) as IssueCreateInput
   createResolution.value = stored.resolution
   createRecoveryError.value = ''
  } catch (cause) {
   if (identityGeneration !== authGeneration.value || unauthorizedGeneration !== operatorUnauthorizedRevision.value) return
   createRecoveryError.value = cause instanceof Error ? cause.message : 'CREATE_INTENT_UNAVAILABLE'
  }
 }
 try { if (storedCreate()) void restoreCreateIntent() } catch (cause) { hasCreateIntent.value = true; createRecoveryError.value = cause instanceof Error ? cause.message : 'CREATE_INTENT_UNAVAILABLE' }
 watch([authGeneration, operatorUnauthorizedRevision], () => {
  pendingCreate.value = null
  createRecoveryError.value = hasCreateIntent.value ? 'CREATE_IDENTITY_RECHECK_REQUIRED: verify the original actor before showing or replaying its retained intent' : ''
 }, { flush: 'sync' })

 const loadState = computed(() => state.value)
 const detailState = computed(() => detailLoadState.value)
 const detail = computed(() => detailStateValue.value)
 const trackedProjects = context.projects
 const registryTotal = computed(() => Math.max(totalCountState.value, rowsState.value.length))
 const pending = computed(() => state.value.kind === 'pending')
 const error = computed(() => state.value.kind === 'error' ? state.value.error.message : null)
 const routeChangeAction = unsupportedOperatorAction(
  'issue-route-change',
  'PATCH /api/issues/{id} target_project',
  'Changing target_project is not exposed by the current issue update endpoint.',
 )

 let generation = 0
 let detailGeneration = 0

 function catalogFilter(filter: IssueSelectionFilter, project: string): IssueSelectionFilter {
  const sourceRole = options?.value.role === 'source'
  if ((filter.project !== undefined && (!isCatalogProject(filter.project) || (!sourceRole && filter.project !== project))) || (filter.sourceProject !== undefined && (!isCatalogProject(filter.sourceProject) || (sourceRole && filter.sourceProject !== project)))) throw new TypeError('Issue filter must use the selected catalog project role')
  return { ...filter, ...(sourceRole ? { sourceProject: project } : { project }) }
 }

 function humanSource(sourceProject: string) {
  if (context.selectionPending.value || context.selectionError.value || context.error.value || !isCatalogProject(sourceProject) || sourceProject !== context.project.value) throw new TypeError('Choose a catalog source project before changing issues')
  return { source_project: sourceProject, source_agent: 'human' as const }
 }

 async function refresh() {
  const current = ++generation
  const scope = context.project.value
  const listScope = JSON.stringify([scope, options?.value.role || 'target', options?.value.statuses || [], options?.value.q || '', options?.value.ownerLabel || '', options?.value.sourceSession || '', options?.value.participantSession || '', options?.value.targetSession || ''])
  if (listScope !== sharedIssuesPageScope) {
   replaceArray(rowsState.value, [])
   totalCountState.value = 0
   sharedIssuesPageScope = listScope
  }
  if (context.selectionPending.value || context.selectionError.value || context.error.value || !scope || (scope !== 'all' && !isCatalogProject(scope))) { state.value = gatedState(listEvidence, 'project-selection', 'Choose a catalog project before reading issues.', rowsState.value); return }
  state.value = pendingState(listEvidence, rowsState.value)
  const params = new URLSearchParams({ limit: String(options?.value.limit ?? 200), offset: String(options?.value.offset ?? 0) })
  if (scope !== 'all') params.set(options?.value.role === 'source' ? 'source_project' : 'project', scope)
  if (options?.value.statuses.length) params.set('status', options.value.statuses.join(','))
  if (options?.value.q) params.set('q', options.value.q)
  if (options?.value.ownerLabel) params.set('owner_label', options.value.ownerLabel)
  if (options?.value.sourceSession) params.set('source_session', options.value.sourceSession)
  if (options?.value.participantSession) params.set('participant_session', options.value.participantSession)
  if (options?.value.targetSession) params.set('target_session', options.value.targetSession)
  const result = await loadOperatorJson<ApiIssueList>(`/api/issues?${params}`, {
   source: 'issues-list',
   empty: (payload) => !(payload.issues || []).length,
  })
  if (current !== generation || scope !== context.project.value) return

  if (result.kind === 'live' || result.kind === 'empty') {
   replaceRecord(projectNamesState.value, Object.fromEntries(context.projects.value.map((id) => [id, projectName(id)])))
   let nextRows: OperatorIssue[]
   let nextTotal: number
   try {
    if (!Array.isArray(result.data.issues) || typeof result.data.total !== 'number' || !Number.isSafeInteger(result.data.total) || result.data.total < result.data.issues.length) throw new TypeError('Invalid issue list total or page')
    nextTotal = result.data.total
    nextRows = result.data.issues.map((row) => mapIssue(row, row.comment_count))
   } catch (cause) {
    const mapped = toOperatorSourceError(cause, { source: 'issues-list', path: listEvidence.endpoint, method: 'GET' })
    mapped.category = 'invalid-response'
    state.value = errorState(listEvidence, mapped, { source: 'issues-list', run: async () => { await refresh(); return state.value } }, rowsState.value)
    return
   }
   replaceArray(rowsState.value, nextRows)
   totalCountState.value = nextTotal
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
  const scope = context.project.value
  const current = ++detailGeneration
  if (context.selectionPending.value || context.selectionError.value || context.error.value || !scope || (scope !== 'all' && !isCatalogProject(scope))) return
  const endpoint = detailPath(id, scope)
  const evidence = endpointEvidence(endpoint, 'issues-detail')
  detailLoadState.value = pendingState(evidence, detailStateValue.value)
  replaceArray(commentsState.value, [])
  replaceArray(history.value, [])
  commentsPage.value = null
  historyPage.value = null
  pageError.value = ''
  try {
   const payload = await readIssueDetail(endpoint, 'issues-detail')
   if (current !== detailGeneration || scope !== context.project.value) return
   const next = payload.issue
    ? mapIssue(payload.issue, payload.comment_count ?? payload.comments?.length)
    : null
   if (next && (next.id !== id || (scope !== 'all' && next.sourceProject !== scope && next.targetProject !== scope))) throw new TypeError('Issue detail identity or project mismatch')
   const nextComments = (payload.comments || []).map(mapComment)
   const nextHistory = (payload.history || []).map(mapHistory)
   const nextCommentsPage = detailPage(payload, 'comments', nextComments.length)
   const nextHistoryPage = detailPage(payload, 'history', nextHistory.length)
   detailStateValue.value = next
   replaceArray(commentsState.value, nextComments)
   replaceArray(history.value, nextHistory)
   commentsPage.value = nextCommentsPage
   historyPage.value = nextHistoryPage
   detailLoadState.value = next ? liveState(evidence, next) : emptyState(evidence, null)
  } catch (nextError) {
   if (current !== detailGeneration || scope !== context.project.value) return
   const mapped = toOperatorSourceError(nextError, {
    source: 'issues-detail',
    path: endpoint,
    method: 'GET',
   })
   if (nextError instanceof TypeError) mapped.category = 'invalid-response'
   detailStateValue.value = null
   replaceArray(commentsState.value, [])
   replaceArray(history.value, [])
   commentsPage.value = null
   historyPage.value = null
   detailLoadState.value = errorState(evidence, mapped, {
    source: 'issues-detail',
    run: async () => {
     await openIssue(id)
     return detailLoadState.value
    },
   }, null)
  }
 }

 async function loadMore(kind: 'comments' | 'history') {
  const page = kind === 'comments' ? commentsPage.value : historyPage.value
  const id = detailStateValue.value?.id
  if (!id || !page || page.nextOffset === null || pageLoading.value) return
  const current = detailGeneration
  const scope = context.project.value
  const offset = page.nextOffset
  pageLoading.value = kind
  pageError.value = ''
  try {
   const payload = await readIssueDetail(detailPath(id, scope, kind === 'comments' ? offset : commentsPage.value?.offset ?? 0, kind === 'history' ? offset : historyPage.value?.offset ?? 0), 'issues-detail-continuation')
   if (current !== detailGeneration || scope !== context.project.value) return
   if (!payload.issue || payload.issue.id !== id || (scope !== 'all' && payload.issue.source_project_id !== scope && payload.issue.target_project_id !== scope)) throw new TypeError('Issue continuation identity or project mismatch')
   const items = kind === 'comments' ? (payload.comments || []).map(mapComment) : (payload.history || []).map(mapHistory)
   const nextPage = detailPage(payload, kind, items.length)
   if (nextPage.offset !== offset) throw new TypeError('Issue continuation offset mismatch')
   if (kind === 'comments') { replaceArray(commentsState.value, items as OperatorIssueComment[]); commentsPage.value = nextPage }
   else { replaceArray(history.value, items as OperatorIssueHistory[]); historyPage.value = nextPage }
  } catch (cause) {
   if (current === detailGeneration) {
    pageError.value = cause instanceof Error ? cause.message : 'Issue continuation unavailable'
    if (cause instanceof OperatorFetchError && (cause.status === 401 || cause.status === 403 || cause.status === 404)) {
     detailStateValue.value = null
     replaceArray(commentsState.value, [])
     replaceArray(history.value, [])
     commentsPage.value = null
     historyPage.value = null
     const mapped = toOperatorSourceError(cause, { source: 'issues-detail-continuation', path: detailPath(id, scope), method: 'GET' })
     detailLoadState.value = errorState(endpointEvidence(detailPath(id, scope), 'issues-detail'), mapped, { source: 'issues-detail', run: async () => { await openIssue(id); return detailLoadState.value } }, null)
    }
   }
  }
  finally { pageLoading.value = null }
 }

 async function dispatchCreate(stored: StoredIssueCreate): Promise<MutationResult<IssueCreateInput, OperatorIssue>> {
  const input = stored.input
  const request = { requestId: crypto.randomUUID(), action: 'issue-create', intent: JSON.parse(JSON.stringify(input)) as IssueCreateInput }
  const response = await issueMutation(request, '/api/issues', jsonInit('POST', { ...createPayload(input), expected_actor: { ...stored.actor } }, { metadata: input.rawMetadata }))
  const id = object(response.body) ? response.body.id : undefined
  let result = response.mutation
  if (typeof id === 'number' && Number.isSafeInteger(id) && id > 0 && object(response.body) && revision(response.body.revision) !== null && typeof response.body.replayed === 'boolean') {
   const replayed = object(response.body) && response.body.replayed === true
   result = await verifyCurrentIssue(result, id, (issue) => issue.id === id && issue.sourceProject === input.sourceProject && issue.targetProject === input.targetProject && (replayed || (issue.title === input.title && issue.body === (input.body ?? '') && issue.priority === input.priority && issue.type === input.type && sameLabels(issue.labels, input.labels ?? []) && metadataReadbackMatches(issue.metadata, input.metadata, input.rawMetadata) && issue.ownerLabel === (input.ownerLabel ?? null) && issue.recipientLabel === (input.recipientLabel ?? null) && (!input.targetSession || issue.targetSession === input.targetSession))))
  }
  if (result.kind === 'committed_verified') {
   try { persistCreate(null, false) } catch { createRecoveryError.value = 'CREATE_STORAGE_UNAVAILABLE: the task was verified, but its retained intent could not be cleared; reconcile before a new task' }
  } else if (result.kind === 'validation_error' || (result.kind === 'stale' && result.code === 'ISSUE_GONE')) {
   persistCreate({ ...stored, resolution: result.code === 'ISSUE_GONE' ? 'gone' : 'rejected' }, false)
  }
  return result
 }
 async function createIssue(input: IssueCreateInput) {
  if (createBusy) throw new TypeError('CREATE_IN_PROGRESS: wait for the dispatched intent')
  createScope(input)
  if (storedCreate()) throw new TypeError('CREATE_RECONCILIATION_REQUIRED: reconcile the retained original task before a new create')
  const snapshot = validateCreate(input)
  createBusy = true
  createPending.value = true
  const current = generation, identityGeneration = authGeneration.value, unauthorizedGeneration = operatorUnauthorizedRevision.value
  try {
   const actor = await currentActor()
   if (current !== generation || identityGeneration !== authGeneration.value || unauthorizedGeneration !== operatorUnauthorizedRevision.value) throw new TypeError('CREATE_SOURCE_MISMATCH: project or authentication scope changed before dispatch')
   createScope(snapshot)
   if (storedCreate()) throw new TypeError('CREATE_RECONCILIATION_REQUIRED: an intent is already retained')
   const stored: StoredIssueCreate = { version: 1, actor, input: snapshot, resolution: 'pending' }
   persistCreate(stored)
   return await dispatchCreate(stored)
  } finally { createBusy = false; createPending.value = false }
 }
 async function reconcileCreate() {
  if (createBusy) throw new TypeError('CREATE_IN_PROGRESS: wait for the dispatched intent')
  const stored = storedCreate()
  if (!stored) throw new TypeError('CREATE_INTENT_UNAVAILABLE: no retained create can be reconciled')
  createBusy = true
  createPending.value = true
  const current = generation, identityGeneration = authGeneration.value, unauthorizedGeneration = operatorUnauthorizedRevision.value
  try {
   const actor = await currentActor()
   if (stored.actor.kind !== actor.kind || stored.actor.reference !== actor.reference) { pendingCreate.value = null; throw new TypeError('CREATE_ACTOR_MISMATCH: return to the original authenticated actor') }
   if (current !== generation || identityGeneration !== authGeneration.value || unauthorizedGeneration !== operatorUnauthorizedRevision.value) throw new TypeError('CREATE_SOURCE_MISMATCH: project or authentication scope changed before reconciliation')
   createScope(stored.input)
   persistCreate(stored)
   return await dispatchCreate(stored)
  } finally { createBusy = false; createPending.value = false }
 }
 async function finishRejectedCreate() {
  const stored = storedCreate()
  if (!stored || stored.resolution === 'pending') throw new TypeError('CREATE_RECONCILIATION_REQUIRED: an unknown create cannot be abandoned for a new key')
  const identityGeneration = authGeneration.value, unauthorizedGeneration = operatorUnauthorizedRevision.value
  const actor = await currentActor()
  if (identityGeneration !== authGeneration.value || unauthorizedGeneration !== operatorUnauthorizedRevision.value) throw new TypeError('CREATE_IDENTITY_RECHECK_REQUIRED: authenticate the original actor again before resolving its retained intent')
  if (stored.actor.kind !== actor.kind || stored.actor.reference !== actor.reference) throw new TypeError('CREATE_ACTOR_MISMATCH: return to the original authenticated actor')
  persistCreate(null)
 }

 async function updateIssue(id: number, input: IssueUpdateInput) {
  const source = humanSource(context.project.value)
  const material = ['title', 'body', 'priority', 'type', 'status', 'labels', 'metadata', 'rawMetadata', 'ownerLabel', 'recipientLabel', 'targetSession'].some((key) => Reflect.get(input, key) !== undefined)
  const base = input.expectedRevision ?? (detailStateValue.value?.id === id ? detailStateValue.value.revision : rowsState.value.find((row) => row.id === id)?.revision)
  const expected = material || input.expectedRevision !== undefined ? requiredRevision(base) : undefined
  if (input.rawMetadata !== undefined) {
   if (input.metadata !== undefined) throw new TypeError('INVALID_ARGUMENT: object and raw metadata are mutually exclusive')
   rawMetadata(input.rawMetadata)
  }
  if (input.commentRawMetadata !== undefined) {
   if (input.commentMetadata !== undefined) throw new TypeError('INVALID_ARGUMENT: object and raw comment metadata are mutually exclusive')
   if (input.comment === undefined) throw new TypeError('INVALID_ARGUMENT: comment metadata requires a comment')
   rawMetadata(input.commentRawMetadata)
  }
  if (input.metadata !== undefined) metadata(input.metadata, true)
  if (input.commentMetadata !== undefined) { metadata(input.commentMetadata, true); if (input.comment === undefined) throw new TypeError('INVALID_ARGUMENT: comment metadata requires a comment') }
  if (input.ownerLabel !== undefined) nullableLabel(input.ownerLabel)
  if (input.recipientLabel !== undefined) nullableLabel(input.recipientLabel)
  if (input.targetSession !== undefined) nullableLabel(input.targetSession)
  if (input.sessionId !== undefined && typeof input.sessionId !== 'string') throw new TypeError('INVALID_ARGUMENT: invalid session attribution')
  input = JSON.parse(JSON.stringify(input)) as IssueUpdateInput
  const request = { requestId: crypto.randomUUID(), action: 'issue-update', intent: { id, input } }
  const response = await issueMutation(request, `/api/issues/${id}`, jsonInit('PATCH', {
   ...(input.title === undefined ? {} : { title: input.title }),
   ...(input.body === undefined ? {} : { body: input.body }),
   ...(input.priority === undefined ? {} : { priority: input.priority }),
   ...(input.type === undefined ? {} : { type: input.type }),
   ...(input.status === undefined ? {} : { status: input.status }),
   ...(input.comment === undefined ? {} : { comment: input.comment }),
   ...(input.labels === undefined ? {} : { labels: input.labels }),
   ...(expected === undefined ? {} : { expected_revision: expected }),
   ...(input.metadata === undefined ? {} : { metadata: input.metadata }),
   ...(input.commentMetadata === undefined ? {} : { comment_metadata: input.commentMetadata }),
   ...(input.ownerLabel === undefined ? {} : { owner_label: input.ownerLabel }),
   ...(input.recipientLabel === undefined ? {} : { recipient_label: input.recipientLabel }),
   ...(input.targetSession === undefined ? {} : { target_session: input.targetSession }),
   ...(input.sessionId ? { session_id: input.sessionId } : {}),
   ...source,
  }, { metadata: input.rawMetadata, comment_metadata: input.commentRawMetadata }))
  const committedCommentId = object(response.body) && typeof response.body.comment_id === 'number' && Number.isSafeInteger(response.body.comment_id) && response.body.comment_id > 0 ? response.body.comment_id : undefined
  return verifyCurrentIssue(response.mutation, id, (issue, comments) => (input.title === undefined || issue.title === input.title)
   && (input.body === undefined || issue.body === input.body)
   && (input.priority === undefined || issue.priority === input.priority)
   && (input.type === undefined || issue.type === input.type)
   && (input.status === undefined || issue.status === input.status)
   && (input.labels === undefined || sameLabels(issue.labels, input.labels))
   && metadataReadbackMatches(issue.metadata, input.metadata, input.rawMetadata)
   && (input.ownerLabel === undefined || issue.ownerLabel === input.ownerLabel)
   && (input.recipientLabel === undefined || issue.recipientLabel === input.recipientLabel)
   && (input.targetSession === undefined || issue.targetSession === input.targetSession)
   && (input.comment === undefined || comments.some((comment) => (committedCommentId === undefined || comment.id === committedCommentId) && comment.body === input.comment && metadataReadbackMatches(comment.metadata, input.commentMetadata, input.commentRawMetadata))), committedCommentId)
 }

 async function commentIssue(id: number, body: string, commentMetadata?: IssueMetadata, commentRawMetadata?: string) {
  return updateIssue(id, { comment: body, ...(commentMetadata === undefined ? {} : { commentMetadata }), ...(commentRawMetadata === undefined ? {} : { commentRawMetadata }) })
 }

 async function rejectIssue(id: number, comment: string, expectedRevision?: string) {
  return updateIssue(id, { status: 'rejected', comment, ...(expectedRevision === undefined ? {} : { expectedRevision }) })
 }

 async function acknowledgeIssue(id: number, sourceProject: string) {
  const source = humanSource(sourceProject)
  const request = { requestId: crypto.randomUUID(), action: 'issue-acknowledge', intent: { id, sourceProject } }
  const response = await issueMutation(request, '/api/issues/acknowledge', jsonInit('POST', { ids: [id], ...source }))
  return verifyCurrentIssue(response.mutation, id, (issue) => issue.status === 'acknowledged')
 }

 async function saveIssueSelection(selection: Exclude<OperatorSelection, { kind: 'frozen_filter' }>): Promise<OperatorSelection> {
  const source = humanSource(context.project.value)
  const requestId = crypto.randomUUID()
  const response = await operatorFetchJson<SelectionEnvelope>('/api/issues/selection', selectionInit(requestId, {
   selection: issueSelectionPayload(selection),
   ...source,
  }), 'issues-selection-snapshot')
  return parseIssueSelection(response)
 }

 async function freezeIssueSelection(filter: IssueSelectionFilter, excludedIds: string[]): Promise<OperatorSelection> {
  const source = humanSource(context.project.value)
  filter = catalogFilter(filter, source.source_project)
  const requestId = crypto.randomUUID()
  const response = await operatorFetchJson<SelectionEnvelope>('/api/issues/selection', selectionInit(requestId, {
   selection: {
    kind: 'frozen_filter',
    filter: {
     ...(filter.project === undefined ? {} : { project: filter.project }),
     ...(filter.sourceProject === undefined ? {} : { source_project: filter.sourceProject }),
     ...(filter.statuses === undefined ? {} : { statuses: filter.statuses }),
     ...(filter.type === undefined ? {} : { type: filter.type }),
    },
    excluded_ids: excludedIds,
   },
   ...source,
  }), 'issues-selection-freeze')
  return parseIssueSelection(response)
 }

 async function currentIssueSelection(): Promise<OperatorSelection> {
  const source = humanSource(context.project.value)
  const requestId = crypto.randomUUID()
  const response = await operatorFetchJson<SelectionEnvelope>('/api/issues/selection/current', selectionInit(requestId, source), 'issues-selection-current')
  return parseIssueSelection(response)
 }

 async function loadIssuePage(filter: IssueSelectionFilter, cursor = ''): Promise<IssueSelectionPage> {
  const source = humanSource(context.project.value)
  filter = catalogFilter(filter, source.source_project)
  const requestId = crypto.randomUUID()
  const response = await operatorFetchJson<IssueSelectionPageEnvelope>('/api/issues/selection/page', selectionInit(requestId, {
   ...(cursor ? { cursor } : {
    filter: {
     ...(filter.project === undefined ? {} : { project: filter.project }),
     ...(filter.sourceProject === undefined ? {} : { source_project: filter.sourceProject }),
     ...(filter.statuses === undefined ? {} : { statuses: filter.statuses }),
     ...(filter.type === undefined ? {} : { type: filter.type }),
    }
   }),
   limit: 50,
   ...source,
  }), 'issues-selection-page')
  return parseIssueSelectionPage(response)
 }

 async function runIssueSelectionOperation(selection: Exclude<OperatorSelection, { kind: 'none' }>, action: IssueSelectionAction, sourceProject: string): Promise<MutationResult<{ selection: OperatorSelection; action: IssueSelectionAction; sourceProject: string }, IssueSelectionCurrent[]>> {
  const source = humanSource(sourceProject)
  if (selection.version < 1 || (selection.kind === 'frozen_filter' && selection.reconfirmationRequired)) throw new TypeError('Issues selection must be freshly confirmed')
  const requestId = crypto.randomUUID()
  const request = { requestId, action: `issues-${action.kind}`, intent: { selection, action, sourceProject } }
  const actionBody = action.kind === 'status'
   ? { status: action.status }
   : action.kind === 'priority'
    ? { priority: action.priority }
    : action.kind === 'labels'
     ? { labels: action.labels }
     : {}
  const selectionBody = selection.kind === 'frozen_filter'
   ? { kind: 'frozen_filter', selection_version: selection.version, selection_token: selection.selectionToken }
   : { kind: selection.kind, selection_version: selection.version }
  return executeMutation(request, operatorFetchResponse('/api/issues/operations', {
   ...selectionInit(requestId, { request_id: requestId, action: action.kind, selection: selectionBody, ...actionBody, ...source }),
   credentials: 'include',
  }), parseIssueSelectionCurrent)
 }

 async function runIssueSelection(ids: number[], action: IssueSelectionAction, sourceProject: string) {
  humanSource(sourceProject)
  const current = generation
  const targets = ids.map((id) => ({ id: String(id) }))
  const selection = await saveIssueSelection({ kind: 'explicit', domain: 'issues', version: 0, targets })
  if (current !== generation) throw new TypeError('Issue project scope changed before dispatch')
  if (selection.kind === 'none') throw new TypeError('Issues selection is empty')
  return runIssueSelectionOperation(selection, action, sourceProject)
 }

 async function deleteIssue(id: number) {
  const source = humanSource(context.project.value)
  const request = { requestId: crypto.randomUUID(), action: 'issue-delete', intent: { id, sourceProject: source.source_project } }
  const response = await issueMutation(request, `/api/issues/${id}?${new URLSearchParams(source)}`, jsonInit('DELETE'))
  return verifyDeletedIssue(response.mutation, id)
 }

 watch([context.project, context.selectionPending, context.selectionError], () => {
  detailGeneration += 1
  detailStateValue.value = null
  replaceArray(commentsState.value, [])
  replaceArray(history.value, [])
  commentsPage.value = null
  historyPage.value = null
  void refresh()
 }, { immediate: true, flush: 'sync' })
 if (options) watch(options, () => { void refresh() })
 onBeforeUnmount(() => { generation += 1; detailGeneration += 1 })

 return {
  rows: rowsState.value,
  comments: commentsState.value,
  history: history.value,
  commentsPage: computed(() => commentsPage.value),
  historyPage: computed(() => historyPage.value),
  pageLoading: computed(() => pageLoading.value),
  pageError: computed(() => pageError.value),
  loadMoreComments: () => loadMore('comments'),
  loadMoreHistory: () => loadMore('history'),
  pendingCreate: computed(() => pendingCreate.value),
  hasCreateIntent: computed(() => hasCreateIntent.value),
  createPending: computed(() => createPending.value),
  createRecoveryError: computed(() => createRecoveryError.value),
  createResolution: computed(() => createResolution.value),
  restoreCreateIntent,
  reconcileCreate,
  finishRejectedCreate,
  projectNames: projectNamesState.value,
  trackedProjects,
  registryTotal,
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
  saveIssueSelection,
  freezeIssueSelection,
  currentIssueSelection,
  loadIssuePage,
  runIssueSelectionOperation,
  runIssueSelection,
  deleteIssue,
 }
}
