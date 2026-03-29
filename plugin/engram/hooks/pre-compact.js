#!/usr/bin/env node
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const readline = require('readline');

const lib = require('./lib');

const MAX_MESSAGE_LENGTH = 5000;
const MAX_MESSAGES_PER_CHUNK = 50;

function truncateText(text, maxLen) {
  if (typeof text !== 'string') return '';
  return text.length <= maxLen ? text : text.slice(0, maxLen);
}

function extractTextContent(content) {
  if (typeof content === 'string') return content;
  if (!Array.isArray(content)) return '';
  let out = '';
  for (const part of content) {
    if (!part || typeof part !== 'object') continue;
    if (part.type === 'text' && typeof part.text === 'string') {
      out += part.text;
    }
  }
  return out;
}

function expandTranscriptPath(transcriptPath) {
  if (typeof transcriptPath !== 'string' || transcriptPath === '') return transcriptPath;
  if (!transcriptPath.startsWith('~')) return transcriptPath;
  const home = os.homedir();
  if (!home) return transcriptPath;
  if (transcriptPath === '~') return home;
  const sep = transcriptPath[1];
  if (sep === '/' || sep === '\\') return `${home}${transcriptPath.slice(1)}`;
  return transcriptPath;
}

/**
 * Parse transcript JSONL into an array of messages.
 * Reuses the same pattern as stop.js parseTranscript.
 */
function parseTranscript(transcriptPath) {
  const expandedPath = expandTranscriptPath(transcriptPath);
  if (!expandedPath) {
    return Promise.resolve([]);
  }

  return new Promise((resolve) => {
    const messages = [];

    const stream = fs.createReadStream(expandedPath, {
      encoding: 'utf8',
      highWaterMark: 1024 * 1024,
    });

    stream.on('error', (error) => {
      console.error(`[pre-compact] Failed to read transcript: ${error.message}`);
      resolve([]);
    });

    const rl = readline.createInterface({
      input: stream,
      crlfDelay: Infinity,
    });

    rl.on('line', (line) => {
      if (!line || !line.trim()) return;

      let item = null;
      try {
        item = JSON.parse(line);
      } catch {
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

      if (messageRole === 'user' || messageRole === 'assistant') {
        messages.push({
          role: messageRole,
          text: truncateText(messageText, MAX_MESSAGE_LENGTH),
        });
      }
    });

    rl.on('close', () => {
      resolve(messages);
    });
  });
}

/**
 * Derive transcript path from known Claude Code project layout.
 * Pattern: ~/.claude/projects/<project-hash>/<session-id>.jsonl
 */
function deriveTranscriptPath(ctx) {
  const sessionId = typeof ctx.SessionID === 'string' ? ctx.SessionID : '';
  if (!sessionId) return null;

  const home = os.homedir();
  if (!home) return null;

  // Claude Code stores transcripts at ~/.claude/projects/<project-dir-hash>/<session-id>.jsonl
  // The project dir hash varies, so we search for the session file
  const projectsDir = path.join(home, '.claude', 'projects');
  try {
    const dirs = fs.readdirSync(projectsDir);
    for (const dir of dirs) {
      const candidate = path.join(projectsDir, dir, `${sessionId}.jsonl`);
      if (fs.existsSync(candidate)) {
        return candidate;
      }
    }
  } catch {
    // projects dir doesn't exist or can't be read
  }
  return null;
}

async function handlePreCompact(ctx, input) {
  // Write discovery data for debugging (always, so we can verify fields)
  const inputKeys = input ? Object.keys(input) : [];
  const ctxKeys = ctx ? Object.keys(ctx) : [];

  const report = {
    timestamp: new Date().toISOString(),
    ctx_keys: ctxKeys,
    input_keys: inputKeys,
    has_transcript_path: !!(input.transcript_path || input.TranscriptPath),
    transcript_path: input.transcript_path || input.TranscriptPath || null,
  };

  // Write discovery report to project dir for agent inspection
  const projectDir = ctx.CWD || process.cwd();
  const logDir = path.join(projectDir, '.agent');
  try {
    fs.mkdirSync(logDir, { recursive: true });
    fs.writeFileSync(
      path.join(logDir, 'pre-compact-discovery.json'),
      JSON.stringify(report, null, 2)
    );
  } catch {
    // Non-critical: just log
  }

  console.error(`[pre-compact] ctx keys: ${ctxKeys.join(', ')}`);
  console.error(`[pre-compact] input keys: ${inputKeys.join(', ')}`);

  // Resolve transcript path: prefer input field, fallback to derived path
  let transcriptPath =
    typeof input.transcript_path === 'string'
      ? input.transcript_path
      : typeof input.TranscriptPath === 'string'
      ? input.TranscriptPath
      : '';

  if (!transcriptPath) {
    transcriptPath = deriveTranscriptPath(ctx) || '';
    if (transcriptPath) {
      console.error(`[pre-compact] transcript_path derived: ${transcriptPath}`);
    } else {
      console.error('[pre-compact] No transcript path available, skipping extraction');
      return '';
    }
  } else {
    console.error(`[pre-compact] transcript_path from input: ${transcriptPath}`);
  }

  // Parse the full transcript
  const messages = await parseTranscript(transcriptPath);
  if (messages.length === 0) {
    console.error('[pre-compact] No messages found in transcript, skipping');
    return '';
  }

  console.error(`[pre-compact] Parsed ${messages.length} messages from transcript`);

  // Fire-and-forget: send to backfill endpoint in chunks.
  // Do NOT await — compaction must proceed regardless of extraction success.
  const project = typeof ctx.Project === 'string' ? ctx.Project : '';
  const sessionId = typeof ctx.SessionID === 'string' ? ctx.SessionID : '';

  // Send in chunks of MAX_MESSAGES_PER_CHUNK
  const chunks = [];
  for (let i = 0; i < messages.length; i += MAX_MESSAGES_PER_CHUNK) {
    chunks.push(messages.slice(i, i + MAX_MESSAGES_PER_CHUNK));
  }

  console.error(`[pre-compact] Sending ${chunks.length} chunk(s) to backfill endpoint`);

  // Fire-and-forget: don't await, use catch to suppress errors
  for (let i = 0; i < chunks.length; i++) {
    const chunk = chunks[i];
    const payload = {
      session_id: sessionId,
      project: project,
      messages: chunk,
      source: 'pre-compact',
      chunk_index: i,
      total_chunks: chunks.length,
    };

    lib.requestPost('/api/backfill/session', payload, 5000).catch((err) => {
      console.error(`[pre-compact] Backfill chunk ${i} failed: ${err.message}`);
    });
  }

  // Return immediately — don't block compaction (Constitution Principle 3)
  return '';
}

(async () => {
  await lib.RunHook('PreCompact', handlePreCompact);
})();
