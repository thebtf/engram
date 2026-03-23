import type { Observation } from './observation'
import type { UserPrompt } from './prompt'
import type { SessionSummary } from './summary'

export type FeedItemType = 'observation' | 'prompt' | 'summary'

export interface ObservationFeedItem extends Observation {
  itemType: 'observation'
  timestamp: Date
}

export interface PromptFeedItem extends UserPrompt {
  itemType: 'prompt'
  timestamp: Date
}

export interface SummaryFeedItem extends SessionSummary {
  itemType: 'summary'
  timestamp: Date
}

export type FeedItem = ObservationFeedItem | PromptFeedItem | SummaryFeedItem

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
  TotalRequests: number
  ObservationsServed: number
  VerifiedStale: number
  DeletedInvalid: number
  SearchRequests: number
  ContextInjections: number
}

export interface Stats {
  uptime: string
  activeSessions: number
  queueDepth: number
  isProcessing: boolean
  connectedClients: number
  sessionsToday: number
  retrieval: RetrievalStats
  observationCount?: number
}

export interface SSEEvent {
  type: 'processing_status' | 'observation' | 'session' | 'prompt' | 'summary' | 'heartbeat' | 'connected'
  title?: string
  action?: string
  project?: string
  isProcessing?: boolean
  queueDepth?: number
}

export type FilterType = 'all' | 'observations' | 'summaries' | 'prompts'

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

export interface GraphStats {
  enabled: boolean
  nodeCount: number
  edgeCount: number
  avgDegree: number
  maxDegree: number
  minDegree: number
  medianDegree: number
  edgeTypes: Record<string, number>
  config: {
    maxHops: number
    branchFactor: number
    edgeWeight: number
    rebuildIntervalMin: number
  }
  message?: string
}

export interface VectorMetrics {
  enabled: boolean
  query_count: number
  avg_latency_ms: number
  p50_latency_ms: number
  p95_latency_ms: number
  p99_latency_ms: number
  total_documents: number
  uptime: string
  message?: string
}
