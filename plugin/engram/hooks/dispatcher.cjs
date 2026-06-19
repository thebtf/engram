#!/usr/bin/env node
'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const EVENT_HOOKS = {
  SessionStart: ['../scripts/ensure-binary.js', './session-start.js'],
  UserPromptSubmit: ['./user-prompt.js'],
  PostToolUse: ['./post-tool-use.js'],
  SubagentStop: ['./subagent-stop.js'],
  PreToolUse: ['./pre-tool-use.js'],
  PreCompact: ['./pre-compact.js'],
  Stop: ['./stop.js'],
  SessionEnd: ['./session-end.js'],
};

function passThrough() {
  process.stdout.write(JSON.stringify({ continue: true }) + '\n');
}

function trimText(value) {
  return typeof value === 'string' ? value.trim() : '';
}

function resolveHookPath(relPath) {
  return path.resolve(__dirname, relPath);
}

function runHook(hookPath, stdinText, pluginRoot) {
  const env = {
    ...process.env,
    CODEX_PLUGIN_ROOT: pluginRoot,
    PLUGIN_ROOT: pluginRoot,
    CLAUDE_PLUGIN_ROOT: pluginRoot,
  };

  const child = spawnSync(process.execPath, [hookPath], {
    input: stdinText,
    encoding: 'utf8',
    env,
    maxBuffer: 1024 * 1024,
    windowsHide: true,
  });

  if (child.error) {
    process.stderr.write(`engram dispatcher: ${path.basename(hookPath)} failed to start: ${child.error.message}\n`);
    return { status: 1, stdout: '', stderr: '' };
  }

  return {
    status: child.status === null || child.status === undefined ? 1 : child.status,
    stdout: child.stdout || '',
    stderr: child.stderr || '',
  };
}

function main() {
  const eventName = process.env.ENGRAM_HOOK_EVENT || process.argv[2] || '';
  const hookList = Object.prototype.hasOwnProperty.call(EVENT_HOOKS, eventName)
    ? EVENT_HOOKS[eventName]
    : null;
  if (!hookList) {
    process.stderr.write(`engram dispatcher: unknown hook event ${JSON.stringify(eventName)}\n`);
    passThrough();
    return;
  }

  let stdinText = '';
  try {
    stdinText = fs.readFileSync(0, 'utf8');
  } catch (error) {
    process.stderr.write(`engram dispatcher: failed to read stdin: ${error.message}\n`);
  }
  const pluginRoot = path.resolve(__dirname, '..');
  let lastStdout = '';

  for (const relPath of hookList) {
    const hookPath = resolveHookPath(relPath);
    if (!fs.existsSync(hookPath)) {
      process.stderr.write(`engram dispatcher: hook file is missing: ${hookPath}\n`);
      continue;
    }

    const result = runHook(hookPath, stdinText, pluginRoot);
    if (trimText(result.stderr)) {
      process.stderr.write(result.stderr.endsWith('\n') ? result.stderr : result.stderr + '\n');
    }
    if (result.status === 0 && trimText(result.stdout)) {
      lastStdout = result.stdout;
    }
    if (result.status !== 0) {
      const diagnostic = trimText(result.stderr) ? '' : trimText(result.stdout);
      process.stderr.write(
        `engram dispatcher: ${path.basename(hookPath)} exited with code ${result.status}`
          + `${diagnostic ? `: ${diagnostic}` : ''}\n`,
      );
    }
  }

  if (trimText(lastStdout)) {
    process.stdout.write(lastStdout.endsWith('\n') ? lastStdout : lastStdout + '\n');
    return;
  }

  passThrough();
}

if (require.main === module) {
  main();
}

module.exports = {
  EVENT_HOOKS,
  main,
};
