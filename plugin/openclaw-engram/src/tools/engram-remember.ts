/**
 * engram_remember + memory_store — store observations in engram memory.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient, BulkImportRequest } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

const CONTENT_MAX_CHARS = 900;

const RememberParamsSchema = z.object({
  title: z.string().min(1),
  content: z.string().min(1),
  type: z.enum(['decision', 'feature', 'change', 'refactor', 'discovery', 'bugfix']).default('change'),
  scope: z.enum(['project', 'global']).default('project'),
  tags: z.array(z.string()).optional(),
});

const StoreParamsSchema = z.object({
  text: z.string().min(1).optional(),
  content: z.string().min(1).optional(),
  title: z.string().optional(),
  category: z.enum(['preference', 'decision', 'entity', 'fact', 'other']).optional(),
  tags: z.array(z.string()).optional(),
}).refine((d) => Boolean(d.text || d.content), { message: 'Either text or content is required' });

const rememberParameters = Type.Object({
  title: Type.String({ description: 'Short descriptive title for the observation' }),
  content: Type.String({ description: 'Content/narrative to remember (max 900 chars)' }),
  type: Type.Optional(Type.Union([
    Type.Literal('decision'), Type.Literal('feature'), Type.Literal('change'),
    Type.Literal('refactor'), Type.Literal('discovery'), Type.Literal('bugfix'),
  ], { description: 'Observation type', default: 'change' })),
  scope: Type.Optional(Type.Union([
    Type.Literal('project'), Type.Literal('global'),
  ], { description: 'Scope: project-local or global', default: 'project' })),
  tags: Type.Optional(Type.Array(Type.String(), { description: 'Optional tags' })),
});

const storeParameters = Type.Object({
  text: Type.Optional(Type.String({ description: 'Text to remember (compat alias for content)' })),
  content: Type.Optional(Type.String({ description: 'Content to remember' })),
  title: Type.Optional(Type.String({ description: 'Short title (auto-generated if omitted)' })),
  category: Type.Optional(Type.Union([
    Type.Literal('preference'), Type.Literal('decision'), Type.Literal('entity'),
    Type.Literal('fact'), Type.Literal('other'),
  ], { description: 'Memory category' })),
  tags: Type.Optional(Type.Array(Type.String(), { description: 'Optional tags' })),
});

const CATEGORY_TO_TYPE: Record<string, string> = {
  preference: 'change',
  decision: 'decision',
  entity: 'change',
  fact: 'discovery',
  other: 'change',
};

async function storeObservation(
  title: string,
  content: string,
  type: string,
  scope: string,
  tags: string[] | undefined,
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): Promise<string> {
  if (!client.isAvailable()) {
    return 'engram is currently unreachable — memory store unavailable';
  }

  const identity = resolveIdentity(ctx.agentId ?? '', ctx.workspaceDir);
  const project = config.project ?? identity.projectId;

  const trimmedContent = content.length > CONTENT_MAX_CHARS ? content.slice(0, CONTENT_MAX_CHARS) : content;

  const observation: BulkImportRequest = {
    title,
    content: trimmedContent,
    type,
    project,
    scope,
    tags,
  };

  const response = await client.bulkImport([observation]);
  if (!response) {
    return 'engram store failed — server returned no response';
  }

  if (response.imported > 0) {
    return `Stored: "${title}" (type: ${type}, scope: ${scope})`;
  }
  if (response.skipped > 0) {
    return `Observation skipped (likely a near-duplicate): "${title}"`;
  }

  const errMsg = response.errors?.join(', ') ?? 'unknown error';
  return `Failed to store observation: ${errMsg}`;
}

export function createEngramRememberTool(
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'engram_remember',
    description:
      'Store an observation in engram persistent memory. ' +
      'Use this to record decisions, discoveries, patterns, or important context for future sessions.',
    parameters: rememberParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = RememberParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }
      return storeObservation(
        parsed.data.title, parsed.data.content, parsed.data.type, parsed.data.scope,
        parsed.data.tags, ctx, client, config,
      );
    },
  };
}

export function createMemoryStoreTool(
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'memory_store',
    label: 'Store Memory',
    description:
      'Store a memory for future sessions. Accepts text or content parameter.',
    parameters: storeParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = StoreParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      const content = parsed.data.text ?? parsed.data.content ?? '';
      const title = parsed.data.title ?? (content.length > 80 ? content.slice(0, 77) + '...' : content);
      const type = parsed.data.category ? (CATEGORY_TO_TYPE[parsed.data.category] ?? 'change') : 'change';

      return storeObservation(title, content, type, 'project', parsed.data.tags, ctx, client, config);
    },
  };
}
