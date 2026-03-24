/**
 * EngramRestClient — typed REST client for the engram HTTP API.
 *
 * All methods return null on error and update the availability tracker.
 * No method ever throws to its caller — engram failures must not block agent operation.
 */

import { AvailabilityTracker } from './availability.js';
import type { PluginConfig } from './config.js';

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

export interface Observation {
  id: number;
  title: string;
  type: string;
  scope?: string;
  narrative?: string;
  facts?: string[];
  tags?: string[];
  similarity?: number;
  project?: string;
}

export interface ContextInjectResponse {
  observations: Observation[];
  sessionId?: number;
}

export interface ContextSearchResponse {
  observations: Observation[];
  /**
   * Observations flagged always_inject=true in the server config.
   * These are behavioral rules that must be rendered in every context injection
   * regardless of query relevance.
   */
  always_inject?: Observation[];
}

export interface SessionInitResponse {
  sessionDbId: number;
  promptNumber: number;
  skipped?: boolean;
}

export interface HealthResponse {
  status: string;
  version?: string;
}

export interface SelfCheckResponse {
  components: Record<string, { status: string; message?: string }>;
}

export interface BulkObservationInput {
  title: string;
  content: string;
  type: string;
  project: string;
  scope?: string;
  tags?: string[];
}

/** @deprecated Use BulkObservationInput instead. */
export type BulkImportRequest = BulkObservationInput;

export interface BulkImportResponse {
  imported: number;
  skipped_duplicates: number;
  errors?: string[];
}

export interface BulkDeleteResponse {
  deleted: number;
}

/** A single observation returned by the decisions search endpoint. */
export interface DecisionSearchObservation {
  title?: string;
  narrative?: string;
  concepts?: string[];
  rejected?: string[];
}

/** Response from POST /api/decisions/search. */
export interface DecisionSearchResponse {
  observations: DecisionSearchObservation[];
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

export class EngramRestClient {
  private readonly baseUrl: string;
  private readonly token: string;
  private readonly defaultTimeoutMs: number;
  readonly availability: AvailabilityTracker;

  constructor(config: PluginConfig) {
    // Extract origin from potentially path-bearing URL
    this.baseUrl = extractOrigin(config.url);
    this.token = config.token;
    this.defaultTimeoutMs = config.timeoutMs;
    this.availability = new AvailabilityTracker();
  }

  // ---------------------------------------------------------------------------
  // Endpoints
  // ---------------------------------------------------------------------------

  /**
   * Fetch session context for injection (static session-level context).
   * POST /api/context/inject
   */
  async getContextInject(
    agentId: string,
    cwd?: string,
  ): Promise<ContextInjectResponse | null> {
    return this.post<ContextInjectResponse>('/api/context/inject', {
      agent_id: agentId,
      ...(cwd ? { cwd } : {}),
    });
  }

  /**
   * Per-turn context search.
   * POST /api/context/search
   */
  async searchContext(body: {
    project: string;
    query: string;
    cwd?: string;
    agent_id?: string;
    /** Source identifier passed through to the server for analytics/routing. */
    source?: string;
  }): Promise<ContextSearchResponse | null> {
    return this.post<ContextSearchResponse>('/api/context/search', body);
  }

  /**
   * Search for relevant decisions.
   * POST /api/decisions/search
   */
  async searchDecisions(body: {
    query: string;
    project: string;
    limit?: number;
  }): Promise<DecisionSearchResponse | null> {
    return this.post('/api/decisions/search', body);
  }

  /**
   * Track a search miss for self-tuning analytics.
   * POST /api/analytics/search-misses (fire-and-forget)
   */
  async trackSearchMiss(body: {
    project: string;
    query: string;
  }): Promise<void> {
    void this.post('/api/analytics/search-misses', body, 3000);
  }

  /**
   * Ingest a tool event for self-learning.
   * POST /api/events/ingest (fire-and-forget — returns void)
   */
  async ingestEvent(body: {
    session_id: string;
    project: string;
    tool_name: string;
    tool_input: string;
    tool_result: string;
    /** Source identifier passed through to the server for analytics/routing. */
    source?: string;
  }): Promise<void> {
    void this.post('/api/events/ingest', body, 3000);
  }

  /**
   * Submit a transcript for session backfill/extraction.
   * POST /api/backfill/session (fire-and-forget — returns void)
   */
  async backfillSession(body: {
    session_id: string;
    project: string;
    content: string;
  }): Promise<void> {
    void this.post('/api/backfill/session', body, 5000);
  }

  /**
   * Initialize a new engram session for this agent interaction.
   * POST /api/sessions/init
   */
  async initSession(body: {
    claudeSessionId: string;
    project: string;
    prompt?: string;
  }): Promise<SessionInitResponse | null> {
    return this.post<SessionInitResponse>('/api/sessions/init', body);
  }

  /**
   * Mark observation IDs as injected into this session.
   * POST /api/sessions/{id}/mark-injected (fire-and-forget)
   */
  async markInjected(sessionDbId: number, ids: number[]): Promise<void> {
    void this.post(
      `/api/sessions/${sessionDbId}/mark-injected`,
      { ids },
      3000,
    );
  }

  /**
   * Health check.
   * GET /api/health
   */
  async health(): Promise<HealthResponse | null> {
    return this.get<HealthResponse>('/api/health', 3000);
  }

  /**
   * Component-level health check.
   * GET /api/selfcheck
   */
  async selfCheck(): Promise<SelfCheckResponse | null> {
    return this.get<SelfCheckResponse>('/api/selfcheck', 5000);
  }

  /**
   * Bulk-import observations.
   * POST /api/observations/bulk-import
   *
   * Server expects: { project, observations: [{ type, title, narrative, scope, concepts }] }
   * Client uses:    { content → narrative, tags → concepts }
   */
  async bulkImport(
    observations: BulkObservationInput[],
  ): Promise<BulkImportResponse | null> {
    if (observations.length === 0) return { imported: 0, skipped_duplicates: 0 };

    // All observations in a batch must share the same project.
    const project = observations[0].project;

    const mapped = observations.map((o) => ({
      type: o.type,
      title: o.title,
      narrative: o.content,
      scope: o.scope,
      concepts: o.tags,
    }));

    return this.post<BulkImportResponse>('/api/observations/bulk-import', {
      project,
      observations: mapped,
    });
  }

  /**
   * Bulk-delete (archive) observations by ID.
   * POST /api/observations/bulk-status  { action: "archive", ids, reason }
   *
   * The server has no dedicated bulk-delete endpoint. Archiving is the closest
   * equivalent — it removes observations from search results and context injection.
   */
  async bulkDelete(ids: string[]): Promise<BulkDeleteResponse | null> {
    const numericIds = ids.map((id) => Number(id)).filter((n) => !Number.isNaN(n));
    if (numericIds.length === 0) return { deleted: 0 };

    const resp = await this.post<{ updated: number; failed: number }>(
      '/api/observations/bulk-status',
      { action: 'archive', ids: numericIds, reason: 'Deleted via memory_forget' },
    );
    if (!resp) return null;
    return { deleted: resp.updated };
  }

  /** Returns true if the server is currently considered reachable. */
  isAvailable(): boolean {
    return this.availability.isAvailable();
  }

  // ---------------------------------------------------------------------------
  // Internal HTTP helpers
  // ---------------------------------------------------------------------------

  private async get<T>(
    path: string,
    timeoutMs?: number,
  ): Promise<T | null> {
    return this.request<T>('GET', path, undefined, timeoutMs);
  }

  private async post<T>(
    path: string,
    body: unknown,
    timeoutMs?: number,
  ): Promise<T | null> {
    return this.request<T>('POST', path, body, timeoutMs);
  }

  private async request<T>(
    method: string,
    path: string,
    body: unknown,
    timeoutMs?: number,
  ): Promise<T | null> {
    if (!this.availability.isAvailable()) return null;

    const url = this.baseUrl + path;
    const timeout = timeoutMs ?? this.defaultTimeoutMs;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeout);

    try {
      const headers: Record<string, string> = {
        'Authorization': `Bearer ${this.token}`,
      };
      if (body !== undefined) {
        headers['Content-Type'] = 'application/json';
      }

      const response = await fetch(url, {
        method,
        headers,
        body: body !== undefined ? JSON.stringify(body) : undefined,
        signal: controller.signal,
      });

      const text = await response.text();
      if (!response.ok) {
        throw new Error(`HTTP ${response.status} ${response.statusText}: ${text}`);
      }

      this.availability.recordSuccess();

      if (!text) return null;
      return JSON.parse(text) as T;
    } catch (err: unknown) {
      this.availability.recordFailure();
      const msg = err instanceof Error ? err.message : String(err);
      console.error(`[engram] ${method} ${path} failed: ${msg}`);
      return null;
    } finally {
      clearTimeout(timer);
    }
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Extract the origin (protocol + host) from a URL that may include a path.
 * Falls back to trimming the trailing segment on parse failure.
 */
function extractOrigin(rawUrl: string): string {
  const trimmed = rawUrl.trim();
  try {
    const parsed = new URL(trimmed);
    return `${parsed.protocol}//${parsed.host}${parsed.pathname.replace(/\/+$/, '')}`;
  } catch {
    return trimmed.replace(/\/$/, '');
  }
}
