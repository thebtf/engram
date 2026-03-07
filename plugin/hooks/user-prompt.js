#!/usr/bin/env node
'use strict';

const lib = require('./lib');

function asString(value) {
  return typeof value === 'string' ? value : '';
}

async function handleUserPrompt(ctx, input) {
  const prompt = asString(input.prompt) || asString(input.Prompt);
  const project = typeof ctx.Project === 'string' ? ctx.Project : '';
  const cwd = typeof ctx.CWD === 'string' ? ctx.CWD : '';

  let contextToInject = '';
  let observationCount = 0;
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

    // Collect injected observation IDs for per-session tracking (called after sessionID is known)
    for (const obs of observations) {
      if (obs && typeof obs === 'object' && typeof obs.id === 'number' && obs.id > 0) {
        searchIds.push(obs.id);
      }
    }

    if (observations.length > 0) {
      observationCount = observations.length;
      let contextBuilder = '<relevant-memory>\n';
      contextBuilder += '# Relevant Knowledge From Previous Sessions\n';
      contextBuilder +=
        'IMPORTANT: Use this information to answer the question directly. Do NOT explore the codebase if the answer is here.\n\n';

      for (let i = 0; i < observations.length; i++) {
        const obs = observations[i];
        if (!obs || typeof obs !== 'object') {
          continue;
        }

        const title = asString(obs.title);
        const obsType = asString(obs.type);
        contextBuilder += `## ${i + 1}. [${obsType}] ${title}\n`;

        if (Array.isArray(obs.facts) && obs.facts.length > 0) {
          let hasFacts = false;
          contextBuilder += 'Key facts:\n';
          for (const fact of obs.facts) {
            if (typeof fact === 'string' && fact !== '') {
              hasFacts = true;
              contextBuilder += `- ${fact}\n`;
            }
          }
          if (hasFacts) {
            contextBuilder += '\n';
          }
        }

        const narrative = asString(obs.narrative);
        if (narrative !== '') {
          contextBuilder += `${narrative}\n\n`;
        }
      }

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
      matchedObservations: observationCount,
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

  if (observationCount > 0) {
    console.error(`[engram] Found ${observationCount} relevant memories for this prompt`);
    return contextToInject;
  }

  return '';
}

(async () => {
  await lib.RunHook('UserPromptSubmit', handleUserPrompt);
})();
