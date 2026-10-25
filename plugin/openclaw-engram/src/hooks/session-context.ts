/** Static instructions used by the single before_prompt_build composition. */

import { quotedPromptScalar } from '../context/formatter.js';

/**
 * Build cacheable static instructions injected once per session.
 * These describe available engram capabilities to the agent.
 */
export function buildStaticInstructions(project: string): string {
 return [
  '# Engram Persistent Memory',
  '',
  'You have access to persistent memory via engram. Available tools:',
  '- `engram_search` — search prior observations, decisions, and patterns',
  '- `engram_remember` — store new observations for future sessions',
  '- `engram_decisions` — query architectural decisions',
  '',
  `Memory is scoped to project ${quotedPromptScalar(project)}. Use \`engram_remember\` to store important insights, decisions, and discoveries.`,
 ].join('\n');
}
