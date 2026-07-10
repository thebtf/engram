/**
 * session_start hook — initializes the engram session record.
 *
 * Context injection has moved to the before_agent_start hook, which receives
 * full agent identity via ctx. This hook only calls initSession so the server
 * can track the conversation from the beginning.
 */

import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
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

    const agentId = ctx.agentId ?? '';
    const identity = resolveIdentity(agentId, ctx.workspaceDir);
    const selectedProject = config.project ?? identity.projectId;
    const registration = await client.registerAndResolveProject(identity, selectedProject);
    if (!registration.ok) {
      (logger ?? console).warn(`[engram] session-start: project registration failed: ${registration.error.code}`);
      return;
    }
    const project = registration.canonicalProject;

    const claudeSessionId = ctx.sessionId ?? agentId;
    if (!claudeSessionId) {
      (logger ?? console).warn(
        '[engram] session-start: no sessionId or agentId available — skipping session init',
      );
      return;
    }

    // Registration is awaited above; the data write may now be fire-and-forget.
    void client.initSession({
      claudeSessionId,
      project,
      prompt: event.initialPrompt,
    });
  } catch (err) {
    (logger ?? console).error('[engram] hook error:', err);
  }
}
