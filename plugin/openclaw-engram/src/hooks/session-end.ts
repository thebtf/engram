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

  // Success signals: explicit completion markers
  const successPatterns = [
    'task complete', 'done', 'finished', 'implemented', 'fixed',
    'merged', 'deployed', 'resolved', 'committed', 'created pr',
  ];
  const hasSuccess = successPatterns.some((p) => textContent.includes(p));

  // Failure signals: explicit error/failure markers
  const failurePatterns = [
    'failed', 'error', 'cannot', 'unable to', 'broke', 'regression',
  ];
  const hasFailure = failurePatterns.some((p) => textContent.includes(p));

  if (hasSuccess && !hasFailure) {
    return { outcome: 'success', reason: 'completion signals detected' };
  }
  if (hasSuccess && hasFailure) {
    return { outcome: 'partial', reason: 'mixed success and failure signals' };
  }
  if (hasFailure) {
    return { outcome: 'failure', reason: 'failure signals detected' };
  }

  // Default: if session had meaningful messages but no clear outcome
  return messages.length >= 3
    ? { outcome: 'partial', reason: 'session ended without clear completion signal' }
    : { outcome: 'abandoned', reason: 'short session with no outcome signals' };
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
    if (config.autoExtract && messages.length > 0) {
      const recent = messages.slice(-MAX_MESSAGES);
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
          `[engram] session-end: submitted ${recent.length} messages for backfill` +
            ` (project ${project}, reason: ${event.reason ?? 'unknown'})`,
        );
      }
    }

    // 2. Record session outcome (fire-and-forget)
    // Resolve DB session ID — may not exist if session was never initialized
    void (async () => {
      try {
        const sessionResp = await client.initSession({
          claudeSessionId: sessionId,
          project,
          prompt: '',
        });

        const dbSessionId =
          sessionResp && typeof sessionResp === 'object' && 'id' in sessionResp
            ? Number(sessionResp.id)
            : 0;

        if (dbSessionId <= 0) {
          (logger ?? console).warn('[engram] session-end: no DB session ID — skipping outcome');
          return;
        }

        const { outcome, reason } = detectOutcome(messages);
        await client.setSessionOutcome(dbSessionId, outcome, reason);
        (logger ?? console).warn(`[engram] session-end: outcome=${outcome} (${reason})`);
      } catch (err) {
        (logger ?? console).error('[engram] session-end outcome error:', err);
      }
    })();
  } catch (err) {
    (logger ?? console).error('[engram] hook error:', err);
  }
}
