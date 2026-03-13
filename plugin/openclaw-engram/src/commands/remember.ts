/**
 * /remember command — quick observation storage shortcut.
 *
 * Usage: /remember <text>
 *
 * Stores the provided text as a "change" type observation in the current project.
 */

import type { EngramRestClient, BulkImportRequest } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { OpenClawPluginCommandDefinition, PluginCommandContext } from '../types/openclaw.js';

const CONTENT_MAX_CHARS = 900;

/**
 * Build the /remember command definition.
 */
export function buildRememberCommand(
  client: EngramRestClient,
  config: PluginConfig,
): OpenClawPluginCommandDefinition {
  return {
    name: 'remember',
    description: 'Quickly store a note in engram memory',
    acceptsArgs: true,

    async handler(ctx: PluginCommandContext) {
      const text = (ctx.args ?? '').trim();
      if (!text) {
        return { text: 'Usage: /remember <text to remember>' };
      }

      if (!client.isAvailable()) {
        return { text: 'engram is currently unreachable — cannot store memory' };
      }

      const identity = resolveIdentity('', undefined);
      const project = config.project ?? identity.projectId;

      // Use the first sentence (up to 80 chars) as the title
      const firstSentence = text.split(/[.!?]/)[0]?.trim() ?? text;
      const title = firstSentence.length > 80
        ? firstSentence.slice(0, 77) + '...'
        : firstSentence;

      const content = text.length > CONTENT_MAX_CHARS
        ? text.slice(0, CONTENT_MAX_CHARS)
        : text;

      const observation: BulkImportRequest = {
        title,
        content,
        type: 'change',
        project,
        scope: 'project',
      };

      const response = await client.bulkImport([observation]);
      if (!response) {
        return { text: 'Failed to store memory — engram returned no response' };
      }

      if (response.imported > 0) {
        return { text: `Stored: "${title}"` };
      }

      if (response.skipped > 0) {
        return { text: `Skipped (likely a near-duplicate): "${title}"` };
      }

      const errMsg = response.errors?.join(', ') ?? 'unknown error';
      return { text: `Failed to store memory: ${errMsg}` };
    },
  };
}
