import { computed, onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { operatorFetchJson, toOperatorSourceError, type OperatorSourceError } from './useOperatorApi'
import { operatorProjectReturnTo, type OperatorUnresolvedProject } from './useOperatorProjectContext'

export type UnresolvedKnowledgeKind = 'memory' | 'document' | 'issue'
export interface UnresolvedKnowledgeItem {
  id: string
  title: string
  content: string
  fields: Array<{ label: string; value: string }>
}
export interface UnresolvedContentPage {
  reference: string
  kind: UnresolvedKnowledgeKind | 'code'
  items: UnresolvedKnowledgeItem[]
  nextCursor: string | null
  provenance: Array<{ label: string; value: string }>
}
export interface UnresolvedHistoryPage {
  reference: string
  kind: UnresolvedKnowledgeKind
  id: string
  items: UnresolvedKnowledgeItem[]
  nextCursor: string | null
}

function object(value: unknown): value is Record<string, unknown> { return typeof value === 'object' && value !== null && !Array.isArray(value) }
export function parseUnresolvedKind(value: unknown): UnresolvedKnowledgeKind {
  if (value === 'memory' || value === 'document' || value === 'issue') return value
  throw new TypeError('Invalid unresolved knowledge kind')
}
function scalar(value: unknown): string {
  if (typeof value === 'string' || typeof value === 'boolean' || (typeof value === 'number' && Number.isFinite(value))) return String(value)
  return ''
}
function contentID(value: unknown): string {
  if ((typeof value === 'string' && value.trim()) || (typeof value === 'number' && Number.isSafeInteger(value) && value > 0)) return String(value)
  throw new TypeError('Invalid retained content identity')
}
function readableFields(value: Record<string, unknown>, keys: string[]) {
  return keys.flatMap((label) => {
    const raw = value[label]
    const text = Array.isArray(raw) ? raw.map(scalar).filter(Boolean).join(' · ') : scalar(raw)
    return text ? [{ label, value: text }] : []
  })
}
function retainedItem(value: unknown, kind: UnresolvedKnowledgeKind, history: boolean): UnresolvedKnowledgeItem {
  if (!object(value)) throw new TypeError('Invalid retained knowledge record')
  const id = contentID(value.id ?? (history ? value.version : undefined))
  const title = kind === 'document' ? scalar(value.path) : kind === 'issue' ? scalar(value.title) : ''
  const content = scalar(value.content ?? value.body)
  if (!history && !content && !title) throw new TypeError('Retained knowledge record has no readable content')
  const fields = readableFields(value, ['version', 'action', 'actor', 'author', 'author_agent', 'author_project', 'reason', 'status', 'priority', 'type', 'doc_type', 'epistemic_type', 'tier', 'tags', 'labels', 'confidence', 'created_at', 'updated_at', 'source_agent', 'source_sessions', 'project', 'source_project', 'target_project', 'content_hash', 'memory_id', 'document_id', 'issue_id', 'before_state_present', 'after_state_present'])
  for (const key of ['before_state', 'after_state']) {
    const state = value[key]
    if (object(state)) fields.push(...readableFields(state, ['content', 'status', 'version', 'reason']).map((field) => ({ ...field, label: `${key}.${field.label}` })))
    else if (typeof state === 'string' && state) fields.push({ label: key, value: state })
  }
  if (history && !fields.length && !content && !title) throw new TypeError('Retained history record has no readable fields')
  return { id, title: title || scalar(value.action) || id, content, fields }
}
function continuation(value: unknown): string | null {
  if (value === null || (typeof value === 'string' && value)) return value
  throw new TypeError('Invalid unresolved continuation')
}
function provenanceFields(value: unknown, prefix = ''): Array<{ label: string; value: string }> {
  if (Array.isArray(value)) return value.flatMap((entry, index) => provenanceFields(entry, `${prefix}${prefix ? '.' : ''}${index + 1}`))
  if (object(value)) return Object.entries(value).flatMap(([key, entry]) => provenanceFields(entry, `${prefix}${prefix ? '.' : ''}${key}`))
  const text = scalar(value)
  return text ? [{ label: prefix || 'source', value: text }] : []
}
export function parseUnresolvedContent(payload: unknown, reference: string, kind: UnresolvedKnowledgeKind, limit: number): UnresolvedContentPage {
  if (!object(payload) || payload.reference !== reference || payload.kind !== kind || !Array.isArray(payload.items) || payload.items.length > limit || !Object.hasOwn(payload, 'provenance') || payload.provenance === undefined) throw new TypeError('Unresolved content scope mismatch')
  const nextCursor = continuation(payload.next_cursor)
  if (nextCursor && !payload.items.length) throw new TypeError('Empty unresolved continuation')
  return { reference, kind, items: payload.items.map((item: unknown) => retainedItem(item, kind, false)), nextCursor, provenance: provenanceFields(payload.provenance) }
}
export function parseUnresolvedHistory(payload: unknown, reference: string, kind: UnresolvedKnowledgeKind, id: string, limit: number): UnresolvedHistoryPage {
  if (!object(payload) || payload.reference !== reference || payload.kind !== kind || contentID(payload.id) !== id || !Array.isArray(payload.items) || payload.items.length > limit) throw new TypeError('Unresolved history scope mismatch')
  const nextCursor = continuation(payload.next_cursor)
  if (nextCursor && !payload.items.length) throw new TypeError('Empty history continuation')
  return { reference, kind, id, items: payload.items.map((item: unknown) => retainedItem(item, kind, true)), nextCursor }
}

export function parseUnresolvedCode(payload: unknown, reference: string, limit: number): UnresolvedContentPage {
  if (!object(payload) || payload.reference !== reference || !Array.isArray(payload.items) || payload.items.length > limit) throw new TypeError('Retained code scope mismatch')
  const nextCursor = continuation(payload.next_cursor)
  if (nextCursor && !payload.items.length) throw new TypeError('Empty retained code continuation')
  const items = payload.items.map((value: unknown): UnresolvedKnowledgeItem => {
    if (!object(value) || value.project_id !== null || value.unresolved_reference !== reference) throw new TypeError('Retained code membership mismatch')
    const id = contentID(value.view_ref ?? value.view_id ?? value.checkout_ref ?? value.checkout_id ?? value.source_ref ?? value.source_id)
    const title = scalar(value.repository ?? value.source_name ?? value.name)
    const snapshot = object(value.indexed_snapshot) ? value.indexed_snapshot : {}
    const content = [scalar(value.working_copy ?? value.checkout_label), scalar(snapshot.label)].filter(Boolean).join('\n')
    if (!title && !content) throw new TypeError('Retained code metadata has no readable description')
    const fields = readableFields(value, ['source_ref', 'source_id', 'checkout_ref', 'checkout_id', 'view_ref', 'view_id', 'snapshot_ref', 'snapshot_id', 'repository', 'working_copy', 'checkout_label', 'created_at', 'updated_at', 'unresolved_reference'])
    fields.push(...readableFields(snapshot, ['label', 'revision', 'published_at']).map((field) => ({ ...field, label: `indexed_snapshot.${field.label}` })))
    return { id, title, content, fields }
  })
  return { reference, kind: 'code', items, nextCursor, provenance: [] }
}

export function useOperatorUnresolved(catalog: { unresolved: Ref<OperatorUnresolvedProject[]>; pending: Ref<boolean>; error: Ref<OperatorSourceError | null> }) {
  const route = useRoute()
  const router = useRouter()
  const reference = computed(() => typeof route.query.reference === 'string' ? route.query.reference : '')
  const retained = computed(() => catalog.unresolved.value.find((row) => row.reference === reference.value))
  const availableKinds = computed(() => retained.value?.reader_kinds || [])
  const kind = computed(() => typeof route.query.kind === 'string' ? route.query.kind : availableKinds.value.includes('memory') ? 'memory' : availableKinds.value[0] || '')
  const metadataOnly = computed(() => Boolean(retained.value && !availableKinds.value.length))
  const unsupportedKind = computed(() => Boolean(retained.value && kind.value && !availableKinds.value.some((value) => value === kind.value)))
  const q = computed(() => typeof route.query.q === 'string' ? route.query.q : '')
  const id = computed(() => typeof route.query.id === 'string' ? route.query.id : kind.value === 'memory' && typeof route.query.memory === 'string' ? route.query.memory : '')
  const content = ref<UnresolvedContentPage | null>(null)
  const history = ref<UnresolvedHistoryPage | null>(null)
  const selected = computed(() => content.value?.items.find((row) => id.value ? row.id === id.value : typeof route.query.path === 'string' && row.title === route.query.path && (route.query.version === undefined || row.fields.some((field) => field.label === 'version' && field.value === route.query.version))))
  const historyID = computed(() => {
    if (id.value) return id.value
    if (kind.value !== 'document' || typeof route.query.path !== 'string') return ''
    if (selected.value) return selected.value.id
    const matching = content.value?.items.filter((row) => row.title === route.query.path) || []
    return matching.length === 1 ? matching[0].id : ''
  })
  const pending = ref(false)
  const historyPending = ref(false)
  const error = ref<OperatorSourceError | null>(null)
  const historyError = ref<OperatorSourceError | null>(null)
  const retryRevision = ref(0)
  const limit = 20
  let contentGeneration = 0
  let historyGeneration = 0
  let contentRequest: AbortController | undefined
  let historyRequest: AbortController | undefined

  watch(() => [route.query.reference, route.query.kind, kind.value, retained.value, catalog.pending.value, catalog.error.value, route.query.q, route.query.cursor, retryRevision.value], async () => {
    const generation = ++contentGeneration
    contentRequest?.abort()
    contentRequest = new AbortController()
    const signal = contentRequest.signal
    content.value = null
    error.value = null
    pending.value = false
    if (route.query.reference === undefined) return
    if (catalog.pending.value || catalog.error.value || metadataOnly.value || unsupportedKind.value || !kind.value) return
    const target = reference.value
    const endpoint = `/api/projects/unresolved/${encodeURIComponent(target)}/${kind.value === 'code' ? 'code' : 'content'}`
    pending.value = true
    try {
      if (!reference.value || typeof route.query.reference !== 'string') throw new TypeError('Invalid retained reference')
      if (route.query.kind !== undefined && typeof route.query.kind !== 'string') throw new TypeError('Invalid retained kind')
      if (route.query.q !== undefined && typeof route.query.q !== 'string') throw new TypeError('Invalid retained query')
      if (route.query.cursor !== undefined && typeof route.query.cursor !== 'string') throw new TypeError('Invalid retained continuation')
      const targetKind = kind.value === 'code' ? 'code' : parseUnresolvedKind(kind.value)
      const params = new URLSearchParams({ limit: String(limit) })
      if (targetKind !== 'code') { params.set('kind', targetKind); params.set('q', q.value) }
      if (typeof route.query.cursor === 'string') params.set('cursor', route.query.cursor)
      const payload = await operatorFetchJson<unknown>(`${endpoint}?${params}`, { signal }, 'unresolved-content')
      if (generation !== contentGeneration || signal.aborted) return
      const page = targetKind === 'code' ? parseUnresolvedCode(payload, target, limit) : parseUnresolvedContent(payload, target, targetKind, limit)
      if (page.nextCursor && page.nextCursor === params.get('cursor')) throw new TypeError('Unresolved continuation did not advance')
      content.value = page
    } catch (cause) {
      if (generation !== contentGeneration || signal.aborted) return
      error.value = toOperatorSourceError(cause, { source: 'unresolved-content', path: endpoint, method: 'GET' })
      if (cause instanceof TypeError) error.value.category = 'invalid-response'
    } finally { if (generation === contentGeneration) pending.value = false }
  }, { immediate: true, flush: 'sync' })

  watch(() => [route.query.reference, route.query.kind, kind.value, retained.value, catalog.pending.value, catalog.error.value, route.query.id, route.query.memory, historyID.value, route.query.historyCursor, retryRevision.value], async () => {
    const generation = ++historyGeneration
    historyRequest?.abort()
    historyRequest = new AbortController()
    const signal = historyRequest.signal
    history.value = null
    historyError.value = null
    historyPending.value = false
    if (catalog.pending.value || catalog.error.value || !reference.value || kind.value === 'code' || !kind.value || metadataOnly.value || unsupportedKind.value) return
    const target = reference.value
    const endpoint = `/api/projects/unresolved/${encodeURIComponent(target)}/history`
    try {
      if (route.query.kind !== undefined && typeof route.query.kind !== 'string') throw new TypeError('Invalid retained kind')
      const targetKind = parseUnresolvedKind(kind.value)
      if (route.query.id !== undefined && (typeof route.query.id !== 'string' || !route.query.id)) throw new TypeError('Invalid retained history identity')
      if (route.query.historyCursor !== undefined && typeof route.query.historyCursor !== 'string') throw new TypeError('Invalid retained history continuation')
      const targetID = historyID.value
      if (!targetID) return
      historyPending.value = true
      const params = new URLSearchParams({ kind: targetKind, id: targetID, limit: String(limit) })
      if (typeof route.query.historyCursor === 'string') params.set('cursor', route.query.historyCursor)
      const payload = await operatorFetchJson<unknown>(`${endpoint}?${params}`, { signal }, 'unresolved-history')
      if (generation !== historyGeneration || signal.aborted) return
      const page = parseUnresolvedHistory(payload, target, targetKind, targetID, limit)
      if (page.nextCursor && page.nextCursor === params.get('cursor')) throw new TypeError('History continuation did not advance')
      history.value = page
    } catch (cause) {
      if (generation !== historyGeneration || signal.aborted) return
      historyError.value = toOperatorSourceError(cause, { source: 'unresolved-history', path: endpoint, method: 'GET' })
      if (cause instanceof TypeError) historyError.value.category = 'invalid-response'
    } finally { if (generation === historyGeneration) historyPending.value = false }
  }, { immediate: true, flush: 'sync' })
  onBeforeUnmount(() => { contentGeneration += 1; historyGeneration += 1; contentRequest?.abort(); historyRequest?.abort() })
  function update(query: Record<string, string | undefined>) { return router.push({ path: route.path, query: { ...route.query, ...query }, hash: route.hash }) }
  function open(item: UnresolvedKnowledgeItem) { return update({ id: item.id, historyCursor: undefined }) }
  const originalLocation = computed(() => typeof route.query.originalLocation === 'string' ? route.query.originalLocation : typeof route.query.returnTo === 'string' ? route.query.returnTo : '')
  const returnTo = computed(() => operatorProjectReturnTo(route.query.returnTo, router, typeof route.query.originalLocation === 'string' ? route.query.originalLocation : ''))
  return { reference, retained, availableKinds, kind, metadataOnly, unsupportedKind, q, id, selected, historyID, content, history, pending, historyPending, error, historyError, originalLocation, returnTo, open, update, retry: () => { retryRevision.value += 1 } }
}
