/**
 * after_tool_call hook — self-learning via tool event ingestion.
 *
 * Every tool call is sent to engram's /api/events/ingest endpoint so that
 * the server can extract patterns and update its observation store over time.
 * This is fire-and-forget: the hook never blocks on a response.
 */

import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import { classifyMessage } from './message-classifier.js';
import type { AfterToolCallEvent, PluginHookContext, PluginLogger } from '../types/openclaw.js';

const TOOL_INPUT_MAX_CHARS = 500;
const TOOL_RESULT_MAX_CHARS = 500;

/**
 * Tool names whose name alone identifies them as heartbeat / keep-alive events
 * regardless of input content. These supplement the content-based classifier.
 */
const HEARTBEAT_TOOL_NAMES = new Set([
  'heartbeat',
  'keepalive',
  'keep_alive',
  'keep-alive',
  'ping',
  'health_check',
  'health-check',
  'status',
  'noop',
  'no-op',
]);

/**
 * Handle the after_tool_call hook.
 *
 * @param event  - The after_tool_call event from OpenClaw.
 * @param ctx    - The hook context containing agent identity fields.
 * @param client - Shared engram REST client.
 * @param config - Resolved plugin config.
 */
export function handleAfterToolCall(
  event: AfterToolCallEvent,
  ctx: PluginHookContext,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): void {
  if (!client.isAvailable()) return;
  if (!config.autoExtract) return;

  // Skip heartbeat / keep-alive tool events unless explicitly opted in.
  // Two-layer check:
  //   1. Tool name allowlist — fast path for well-known heartbeat tool names.
  //   2. Content-based classifier — catches heartbeat file reads/writes by input content.
  const toolNameLower = (event.toolName ?? '').toLowerCase();
  if (!config.heartbeat?.ingest) {
    if (HEARTBEAT_TOOL_NAMES.has(toolNameLower)) return;
    // Build a probe string from tool name + serialized input for content classification
    let inputProbe: string;
    try {
      inputProbe = JSON.stringify(event.toolInput ?? '');
    } catch {
      inputProbe = '';
    }
    const category = classifyMessage(`${event.toolName ?? ''} ${inputProbe}`, event.toolName);
    if (category === 'heartbeat' || category === 'system') return;
  }

  const agentId = ctx.agentId ?? '';
  const sessionId = ctx.sessionId ?? ctx.sessionKey ?? agentId;
  if (!sessionId?.trim()) return; // no session identity available — skip
  const identity = resolveIdentity(agentId, ctx.workspaceDir);
  const project = config.project ?? identity.projectId;

  let toolInput: string;
  let toolResult: string;
  try {
    toolInput = truncate(JSON.stringify(event.toolInput ?? ''), TOOL_INPUT_MAX_CHARS);
    toolResult = truncate(JSON.stringify(event.toolResult ?? ''), TOOL_RESULT_MAX_CHARS);
  } catch {
    toolInput = '[unserializable]';
    toolResult = '[unserializable]';
  }

  // Fire-and-forget — do not await
  void client.ingestEvent({
    session_id: sessionId,
    project,
    tool_name: event.toolName ?? 'unknown',
    tool_input: toolInput,
    tool_result: toolResult,
    source: 'openclaw',
  }).catch(() => { /* swallow — fire-and-forget */ });

  // Contradiction detection: check Write/Edit results against stored decisions
  if (isWriteOrEdit(event.toolName)) {
    void checkContradictions(client, project, event.toolResult, logger).catch(() => {});
  }
}

function truncate(value: string, maxChars: number): string {
  if (value.length <= maxChars) return value;
  return value.slice(0, maxChars);
}

// ---------------------------------------------------------------------------
// Contradiction detection helpers
// ---------------------------------------------------------------------------

const WRITE_TOOLS = new Set(['write', 'edit']);

function isWriteOrEdit(toolName?: string): boolean {
  return typeof toolName === 'string' && WRITE_TOOLS.has(toolName.toLowerCase());
}

async function checkContradictions(
  client: EngramRestClient,
  project: string,
  toolResult: unknown,
  logger: PluginLogger | undefined,
): Promise<void> {
  // Extract text content from tool result
  const content = extractResultText(toolResult);
  if (!content || content.length < 20) return;

  // Take a snippet of the written content as the search query
  const snippet = content.slice(0, 200);

  // Timeout: 3s max for contradiction check (fire-and-forget, must not stall)
  const response = await Promise.race([
    client.searchDecisions({ query: snippet, project, limit: 5 }),
    new Promise<null>((resolve) => setTimeout(() => resolve(null), 3000)),
  ]);

  if (!response?.observations?.length) return;

  const contentLower = content.toLowerCase();

  // Stop-words that are common noise tokens — skip them even if they appear
  // in rejected[] or are extracted from narrative patterns.
  const stopWords = new Set(['not', 'instead', 'rather', 'avoid', 'use', 'the', 'and', 'for', 'with', 'but']);

  /** Returns true if `term` appears as a whole word in `text`. */
  function containsWholeWord(text: string, term: string): boolean {
    const escaped = term.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    return new RegExp(`\\b${escaped}\\b`).test(text);
  }

  for (const obs of response.observations) {
    // Prefer structured rejected[] field for reliable contradiction detection
    const rejectedItems = obs.rejected ?? [];
    for (const rejectedTerm of rejectedItems) {
      const term = rejectedTerm.toLowerCase().trim();
      if (term.length < 3) continue;
      if (stopWords.has(term)) continue;
      if (containsWholeWord(contentLower, term)) {
        (logger ?? console).warn(
          `[engram] CONTRADICTION: written code contains "${rejectedTerm}" which was rejected in decision: "${obs.title ?? ''}"`,
        );
      }
    }

    // Fallback: parse rejection from narrative text when rejected[] is empty
    if (rejectedItems.length === 0) {
      const narrative = obs.narrative ?? '';
      const lowerNarrative = narrative.toLowerCase();
      const rejectionPatterns = [
        'instead of',
        'not ',
        'rather than',
        'rejected',
        'avoid ',
        "don't use",
        'do not use',
      ];

      for (const pattern of rejectionPatterns) {
        const idx = lowerNarrative.indexOf(pattern);
        if (idx === -1) continue;

        const afterPattern = narrative.slice(idx + pattern.length, idx + pattern.length + 50).trim();
        const rejectedWord = afterPattern.split(/[\s,.;:!?]+/)[0]?.toLowerCase();
        if (!rejectedWord || rejectedWord.length < 3) continue;
        if (stopWords.has(rejectedWord)) continue;

        if (containsWholeWord(contentLower, rejectedWord)) {
          (logger ?? console).warn(
            `[engram] CONTRADICTION: written code contains "${rejectedWord}" which was rejected in decision: "${obs.title ?? narrative.slice(0, 80)}"`,
          );
        }
      }
    }
  }
}

function extractResultText(result: unknown): string {
  if (typeof result === 'string') return result;
  if (result && typeof result === 'object') {
    const obj = result as Record<string, unknown>;
    if (typeof obj.content === 'string') return obj.content;
    if (typeof obj.output === 'string') return obj.output;
    if (typeof obj.new_string === 'string') return obj.new_string;
  }
  return '';
}
