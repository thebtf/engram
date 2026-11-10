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
  'Engram may provide persistent project memory when its tools are available:',
  '- `engram_search` — search prior observations, decisions, and patterns',
  '- `engram_remember` — store authorized, reusable observations',
  '- `engram_decisions` — query prior architectural decisions',
  '',
  `Memory is scoped to project ${quotedPromptScalar(project)}. Use delivered evidence first; look up additional context only when useful to the current task. Store only within the user's scope and authorization.`,
 ].join('\n');
}
