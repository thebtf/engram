/**
 * engram_timeline — fetch timeline of observations.
 *
 * WHEN TO USE: When exploring what happened recently in a project,
 * or when you need temporal context around a specific event.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient, Observation } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

const TimelineParamsSchema = z.object({
  mode: z.enum(['recent', 'anchor', 'query']).optional().default('recent'),
  query: z.string().optional(),
  anchor_id: z.number().int().positive().optional(),
  limit: z.number().int().min(1).max(50).optional().default(20),
});

const timelineParameters = Type.Object({
  mode: Type.Optional(Type.String({
    description: 'Timeline mode: recent (default), anchor (around specific ID), query (search + timeline)',
    enum: ['recent', 'anchor', 'query'],
  })),
  query: Type.Optional(Type.String({ description: 'Search query (for query mode)' })),
  anchor_id: Type.Optional(Type.Number({ description: 'Observation ID (for anchor mode)' })),
  limit: Type.Optional(Type.Number({ description: 'Max observations to return (default: 20)' })),
});

export function createEngramTimelineTool(
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'engram_timeline',
    description:
      'Fetch a timeline of observations. Modes: recent (what happened lately), ' +
      'anchor (context around a specific observation), query (search + timeline).',
    parameters: timelineParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = TimelineParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return 'engram is currently unreachable — timeline unavailable';
      }

      const identity = resolveIdentity(ctx.agentId ?? '', ctx.workspaceDir);
      const project = config.project ?? identity.projectId;

      const observations = await client.getTimeline(project, parsed.data.mode, {
        query: parsed.data.query,
        anchor_id: parsed.data.anchor_id,
        limit: parsed.data.limit,
      });

      if (observations.length === 0) {
        return 'No observations found in timeline.';
      }

      return formatTimeline(observations);
    },
  };
}

function formatTimeline(observations: Observation[]): string {
  let out = '# Timeline\n\n';
  observations.forEach((obs, i) => {
    const typeLabel = (obs.type ?? 'observation').toUpperCase();
    out += `${i + 1}. [${typeLabel}] ${obs.title ?? 'Untitled'}\n`;
    if (obs.narrative) out += `   ${obs.narrative.slice(0, 200)}\n`;
  });
  return out.trimEnd();
}
