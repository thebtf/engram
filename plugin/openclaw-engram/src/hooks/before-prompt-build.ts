/**
 * before_prompt_build hook — per-turn dynamic context search.
 *
 * Queries engram with the current user prompt and injects the top matching
 * observations as prependContext so they appear immediately before the user's
 * message in each turn.
 */

import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import { formatContext } from '../context/formatter.js';
import type {
  BeforePromptBuildEvent,
  PromptBuildResult,
  PluginLogger,
} from '../types/openclaw.js';

/**
 * Handle the before_prompt_build hook.
 *
 * @param event  - The before_prompt_build event from OpenClaw.
 * @param client - Shared engram REST client.
 * @param config - Resolved plugin config.
 * @returns      Context to prepend, or void if nothing to inject.
 */
export async function handleBeforePromptBuild(
  event: BeforePromptBuildEvent,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): Promise<PromptBuildResult | void> {
  if (!client.isAvailable()) return;
  if (!event.prompt || event.prompt.trim() === '') return;

  const agentId = event.agentId ?? '';
  const identity = resolveIdentity(agentId, event.workspaceDir);
  const project = config.project ?? identity.projectId;

  let response;
  try {
    response = await client.searchContext({
      project,
      query: event.prompt,
      cwd: event.workspaceDir,
      agent_id: agentId,
    });
  } catch (err) {
    (logger ?? console).warn('[engram] before-prompt-build: searchContext failed', err);
    return;
  }

  if (!response || !Array.isArray(response.observations) || response.observations.length === 0) {
    return;
  }

  const { context, injectedIds, trimmedCount } = formatContext(
    response.observations,
    { tokenBudget: config.tokenBudget },
  );

  if (trimmedCount > 0) {
    (logger ?? console).warn(
      `[engram] before-prompt-build: trimmed ${trimmedCount} observations to fit token budget`,
    );
  }

  if (!context) return;

  (logger ?? console).warn(
    `[engram] before-prompt-build: injecting ${injectedIds.length} observations for project ${project}`,
  );

  // Mark injected observations (fire-and-forget)
  if (injectedIds.length > 0) {
    try {
      const sessionResp = await client.initSession({
        claudeSessionId: event.sessionId ?? agentId,
        project,
        prompt: event.prompt,
      });
      if (sessionResp && !sessionResp.skipped && sessionResp.sessionDbId) {
        void client.markInjected(sessionResp.sessionDbId, injectedIds)
          .catch(() => { /* swallow — fire-and-forget */ });
      }
    } catch {
      // Non-critical — context was already injected
    }
  }

  return { prependContext: context };
}
