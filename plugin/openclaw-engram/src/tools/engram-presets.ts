/**
 * engram_changes and engram_how_it_works — search preset wrappers.
 *
 * These are thin wrappers around searchContext with preset parameters,
 * providing shortcut access to common query patterns.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient, Observation } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

const PresetParamsSchema = z.object({
  query: z.string().min(1),
});

const presetParameters = Type.Object({
  query: Type.String({ description: 'Search query' }),
});

function createPresetTool(
  name: string,
  description: string,
  preset: string,
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return {
    name,
    description,
    parameters: presetParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = PresetParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return `engram is currently unreachable — ${name} unavailable`;
      }

      const identity = resolveIdentity(ctx.agentId ?? '', ctx.workspaceDir);
      const project = config.project ?? identity.projectId;

      const response = await client.searchContext({
        project,
        query: parsed.data.query,
        cwd: ctx.workspaceDir,
        agent_id: ctx.agentId,
        preset,
      });

      const observations: Observation[] = response?.observations ?? [];
      if (observations.length === 0) {
        return `No results found for: ${parsed.data.query}`;
      }

      let out = `# ${preset === 'changes' ? 'Recent Changes' : 'How It Works'}\n\n`;
      observations.forEach((obs, i) => {
        const typeLabel = (obs.type ?? 'observation').toUpperCase();
        out += `## ${i + 1}. [${typeLabel}] ${obs.title ?? 'Untitled'}\n`;
        if (obs.narrative) out += `${obs.narrative}\n`;
        out += '\n';
      });
      return out.trimEnd();
    },
  };
}

export function createEngramChangesTool(
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return createPresetTool(
    'engram_changes',
    'Find recent code changes, refactorings, and modifications. ' +
    'Use when you need to understand what changed recently in the codebase.',
    'changes',
    ctx,
    client,
    config,
  );
}

export function createEngramHowItWorksTool(
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return createPresetTool(
    'engram_how_it_works',
    'Understand system architecture, design patterns, and implementation details. ' +
    'Use when exploring unfamiliar code to get a high-level understanding.',
    'how_it_works',
    ctx,
    client,
    config,
  );
}
