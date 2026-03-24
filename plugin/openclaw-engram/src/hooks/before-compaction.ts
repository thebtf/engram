/**
 * before_compaction hook — transcript backfill before context window compaction.
 *
 * Serializes the recent conversation messages and submits them to engram's
 * /api/backfill/session endpoint for server-side observation extraction.
 * This is fire-and-forget: the hook must never block compaction.
 */

import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { normalizeEngramContent } from './content.js';
import { resolveIdentity } from '../identity.js';
import type { BeforeCompactionEvent, ConversationMessage, PluginHookContext, PluginLogger } from '../types/openclaw.js';

/** Maximum recent messages to include in the backfill payload. */
const MAX_MESSAGES = 20;

/**
 * Handle the before_compaction hook.
 *
 * @param event  - The before_compaction event from OpenClaw.
 * @param ctx    - The hook context containing agent identity fields.
 * @param client - Shared engram REST client.
 * @param config - Resolved plugin config.
 */
export function handleBeforeCompaction(
  event: BeforeCompactionEvent,
  ctx: PluginHookContext,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): void {
  try {
    if (!client.isAvailable()) return;
    if (!config.autoExtract) return;

    const agentId = ctx.agentId ?? '';
    const sessionId = ctx.sessionId ?? ctx.sessionKey ?? agentId;
    if (!sessionId?.trim()) return; // no session identity available — skip
    const identity = resolveIdentity(agentId, ctx.workspaceDir);
    const project = config.project ?? identity.projectId;

    const messages = Array.isArray(event.messages) ? event.messages : [];
    const recent = messages.slice(-MAX_MESSAGES);
    const content = serializeMessages(recent);
    if (!content) return;

    const truncated = normalizeEngramContent(content);

    // Fire-and-forget — do not await
    void client.backfillSession({
      session_id: sessionId,
      project,
      content: truncated,
    });

    (logger ?? console).warn(
      `[engram] before-compaction: submitting ${recent.length} messages for backfill (project ${project})`,
    );
  } catch (err) {
    (logger ?? console).error('[engram] hook error:', err);
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function serializeMessages(messages: ConversationMessage[]): string {
  return messages
    .map((m) => `[${m.role}]: ${m.content}`)
    .join('\n\n');
}

