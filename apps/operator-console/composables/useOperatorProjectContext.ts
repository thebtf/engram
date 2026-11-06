import { computed, effectScope, ref, watch } from 'vue'
import { useRoute, useRouter, type LocationQuery, type RouteLocationNormalizedLoaded, type Router } from 'vue-router'
import { operatorFetchJson, toOperatorSourceError, type OperatorSourceError } from './useOperatorApi'

export interface OperatorProjectDescriptor { project_id: string; name: string; repository_qualifier?: string }
export type OperatorRetainedReaderKind = 'memory' | 'document' | 'issue' | 'code'
export interface OperatorUnresolvedProject {
  reference: string; name: string; reason: string; reader_kinds: OperatorRetainedReaderKind[]
  families?: Array<{ kind: string; table_name: string; membership_count: number; reader_support: 'declared' | 'separate' | 'metadata_only' }>
}
export interface OperatorLogicalGroup { primary_project_id: string; alias_project_ids: string[] }
export interface OperatorProjectCatalog { items: OperatorProjectDescriptor[]; unresolved: OperatorUnresolvedProject[]; logicalGroups: OperatorLogicalGroup[]; logicalGroupsPresent: boolean; logicalGroupsInvalid: boolean; uncertainDocumentProjects: string[] }
export type OperatorProjectResolution = { status: 'resolved'; project_id: string; name: string } | { status: 'unresolved'; reference: string; reason: string }
export interface OperatorKnowledgePage<T> { items: T[]; total: number; offset: number; limit: number; has_more: boolean }

const project = ref('')
const descriptors = ref<OperatorProjectDescriptor[]>([])
const unresolved = ref<OperatorUnresolvedProject[]>([])
const logicalGroups = ref<OperatorLogicalGroup[]>([])
const logicalGroupsInvalid = ref(false)
const documentCapabilityActive = ref(false)
let documentCapabilityObserved = false
let documentCapabilityUncertain = false
const uncertainDocumentProjects = ref<string[]>([])
const projects = computed(() => descriptors.value.map((row) => row.project_id))
const projectSections = computed(() => {
  const used = new Set(logicalGroups.value.flatMap(group => [group.primary_project_id, ...group.alias_project_ids]))
  const sections: Array<{ key: string; label: string; ids: string[] }> = logicalGroups.value.map(group => ({ key: group.primary_project_id, label: projectChoiceLabel(group.primary_project_id), ids: [group.primary_project_id, ...group.alias_project_ids] }))
  const remaining = projects.value.filter(id => !used.has(id))
  if (remaining.length) sections.push({ key: 'ungrouped', label: '', ids: remaining })
  return sections
})
const pending = ref(false)
const error = ref<OperatorSourceError | null>(null)
const selectionPending = ref(false)
const selectionError = ref<OperatorSourceError | null>(null)
const catalogRevision = ref(0)
const remembered = new Map<string, LocationQuery>()
let catalogRequest: Promise<void> | undefined
const navigationKey = 'engram.console.knowledgeNavigation'
let navigationLoaded = false
let catalogGeneration = 0
let selectionGeneration = 0
let observedRoute: RouteLocationNormalizedLoaded | undefined
let lastResolvedLocation: string | undefined
let stopSelectionWatch: (() => void) | undefined
const selectionScope = effectScope(true)

const navigationFields: Record<string, true> = Object.fromEntries(['project', 'q', 'filter', 'epistemic_type', 'page', 'size', 'allRead', 'memory', 'memoryProject', 'from', 'searchQuery', 'focusMemory', 'path', 'document_id', 'docProject', 'pathPrefix', 'path_prefix', 'docType', 'version', 'compare', 'session', 'draftQ', 'draftPrefix', 'draftType', 'principal', 'principalKind', 'principalProject', 'principalDomain', 'principalVisibility', 'principalPrivate'].map((key) => [key, true]))
export function safeNavigation(query: LocationQuery, fullPath?: string): LocationQuery {
  const safe: LocationQuery = Object.fromEntries(Object.entries(query).filter(([key, value]) => Object.hasOwn(navigationFields, key) && (typeof value === 'string' || value === null || (Array.isArray(value) && value.every((item) => typeof item === 'string' || item === null)))))
  const start = fullPath?.indexOf('?') ?? -1
  if (fullPath && start >= 0 && safe.docType !== undefined) {
    const end = fullPath.indexOf('#', start)
    const values: Array<string | null> = []
    for (const part of fullPath.slice(start + 1, end < 0 ? undefined : end).split('&')) {
      const equal = part.indexOf('=')
      const rawKey = equal < 0 ? part : part.slice(0, equal)
      let key: string
      try { key = decodeURIComponent(rawKey.replace(/\+/g, ' ')) } catch { continue }
      if (key !== 'docType') continue
      if (equal < 0) { values.push(null); continue }
      try { values.push(decodeURIComponent(part.slice(equal + 1).replace(/\+/g, ' '))) }
      catch { values.push(null) }
    }
    if (values.length) safe.docType = Array.isArray(safe.docType) ? values : values[0]
  }
  return safe
}
function saveNavigation() {
  try { sessionStorage.setItem(navigationKey, JSON.stringify([...remembered])) } catch { /* Storage is optional. */ }
}

function loadNavigation() {
  if (navigationLoaded || typeof window === 'undefined') return
  navigationLoaded = true
  try {
    const saved: unknown = JSON.parse(sessionStorage.getItem(navigationKey) || '[]')
    if (!Array.isArray(saved)) return
    for (const entry of saved) {
      if (!Array.isArray(entry) || typeof entry[0] !== 'string' || !entry[1] || typeof entry[1] !== 'object') continue
      const query: LocationQuery = {}
      for (const [key, value] of Object.entries(entry[1])) {
        if (typeof value === 'string' || value === null || (Array.isArray(value) && value.every((item) => typeof item === 'string' || item === null))) query[key] = value
      }
      remembered.set(entry[0], safeNavigation(query))
    }
  } catch { /* Navigation remains usable when session storage is unavailable. */ }
}

function object(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function isProjectUUID(value: unknown): value is string {
  return typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value)
}

function nonempty(value: unknown): value is string { return typeof value === 'string' && Boolean(value.trim()) }

export function parseProjectCatalog(payload: unknown): OperatorProjectCatalog {
  if (!object(payload) || !Array.isArray(payload.items) || !Array.isArray(payload.unresolved)) throw new TypeError('Invalid project catalog')
  const ids = new Set<string>()
  const references = new Set<string>()
  const items = payload.items.map((row: unknown): OperatorProjectDescriptor => {
    if (!object(row) || !isProjectUUID(row.project_id) || !nonempty(row.name) || ids.has(row.project_id) || (row.repository_qualifier !== undefined && !nonempty(row.repository_qualifier))) throw new TypeError('Invalid project identity')
    ids.add(row.project_id)
    return { project_id: row.project_id, name: row.name, ...(typeof row.repository_qualifier === 'string' ? { repository_qualifier: row.repository_qualifier } : {}) }
  })
  const retained = payload.unresolved.map((row: unknown): OperatorUnresolvedProject => {
    if (!object(row) || !nonempty(row.reference) || !nonempty(row.name) || !nonempty(row.reason) || references.has(row.reference) || !Array.isArray(row.reader_kinds)) throw new TypeError('Invalid unresolved project')
    const reader_kinds = row.reader_kinds.map((kind: unknown): OperatorRetainedReaderKind => {
      if (kind === 'memory' || kind === 'document' || kind === 'issue' || kind === 'code') return kind
      throw new TypeError('Invalid retained reader kind')
    })
    if (new Set(reader_kinds).size !== reader_kinds.length) throw new TypeError('Duplicate retained reader kind')
    references.add(row.reference)
    let families: OperatorUnresolvedProject['families']
    if (row.families !== undefined) {
      if (!Array.isArray(row.families)) throw new TypeError('Invalid retained families')
      const tables = new Set<string>()
      families = row.families.map((family: unknown) => {
        if (!object(family) || !nonempty(family.kind) || !nonempty(family.table_name) || tables.has(family.table_name) || typeof family.membership_count !== 'number' || !Number.isSafeInteger(family.membership_count) || family.membership_count < 0 || (family.reader_support !== 'declared' && family.reader_support !== 'separate' && family.reader_support !== 'metadata_only')) throw new TypeError('Invalid retained family metadata')
        tables.add(family.table_name)
        return { kind: family.kind, table_name: family.table_name, membership_count: family.membership_count, reader_support: family.reader_support }
      })
    }
    return { reference: row.reference, name: row.name, reason: row.reason, reader_kinds, ...(families === undefined ? {} : { families }) }
  })
  const candidates: OperatorLogicalGroup[] = []
  let logicalGroupsInvalid = false
  const implicated = new Set<string>()
  if (Object.hasOwn(payload, 'logical_groups')) {
    if (!Array.isArray(payload.logical_groups)) { logicalGroupsInvalid = true; for (const id of ids) implicated.add(id) }
    else for (const group of payload.logical_groups) {
      if (!object(group) || !isProjectUUID(group.primary_project_id) || !ids.has(group.primary_project_id) || !Array.isArray(group.alias_project_ids) || !group.alias_project_ids.length || group.alias_project_ids.some((id: unknown) => !isProjectUUID(id) || !ids.has(id)) || new Set([group.primary_project_id, ...group.alias_project_ids]).size !== group.alias_project_ids.length + 1) {
        logicalGroupsInvalid = true
        const mentioned = object(group) ? [group.primary_project_id, ...(Array.isArray(group.alias_project_ids) ? group.alias_project_ids : [])] : []
        let identified = false
        for (const id of mentioned) if (typeof id === 'string' && ids.has(id)) { implicated.add(id); identified = true }
        if (!identified) for (const id of ids) implicated.add(id)
        continue
      }
      candidates.push({ primary_project_id: group.primary_project_id, alias_project_ids: group.alias_project_ids })
    }
  }
  const counts = new Map<string, number>()
  for (const group of candidates) for (const id of [group.primary_project_id, ...group.alias_project_ids]) counts.set(id, (counts.get(id) || 0) + 1)
  for (const group of candidates) if ([group.primary_project_id, ...group.alias_project_ids].some(id => (counts.get(id) || 0) > 1)) for (const id of [group.primary_project_id, ...group.alias_project_ids]) implicated.add(id)
  let changed = true
  while (changed) {
    changed = false
    for (const group of candidates) {
      const members = [group.primary_project_id, ...group.alias_project_ids]
      if (members.some(id => implicated.has(id))) for (const id of members) if (!implicated.has(id)) { implicated.add(id); changed = true }
    }
  }
  const logicalGroups = candidates.filter(group => {
    const valid = [group.primary_project_id, ...group.alias_project_ids].every(id => counts.get(id) === 1 && !implicated.has(id))
    if (!valid) logicalGroupsInvalid = true
    return valid
  })
  return { items, unresolved: retained, logicalGroups, logicalGroupsPresent: Object.hasOwn(payload, 'logical_groups'), logicalGroupsInvalid, uncertainDocumentProjects: [...implicated] }
}

export function parseProjectResolution(payload: unknown): OperatorProjectResolution {
  if (!object(payload)) throw new TypeError('Invalid project resolution')
  if (payload.status === 'resolved' && isProjectUUID(payload.project_id) && nonempty(payload.name)) return { status: 'resolved', project_id: payload.project_id, name: payload.name }
  if (payload.status === 'unresolved' && nonempty(payload.reference) && nonempty(payload.reason)) return { status: 'unresolved', reference: payload.reference, reason: payload.reason }
  throw new TypeError('Invalid project resolution')
}

export function catalogProjectName(id: string, items: OperatorProjectDescriptor[]): string {
  const row = items.find((item) => item.project_id === id)
  if (!row) return id
  const sameName = items.filter((item) => item.name === row.name)
  if (sameName.length === 1) return row.name
  const label = (item: OperatorProjectDescriptor) => item.repository_qualifier ? `${item.name} · ${item.repository_qualifier}` : item.name
  const text = label(row)
  const collisions = sameName.filter((item) => label(item) === text).sort((a, b) => a.project_id.localeCompare(b.project_id))
  return collisions.length > 1 ? `${text} #${collisions.findIndex((item) => item.project_id === id) + 1}` : text
}

export function projectName(id: string) { return catalogProjectName(id, descriptors.value) }
export function projectChoiceLabel(id: string, reservedLabels: readonly string[] = []): string {
  const row = descriptors.value.find((item) => item.project_id === id)
  if (!row) return id
  const display = row.repository_qualifier ? `${row.name} · ${row.repository_qualifier}` : row.name
  const visibleLabel = display.replace(/[ \t\r\n\f]+/g, ' ').replace(/^ | $/g, '')
  const reserved = reservedLabels.map((label) => label.replace(/[ \t\r\n\f]+/g, ' ').replace(/^ | $/g, ''))
  const needsIdentity = reserved.includes(visibleLabel) || (visibleLabel === 'Registered project' && !row.repository_qualifier) || descriptors.value.some((item) => {
    if (item.project_id === id) return false
    const itemLabel = item.repository_qualifier ? `${item.name} · ${item.repository_qualifier}` : item.name
    return itemLabel.replace(/[ \t\r\n\f]+/g, ' ').replace(/^ | $/g, '') === visibleLabel
  })
  if (!needsIdentity) return display
  const prefix = id.slice(0, 8)
  const normalizedPrefix = prefix.toLowerCase()
  const otherLabels = new Set<string>(reserved)
  let collision = false
  for (const item of descriptors.value) {
    if (item.project_id === id) continue
    const itemLabel = item.repository_qualifier ? `${item.name} · ${item.repository_qualifier}` : item.name
    otherLabels.add(itemLabel.replace(/[ \t\r\n\f]+/g, ' ').replace(/^ | $/g, ''))
    if (item.project_id.slice(0, 8).toLowerCase() === normalizedPrefix) collision = true
  }
  let result = `${display} · ${collision ? id : prefix}`
  if (otherLabels.has(result.replace(/[ \t\r\n\f]+/g, ' ').replace(/^ | $/g, ''))) result = `${display} · ${id}`
  for (let i = 0; i < otherLabels.size && otherLabels.has(result.replace(/[ \t\r\n\f]+/g, ' ').replace(/^ | $/g, '')); i++) {
    result += ` · ${id}`
  }
  return result
}
export function localizedProjectChoiceLabel(id: string, translate: (key: string) => string): string {
  return projectChoiceLabel(id, [translate('knowledge.allProjects'), translate('knowledge.chooseProject'), translate('knowledge.browseAll')])
}
export function isCatalogProject(id: string): boolean { return projects.value.includes(id) }
export function supportsDocumentLineage(id: string): boolean { return documentCapabilityActive.value && isCatalogProject(id) && !documentScopeUncertain(id) }
export function documentScopeUncertain(id: string): boolean { return uncertainDocumentProjects.value.includes(id) }
export function validDocumentID(value: unknown): value is string { return typeof value === 'string' && /^[1-9]\d*$/.test(value) && Number.isSafeInteger(Number(value)) }

function knownProjectQuery(query: LocationQuery): boolean {
  return ['project', 'memoryProject', 'docProject', 'principalProject'].every((key) => {
    const value = query[key]
    return value === undefined || (key === 'project' && (value === '' || value === 'all')) || (key === 'principalProject' && (value === 'all' || value === 'current')) || (typeof value === 'string' && isCatalogProject(value))
  })
}

export function operatorProjectReturnTo(value: unknown, router: Router, originalLocation = ''): string {
  if (typeof value !== 'string' || value === originalLocation || /[\\\u0000-\u001f\u007f]/.test(value) || !/^\/(?:[a-zA-Z0-9_-]+\/)*[a-zA-Z0-9_-]*$/.test(value.split(/[?#]/, 1)[0])) return '/projects'
  const target = router.resolve(value)
  return target.matched.length && !target.matched.some((record) => record.path === '/projects/unresolved') && knownProjectQuery(target.query) ? target.fullPath : '/projects'
}

function refreshProjects(): Promise<void> {
  const generation = ++catalogGeneration
  selectionGeneration += 1
  project.value = ''
  selectionPending.value = true
  pending.value = true
  error.value = null
  catalogRequest = (async () => {
    try {
      const catalog = parseProjectCatalog(await operatorFetchJson<unknown>('/api/projects', undefined, 'project-context'))
      if (generation !== catalogGeneration) return
      descriptors.value = catalog.items.sort((a, b) => a.name.localeCompare(b.name) || (a.repository_qualifier || '').localeCompare(b.repository_qualifier || '') || a.project_id.localeCompare(b.project_id))
      unresolved.value = catalog.unresolved
      const missingAfterUncertain = !catalog.logicalGroupsPresent && (documentCapabilityObserved || documentCapabilityUncertain)
      if (catalog.logicalGroupsPresent && !catalog.logicalGroupsInvalid) { documentCapabilityObserved = true; documentCapabilityUncertain = false }
      else if (catalog.logicalGroupsInvalid || missingAfterUncertain) documentCapabilityUncertain = true
      logicalGroups.value = catalog.logicalGroups
      logicalGroupsInvalid.value = catalog.logicalGroupsInvalid || missingAfterUncertain
      uncertainDocumentProjects.value = missingAfterUncertain || (catalog.logicalGroupsInvalid && !documentCapabilityObserved) ? catalog.items.map(item => item.project_id) : catalog.uncertainDocumentProjects
      documentCapabilityActive.value = documentCapabilityObserved && catalog.logicalGroupsPresent
    } catch (cause) {
      if (generation !== catalogGeneration) return
      descriptors.value = []
      unresolved.value = []
      logicalGroups.value = []
      logicalGroupsInvalid.value = false
      uncertainDocumentProjects.value = []
      documentCapabilityActive.value = false
      error.value = toOperatorSourceError(cause, { source: 'project-context', path: '/api/projects', method: 'GET' })
      if (cause instanceof TypeError) error.value.category = 'invalid-response'
    } finally {
      if (generation === catalogGeneration) { pending.value = false; catalogRevision.value += 1 }
    }
  })()
  return catalogRequest
}

async function ensureCatalog() {
  if (!catalogRequest) refreshProjects()
  let request: Promise<void> | undefined
  do { request = catalogRequest; await request } while (request !== catalogRequest)
  if (error.value) throw error.value
}

async function readProjectResolution(id: string): Promise<OperatorProjectResolution> {
  await ensureCatalog()
  const row = descriptors.value.find((item) => item.project_id === id)
  if (row) return { status: 'resolved', project_id: id, name: row.name }
  if (!id) throw new TypeError('Project selection is required')
  const payload = parseProjectResolution(await operatorFetchJson<unknown>(`/api/projects/resolve?${new URLSearchParams({ legacy_id: id })}`, undefined, 'project-resolution'))
  if (payload.status === 'resolved' && !isCatalogProject(payload.project_id)) throw new TypeError('Resolved project is absent from the catalog')
  return payload
}

export async function resolveCatalogProject(id: string): Promise<string | null> {
  const resolved = await readProjectResolution(id)
  return resolved.status === 'resolved' ? resolved.project_id : null
}

export function parseKnowledgePage<T>(payload: OperatorKnowledgePage<T>, offset: number, limit: number) {
  if (!Number.isSafeInteger(offset) || offset < 0 || !Number.isSafeInteger(limit) || limit < 1 || !payload || !Array.isArray(payload.items) || !Number.isSafeInteger(payload.total) || payload.total < 0 || payload.offset !== offset || payload.limit !== limit || typeof payload.has_more !== 'boolean' || payload.items.length > limit || payload.items.length > Math.max(0, payload.total - offset)) throw new TypeError('Invalid knowledge page')
  if (payload.has_more !== (offset < payload.total && payload.items.length < payload.total - offset) || (payload.has_more && !payload.items.length)) throw new TypeError('Invalid knowledge continuation')
  return payload
}

export async function loadKnowledgePage<T extends { project?: string }>(endpoint: '/api/memories' | '/api/documents', scope: string, params: URLSearchParams, signal?: AbortSignal): Promise<OperatorKnowledgePage<T>> {
  const offset = Number(params.get('offset') || 0)
  const limit = Number(params.get('limit') || 20)
  if (!Number.isSafeInteger(offset) || offset < 0 || !Number.isSafeInteger(limit) || limit < 1) throw new TypeError('Invalid knowledge paging')
  if (!scope) return { items: [], total: 0, offset, limit, has_more: false }
  await ensureCatalog()
  if (scope !== 'all' && !isCatalogProject(scope)) throw new TypeError('Knowledge reader requires a catalog project')
  if (scope === 'all' && logicalGroupsInvalid.value) throw new TypeError('Invalid logical project grouping')
  const aliases = new Set(logicalGroups.value.flatMap(group => group.alias_project_ids))
  const catalogIDs = projects.value.filter(id => !aliases.has(id))
  const read = async (id: string, from: number, size: number) => {
    const scoped = new URLSearchParams(params)
    scoped.set('project', id)
    scoped.set('offset', String(from))
    scoped.set('limit', String(size))
    const payload = await operatorFetchJson<OperatorKnowledgePage<T>>(`${endpoint}?${scoped}`, { signal }, 'knowledge-list')
    const page = parseKnowledgePage(payload, from, size)
    if (page.items.some((row) => !row || row.project !== id)) throw new TypeError('Knowledge page scope mismatch')
    return page
  }
  if (scope !== 'all') return read(scope, offset, limit)
  if (error.value) throw error.value
  const items: T[] = []
  let total = 0
  // Explicit all-projects browsing composes authorized identities, never a label or an omitted project.
  for (const id of catalogIDs) {
    if (signal?.aborted) throw new DOMException('Aborted', 'AbortError')
    const first = await read(id, 0, 1)
    const start = Math.max(0, offset - total)
    if (first.total > start && items.length < limit) {
      const result = await read(id, start, limit - items.length)
      items.push(...result.items)
    }
    total += first.total
  }
  return { items, total, offset, limit, has_more: offset + items.length < total }
}

export function useOperatorProjectContext() {
  const route = useRoute()
  const router = useRouter()
  loadNavigation()
  if (!catalogRequest) refreshProjects()

  function rememberCurrent() {
    if (selectionPending.value || selectionError.value || route.path === '/projects/unresolved') return
    remembered.set(`${project.value}\u0000${route.path}`, safeNavigation(route.query, route.fullPath))
    saveNavigation()
  }

  if (observedRoute !== route) {
    stopSelectionWatch?.()
    observedRoute = route
    stopSelectionWatch = selectionScope.run(() => watch(() => [route.fullPath, catalogRevision.value], async () => {
      const generation = ++selectionGeneration
      const location = route.fullPath
      const path = route.path
      const query = { ...route.query, ...safeNavigation(route.query, route.fullPath) }
      const scope = query.project
      const anchors = ['memoryProject', 'docProject', 'principalProject']
      const current = () => generation === selectionGeneration && route.fullPath === location
      selectionError.value = null
      if (path === '/projects/unresolved') { project.value = ''; selectionPending.value = false; return }
      const known = knownProjectQuery(query)
      if (!pending.value && !error.value && catalogRevision.value && known) {
        selectionPending.value = false
        project.value = typeof scope === 'string' ? scope : ''
        lastResolvedLocation = location
        rememberCurrent()
        return
      }
      selectionPending.value = true
      project.value = ''
      try {
        await ensureCatalog()
        if (!current()) return
        for (const key of ['project', ...anchors]) {
          const value = query[key]
          if (value === undefined || (key === 'project' && (value === '' || value === 'all')) || (key === 'principalProject' && (value === 'all' || value === 'current'))) continue
          if (typeof value !== 'string' || !value) throw new TypeError('Invalid project route value')
          const resolution = await readProjectResolution(value)
          if (!current()) return
          if (resolution.status === 'unresolved') {
            const kind = key === 'docProject' || path === '/documents' ? 'document' : path === '/issues' || path.startsWith('/issues/') ? 'issue' : path === '/code' ? 'code' : 'memory'
            const id = kind === 'issue' && typeof route.params.id === 'string' ? route.params.id : undefined
            await router.replace({ path: '/projects/unresolved', query: { ...query, ...(id ? { id } : {}), reference: resolution.reference, kind, originalLocation: location, returnTo: operatorProjectReturnTo(query.returnTo ?? lastResolvedLocation, router, location) }, hash: route.hash })
            return
          }
          query[key] = resolution.project_id
        }
        if (!current()) return
        if (['project', ...anchors].some((key) => query[key] !== route.query[key])) {
          await router.replace({ path, query, hash: route.hash })
          return
        }
        selectionPending.value = false
        project.value = typeof query.project === 'string' ? query.project : ''
        lastResolvedLocation = location
        rememberCurrent()
      } catch (cause) {
        if (!current()) return
        selectionError.value = error.value || toOperatorSourceError(cause, { source: 'project-resolution', path: '/api/projects/resolve', method: 'GET' })
        if (cause instanceof TypeError) selectionError.value.category = 'invalid-response'
      } finally { if (current()) selectionPending.value = false }
    }, { immediate: true, flush: 'sync' }))
  }
  watch(() => route.fullPath, rememberCurrent, { flush: 'post' })

  function to(path: string) {
    const query = { ...remembered.get(`${project.value}\u0000${path}`) }
    if (project.value) query.project = project.value
    else delete query.project
    return { path, query }
  }
  function remember(path: string, fields: LocationQuery) {
    if (selectionPending.value || selectionError.value || route.path === '/projects/unresolved') return
    const key = `${project.value}\u0000${path}`
    remembered.set(key, safeNavigation({ ...remembered.get(key), ...fields, project: project.value }, path === route.path ? route.fullPath : undefined))
    saveNavigation()
  }
  function select(id: string, path = route.path) {
    if (id !== 'all' && !isCatalogProject(id)) return
    if (path === '/projects/unresolved') path = '/projects'
    const query: LocationQuery = { ...remembered.get(`${id}\u0000${path}`), project: id }
    delete query.allRead
    return router.push({ path, query })
  }
  const label = computed(() => project.value ? projectName(project.value) : '')
  return { project, projects, descriptors, unresolved, projectSections, logicalGroupsInvalid, pending, error, selectionPending, selectionError, label, refresh: refreshProjects, select, to, remember, isCatalogProject, resolveCatalogProject }
}
