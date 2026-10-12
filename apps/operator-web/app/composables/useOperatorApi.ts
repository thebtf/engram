import { parseProjectCatalog, parseProjectResolution, parseRetainedPage, parseReaderPage, credentialBinding, assertCredentialMetadata, UnresolvedProjectError, type ProjectCatalog, type RetainedKind, type RetainedPage, type ReaderPage, type MemoryReaderQuery } from '../../../../ui/src/utils/projectCatalog'

export interface NullableStringValue {
  String: string
  Valid: boolean
}

export interface SDKSession {
  id: number
  claude_session_id: string
  project: string
  status: string
  started_at: string
  started_at_epoch: number
  prompt_counter: number
  user_prompt: NullableStringValue
}

export interface SessionListResponse {
  sessions: SDKSession[]
  total: number
  limit: number
  offset: number
}

export interface Rule {
  id: number
  project?: string | null
  content: string
  edited_by?: string
  priority: number
  version: number
  created_at: string
  updated_at: string
}

export interface RuleListResponse {
  rules: Rule[]
  total: number
}

export interface Issue {
  id: number
  title: string
  body: string
  status: 'open' | 'acknowledged' | 'resolved' | 'reopened' | 'closed' | 'rejected'
  priority: 'critical' | 'high' | 'medium' | 'low'
  type: 'bug' | 'feature' | 'improvement' | 'task'
  source_project: string
  target_project: string
  source_agent: string
  labels: string[]
  comment_count?: number
  created_at: string
  updated_at: string
}

export interface IssueComment {
  id: number
  issue_id: number
  author_project: string
  author_agent: string
  body: string
  created_at: string
}

export interface IssueListResponse {
  issues: Issue[]
  total: number
  project_names?: Record<string, string>
}

export interface IssueDetailResponse {
  issue: Issue
  comments: IssueComment[]
  comment_count: number
  source_project_display_name?: string
  target_project_display_name?: string
}

export interface VaultCredential {
  id: number
  name: string
  scope: string
  project?: string
  project_id?: string | null
  created_at: string
  updated_at?: string
}

export interface VaultStatus {
  encrypted: boolean
  key_fingerprint?: string
  credential_count: number
}

export interface RetrievalStats {
  total_requests: number
  observations_served: number
  search_requests: number
  context_injections: number
  stale_excluded?: number
  fresh_count?: number
  duplicates_removed?: number
}

export interface Stats {
  uptime: string
  activeSessions: number
  queueDepth: number
  isProcessing: boolean
  connectedClients: number
  sessionsToday: number
  retrieval: RetrievalStats
}

export interface ComponentHealth {
  name: string
  status: 'healthy' | 'degraded' | 'unhealthy'
  message?: string
}

export interface SelfCheckResponse {
  overall: 'healthy' | 'degraded' | 'unhealthy'
  version: string
  uptime: string
  components: ComponentHealth[]
}

export interface MaintenanceStats {
  last_consolidation?: string
  last_maintenance?: string
  observations_consolidated?: number
  stale_removed?: number
}

export interface UpdateInfo {
  available: boolean
  current_version: string
  latest_version: string
  release_notes?: string
  published_at?: string
}

export interface Memory {
  id: number
  project: string
  content: string
  tags: string[]
  source_agent?: string
  edited_by?: string
  status?: string
  tier?: string
  epistemic_type?: string
  privacy_scope?: string
  confidence?: number
  citation_count?: number
  injection_count?: number
  access_count?: number
  created_at: string
  updated_at: string
}

function apiBase(): string {
  return useRuntimeConfig().public.apiBase
}

async function apiGet<T>(path: string): Promise<T> {
  return $fetch<T>(`${apiBase()}${path}`, {
    credentials: 'include',
  })
}

async function apiPost<T>(path: string, body: unknown): Promise<T> {
  return $fetch<T>(`${apiBase()}${path}`, {
    method: 'POST',
    body,
    credentials: 'include',
  })
}

async function apiDelete<T>(path: string): Promise<T> {
  return $fetch<T>(`${apiBase()}${path}`, {
    method: 'DELETE',
    credentials: 'include',
  })
}

let projectCatalog: ProjectCatalog | null = null

export async function fetchProjects(): Promise<ProjectCatalog> {
  const catalog = parseProjectCatalog(await apiGet<unknown>('/projects'))
  projectCatalog = catalog
  return catalog
}

export async function resolveProject(project: string): Promise<string> {
  if (!project || project === 'all' || project === 'global') throw new Error('A concrete project is required')
  const catalog = projectCatalog ?? await fetchProjects()
  if (catalog.items.some(item => item.project_id === project)) return project
  const result = parseProjectResolution(await apiGet<unknown>(`/projects/resolve?${new URLSearchParams({ legacy_id: project })}`))
  if (result.status === 'unresolved') throw new UnresolvedProjectError(result.reference, result.reason)
  const current = catalog.items.some(item => item.project_id === result.project_id) ? catalog : await fetchProjects()
  if (!current.items.some(item => item.project_id === result.project_id)) throw new Error('Resolved project is absent from the catalog')
  return result.project_id
}

export async function fetchRetainedContent(reference: string, kind: RetainedKind, id?: string | number, cursor?: string, q?: string): Promise<RetainedPage> {
  const params = new URLSearchParams({ kind, limit: '50' })
  if (id !== undefined) params.set('id', String(id))
  if (cursor) params.set('cursor', cursor)
  if (q && id === undefined) params.set('q', q)
  const data = await apiGet<unknown>(`/projects/unresolved/${encodeURIComponent(reference)}/${id === undefined ? 'content' : 'history'}?${params}`)
  return parseRetainedPage(data, reference, kind, id)
}

export async function fetchStats(project?: string | null): Promise<Stats> {
  const params = new URLSearchParams()
  if (project) params.append('project', await resolveProject(project))
  const query = params.toString()
  return apiGet<Stats>(`/stats${query ? `?${query}` : ''}`)
}

export async function fetchSessions(params: {
  project?: string
  limit?: number
  offset?: number
} = {}): Promise<SessionListResponse> {
  const searchParams = new URLSearchParams()
  if (params.project) searchParams.set('project', await resolveProject(params.project))
  if (params.limit !== undefined) searchParams.set('limit', String(params.limit))
  if (params.offset !== undefined) searchParams.set('offset', String(params.offset))
  const query = searchParams.toString()
  return apiGet<SessionListResponse>(`/sessions/list${query ? `?${query}` : ''}`)
}

export async function fetchRules(params: {
  project?: string
  limit?: number
} = {}): Promise<RuleListResponse> {
  const searchParams = new URLSearchParams()
  if (params.project) searchParams.set('project', await resolveProject(params.project))
  if (params.limit !== undefined) searchParams.set('limit', String(params.limit))
  const query = searchParams.toString()
  return apiGet<RuleListResponse>(`/rules${query ? `?${query}` : ''}`)
}

export async function fetchIssues(params: {
  project?: string
  status?: string
  limit?: number
  offset?: number
  type?: string
  sourceProject?: string
  sourceAgent?: string
} = {}): Promise<IssueListResponse> {
  const searchParams = new URLSearchParams()
  if (params.project) searchParams.set('project', await resolveProject(params.project))
  if (params.sourceProject) searchParams.set('source_project', await resolveProject(params.sourceProject))
  if (params.sourceAgent) searchParams.set('source_agent', params.sourceAgent)
  if (params.status) searchParams.set('status', params.status)
  if (params.limit !== undefined) searchParams.set('limit', String(params.limit))
  if (params.offset !== undefined) searchParams.set('offset', String(params.offset))
  if (params.type) searchParams.set('type', params.type)
  const query = searchParams.toString()
  return apiGet<IssueListResponse>(`/issues${query ? `?${query}` : ''}`)
}

export async function fetchIssue(id: number): Promise<IssueDetailResponse> {
  return apiGet<IssueDetailResponse>(`/issues/${id}`)
}


export async function createIssue(data: {
  title: string
  body?: string
  type?: string
  priority?: string
  target_project: string
  source_project: string
}): Promise<{ id: number; message: string }> {
  const targetProject = await resolveProject(data.target_project)
  const sourceProject = await resolveProject(data.source_project)

  return apiPost<{ id: number; message: string }>('/issues', {
    ...data,
    target_project: targetProject,
    type: data.type || 'task',
    priority: data.priority || 'medium',
    source_project: sourceProject,
    source_agent: 'human',
  })
}

export async function fetchVaultStatus(): Promise<VaultStatus> {
  const raw = await apiGet<any>('/vault/status')
  return {
    encrypted: raw.key_configured ?? false,
    key_fingerprint: raw.fingerprint ?? raw.key_fingerprint,
    credential_count: raw.credential_count ?? 0,
  }
}

export async function fetchCredentials(): Promise<VaultCredential[]> {
  return apiGet<VaultCredential[]>('/vault/credentials')
}

export async function fetchCredential(credential: VaultCredential): Promise<{ name: string; value: string }> {
  const binding = credentialBinding(credential)
  const params = new URLSearchParams(binding.project_id === null ? { scope: 'global' } : { project: binding.project_id })
  const result = await apiGet<{ name: string; value: string }>(`/vault/credentials/${encodeURIComponent(binding.name)}?${params}`)
  assertCredentialMetadata(result, binding)
  const current = (await fetchCredentials()).find(row => row.id === binding.id)
  if (!current) throw new Error('Credential metadata readback missing')
  assertCredentialMetadata(credentialBinding(current), binding)
  return result
}

export async function deleteCredential(credential: VaultCredential): Promise<void> {
  const binding = credentialBinding(credential)
  const current = (await fetchCredentials()).find(row => row.id === binding.id)
  if (!current) throw new Error('Credential metadata readback missing')
  assertCredentialMetadata(credentialBinding(current), binding)
  const params = new URLSearchParams(binding.project_id === null ? { scope: 'global' } : { project: binding.project_id })
  const result = await apiDelete<{ current_state?: unknown }>(`/vault/credentials/${encodeURIComponent(binding.name)}?${params}`)
  if (result.current_state !== undefined) assertCredentialMetadata(result.current_state, binding)
  if ((await fetchCredentials()).some(row => row.id === binding.id)) throw new Error('Credential deletion readback failed')
}

export async function fetchConfig(): Promise<Record<string, Record<string, unknown>>> {
  return apiGet<Record<string, Record<string, unknown>>>('/config')
}

export async function fetchMaintenanceStats(): Promise<MaintenanceStats> {
  return apiGet<MaintenanceStats>('/maintenance/stats')
}

export async function fetchSelfCheck(): Promise<SelfCheckResponse> {
  return apiGet<SelfCheckResponse>('/selfcheck')
}

export async function checkForUpdate(): Promise<UpdateInfo> {
  return apiGet<UpdateInfo>('/update/check')
}

export async function fetchMemories(query: MemoryReaderQuery): Promise<ReaderPage<Memory>> {
  const project = await resolveProject(query.project)
  const offset = query.offset ?? 0
  const limit = query.limit ?? 100
  const params = new URLSearchParams({ project, offset: String(offset), limit: String(limit) })
  if (query.query) params.set('query', query.query)
  for (const filter of query.filters ?? []) params.append('filter', filter)
  return parseReaderPage(await apiGet<ReaderPage<Memory>>(`/memories?${params}`), project, offset, limit)
}
