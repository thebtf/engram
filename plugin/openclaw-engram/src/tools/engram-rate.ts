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
  rating: z.enum(['useful', 'not_useful']),
});

const rateParameters = Type.Object({
  id: Type.Number({ description: 'Observation ID to rate' }),
  rating: Type.String({
    description: 'Rating value',
    enum: ['useful', 'not_useful'],
  }),
});

export function createEngramRateTool(
  _ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  _config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'engram_rate',
    description:
      'Rate a recalled observation with rating="useful" or rating="not_useful". ' +
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

      const success = await client.rateObservation(parsed.data.id, parsed.data.rating);
      return success
        ? `Rated observation ${parsed.data.id} as ${parsed.data.rating}`
        : `Failed to rate observation ${parsed.data.id}`;
    },
  };
}
