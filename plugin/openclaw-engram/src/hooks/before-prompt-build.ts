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
import { formatContext, formatAlwaysInject } from '../context/formatter.js';
import { TurnTracker } from '../context/tiers.js';
import type { TierResult } from '../context/tiers.js';
import { classifyMessage } from './message-classifier.js';
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

    // Skip non-user messages — heartbeats and SDK metadata are not real queries
    const category = classifyMessage(event.prompt);
    if (category !== 'user_prompt') return;

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
        source: 'openclaw',
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

    // Render always_inject behavioral rules (appended before the main context block).
    // These are observations marked always_inject=true on the server — they contain
    // behavioral guidance that must be present in every turn regardless of query match.
    const alwaysInjectObs = Array.isArray(response.always_inject) ? response.always_inject : [];
    const alwaysInjectBlock = alwaysInjectObs.length > 0
      ? formatAlwaysInject(alwaysInjectObs)
      : '';

    if (!context && !alwaysInjectBlock) return;

    const totalInjected = injectedIds.length + alwaysInjectObs.length;
    (logger ?? console).warn(
      `[engram] before-prompt-build: injecting ${injectedIds.length} observations + ${alwaysInjectObs.length} always_inject rules for project ${project}`,
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

    // Combine: always_inject rules first (highest priority), then query-matched context
    const combined = [alwaysInjectBlock, context].filter(Boolean).join('\n');
    void totalInjected; // used only in log message above
    return { prependContext: combined };
  } catch (err) {
    (logger ?? console).error('[engram] hook error:', err);
  }
}
