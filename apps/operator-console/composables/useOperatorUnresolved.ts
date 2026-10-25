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
}
export interface UnresolvedHistoryPage {
  reference: string
  kind: UnresolvedKnowledgeKind
  id: string
  items: UnresolvedKnowledgeItem[]
  nextCursor: string | null
}

type RetainedRecordItem = UnresolvedKnowledgeItem & { reference: string }
interface RetainedRecordPage { kind: UnresolvedKnowledgeKind; items: RetainedRecordItem[]; nextCursor: string | null }
const historicalScope = 'unresolved_history'
const historicalKinds: UnresolvedKnowledgeKind[] = ['memory', 'document', 'issue']
function positiveID(value: unknown): string {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value <= 0) throw new TypeError('Invalid historical native identity')
  return String(value)
}
function historicalRecord(value: unknown): asserts value is Record<string, unknown> {
  if (!object(value) || value.attribution_scope !== historicalScope || Object.hasOwn(value, 'project_key')) throw new TypeError('Historical attribution mismatch')
}
export function parseRetainedRecordQuery(payload: unknown, kind: UnresolvedKnowledgeKind, limit: number): RetainedRecordPage {
  parseUnresolvedKind(kind)
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 200 || !object(payload) || payload.view !== 'retained_records' || payload.attribution_scope !== historicalScope || payload.kind !== kind || !Array.isArray(payload.items) || payload.items.length > limit || Object.hasOwn(payload, 'total') || Object.hasOwn(payload, 'has_more')) throw new TypeError('Historical query envelope mismatch')
  const ids = new Set<string>()
  const items = payload.items.map((item: unknown): RetainedRecordItem => {
    if (!object(item) || typeof item.reference !== 'string' || !/^urn:engram:migration:unresolved:[0-9a-f]{64}$/.test(item.reference)) throw new TypeError('Invalid historical reference')
    const id = positiveID(item.id)
    historicalRecord(item.record)
    if (positiveID(item.record.id) !== id || ids.has(id)) throw new TypeError('Historical record identity mismatch')
    ids.add(id)
    return { ...retainedItem(item.record, kind, false), reference: item.reference }
  })
  return { kind, items, nextCursor: continuation(payload.next_cursor) }
}

function historicalSelection(payload: unknown, id: string, history: boolean) {
  historicalRecord(payload)
  if (!/^[1-9]\d*$/.test(id) || !Number.isSafeInteger(Number(id)) || !Array.isArray(payload.items) || (payload.id !== undefined && positiveID(payload.id) !== id)) throw new TypeError('Historical selection mismatch')
  for (const item of payload.items) {
    historicalRecord(item)
    if (!history && positiveID(item.id) !== id) throw new TypeError('Historical content identity mismatch')
  }
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
export function parseUnresolvedContent(payload: unknown, reference: string, kind: UnresolvedKnowledgeKind, limit: number): UnresolvedContentPage {
  if (!object(payload) || payload.reference !== reference || payload.kind !== kind || !Array.isArray(payload.items) || payload.items.length > limit) throw new TypeError('Unresolved content scope mismatch')
  const nextCursor = continuation(payload.next_cursor)
  if (nextCursor && !payload.items.length) throw new TypeError('Empty unresolved continuation')
  return { reference, kind, items: payload.items.map((item: unknown) => retainedItem(item, kind, false)), nextCursor }
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
  return { reference, kind: 'code', items, nextCursor }
}

export function useOperatorUnresolved(catalog: { unresolved: Ref<OperatorUnresolvedProject[]>; pending: Ref<boolean>; error: Ref<OperatorSourceError | null> }) {
  const route = useRoute()
  const router = useRouter()
  const recordMode = computed(() => route.query.view !== undefined)
  const reference = computed(() => typeof route.query.reference === 'string' ? route.query.reference : '')
  const retained = computed(() => catalog.unresolved.value.find((row) => row.reference === reference.value))
  const availableKinds = computed(() => recordMode.value ? historicalKinds : retained.value?.reader_kinds || [])
  const kind = computed(() => typeof route.query.kind === 'string' ? route.query.kind : availableKinds.value.includes('memory') ? 'memory' : availableKinds.value[0] || '')
  const metadataOnly = computed(() => !recordMode.value && Boolean(retained.value && !availableKinds.value.length))
  const unsupportedKind = computed(() => Boolean(kind.value && (recordMode.value || retained.value) && !availableKinds.value.some((value) => value === kind.value)))
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
  const records = ref<RetainedRecordPage | null>(null)
  const recordsPending = ref(false)
  const recordsError = ref<OperatorSourceError | null>(null)
  let recordsGeneration = 0
  let recordsRequest: AbortController | undefined
  watch([() => route.query.view, () => route.query.attribution_scope, () => route.query.kind, () => route.query.q, () => route.query.limit, () => route.query.cursor, retryRevision], async () => {
    const generation = ++recordsGeneration
    recordsRequest?.abort()
    recordsRequest = new AbortController()
    const signal = recordsRequest.signal
    records.value = null
    recordsError.value = null
    recordsPending.value = false
    if (!recordMode.value) return
    recordsPending.value = true
    try {
      if (route.query.view !== 'retained_records' || route.query.attribution_scope !== historicalScope || (route.query.q !== undefined && typeof route.query.q !== 'string') || (route.query.cursor !== undefined && (typeof route.query.cursor !== 'string' || !route.query.cursor)) || (route.query.limit !== undefined && (typeof route.query.limit !== 'string' || !/^[1-9]\d*$/.test(route.query.limit)))) throw new TypeError('Invalid historical query')
      const queryKind = parseUnresolvedKind(route.query.kind)
      const size = route.query.limit === undefined ? limit : Number(route.query.limit)
      if (!Number.isSafeInteger(size) || size < 1 || size > 200 || new TextEncoder().encode(q.value).length > 1024) throw new TypeError('Invalid historical query bounds')
      const params = new URLSearchParams({ view: 'retained_records', attribution_scope: historicalScope, kind: queryKind, q: q.value, limit: String(size) })
      if (typeof route.query.cursor === 'string') params.set('cursor', route.query.cursor)
      const payload = await operatorFetchJson<unknown>(`/api/projects?${params}`, { signal }, 'unresolved-records')
      if (generation !== recordsGeneration || signal.aborted) return
      const page = parseRetainedRecordQuery(payload, queryKind, size)
      if (page.nextCursor && page.nextCursor === params.get('cursor')) throw new TypeError('Historical continuation did not advance')
      records.value = page
    } catch (cause) {
      if (generation !== recordsGeneration || signal.aborted) return
      recordsError.value = toOperatorSourceError(cause, { source: 'unresolved-records', path: '/api/projects', method: 'GET' })
      if (cause instanceof TypeError) recordsError.value.category = 'invalid-response'
    } finally { if (generation === recordsGeneration) recordsPending.value = false }
  }, { immediate: true, flush: 'sync' })

  watch(() => [route.query.reference, route.query.kind, kind.value, recordMode.value ? undefined : retained.value, recordMode.value ? false : catalog.pending.value, recordMode.value ? null : catalog.error.value, route.query.q, route.query.cursor, recordMode.value, route.query.attribution_scope, recordMode.value ? route.query.id : undefined, recordsError.value, retryRevision.value], async () => {
    const generation = ++contentGeneration
    contentRequest?.abort()
    contentRequest = new AbortController()
    const signal = contentRequest.signal
    content.value = null
    error.value = null
    pending.value = false
    if (route.query.reference === undefined) return
    if ((!recordMode.value && (catalog.pending.value || catalog.error.value)) || metadataOnly.value || unsupportedKind.value || !kind.value || (recordMode.value && recordsError.value)) return
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
      if (recordMode.value) {
        if (route.query.attribution_scope !== historicalScope || targetKind === 'code' || typeof route.query.id !== 'string' || !/^[1-9]\d*$/.test(route.query.id) || !Number.isSafeInteger(Number(route.query.id))) throw new TypeError('Invalid historical selection')
        params.set('kind', targetKind)
        params.set('id', route.query.id)
        params.set('attribution_scope', historicalScope)
      } else {
        if (targetKind !== 'code') { params.set('kind', targetKind); params.set('q', q.value) }
        if (typeof route.query.cursor === 'string') params.set('cursor', route.query.cursor)
      }
      const payload = await operatorFetchJson<unknown>(`${endpoint}?${params}`, { signal }, 'unresolved-content')
      if (generation !== contentGeneration || signal.aborted) return
      if (recordMode.value) {
        historicalSelection(payload, id.value, false)
        if (historyError.value) return
      }
      const page = targetKind === 'code' ? parseUnresolvedCode(payload, target, limit) : parseUnresolvedContent(payload, target, targetKind, limit)
      if (page.nextCursor && page.nextCursor === params.get('cursor')) throw new TypeError('Unresolved continuation did not advance')
      content.value = page
    } catch (cause) {
      if (generation !== contentGeneration || signal.aborted) return
      error.value = toOperatorSourceError(cause, { source: 'unresolved-content', path: endpoint, method: 'GET' })
      if (cause instanceof TypeError) error.value.category = 'invalid-response'
      if (recordMode.value) history.value = null
    } finally { if (generation === contentGeneration) pending.value = false }
  }, { immediate: true, flush: 'sync' })

  watch(() => [route.query.reference, route.query.kind, kind.value, recordMode.value ? undefined : retained.value, recordMode.value ? false : catalog.pending.value, recordMode.value ? null : catalog.error.value, route.query.id, route.query.memory, historyID.value, route.query.historyCursor, recordMode.value, route.query.attribution_scope, recordsError.value, retryRevision.value], async () => {
    const generation = ++historyGeneration
    historyRequest?.abort()
    historyRequest = new AbortController()
    const signal = historyRequest.signal
    history.value = null
    historyError.value = null
    historyPending.value = false
    if ((!recordMode.value && (catalog.pending.value || catalog.error.value)) || !reference.value || kind.value === 'code' || !kind.value || metadataOnly.value || unsupportedKind.value || (recordMode.value && recordsError.value)) return
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
      if (recordMode.value) {
        if (route.query.attribution_scope !== historicalScope || !/^[1-9]\d*$/.test(targetID) || !Number.isSafeInteger(Number(targetID))) throw new TypeError('Invalid historical history selection')
        params.set('attribution_scope', historicalScope)
      }
      if (typeof route.query.historyCursor === 'string') params.set('cursor', route.query.historyCursor)
      const payload = await operatorFetchJson<unknown>(`${endpoint}?${params}`, { signal }, 'unresolved-history')
      if (generation !== historyGeneration || signal.aborted) return
      if (recordMode.value) {
        historicalSelection(payload, targetID, true)
        if (error.value) return
      }
      const page = parseUnresolvedHistory(payload, target, targetKind, targetID, limit)
      if (page.nextCursor && page.nextCursor === params.get('cursor')) throw new TypeError('History continuation did not advance')
      history.value = page
    } catch (cause) {
      if (generation !== historyGeneration || signal.aborted) return
      historyError.value = toOperatorSourceError(cause, { source: 'unresolved-history', path: endpoint, method: 'GET' })
      if (cause instanceof TypeError) historyError.value.category = 'invalid-response'
      if (recordMode.value) content.value = null
    } finally { if (generation === historyGeneration) historyPending.value = false }
  }, { immediate: true, flush: 'sync' })
  onBeforeUnmount(() => { contentGeneration += 1; historyGeneration += 1; recordsGeneration += 1; contentRequest?.abort(); historyRequest?.abort(); recordsRequest?.abort() })
  function update(query: Record<string, string | undefined>) { return router.push({ path: route.path, query: { ...route.query, ...query }, hash: route.hash }) }
  function open(item: UnresolvedKnowledgeItem) {
    if (recordMode.value) {
      const issued = records.value?.items.find(row => row === item)
      if (!issued) return
      return update({ reference: issued.reference, id: issued.id, historyCursor: undefined })
    }
    return update({ id: item.id, historyCursor: undefined })
  }
  const originalLocation = computed(() => typeof route.query.originalLocation === 'string' ? route.query.originalLocation : typeof route.query.returnTo === 'string' ? route.query.returnTo : '')
  const returnTo = computed(() => operatorProjectReturnTo(route.query.returnTo, router, typeof route.query.originalLocation === 'string' ? route.query.originalLocation : ''))
  return { recordMode, records, recordsPending, recordsError, reference, retained, availableKinds, kind, metadataOnly, unsupportedKind, q, id, selected, historyID, content, history, pending, historyPending, error, historyError, originalLocation, returnTo, open, update, retry: () => { retryRevision.value += 1 } }
}
