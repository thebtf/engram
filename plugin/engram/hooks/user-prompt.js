#!/usr/bin/env node
'use strict';

const lib = require('./lib');

const correctionPatterns = [
  /\bactually\b/i,
  /\bthat's wrong\b/i,
  /\bthat is wrong\b/i,
  /\bno,\s/i,
  /\bnot\s+\w+,?\s+but\b/i,
  /\bi meant\b/i,
  /\bi mean\b/i,
  /\bна самом деле\b/i,
  /\bнет,\s/i,
  /\bне так\b/i,
  /\bя имел в виду\b/i,
  /\bя имела в виду\b/i,
  /\bнеправильно\b/i,
];

function detectCorrection(text) {
  if (!text || typeof text !== 'string') return false;
  return correctionPatterns.some(p => p.test(text));
}
const ambientTimeoutMs = 200;
const ambientHintLimit = 3;
const supportHookTimeoutMs = 8000;
const promptSliceLimit = 2000;
const correctionSliceLimit = 5000;
const ambientIntro = 'Memory suggests (you may ignore)';

function normalizeInlineWhitespace(value) {
  return String(value || '').trim().split(/\s+/).filter(Boolean).join(' ');
}

function truncateASCII(value, limit) {
  if (limit <= 0) return '';
  if (value.length <= limit) return value;
  if (limit <= 3) return value.slice(0, limit);
  return `${value.slice(0, limit - 3)}...`;
}

function sanitizeAmbientHint(hint) {
  if (!hint || typeof hint !== 'object' || Array.isArray(hint)) return null;
  const title = truncateASCII(normalizeInlineWhitespace(hint.title), 80);
  if (!title) return null;
  const reason = truncateASCII(normalizeInlineWhitespace(hint.reason), 120);
  const source = normalizeInlineWhitespace(hint.source);
  const score = Number(hint.score);
  return {
    title,
    reason,
    source,
    score: Number.isFinite(score) ? score : 0,
  };
}
function formatAmbientHintLine(hint) {
  let line = `- ${hint.title}`;
  if (hint.reason) {
    line += ` — ${hint.reason}`;
  }
  if (hint.source) {
    line += ` [${hint.source}`;
    if (hint.score !== 0) {
      line += ` ${hint.score.toFixed(2)}`;
    }
    line += ']';
  } else if (hint.score !== 0) {
    line += ` [score ${hint.score.toFixed(2)}]`;
  }
  return line;
}

function slicePromptText(promptText) {
  return promptText.slice(0, promptSliceLimit);
}

function buildAmbientRequest(project, sessionID, promptText) {
  return {
    session_id: sessionID,
    project,
    prompt_text: slicePromptText(promptText),
    limit: ambientHintLimit,
  };
}

function buildSegmentCheckRequest(project, sessionID, promptText) {
  return {
    session_id: sessionID,
    project,
    prompt_text: slicePromptText(promptText),
  };
}

function buildCorrectionRequest(project, sessionID, promptText) {
  return {
    session_id: sessionID,
    project,
    user_message: promptText.slice(0, correctionSliceLimit),
  };
}

function fireAndForgetSupportRequest(endpoint, body) {
  lib.requestPost(endpoint, body, supportHookTimeoutMs).catch(() => { });
}

function scheduleSupportHooks(project, sessionID, promptText) {
  fireAndForgetSupportRequest('/api/hooks/segment-check', buildSegmentCheckRequest(project, sessionID, promptText));
  if (detectCorrection(promptText)) {
    fireAndForgetSupportRequest('/api/hooks/correction', buildCorrectionRequest(project, sessionID, promptText));
  }
}

function formatAmbientAdditionalContext(payload) {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return '';
  if (typeof payload.additional_context === 'string' && payload.additional_context !== '') {
    return payload.additional_context;
  }
  if (!Array.isArray(payload.hints) || payload.hints.length === 0) return '';

  const hints = payload.hints
    .map((hint) => sanitizeAmbientHint(hint))
    .filter(Boolean)
    .slice(0, ambientHintLimit);
  if (hints.length === 0) return '';

  const lines = [ambientIntro];
  for (const hint of hints) {
    lines.push(formatAmbientHintLine(hint));
  }
  return lines.join('\n');
}

async function requestAmbientPayloadWithinBudget(project, sessionID, promptText) {
  let timeoutHandle;
  try {
    return await Promise.race([
      lib.requestPost(
        '/api/hooks/ambient-candidates',
        buildAmbientRequest(project, sessionID, promptText),
        ambientTimeoutMs,
      ),
      new Promise((resolve) => {
        timeoutHandle = setTimeout(() => resolve(null), ambientTimeoutMs);
      }),
    ]);
  } finally {
    clearTimeout(timeoutHandle);
  }
}

async function fetchAmbientAdditionalContext(project, sessionID, promptText) {
  try {
    const payload = await requestAmbientPayloadWithinBudget(project, sessionID, promptText);
    return formatAmbientAdditionalContext(payload);
  } catch (_) {
    return '';
  }
}

async function handleUserPrompt(ctx = {}, input = {}) {
  const project = typeof ctx.Project === 'string' ? ctx.Project : '';
  const sessionID = typeof ctx.SessionID === 'string' ? ctx.SessionID : '';
  if (!project || !sessionID) return '';

  const promptText = typeof input.user_message === 'string'
    ? input.user_message
    : typeof input.message === 'string'
      ? input.message
      : '';

  if (!promptText) return '';

  scheduleSupportHooks(project, sessionID, promptText);

  return fetchAmbientAdditionalContext(project, sessionID, promptText);
}

if (require.main === module) {
  (async () => {
    await lib.RunHook('UserPromptSubmit', handleUserPrompt);
  })();
}

module.exports = {
  handleUserPrompt,
  detectCorrection,
};
