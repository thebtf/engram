/**
 * session_end hook — final backfill, outcome recording, and utility tracking.
 *
 * Submits remaining conversation content to engram for extraction,
 * records session outcome (success/partial/abandoned) for closed-loop learning,
 * and tracks utility signals for injected observations.
 */

import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { normalizeEngramContent } from './content.js';
import { resolveIdentity } from '../identity.js';
import { classifyMessage } from './message-classifier.js';
import type { SessionEndEvent, ConversationMessage, PluginHookContext, PluginLogger } from '../types/openclaw.js';

const MAX_MESSAGES = 20;

/** Detect session outcome from conversation signals. */
function detectOutcome(messages: ConversationMessage[]): { outcome: string; reason: string } {
  if (messages.length === 0) {
    return { outcome: 'abandoned', reason: 'no messages in session' };
  }

  const textContent = messages
    .map((m) => (typeof m.content === 'string' ? m.content : '').toLowerCase())
    .join('\n');

  // Success signals: multi-word phrases to avoid false positives from common words
  const successPatterns = [
    'task complete', 'successfully implemented', 'pr merged',
    'deployed to', 'issue resolved', 'committed and pushed',
  ];
  const hasSuccess = successPatterns.some((p) => textContent.includes(p));

  // Failure signals: also multi-word to reduce false positives
  const failurePatterns = [
    'build failed', 'test failed', 'unable to fix', 'regression found',
  ];
  const hasFailure = failurePatterns.some((p) => textContent.includes(p));

  if (hasSuccess && !hasFailure) {
    return { outcome: 'success', reason: 'completion signals detected' };
  }
  if (hasSuccess && hasFailure) {
    return { outcome: 'partial', reason: 'mixed success and failure signals' };
  }
  if (hasFailure) {
    return { outcome: 'partial', reason: 'failure signals detected but session continued' };
  }

  // Default: completed (agent ran to end) — conservative, avoid false negatives
  return { outcome: 'partial', reason: 'session ended without explicit outcome signal' };
}

/**
 * Handle the session_end hook.
 *
 * @param event  - The session_end event from OpenClaw.
 * @param ctx    - The hook context containing agent identity fields.
 * @param client - Shared engram REST client.
 * @param config - Resolved plugin config.
 */
export function handleSessionEnd(
  event: SessionEndEvent,
  ctx: PluginHookContext,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): void {
  try {
    if (!client.isAvailable()) return;

    const agentId = ctx.agentId ?? '';
    const identity = resolveIdentity(agentId, ctx.workspaceDir);
    const project = config.project ?? identity.projectId;

    const messages: ConversationMessage[] = Array.isArray(event.messages) ? event.messages : [];
    const sessionId = ctx.sessionId ?? ctx.sessionKey ?? agentId;
    if (!sessionId?.trim()) return;

    // 1. Backfill remaining conversation content (fire-and-forget)
    //    Filter in two layers: role-based (structural) + content-based (heuristic fallback).
    //    OpenClaw marks SDK keepalives / tool events with role='system'|'tool', so role
    //    filtering is the primary defense. classifyMessage catches edge cases where
    //    denials or metadata leak into user/assistant messages.
    if (config.autoExtract && messages.length > 0) {
      const filtered = messages.filter((m) => {
        // Primary filter: only keep real conversation roles.
        if (m.role !== 'user' && m.role !== 'assistant') return false;
        const content = typeof m.content === 'string' ? m.content : '';
        if (!content.trim()) return false;
        // Secondary filter: content classifier for embedded noise.
        return classifyMessage(content) === 'user_prompt';
      });
      const recent = filtered.slice(-MAX_MESSAGES);
      const content = recent
        .map((m) => `[${m.role}]: ${m.content}`)
        .join('\n\n');

      if (content) {
        const truncated = normalizeEngramContent(content);
        void client.backfillSession({
          session_id: sessionId,
          project,
          content: truncated,
        });

        (logger ?? console).warn(
          `[engram] session-end: submitted ${recent.length}/${messages.length} messages for backfill` +
            ` (filtered ${messages.length - filtered.length} heartbeat/system,` +
            ` project ${project}, reason: ${event.reason ?? 'unknown'})`,
        );
      }
    }

    // 2. Record session outcome (fire-and-forget)
    // Server accepts Claude session ID string directly — no DB ID lookup needed.
    void (async () => {
      try {
        const { outcome, reason } = detectOutcome(messages);
        await client.setSessionOutcome(sessionId, outcome, reason);
        (logger ?? console).warn(`[engram] session-end: outcome=${outcome} (${reason})`);
      } catch (err) {
        (logger ?? console).error('[engram] session-end outcome error:', err);
      }
    })();
  } catch (err) {
    (logger ?? console).error('[engram] hook error:', err);
  }
}
