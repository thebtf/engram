/**
 * memory_forget — delete observations from engram by ID.
 *
 * Maps to engram's bulk_delete_observations endpoint.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

const ForgetParamsSchema = z.object({
  id: z.string().min(1),
});

const forgetParameters = Type.Object({
  id: Type.String({ description: 'Observation UUID to delete' }),
});

export function createMemoryForgetTool(
  _ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  _config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'memory_forget',
    label: 'Forget Memory',
    description: 'Delete a specific observation from engram memory by its ID.',
    parameters: forgetParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = ForgetParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return 'engram is currently unreachable — memory forget unavailable';
      }

      const response = await client.bulkDelete([parsed.data.id]);
      if (!response) {
        return 'engram delete failed — server returned no response';
      }

      return response.deleted > 0
        ? `Deleted observation: ${parsed.data.id}`
        : `Observation not found or already deleted: ${parsed.data.id}`;
    },
  };
}
