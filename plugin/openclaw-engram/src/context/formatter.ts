/**
 * Context formatter — converts engram observations into an XML context block
 * suitable for injection into agent prompts.
 *
 * Faithfully ported from plugin/engram/hooks/user-prompt.js with TypeScript types.
 */

import type { Observation } from '../client.js';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface FormatterOptions {
  /** Token budget (~4 chars per token). Default: 2000. */
  tokenBudget?: number;
}

interface GroupedObservations {
  decisions: Observation[];
  patterns: Observation[];
  changes: Observation[];
  general: Observation[];
}

const SECTION_DEFS: Array<{ key: keyof GroupedObservations; label: string }> = [
  { key: 'decisions', label: 'Decisions' },
  { key: 'patterns', label: 'Patterns & Best Practices' },
  { key: 'changes', label: 'Recent Changes' },
  { key: 'general', label: 'General Context' },
];

// ---------------------------------------------------------------------------
// Format result
// ---------------------------------------------------------------------------

export interface FormatResult {
  /** Formatted XML context string, or empty string if nothing to inject. */
  context: string;
  /** IDs of observations that made it into the context (after dedup + budget trim). */
  injectedIds: number[];
  /** Number of observations trimmed by token budget. */
  trimmedCount: number;
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Format always_inject observations into a compact behavioral rules block.
 *
 * These are observations marked always_inject=true on the server. They represent
 * standing behavioral rules (e.g., "always use X", "never do Y") that must be
 * injected into every turn regardless of query relevance.
 *
 * Rendered as a lightweight XML block separate from the main engram-context so
 * the agent can distinguish standing rules from query-matched knowledge.
 *
 * @param observations - The always_inject observations from the search response.
 * @returns            A non-empty XML block string, or '' if nothing to render.
 */
export function formatAlwaysInject(observations: Observation[]): string {
  const safe = observations.filter(
    (obs) => asString(obs.type).toLowerCase() !== 'credential',
  );
  if (safe.length === 0) return '';

  let out = '<engram-behavioral-rules>\n';
  out += '# Standing Behavioral Rules (Always Active)\n';
  out += 'IMPORTANT: These rules apply to ALL tasks in this session. Follow them unconditionally.\n\n';

  for (let i = 0; i < safe.length; i++) {
    const obs = safe[i];
    const title = escapeXml(obs.title);
    const obsType = escapeXml(asString(obs.type).toUpperCase());
    const scopeTag =
      typeof obs.scope === 'string' && obs.scope === 'global' ? ' [GLOBAL]' : '';
    out += `## ${i + 1}. [${obsType}] ${title}${scopeTag}\n`;

    const facts = Array.isArray(obs.facts) ? obs.facts : [];
    if (facts.length > 0) {
      let hasFacts = false;
      for (const fact of facts) {
        if (typeof fact === 'string' && fact !== '') {
          if (!hasFacts) {
            out += 'Key facts:\n';
            hasFacts = true;
          }
          out += `- ${escapeXml(fact)}\n`;
        }
      }
      if (hasFacts) out += '\n';
    }

    const narrative = escapeXml(obs.narrative);
    if (narrative !== '') out += `${narrative}\n\n`;
  }

  out += '</engram-behavioral-rules>\n';
  return out;
}

/**
 * Format an array of engram observations into an XML context block.
 *
 * Steps:
 *   1. Filter credentials
 *   2. Sort by similarity (descending)
 *   3. Jaccard dedup (>0.8 title word overlap)
 *   4. Group by type (decisions → patterns → changes → general)
 *   5. Apply token budget
 *   6. Re-group and render XML
 */
export function formatContext(
  observations: Observation[],
  options: FormatterOptions = {},
): FormatResult {
  const tokenBudget = options.tokenBudget ?? 2000;

  // 1. Filter credentials
  const safe = observations.filter(
    (obs) => asString(obs.type).toLowerCase() !== 'credential',
  );

  if (safe.length === 0) {
    return { context: '', injectedIds: [], trimmedCount: 0 };
  }

  // 2. Sort by similarity (highest first)
  const sorted = [...safe].sort((a, b) => (b.similarity ?? 0) - (a.similarity ?? 0));

  // 3. Jaccard dedup
  const deduped = jaccardDedup(sorted);

  // 4. Group and priority-order
  const grouped = groupByType(deduped);
  const ordered: Observation[] = [
    ...grouped.decisions,
    ...grouped.patterns,
    ...grouped.changes,
    ...grouped.general,
  ];

  // 5. Token budget
  const { budgeted, trimmedCount } = applyTokenBudget(ordered, tokenBudget);

  // Collect injected IDs
  const injectedIds: number[] = [];
  for (const obs of budgeted) {
    if (obs.id > 0) injectedIds.push(obs.id);
  }

  if (budgeted.length === 0) {
    return { context: '', injectedIds: [], trimmedCount };
  }

  // 6. Re-group and render
  const finalGroups = groupByType(budgeted);
  const context = renderXml(finalGroups);

  return { context, injectedIds, trimmedCount };
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

function asString(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function escapeXml(value: unknown): string {
  return asString(value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

/**
 * Deduplicate observations by title word overlap (Jaccard > 0.8 = near-duplicate).
 * Preserves the higher-similarity observation (input is already sorted).
 */
function jaccardDedup(observations: Observation[]): Observation[] {
  const kept: Observation[] = [];
  for (const obs of observations) {
    const title = asString(obs.title).toLowerCase();
    const words = new Set(title.split(/\s+/).filter((w) => w.length > 2));
    let isDup = false;
    for (const k of kept) {
      const kTitle = asString(k.title).toLowerCase();
      const kWords = new Set(kTitle.split(/\s+/).filter((w) => w.length > 2));
      if (words.size === 0 || kWords.size === 0) continue;
      const intersection = [...words].filter((w) => kWords.has(w)).length;
      const union = new Set([...words, ...kWords]).size;
      if (union > 0 && intersection / union > 0.8) {
        isDup = true;
        break;
      }
    }
    if (!isDup) kept.push(obs);
  }
  return kept;
}

function groupByType(observations: Observation[]): GroupedObservations {
  const groups: GroupedObservations = {
    decisions: [],
    patterns: [],
    changes: [],
    general: [],
  };
  for (const obs of observations) {
    const t = asString(obs.type).toLowerCase();
    if (t === 'decision') {
      groups.decisions.push(obs);
    } else if (t === 'feature' || t === 'discovery') {
      groups.patterns.push(obs);
    } else if (t === 'change' || t === 'refactor') {
      groups.changes.push(obs);
    } else {
      groups.general.push(obs);
    }
  }
  return groups;
}

function applyTokenBudget(
  observations: Observation[],
  tokenBudget: number,
): { budgeted: Observation[]; trimmedCount: number } {
  let tokenCount = 0;
  const budgeted: Observation[] = [];

  for (const obs of observations) {
    const title = asString(obs.title);
    const narrative = asString(obs.narrative);
    const facts = Array.isArray(obs.facts) ? obs.facts : [];
    let chars = title.length + narrative.length + 50;
    for (const f of facts) {
      if (typeof f === 'string') chars += f.length;
    }
    const tokens = Math.ceil(chars / 4);
    if (tokenCount + tokens > tokenBudget) continue;
    tokenCount += tokens;
    budgeted.push(obs);
  }

  return { budgeted, trimmedCount: observations.length - budgeted.length };
}

function renderXml(groups: GroupedObservations): string {
  let out = '<engram-context>\n';
  out += '# Relevant Knowledge From Previous Sessions\n';
  out +=
    'IMPORTANT: Use this information to answer the question directly. Do NOT explore the codebase if the answer is here.\n\n';

  let idx = 1;
  for (const { key, label } of SECTION_DEFS) {
    const sectionObs = groups[key];
    if (sectionObs.length === 0) continue;

    out += `### ${label}\n`;
    for (const obs of sectionObs) {
      const title = escapeXml(obs.title);
      const obsType = escapeXml(asString(obs.type).toUpperCase());
      const score =
        typeof obs.similarity === 'number' ? obs.similarity.toFixed(2) : '';
      const scoreTag = score ? ` [relevance: ${score}]` : '';
      const scopeTag =
        typeof obs.scope === 'string' && obs.scope === 'global' ? ' [GLOBAL]' : '';

      out += `## ${idx}. [${obsType}] ${title}${scopeTag}${scoreTag}\n`;

      const facts = Array.isArray(obs.facts) ? obs.facts : [];
      if (facts.length > 0) {
        out += 'Key facts:\n';
        let hasFacts = false;
        for (const fact of facts) {
          if (typeof fact === 'string' && fact !== '') {
            hasFacts = true;
            out += `- ${escapeXml(fact)}\n`;
          }
        }
        if (hasFacts) out += '\n';
      }

      const narrative = escapeXml(obs.narrative);
      if (narrative !== '') out += `${narrative}\n\n`;

      idx++;
    }
  }

  out += '\n---\n';
  out +=
    'REMINDER: Before modifying any file mentioned above, call `engram_search(query="path")` to check for additional context. ';
  out +=
    'Before architectural decisions, call `engram_decisions(query="...")`. These engram tools are available and MUST be used.\n';
  out += '</engram-context>\n';
  return out;
}
