/**
 * before_compaction hook — transcript backfill before context window compaction.
 *
 * Serializes the recent conversation messages and submits them to engram's
 * /api/backfill/session endpoint for server-side observation extraction.
 * This is fire-and-forget: the hook must never block compaction.
 */

import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { BeforeCompactionEvent, ConversationMessage, PluginLogger } from '../types/openclaw.js';

/** Maximum recent messages to include in the backfill payload. */
const MAX_MESSAGES = 20;
/** Soft character limit for the content field (server hard limit: 10,000). */
const CONTENT_MAX_CHARS = 6000;

/**
 * Handle the before_compaction hook.
 *
 * @param event  - The before_compaction event from OpenClaw.
 * @param client - Shared engram REST client.
 * @param config - Resolved plugin config.
 */
export function handleBeforeCompaction(
  event: BeforeCompactionEvent,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): void {
  if (!client.isAvailable()) return;
  if (!config.autoExtract) return;

  const agentId = event.agentId ?? '';
  const identity = resolveIdentity(agentId, event.workspaceDir);
  const project = config.project ?? identity.projectId;

  const messages = Array.isArray(event.messages) ? event.messages : [];
  const recent = messages.slice(-MAX_MESSAGES);
  const content = serializeMessages(recent);
  if (!content) return;

  const truncated = content.length > CONTENT_MAX_CHARS
    ? content.slice(0, CONTENT_MAX_CHARS)
    : content;

  // Fire-and-forget — do not await
  void client.backfillSession({
    session_id: agentId,
    project,
    content: truncated,
  });

  (logger ?? console).warn(
    `[engram] before-compaction: submitting ${recent.length} messages for backfill (project ${project})`,
  );
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function serializeMessages(messages: ConversationMessage[]): string {
  return messages
    .map((m) => `[${m.role}]: ${m.content}`)
    .join('\n\n');
}
