/**
 * engram_find_by_file — find observations related to a specific file.
 *
 * WHEN TO USE: BEFORE modifying any file. Returns what engram knows about it —
 * past bugs, decisions, patterns, and important context that should inform your changes.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient, Observation } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

const FindByFileParamsSchema = z.object({
  file: z.string().min(1),
  limit: z.number().int().min(1).max(20).optional().default(5),
});

const findByFileParameters = Type.Object({
  file: Type.String({ description: 'File path to look up (absolute or relative)' }),
  limit: Type.Optional(Type.Number({ description: 'Max observations to return (default: 5)' })),
});

export function createEngramFindByFileTool(
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'engram_find_by_file',
    description:
      'Find observations related to a specific file. ' +
      'Call BEFORE modifying any file to check what engram knows about it — ' +
      'past bugs, decisions, patterns, and context that should inform your changes.',
    parameters: findByFileParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = FindByFileParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return 'engram is currently unreachable — file context unavailable';
      }

      const identity = resolveIdentity(ctx.agentId ?? '', ctx.workspaceDir);
      const project = config.project ?? identity.projectId;

      const observations = await client.getFileContext(
        parsed.data.file,
        project,
        parsed.data.limit,
      );

      if (observations.length === 0) {
        return `No observations found for file: ${parsed.data.file}`;
      }

      return formatFileContext(parsed.data.file, observations);
    },
  };
}

function formatFileContext(file: string, observations: Observation[]): string {
  let out = `# Known Context for ${file}\n\n`;
  observations.forEach((obs, i) => {
    const typeLabel = (obs.type ?? 'observation').toUpperCase();
    out += `## ${i + 1}. [${typeLabel}] ${obs.title ?? 'Untitled'}\n`;
    if (obs.narrative) out += `${obs.narrative}\n`;
    out += '\n';
  });
  return out.trimEnd();
}
