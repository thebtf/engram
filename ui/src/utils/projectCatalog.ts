export interface ProjectDescriptor {
  project_id: string
  name: string
  repository_qualifier?: string
}

export interface UnresolvedProject {
  reference: string
  name: string
  reason: string
  reader_kinds: RetainedKind[]
}

export interface ProjectCatalog {
  items: ProjectDescriptor[]
  unresolved: UnresolvedProject[]
}

export type ProjectResolution =
  | { status: 'resolved'; project_id: string; name: string }
  | { status: 'unresolved'; reference: string; reason: string }

export function isProjectId(value: unknown): value is string {
  return typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value)
}

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid project response')
  return value as Record<string, unknown>
}

function text(value: unknown): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error('Invalid project response')
  return value
}

export function parseProjectCatalog(value: unknown): ProjectCatalog {
  const data = object(value)
  if (!Array.isArray(data.items) || !Array.isArray(data.unresolved)) throw new Error('Invalid project catalog')
  const ids = new Set<string>()
  const items = data.items.map(value => {
    const row = object(value)
    if (!isProjectId(row.project_id) || ids.has(row.project_id.toLowerCase())) throw new Error('Invalid project ID')
    ids.add(row.project_id.toLowerCase())
    return {
      project_id: row.project_id,
      name: text(row.name),
      ...(row.repository_qualifier === undefined ? {} : { repository_qualifier: text(row.repository_qualifier) }),
    }
  })
  const references = new Set<string>()
  const unresolved = data.unresolved.map(value => {
    const row = object(value)
    const reference = text(row.reference)
    if (references.has(reference)) throw new Error('Duplicate unresolved reference')
    references.add(reference)
    if (!Array.isArray(row.reader_kinds)) throw new Error('Missing retained reader kinds')
    const reader_kinds = row.reader_kinds.map((kind: unknown): RetainedKind => {
      if (kind === 'memory' || kind === 'document' || kind === 'issue' || kind === 'code') return kind
      throw new Error('Invalid retained reader kind')
    })
    if (new Set(reader_kinds).size !== reader_kinds.length) throw new Error('Duplicate retained reader kind')
    return { reference, name: text(row.name), reason: text(row.reason), reader_kinds }
  })
  return { items, unresolved }
}

export function parseProjectResolution(value: unknown): ProjectResolution {
  const row = object(value)
  if (row.status === 'resolved' && isProjectId(row.project_id)) {
    return { status: 'resolved', project_id: row.project_id, name: text(row.name) }
  }
  if (row.status === 'unresolved') {
    return { status: 'unresolved', reference: text(row.reference), reason: text(row.reason) }
  }
  throw new Error('Invalid project resolution')
}

export class UnresolvedProjectError extends Error {
  readonly reference: string
  constructor(reference: string, reason: string) {
    super(reason)
    this.name = 'UnresolvedProjectError'
    this.reference = reference
  }
}

export function projectLabel(items: ProjectDescriptor[], id: string, unknownLabel: string, _ambiguousLabel: string): string {
  const project = items.find(item => item.project_id === id)
  if (!project) return unknownLabel
  const baseLabel = (item: ProjectDescriptor) => item.repository_qualifier ? `${item.name} · ${item.repository_qualifier}` : item.name
  const label = baseLabel(project)
  let count = 0
  let ordinal = 1
  for (const item of items) {
    if (baseLabel(item) !== label) continue
    count++
    if (item.project_id.toLowerCase() < project.project_id.toLowerCase()) ordinal++
  }
  return count > 1 ? `${label} #${ordinal}` : label
}

export interface ReaderPage<T> {
  items: T[]
  total: number
  offset: number
  limit: number
  has_more: boolean
}

export interface MemoryReaderQuery {
  project: string
  query?: string
  filters?: Array<'all' | 'active' | 'flagged' | 'stale' | 'low' | 'chain'>
  offset?: number
  limit?: number
}

export function parseReaderPage<T extends { project: string }>(page: ReaderPage<T>, project: string, offset: number, limit: number): ReaderPage<T> {
  if (!page || !Array.isArray(page.items) || !Number.isSafeInteger(page.total) || page.total < 0 || page.offset !== offset || page.limit !== limit
    || typeof page.has_more !== 'boolean' || page.items.length > limit || page.items.length > Math.max(0, page.total - offset)) throw new Error('Invalid reader page')
  if (page.has_more !== (offset < page.total && page.items.length < page.total - offset) || (page.has_more && !page.items.length)) throw new Error('Invalid reader continuation')
  if (page.items.some(item => !item || item.project !== project)) throw new Error('Reader page scope mismatch')
  return page
}

export interface CredentialBinding {
  id: number
  name: string
  scope: 'project' | 'global'
  project_id: string | null
  unresolved_reference?: string
  binding_error?: string
  origin_project?: string
}

export function credentialBinding(credential: { id: number; name: string; scope: string; project?: string; project_id?: string | null; unresolved_reference?: string; binding_error?: string; origin_project?: string }): CredentialBinding {
  if (!Number.isSafeInteger(credential.id) || credential.id <= 0 || typeof credential.name !== 'string' || !credential.name
    || (credential.origin_project !== undefined && typeof credential.origin_project !== 'string')) throw new Error('Invalid credential metadata')
  if (credential.binding_error !== undefined) {
    if ((credential.binding_error !== 'CREDENTIAL_BINDING_UNAVAILABLE' && credential.binding_error !== 'CREDENTIAL_BINDING_CONFLICT')
      || credential.project_id !== null || credential.project || credential.unresolved_reference !== undefined
      || (credential.scope !== 'project' && credential.scope !== 'global')) throw new Error('Invalid unavailable credential metadata')
    return { id: credential.id, name: credential.name, scope: credential.scope, project_id: null, binding_error: credential.binding_error, origin_project: credential.origin_project }
  }
  if (credential.unresolved_reference !== undefined) {
    if (credential.scope !== 'project' || credential.project_id !== null || credential.project
      || typeof credential.unresolved_reference !== 'string' || !/^urn:engram:migration:unresolved:[0-9a-f]{64}$/.test(credential.unresolved_reference)) throw new Error('Invalid retained credential metadata')
    return { id: credential.id, name: credential.name, scope: 'project', project_id: null, unresolved_reference: credential.unresolved_reference, origin_project: credential.origin_project }
  }
  const project = credential.project_id === undefined ? credential.project || null : credential.project_id
  if (credential.scope === 'global' && project === null && !credential.project) return { id: credential.id, name: credential.name, scope: 'global', project_id: null }
  if (credential.scope === 'project' && isProjectId(project) && (!credential.project || credential.project === project)) return { id: credential.id, name: credential.name, scope: 'project', project_id: project }
  throw new Error('Credential requires an exact project or explicit global scope')
}

export function assertCredentialMetadata(value: unknown, expected: CredentialBinding): void {
  const metadata = object(value)
  if (metadata.binding_error !== expected.binding_error) throw new Error('Credential metadata scope mismatch')
  if (metadata.unresolved_reference !== expected.unresolved_reference) throw new Error('Credential metadata scope mismatch')
  for (const key of ['id', 'name', 'scope', 'project_id'] as const) {
    if ((expected.unresolved_reference !== undefined || metadata[key] !== undefined) && metadata[key] !== expected[key]) throw new Error('Credential metadata scope mismatch')
  }
  if (metadata.project !== undefined && metadata.project !== (expected.project_id ?? '')) throw new Error('Credential metadata scope mismatch')
}

export type RetainedKind = 'memory' | 'document' | 'issue' | 'code'
export interface RetainedItem {
  id: string | number
  title: string
  content: string
  date: string
  fields: Array<{ label: string; value: string }>
}
export interface RetainedPage {
  items: RetainedItem[]
  next_cursor: string | null
  provenance?: unknown
}

export function parseRetainedPage(value: unknown, reference: string, kind: RetainedKind, id?: string | number): RetainedPage {
  const data = object(value)
  if (kind === 'code') {
    if (id !== undefined || data.reference !== reference || !Array.isArray(data.items) || data.items.length > 50
      || (data.next_cursor !== null && (typeof data.next_cursor !== 'string' || !data.next_cursor)) || (data.next_cursor && !data.items.length)) throw new Error('Invalid retained code page')
    const items = data.items.map(value => {
      const row = object(value)
      if (row.project_id !== null || row.unresolved_reference !== reference) throw new Error('Retained code membership mismatch')
      const snapshot = row.indexed_snapshot === undefined ? {} : object(row.indexed_snapshot)
      const fields = ['source_ref', 'checkout_ref', 'view_ref', 'repository', 'working_copy'].flatMap(label => typeof row[label] === 'string' ? [{ label, value: row[label] }] : [])
      fields.push(...['label', 'revision', 'published_at'].flatMap(label => typeof snapshot[label] === 'string' ? [{ label: `indexed_snapshot.${label}`, value: snapshot[label] }] : []))
      return { id: text(row.view_ref ?? row.checkout_ref ?? row.source_ref), title: text(row.repository), content: typeof row.working_copy === 'string' ? row.working_copy : '', date: '', fields }
    })
    return { items, next_cursor: data.next_cursor }
  }
  if (data.reference !== reference || data.kind !== kind || (id !== undefined && String(data.id) !== String(id)) || !Array.isArray(data.items) || data.items.length > 50
    || (data.next_cursor !== null && (typeof data.next_cursor !== 'string' || !data.next_cursor))) throw new Error('Invalid retained content page')
  if (data.next_cursor && data.items.length === 0) throw new Error('Empty retained continuation')
  const items = data.items.map(value => {
    const row = object(value)
    const recordId = row.id ?? (id !== undefined ? row.version : undefined)
    if ((typeof recordId !== 'string' || !recordId.trim()) && (typeof recordId !== 'number' || !Number.isSafeInteger(recordId) || recordId <= 0)) throw new Error('Invalid retained content ID')
    const fields = ['version', 'action', 'actor', 'author', 'author_agent', 'author_project', 'reason', 'status', 'priority', 'type', 'doc_type', 'epistemic_type', 'tier', 'tags', 'labels', 'confidence', 'created_at', 'updated_at', 'source_agent', 'source_sessions', 'project', 'source_project', 'target_project', 'content_hash', 'memory_id', 'document_id', 'issue_id', 'before_state_present', 'after_state_present'].flatMap(label => {
      const value = row[label]
      if (typeof value === 'string' || typeof value === 'boolean' || (typeof value === 'number' && Number.isFinite(value))) return [{ label, value: String(value) }]
      if (Array.isArray(value)) return [{ label, value: value.filter(item => typeof item === 'string' || typeof item === 'number').join(' · ') }]
      return []
    })
    for (const key of ['before_state', 'after_state']) {
      const value = row[key]
      if (typeof value === 'string') fields.push({ label: key, value })
      else if (value && typeof value === 'object' && !Array.isArray(value)) {
        for (const [label, field] of Object.entries(value)) {
          if (['content', 'status', 'version', 'reason'].includes(label) && (typeof field === 'string' || typeof field === 'number')) fields.push({ label: `${key}.${label}`, value: String(field) })
        }
      }
    }
    const title = typeof row.title === 'string' ? row.title : typeof row.path === 'string' ? row.path : typeof row.action === 'string' ? row.action : ''
    const content = typeof row.content === 'string' ? row.content : typeof row.body === 'string' ? row.body : ''
    if (!title && !content && !fields.length) throw new Error('Unreadable retained content')
    return {
      id: recordId,
      title,
      content,
      fields,
      date: typeof row.updated_at === 'string' ? row.updated_at : typeof row.created_at === 'string' ? row.created_at : '',
    }
  })
  return { items, next_cursor: data.next_cursor, ...(id === undefined ? { provenance: data.provenance } : {}) }
}
