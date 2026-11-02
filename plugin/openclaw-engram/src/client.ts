/**
 * EngramRestClient — typed REST client for the engram HTTP API.
 *
 * Read methods return null on error; memory writes return explicit per-input outcomes.
 * No method ever throws to its caller — engram failures must not block agent operation.
 */

import { randomUUID } from 'node:crypto';
import { z } from 'zod';
import { AvailabilityTracker } from './availability.js';
import type { PluginConfig } from './config.js';
import { resolveIdentity, validateProjectID, type ProjectIdentity } from './identity.js';

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
 guidance?: Observation[];
 always_inject?: Observation[];
 /** Section views, not additional memory pools to append to observations. */
 recent?: Observation[];
 relevant?: Observation[];
 strategy?: string;
 rule_router?: RuleRouterPayload;
}

export interface ContextSearchResponse {
 observations: Observation[];
 guidance?: Observation[];
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

export interface MemoryInput {
 title: string;
 content: string;
 type: string;
 project: string;
 scope?: 'project' | 'global' | 'agent';
 tags?: string[];
}

export interface MemoryAttribution {
 sourceAgent?: string;
 sessionId?: string;
}

const StoredMemorySchema = z.object({
 id: z.number().int().positive(),
 project: z.string(),
 content: z.string(),
});

export type StoredMemory = z.infer<typeof StoredMemorySchema>;
export interface MemoryWriteFailure {
 ok: false;
 outcome: 'not_sent' | 'rejected' | 'unknown';
 error: string;
}

export type MemoryWriteResult = { ok: true; memory: StoredMemory } | MemoryWriteFailure;

const MemoryTransitionActionSchema = z.enum(['suppress', 'unsuppress', 'archive']);
export type MemoryTransitionAction = z.infer<typeof MemoryTransitionActionSchema>;

const MemoryTransitionReceiptSchema = z.object({
 id: z.number().int().positive().max(Number.MAX_SAFE_INTEGER),
 action: MemoryTransitionActionSchema,
 status: z.enum(['active', 'flagged', 'archived']),
 version: z.number().int().positive().max(Number.MAX_SAFE_INTEGER),
 project: z.guid().refine(project => project !== '00000000-0000-0000-0000-000000000000' && project === project.toLowerCase()),
});

export type MemoryTransitionResult =
 | { ok: true; memory: z.infer<typeof MemoryTransitionReceiptSchema> }
 | MemoryWriteFailure;

export interface ProjectResolutionSuccess {
 ok: true;
 canonicalProject: string;
}


export interface ProjectResolutionFailure {
 ok: false;
 error: {
  code: string;
  message: string;
  upgradeAction: string;
  httpStatus: number;
 };
}

export type ProjectResolutionResult = ProjectResolutionSuccess | ProjectResolutionFailure;

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

export interface ProjectResolverClient {
 resolveWorkspaceProject(agentId: string, workspaceDir?: string): Promise<ProjectIdentity>;
}

export async function resolveProject(
 client: ProjectResolverClient,
 agentId: string,
 workspaceDir: string | undefined,
 configuredProject?: string,
): Promise<ProjectResolutionResult> {
 try {
  const project = configuredProject !== undefined
   ? validateProjectID(configuredProject)
   : (await client.resolveWorkspaceProject(agentId, workspaceDir)).projectId;
  return { ok: true, canonicalProject: project };
 } catch (error) {
  const message = error instanceof Error ? error.message : String(error);
  const code = /^PROJECT_[A-Z_]+$/.test(message) ? message : 'PROJECT_RESOLUTION_UNAVAILABLE';
  const supplied = error instanceof Error && 'guidance' in error && typeof error.guidance === 'string' && error.guidance.length <= 3072 ? error.guidance : '';
  const guidance = supplied || (code === 'PROJECT_MIGRATION_REQUIRED' || code === 'PROJECT_IDENTITY_CONFLICT' || code === 'PROJECT_CONTEXT_REQUIRED' || code === 'PROJECT_RESOLUTION_UNAVAILABLE'
   ? 'Use the host-reported installed Engram launcher: node <installed-plugin-path>/scripts/run-engram.js project resolve --root <actual-caller-root> --json --check (or the same installed native client). No bootstrap, marker/cache writes or migration. A valid historical id/name marker returns local migration_required evidence, match=legacy_marker, null canonical metadata and no HTTP/remote requirement; it is not a verified server binding. Next use existing authenticated GET /api/projects/resolve?legacy_id=<URI-encoded-exact-marker-id>, with the validated id encoded as query data, not executable text. The reader returns resolved canonical project_id or unresolved reference; unresolved is not a mapping. Preserve marker/cache/data. For migration_required/conflict obtain the owner-confirmed existing-project mapping and authorized conservation plan: admin GET /api/projects/consolidation/census?source_version=<exact-source>, then POST /api/projects/consolidation/preflight with structural evidence; backup, isolated rehearsal and explicit apply authority are separate. Do not init/reset/create a replacement UUID or change permissions. Project-free issues get/explicit-filter list can continue. Retry only after verified canonical identity. Guide: https://github.com/thebtf/engram/blob/main/docs/operating-engram.md#context-recovery'
   : 'Preserve the actual caller marker, common cache, v2 anchor and data. Inspect that caller root and .engram-project with bounded regular-file reads (4096 bytes maximum), and the Git common directory engram-project.json separately. Do not follow symlinks, borrow another repository identity or execute marker content. Invalid/unknown fields or error codes require exact owner inspection/mapping and authorized repair, not another failing check, deletion, init or replacement UUID. Keep the operation held; do not bypass permissions or treat failure as an empty successful result. Guide: https://github.com/thebtf/engram/blob/main/docs/operating-engram.md#context-recovery');
  return projectResolutionFailure(code, guidance, guidance, code === 'PROJECT_RESOLUTION_UNAVAILABLE' ? 503 : 400);
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

 async resolveWorkspaceProject(agentId: string, workspaceDir?: string): Promise<ProjectIdentity> {
  return resolveIdentity(agentId, workspaceDir, {
   env: { ...process.env, ENGRAM_URL: this.baseUrl, ENGRAM_TOKEN: this.token },
  });
 }

 /**
  * Fetch session context for injection (static session-level context).
  * POST /api/context/inject
  */
 async getContextInject(
  agentId: string,
  cwd: string | undefined,
  project: string,
  sessionId?: string,
 ): Promise<ContextInjectResponse | null> {
  return this.post<ContextInjectResponse>('/api/context/inject', {
   agent_id: agentId, ...(cwd ? { cwd } : {}), project,
   ...(sessionId ? { session_id: sessionId } : {}),
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
  /** Audit attribution only; search visibility remains project-scoped. */
  session_id?: string;
  /** Source identifier passed through to the server for analytics/routing. */
  source?: string;
  /** Search preset: decisions, changes, how_it_works. */
  preset?: string;
 }): Promise<ContextSearchResponse | null> {
  // Context search does vector query (embedding + pgvector) — needs more than default 5s.
  return this.post<ContextSearchResponse>('/api/context/search', body, 15_000);
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

 /** Each input produces one confirmed memory or an explicit failure; no retry or deduplication is implied. */
 async storeMemories(
  memories: MemoryInput[],
  attribution: MemoryAttribution = {},
 ): Promise<MemoryWriteResult[]> {
  const results: MemoryWriteResult[] = [];
  for (const memory of memories) {
   let project: string;
   try {
    project = validateProjectID(memory.project).toLowerCase();
   } catch (error) {
    results.push({ ok: false, outcome: 'not_sent', error: error instanceof Error ? error.message : String(error) });
    continue;
   }
   const scope = memory.scope ?? 'project';
   if (!['project', 'global', 'agent'].includes(scope)) {
    results.push({ ok: false, outcome: 'not_sent', error: 'Unsupported memory scope' });
    continue;
   }
   // The canonical handler has no observation title/type columns. Preserve
   // them as ordinary content and a tag, not as native classification fields.
   const body = {
    project,
    content: memory.title === memory.content ? memory.content : `${memory.title}\n\n${memory.content}`,
    tags: [...(memory.tags ?? []), `type:${memory.type}`],
    privacy_scope: scope === 'global' ? 'global' : 'project',
    ...(scope === 'agent' ? { agent_visibility: 'private' } : {}),
    ...(attribution.sourceAgent ? { source_agent: attribution.sourceAgent } : {}),
    ...(attribution.sessionId ? { session_id: attribution.sessionId } : {}),
   };
   let failure: MemoryWriteFailure = { ok: false, outcome: 'unknown', error: 'Memory write returned no confirmation; outcome is unknown, do not blindly retry' };
   const response = await this.request<unknown>('POST', '/api/memories', body, undefined, result => {
    failure = result.outcome === 'unknown' ? { ...result, error: `${result.error}; inspect the outcome before retrying` } : result;
   });
   const parsed = StoredMemorySchema.safeParse(response);
   if (!parsed.success || parsed.data.project !== project || parsed.data.content !== body.content) {
    results.push(response === null ? failure : { ok: false, outcome: 'unknown', error: 'Invalid memory write confirmation; outcome is unknown, do not blindly retry' });
   } else {
    results.push({ ok: true, memory: parsed.data });
   }
  }
  return results;
 }

 /** One retained-memory transition, with no workspace resolution or automatic retry. */
 async transitionMemory(id: number, action: MemoryTransitionAction, project?: string): Promise<MemoryTransitionResult> {
  if (!Number.isSafeInteger(id) || id <= 0 || !MemoryTransitionActionSchema.safeParse(action).success) {
   return { ok: false, outcome: 'not_sent', error: 'Invalid memory transition' };
  }
  let selector = '';
  if (project !== undefined) {
   try {
    project = validateProjectID(project).toLowerCase();
    selector = `?project=${encodeURIComponent(project)}`;
   } catch (error) {
    return { ok: false, outcome: 'not_sent', error: error instanceof Error ? error.message : String(error) };
   }
  }
  let failure: MemoryWriteFailure = { ok: false, outcome: 'unknown', error: 'Memory transition returned no confirmation; outcome is unknown, do not blindly retry' };
  const response = await this.request<unknown>('POST', `/api/memories/${id}/transition${selector}`, { action }, undefined, (result, status) => {
   // This endpoint guarantees HTTP 409 means rollback with no mutation.
   if (status === 409) {
    failure = { ...result, outcome: 'rejected' };
    return;
   }
   failure = result.outcome === 'unknown' ? { ...result, error: `${result.error}; outcome is unknown, inspect the memory before retrying` } : result;
  }, 409);
  const parsed = MemoryTransitionReceiptSchema.safeParse(response);
  const expectedStatus = action === 'suppress' ? 'flagged' : action === 'unsuppress' ? 'active' : 'archived';
  if (!parsed.success || parsed.data.id !== id || parsed.data.action !== action || parsed.data.status !== expectedStatus || (project !== undefined && parsed.data.project !== project)) {
   return response === null ? failure : { ok: false, outcome: 'unknown', error: 'Invalid memory transition confirmation; outcome is unknown, do not blindly retry' };
  }
  return { ok: true, memory: parsed.data };
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
  const resp = await this.post<{ observations: Observation[] }>('/api/context/search', body, 15_000);
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
 async getCredential(
  name: string,
  namespace: { scope: 'project'; project: string } | { scope: 'global' },
 ): Promise<{ name: string; value: string; scope: string; project_id: string | null; id: number } | null> {
  const query = new URLSearchParams({ scope: namespace.scope });
  if (namespace.scope === 'project') query.set('project', namespace.project);
  return this.get(`/api/vault/credentials/${encodeURIComponent(name)}?${query}`);
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
  onFailure?: (failure: MemoryWriteFailure, status?: number) => void,
  reachableRejectionStatus?: number,
 ): Promise<T | null> {
  if (!this.availability.isAvailable()) {
   onFailure?.({ ok: false, outcome: 'not_sent', error: 'engram is currently unreachable — memory was not sent' });
   return null;
  }

  const url = this.baseUrl + path;
  const timeout = timeoutMs ?? this.defaultTimeoutMs;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeout);
  const startMs = Date.now();
  let outcome: MemoryWriteFailure['outcome'] = 'unknown';
  let responseStatus: number | undefined;
  let availabilityFailure = true;

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
   responseStatus = response.status;
   if (!response.ok && [400, 401, 403, 404, 405, 413, 415, 422].includes(response.status)) outcome = 'rejected';

   const text = await response.text();
   const elapsedMs = Date.now() - startMs;

   if (!response.ok) {
    // Reachability success is distinct from the rejected application outcome.
    availabilityFailure = response.status !== reachableRejectionStatus;
    if (!availabilityFailure) this.availability.recordSuccess();
    throw new Error(`HTTP ${response.status} ${response.statusText} (${elapsedMs}ms): ${text.slice(0, 200)}`);
   }

   this.availability.recordSuccess();

   if (!text) return null;
   return JSON.parse(text) as T;
  } catch (err: unknown) {
   const elapsedMs = Date.now() - startMs;
   if (availabilityFailure) this.availability.recordFailure();
   const msg = err instanceof Error ? err.message : String(err);
   onFailure?.({ ok: false, outcome, error: msg }, responseStatus);
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

function projectResolutionFailure(
 code: string,
 message: string,
 upgradeAction: string,
 httpStatus: number,
): ProjectResolutionFailure {
 return { ok: false, error: { code, message, upgradeAction, httpStatus } };
}

