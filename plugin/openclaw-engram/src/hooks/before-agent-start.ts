/**
 * before_agent_start hook — fetches static session context from engram and
 * injects it as appendSystemContext so it is available for the entire session.
 *
 * This hook replaces the context-injection logic that was previously in
 * session_start. The SDK passes agent identity via ctx (not event), so all
 * identity resolution happens here via the ctx parameter.
 */

import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import {
  formatContext,
  formatRuleRouter,
  isRouterPayloadEnabled,
  quotedPromptScalar,
} from '../context/formatter.js';
import type {
  BeforeAgentStartEvent,
  BeforeAgentStartResult,
  PluginHookContext,
  PluginLogger,
} from '../types/openclaw.js';

/**
 * Handle the before_agent_start hook.
 *
 * @param event  - The before_agent_start event from OpenClaw.
 * @param ctx    - The hook context containing agent identity fields.
 * @param client - Shared engram REST client.
 * @param config - Resolved plugin config.
 * @param logger - Optional logger (falls back to console).
 * @returns      Append-system-context result, or void if nothing to inject.
 */
export async function handleBeforeAgentStart(
  event: BeforeAgentStartEvent,
  ctx: PluginHookContext,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): Promise<BeforeAgentStartResult | void> {
  try {
    if (!client.isAvailable()) return;

    const agentId = ctx.agentId ?? '';
    const identity = resolveIdentity(agentId, ctx.workspaceDir);
    const selectedProject = config.project ?? identity.projectId;
    const registration = await client.registerAndResolveProject(identity, selectedProject);
    if (!registration.ok) {
      (logger ?? console).warn(`[engram] before-agent-start: project registration failed: ${registration.error.code}`);
      return;
    }
    const project = registration.canonicalProject;

    const response = await client.getContextInject(
      agentId,
      ctx.workspaceDir,
      project,
    );

    if (!response) return;

    const observations = Array.isArray(response.observations) ? response.observations : [];
    const { context, injectedIds, trimmedCount } = formatContext(
      observations,
      { tokenBudget: config.tokenBudget },
    );
    const routerBlock = isRouterPayloadEnabled(response.rule_router)
      ? formatRuleRouter(response.rule_router)
      : '';

    if (trimmedCount > 0) {
      (logger ?? console).warn(
        `[engram] before-agent-start: trimmed ${trimmedCount} observations to fit token budget`,
      );
    }

    if (!context && !routerBlock) return;

    // Mark observations as injected (fire-and-forget)
    if (injectedIds.length > 0 && response.sessionId) {
      void client.markInjected(response.sessionId, injectedIds);
    }

    // Initialize session tracking (fire-and-forget)
    const claudeSessionId = ctx.sessionId ?? agentId;
    if (!claudeSessionId) {
      (logger ?? console).warn(
        '[engram] before-agent-start: no sessionId or agentId available — skipping session init',
      );
    } else {
      void client.initSession({
        claudeSessionId,
        project,
        prompt: event.initialPrompt,
      });
    }

    (logger ?? console).warn(
      `[engram] before-agent-start: injected ${injectedIds.length} observations for project ${project}`,
    );

    // Build static instructions + dynamic session context
    const staticInstructions = buildStaticInstructions(project);
    const dynamicContext = [routerBlock, context].filter(Boolean).join('\n\n');
    const fullContext = staticInstructions + '\n\n' + dynamicContext;

    return { appendSystemContext: fullContext };
  } catch (err) {
    (logger ?? console).error('[engram] hook error:', err);
  }
}

/**
 * Build cacheable static instructions injected once per session.
 * These describe available engram capabilities to the agent.
 */
export function buildStaticInstructions(project: string): string {
  return [
    '# Engram Persistent Memory',
    '',
    'You have access to persistent memory via engram. Available tools:',
    '- `engram_search` — search prior observations, decisions, and patterns',
    '- `engram_remember` — store new observations for future sessions',
    '- `engram_decisions` — query architectural decisions',
    '',
    `Memory is scoped to project ${quotedPromptScalar(project)}. Use \`engram_remember\` to store important insights, decisions, and discoveries.`,
  ].join('\n');
}
