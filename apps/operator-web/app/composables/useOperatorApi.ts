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

export async function fetchProjects(): Promise<string[]> {
  return apiGet<string[]>('/projects')
}

export async function fetchStats(project?: string | null): Promise<Stats> {
  const params = new URLSearchParams()
  if (project) params.append('project', project)
  const query = params.toString()
  return apiGet<Stats>(`/stats${query ? `?${query}` : ''}`)
}

export async function fetchSessions(params: {
  project?: string
  limit?: number
  offset?: number
} = {}): Promise<SessionListResponse> {
  const searchParams = new URLSearchParams()
  if (params.project) searchParams.set('project', params.project)
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
  if (params.project) searchParams.set('project', params.project)
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
} = {}): Promise<IssueListResponse> {
  const searchParams = new URLSearchParams()
  if (params.project) searchParams.set('project', params.project)
  if (params.sourceProject) searchParams.set('source_project', params.sourceProject)
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

export async function fetchTrackedProjects(): Promise<string[]> {
  try {
    const data = await apiGet<{ projects?: string[] }>('/issues/tracked-projects')
    return data.projects || []
  } catch {
    return []
  }
}

export async function createIssue(data: {
  title: string
  body?: string
  type?: string
  priority?: string
  target_project: string
}): Promise<{ id: number; message: string }> {
  const targetProject = data.target_project.trim()
  if (!targetProject) {
    throw new Error('target_project is required')
  }

  return apiPost<{ id: number; message: string }>('/issues', {
    ...data,
    target_project: targetProject,
    type: data.type || 'task',
    priority: data.priority || 'medium',
    source_project: 'dashboard',
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

export async function fetchCredential(name: string, project?: string): Promise<{ name: string; value: string }> {
  const params = project ? `?project=${encodeURIComponent(project)}` : ''
  return apiGet<{ name: string; value: string }>(`/vault/credentials/${encodeURIComponent(name)}${params}`)
}

export async function deleteCredential(name: string, project?: string): Promise<void> {
  const params = project ? `?project=${encodeURIComponent(project)}` : ''
  await apiDelete<Record<string, unknown>>(`/vault/credentials/${encodeURIComponent(name)}${params}`)
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

export async function fetchMemories(project: string, limit = 100): Promise<Memory[]> {
  const params = new URLSearchParams({
    project,
    limit: String(limit),
  })
  return apiGet<Memory[]>(`/memories?${params.toString()}`)
}
