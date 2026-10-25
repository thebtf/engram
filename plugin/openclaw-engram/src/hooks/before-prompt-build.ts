/** before_prompt_build: resolve current identity, then compose static and turn context once. */

import { resolveProject } from '../client.js';
import type { EngramRestClient, ContextSearchResponse } from '../client.js';
import type { PluginConfig } from '../config.js';
import { formatPromptContext } from '../context/formatter.js';
import { classifyTurn } from '../context/tiers.js';
import { classifyMessage } from './message-classifier.js';
import { buildStaticInstructions } from './session-context.js';
import type { BeforePromptBuildEvent, PromptBuildResult, PluginHookContext, PluginLogger } from '../types/openclaw.js';

export async function handleBeforePromptBuild(
  event: BeforePromptBuildEvent,
  ctx: PluginHookContext,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): Promise<PromptBuildResult | void> {
  try {
    if (!client.isAvailable()) return;
    const agentId = ctx.agentId ?? '';
    const registration = await resolveProject(client, agentId, ctx.workspaceDir, config.project);
    if (!registration.ok) {
      (logger ?? console).warn(`[engram] before-prompt-build: project registration failed: ${registration.error.code}`);
      return;
    }
    const project = registration.canonicalProject;
    const sessionId = ctx.sessionId?.trim() ? ctx.sessionId : undefined;
    const session = await client.getContextInject(agentId, ctx.workspaceDir, project, sessionId);
    const query = event.currentUserMessage ?? event.prompt;
    const tier = classifyTurn(query, event.messages);
    let turn: ContextSearchResponse | null = null;
    if (query?.trim() && classifyMessage(query) === 'user_prompt' && tier.tier !== 'NONE') {
      turn = await client.searchContext({
        project, query, cwd: ctx.workspaceDir, agent_id: agentId,
        ...(sessionId ? { session_id: sessionId } : {}), source: 'openclaw',
      });
      if (turn && (turn.observations ?? []).length === 0 && query.trim().length > 10) {
        void client.trackSearchMiss({ project, query: query.trim().replace(/\s+/g, ' ').slice(0, 512) }).catch(() => { });
      }
    }
    const result = formatPromptContext(session, turn, {
      tokenBudget: config.tokenBudget ?? 2000,
      contextLimit: config.contextLimit ?? 10,
      sessionContextLimit: config.sessionContextLimit ?? 20,
      turnBudget: tier.tokenBudget,
      instructions: buildStaticInstructions(project),
    });
    if (result.budgetExceeded) {
      (logger ?? console).warn('[engram] prompt budget cannot fit complete required guidance; skipping enrichment');
      return;
    }
    if (!result.appendSystemContext && !result.prependContext) return;
    if (sessionId) {
      const initialized = await client.initSession({ claudeSessionId: sessionId, project, ...(query?.trim() ? { prompt: query } : {}) });
      if (result.injectedIds.length > 0 && initialized && !initialized.skipped && initialized.sessionDbId) {
        void client.markInjected(initialized.sessionDbId, result.injectedIds).catch(() => { });
      }
    }
    return { appendSystemContext: result.appendSystemContext, prependContext: result.prependContext };
  } catch (err) {
    (logger ?? console).error('[engram] hook error:', err);
  }
}
