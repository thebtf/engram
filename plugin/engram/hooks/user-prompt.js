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

async function handleUserPrompt(ctx, input) {
  const project = typeof ctx.Project === 'string' ? ctx.Project : '';
  const sessionID = typeof ctx.SessionID === 'string' ? ctx.SessionID : '';
  if (!project) return '';

  const promptText = typeof input.user_message === 'string'
    ? input.user_message
    : typeof input.message === 'string'
      ? input.message
      : '';

  if (!promptText) return '';

  // Fire-and-forget: segment topic shift detection.
  lib.requestPost('/api/hooks/segment-check', {
    session_id: sessionID,
    project,
    prompt_text: promptText.slice(0, 2000),
  }, 8000).catch(() => {});

  // Fire-and-forget: correction detection.
  if (detectCorrection(promptText)) {
    lib.requestPost('/api/hooks/correction', {
      session_id: sessionID,
      project,
      user_message: promptText.slice(0, 5000),
    }, 8000).catch(() => {});
  }

  return '';
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
