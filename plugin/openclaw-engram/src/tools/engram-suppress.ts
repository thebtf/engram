/**
 * engram_suppress — suppress an observation from future search results.
 *
 * WHEN TO USE: When an observation is outdated, wrong, or no longer relevant.
 * Suppression is reversible — the observation stays in the database but is hidden from search.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

const SuppressParamsSchema = z.object({
  id: z.number().int().positive(),
});

const suppressParameters = Type.Object({
  id: Type.Number({ description: 'Observation ID to suppress' }),
});

export function createEngramSuppressTool(
  _ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  _config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'engram_suppress',
    description:
      'Suppress an observation from future search results (reversible). ' +
      'Use when an observation is outdated, wrong, or no longer relevant.',
    parameters: suppressParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = SuppressParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return 'engram is currently unreachable — suppress unavailable';
      }

      const success = await client.suppressObservation(parsed.data.id);
      return success
        ? `Suppressed observation ${parsed.data.id} — it will no longer appear in search results`
        : `Failed to suppress observation ${parsed.data.id}`;
    },
  };
}
