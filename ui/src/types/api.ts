export interface SDKSessionItem {
  id: number
  claude_session_id: string
  project: string
  status: string
  started_at: string
  completed_at: string | null
  prompt_counter: number
  user_prompt: string
}

export interface SDKSessionListResponse {
  sessions: SDKSessionItem[]
  total: number
  limit: number
  offset: number
}

export interface RetrievalStats {
  total_requests: number
  observations_served: number
  verified_stale: number
  deleted_invalid: number
  search_requests: number
  context_injections: number
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

export interface SSEEvent {
  type: 'processing_status' | 'session' | 'heartbeat' | 'connected'
  title?: string
  action?: string
  project?: string
  isProcessing?: boolean
  queueDepth?: number
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
