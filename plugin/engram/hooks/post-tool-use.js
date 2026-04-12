#!/usr/bin/env node
'use strict';

const lib = require('./lib');

/**
 * Detect positive signals from Bash tool invocations.
 * Returns a delta object with counters to increment.
 * Only inspects tool metadata (name, args) — never transcript content (NFR-4).
 *
 * Note: Tool filtering is handled by the matcher in hooks.json
 * (Write|Edit|Bash|Agent|mcp__aimux). This hook is never called for
 * read-only tools (Read, Grep, Glob, etc.).
 */
function detectSignals(toolName, toolInput, exitCode) {
  const delta = {};

  if (toolName === 'Bash') {
    const command =
      typeof toolInput === 'string'
        ? toolInput
        : typeof toolInput === 'object' && toolInput !== null
        ? String(toolInput.command || toolInput.cmd || '')
        : '';

    if (command) {
      // Positive: git commit or PR creation/merge
      if (command.includes('git commit')) {
        delta.commits = 1;
      }
      if (command.includes('gh pr create')) {
        delta.prs = 1;
      }
      if (command.includes('gh pr merge')) {
        delta.prs = 1;
      }
    }
  }

  // Negative: non-zero exit code = error streak
  const code = typeof exitCode === 'number' ? exitCode : Number(exitCode);
  if (Number.isFinite(code) && code !== 0) {
    delta.errors = 1;
  }

  return delta;
}

async function handlePostToolUse(ctx, input) {
  const toolName =
    typeof input.tool_name === 'string'
      ? input.tool_name
      : typeof input.ToolName === 'string'
      ? input.ToolName
      : '';

  console.error(`[post-tool-use] ${toolName}`);

  // Accumulate session signals from tool metadata (not transcript content)
  const exitCode =
    input.tool_response !== undefined && input.tool_response !== null
      ? (input.tool_response.exit_code !== undefined
        ? input.tool_response.exit_code
        : input.tool_response.exitCode)
      : undefined;

  const delta = detectSignals(toolName, input.tool_input, exitCode);
  if (Object.keys(delta).length > 0 && ctx.SessionID) {
    lib.incrementSessionSignals(ctx.SessionID, delta);
  }

  try {
    await lib.requestPost('/api/sessions/observations', {
      claudeSessionId: ctx.SessionID,
      project: ctx.Project,
      tool_name: toolName,
      tool_input: input.tool_input,
      tool_response: input.tool_response,
      cwd: ctx.CWD,
    });
  } catch (error) {
    console.error(
      `[post-tool-use] Warning: failed to notify worker: ${error.message}`
    );
  }

  return '';
}

(async () => {
  await lib.RunHook('PostToolUse', handlePostToolUse);
})();
