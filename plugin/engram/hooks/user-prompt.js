#!/usr/bin/env node
'use strict';

const lib = require('./lib');

function asString(value) {
  return typeof value === 'string' ? value : '';
}

function escapeXmlTags(value) {
  return asString(value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

async function handleUserPrompt(ctx, input) {
  const prompt = asString(input.prompt) || asString(input.Prompt);
  const project = typeof ctx.Project === 'string' ? ctx.Project : '';
  const cwd = typeof ctx.CWD === 'string' ? ctx.CWD : '';

  // Skip system-generated messages and non-Claude-Code prompts
  if (prompt && (
    prompt.includes('<task-notification>') ||
    prompt.includes('<command-name>') ||
    prompt.includes('HEARTBEAT.md') ||
    prompt.startsWith('Read HEARTBEAT') ||
    prompt.includes('Conversation info (untrusted metadata)') ||
    prompt.includes('Sender (untrusted metadata)')
  )) {
    return '';
  }

  let contextToInject = '';
  let behaviorRulesBlock = '';
  let observationCount = 0;
  let matchedCount = 0;
  const searchIds = [];

  try {
    const searchResult = await lib.requestPost('/api/context/search', {
      project,
      query: prompt,
      cwd,
    });

    const observations = Array.isArray(searchResult.observations)
      ? searchResult.observations
      : [];

    // total_results reflects how many observations matched before the server-side
    // max_results cap was applied. When present and larger than the returned array,
    // it means the server truncated the results. Fall back to the array length only
    // when total_results is absent (older server versions).
    const totalResults =
      typeof searchResult.total_results === 'number'
        ? searchResult.total_results
        : observations.length;

    // Filter out credentials from context injection (leak prevention).
    // Credentials are only accessible via the dedicated get_credential MCP tool.
    const safeObservations = observations.filter(obs => {
      const t = asString(obs.type).toLowerCase();
      return t !== 'credential';
    });

    // Split: behavioral rules (concept: user-preference) vs technical observations.
    // Rules are injected as <user-behavior-rules> BEFORE <relevant-memory>.
    const behaviorRules = [];
    const technicalObs = [];
    for (const obs of safeObservations) {
      const concepts = Array.isArray(obs.concepts) ? obs.concepts : [];
      if (concepts.includes('user-preference')) {
        behaviorRules.push(obs);
      } else {
        technicalObs.push(obs);
      }
    }

    if (behaviorRules.length > 0) {
      behaviorRulesBlock = '<user-behavior-rules>\n';
      behaviorRulesBlock += '# Behavioral Rules From User Corrections\n';
      behaviorRulesBlock += 'These rules reflect how the user prefers to work. Follow them.\n\n';
      for (const rule of behaviorRules.slice(0, 10)) {
        const title = escapeXmlTags(rule.title);
        const narrative = escapeXmlTags(rule.narrative);
        behaviorRulesBlock += `## ${title}\n${narrative}\n\n`;
      }
      behaviorRulesBlock += '</user-behavior-rules>\n';
    }

    if (technicalObs.length > 0) {
      // Sort by similarity score (highest first)
      technicalObs.sort((a, b) => (b.similarity || 0) - (a.similarity || 0));

      // Dedup by title word overlap (>80% Jaccard = near-duplicate)
      const dedupedObs = [];
      for (const obs of technicalObs) {
        const title = asString(obs.title).toLowerCase();
        const words = new Set(title.split(/\s+/).filter(w => w.length > 2));
        let isDup = false;
        for (const kept of dedupedObs) {
          const keptTitle = asString(kept.title).toLowerCase();
          const keptWords = new Set(keptTitle.split(/\s+/).filter(w => w.length > 2));
          if (words.size === 0 || keptWords.size === 0) continue;
          const intersection = [...words].filter(w => keptWords.has(w)).length;
          const union = new Set([...words, ...keptWords]).size;
          if (union > 0 && intersection / union > 0.8) {
            isDup = true;
            break;
          }
        }
        if (!isDup) dedupedObs.push(obs);
      }

      // Group by type
      const groups = {
        decisions: [],
        patterns: [],
        changes: [],
        general: [],
      };
      for (const obs of dedupedObs) {
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

      // Token budget: ~4 chars per token, cap at 2000 tokens
      const TOKEN_BUDGET = 2000;
      let tokenCount = 0;
      const budgetObs = [];

      // Process in priority order: decisions > patterns > changes > general
      const ordered = [
        ...groups.decisions,
        ...groups.patterns,
        ...groups.changes,
        ...groups.general,
      ];
      for (const obs of ordered) {
        const title = asString(obs.title);
        const narrative = asString(obs.narrative);
        const facts = Array.isArray(obs.facts) ? obs.facts : [];
        let chars = title.length + narrative.length + 50;
        for (const f of facts) {
          if (typeof f === 'string') chars += f.length;
        }
        const tokens = Math.ceil(chars / 4);
        if (tokenCount + tokens > TOKEN_BUDGET && budgetObs.length > 0) {
          break;
        }
        tokenCount += tokens;
        budgetObs.push(obs);
      }

      const trimmed = ordered.length - budgetObs.length;
      if (trimmed > 0) {
        console.error(`[engram] Trimmed ${trimmed} observations to fit token budget (${TOKEN_BUDGET})`);
      }

      // Collect injected observation IDs after dedup and token trimming
      // so /mark-injected only tracks observations that actually made it into context.
      for (const obs of budgetObs) {
        if (obs && typeof obs === 'object' && typeof obs.id === 'number' && obs.id > 0) {
          searchIds.push(obs.id);
        }
      }

      // Re-group after budget trimming
      const finalGroups = {
        decisions: [],
        patterns: [],
        changes: [],
        general: [],
      };
      for (const obs of budgetObs) {
        const t = asString(obs.type).toLowerCase();
        if (t === 'decision') {
          finalGroups.decisions.push(obs);
        } else if (t === 'feature' || t === 'discovery') {
          finalGroups.patterns.push(obs);
        } else if (t === 'change' || t === 'refactor') {
          finalGroups.changes.push(obs);
        } else {
          finalGroups.general.push(obs);
        }
      }

      // observationCount tracks injected (post-trim) count for deciding whether
      // to return context. matchedCount is the true total matched count from the
      // server (pre-max_results-cap), so the badge shows meaningful signal.
      matchedCount = totalResults - (observations.length - technicalObs.length);
      observationCount = budgetObs.length;
      let contextBuilder = '<relevant-memory>\n';
      contextBuilder += '# Relevant Knowledge From Previous Sessions\n';
      contextBuilder +=
        'IMPORTANT: Use this information to answer the question directly. Do NOT explore the codebase if the answer is here.\n\n';

      let idx = 1;
      const sections = [
        { key: 'decisions', label: 'Decisions' },
        { key: 'patterns', label: 'Patterns & Best Practices' },
        { key: 'changes', label: 'Recent Changes' },
        { key: 'general', label: 'General Context' },
      ];

      for (const section of sections) {
        const sectionObs = finalGroups[section.key];
        if (sectionObs.length === 0) continue;

        contextBuilder += `### ${section.label}\n`;
        for (const obs of sectionObs) {
          const title = escapeXmlTags(obs.title);
          const obsType = escapeXmlTags(asString(obs.type).toUpperCase());
          const score = typeof obs.similarity === 'number' ? obs.similarity.toFixed(2) : '';
          const scoreTag = score ? ` [relevance: ${score}]` : '';
          const scopeTag = (typeof obs.scope === 'string' && obs.scope === 'global') ? ' [GLOBAL]' : '';

          contextBuilder += `## ${idx}. [${obsType}] ${title}${scopeTag}${scoreTag}\n`;

          if (Array.isArray(obs.facts) && obs.facts.length > 0) {
            contextBuilder += 'Key facts:\n';
            let hasFacts = false;
            for (const fact of obs.facts) {
              if (typeof fact === 'string' && fact !== '') {
                hasFacts = true;
                contextBuilder += `- ${escapeXmlTags(fact)}\n`;
              }
            }
            if (hasFacts) contextBuilder += '\n';
          }

          const narrative = escapeXmlTags(obs.narrative);
          if (narrative !== '') {
            contextBuilder += `${narrative}\n\n`;
          }

          idx++;
        }
      }

      contextBuilder += '\n---\n';
      contextBuilder += 'REMINDER: Before modifying any file mentioned above, call `find_by_file(files="path")` to check for additional context. ';
      contextBuilder += 'Before architectural decisions, call `decisions(query="...")`. These engram MCP tools are available and MUST be used.\n';
      contextBuilder += '</relevant-memory>\n';
      contextToInject = contextBuilder;
    }
  } catch (error) {
    console.error(`[engram] context search failed: ${error.message}`);
  }

  let sessionInitResult;
  try {
    sessionInitResult = await lib.requestPost('/api/sessions/init', {
      claudeSessionId: ctx.SessionID,
      project: ctx.Project,
      prompt,
      matchedObservations: matchedCount,
    });
  } catch (error) {
    console.error(`[user-prompt] Failed to initialize session: ${error.message}`);
    return '';
  }

  if (sessionInitResult && sessionInitResult.skipped === true) {
    console.error('[user-prompt] Session skipped (private)');
    return '';
  }

  const sessionDbId = Number(sessionInitResult && sessionInitResult.sessionDbId);
  const promptNumber = Number(sessionInitResult && sessionInitResult.promptNumber);

  if (!Number.isFinite(sessionDbId) || !Number.isFinite(promptNumber)) {
    console.error('[user-prompt] Invalid session init response: missing sessionDbId or promptNumber');
    return '';
  }

  const sessionID = Math.trunc(sessionDbId);
  const promptNo = Math.trunc(promptNumber);
  console.error(`[user-prompt] Session ${sessionID}, prompt #${promptNo}`);

  // Mark injected observations for this session (per-session tracking + global counter)
  if (searchIds.length > 0) {
    lib.requestPost(`/api/sessions/${sessionID}/mark-injected`, { ids: searchIds }, 3000).catch((err) => {
      console.error(`[engram] session mark-injected failed: ${err.message}`);
    });
  }

  lib
    .requestPost(`/sessions/${sessionID}/init`, {
      userPrompt: prompt,
      promptNumber: promptNo,
    })
    .catch((error) => {
      console.error(`[user-prompt] Failed to notify session start: ${error.message}`);
    });

  // Assemble final output: behavior rules FIRST (higher priority), then technical context.
  const output = behaviorRulesBlock + contextToInject;
  if (output) {
    console.error(`[engram] Injecting: ${behaviorRulesBlock ? 'behavior rules + ' : ''}${observationCount} observations`);
    return output;
  }

  return '';
}

(async () => {
  await lib.RunHook('UserPromptSubmit', handleUserPrompt);
})();
