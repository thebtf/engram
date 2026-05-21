#!/usr/bin/env node
'use strict';

const fs = require('fs');
const lib = require('./lib');

// Maximum bytes of agent output text to send in a single stop-hook POST.
// Keeps the request well within network/server limits and the 30s hook timeout.
const MAX_OUTPUT_BYTES = 512 * 1024; // 500 KB

/**
 * Find the conversation transcript file path.
 * Claude Code injects `transcript_path` into the hook input JSON.
 * Falls back to environment variables documented in the CC integration guide.
 *
 * @param {Object} input - Raw hook input parsed from stdin
 * @returns {string} Absolute path to the JSONL transcript, or '' if not found
 */
function findTranscriptPath(input) {
  // Primary: CC provides this field in the Stop hook input payload.
  if (input && typeof input.transcript_path === 'string' && input.transcript_path.trim() !== '') {
    return input.transcript_path.trim();
  }
  // Fallback: environment variables (set by some CC host configurations)
  const fromEnv =
    process.env.CLAUDE_CONVERSATION_PATH ||
    process.env.CLAUDE_TRANSCRIPT_PATH ||
    '';
  return fromEnv.trim();
}

/**
 * Extract concatenated assistant-role text from a JSONL transcript file.
 * Each line is a JSON object; we collect lines where role === 'assistant'
 * and concatenate their text content.
 *
 * @param {string} filePath - Absolute path to the JSONL file
 * @returns {string} Concatenated agent output text, truncated to MAX_OUTPUT_BYTES
 */
function extractAgentOutput(filePath) {
  let raw;
  try {
    raw = fs.readFileSync(filePath, 'utf8');
  } catch (err) {
    process.stderr.write(`engram stop hook: failed to read transcript: ${err.message}\n`);
    return '';
  }

  const lines = raw.split('\n');
  const parts = [];
  let totalBytes = 0;
  let truncated = false;

  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed) continue;

    let record;
    try {
      record = JSON.parse(trimmed);
    } catch {
      continue;
    }

    // Accept both role-based and type-based assistant message shapes.
    if (record.role !== 'assistant' && record.type !== 'assistant') continue;

    // Extract text from the content field (string or array-of-blocks).
    let text = '';
    if (typeof record.content === 'string') {
      text = record.content;
    } else if (Array.isArray(record.content)) {
      for (const block of record.content) {
        if (block && block.type === 'text' && typeof block.text === 'string') {
          text += block.text;
        }
      }
    } else if (typeof record.text === 'string') {
      // Flat-text variant some CC versions emit.
      text = record.text;
    }

    if (!text) continue;

    const chunkBytes = Buffer.byteLength(text, 'utf8');
    if (totalBytes + chunkBytes > MAX_OUTPUT_BYTES) {
      // Include as much of this chunk as fits, then stop.
      // Use Buffer to slice by byte count (not JS string code units).
      const remaining = MAX_OUTPUT_BYTES - totalBytes;
      if (remaining > 0) {
        const buf = Buffer.from(text, 'utf8').subarray(0, remaining);
        parts.push(buf.toString('utf8'));
      }
      truncated = true;
      break;
    }

    parts.push(text);
    totalBytes += chunkBytes;
  }

  if (parts.length === 0) return '';

  const joined = parts.join('\n');
  return truncated ? joined + '\n[truncated]' : joined;
}

/**
 * Stop hook handler.
 *
 * Reads the conversation transcript, extracts assistant output, and POSTs it
 * to the engram server as a session-end event. All failures are non-fatal;
 * the hook always returns '' so Claude Code continues normally.
 *
 * Note: the Stop hook is not in HOOKS_WITH_EVENT_NAME, so any return value
 * other than '' is silently dropped by lib.writeResponse. Side effects via
 * POST are the only real output channel.
 *
 * @param {Object} ctx  - Hook context from lib.RunHook (SessionID, Project, CWD, …)
 * @param {Object} input - Raw input payload from Claude Code
 * @returns {string} Always ''
 */
async function handleStop(ctx, input) {
  const sessionID = typeof ctx.SessionID === 'string' ? ctx.SessionID : '';
  const project = typeof ctx.Project === 'string' ? ctx.Project : '';

  if (!sessionID || !project) {
    return '';
  }

  // Remove the crash-safety pending marker now that the session ended cleanly.
  // (lib.createPendingMarker is called on session-start; we clear it here.)
  try {
    const fs2 = require('fs');
    const os = require('os');
    const path = require('path');
    const markerPath = path.join(os.tmpdir(), `.engram-pending-${sessionID}`);
    if (fs2.existsSync(markerPath)) {
      fs2.unlinkSync(markerPath);
    }
  } catch {
    // Non-critical — marker cleanup failure should not block the POST.
  }

  const transcriptPath = findTranscriptPath(input);
  if (!transcriptPath) {
    // No transcript path available — record a minimal stop event.
    try {
      await lib.requestPost('/api/hooks/session-end', {
        session_id: sessionID,
        project,
        agent_output_text: '',
      }, 60000);
    } catch (err) {
      process.stderr.write(`engram stop hook: POST failed (no transcript): ${err.message}\n`);
    }
    return '';
  }

  const agentOutput = extractAgentOutput(transcriptPath);

  try {
    await lib.requestPost('/api/hooks/session-end', {
      session_id: sessionID,
      project,
      agent_output_text: agentOutput,
    }, 60000);
  } catch (err) {
    process.stderr.write(`engram stop hook: POST failed: ${err.message}\n`);
  }

  return '';
}

if (require.main === module) {
  (async () => {
    await lib.RunHook('Stop', handleStop);
  })();
}

module.exports = {
  handleStop,
  findTranscriptPath,
  extractAgentOutput,
};
