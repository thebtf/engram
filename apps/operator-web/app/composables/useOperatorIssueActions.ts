import {
  IssueApiError,
  assertIssueRevision,
  issueApiRequest,
  prepareIssueMetadata,
  prepareIssueMetadataRaw,
  resolveProject,
  type Issue,
  type IssueMetadata,
  type IssueRevision,
  type IssueUpdateResponse,
} from '~/composables/useOperatorApi'

export type IssueStatus = Issue['status']
export type IssuePriority = Issue['priority']
export type IssueType = Issue['type']

export interface IssueUpdateFields {
  expected_revision?: IssueRevision
  status?: IssueStatus
  comment?: string
  source_project?: string
  source_agent?: string
  session_id?: string
  title?: string
  body?: string
  priority?: IssuePriority
  type?: IssueType
  labels?: readonly string[]
  metadata?: IssueMetadata
  metadataRaw?: string
  comment_metadata?: IssueMetadata
  commentMetadataRaw?: string
  owner_label?: string | null
  recipient_label?: string | null
}


export type IssueUpdateInput = IssueUpdateFields & { source_project: string; source_agent: string }

const materialIssueFields = ['title', 'body', 'priority', 'type', 'labels', 'metadata', 'metadataRaw', 'owner_label', 'recipient_label', 'status'] as const

export async function updateIssue(id: number, fields: IssueUpdateInput): Promise<IssueUpdateResponse> {
  if (!Number.isSafeInteger(id) || id <= 0) throw new IssueApiError('INVALID_ARGUMENT')
  if (typeof fields.source_project !== 'string' || typeof fields.source_agent !== 'string') throw new IssueApiError('INVALID_ARGUMENT')
  for (const name of ['title', 'body', 'priority', 'type', 'status', 'session_id'] as const) {
    if (fields[name] !== undefined && typeof fields[name] !== 'string') throw new IssueApiError('INVALID_ARGUMENT')
  }
  for (const name of ['owner_label', 'recipient_label'] as const) {
    if (fields[name] !== undefined && fields[name] !== null && (typeof fields[name] !== 'string' || fields[name] === '')) throw new IssueApiError('INVALID_ARGUMENT')
  }
  if (fields.labels !== undefined && (!Array.isArray(fields.labels) || fields.labels.some(label => typeof label !== 'string'))) throw new IssueApiError('INVALID_ARGUMENT')
  const material = materialIssueFields.some(name => fields[name] !== undefined)
  if (material && fields.expected_revision === undefined) throw new IssueApiError('REVISION_REQUIRED')
  if (fields.expected_revision !== undefined) assertIssueRevision(fields.expected_revision)
  if ((!material && fields.comment === undefined) || ((fields.comment_metadata !== undefined || fields.commentMetadataRaw !== undefined) && fields.comment === undefined)
    || (fields.comment !== undefined && (typeof fields.comment !== 'string' || fields.comment.trim() === ''))) {
    throw new IssueApiError('INVALID_ARGUMENT')
  }
  if ((fields.metadataRaw !== undefined && fields.metadata !== undefined)
    || (fields.commentMetadataRaw !== undefined && fields.comment_metadata !== undefined)) throw new IssueApiError('INVALID_ARGUMENT')
  const snapshot = { ...fields }
  if (fields.labels !== undefined) snapshot.labels = Object.freeze([...fields.labels])
  if (fields.metadata !== undefined) snapshot.metadata = prepareIssueMetadata(fields.metadata)
  if (fields.comment_metadata !== undefined) snapshot.comment_metadata = prepareIssueMetadata(fields.comment_metadata)
  if (fields.metadataRaw !== undefined) snapshot.metadataRaw = prepareIssueMetadataRaw(fields.metadataRaw)
  if (fields.commentMetadataRaw !== undefined) snapshot.commentMetadataRaw = prepareIssueMetadataRaw(fields.commentMetadataRaw)
  const source_project = await resolveProject(snapshot.source_project)
  const response = await issueApiRequest<IssueUpdateResponse>(`/issues/${id}`, 'PATCH', { ...snapshot, source_project })
  try {
    assertIssueRevision(response?.revision)
    if (response.id !== id || typeof response.changed !== 'boolean' || (!material && response.changed)
      || (response.comment_id !== null && (!Number.isSafeInteger(response.comment_id) || response.comment_id <= 0))) {
      throw new IssueApiError('INVALID_RESPONSE', 'unknown')
    }
  } catch {
    throw new IssueApiError('INVALID_RESPONSE', 'unknown')
  }
  return response
}

export async function deleteIssue(id: number, sourceProject: string): Promise<void> {
  const source_project = await resolveProject(sourceProject)
  const params = new URLSearchParams({ source_project, source_agent: 'human' })
  await issueApiRequest<Record<string, unknown>>(`/issues/${id}?${params}`, 'DELETE')
}

export async function acknowledgeIssues(ids: number[], sourceProject: string): Promise<{ acknowledged: number }> {
  const source_project = await resolveProject(sourceProject)
  return issueApiRequest<{ acknowledged: number }>('/issues/acknowledge', 'POST', { ids: [...ids], source_project, source_agent: 'human' })
}
