/**
 * Mock data — the SEAM. This is the ONLY place the design scaffold invents data.
 * DEVELOPER: replace each function body with live data behind the same shapes. The page
 * components never change — they consume these composables, not raw fetches.
 *
 * Current live-wire policy:
 * - Memories, issues, vault status, vault credentials, and server info are backed by the
 *   current Engram HTTP surface.
 * - Model rows remain mock until there is a direct, truthful model-health endpoint for
 *   this exact UI contract.
 */

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

interface ApiMemory {
  id: number | string
  project?: string
  content?: string
  tags?: string[]
  tier?: string
  epistemic_type?: string
  confidence?: number
  citation_count?: number
  injection_count?: number
  updated_at?: string
  created_at?: string
  status?: string
}

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
  components?: Array<{ status?: 'healthy' | 'degraded' | 'unhealthy' }>
}

interface ApiStatsVnext {
  noise_ratio?: number
}

const DEFAULT_API_BASE = '/api'

function apiBase(): string {
  const configured = useRuntimeConfig().public.apiBase as string | undefined
  return configured && configured.trim() ? configured : DEFAULT_API_BASE
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

async function fetchJson<T>(path: string): Promise<T> {
  const response = await fetch(`${apiBase()}${path}`, {
    credentials: 'include',
  })

  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText} for ${path}`)
  }

  return response.json() as Promise<T>
}

async function fetchText(path: string): Promise<string> {
  const response = await fetch(`${apiBase()}${path}`, {
    credentials: 'include',
  })

  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText} for ${path}`)
  }

  return response.text()
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

function mapTier(value?: string): Memory['tier'] {
  if (value === 'semantic' || value === 'episodic' || value === 'procedural') {
    return value
  }

  return 'procedural'
}

function isNoiseStatus(value?: string): boolean {
  const normalized = String(value || '').trim().toLowerCase()
  return normalized === 'noise' || normalized === 'flagged' || normalized === 'suppressed'
}

function normalizeNoise(value?: number): string {
  if (typeof value !== 'number' || Number.isNaN(value)) return '—'
  return value.toFixed(2)
}

function mapMemoryRow(row: ApiMemory): Memory {
  return {
    id: String(row.id),
    content: row.content || '—',
    tags: Array.isArray(row.tags) ? row.tags : [],
    tier: mapTier(row.tier),
    type: row.epistemic_type || 'note',
    project: row.project || 'global',
    conf: typeof row.confidence === 'number' ? row.confidence : 0,
    cite: typeof row.citation_count === 'number' ? row.citation_count : 0,
    inj: typeof row.injection_count === 'number' ? row.injection_count : 0,
    age: compactAge(row.updated_at || row.created_at),
    noise: isNoiseStatus(row.status),
  }
}

async function loadAllMemories(): Promise<Memory[]> {
  const projects = await fetchJson<string[]>('/api/projects')
  const uniqueProjects = [...new Set(projects.filter(Boolean))]
  const combined: Memory[] = []

  for (const project of uniqueProjects) {
    const raw = (await fetchText(`/api/memories?project=${encodeURIComponent(project)}&limit=200`)).trim()
    if (!raw) continue

    let parsed: unknown
    try {
      parsed = JSON.parse(raw)
    } catch {
      continue
    }

    if (!Array.isArray(parsed)) continue

    for (const row of parsed as ApiMemory[]) {
      combined.push(mapMemoryRow(row))
    }
  }

  const deduped = new Map<string, Memory>()
  for (const row of combined) {
    deduped.set(`${row.project}:${row.id}`, row)
  }

  return [...deduped.values()]
}

export const useMemories = () => {
  const state = useState<Memory[]>('live:memories', () => [])
  const rows = state.value

  startOnce('memories', async () => {
    const liveRows = await loadAllMemories()
    replaceArray(rows, liveRows)
  })

  return rows
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
