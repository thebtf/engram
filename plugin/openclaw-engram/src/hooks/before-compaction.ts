/**
 * before_compaction hook — transcript backfill before context window compaction.
 *
 * Serializes the recent conversation messages and submits them to engram's
 * /api/backfill/session endpoint for server-side observation extraction.
 * This is fire-and-forget: the hook must never block compaction.
 */

import { resolveProject } from '../client.js';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { serializeConversation } from './content.js';
import type { BeforeCompactionEvent, PluginHookContext, PluginLogger } from '../types/openclaw.js';

/**
 * Handle the before_compaction hook.
 *
 * @param event  - The before_compaction event from OpenClaw.
 * @param ctx    - The hook context containing agent identity fields.
 * @param client - Shared engram REST client.
 * @param config - Resolved plugin config.
 */
export async function handleBeforeCompaction(
  event: BeforeCompactionEvent,
  ctx: PluginHookContext,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): Promise<void> {
  try {
    if (!client.isAvailable()) return;
    if (!config.autoExtract) return;

    const agentId = ctx.agentId ?? '';
    const sessionId = ctx.sessionId;
    if (!sessionId?.trim()) return; // no session identity available — skip
    const registration = await resolveProject(client, agentId, ctx.workspaceDir, config.project);
    if (!registration.ok) {
      (logger ?? console).warn(`[engram] before-compaction: project registration failed: ${registration.error.code}`);
      return;
    }
    const project = registration.canonicalProject;

    const messages = Array.isArray(event.messages) ? event.messages : [];
    const content = serializeConversation(messages);
    if (!content) return;

    // Registration is awaited above; the data write may now be fire-and-forget.
    void client.backfillSession({
      session_id: sessionId,
      project,
      content,
    });

    (logger ?? console).warn(
      `[engram] transcript: submitting bounded visible conversation for project ${project}`,
    );
  } catch (err) {
    (logger ?? console).error('[engram] hook error:', err);
  }
}
