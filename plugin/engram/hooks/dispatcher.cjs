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

const LEGACY_HOOK_ENTRYPOINTS = {
  'session-start.js': 'SessionStart',
  'user-prompt.js': 'UserPromptSubmit',
  'post-tool-use.js': 'PostToolUse',
  'subagent-stop.js': 'SubagentStop',
  'pre-tool-use.js': 'PreToolUse',
  'pre-compact.js': 'PreCompact',
  'stop.js': 'Stop',
  'session-end.js': 'SessionEnd',
};
const LEGACY_SHIM_MARKER = 'ENGRAM_LEGACY_HOOK_SHIM';

function passThrough() {
  process.stdout.write(JSON.stringify({ continue: true }) + '\n');
}

function trimText(value) {
  return typeof value === 'string' ? value.trim() : '';
}

function resolveHookPath(relPath) {
  return path.resolve(__dirname, relPath);
}

function isPathInside(parent, child) {
  const relative = path.relative(parent, child);
  return relative === '' || (!!relative && !relative.startsWith('..') && !path.isAbsolute(relative));
}

function readPluginName(pluginRoot) {
  for (const relPath of [
    path.join('.codex-plugin', 'plugin.json'),
    path.join('.claude-plugin', 'plugin.json'),
  ]) {
    try {
      const parsed = JSON.parse(fs.readFileSync(path.join(pluginRoot, relPath), 'utf8'));
      if (parsed && typeof parsed.name === 'string') {
        return parsed.name;
      }
    } catch {
      // Try the next manifest shape.
    }
  }
  return '';
}

function existingManifestNames(pluginRoot) {
  const names = [];
  for (const relPath of [
    path.join('.codex-plugin', 'plugin.json'),
    path.join('.claude-plugin', 'plugin.json'),
  ]) {
    try {
      const manifestPath = path.join(pluginRoot, relPath);
      if (!fs.existsSync(manifestPath)) {
        continue;
      }
      const parsed = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
      names.push(parsed && typeof parsed.name === 'string' ? parsed.name : '');
    } catch {
      names.push('');
    }
  }
  return names;
}

function inferCodexCacheBaseDir(pluginRoot) {
  const resolved = path.resolve(pluginRoot);
  const parsed = path.parse(resolved);
  const relative = resolved.slice(parsed.root.length);
  const parts = relative.split(path.sep).filter(Boolean);
  const cacheIndex = parts.lastIndexOf('cache');

  if (
    cacheIndex < 1 ||
    parts[cacheIndex - 1] !== 'plugins' ||
    parts.length < cacheIndex + 4
  ) {
    return '';
  }

  const cacheBase = path.join(parsed.root, ...parts.slice(0, cacheIndex + 3));
  const marketplace = parts[cacheIndex + 1];
  const pluginName = parts[cacheIndex + 2];
  if (!(
    (marketplace === 'engram-marketplace' && pluginName === 'engram') ||
    (marketplace === 'engram' && pluginName === 'engram')
  )) {
    return '';
  }
  if (readPluginName(resolved) !== 'engram') {
    return '';
  }
  return cacheBase;
}

function versionParts(value) {
  return String(value).split(/[^0-9]+/).filter(Boolean).map(Number);
}

function compareVersionNames(a, b) {
  const aa = versionParts(a);
  const bb = versionParts(b);
  const max = Math.max(aa.length, bb.length);
  for (let i = 0; i < max; i++) {
    const diff = (aa[i] || 0) - (bb[i] || 0);
    if (diff) return diff;
  }
  return String(a).localeCompare(String(b));
}

function isSemverLikeVersionDir(name) {
  return /^v?\d+\.\d+\.\d+(?:[-+][A-Za-z0-9._-]+)?$/.test(String(name));
}

function isRepairableLegacyRoot(versionRoot) {
  const manifestNames = existingManifestNames(versionRoot);
  if (manifestNames.length > 0) {
    return manifestNames.every((name) => name === 'engram');
  }

  let entries = [];
  try {
    entries = fs.readdirSync(versionRoot, { withFileTypes: true });
  } catch {
    return false;
  }

  // Empty or hooks-only directories are common after interrupted cache cleanup.
  return entries.every((entry) => entry.name === 'hooks');
}

function buildLegacyShim(eventName, dispatcherPath) {
  const source = `
'use strict';
// ${LEGACY_SHIM_MARKER}
const fs = require('node:fs');
const path = require('node:path');
process.env.ENGRAM_HOOK_EVENT = process.env.ENGRAM_HOOK_EVENT || ${JSON.stringify(eventName)};
const dispatcher=${JSON.stringify(dispatcherPath ? path.resolve(dispatcherPath) : '')};
try{
  if(dispatcher&&path.resolve(dispatcher)!==__filename&&fs.existsSync(dispatcher)){
    const mod=require(dispatcher);
    if(mod&&typeof mod.main==='function'){mod.main();return}
  }
}catch(error){
  try{process.stderr.write('engram legacy hook shim: dispatcher failed open: '+error.message+'\\n')}catch(_){}
}
process.stdout.write(JSON.stringify({continue:true})+'\\n');
`.trimStart();

  return `#!/usr/bin/env node\n${source}`;
}

function shouldWriteLegacyShim(hookPath) {
  if (!fs.existsSync(hookPath)) {
    return true;
  }
  try {
    const content = fs.readFileSync(hookPath, 'utf8');
    return (
      content.includes(LEGACY_SHIM_MARKER) ||
      content.includes('function cacheRoots(){const home=process.env.CODEX_HOME')
    );
  } catch {
    return false;
  }
}

function repairLegacyCodexCacheHooks(pluginRoot) {
  const cacheBase = inferCodexCacheBaseDir(pluginRoot);
  if (!cacheBase) {
    return [];
  }

  let entries = [];
  try {
    entries = fs.readdirSync(cacheBase, { withFileTypes: true });
  } catch {
    return [];
  }

  const currentRoot = path.resolve(pluginRoot);
  const currentDispatcher = path.join(currentRoot, 'hooks', 'dispatcher.cjs');
  let realCacheBase = '';
  try {
    realCacheBase = fs.realpathSync.native(cacheBase);
  } catch {
    return [];
  }
  const versions = entries
    .filter((entry) => entry.isDirectory())
    .map((entry) => entry.name)
    .sort(compareVersionNames);
  const touched = [];

  for (const version of versions) {
    if (!isSemverLikeVersionDir(version)) {
      continue;
    }
    const versionRoot = path.join(cacheBase, version);
    if (path.resolve(versionRoot) === currentRoot) {
      continue;
    }
    const hooksDir = path.join(versionRoot, 'hooks');
    try {
      const stat = fs.lstatSync(versionRoot);
      if (stat.isSymbolicLink()) {
        continue;
      }
      const realVersionRoot = fs.realpathSync.native(versionRoot);
      if (!isPathInside(realCacheBase, realVersionRoot)) {
        continue;
      }
      if (!isRepairableLegacyRoot(versionRoot)) {
        continue;
      }
      if (fs.existsSync(hooksDir) && fs.lstatSync(hooksDir).isSymbolicLink()) {
        continue;
      }
      fs.mkdirSync(hooksDir, { recursive: true });
      for (const [fileName, eventName] of Object.entries(LEGACY_HOOK_ENTRYPOINTS)) {
        const hookPath = path.join(hooksDir, fileName);
        if (!shouldWriteLegacyShim(hookPath)) {
          continue;
        }
        fs.writeFileSync(hookPath, buildLegacyShim(eventName, currentDispatcher), { encoding: 'utf8', mode: 0o755 });
        touched.push(hookPath);
      }
    } catch (error) {
      process.stderr.write(`engram dispatcher: legacy hook cache repair skipped for ${versionRoot}: ${error.message}\n`);
    }
  }

  return touched;
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
  if (eventName === 'SessionStart') {
    const repaired = repairLegacyCodexCacheHooks(pluginRoot);
    if (repaired.length) {
      process.stderr.write(`engram dispatcher: repaired ${repaired.length} legacy Codex hook entrypoints\n`);
    }
  }
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
  buildLegacyShim,
  inferCodexCacheBaseDir,
  isSemverLikeVersionDir,
  main,
  repairLegacyCodexCacheHooks,
};
