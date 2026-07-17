/**
 * before_prompt_build hook — per-turn dynamic context search.
 *
 * Queries engram with the current user prompt and injects the top matching
 * observations as prependContext so they appear immediately before the user's
 * message in each turn.
 */

import { resolveAndRegisterProject } from '../client.js';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import {
  formatContext,
  formatAlwaysInject,
  formatRuleRouter,
  isRouterPayloadEnabled,
} from '../context/formatter.js';
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
    const registration = await resolveAndRegisterProject(client, agentId, ctx.workspaceDir, config.project);
    if (!registration.ok) {
      (logger ?? console).warn(`[engram] before-prompt-build: project registration failed: ${registration.error.code}`);
      return;
    }
    const project = registration.canonicalProject;

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

    if (!response) return;

    const observations = Array.isArray(response.observations) ? response.observations : [];
    const routerBlock = isRouterPayloadEnabled(response.rule_router)
      ? formatRuleRouter(response.rule_router)
      : '';
    const alwaysInjectObs = Array.isArray(response.always_inject) ? response.always_inject : [];
    const alwaysInjectBlock = !routerBlock && alwaysInjectObs.length > 0
      ? formatAlwaysInject(alwaysInjectObs)
      : '';

    if (observations.length === 0) {
      // Track search miss for self-tuning analytics (fire-and-forget).
      // Normalize and truncate prompt to avoid sending raw PII or very long strings.
      const normalizedPrompt = event.prompt?.trim() ?? '';
      if (normalizedPrompt.length > 10) {
        const query = normalizedPrompt.replace(/\s+/g, ' ').slice(0, 512);
        void client.trackSearchMiss({ project, query }).catch(() => {});
      }
      if (!routerBlock && !alwaysInjectBlock) return;
    }

    const { context, injectedIds, trimmedCount } = formatContext(
      observations,
      { tokenBudget: tier.tokenBudget },
    );

    if (trimmedCount > 0) {
      (logger ?? console).warn(
        `[engram] before-prompt-build: trimmed ${trimmedCount} observations to fit token budget`,
      );
    }

    if (!context && !alwaysInjectBlock && !routerBlock) return;

    (logger ?? console).warn(
      `[engram] before-prompt-build: injecting ${injectedIds.length} observations + ${routerBlock ? 'router packets' : `${alwaysInjectObs.length} always_inject rules`} for project ${project}`,
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

    // Combine: router/always-inject guidance first, then query-matched context.
    const combined = [routerBlock, alwaysInjectBlock, context].filter(Boolean).join('\n');
    return { prependContext: combined };
  } catch (err) {
    (logger ?? console).error('[engram] hook error:', err);
  }
}
