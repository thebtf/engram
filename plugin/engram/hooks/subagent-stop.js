#!/usr/bin/env node
'use strict';

const lib = require('./lib');

async function handleSubagentStop(ctx) {
  console.error(`[subagent-stop] Subagent completed in project ${ctx.Project}`);

  try {
    await lib.requestPost('/api/sessions/subagent-complete', {
      claudeSessionId: ctx.SessionID,
      project: ctx.Project,
    });
  } catch (error) {
    console.error(
      `[subagent-stop] Warning: failed to notify worker: ${error.message}`
    );
  }

  return '';
}

if (require.main === module) {
  (async () => {
    await lib.RunHook('SubagentStop', handleSubagentStop);
  })();
}
