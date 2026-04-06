#!/usr/bin/env node
'use strict';

const lib = require('./lib');

function getString(value) {
  return typeof value === 'string' ? value : '';
}

function escapeXmlTags(text) {
  if (typeof text !== 'string') return '';
  return text.replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

/**
 * PostCompact: re-inject full behavioral rules after context compaction.
 *
 * Context compaction drops session-start injections. Without this hook,
 * behavioral rules (user-preference + always-inject) are lost for the
 * remainder of the session. user-prompt.js injects compact titles only.
 * This hook restores full rules (title + narrative) once after compaction.
 */
async function handlePostCompact(ctx, input) {
  if (!process.env.ENGRAM_URL) {
    return '';
  }

  const project = typeof ctx.Project === 'string' ? ctx.Project : '';
  if (!project) {
    console.error('[post-compact] No project context, skipping rules re-injection');
    return '';
  }

  // Fetch behavioral rules from server (same as session-start)
  let searchResult;
  try {
    searchResult = await lib.requestPost('/api/context/search', {
      project,
      query: 'behavioral rules user preferences',
      obs_type: 'guidance',
    });
  } catch (error) {
    console.error(`[post-compact] Failed to fetch behavioral rules: ${error.message}`);
    return '';
  }

  // Collect always-inject + user-preference observations
  const alwaysInject = Array.isArray(searchResult.always_inject)
    ? searchResult.always_inject
    : [];
  const observations = Array.isArray(searchResult.observations)
    ? searchResult.observations
    : [];

  const behaviorRules = [];
  for (const obs of observations) {
    const concepts = Array.isArray(obs.concepts) ? obs.concepts : [];
    if (concepts.includes('user-preference')) {
      behaviorRules.push(obs);
    }
  }

  // Merge and deduplicate
  const allRules = [...alwaysInject, ...behaviorRules];
  const seenIds = new Set();
  const uniqueRules = [];
  for (const rule of allRules) {
    const id = rule && typeof rule.id === 'number' ? rule.id : null;
    if (id !== null && seenIds.has(id)) continue;
    if (id !== null) seenIds.add(id);
    uniqueRules.push(rule);
  }

  if (uniqueRules.length === 0) {
    console.error('[post-compact] No behavioral rules to re-inject');
    return '';
  }

  // Build full rules block (same format as session-start)
  let output = '<user-behavior-rules>\n';
  output += '# Behavioral Rules (Re-injected After Compaction)\n';
  output += 'These rules were originally injected at session start. Re-injected because context was compacted.\n\n';
  for (const rule of uniqueRules.slice(0, 20)) {
    if (!rule || typeof rule !== 'object') continue;
    const title = escapeXmlTags(getString(rule.title));
    const narrative = escapeXmlTags(getString(rule.narrative));
    output += `## ${title}\n`;
    if (narrative !== '') {
      output += `${narrative}\n`;
    }
    output += '\n';
  }
  output += '</user-behavior-rules>\n';

  console.error(`[post-compact] Re-injected ${uniqueRules.length} full behavioral rules`);
  return output;
}

(async () => {
  await lib.RunHook('PostCompact', handlePostCompact);
})();
