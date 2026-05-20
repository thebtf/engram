#!/usr/bin/env node
'use strict';

const lib = require('./lib');

/**
 * Extract a topic string from the hook input for context query.
 * Claude Code may supply recent conversation summary or the last user message
 * in the input payload. We use the best available signal.
 *
 * @param {Object} input - Raw hook input payload
 * @returns {string} Topic string, or '' if none found
 */
function extractTopic(input) {
  if (!input || typeof input !== 'object') return '';

  // Claude Code pre-compact payload may include a summary or trigger reason.
  if (typeof input.summary === 'string' && input.summary.trim() !== '') {
    return input.summary.trim().slice(0, 200);
  }

  // Some CC versions include the last human message in the trigger context.
  if (typeof input.last_human_message === 'string' && input.last_human_message.trim() !== '') {
    return input.last_human_message.trim().slice(0, 200);
  }

  // Conversation title or description field.
  if (typeof input.conversation_title === 'string' && input.conversation_title.trim() !== '') {
    return input.conversation_title.trim().slice(0, 200);
  }

  return '';
}

/**
 * Format inject API response as an <engram-reinjection> block.
 * Mirrors the style used in session-start.js for consistency.
 *
 * @param {Object} payload - Response from /api/context/inject
 * @returns {string} Formatted block, or '' if payload is empty
 */
function formatReinjectionBlock(payload) {
  if (!payload || typeof payload !== 'object') return '';

  const memories = Array.isArray(payload.memories) ? payload.memories : [];
  const rules = Array.isArray(payload.rules) ? payload.rules : [];

  if (memories.length === 0 && rules.length === 0) return '';

  let block = '<engram-reinjection>\n';
  block += '# Pre-Compact Memory Re-injection\n';
  block += 'Engram re-injected relevant context before context compaction.\n\n';

  if (rules.length > 0) {
    block += '## Active Behavioral Rules\n';
    for (const rule of rules) {
      if (!rule || typeof rule !== 'object') continue;
      const content =
        typeof rule.content === 'string' ? rule.content.trim() :
        typeof rule.narrative === 'string' ? rule.narrative.trim() : '';
      if (content) block += `- ${content}\n`;
    }
    block += '\n';
  }

  if (memories.length > 0) {
    block += '## Relevant Memories\n';
    for (const memory of memories) {
      if (!memory || typeof memory !== 'object') continue;
      const content = typeof memory.content === 'string' ? memory.content.trim() : '';
      if (content) block += `- ${content}\n`;
    }
    block += '\n';
  }

  block += '</engram-reinjection>';
  return block;
}

/**
 * Pre-compact hook handler.
 *
 * Before Claude Code compacts the context window, this hook:
 *   1. Extracts a topic from the input (best-effort)
 *   2. Requests relevant memory re-injection from the engram server
 *   3. Formats the response as an <engram-reinjection> block
 *
 * Note: the PreCompact hook is not in HOOKS_WITH_EVENT_NAME, so
 * lib.writeResponse will silently drop any additionalContext string.
 * The formatted block is returned for testing purposes and for future
 * CC versions that may support PreCompact additionalContext.
 *
 * @param {Object} ctx   - Hook context from lib.RunHook
 * @param {Object} input - Raw input payload from Claude Code
 * @returns {string} Always '' (CC drops PreCompact context; see comment above)
 */
async function handlePreCompact(ctx, input) {
  const project = typeof ctx.Project === 'string' ? ctx.Project : '';

  if (!project) {
    return '';
  }

  const topic = extractTopic(input);

  const endpoint = topic
    ? `/api/context/inject?project=${encodeURIComponent(project)}&query=${encodeURIComponent(topic)}`
    : `/api/context/inject?project=${encodeURIComponent(project)}`;

  try {
    const payload = await lib.requestGet(endpoint, 8000);
    // Format the block (used in tests; CC drops it at runtime — see note above).
    formatReinjectionBlock(payload);
  } catch (err) {
    process.stderr.write(`engram pre-compact hook: inject fetch failed: ${err.message}\n`);
  }

  // Always return '' — PreCompact additionalContext is not delivered by CC.
  return '';
}

if (require.main === module) {
  (async () => {
    await lib.RunHook('PreCompact', handlePreCompact);
  })();
}

module.exports = {
  handlePreCompact,
  extractTopic,
  formatReinjectionBlock,
};
