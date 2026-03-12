/**
 * memory_get — dual-mode memory retrieval.
 *
 * If `path` param is provided: reads a local workspace .md file.
 * Otherwise: performs an engram search as fallback.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import { readFile } from 'node:fs/promises';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import { formatContext } from '../context/formatter.js';
import type { AnyAgentTool, OpenClawPluginToolContext, OpenClawPluginApi } from '../types/openclaw.js';

const GetParamsSchema = z.object({
  path: z.string().optional(),
  query: z.string().optional(),
}).refine((d) => Boolean(d.path || d.query), { message: 'Either path or query is required' });

const getParameters = Type.Object({
  path: Type.Optional(Type.String({ description: 'Workspace-relative path to a .md file' })),
  query: Type.Optional(Type.String({ description: 'Search query (used if path not provided)' })),
});

export function createMemoryGetTool(
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
  api: OpenClawPluginApi,
): AnyAgentTool {
  return {
    name: 'memory_get',
    label: 'Get Memory',
    description:
      'Retrieve a memory by file path (workspace .md files) or search query (engram fallback).',
    parameters: getParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = GetParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      // Mode 1: Local file read
      if (parsed.data.path) {
        return readLocalFile(parsed.data.path, api);
      }

      // Mode 2: Engram search fallback
      if (parsed.data.query) {
        return searchEngram(parsed.data.query, ctx, client, config);
      }

      return 'Either path or query must be provided.';
    },
  };
}

async function readLocalFile(filePath: string, api: OpenClawPluginApi): Promise<string> {
  try {
    const resolved = api.resolvePath(filePath);
    const content = await readFile(resolved, 'utf-8');
    if (!content.trim()) {
      return `File is empty: ${filePath}`;
    }
    return content;
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    return `Failed to read file "${filePath}": ${msg}`;
  }
}

async function searchEngram(
  query: string,
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): Promise<string> {
  if (!client.isAvailable()) {
    return 'engram is currently unreachable — memory get unavailable';
  }

  const identity = resolveIdentity(ctx.agentId ?? '', ctx.workspaceDir);
  const project = config.project ?? identity.projectId;

  const response = await client.searchContext({
    project,
    query,
    cwd: ctx.workspaceDir,
    agent_id: ctx.agentId,
  });

  if (!response) {
    return 'engram search failed — server returned no response';
  }

  const observations = Array.isArray(response.observations) ? response.observations : [];
  if (observations.length === 0) {
    return 'No relevant memories found.';
  }

  const { context } = formatContext(observations, { tokenBudget: config.tokenBudget });
  return context || `Found ${observations.length} observation(s) but could not format context.`;
}
