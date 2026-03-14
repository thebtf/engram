#!/usr/bin/env node
'use strict';

const fs = require('fs');
const os = require('os');
const readline = require('readline');

const lib = require('./lib');

function extractTextContent(content) {
  if (typeof content === 'string') {
    return content;
  }

  if (!Array.isArray(content)) {
    return '';
  }

  let out = '';
  for (const part of content) {
    if (!part || typeof part !== 'object') {
      continue;
    }

    if (part.type === 'text' && typeof part.text === 'string') {
      out += part.text;
    }
  }

  return out;
}

function expandTranscriptPath(transcriptPath) {
  if (typeof transcriptPath !== 'string' || transcriptPath === '') {
    return transcriptPath;
  }

  if (!transcriptPath.startsWith('~')) {
    return transcriptPath;
  }

  const home = os.homedir();
  if (!home) {
    return transcriptPath;
  }

  if (transcriptPath === '~') {
    return home;
  }

  const separator = transcriptPath[1];
  if (separator === '/' || separator === '\\') {
    return `${home}${transcriptPath.slice(1)}`;
  }

  return transcriptPath;
}

const MAX_MESSAGES = 50;
const MAX_MESSAGE_LENGTH = 5000;

function truncateText(text, maxLen) {
  if (typeof text !== 'string') return '';
  return text.length <= maxLen ? text : text.slice(0, maxLen);
}

async function parseTranscript(transcriptPath) {
  const expandedPath = expandTranscriptPath(transcriptPath);
  if (!expandedPath) {
    return { lastUser: '', lastAssistant: '', messages: [] };
  }

  return new Promise((resolve) => {
    let lastUser = '';
    let lastAssistant = '';
    const messages = [];

    const stream = fs.createReadStream(expandedPath, {
      encoding: 'utf8',
      highWaterMark: 1024 * 1024,
    });

    stream.on('error', (error) => {
      console.error(`[stop] Failed to read transcript: ${error.message}`);
      resolve({ lastUser, lastAssistant, messages });
    });

    const rl = readline.createInterface({
      input: stream,
      crlfDelay: Infinity,
    });

    rl.on('line', (line) => {
      if (!line || !line.trim()) {
        return;
      }

      let item = null;
      try {
        item = JSON.parse(line);
      } catch (error) {
        return;
      }

      const messageRole =
        typeof item.type === 'string'
          ? item.type.toLowerCase()
          : item.message && typeof item.message.role === 'string'
          ? item.message.role.toLowerCase()
          : '';

      const messageText =
        item.message && Object.prototype.hasOwnProperty.call(item.message, 'content')
          ? extractTextContent(item.message.content)
          : '';

      if (messageRole === 'user') {
        lastUser = messageText;
        messages.push({ role: 'user', text: truncateText(messageText, MAX_MESSAGE_LENGTH) });
      } else if (messageRole === 'assistant') {
        lastAssistant = messageText;
        messages.push({ role: 'assistant', text: truncateText(messageText, MAX_MESSAGE_LENGTH) });
      }

      // Ring buffer: keep only last MAX_MESSAGES
      if (messages.length > MAX_MESSAGES) {
        messages.shift();
      }
    });

    rl.on('close', () => {
      resolve({ lastUser, lastAssistant, messages });
    });
  });
}

/**
 * Detect whether an injected observation was used or corrected in assistant messages.
 * Returns: "used" | "corrected" | "ignored"
 */
function detectUtilitySignal(obs, assistantTextLower) {
  const title = typeof obs.title === 'string' ? obs.title : '';
  const facts = Array.isArray(obs.facts) ? obs.facts : [];

  // Build search terms from title and facts (min length to avoid false positives)
  const searchTerms = [];
  if (title.length >= 10) {
    searchTerms.push(title.toLowerCase());
  }
  for (const fact of facts) {
    if (typeof fact === 'string' && fact.length >= 15) {
      searchTerms.push(fact.toLowerCase());
    }
  }

  if (searchTerms.length === 0) return 'ignored';

  // Check for verbatim citation (any search term appears in assistant text)
  let cited = false;
  for (const term of searchTerms) {
    if (assistantTextLower.includes(term)) {
      cited = true;
      break;
    }
  }

  // Fuzzy title matching: normalize and check >60% word overlap
  if (!cited && title.length >= 10) {
    const titleWords = title.toLowerCase().split(/\s+/).filter(w => w.length > 3);
    if (titleWords.length >= 3) {
      const assistantWords = new Set(assistantTextLower.split(/\s+/));
      let matchCount = 0;
      for (const w of titleWords) {
        if (assistantWords.has(w)) matchCount++;
      }
      if (matchCount / titleWords.length > 0.6) {
        cited = true;
      }
    }
  }

  // Concept keyword reuse: check if observation concepts appear in assistant tool calls
  if (!cited && Array.isArray(obs.concepts) && obs.concepts.length > 0) {
    let conceptMatches = 0;
    for (const concept of obs.concepts) {
      if (typeof concept === 'string' && concept.length >= 3 && assistantTextLower.includes(concept.toLowerCase())) {
        conceptMatches++;
      }
    }
    // At least 2 concept matches to signal relevance
    if (conceptMatches >= 2) {
      cited = true;
    }
  }

  if (!cited) return 'ignored';

  // Check for explicit correction patterns in a local window around each cited term.
  // Only use unambiguous correction markers to avoid false positives from normal prose.
  const correctionPatterns = [
    'actually,',
    "that's not",
    'that is not',
    'not quite right',
    'incorrect',
    "that's wrong",
    'that is wrong',
    'correction:',
    'was wrong',
    'but actually',
    'outdated',
  ];

  const WINDOW_SIZE = 200;
  for (const term of searchTerms) {
    let searchFrom = 0;
    // Find all occurrences of this term and check local window around each
    while (searchFrom < assistantTextLower.length) {
      const termIdx = assistantTextLower.indexOf(term, searchFrom);
      if (termIdx < 0) break;

      const windowStart = Math.max(0, termIdx - WINDOW_SIZE);
      const windowEnd = Math.min(assistantTextLower.length, termIdx + term.length + WINDOW_SIZE);
      const window = assistantTextLower.slice(windowStart, windowEnd);

      for (const pattern of correctionPatterns) {
        if (window.includes(pattern)) {
          return 'corrected';
        }
      }

      searchFrom = termIdx + term.length;
    }
  }

  return 'used';
}

async function handleStop(ctx, input) {
  console.error(`[stop] Raw input: ${String(ctx.RawInput || '')}`);

  let sessionResult;
  try {
    sessionResult = await lib.requestGet(
      `/api/sessions?claudeSessionId=${encodeURIComponent(ctx.SessionID)}`
    );
  } catch (error) {
    return '';
  }

  const sessionID = Number(sessionResult && sessionResult.id);
  if (!Number.isFinite(sessionID)) {
    return '';
  }

  const transcriptPath =
    typeof input.transcript_path === 'string'
      ? input.transcript_path
      : typeof input.TranscriptPath === 'string'
      ? input.TranscriptPath
      : '';

  const { lastUser, lastAssistant, messages } = await parseTranscript(transcriptPath);

  console.error(`[stop] Transcript path: ${transcriptPath}`);
  console.error(`[stop] Last user message length: ${String(lastUser).length}`);
  console.error(`[stop] Last assistant message length: ${String(lastAssistant).length}`);
  if (String(lastAssistant).length > 300) {
    console.error(`[stop] Last assistant preview: ${String(lastAssistant).slice(0, 300)}...`);
  }

  console.error(
    `[stop] Requesting summary for session ${sessionID} (transcript: ${
      transcriptPath !== ''
    })`
  );

  try {
    await lib.requestPost(`/sessions/${sessionID}/summarize`, {
      lastUserMessage: lastUser,
      lastAssistantMessage: lastAssistant,
    });
  } catch (error) {
    console.error(`[stop] Warning: summary request failed: ${error.message}`);
  }

  // Extract learnings from session transcript (LLM-based, may take seconds)
  if (messages.length > 0) {
    const project = typeof ctx.Project === 'string' ? ctx.Project : '';
    try {
      const learnResult = await lib.requestPost(
        `/api/sessions/${sessionID}/extract-learnings`,
        { messages, project },
        30000
      );
      const count = (learnResult && learnResult.count) || 0;
      const status = (learnResult && learnResult.status) || 'unknown';
      console.error(`[stop] extract-learnings: status=${status}, count=${count}`);
    } catch (error) {
      console.error(`[stop] Warning: extract-learnings failed: ${error.message}`);
    }
  }

  // Index session transcript for full-text search
  try {
    const expandedPath = expandTranscriptPath(transcriptPath);
    if (expandedPath) {
      const stat = fs.statSync(expandedPath);
      if (stat.size > 0 && stat.size <= 5 * 1024 * 1024) {
        const transcriptContent = fs.readFileSync(expandedPath, 'utf8');
        const wsId = lib.WorkstationID();
        const endpoint = `/api/sessions/index?workstation_id=${encodeURIComponent(wsId)}&session_id=${encodeURIComponent(ctx.SessionID)}`;
        const indexResult = await lib.requestUpload(endpoint, transcriptContent, 15000);
        console.error(`[stop] session indexed: status=${indexResult.status || 'unknown'}, exchanges=${indexResult.exchange_count || 0}`);
      } else if (stat.size > 5 * 1024 * 1024) {
        console.error(`[stop] session transcript too large (${stat.size} bytes), skipping indexing`);
      }
    }
  } catch (err) {
    console.error(`[stop] session index failed: ${err.message}`);
  }

  // Detect utility signals for injected observations
  try {
    const injectedResult = await lib.requestGet(
      `/api/sessions/${sessionID}/injected-observations`
    );
    const injectedObs = Array.isArray(injectedResult && injectedResult.observations)
      ? injectedResult.observations
      : [];

    if (injectedObs.length > 0 && messages.length > 0) {
      const assistantText = messages
        .filter((m) => m.role === 'assistant')
        .map((m) => m.text)
        .join('\n');
      const assistantTextLower = assistantText.toLowerCase();

      for (const obs of injectedObs) {
        if (!obs || typeof obs !== 'object' || typeof obs.id !== 'number') continue;

        const signal = detectUtilitySignal(obs, assistantTextLower);
        if (signal === 'ignored') continue;

        lib.requestPost(`/api/observations/${obs.id}/utility`, { signal }, 3000).catch((err) => {
          console.error(`[stop] utility signal failed for obs ${obs.id}: ${err.message}`);
        });
      }

      console.error(`[stop] Checked ${injectedObs.length} injected observations for utility signals`);
    }
  } catch (error) {
    console.error(`[stop] Warning: utility signal detection failed: ${error.message}`);
  }

  return '';
}

(async () => {
  await lib.RunHook('Stop', handleStop);
})();
