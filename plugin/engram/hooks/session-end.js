#!/usr/bin/env node
'use strict';

// HTTP client timeout: 1200ms.
// Claude Code SessionEnd hook budget: 1500ms (env: CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS).
// The 300ms headroom covers: node process startup (~80ms), JSON parse, HTTP connection establishment,
// and early-return logging. If this drifts close to 1500ms in practice, the hook will be killed
// mid-flight and the propagate-outcome call will not complete.

const lib = require('./lib');

async function handleSessionEnd(ctx, input) {
  const sessionID = ctx.SessionID || input.session_id || input.SessionID || '';
  if (!sessionID) {
    console.error('[session-end] No session_id in hook input — skipping');
    return '';
  }

  try {
    await lib.requestPost(
      `/api/sessions/${encodeURIComponent(sessionID)}/propagate-outcome`,
      {},
      1200
    );
    console.error(`[session-end] propagate-outcome fired for session=${sessionID}`);
  } catch (err) {
    console.error(`[session-end] propagate-outcome failed: ${err.message}`);
  }

  return '';
}

(async () => {
  await lib.RunHook('SessionEnd', handleSessionEnd);
})();
