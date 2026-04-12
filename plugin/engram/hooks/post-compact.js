#!/usr/bin/env node
'use strict';

const lib = require('./lib');

/**
 * PostCompact: signal that compaction occurred so the next UserPromptSubmit
 * can re-inject full behavioral rules.
 *
 * PostCompact is an observability-only hook — Claude Code's hookSpecificOutput
 * schema is a discriminated union that only accepts PreToolUse, UserPromptSubmit,
 * and PostToolUse.  Any additionalContext returned here would be rejected with
 * "Invalid input".  Instead, we set a session signal that user-prompt.js
 * picks up on the very next turn.
 */
async function handlePostCompact(ctx) {
  if (!ctx.SessionID) {
    console.error('[post-compact] No session ID, skipping compaction signal');
    return '';
  }

  lib.incrementSessionSignals(ctx.SessionID, { compacted: 1 });
  console.error('[post-compact] Compaction signal set — full behavioral rules will be re-injected on next prompt');
  return '';
}

(async () => {
  await lib.RunHook('PostCompact', handlePostCompact);
})();
