/**
 * session_start hook — fetches static session context from engram and injects
 * it as appendSystemContext so it is available for the entire session.
 */

import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import { formatContext } from '../context/formatter.js';
import type {
  SessionStartEvent,
  SessionStartResult,
  PluginLogger,
} from '../types/openclaw.js';

/**
 * Handle the session_start hook.
 *
 * @param event  - The session_start event from OpenClaw.
 * @param client - Shared engram REST client.
 * @param config - Resolved plugin config.
 * @returns      Append-system-context result or void.
 */
export async function handleSessionStart(
  event: SessionStartEvent,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): Promise<SessionStartResult | void> {
  try {
    if (!client.isAvailable()) return;

    const agentId = event.agentId ?? '';
    const identity = resolveIdentity(agentId, event.workspaceDir);
    const project = config.project ?? identity.projectId;

    const response = await client.getContextInject(
      agentId,
      event.workspaceDir,
    );

    if (!response || !Array.isArray(response.observations) || response.observations.length === 0) {
      return;
    }

    const { context, injectedIds, trimmedCount } = formatContext(
      response.observations,
      { tokenBudget: config.tokenBudget },
    );

    if (trimmedCount > 0) {
      (logger ?? console).warn(`[engram] session-start: trimmed ${trimmedCount} observations to fit token budget`);
    }

    if (!context) return;

    // Mark observations as injected (fire-and-forget)
    if (injectedIds.length > 0 && response.sessionId) {
      void client.markInjected(response.sessionId, injectedIds);
    }

    // Initialize session tracking (fire-and-forget)
    const claudeSessionId = event.sessionId ?? agentId;
    if (!claudeSessionId) {
      (logger ?? console).warn('[engram] session-start: no sessionId or agentId available — skipping session init');
    } else {
      void client.initSession({
        claudeSessionId,
        project,
        prompt: event.initialPrompt,
      });
    }

    (logger ?? console).warn(
      `[engram] session-start: injected ${injectedIds.length} observations for project ${project}`,
    );

    // Build static instructions + dynamic session context
    const staticInstructions = buildStaticInstructions(project);
    const fullContext = staticInstructions + '\n\n' + context;

    return { appendSystemContext: fullContext };
  } catch (err) {
    (logger ?? console).error('[engram] hook error:', err);
  }
}

/**
 * Build cacheable static instructions injected once per session.
 * These describe available engram capabilities to the agent.
 */
function buildStaticInstructions(project: string): string {
  return [
    '# Engram Persistent Memory',
    '',
    'You have access to persistent memory via engram. Available tools:',
    '- `engram_search` — search prior observations, decisions, and patterns',
    '- `engram_remember` — store new observations for future sessions',
    '- `engram_decisions` — query architectural decisions',
    '',
    `Memory is scoped to project "${project}". Use \`engram_remember\` to store important insights, decisions, and discoveries.`,
  ].join('\n');
}
