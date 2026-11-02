/**
 * engram_suppress — suppress a retained memory or restore a flagged memory.
 * Suppression is reversible; neither action deletes the memory.
 */

import { z } from 'zod';
import { IntegerSchema } from '../validation.js';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

const SuppressParamsSchema = z.object({
  id: IntegerSchema.positive(),
  action: z.enum(['suppress', 'unsuppress']).optional().default('suppress'),
});

const suppressParameters = Type.Object({
  id: Type.Number({ description: 'Memory ID to suppress or restore' }),
  action: Type.Optional(Type.Union([
    Type.Literal('suppress'),
    Type.Literal('unsuppress'),
  ], { description: 'Default: suppress. Use unsuppress to restore a flagged memory.' })),
});

export function createEngramSuppressTool(
  _ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'engram_suppress',
    description:
      'Suppress a memory from future search results (reversible). ' +
      'Use action=unsuppress to restore a previously suppressed memory.',
    parameters: suppressParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = SuppressParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return 'engram is currently unreachable — suppress unavailable';
      }

      const { id, action } = parsed.data;
      const result = await client.transitionMemory(id, action, config.project);
      if (!result.ok) {
        return result.outcome === 'unknown'
          ? `Memory ${action} outcome unknown: ${result.error}`
          : `Failed to ${action} memory ${id}: ${result.error}`;
      }
      return action === 'unsuppress'
        ? `Restored memory ${id} — it can appear in search results again`
        : `Suppressed memory ${id} — it will no longer appear in search results`;
    },
  };
}
