import { computed, ref as sharedRef, watch, onBeforeUnmount, type Ref as SharedRef } from 'vue'
import { isCatalogProject, projectName, useOperatorProjectContext } from './useOperatorProjectContext'

let sharedIssuesPageRows: SharedRef<OperatorIssue[]> | undefined
let sharedIssuesPageComments: SharedRef<OperatorIssueComment[]> | undefined
let sharedIssuesPageProjectNames: SharedRef<Record<string, string>> | undefined
let sharedIssuesPageTotal: SharedRef<number> | undefined
let sharedIssuesPageDetail: SharedRef<OperatorIssue | null> | undefined
let sharedIssuesPageState: SharedRef<OperatorLoadState<OperatorIssue[]>> | undefined
let sharedIssuesPageDetailState: SharedRef<OperatorLoadState<OperatorIssue | null>> | undefined

import type { ComputedRef } from 'vue'
import type { OperatorSelection, OperatorSelectionTarget } from './useOperatorSelection'
import { parseOperatorSelectionSnapshot } from './useOperatorSelection'
import type { OperatorLoadState } from './useOperatorApi'
import { executeMutation, type MutationCurrentStateParser, type MutationResult } from './useApi'
import {
 emptyState,
 endpointEvidence,
 errorState,
 gatedState,
 liveState,
 loadOperatorJson,
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
 sourceProject: string
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
 return { mutation: await executeMutation<TIntent, OperatorIssue>(request, Promise.resolve(response), () => undefined), body }
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


function mapIssue(
 row: ApiIssueRow,
 commentCount = row.comment_count,
): OperatorIssue {
 const sourceProject = row.source_project || ''
 const targetProject = row.target_project || ''
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
  sourceDisplayName: isCatalogProject(sourceProject) ? projectName(sourceProject) : '',
  targetDisplayName: isCatalogProject(targetProject) ? projectName(targetProject) : '',
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
  createdAt,
  age: compactAge(createdAt),
 }
}
async function verifyCurrentIssue<TIntent>(
 mutation: MutationResult<TIntent, OperatorIssue>,
 id: number,
 matches: (issue: OperatorIssue, comments: OperatorIssueComment[]) => boolean,
): Promise<MutationResult<TIntent, OperatorIssue>> {
 if (mutation.kind !== 'committed_verification_pending' || mutation.commitment !== 'committed') return mutation

 let issue: OperatorIssue | null = null
 let comments: OperatorIssueComment[] = []
 try {
  const payload = await operatorFetchJson<ApiIssueDetail>(`/api/issues/${id}`, undefined, 'issues-mutation-readback')
  issue = payload.issue ? mapIssue(payload.issue, payload.comment_count ?? payload.comments?.length) : null
  comments = (payload.comments || []).map(mapComment)
 } catch {
  issue = null
 }
 if (!issue || !matches(issue, comments)) return { ...mutation, reason: 'readback_invalid' }

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
  const response = await operatorFetchResponse(`/api/issues/${id}`, { credentials: 'include' })
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

function sortIssues(rows: OperatorIssue[]) {
 const priorityRank: Record<OperatorIssuePriority, number> = { critical: 0, high: 1, medium: 2, low: 3 }
 return [...rows].sort((left, right) => {
  const rank = priorityRank[left.priority] - priorityRank[right.priority]
  if (rank !== 0) return rank
  return right.id - left.id
 })
}


export function useOperatorIssues(): {
 rows: OperatorIssue[]
 comments: OperatorIssueComment[]
 projectNames: Record<string, string>
 trackedProjects: ComputedRef<string[]>
 registryTotal: ComputedRef<number>
 detail: ComputedRef<OperatorIssue | null>
 loadState: ComputedRef<OperatorLoadState<OperatorIssue[]>>
 detailState: ComputedRef<OperatorLoadState<OperatorIssue | null>>
 pending: ComputedRef<boolean>
 error: ComputedRef<string | null>
 routeChangeAction: OperatorUnsupportedAction
 refresh: () => Promise<void>
 openIssue: (id: number) => Promise<void>
 createIssue: (input: IssueCreateInput) => Promise<MutationResult<IssueCreateInput, OperatorIssue>>
 updateIssue: (id: number, input: IssueUpdateInput) => Promise<MutationResult<{ id: number; input: IssueUpdateInput }, OperatorIssue>>
 commentIssue: (id: number, body: string) => Promise<MutationResult<{ id: number; input: IssueUpdateInput }, OperatorIssue>>
 rejectIssue: (id: number, comment: string) => Promise<MutationResult<{ id: number; input: IssueUpdateInput }, OperatorIssue>>
 acknowledgeIssue: (id: number, sourceProject: string) => Promise<MutationResult<{ id: number; sourceProject: string }, OperatorIssue>>
 saveIssueSelection: (selection: Exclude<OperatorSelection, { kind: 'frozen_filter' }>) => Promise<OperatorSelection>
 freezeIssueSelection: (filter: IssueSelectionFilter, excludedIds: string[]) => Promise<OperatorSelection>
 currentIssueSelection: () => Promise<OperatorSelection>
 loadIssuePage: (filter: IssueSelectionFilter, cursor?: string) => Promise<IssueSelectionPage>
 runIssueSelectionOperation: (selection: Exclude<OperatorSelection, { kind: 'none' }>, action: IssueSelectionAction, sourceProject: string) => Promise<MutationResult<{ selection: OperatorSelection; action: IssueSelectionAction; sourceProject: string }, IssueSelectionCurrent[]>>
 runIssueSelection: (ids: number[], action: IssueSelectionAction, sourceProject: string) => Promise<MutationResult<{ selection: OperatorSelection; action: IssueSelectionAction; sourceProject: string }, IssueSelectionCurrent[]>>
 deleteIssue: (id: number) => Promise<MutationResult<{ id: number; sourceProject: string }>>
} {
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

 function catalogFilter(filter: IssueSelectionFilter): IssueSelectionFilter {
  const scope = filter.project ?? context.project.value
  if (context.selectionPending.value || context.selectionError.value || context.error.value || !scope || (scope !== 'all' && !isCatalogProject(scope)) || (filter.sourceProject !== undefined && !isCatalogProject(filter.sourceProject))) throw new TypeError('A catalog issue project is required')
  return { ...filter, project: scope === 'all' ? undefined : scope }
 }

 function humanSource(sourceProject: string) {
  if (context.selectionPending.value || context.selectionError.value || context.error.value || !isCatalogProject(sourceProject) || sourceProject !== context.project.value) throw new TypeError('Choose a catalog source project before changing issues')
  return { source_project: sourceProject, source_agent: 'human' as const }
 }

 async function refresh() {
  const current = ++generation
  const scope = context.project.value
  replaceArray(rowsState.value, [])
  totalCountState.value = 0
  state.value = pendingState(listEvidence, [])
  if (context.selectionPending.value || context.selectionError.value || context.error.value || !scope || (scope !== 'all' && !isCatalogProject(scope))) { state.value = gatedState(listEvidence, 'project-selection', 'Choose a catalog project before reading issues.', []); return }
  const params = new URLSearchParams({ limit: '200' })
  if (scope !== 'all') params.set('project', scope)
  const result = await loadOperatorJson<ApiIssueList>(`/api/issues?${params}`, {
   source: 'issues-list',
   empty: (payload) => !(payload.issues || []).length,
  })
  if (current !== generation || scope !== context.project.value) return

  if (result.kind === 'live' || result.kind === 'empty') {
   replaceRecord(projectNamesState.value, Object.fromEntries(context.projects.value.map((id) => [id, projectName(id)])))
   const nextRows = sortIssues((result.data.issues || []).map((row) => mapIssue(row, row.comment_count)))
   replaceArray(rowsState.value, nextRows)
   totalCountState.value = typeof result.data.total === 'number' ? result.data.total : nextRows.length
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
  const endpoint = `/api/issues/${id}${scope === 'all' ? '' : `?project=${encodeURIComponent(scope)}`}`
  const evidence = endpointEvidence(endpoint, 'issues-detail')
  detailLoadState.value = pendingState(evidence, detailStateValue.value)
  replaceArray(commentsState.value, [])
  try {
   const payload = await operatorFetchJson<ApiIssueDetail>(endpoint, undefined, 'issues-detail')
   if (current !== detailGeneration || scope !== context.project.value) return
   const next = payload.issue
    ? mapIssue({
     ...payload.issue,
     source_project_display_name: payload.source_project_display_name,
     target_project_display_name: payload.target_project_display_name,
    }, payload.comment_count ?? payload.comments?.length)
    : null
   if (next && (next.id !== id || (scope !== 'all' && next.sourceProject !== scope && next.targetProject !== scope))) throw new TypeError('Issue detail identity or project mismatch')
   detailStateValue.value = next
   replaceArray(commentsState.value, (payload.comments || []).map((row) => mapComment(row)))
   detailLoadState.value = next ? liveState(evidence, next) : emptyState(evidence, null)
  } catch (nextError) {
   if (current !== detailGeneration || scope !== context.project.value) return
   const mapped = toOperatorSourceError(nextError, {
    source: 'issues-detail',
    path: endpoint,
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
  const sourceProject = input.sourceProject
  if (context.selectionPending.value || context.selectionError.value || context.error.value || !isCatalogProject(sourceProject) || !isCatalogProject(input.targetProject) || (context.project.value !== 'all' && sourceProject !== context.project.value)) throw new TypeError('Explicit catalog source and target projects are required')
  const request = { requestId: crypto.randomUUID(), action: 'issue-create', intent: input }
  const response = await issueMutation(request, '/api/issues', jsonInit('POST', {
   title: input.title,
   body: input.body || '',
   priority: input.priority,
   type: input.type,
   source_project: sourceProject,
   target_project: input.targetProject,
   source_agent: 'human',
   labels: input.labels || [],
  }))
  const id = typeof response.body === 'object' && response.body !== null && !Array.isArray(response.body)
   ? Reflect.get(response.body, 'id')
   : undefined
  if (typeof id !== 'number' || !Number.isSafeInteger(id) || id <= 0) return response.mutation
  return verifyCurrentIssue(response.mutation, id, (issue) => issue.title === input.title
   && issue.body === (input.body || '')
   && issue.priority === input.priority
   && issue.type === input.type
   && issue.sourceProject === sourceProject
   && issue.targetProject === input.targetProject
   && issue.labels.length === (input.labels || []).length
   && issue.labels.every((label, index) => label === (input.labels || [])[index]))
 }

 async function updateIssue(id: number, input: IssueUpdateInput) {
  const source = humanSource(context.project.value)
  const request = { requestId: crypto.randomUUID(), action: 'issue-update', intent: { id, input } }
  const response = await issueMutation(request, `/api/issues/${id}`, jsonInit('PATCH', {
   ...(input.title === undefined ? {} : { title: input.title }),
   ...(input.body === undefined ? {} : { body: input.body }),
   ...(input.priority === undefined ? {} : { priority: input.priority }),
   ...(input.type === undefined ? {} : { type: input.type }),
   ...(input.status === undefined ? {} : { status: input.status }),
   ...(input.comment === undefined ? {} : { comment: input.comment }),
   ...(input.labels === undefined ? {} : { labels: input.labels }),
   ...source,
  }))
  return verifyCurrentIssue(response.mutation, id, (issue, comments) => (input.title === undefined || issue.title === input.title)
   && (input.body === undefined || issue.body === input.body)
   && (input.priority === undefined || issue.priority === input.priority)
   && (input.type === undefined || issue.type === input.type)
   && (input.status === undefined || issue.status === input.status)
   && (input.labels === undefined || (issue.labels.length === input.labels.length && issue.labels.every((label, index) => label === input.labels![index])))
   && (input.comment === undefined || comments.some((comment) => comment.body === input.comment)))
 }

 async function commentIssue(id: number, body: string) {
  return updateIssue(id, { comment: body })
 }

 async function rejectIssue(id: number, comment: string) {
  return updateIssue(id, { status: 'rejected', comment })
 }

 async function acknowledgeIssue(id: number, sourceProject: string) {
  const source = humanSource(sourceProject)
  const request = { requestId: crypto.randomUUID(), action: 'issue-acknowledge', intent: { id, sourceProject } }
  const response = await issueMutation(request, '/api/issues/acknowledge', jsonInit('POST', { ids: [id], ...source }))
  return verifyCurrentIssue(response.mutation, id, (issue) => issue.status === 'acknowledged')
 }

 async function saveIssueSelection(selection: Exclude<OperatorSelection, { kind: 'frozen_filter' }>): Promise<OperatorSelection> {
  const requestId = crypto.randomUUID()
  const response = await operatorFetchJson<SelectionEnvelope>('/api/issues/selection', selectionInit(requestId, {
   selection: issueSelectionPayload(selection),
  }), 'issues-selection-snapshot')
  return parseIssueSelection(response)
 }

 async function freezeIssueSelection(filter: IssueSelectionFilter, excludedIds: string[]): Promise<OperatorSelection> {
  filter = catalogFilter(filter)
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
  }), 'issues-selection-freeze')
  return parseIssueSelection(response)
 }

 async function currentIssueSelection(): Promise<OperatorSelection> {
  const requestId = crypto.randomUUID()
  const response = await operatorFetchJson<SelectionEnvelope>('/api/issues/selection/current', selectionInit(requestId, {}), 'issues-selection-current')
  return parseIssueSelection(response)
 }

 async function loadIssuePage(filter: IssueSelectionFilter, cursor = ''): Promise<IssueSelectionPage> {
  filter = catalogFilter(filter)
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
  const response = await issueMutation(request, `/api/issues/${id}`, jsonInit('DELETE'))
  return verifyDeletedIssue(response.mutation, id)
 }

 watch([context.project, context.selectionPending, context.selectionError], () => {
  detailGeneration += 1
  detailStateValue.value = null
  replaceArray(commentsState.value, [])
  void refresh()
 }, { immediate: true, flush: 'sync' })
 onBeforeUnmount(() => { generation += 1; detailGeneration += 1 })

 return {
  rows: rowsState.value,
  comments: commentsState.value,
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
