/**
 * session_start hook — initializes the engram session record.
 *
 * Context injection runs in before_prompt_build, which receives workspace
 * identity via ctx. This hook only calls initSession so the server
 * can track the conversation from the beginning.
 */

import { resolveProject } from '../client.js';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import type {
  SessionStartEvent,
  PluginHookContext,
  PluginLogger,
} from '../types/openclaw.js';

/**
 * Handle the session_start hook.
 *
 * @param event  - The session_start event from OpenClaw.
 * @param ctx    - The hook context containing agent identity fields.
 * @param client - Shared engram REST client.
 * @param config - Resolved plugin config.
 * @param logger - Optional logger (falls back to console).
 */
export async function handleSessionStart(
  event: SessionStartEvent,
  ctx: PluginHookContext,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): Promise<void> {
  try {
    if (!client.isAvailable()) return;
    const claudeSessionId = event.sessionId;
    if (!claudeSessionId?.trim() || (ctx.sessionId && ctx.sessionId !== claudeSessionId)) return;

    const agentId = ctx.agentId ?? '';
    const registration = await resolveProject(client, agentId, ctx.workspaceDir, config.project);
    if (!registration.ok) {
      (logger ?? console).warn(`[engram] session-start: project registration failed: ${registration.error.code}`);
      return;
    }
    const project = registration.canonicalProject;


    // Registration is awaited above; the data write may now be fire-and-forget.
    void client.initSession({
      claudeSessionId,
      project,
    });
  } catch (err) {
    (logger ?? console).error('[engram] hook error:', err);
  }
}
