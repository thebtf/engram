import { computed, ref, watch } from 'vue'
import { useRoute, useRouter, type LocationQuery } from 'vue-router'
import { operatorFetchJson, toOperatorSourceError, type OperatorSourceError } from './useOperatorApi'

export interface OperatorProjectDescriptor { id: string; display_name: string }
export interface OperatorProjectCatalog { items: OperatorProjectDescriptor[] }
export interface OperatorKnowledgePage<T> { items: T[]; total: number; offset: number; limit: number; has_more: boolean }

const project = ref('')
const descriptors = ref<OperatorProjectDescriptor[]>([])
const projects = computed(() => descriptors.value.map((row) => row.id))
const pending = ref(false)
const error = ref<OperatorSourceError | null>(null)
const remembered = new Map<string, LocationQuery>()
let catalogRequest: Promise<void> | undefined
const navigationKey = 'engram.console.knowledgeNavigation'
let navigationLoaded = false
let catalogGeneration = 0

const navigationFields: Record<string, true> = Object.fromEntries(['project', 'q', 'filter', 'page', 'size', 'allRead', 'memory', 'memoryProject', 'from', 'searchQuery', 'focusMemory', 'path', 'docProject', 'pathPrefix', 'path_prefix', 'docType', 'version', 'compare', 'session', 'draftQ', 'draftPrefix', 'draftType', 'principal', 'principalKind', 'principalProject', 'principalDomain', 'principalVisibility', 'principalPrivate'].map((key) => [key, true]))
function safeNavigation(query: LocationQuery): LocationQuery {
  return Object.fromEntries(Object.entries(query).filter(([key, value]) => Object.hasOwn(navigationFields, key) && (typeof value === 'string' || value === null || (Array.isArray(value) && value.every((item) => typeof item === 'string' || item === null)))))
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

export function parseProjectCatalog(payload: OperatorProjectCatalog) {
  if (!payload || !Array.isArray(payload.items)) throw new TypeError('Invalid project catalog')
  const ids = new Set<string>()
  for (const row of payload.items) {
    if (!row || typeof row.id !== 'string' || !row.id || typeof row.display_name !== 'string' || ids.has(row.id)) throw new TypeError('Invalid project identity')
    ids.add(row.id)
  }
  return payload.items
}

export function projectName(id: string) {
  const row = descriptors.value.find((item) => item.id === id)
  if (!row) return id
  const name = row.display_name || row.id
  return descriptors.value.some((item) => item.id !== id && (item.display_name || item.id) === name) ? `${name} · ${id}` : name
}

async function refreshProjects() {
  const generation = ++catalogGeneration
  pending.value = true
  error.value = null
  try {
    const payload = await operatorFetchJson<OperatorProjectCatalog>('/api/projects', undefined, 'project-context')
    if (generation !== catalogGeneration) return
    descriptors.value = parseProjectCatalog(payload).sort((a, b) => (a.display_name || a.id).localeCompare(b.display_name || b.id) || a.id.localeCompare(b.id))
  } catch (cause) {
    if (generation !== catalogGeneration) return
    descriptors.value = []
    error.value = toOperatorSourceError(cause, { source: 'project-context', path: '/api/projects', method: 'GET' })
    if (cause instanceof TypeError) error.value.category = 'invalid-response'
  } finally { if (generation === catalogGeneration) pending.value = false }
}

export function parseKnowledgePage<T>(payload: OperatorKnowledgePage<T>, offset: number, limit: number) {
  if (!payload || !Array.isArray(payload.items) || !Number.isSafeInteger(payload.total) || payload.total < 0 || payload.offset !== offset || payload.limit !== limit || typeof payload.has_more !== 'boolean' || payload.items.length > limit || payload.items.length > Math.max(0, payload.total - offset)) throw new TypeError('Invalid knowledge page')
  if (payload.has_more !== (offset < payload.total && payload.items.length < payload.total - offset) || (payload.has_more && !payload.items.length)) throw new TypeError('Invalid knowledge continuation')
  return payload
}

export async function loadKnowledgePage<T extends { project?: string }>(endpoint: '/api/memories' | '/api/documents', scope: string, params: URLSearchParams, signal?: AbortSignal): Promise<OperatorKnowledgePage<T>> {
  const offset = Number(params.get('offset') || 0)
  const limit = Number(params.get('limit') || 20)
  if (!scope) return { items: [], total: 0, offset, limit, has_more: false }
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
  if (!catalogRequest) catalogRequest = refreshProjects()
  await catalogRequest
  if (error.value) throw error.value
  const items: T[] = []
  let total = 0
  // Explicit all-projects browsing composes authorized identities, never a label or an omitted project.
  for (const id of projects.value) {
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
  watch(() => route.fullPath, () => {
    if (typeof route.query.project === 'string') project.value = route.query.project
    remembered.set(`${project.value}\u0000${route.path}`, safeNavigation(route.query))
    saveNavigation()
  }, { immediate: true, flush: 'sync' })
  if (!catalogRequest) catalogRequest = refreshProjects()

  function to(path: string) {
    const query = { ...remembered.get(`${project.value}\u0000${path}`) }
    if (project.value) query.project = project.value
    return { path, query }
  }
  function remember(path: string, fields: LocationQuery) {
    const key = `${project.value}\u0000${path}`
    remembered.set(key, safeNavigation({ ...remembered.get(key), ...fields, project: project.value }))
    saveNavigation()
  }
  function select(id: string, path = route.path) {
    if (id !== 'all' && !projects.value.includes(id)) return
    const query: LocationQuery = { ...remembered.get(`${id}\u0000${path}`), project: id }
    delete query.allRead
    return router.push({ path, query })
  }
  const label = computed(() => project.value ? projectName(project.value) : '')
  return { project, projects, descriptors, pending, error, label, refresh: () => { catalogRequest = refreshProjects(); return catalogRequest }, select, to, remember }
}
