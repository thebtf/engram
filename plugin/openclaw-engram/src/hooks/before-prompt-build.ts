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
import { TurnTracker } from '../context/tiers.js';
import type { TierResult } from '../context/tiers.js';
import type {
  BeforePromptBuildEvent,
  PromptBuildResult,
  PluginHookContext,
  PluginLogger,
} from '../types/openclaw.js';

const turnTracker = new TurnTracker();

/**
 * Handle the before_prompt_build hook.
 *
 * @param event  - The before_prompt_build event from OpenClaw.
 * @param ctx    - The hook context containing agent identity fields.
 * @param client - Shared engram REST client.
 * @param config - Resolved plugin config.
 * @returns      Context to prepend, or void if nothing to inject.
 */
export async function handleBeforePromptBuild(
  event: BeforePromptBuildEvent,
  ctx: PluginHookContext,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): Promise<PromptBuildResult | void> {
  try {
    if (!client.isAvailable()) return;
    if (!event.prompt || event.prompt.trim() === '') return;

    // Skip HEARTBEAT prompts — they are workspace health checks, not real user queries
    const promptLower = event.prompt.toLowerCase();
    if (promptLower.includes('heartbeat.md') || promptLower.includes('heartbeat_ok')) {
      return;
    }

    const tier: TierResult = turnTracker.classify(event.prompt ?? '', event.messages);
    logger?.debug(`[engram] before-prompt-build: tier=${tier.tier} budget=${tier.tokenBudget} reason=${tier.reason}`);

    if (tier.tier === 'NONE') return;

    const agentId = ctx.agentId ?? '';
    const identity = resolveIdentity(agentId, ctx.workspaceDir);
    const project = config.project ?? identity.projectId;

    let response;
    try {
      response = await client.searchContext({
        project,
        query: event.prompt,
        cwd: ctx.workspaceDir,
        agent_id: agentId,
      });
    } catch (err) {
      (logger ?? console).warn('[engram] before-prompt-build: searchContext failed', err);
      return;
    }

    if (!response || !Array.isArray(response.observations) || response.observations.length === 0) {
      // Track search miss for self-tuning analytics (fire-and-forget).
      // Normalize and truncate prompt to avoid sending raw PII or very long strings.
      const normalizedPrompt = event.prompt?.trim() ?? '';
      if (normalizedPrompt.length > 10) {
        const query = normalizedPrompt.replace(/\s+/g, ' ').slice(0, 512);
        void client.trackSearchMiss({ project, query }).catch(() => {});
      }
      return;
    }

    const { context, injectedIds, trimmedCount } = formatContext(
      response.observations,
      { tokenBudget: tier.tokenBudget },
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
          claudeSessionId: ctx.sessionId ?? agentId,
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
  } catch (err) {
    (logger ?? console).error('[engram] hook error:', err);
  }
}
