/**
 * memory_forget — suppress or archive a retained memory by ID.
 * Default: reversible suppression. With permanent=true: archive, not erasure.
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
      description: 'If true, archives the retained memory without erasing it. Default: false (reversible suppress).',
    }),
  ),
});

export function createMemoryForgetTool(
  _ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'memory_forget',
    label: 'Forget Memory',
    description:
      'Suppress an observation from future search results (reversible). ' +
      'Use permanent=true to archive it instead; the memory is retained, not erased.',
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

      const action = parsed.data.permanent ? 'archive' : 'suppress';
      const result = await client.transitionMemory(numericId, action, config.project);
      if (!result.ok) {
        return result.outcome === 'unknown'
          ? `Memory ${action} outcome unknown: ${result.error}`
          : `Failed to ${action} memory ${parsed.data.id}: ${result.error}`;
      }
      return parsed.data.permanent
        ? `Permanently archived memory: ${parsed.data.id} (retained, not erased)`
        : `Suppressed memory: ${parsed.data.id} (restore with engram_suppress action=unsuppress)`;
    },
  };
}
