/**
 * session_end hook — final backfill and session statistics.
 *
 * Submits any remaining conversation content to engram for extraction.
 * This is the last chance to persist observations from the session.
 */

import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { SessionEndEvent, ConversationMessage, PluginLogger } from '../types/openclaw.js';

const MAX_MESSAGES = 20;
const CONTENT_MAX_CHARS = 6000;

/**
 * Handle the session_end hook.
 *
 * @param event  - The session_end event from OpenClaw.
 * @param client - Shared engram REST client.
 * @param config - Resolved plugin config.
 */
export function handleSessionEnd(
  event: SessionEndEvent,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): void {
  if (!client.isAvailable()) return;
  if (!config.autoExtract) return;

  const agentId = event.agentId ?? '';
  const identity = resolveIdentity(agentId, event.workspaceDir);
  const project = config.project ?? identity.projectId;

  const messages: ConversationMessage[] = Array.isArray(event.messages) ? event.messages : [];
  if (messages.length === 0) return;

  const recent = messages.slice(-MAX_MESSAGES);
  const content = recent
    .map((m) => `[${m.role}]: ${m.content}`)
    .join('\n\n');

  if (!content) return;

  const truncated = content.length > CONTENT_MAX_CHARS
    ? content.slice(0, CONTENT_MAX_CHARS)
    : content;

  const sessionId = event.sessionId ?? '';
  if (!sessionId) return;

  // Fire-and-forget — do not await
  void client.backfillSession({
    session_id: sessionId,
    project,
    content: truncated,
  });

  (logger ?? console).warn(
    `[engram] session-end: submitted ${recent.length} messages for backfill` +
      ` (project ${project}, reason: ${event.reason ?? 'unknown'})`,
  );
}
