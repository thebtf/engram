/**
 * Mock data — the SEAM. This is the ONLY place the design scaffold invents data.
 * DEVELOPER: replace each function body with live data behind the same shapes. The page
 * components never change — they consume these composables, not raw fetches.
 *
 * Current live-wire policy:
 * - Memories, issues, vault status, vault credentials, server info, rules, projects,
 *   health, and server config are backed by the current Engram HTTP surface.
 * - Model rows remain mock until there is a direct, truthful model-health endpoint for
 *   this exact UI contract.
 */

import { operatorApiBase, operatorApiUrl } from './useOperatorApi'
import { useOperatorMemoryLab } from './useOperatorMemoryLab'

export interface Memory {
  id: string
  content: string
  tags: string[]
  tier: 'semantic' | 'episodic' | 'procedural'
  type: string
  project: string
  conf: number
  cite: number
  inj: number
  age: string
  noise?: boolean
}

export interface Issue {
  id: number
  title: string
  status: 'open' | 'acknowledged' | 'reopened' | 'resolved' | 'closed'
  priority: string
  type: string
  age: string
  comments: number
}

export interface Cred {
  id: string
  project: string
  scope: string
  created: string
}

export interface ModelRow {
  id: string
  provider: string
  health: 'ok' | 'standby' | 'degraded'
  costs: string
}

export interface RuleRow {
  id: number
  content: string
  project: string
  priority: number
  updated: string
}

export interface RuleCreateInput {
  content: string
  priority?: number
  project?: string
  editedBy?: string
}

export interface RuleUpdateInput {
  content?: string
  priority?: number
  editedBy?: string
}

export interface ProjectSummary {
  id: string
  sessions: number
  last: string
}

export interface ServerConfigSnapshot {
  injectUnified: boolean
  telemetryEnabled: boolean
  enforceSourceProject: boolean
  contextObservations: string
  contextMaxTokens: string
  contextSessionCount: string
  vectorStrategy: string
  databaseMaxConns: string
  logBufferSize: string
}

export interface HealthSnapshot {
  overall: string
  components: Array<{ name: string; status: 'healthy' | 'degraded' | 'unhealthy' }>
  embedding: {
    chunkCount: string
    withVectors: string
    dimension: string
    coverage: string
  }
  hasEmbedding: boolean
}

export const OPERATOR_DATA_AREAS = {
  memory: {
    owner: 'OC-1-CR-005 memory page lane',
    exports: ['Memory', 'useMemories'],
    source: 'useMockData.ts memory ownership section',
  },
  issues: {
    owner: 'OC-1-CR-007 issues page lane',
    exports: ['Issue', 'useIssues'],
    source: 'useMockData.ts issues ownership section',
  },
  vault: {
    owner: 'OC-1-CR-008 secrets page lane',
    exports: ['Cred', 'useCreds', 'useVaultStatus'],
    source: 'useMockData.ts vault ownership section',
  },
  rules: {
    owner: 'OC-1-CR-006 rules page lane',
    exports: ['RuleRow', 'RuleCreateInput', 'RuleUpdateInput', 'useRules'],
    source: 'useMockData.ts rules ownership section',
  },
  projects: {
    owner: 'OC-1-CR-009 projects page lane',
    exports: ['ProjectSummary', 'useProjects'],
    source: 'useMockData.ts projects ownership section',
  },
  health: {
    owner: 'OC-1-CR-010 health/settings lane',
    exports: ['HealthSnapshot', 'ServerConfigSnapshot', 'useHealthSnapshot', 'useServerConfigSnapshot'],
    source: 'useMockData.ts health/settings ownership section',
  },
  serverInfo: {
    owner: 'OC-1-CR-003 shell/overview lane',
    exports: ['useServerInfo', 'useModels'],
    source: 'useMockData.ts server-info/model ownership section',
  },
} as const

interface ApiIssueRow {
  id: number
  title: string
  status: Issue['status']
  priority: string
  type: string
  created_at: string
  comment_count?: number
}

interface ApiIssueList {
  issues?: ApiIssueRow[]
}

interface ApiVaultCredential {
  name: string
  project?: string
  scope?: string
  created_at?: string
}

interface ApiVaultStatus {
  key_configured?: boolean
  fingerprint?: string
  key_source?: string
}

interface ApiStats {
  uptime?: string
}

interface ApiSelfcheck {
  version?: string
  uptime?: string
  overall?: 'healthy' | 'degraded' | 'unhealthy'
  components?: Array<{ name?: string; status?: 'healthy' | 'degraded' | 'unhealthy' }>
}

interface ApiStatsVnext {
  noise_ratio?: number
  embedding?: {
    chunk_count?: number
    memories_with_chunks?: number
    active_memory_count?: number
    dimension?: number
    embedding_coverage?: number
  }
}

interface ApiRuleRow {
  id: number
  project?: string
  content?: string
  priority?: number
  edited_by?: string
  created_at?: string
  updated_at?: string
}

interface ApiSessionRow {
  id?: number
  project?: string
  started_at?: string
  status?: string
}

interface ApiSessionsList {
  sessions?: ApiSessionRow[]
  total?: number
}

interface ApiConfig {
  context?: {
    observations?: number
    max_tokens?: number
    session_count?: number
  }
  memory?: {
    inject_unified?: boolean
  }
  storage?: {
    vector_strategy?: string
    database_max_conns?: number
    log_buffer_size?: number
  }
  features?: {
    telemetry_enabled?: boolean
    enforce_source_project?: boolean
  }
}

function apiBase(): string {
  return operatorApiBase()
}

function displayHost(base: string, configuredHost?: string): string {
  if (configuredHost && configuredHost.trim()) {
    return configuredHost.trim()
  }

  if (base.startsWith('/')) {
    if (import.meta.client && typeof window !== 'undefined') {
      return window.location.host
    }

    return 'engram'
  }

  try {
    return new URL(base).host
  } catch {
    return base.replace(/^https?:\/\//, '').replace(/\/$/, '') || 'unleashed.lan:37777'
  }
}

function replaceArray<T>(target: T[], next: readonly T[]) {
  target.splice(0, target.length, ...next)
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useMockData] ${key} live load failed`, error)
      }
    })
  }
}

async function fetchApi(path: string, init: RequestInit = {}): Promise<Response> {
  const response = await fetch(operatorApiUrl(path, apiBase()), {
    credentials: 'include',
    ...init,
  })

  if (!response.ok) {
    const text = await response.text()
    const detail = text.trim() ? `: ${text.trim().slice(0, 240)}` : ''
    throw new Error(`${response.status} ${response.statusText} for ${path}${detail}`)
  }

  return response
}

async function fetchJson<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetchApi(path, init)
  if (response.status === 204) {
    return undefined as T
  }

  const text = await response.text()
  if (!text.trim()) {
    return undefined as T
  }

  const contentType = response.headers.get('content-type') || ''
  if (!contentType.includes('application/json')) {
    return text as T
  }

  try {
    return JSON.parse(text) as T
  } catch (error) {
    const detail = error instanceof Error ? error.message : String(error)
    throw new Error(`Invalid JSON from ${path}: ${detail}: ${text.trim().slice(0, 240)}`)
  }
}

function jsonInit(method: 'POST' | 'PATCH' | 'PUT' | 'DELETE', body?: unknown): RequestInit {
  const init: RequestInit = { method }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  return init
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function compactAge(timestamp?: string): string {
  if (!timestamp) return '—'

  const value = Date.parse(timestamp)
  if (Number.isNaN(value)) return '—'

  const seconds = Math.max(0, Math.floor((Date.now() - value) / 1000))
  if (seconds < 60) return `${seconds}с`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}м`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}ч`
  return `${Math.floor(seconds / 86400)}д`
}

function normalizeNoise(value?: number): string {
  if (typeof value !== 'number' || Number.isNaN(value)) return '—'
  return value.toFixed(2)
}

function normalizeProject(value?: string): string {
  return value && value.trim() ? value.trim() : 'global'
}

function displayValue(value: unknown): string {
  if (typeof value === 'number' && !Number.isNaN(value)) {
    return String(value)
  }
  if (typeof value === 'string' && value.trim()) {
    return value
  }
  return '—'
}

function percentValue(value?: number): string {
  if (typeof value !== 'number' || Number.isNaN(value)) {
    return '—'
  }
  return `${Math.round(value)}%`
}

function mapRuleRow(row: ApiRuleRow): RuleRow {
  return {
    id: row.id,
    content: row.content || '—',
    project: normalizeProject(row.project),
    priority: typeof row.priority === 'number' ? row.priority : 0,
    updated: compactAge(row.updated_at || row.created_at),
  }
}

async function listProjects(): Promise<string[]> {
  const projects = await fetchJson<string[]>('/api/projects')
  return [...new Set((projects || []).filter((value) => typeof value === 'string' && value.trim()))]
}

async function loadAllRules(): Promise<RuleRow[]> {
  const projects = await listProjects()
  const combined = new Map<number, RuleRow>()
  const globalRows = await fetchJson<ApiRuleRow[]>('/api/rules?limit=100')

  for (const row of globalRows || []) {
    combined.set(row.id, mapRuleRow(row))
  }

  for (const project of projects) {
    const rows = await fetchJson<ApiRuleRow[]>(`/api/rules?project=${encodeURIComponent(project)}&limit=100`)
    for (const row of rows || []) {
      combined.set(row.id, mapRuleRow(row))
    }
  }

  return [...combined.values()].sort((left, right) => {
    if (left.priority !== right.priority) {
      return right.priority - left.priority
    }
    return left.content.localeCompare(right.content)
  })
}

async function loadProjectSummaries(): Promise<ProjectSummary[]> {
  const [projects, sessionsPayload] = await Promise.all([
    listProjects(),
    fetchJson<ApiSessionsList>('/api/sessions/list?limit=5000'),
  ])

  const sessions = sessionsPayload.sessions || []
  const perProject = new Map<string, { sessions: number; lastStamp: number; last: string }>()

  for (const project of projects) {
    perProject.set(project, { sessions: 0, lastStamp: 0, last: '—' })
  }

  for (const session of sessions) {
    const project = normalizeProject(session.project)
    const current = perProject.get(project) || { sessions: 0, lastStamp: 0, last: '—' }
    current.sessions += 1
    const stamp = Date.parse(session.started_at || '')
    if (!Number.isNaN(stamp) && stamp >= current.lastStamp) {
      current.lastStamp = stamp
      current.last = compactAge(session.started_at)
    }
    perProject.set(project, current)
  }

  return [...perProject.entries()]
    .map(([id, entry]) => ({
      id,
      sessions: entry.sessions,
      last: entry.last,
    }))
    .sort((left, right) => {
      if (left.sessions !== right.sessions) {
        return right.sessions - left.sessions
      }
      return left.id.localeCompare(right.id)
    })
}

export const useMemories = () => {
  return useOperatorMemoryLab().rows
}

export const useIssues = () => {
  const state = useState<Issue[]>('live:issues', () => [])
  const rows = state.value

  startOnce('issues', async () => {
    const payload = await fetchJson<ApiIssueList>('/api/issues?limit=50')
    const liveRows = (payload.issues || []).map((issue) => ({
      id: issue.id,
      title: issue.title,
      status: issue.status,
      priority: issue.priority,
      type: issue.type,
      age: compactAge(issue.created_at),
      comments: issue.comment_count ?? 0,
    }))
    replaceArray(rows, liveRows)
  })

  return rows
}

export const useCreds = () => {
  const state = useState<Cred[]>('live:creds', () => [])
  const rows = state.value

  startOnce('creds', async () => {
    const payload = await fetchJson<ApiVaultCredential[]>('/api/vault/credentials')
    const liveRows = payload.map((row) => ({
      id: row.name,
      project: row.project || 'global',
      scope: row.scope || 'project',
      created: compactAge(row.created_at),
    }))
    replaceArray(rows, liveRows)
  })

  return rows
}

export const useVaultStatus = () => {
  const state = useState('live:vault-status', () => ({
    encrypted: false,
    fingerprint: '—',
    source: '—',
  }))
  const vault = state.value

  startOnce('vault-status', async () => {
    const payload = await fetchJson<ApiVaultStatus>('/api/vault/status')
    Object.assign(vault, {
      encrypted: Boolean(payload.key_configured),
      fingerprint: payload.fingerprint || '—',
      source: payload.key_source || '—',
    })
  })

  return vault
}

export const useRules = () => {
  const rows = useState<RuleRow[]>('live:rules', () => [])
  const pending = useState<boolean>('live:rules:pending', () => false)
  const error = useState<string | null>('live:rules:error', () => null)

  async function refresh() {
    pending.value = true
    error.value = null
    try {
      rows.value = await loadAllRules()
    } catch (nextError) {
      error.value = errorMessage(nextError)
    } finally {
      pending.value = false
    }
  }

  async function create(input: RuleCreateInput) {
    await fetchJson<ApiRuleRow>('/api/rules', jsonInit('POST', {
      content: input.content,
      priority: input.priority ?? 0,
      edited_by: input.editedBy || 'operator-console',
      ...(input.project ? { project: input.project } : {}),
    }))
    await refresh()
  }

  async function update(id: number, input: RuleUpdateInput) {
    await fetchJson<ApiRuleRow>(`/api/rules/${id}`, jsonInit('PATCH', {
      ...(input.content !== undefined ? { content: input.content } : {}),
      ...(input.priority !== undefined ? { priority: input.priority } : {}),
      ...(input.editedBy !== undefined ? { edited_by: input.editedBy } : {}),
    }))
    await refresh()
  }

  async function remove(id: number) {
    await fetchJson(`/api/rules/${id}`, jsonInit('DELETE'))
    rows.value = rows.value.filter((row) => row.id !== id)
  }

  startOnce('rules', refresh)

  return { rows, pending, error, refresh, create, update, remove }
}

export const useProjects = () => {
  const rows = useState<ProjectSummary[]>('live:projects', () => [])
  const pending = useState<boolean>('live:projects:pending', () => false)
  const error = useState<string | null>('live:projects:error', () => null)

  async function refresh() {
    pending.value = true
    error.value = null
    try {
      rows.value = await loadProjectSummaries()
    } catch (nextError) {
      error.value = errorMessage(nextError)
    } finally {
      pending.value = false
    }
  }

  startOnce('projects', refresh)

  return { rows, pending, error, refresh }
}

export const useServerConfigSnapshot = () => {
  const snapshot = useState<ServerConfigSnapshot>('live:server-config', () => ({
    injectUnified: false,
    telemetryEnabled: false,
    enforceSourceProject: false,
    contextObservations: '—',
    contextMaxTokens: '—',
    contextSessionCount: '—',
    vectorStrategy: '—',
    databaseMaxConns: '—',
    logBufferSize: '—',
  }))
  const pending = useState<boolean>('live:server-config:pending', () => false)
  const error = useState<string | null>('live:server-config:error', () => null)

  async function refresh() {
    pending.value = true
    error.value = null
    try {
      const payload = await fetchJson<ApiConfig>('/api/config')
      snapshot.value = {
        injectUnified: Boolean(payload.memory?.inject_unified),
        telemetryEnabled: Boolean(payload.features?.telemetry_enabled),
        enforceSourceProject: Boolean(payload.features?.enforce_source_project),
        contextObservations: displayValue(payload.context?.observations),
        contextMaxTokens: displayValue(payload.context?.max_tokens),
        contextSessionCount: displayValue(payload.context?.session_count),
        vectorStrategy: displayValue(payload.storage?.vector_strategy),
        databaseMaxConns: displayValue(payload.storage?.database_max_conns),
        logBufferSize: displayValue(payload.storage?.log_buffer_size),
      }
    } catch (nextError) {
      error.value = errorMessage(nextError)
    } finally {
      pending.value = false
    }
  }

  startOnce('server-config', refresh)

  return { snapshot, pending, error, refresh }
}

export const useHealthSnapshot = () => {
  const snapshot = useState<HealthSnapshot>('live:health', () => ({
    overall: 'unknown',
    components: [],
    embedding: {
      chunkCount: '—',
      withVectors: '—',
      dimension: '—',
      coverage: '—',
    },
    hasEmbedding: false,
  }))
  const pending = useState<boolean>('live:health:pending', () => false)
  const error = useState<string | null>('live:health:error', () => null)

  async function refresh() {
    pending.value = true
    error.value = null
    try {
      const [selfcheck, vnext] = await Promise.all([
        fetchJson<ApiSelfcheck>('/api/selfcheck'),
        fetchJson<ApiStatsVnext>('/api/stats/vnext'),
      ])

      const embedding = vnext.embedding
      snapshot.value = {
        overall: selfcheck.overall || 'unknown',
        components: (selfcheck.components || []).map((component) => ({
          name: component.name || 'unknown',
          status: component.status || 'unhealthy',
        })),
        embedding: {
          chunkCount: displayValue(embedding?.chunk_count),
          withVectors: displayValue(embedding?.memories_with_chunks ?? embedding?.active_memory_count),
          dimension: displayValue(embedding?.dimension),
          coverage: percentValue(embedding?.embedding_coverage),
        },
        hasEmbedding: Boolean(embedding),
      }
    } catch (nextError) {
      error.value = errorMessage(nextError)
    } finally {
      pending.value = false
    }
  }

  startOnce('health', refresh)

  return { snapshot, pending, error, refresh }
}

export const useModels = () => ([
  { id: 'recall/embedding-small', provider: 'OpenAI-compatible', health: 'ok', costs: 'In $0.01 · Out —' },
  { id: 'recall/reranker-v2', provider: 'LM Studio', health: 'standby', costs: 'local' },
  { id: 'engram/ops-llm', provider: 'OpenAI-compatible', health: 'ok', costs: 'In $0.00 · Out $0.01' },
  { id: 'fallback/gpt-4-1-mini', provider: 'OpenAI', health: 'degraded', costs: 'In $0.04 · Out $0.16' },
] as ModelRow[])

export const useServerInfo = () => {
  const config = useRuntimeConfig().public
  const base = apiBase()
  const state = useState('live:server-info', () => ({
    host: displayHost(base, config.apiDisplayHost as string | undefined),
    version: '—',
    uptime: '—',
    health: '—',
    noise: '—',
  }))
  const info = state.value

  startOnce('server-info', async () => {
    const [statsResult, selfcheckResult, vnextResult] = await Promise.allSettled([
      fetchJson<ApiStats>('/api/stats'),
      fetchJson<ApiSelfcheck>('/api/selfcheck'),
      fetchJson<ApiStatsVnext>('/api/stats/vnext'),
    ])

    const next = {
      host: displayHost(base, config.apiDisplayHost as string | undefined),
      version: '—',
      uptime: '—',
      health: '—' as string | number,
      noise: '—' as string | number,
    }

    if (statsResult.status === 'fulfilled' && statsResult.value.uptime) {
      next.uptime = statsResult.value.uptime
    }

    if (selfcheckResult.status === 'fulfilled') {
      next.version = selfcheckResult.value.version || next.version
      next.uptime = selfcheckResult.value.uptime || next.uptime
      const degraded = (selfcheckResult.value.components || []).filter((component) => component.status && component.status !== 'healthy').length
      next.health = degraded
    }

    if (vnextResult.status === 'fulfilled') {
      next.noise = normalizeNoise(vnextResult.value.noise_ratio)
    }

    Object.assign(info, next)
  })

  return info
}
