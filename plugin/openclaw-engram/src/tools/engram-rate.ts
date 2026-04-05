/**
 * engram_rate — rate an observation as useful or not useful.
 *
 * WHEN TO USE: After recalling a memory that influenced your work.
 * Rate it so engram learns what to prioritize in future sessions.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

const RateParamsSchema = z.object({
  id: z.number().int().positive(),
  useful: z.boolean(),
});

const rateParameters = Type.Object({
  id: Type.Number({ description: 'Observation ID to rate' }),
  useful: Type.Boolean({ description: 'true if the observation was helpful, false if not' }),
});

export function createEngramRateTool(
  _ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  _config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'engram_rate',
    description:
      'Rate a recalled observation as useful or not useful. ' +
      'Call this AFTER using a memory that influenced your response — helps engram learn what to prioritize.',
    parameters: rateParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = RateParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return 'engram is currently unreachable — rating unavailable';
      }

      const success = await client.rateObservation(parsed.data.id, parsed.data.useful);
      const label = parsed.data.useful ? 'useful' : 'not useful';
      return success
        ? `Rated observation ${parsed.data.id} as ${label}`
        : `Failed to rate observation ${parsed.data.id}`;
    },
  };
}
