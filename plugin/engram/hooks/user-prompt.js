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

function buildSearchRequest(project, prompt, cwd, filesBeingEdited) {
  const request = {
    project,
    query: prompt,
    cwd,
  };
  if (Array.isArray(filesBeingEdited) && filesBeingEdited.length > 0) {
    request.files_being_edited = filesBeingEdited.filter((entry) => typeof entry === 'string' && entry !== '');
  }
  return request;
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
  let injectedRulesCount = 0;
  let observationCount = 0;
  let matchedCount = 0;
  const searchIds = [];
  const filesBeingEdited = ctx.SessionID ? lib.getSessionFiles(ctx.SessionID) : [];

  try {
    const searchResult = await lib.requestPost('/api/context/search', buildSearchRequest(project, prompt, cwd, filesBeingEdited));

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

    // Always-inject tier: unconditional rules from server (FR-1, FR-6).
    // These are separate from similarity-matched results — tagged with concept "always-inject".
    const alwaysInjectRules = Array.isArray(searchResult.always_inject)
      ? searchResult.always_inject
      : [];

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

    // Behavioral rules: inject compact titles only (not full narrative).
    // Session-start injects full rules once. After compaction, those may be lost.
    // This lightweight reminder (~300-500 tokens) ensures rules survive compaction.
    //
    // Exception: when PostCompact sets a "compacted" signal, we inject FULL rules
    // (title + narrative) instead of compact titles. PostCompact cannot inject
    // context directly (hookSpecificOutput is a discriminated union that rejects
    // non-PreToolUse/UserPromptSubmit/PostToolUse hooks), so it delegates to us
    // via session signals.
    const allBehaviorRules = [...alwaysInjectRules, ...behaviorRules];
    const seenRuleIds = new Set();
    const uniqueRules = [];
    for (const rule of allBehaviorRules) {
      const ruleId = rule && typeof rule.id === 'number' ? rule.id : null;
      if (ruleId !== null && seenRuleIds.has(ruleId)) continue;
      if (ruleId !== null) seenRuleIds.add(ruleId);
      uniqueRules.push(rule);
    }

    // Check if PostCompact signaled that context was compacted
    const signals = lib.getSessionSignals(ctx.SessionID);
    const wasCompacted = signals && signals.compacted > 0;
    if (wasCompacted) {
      // Reset compacted signal so subsequent prompts use compact format
      lib.incrementSessionSignals(ctx.SessionID, { compacted: -(signals.compacted || 0) });
    }

    injectedRulesCount = uniqueRules.length;
    if (injectedRulesCount > 0) {
      if (wasCompacted) {
        // Full rules after compaction (title + narrative, ~2-3K tokens)
        behaviorRulesBlock = '<user-behavior-rules>\n';
        behaviorRulesBlock += '# Behavioral Rules (Re-injected After Compaction)\n';
        behaviorRulesBlock += 'These rules were originally injected at session start. Re-injected because context was compacted.\n\n';
        for (const rule of uniqueRules.slice(0, 20)) {
          if (!rule || typeof rule !== 'object') continue;
          behaviorRulesBlock += `## ${escapeXmlTags(asString(rule.title))}\n`;
          const narrative = escapeXmlTags(asString(rule.narrative));
          if (narrative !== '') {
            behaviorRulesBlock += `${narrative}\n`;
          }
          behaviorRulesBlock += '\n';
        }
        behaviorRulesBlock += '</user-behavior-rules>\n';
      } else {
        // Compact titles for regular prompts (~300-500 tokens)
        behaviorRulesBlock = '<behavioral-rules>\n';
        for (const rule of uniqueRules.slice(0, 20)) {
          behaviorRulesBlock += `- ${escapeXmlTags(asString(rule.title))}\n`;
        }
        behaviorRulesBlock += '</behavioral-rules>\n';
      }
    }

    // Filter out observations with negligible similarity (noise from global scope leak).
    // Preserve observations without a similarity score (e.g., always-inject tagged).
    const MIN_SIMILARITY = 0.10;
    const relevantObs = technicalObs.filter(obs => {
      if (typeof obs.similarity !== 'number') return true; // no score = keep (always-inject)
      return obs.similarity >= MIN_SIMILARITY;
    });

    if (relevantObs.length > 0) {
      // Sort by similarity score (highest first)
      relevantObs.sort((a, b) => (b.similarity || 0) - (a.similarity || 0));

      // Dedup by title word overlap (>80% Jaccard = near-duplicate)
      const dedupedObs = [];
      for (const obs of relevantObs) {
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

      // Separate wiki and entity observations from regular observations
      const wikiObs = [];
      const entityObs = [];
      const regularObs = [];
      for (const obs of dedupedObs) {
        const t = asString(obs.type).toLowerCase();
        if (t === 'wiki') {
          wikiObs.push(obs);
        } else if (t === 'entity') {
          entityObs.push(obs);
        } else {
          regularObs.push(obs);
        }
      }

      // Group regular observations by type
      const groups = {
        decisions: [],
        patterns: [],
        changes: [],
        general: [],
      };
      for (const obs of regularObs) {
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
      observationCount = budgetObs.length + wikiObs.length + entityObs.length;

      // Build wiki knowledge section (before relevant-memory)
      let wikiBlock = '';
      if (wikiObs.length > 0) {
        const wikiCap = Math.min(wikiObs.length, 3); // Cap at 3 wiki results
        wikiBlock = '<wiki-knowledge>\n';
        for (let i = 0; i < wikiCap; i++) {
          const w = wikiObs[i];
          wikiBlock += `## ${escapeXmlTags(asString(w.title))}\n`;
          const narrative = escapeXmlTags(asString(w.narrative));
          if (narrative) wikiBlock += `${narrative}\n`;
          wikiBlock += '\n';
          if (w && typeof w.id === 'number' && w.id > 0) searchIds.push(w.id);
        }
        wikiBlock += '</wiki-knowledge>\n';
      }

      // Entity references as one-liners (after wiki, before relevant-memory)
      if (entityObs.length > 0) {
        for (const e of entityObs.slice(0, 5)) {
          wikiBlock += `[ENTITY] ${escapeXmlTags(asString(e.title))} — ${escapeXmlTags(asString(e.subtitle || e.narrative))}\n`;
          if (e && typeof e.id === 'number' && e.id > 0) searchIds.push(e.id);
        }
        if (entityObs.length > 0) wikiBlock += '\n';
      }

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

          contextBuilder += `## ${idx}. [${obsType}] ${title}${scopeTag}${scoreTag} (id:${obs.id || '?'})\n`;

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

      contextBuilder += '</relevant-memory>\n';
      contextToInject = wikiBlock + contextBuilder;
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
    .requestPost(`/api/sessions/${sessionID}/init`, {
      userPrompt: prompt,
      promptNumber: promptNo,
    })
    .catch((error) => {
      console.error(`[user-prompt] Failed to notify session start: ${error.message}`);
    });

  // Combine: compact behavioral rule titles + technical observations
  const output = behaviorRulesBlock + contextToInject;
  if (output) {
    console.error(`[engram] Injecting: ${behaviorRulesBlock ? injectedRulesCount + ' rules + ' : ''}${observationCount} observations`);
    return output;
  }

  return '';
}

if (require.main === module) {
  (async () => {
    await lib.RunHook('UserPromptSubmit', handleUserPrompt);
  })();
}

module.exports = {
  buildSearchRequest,
  handleUserPrompt,
};
