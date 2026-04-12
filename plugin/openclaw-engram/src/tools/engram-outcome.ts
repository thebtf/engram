/**
 * engram_outcome — record session outcome for closed-loop learning.
 *
 * WHEN TO USE: At the end of a session or task. Records whether the goal was achieved,
 * enabling engram to learn which observations lead to successful outcomes.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

const OutcomeParamsSchema = z.object({
  outcome: z.enum(['success', 'partial', 'failure', 'abandoned']),
  reason: z.string().optional(),
});

const outcomeParameters = Type.Object({
  outcome: Type.String({
    description: 'Session outcome: success, partial, failure, or abandoned',
    enum: ['success', 'partial', 'failure', 'abandoned'],
  }),
  reason: Type.Optional(Type.String({ description: 'Brief explanation of the outcome' })),
});

export function createEngramOutcomeTool(
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'engram_outcome',
    description:
      'Record the outcome of the current session (success/partial/failure/abandoned). ' +
      'Call at the END of a task or session. Enables closed-loop learning — engram uses outcomes ' +
      'to learn which observations lead to better results.',
    parameters: outcomeParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = OutcomeParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return 'engram is currently unreachable — outcome recording unavailable';
      }

      // Server endpoint accepts Claude session ID string directly
      const claudeSessionId = ctx.sessionId ?? ctx.sessionKey ?? '';
      if (!claudeSessionId) {
        return 'Cannot record outcome — no session ID available';
      }

      const success = await client.setSessionOutcome(
        claudeSessionId,
        parsed.data.outcome,
        parsed.data.reason,
      );

      return success
        ? `Session outcome recorded: ${parsed.data.outcome}${parsed.data.reason ? ` (${parsed.data.reason})` : ''}`
        : 'Failed to record session outcome';
    },
  };
}
