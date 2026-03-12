/**
 * engram_decisions — query architectural decisions stored in engram.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient, Observation } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

const DecisionsParamsSchema = z.object({
  query: z.string().min(1),
});

const decisionsParameters = Type.Object({
  query: Type.String({ description: 'Query to search for relevant architectural decisions' }),
});

export function createEngramDecisionsTool(
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'engram_decisions',
    description:
      'Query architectural decisions and design choices stored in engram. ' +
      'Use this before making architectural decisions to surface prior reasoning and constraints.',
    parameters: decisionsParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = DecisionsParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return 'engram is currently unreachable — decisions query unavailable';
      }

      const identity = resolveIdentity(ctx.agentId ?? '', ctx.workspaceDir);
      const project = config.project ?? identity.projectId;

      const response = await client.searchContext({
        project,
        query: parsed.data.query,
        cwd: ctx.workspaceDir,
        agent_id: ctx.agentId,
      });

      if (!response) {
        return 'engram decisions query failed — server returned no response';
      }

      const observations = (Array.isArray(response.observations) ? response.observations : [])
        .filter((obs) => obs.type?.toLowerCase() === 'decision');

      if (observations.length === 0) {
        return 'No architectural decisions found for this query.';
      }

      return formatDecisions(observations);
    },
  };
}

function formatDecisions(decisions: Observation[]): string {
  let out = '# Relevant Architectural Decisions\n\n';
  decisions.forEach((d, i) => {
    const score = typeof d.similarity === 'number' ? ` [relevance: ${d.similarity.toFixed(2)}]` : '';
    const scopeTag = d.scope === 'global' ? ' [GLOBAL]' : '';
    out += `## ${i + 1}. ${d.title}${scopeTag}${score}\n`;
    const facts = Array.isArray(d.facts) ? d.facts : [];
    if (facts.length > 0) {
      out += 'Rationale:\n';
      for (const fact of facts) {
        if (typeof fact === 'string' && fact) out += `- ${fact}\n`;
      }
      out += '\n';
    }
    if (d.narrative) out += `${d.narrative}\n\n`;
  });
  return out.trimEnd();
}
