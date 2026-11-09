/**
 * EngramRestClient — typed REST client for the engram HTTP API.
 *
 * General reads retain null-on-error behavior; memory writes return explicit outcomes.
 * Issue endpoints preserve typed safe failures without logging private bodies or retrying mutations.
 */

import { randomUUID } from 'node:crypto';
import { z } from 'zod';
import { IntegerSchema, StringArraySchema } from './validation.js';
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

export interface IssueErrorResponse {
 error: { code: string; message: string; current_revision?: string };
}
export type IssueResult<T> = T | IssueErrorResponse;

export const IssueRevisionSchema = z.string().regex(/^[1-9][0-9]*$/).refine(
 value => value.length < 19 || (value.length === 19 && value <= '9223372036854775807'),
 { error: 'Revision must be a canonical positive int64 decimal string' },
);
export const IssuePrioritySchema = z.enum(['critical', 'high', 'medium', 'low']);
export const IssueTypeSchema = z.enum(['bug', 'feature', 'improvement', 'task']);
export const IssueStatusSchema = z.enum(['open', 'acknowledged', 'resolved', 'reopened', 'closed', 'rejected']);
const IssueActorSchema = z.object({ kind: z.enum(['client', 'operator', 'system', 'unknown']), reference: z.string().nullable() });
const IssueExpectedActorSchema = z.object({ kind: z.enum(['client', 'operator', 'unknown']), reference: z.string().nullable() });
const IssueReadMetadataSchema = z.record(z.string(), z.json());
export type IssueMetadata = z.infer<typeof IssueReadMetadataSchema>;

function issueMetadataProblem(value: unknown): 'INVALID_ARGUMENT' | 'PAYLOAD_TOO_LARGE' | null {
 if (value === null || typeof value !== 'object' || Array.isArray(value)) return 'INVALID_ARGUMENT';
 const ancestors = new Set<object>();
 function visit(node: unknown, depth: number): 'INVALID_ARGUMENT' | 'PAYLOAD_TOO_LARGE' | null {
  if (node === null || typeof node === 'string' || typeof node === 'boolean') return null;
  if (typeof node === 'number') return Number.isFinite(node) && (!Number.isInteger(node) || Number.isSafeInteger(node)) ? null : 'INVALID_ARGUMENT';
  if (typeof node !== 'object' || ancestors.has(node)) return 'INVALID_ARGUMENT';
  if (depth > 256) return 'PAYLOAD_TOO_LARGE';
  const prototype = Object.getPrototypeOf(node);
  if (!Array.isArray(node) && prototype !== Object.prototype && prototype !== null) return 'INVALID_ARGUMENT';
  if (Object.getOwnPropertySymbols(node).length !== 0) return 'INVALID_ARGUMENT';
  const serializer = Object.getOwnPropertyDescriptor(node, 'toJSON');
  if (serializer && (!('value' in serializer) || typeof serializer.value === 'function')) return 'INVALID_ARGUMENT';
  if (Array.isArray(node) && Object.keys(node).length !== node.length) return 'INVALID_ARGUMENT';
  ancestors.add(node);
  if (Array.isArray(node)) {
   for (let index = 0; index < node.length; index++) {
    const descriptor = Object.getOwnPropertyDescriptor(node, index);
    if (!descriptor || !('value' in descriptor)) return 'INVALID_ARGUMENT';
    const problem = visit(descriptor.value, depth + 1);
    if (problem) return problem;
   }
  } else {
   for (const key of Object.keys(node)) {
    const descriptor = Object.getOwnPropertyDescriptor(node, key);
    if (!descriptor || !('value' in descriptor)) return 'INVALID_ARGUMENT';
    const problem = visit(descriptor.value, depth + 1);
    if (problem) return problem;
   }
  }
  ancestors.delete(node);
  return null;
 }
 try {
  const problem = visit(value, 1);
  if (problem) return problem;
  return Buffer.byteLength(JSON.stringify(value), 'utf8') > 1024 * 1024 ? 'PAYLOAD_TOO_LARGE' : null;
 } catch {
  return 'INVALID_ARGUMENT';
 }
}

const IssueWriteMetadataSchema = z.unknown().transform((value, ctx): IssueMetadata => {
 const problem = issueMetadataProblem(value);
 if (problem) {
  ctx.addIssue({ code: 'custom', message: 'Metadata must be an admitted JSON object within 1 MiB and 256 nested containers', params: { issueCode: problem } });
  return z.NEVER;
 }
 // Validate the actual JS value; earlier duplicate members and numeric spellings cannot be recovered.
 return value as IssueMetadata;
});

function hasLoneSurrogate(value: string): boolean {
 for (let index = 0; index < value.length; index++) {
  const code = value.charCodeAt(index);
  if (code >= 0xd800 && code <= 0xdbff) {
   const next = value.charCodeAt(++index);
   if (!(next >= 0xdc00 && next <= 0xdfff)) return true;
  } else if (code >= 0xdc00 && code <= 0xdfff) return true;
 }
 return false;
}
const IssueRequestKeySchema = z.string().superRefine((value, ctx) => {
 if (value.length === 0 || value.includes('\0') || hasLoneSurrogate(value)) {
  ctx.addIssue({ code: 'custom', message: 'request_key must be nonempty valid UTF-8 without NUL' });
 } else if (Buffer.byteLength(value, 'utf8') > 512) {
  ctx.addIssue({ code: 'custom', message: 'request_key exceeds 512 UTF-8 bytes', params: { issueCode: 'PAYLOAD_TOO_LARGE' } });
 }
});
function resolvedSinceMillis(value: string): string | undefined {
 if (/^[+-]?[0-9]+$/.test(value)) {
  const negative = value.startsWith('-');
  const digits = value.replace(/^[+-]?0*/, '') || '0';
  const limit = negative ? '9223372036854775808' : '9223372036854775807';
  return digits.length < limit.length || (digits.length === limit.length && digits <= limit) ? value : undefined;
 }
 const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,3})?(Z|[+-]\d{2}:\d{2})$/.exec(value);
 if (!match) return undefined;
 const [, year, month, day, hour, minute, second, zone] = match;
 const y = Number(year), m = Number(month), d = Number(day);
 const leap = y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0);
 const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
 if (m < 1 || m > 12 || d < 1 || d > days[m - 1] || Number(hour) > 23 || Number(minute) > 59 || Number(second) > 59 ||
  zone === '-00:00' || (zone !== 'Z' && (Number(zone.slice(1, 3)) > 23 || Number(zone.slice(4)) > 59))) return undefined;
 const millis = Date.parse(value);
 return Number.isFinite(millis) ? String(millis) : undefined;
}
const IssueResolvedSinceSchema = z.string().transform((value, ctx) => {
 const millis = resolvedSinceMillis(value);
 if (millis === undefined) {
  ctx.addIssue({ code: 'custom', message: 'resolved_since must be a signed int64 epoch-millisecond string or a valid ISO timestamp with explicit offset and at most millisecond precision' });
  return z.NEVER;
 }
 return millis;
});

export const IssueCreateRequestSchema = z.object({
 title: z.string().min(1), body: z.string().optional(), priority: IssuePrioritySchema.optional(), type: IssueTypeSchema.optional(),
 labels: StringArraySchema.optional(), metadata: IssueWriteMetadataSchema.optional(),
 owner_label: z.string().min(1).nullable().optional(), recipient_label: z.string().min(1).nullable().optional(),
 target_session: z.string().min(1).optional(),
 request_key: IssueRequestKeySchema.optional(), expected_actor: IssueExpectedActorSchema.optional(), target_project: z.string(),
 source_project: z.string().optional(), source_agent: z.string().optional(), created_by_session: z.string().optional(),
 session_id: z.string().min(1).optional(), client: z.string().optional(),
});
export type CreateIssueRequest = z.infer<typeof IssueCreateRequestSchema>;

export const IssueUpdateRequestSchema = z.object({
 expected_revision: IssueRevisionSchema.optional(), title: z.string().min(1).optional(), body: z.string().optional(),
 priority: IssuePrioritySchema.optional(), type: IssueTypeSchema.optional(), labels: StringArraySchema.optional(),
 metadata: IssueWriteMetadataSchema.optional(), owner_label: z.string().min(1).nullable().optional(), recipient_label: z.string().min(1).nullable().optional(),
 target_session: z.string().min(1).nullable().optional(),
 status: IssueStatusSchema.optional(), comment: z.string().min(1).optional(), comment_metadata: IssueWriteMetadataSchema.optional(),
 source_project: z.string().optional(), source_agent: z.string().optional(), session_id: z.string().min(1).optional(), client: z.string().optional(),
}).superRefine((value, ctx) => {
 const fields = value.title !== undefined || value.body !== undefined || value.priority !== undefined || value.type !== undefined ||
  value.labels !== undefined || value.metadata !== undefined || value.owner_label !== undefined || value.recipient_label !== undefined || value.target_session !== undefined;
 const lifecycle = value.status === 'resolved' || value.status === 'reopened' || value.status === 'closed';
 if ((fields || (value.status !== undefined && !lifecycle)) && value.expected_revision === undefined) ctx.addIssue({ code: 'custom', path: ['expected_revision'], message: 'Material mutation requires expected_revision', params: { issueCode: 'REVISION_REQUIRED' } });
 if (!fields && value.status === undefined && value.comment === undefined) ctx.addIssue({ code: 'custom', message: 'At least one edit, state change or comment is required' });
 if (value.comment_metadata !== undefined && value.comment === undefined) ctx.addIssue({ code: 'custom', path: ['comment_metadata'], message: 'comment_metadata requires a comment' });
});
export type UpdateIssueRequest = z.infer<typeof IssueUpdateRequestSchema>;

export const IssueOffsetSchema = z.number().int().nonnegative();
export const IssuePageLimitSchema = IntegerSchema.positive().max(200);
export const IssueListRequestSchema = z.object({
 project: z.string().optional(), source_project: z.string().optional(), source_agent: z.string().optional(), status: z.string().optional(),
 source_session: z.string().optional(), target_session: z.string().min(1).optional(), participant_session: z.string().optional(),
 type: IssueTypeSchema.optional(), q: z.string().optional(), owner_label: z.string().optional(), recipient_label: z.string().optional(),
 resolved_since: IssueResolvedSinceSchema.optional(), limit: IssuePageLimitSchema.default(20), offset: IssueOffsetSchema.default(0),
});
export type IssueListRequest = z.input<typeof IssueListRequestSchema>;
export const IssueDetailRequestSchema = z.object({
 detail_mode: z.literal('paged').default('paged'), comments_offset: IssueOffsetSchema.default(0), comments_limit: IssuePageLimitSchema.default(50),
 history_offset: IssueOffsetSchema.default(0), history_limit: IssuePageLimitSchema.default(50),
});
export type IssueDetailRequest = z.input<typeof IssueDetailRequestSchema>;

const IssueSummarySchema = z.object({
 id: IntegerSchema.positive(), revision: IssueRevisionSchema, title: z.string(), status: z.string(), priority: z.string(), type: z.string(),
 source_project: z.string(), target_project: z.string(), source_agent: z.string(), labels: z.array(z.string()).nullable(),
 owner_label: z.string().nullable(), recipient_label: z.string().nullable(), comment_count: IssueOffsetSchema,
 created_at: z.string(), updated_at: z.string(),
}).passthrough();
const IssueSchema = IssueSummarySchema.extend({ body: z.string(), metadata: IssueReadMetadataSchema, actor: IssueActorSchema.optional(), comment_count: IssueOffsetSchema.optional() });
export type Issue = z.infer<typeof IssueSchema>;
const IssueCommentSchema = z.object({
 id: IntegerSchema.positive(), issue_id: IntegerSchema.positive(), author_project: z.string(), author_agent: z.string(), body: z.string(),
 metadata: IssueReadMetadataSchema, actor: IssueActorSchema.optional(), created_at: z.string(),
}).passthrough();
export type IssueComment = z.infer<typeof IssueCommentSchema>;
const IssueHistorySchema = z.object({
 revision: IssueRevisionSchema, previous_revision: IssueRevisionSchema.nullable(), action: z.string(), changed_fields: z.array(z.string()), actor: IssueActorSchema,
 attribution: z.object({ source_project: z.string().nullable(), source_agent: z.string().nullable(), session: z.string().nullable() }).passthrough(),
 created_at: z.string(), changes: IssueReadMetadataSchema,
}).passthrough();
const IssueListResponseSchema = z.object({
 issues: z.array(IssueSummarySchema), total: IssueOffsetSchema, limit: IssuePageLimitSchema, offset: IssueOffsetSchema,
 next_offset: IssueOffsetSchema.nullable(), project_names: z.record(z.string(), z.string()),
}).passthrough();
export type IssueListResponse = z.infer<typeof IssueListResponseSchema>;
const IssueDetailResponseSchema = z.object({
 issue: IssueSchema, comments: z.array(IssueCommentSchema), history: z.array(IssueHistorySchema), comment_count: IssueOffsetSchema, history_count: IssueOffsetSchema,
 comments_offset: IssueOffsetSchema, comments_limit: IssuePageLimitSchema, history_offset: IssueOffsetSchema, history_limit: IssuePageLimitSchema,
 comments_next_offset: IssueOffsetSchema.nullable(), history_next_offset: IssueOffsetSchema.nullable(),
 comments_complete: z.boolean(), history_complete: z.boolean(), comments_oversized: z.boolean(), history_oversized: z.boolean(),
}).passthrough().refine(value =>
 value.comments_complete === (value.comments_next_offset === null) && value.history_complete === (value.history_next_offset === null) &&
 (value.comments_next_offset === null || (value.comments.length > 0 && value.comments_next_offset === value.comments_offset + value.comments.length)) &&
 (value.history_next_offset === null || (value.history.length > 0 && value.history_next_offset === value.history_offset + value.history.length)),
 { error: 'Inconsistent issue detail continuation' },
);
export type IssueDetailResponse = z.infer<typeof IssueDetailResponseSchema>;
const CreateIssueResponseSchema = z.object({ id: IntegerSchema.positive(), revision: IssueRevisionSchema, replayed: z.boolean(), message: z.string() }).passthrough();
export type CreateIssueResponse = z.infer<typeof CreateIssueResponseSchema>;
const UpdateIssueResponseSchema = z.object({ id: IntegerSchema.positive(), revision: IssueRevisionSchema, changed: z.boolean(), comment_id: IntegerSchema.positive().nullable(), message: z.string() }).passthrough();
export type UpdateIssueResponse = z.infer<typeof UpdateIssueResponseSchema>;
const IssueErrorResponseSchema = z.object({ error: z.object({ code: z.string().regex(/^[A-Z][A-Z0-9_]*$/), message: z.string(), current_revision: IssueRevisionSchema.optional() }).passthrough() }).passthrough();

export function issueValidationFailure(error: z.ZodError): IssueErrorResponse {
 const classified = error.issues.find(issue => issue.code === 'custom' && typeof issue.params?.issueCode === 'string');
 const code = classified?.code === 'custom' ? String(classified.params?.issueCode) : 'INVALID_ARGUMENT';
 const timestamp = error.issues.some(issue => issue.path[0] === 'resolved_since');
 return { error: { code, message: code === 'REVISION_REQUIRED' ? 'Material mutation requires expected_revision' : code === 'PAYLOAD_TOO_LARGE' ? 'Issue input exceeds an admitted bound' : timestamp ? 'resolved_since must be a signed int64 epoch-millisecond string or a valid ISO timestamp with explicit offset and at most millisecond precision' : 'Invalid issue parameters' } };
}

export function isIssueError(value: unknown): value is IssueErrorResponse {
 return IssueErrorResponseSchema.safeParse(value).success;
}

const IssueHTTPErrorCodes: Readonly<Record<number, string>> = {
 400: 'INVALID_ARGUMENT', 401: 'UNAUTHENTICATED', 403: 'FORBIDDEN', 404: 'ISSUE_NOT_FOUND',
 410: 'ISSUE_GONE', 413: 'PAYLOAD_TOO_LARGE', 428: 'REVISION_REQUIRED',
};

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

 async listIssues(params: IssueListRequest): Promise<IssueResult<IssueListResponse>> {
  const parsed = IssueListRequestSchema.safeParse(params);
  if (!parsed.success) return issueValidationFailure(parsed.error);
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(parsed.data)) {
   if (value !== undefined) query.set(key, String(value));
  }
  return this.issueRequest('GET', `/api/issues?${query}`, undefined, IssueListResponseSchema);
 }

 async getIssue(id: number, params: IssueDetailRequest = {}): Promise<IssueResult<IssueDetailResponse>> {
  const identifier = IntegerSchema.positive().safeParse(id);
  if (!identifier.success) return issueValidationFailure(identifier.error);
  const parsed = IssueDetailRequestSchema.safeParse(params);
  if (!parsed.success) return issueValidationFailure(parsed.error);
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(parsed.data)) query.set(key, String(value));
  return this.issueRequest('GET', `/api/issues/${id}?${query}`, undefined, IssueDetailResponseSchema);
 }

 async createIssue(body: CreateIssueRequest): Promise<IssueResult<CreateIssueResponse>> {
  const parsed = IssueCreateRequestSchema.safeParse(body);
  if (!parsed.success) return issueValidationFailure(parsed.error);
  return this.issueRequest('POST', '/api/issues', parsed.data, CreateIssueResponseSchema);
 }

 async updateIssue(id: number, body: UpdateIssueRequest): Promise<IssueResult<UpdateIssueResponse>> {
  const identifier = IntegerSchema.positive().safeParse(id);
  if (!identifier.success) return issueValidationFailure(identifier.error);
  const parsed = IssueUpdateRequestSchema.safeParse(body);
  if (!parsed.success) return issueValidationFailure(parsed.error);
  return this.issueRequest('PATCH', `/api/issues/${id}`, parsed.data, UpdateIssueResponseSchema);
 }

 private async issueRequest<T>(method: string, path: string, body: unknown, schema: z.ZodType<T>): Promise<IssueResult<T>> {
  const unavailable: IssueErrorResponse = { error: { code: 'UNAVAILABLE', message: 'Issue service unavailable; reconcile the original intent before retrying' } };
  if (!this.availability.isAvailable()) return unavailable;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), this.defaultTimeoutMs);
  try {
   const headers: Record<string, string> = {
    Authorization: `Bearer ${this.token}`,
    'X-Engram-Project-Identity-Adapter': 'openclaw',
    'X-Request-ID': randomUUID(),
   };
   if (body !== undefined) headers['Content-Type'] = 'application/json';
   const response = await fetch(this.baseUrl + path, {
    method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal,
   });
   let payload: unknown;
   try {
    payload = JSON.parse(await response.text());
   } catch {
    if (response.ok) {
     this.availability.recordFailure();
     return unavailable;
    }
   }
   const failure = IssueErrorResponseSchema.safeParse(payload);
   if (!response.ok || failure.success) {
    if (response.status >= 500) this.availability.recordFailure();
    else this.availability.recordSuccess();
    if (failure.success) return payload as IssueErrorResponse;
    if (response.status === 409) {
     return { error: { code: 'UNAVAILABLE', message: 'HTTP 409 conflict has no typed reason; the outcome is unconfirmed. Preserve the original intent and reconcile explicitly before any deliberate retry.' } };
    }
    return { error: { code: IssueHTTPErrorCodes[response.status] ?? 'UNAVAILABLE', message: 'Issue request refused; no successful result was received' } };
   }
   const parsed = schema.safeParse(payload);
   if (!parsed.success) {
    this.availability.recordFailure();
    return unavailable;
   }
   this.availability.recordSuccess();
   // Schema admission does not authorize rewriting historical bytes or dropping unknown response keys.
   return payload as T;
  } catch {
   this.availability.recordFailure();
   // Issue URLs, response bodies and exception messages can contain private work-item data.
   return unavailable;
  } finally {
   clearTimeout(timer);
  }
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

