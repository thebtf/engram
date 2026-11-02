/**
 * /remember command — quick observation storage shortcut.
 *
 * Usage: /remember <text>
 *
 * Stores the provided text as a "change" type observation in the current project.
 */

import { resolveProject } from '../client.js';
import type { EngramRestClient, MemoryInput } from '../client.js';
import type { PluginConfig } from '../config.js';
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

   const registration = await resolveProject(client, '', config.workspaceDir, config.project);
   if (!registration.ok) {
    return { text: `Project identity unavailable: ${registration.error.code} (${registration.error.upgradeAction})` };
   }
   const project = registration.canonicalProject;

   // Use the first sentence (up to 80 chars) as the title
   const firstSentence = text.split(/[.!?]/)[0]?.trim() ?? text;
   const title = firstSentence.length > 80
    ? firstSentence.slice(0, 77) + '...'
    : firstSentence;

   const content = text.length > CONTENT_MAX_CHARS
    ? text.slice(0, CONTENT_MAX_CHARS)
    : text;

   const observation: MemoryInput = {
    title,
    content,
    type: 'change',
    project,
    scope: 'project',
   };

   const [result] = await client.storeMemories([observation], { sourceAgent: 'openclaw-command' });
   if (result?.ok) {
    return { text: `Stored: "${title}"` };
   }
   return { text: `Failed to store memory: ${result?.error ?? 'No memory write confirmation'}` };
  },
 };
}
