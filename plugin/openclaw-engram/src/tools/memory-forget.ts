/**
 * memory_forget — suppress or permanently delete observations from engram by ID.
 *
 * Default: suppress (reversible soft-hide from search results).
 * With permanent=true: archive (permanent removal).
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

const ForgetParamsSchema = z.object({
  id: z.string().regex(/^[1-9]\d*$/, 'Must be a positive integer'),
  permanent: z.boolean().optional().default(false),
});

const forgetParameters = Type.Object({
  id: Type.String({ description: 'Observation ID (positive integer)' }),
  permanent: Type.Optional(
    Type.Boolean({
      description: 'If true, permanently archives the observation. Default: false (reversible suppress).',
    }),
  ),
});

export function createMemoryForgetTool(
  _ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  _config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'memory_forget',
    label: 'Forget Memory',
    description:
      'Suppress an observation from future search results (reversible). ' +
      'Use permanent=true to permanently archive it instead.',
    parameters: forgetParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = ForgetParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return 'engram is currently unreachable — memory forget unavailable';
      }

      const numericId = parseInt(parsed.data.id, 10);
      if (!Number.isSafeInteger(numericId) || numericId <= 0) {
        return `Invalid observation ID: ${parsed.data.id}`;
      }

      if (parsed.data.permanent) {
        const response = await client.bulkDelete([String(numericId)]);
        if (!response) {
          return 'engram archive failed — server returned no response';
        }
        return response.deleted > 0
          ? `Permanently archived observation: ${parsed.data.id}`
          : `Observation not found or already archived: ${parsed.data.id}`;
      }

      const suppressed = await client.suppressObservation(numericId);
      return suppressed
        ? `Suppressed observation: ${parsed.data.id} (reversible — will no longer appear in search)`
        : `Failed to suppress observation: ${parsed.data.id}`;
    },
  };
}
