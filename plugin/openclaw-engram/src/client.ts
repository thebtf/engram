/**
 * EngramRestClient — typed REST client for the engram HTTP API.
 *
 * All methods return null on error and update the availability tracker.
 * No method ever throws to its caller — engram failures must not block agent operation.
 */

import { randomUUID } from 'node:crypto';
import { AvailabilityTracker } from './availability.js';
import type { PluginConfig } from './config.js';
import { resolveIdentity, validateCanonicalProjectV2, validateProjectSelectorV2, type ProjectIdentity, type ProjectIdentityV2 } from './identity.js';
import { buildProjectIdentityV3, type ProjectIdentityV3 } from './project-identity-v3.js';

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
  rule_router?: RuleRouterPayload;
}

export interface ContextSearchResponse {
  observations: Observation[];
  /**
   * Observations flagged always_inject=true in the server config.
   * These are behavioral rules that must be rendered in every context injection
   * regardless of query relevance.
   */
  always_inject?: Observation[];
  rule_router?: RuleRouterPayload;
}

export interface RuleRouterPacket {
  rule_version_id?: number;
  legacy_behavioral_rule_id?: number;
  bucket?: string;
  scope?: string;
  audience?: string;
  content?: string;
  summary?: string;
  evidence_handles?: string[];
  state?: string;
  budget_class?: string;
  priority?: number;
  suppression_reason?: string;
}

export interface RuleRouterPayload {
  enabled?: boolean;
  mode?: string;
  kernel_count?: number;
  contextual_count?: number;
  suppressed_count?: number;
  budget_outcome?: string;
  kernel?: RuleRouterPacket[];
  contextual?: RuleRouterPacket[];
  suppressed?: RuleRouterPacket[];
  fallback_reason?: string;
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

export interface ProjectRegistrationSuccess {
  ok: true;
  canonicalProject: string;
}

export interface ResolvedProjectRegistrationSuccess extends ProjectRegistrationSuccess {
  projectSelector: string;
  projectIdentityV2?: ProjectIdentityV2;
 projectIdentityV3?: ProjectIdentityV3;
}

export interface ProjectRegistrationFailure {
  ok: false;
  error: {
    code: string;
    message: string;
    upgradeAction: string;
    httpStatus: number;
  };
}

export type ProjectRegistrationResult = ProjectRegistrationSuccess | ProjectRegistrationFailure;
export type ResolvedProjectRegistrationResult = ResolvedProjectRegistrationSuccess | ProjectRegistrationFailure;

export interface OutcomeRetirement {
  contract_version: 'engram.outcome-retirement.v1';
  code: 'OUTCOME_CALLBACK_RETIRED';
  action: 'upgrade_outcome_adapter';
}

export type OutcomeRecordingResult = boolean | OutcomeRetirement;

export function isOutcomeRetirement(result: OutcomeRecordingResult): result is OutcomeRetirement {
  return typeof result !== 'boolean';
}

function parseOutcomeRetirement(payload: unknown): OutcomeRetirement | null {
  if (payload === null || typeof payload !== 'object') return null;
  const { contract_version, code, action } = payload as Record<string, unknown>;
  if (
    contract_version !== 'engram.outcome-retirement.v1' ||
    code !== 'OUTCOME_CALLBACK_RETIRED' ||
    action !== 'upgrade_outcome_adapter'
  ) return null;
  return { contract_version, code, action };
}

function parseOutcomeRetirementPayload(text: string): OutcomeRetirement | null {
  try {
    return parseOutcomeRetirement(JSON.parse(text));
  } catch {
    return null;
  }
}

export interface ProjectRegistrationClient {
  registerAndResolveProject(identity: ProjectIdentity, selector: string): Promise<ProjectRegistrationResult>;
 readonly clientInstanceId?: string;
}

/**
 * Register an explicit project override as a selector-only shared scope.
 * Without an override, resolve and register the workspace's full identity.
 */
export async function resolveAndRegisterProject(
  client: ProjectRegistrationClient,
  agentId: string,
  workspaceDir: string | undefined,
  configuredProject?: string,
): Promise<ResolvedProjectRegistrationResult> {
  try {
  const clientInstanceId = client.clientInstanceId;
  const identity: ProjectIdentity = clientInstanceId !== undefined
   ? resolveIdentity(agentId, workspaceDir, clientInstanceId)
   : configuredProject !== undefined
      ? { projectId: configuredProject, agentId }
      : resolveIdentity(agentId, workspaceDir);
  const selector = clientInstanceId !== undefined ? identity.projectId : configuredProject ?? identity.projectId;
    const registration = await client.registerAndResolveProject(identity, selector);
    if (!registration.ok) return registration;
    return {
      ...registration,
      projectSelector: selector,
   ...(identity.projectIdentityV3 ? { projectIdentityV3: identity.projectIdentityV3 } : {}),
      ...(identity.projectIdentityV2 ? { projectIdentityV2: identity.projectIdentityV2 } : {}),
    };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
  if (message === 'PROJECT_DESCRIPTOR_INVALID') return projectDescriptorFailure();
    const invalid = message.startsWith('PROJECT_IDENTITY_INVALID:');
    return projectRegistrationFailure(
      invalid ? 'PROJECT_IDENTITY_INVALID' : 'PROJECT_IDENTITY_UNAVAILABLE',
      message,
      invalid ? 'regenerate_project_identity_v2' : 'retry_project_identity_registration',
      invalid ? 400 : 503,
    );
  }
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
// Issue types
// ---------------------------------------------------------------------------

export interface Issue {
  id: number;
  title: string;
  body: string;
  status: string;
  priority: string;
  source_project: string;
  target_project: string;
  source_agent: string;
  labels: string[];
  comment_count?: number;
  created_at: string;
  updated_at: string;
}

export interface IssueComment {
  id: number;
  issue_id: number;
  author_project: string;
  author_agent: string;
  body: string;
  created_at: string;
}

export interface IssueListResponse {
  issues: Issue[];
  total: number;
}

export interface IssueDetailResponse {
  issue: Issue;
  comments: IssueComment[];
  comment_count: number;
}

export interface CreateIssueRequest {
  title: string;
  body?: string;
  priority?: string;
  source_project?: string;
  target_project: string;
  source_agent?: string;
  created_by_session?: string;
  labels?: string[];
}

export interface CreateIssueResponse {
  id: number;
  message: string;
}

export interface UpdateIssueRequest {
  status?: string;
  comment?: string;
  source_project?: string;
  source_agent?: string;
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

export class EngramRestClient {
  private readonly baseUrl: string;
  private readonly token: string;
  private readonly defaultTimeoutMs: number;
  private readonly completedProjectRegistrations = new Map<string, ProjectRegistrationResult>();
  private readonly inFlightProjectRegistrations = new Map<string, Promise<ProjectRegistrationResult>>();
 private readonly v3ContextDescriptors = new Map<string, ProjectIdentityV3>();
  readonly availability: AvailabilityTracker;
 readonly clientInstanceId?: string;

  constructor(config: PluginConfig) {
    // Extract origin from potentially path-bearing URL
    this.baseUrl = extractOrigin(config.url);
    this.token = config.token;
    this.defaultTimeoutMs = config.timeoutMs;
    this.availability = new AvailabilityTracker();
  this.clientInstanceId = config.clientInstanceId;
  }

  // ---------------------------------------------------------------------------
  // Endpoints
  // ---------------------------------------------------------------------------

  /**
   * Registration barrier for every project-scoped OpenClaw access.
   *
   * The full identity and outer compatibility selector are resolved before a
   * caller sends its first data request. Concurrent and late calls reuse the
   * same result. Project metadata routes a namespace; the bearer header remains
   * the independent authorization gate.
   */
  async registerAndResolveProject(
    identity: ProjectIdentity,
    selector: string,
  ): Promise<ProjectRegistrationResult> {
    const requestedDescriptor = identity.projectIdentityV3;
    let descriptor: ProjectIdentityV3 | undefined;
    let validatedIdentity = identity;
    if (requestedDescriptor !== undefined) {
      const validatedDescriptor = validateProjectDescriptorV3(requestedDescriptor);
      if (!validatedDescriptor) return projectDescriptorFailure();
      descriptor = validatedDescriptor;
      validatedIdentity = { ...identity, projectIdentityV3: descriptor };
    }
    let validatedSelector = selector;
    if (!descriptor) {
      try {
        validatedSelector = validateProjectSelectorV2(selector);
      } catch {
        return {
          ok: false,
          error: {
            code: 'PROJECT_IDENTITY_INVALID',
            message: 'project selector is empty or malformed',
            upgradeAction: 'regenerate_project_identity_v2',
            httpStatus: 400,
          },
        };
      }
    }

    const key = JSON.stringify(descriptor ?? [validatedSelector, identity.projectIdentityV2 ?? null]);
    const completed = this.completedProjectRegistrations.get(key);
    if (completed) return completed;
    const inFlight = this.inFlightProjectRegistrations.get(key);
    if (inFlight) return inFlight;

    const registration = this.performProjectRegistration(validatedIdentity, validatedSelector);
    this.inFlightProjectRegistrations.set(key, registration);
    try {
      const result = await registration;
      if (result.ok && descriptor) {
        this.v3ContextDescriptors.set(selector, descriptor);
        this.v3ContextDescriptors.set(result.canonicalProject, descriptor);
      }
      if (result.ok || result.error.httpStatus === 400 || result.error.httpStatus === 409) {
        this.completedProjectRegistrations.set(key, result);
      }
      return result;
    } finally {
      this.inFlightProjectRegistrations.delete(key);
    }
  }

  private async performProjectRegistration(
    identity: ProjectIdentity,
    selector: string,
  ): Promise<ProjectRegistrationResult> {
    if (!this.availability.isAvailable()) {
      return projectRegistrationFailure(
        'PROJECT_IDENTITY_UNAVAILABLE',
        'engram is temporarily unavailable',
        'retry_project_identity_registration',
        503,
      );
    }

    const path = '/api/context/inject';
    const url = this.baseUrl + path;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.defaultTimeoutMs);
  const body = identity.projectIdentityV3
   ? {
    project_descriptor: identity.projectIdentityV3,
    identity_only: true,
   }
   : {
      project: selector,
      ...(identity.projectIdentityV2 ? { project_identity: identity.projectIdentityV2 } : {}),
      identity_only: true,
    };

    try {
      const response = await fetch(url, {
        method: 'POST',
        headers: {
          'Authorization': `Bearer ${this.token}`,
          'Content-Type': 'application/json',
          'X-Engram-Project-Identity-Adapter': 'openclaw',
          'X-Request-ID': randomUUID(),
        },
        body: JSON.stringify(body),
        signal: controller.signal,
      });
      const text = await response.text();
      let payload: unknown = {};
      if (text) {
        try { payload = JSON.parse(text); } catch { payload = {}; }
      }

      if (!response.ok) {
        if (response.status >= 500 || response.status === 401 || response.status === 403) {
          this.availability.recordFailure();
        }
        const parsed = parseProjectRegistrationError(payload, response.status);
        return projectRegistrationFailure(parsed.code, parsed.message, parsed.upgradeAction, response.status);
      }

      const canonical = readCanonicalProject(payload, identity.projectIdentityV3);
      if (!canonical) {
        this.availability.recordFailure();
        return projectRegistrationFailure(
          'PROJECT_IDENTITY_UNAVAILABLE',
          'project identity registration response is malformed',
          'retry_project_identity_registration',
          503,
        );
      }
      this.availability.recordSuccess();
      return { ok: true, canonicalProject: canonical };
    } catch (err: unknown) {
      this.availability.recordFailure();
      const message = err instanceof Error ? err.message : String(err);
      return projectRegistrationFailure(
        'PROJECT_IDENTITY_UNAVAILABLE',
        message,
        'retry_project_identity_registration',
        503,
      );
    } finally {
      clearTimeout(timer);
    }
  }

  private scopedProjectRequest<T extends { project: string; agent_id?: string }>(
    body: T,
  ): T | (Omit<T, 'project' | 'agent_id'> & { project_descriptor: ProjectIdentityV3 }) | null {
    if (this.clientInstanceId === undefined) return body;
    const descriptor = this.v3ContextDescriptors.get(body.project);
    if (!descriptor) return null;
    const { project: _, agent_id: _agentID, ...request } = body;
    return { ...request, project_descriptor: descriptor };
  }

  /**
   * Fetch session context for injection (static session-level context).
   * POST /api/context/inject
   */
  async getContextInject(
    agentId: string,
    cwd?: string,
    project?: string,
    projectIdentityV2?: ProjectIdentityV2,
  ): Promise<ContextInjectResponse | null> {
  // A V3 context request only reuses the descriptor carried through successful registration.
  const projectDescriptor = project ? this.v3ContextDescriptors.get(project) : undefined;
  if (this.clientInstanceId !== undefined && !projectDescriptor) return null;
  return this.post<ContextInjectResponse>('/api/context/inject', projectDescriptor
   ? { project_descriptor: projectDescriptor }
   : {
      agent_id: agentId,
      ...(cwd ? { cwd } : {}),
      ...(project ? { project } : {}),
      ...(projectIdentityV2 ? { project_identity: projectIdentityV2 } : {}),
    }, 15_000);
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
    /** Search preset: decisions, changes, how_it_works. */
    preset?: string;
  }): Promise<ContextSearchResponse | null> {
    // Context search does vector query (embedding + pgvector) — needs more than default 5s.
    const request = this.scopedProjectRequest(body);
    return request ? this.post<ContextSearchResponse>('/api/context/search', request, 15_000) : null;
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
   * Server expects: { project, session_id?, observations: [{ type, title, narrative, scope, concepts }] }
   * Client uses:    { content → narrative, tags → concepts }
   *
   * Passing sessionId reuses the caller's existing session instead of creating a new
   * synthetic one per call, preventing phantom session proliferation.
   */
  async bulkImport(
    observations: BulkObservationInput[],
    sessionId?: string,
  ): Promise<BulkImportResponse | null> {
    if (this.clientInstanceId !== undefined) return null;
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
      ...(sessionId ? { session_id: sessionId } : {}),
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
  /**
   * Suppress an observation (reversible soft-hide from search results).
   * POST /api/observations/bulk-status { action: "suppress", ids: [id] }
   */
  async suppressObservation(id: number): Promise<boolean> {
    const resp = await this.post<{ updated: number }>('/api/observations/bulk-status', {
      action: 'suppress',
      ids: [id],
    });
    return resp != null && resp.updated > 0;
  }

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

  /**
   * Rate an observation as useful or not useful.
   * MCP uses feedback(action="rate", id, rating="useful"|"not_useful").
   */
  async rateObservation(id: number, rating: 'useful' | 'not_useful'): Promise<boolean> {
    const resp = await this.post<{
      result?: { content?: Array<{ type?: string; text?: string }>; isError?: boolean };
      error?: { code?: number; message?: string };
    }>(
      '/mcp',
      {
        jsonrpc: '2.0',
        id: 1,
        method: 'tools/call',
        params: {
          name: 'feedback',
          arguments: {
            action: 'rate',
            id,
            rating,
          },
        },
      },
    );

    if (!resp) return false;

    if (resp.error) {
      console.error(`[engram] rateObservation failed: ${resp.error.message ?? 'unknown error'}`);
      return false;
    }

    if (resp.result?.isError) {
      console.error('[engram] rateObservation failed: MCP tool result flagged as error');
      return false;
    }

    const content = resp.result?.content;
    if (!Array.isArray(content) || content.length === 0) {
      return false;
    }

    return content.some((item) => typeof item?.text === 'string' && item.text.trim().length > 0);
  }

  /**
   * Record session outcome for closed-loop learning.
   * POST /api/sessions/{sessionId}/outcome { outcome, reason }
   * sessionId is the Claude session ID string (not numeric DB ID).
   */
  async setSessionOutcome(
    sessionId: string,
    outcome: string,
    reason?: string,
  ): Promise<OutcomeRecordingResult> {
    if (!this.availability.isAvailable()) return false;

    const path = `/api/sessions/${encodeURIComponent(sessionId)}/outcome`;
    const timeout = 3000;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeout);
    const startMs = Date.now();

    try {
      const response = await fetch(this.baseUrl + path, {
        method: 'POST',
        headers: {
          Authorization: `Bearer ${this.token}`,
          'Content-Type': 'application/json',
          'X-Engram-Project-Identity-Adapter': 'openclaw',
          'X-Request-ID': randomUUID(),
        },
        body: JSON.stringify({ outcome, reason: reason ?? '' }),
        signal: controller.signal,
      });
      const text = await response.text();

      if (response.status === 410) {
        const retirement = parseOutcomeRetirementPayload(text);
        if (retirement) return retirement;
      }
      if (!response.ok) {
        throw new Error(`HTTP ${response.status} ${response.statusText} (${Date.now() - startMs}ms): ${text.slice(0, 200)}`);
      }

      this.availability.recordSuccess();
      return text ? JSON.parse(text) !== null : false;
    } catch (err: unknown) {
      const elapsedMs = Date.now() - startMs;
      this.availability.recordFailure();
      const msg = err instanceof Error ? err.message : String(err);
      const isAbort = err instanceof Error && err.name === 'AbortError';
      console.error(
        `[engram] POST ${path} failed after ${elapsedMs}ms` +
        `${isAbort ? ` (timeout=${timeout}ms)` : ''}: ${msg}`,
      );
      return false;
    } finally {
      clearTimeout(timer);
    }
  }

  /**
   * Get file-context observations for a specific file.
   * GET /api/context/by-file?path={file}&project={project}&limit={limit}
   */
  async getFileContext(
    file: string,
    project: string,
    limit = 5,
    timeoutMs = 3000,
  ): Promise<Observation[]> {
    const params = new URLSearchParams({ path: file, project, limit: String(limit) });
    const resp = await this.get<{ observations: Observation[] }>(
      `/api/context/by-file?${params.toString()}`,
      timeoutMs,
    );
    return resp?.observations ?? [];
  }

  /**
   * Get timeline of observations.
   * POST /api/context/search with timeline params.
   */
  async getTimeline(
    project: string,
    mode: 'recent' | 'anchor' | 'query',
    params?: { query?: string; anchor_id?: number; limit?: number },
  ): Promise<Observation[]> {
    const body: { project: string; mode: 'recent' | 'anchor' | 'query'; query?: string; anchor_id?: number; limit?: number } = { project, mode };
    if (params?.query) body.query = params.query;
    if (params?.anchor_id) body.anchor_id = params.anchor_id;
    if (params?.limit) body.limit = params.limit;
    const request = this.scopedProjectRequest(body);
    const resp = request ? await this.post<{ observations: Observation[] }>('/api/context/search', request, 15_000) : null;
    return resp?.observations ?? [];
  }

  /**
   * Store an encrypted credential in the vault.
   * POST /api/vault/credentials { name, value, scope, project }
   */
  async storeCredential(
    name: string,
    value: string,
    scope: string,
    project?: string,
  ): Promise<boolean> {
    const resp = await this.post<{ success: boolean }>('/api/vault/credentials', {
      name,
      value,
      scope,
      project: project ?? '',
    });
    return resp != null;
  }

  /**
   * Retrieve and decrypt a credential from the vault.
   * GET /api/vault/credentials/{name}
   */
  async getCredential(name: string): Promise<{ name: string; value: string } | null> {
    return this.get<{ name: string; value: string }>(`/api/vault/credentials/${encodeURIComponent(name)}`);
  }

  // ---------------------------------------------------------------------------
  // Issues — cross-project agent issue tracking
  // ---------------------------------------------------------------------------

  async listIssues(params: {
    project?: string;
    source_project?: string;
    status?: string;
    limit?: number;
    offset?: number;
  }): Promise<IssueListResponse | null> {
    const qs = new URLSearchParams();
    if (params.project) qs.set('project', params.project);
    if (params.source_project) qs.set('source_project', params.source_project);
    if (params.status) qs.set('status', params.status);
    if (params.limit) qs.set('limit', String(params.limit));
    if (params.offset) qs.set('offset', String(params.offset));
    const query = qs.toString();
    return this.get<IssueListResponse>(`/api/issues${query ? `?${query}` : ''}`);
  }

  async getIssue(id: number): Promise<IssueDetailResponse | null> {
    return this.get<IssueDetailResponse>(`/api/issues/${id}`);
  }

  async createIssue(body: CreateIssueRequest): Promise<CreateIssueResponse | null> {
    return this.post<CreateIssueResponse>('/api/issues', body);
  }

  async updateIssue(id: number, body: UpdateIssueRequest): Promise<{ message: string } | null> {
    return this.request<{ message: string }>('PATCH', `/api/issues/${id}`, body);
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
    const startMs = Date.now();

    try {
      const headers: Record<string, string> = {
        'Authorization': `Bearer ${this.token}`,
        'X-Engram-Project-Identity-Adapter': 'openclaw',
        'X-Request-ID': randomUUID(),
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
      const elapsedMs = Date.now() - startMs;

      if (!response.ok) {
        throw new Error(`HTTP ${response.status} ${response.statusText} (${elapsedMs}ms): ${text.slice(0, 200)}`);
      }

      this.availability.recordSuccess();

      if (!text) return null;
      return JSON.parse(text) as T;
    } catch (err: unknown) {
      const elapsedMs = Date.now() - startMs;
      this.availability.recordFailure();
      const msg = err instanceof Error ? err.message : String(err);
      const isAbort = err instanceof Error && err.name === 'AbortError';
      console.error(
        `[engram] ${method} ${path} failed after ${elapsedMs}ms` +
        `${isAbort ? ` (timeout=${timeout}ms)` : ''}: ${msg}`,
      );
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

function projectRegistrationFailure(
  code: string,
  message: string,
  upgradeAction: string,
  httpStatus: number,
): ProjectRegistrationFailure {
  return { ok: false, error: { code, message, upgradeAction, httpStatus } };
}

function projectDescriptorFailure(): ProjectRegistrationFailure {
 return projectRegistrationFailure(
  'PROJECT_DESCRIPTOR_INVALID',
  'project descriptor is invalid',
  'repair_project_descriptor',
  400,
 );
}

const V3_DESCRIPTOR_FIELDS: Record<string, true> = {
  version: true,
  anchor_project_id: true,
  name: true,
  scope: true,
  normalized_git_remotes: true,
  legacy_identifiers: true,
  client_instance_id: true,
};
const V3_UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/iu;
const V3_PROJECT_RESOLUTION_FIELDS: Record<string, true> = {
  outcome: true,
  project_key: true,
  resolved_scope: true,
  correlation: true,
  redirect_reference: true,
};

function isValidOpaqueReferenceV3(value: unknown): value is string {
  return typeof value === 'string' &&
    value.length > 0 &&
    Array.from(value).length <= 256 &&
    !/[\p{White_Space}\p{Cc}\/\\@]/u.test(value);
}

function validateProjectDescriptorV3(descriptor: unknown): ProjectIdentityV3 | null {
  try {
    if (typeof descriptor !== 'object' || descriptor === null || Array.isArray(descriptor)) return null;
    const keys = Object.keys(descriptor);
    if (keys.length !== 7 || keys.some((key) => !Object.hasOwn(V3_DESCRIPTOR_FIELDS, key))) return null;
    if (
      !('version' in descriptor) ||
      !('anchor_project_id' in descriptor) ||
      !('name' in descriptor) ||
      !('scope' in descriptor) ||
      !('normalized_git_remotes' in descriptor) ||
      !('legacy_identifiers' in descriptor) ||
      !('client_instance_id' in descriptor)
    ) return null;
    return buildProjectIdentityV3({
      anchor: {
        version: descriptor.version,
        project_id: descriptor.anchor_project_id,
        name: descriptor.name,
        scope: descriptor.scope,
      },
      version: descriptor.version,
      anchor_project_id: descriptor.anchor_project_id,
      name: descriptor.name,
      scope: descriptor.scope,
      normalized_git_remotes: descriptor.normalized_git_remotes,
      legacy_identifiers: descriptor.legacy_identifiers,
      client_instance_id: descriptor.client_instance_id,
    });
  } catch {
    return null;
  }
}

function readCanonicalProject(payload: unknown, descriptor?: ProjectIdentityV3): string {
  if (!payload || typeof payload !== 'object') return '';
  if (descriptor) {
    if (!Object.hasOwn(payload, 'project_resolution_v3') || !('project_resolution_v3' in payload)) return '';
    const resolution = payload.project_resolution_v3;
    if (!resolution || typeof resolution !== 'object' || Array.isArray(resolution)) return '';
    const typedResolution = resolution as Record<string, unknown>;
    const keys = Object.keys(typedResolution);
    if (
      keys.some((key) => !Object.hasOwn(V3_PROJECT_RESOLUTION_FIELDS, key)) ||
      !Object.hasOwn(typedResolution, 'outcome') ||
      !Object.hasOwn(typedResolution, 'project_key') ||
      !Object.hasOwn(typedResolution, 'resolved_scope') ||
      !Object.hasOwn(typedResolution, 'correlation')
    ) return '';
    const { outcome, project_key, resolved_scope, correlation, redirect_reference: redirectReference } = typedResolution;
    if (
      (outcome !== 'PROJECT_RESOLVED' && outcome !== 'PROJECT_REDIRECTED') ||
      keys.length !== (outcome === 'PROJECT_REDIRECTED' ? 5 : 4) ||
      typeof project_key !== 'string' ||
      !V3_UUID.test(project_key) ||
      resolved_scope !== descriptor.scope ||
      !isValidOpaqueReferenceV3(correlation) ||
      (outcome === 'PROJECT_REDIRECTED' && !isValidOpaqueReferenceV3(redirectReference))
    ) return '';
    return project_key;
  }
  if (!('canonical_project' in payload)) return '';
  try {
    return validateCanonicalProjectV2(payload.canonical_project);
  } catch {
    return '';
  }
}

function parseProjectRegistrationError(
  payload: unknown,
  status: number,
): { code: string; message: string; upgradeAction: string } {
  const fallback = status === 400
    ? { code: 'PROJECT_IDENTITY_INVALID', upgradeAction: 'regenerate_project_identity_v2' }
    : status === 409
      ? { code: 'PROJECT_IDENTITY_AMBIGUOUS', upgradeAction: 'send_project_identity_v2' }
      : { code: 'PROJECT_IDENTITY_UNAVAILABLE', upgradeAction: 'retry_project_identity_registration' };
  if (!payload || typeof payload !== 'object') {
    return { ...fallback, message: `HTTP ${status} project identity registration failed` };
  }
  const error = (payload as { error?: unknown }).error;
  if (!error || typeof error !== 'object') {
    return { ...fallback, message: `HTTP ${status} project identity registration failed` };
  }
  const typed = error as { code?: unknown; message?: unknown; upgrade_action?: unknown };
  return {
    code: typeof typed.code === 'string' ? typed.code : fallback.code,
    message: typeof typed.message === 'string' ? typed.message : `HTTP ${status} project identity registration failed`,
    upgradeAction: typeof typed.upgrade_action === 'string' ? typed.upgrade_action : fallback.upgradeAction,
  };
}
