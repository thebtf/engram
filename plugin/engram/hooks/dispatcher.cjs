#!/usr/bin/env node
'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const EVENT_HOOKS = {
  SessionStart: ['../scripts/ensure-binary.js', './session-start.js'],
  UserPromptSubmit: ['./user-prompt.js'],
  SubagentStop: ['./subagent-stop.js'],
  PreToolUse: ['./pre-tool-use.js'],
  PreCompact: ['./pre-compact.js'],
  Stop: ['./stop.js'],
  SessionEnd: ['./session-end.js'],
};

const LEGACY_HOOK_ENTRYPOINTS = {
  'session-start.js': 'SessionStart',
  'user-prompt.js': 'UserPromptSubmit',
  'subagent-stop.js': 'SubagentStop',
  'pre-tool-use.js': 'PreToolUse',
  'pre-compact.js': 'PreCompact',
  'stop.js': 'Stop',
  'session-end.js': 'SessionEnd',
};
const LEGACY_SHIM_MARKER = 'ENGRAM_LEGACY_HOOK_SHIM';
const LEGACY_COMPATIBILITY_VERSION_DIRS = [
  '6.25.1',
  '6.26.0',
  '6.27.1',
  '6.28.0',
];
const LEGACY_COMPATIBILITY_VERSION_ENV = 'ENGRAM_LEGACY_HOOK_COMPAT_VERSIONS';
const STABLE_BRIDGE_MARKER = 'ENGRAM_STABLE_HOOK_BRIDGE';
const STABLE_BRIDGE_RELATIVE_PATH = path.join('hooks', 'dispatcher.cjs');
const PLUGIN_DATA_ENV_NAMES = [
  'PLUGIN_DATA',
  'CLAUDE_PLUGIN_DATA',
  'CODEX_PLUGIN_DATA',
];
const KNOWN_CODEX_DATA_DIRS = [
  'engram-engram-marketplace',
  'engram-engram',
  'engram-marketplace-engram',
];

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

function readPluginVersion(pluginRoot) {
  for (const relPath of [
    path.join('.codex-plugin', 'plugin.json'),
    path.join('.claude-plugin', 'plugin.json'),
  ]) {
    try {
      const parsed = JSON.parse(fs.readFileSync(path.join(pluginRoot, relPath), 'utf8'));
      if (parsed && typeof parsed.version === 'string') {
        return parsed.version;
      }
    } catch {
      // Try the next manifest shape.
    }
  }
  return path.basename(path.resolve(pluginRoot));
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

function inferCodexCacheInfo(pluginRoot) {
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
    return null;
  }

  const cacheBase = path.join(parsed.root, ...parts.slice(0, cacheIndex + 3));
  const marketplace = parts[cacheIndex + 1];
  const pluginName = parts[cacheIndex + 2];
  if (!(
    (marketplace === 'engram-marketplace' && pluginName === 'engram') ||
    (marketplace === 'engram' && pluginName === 'engram')
  )) {
    return null;
  }
  if (readPluginName(resolved) !== 'engram') {
    return null;
  }
  return {
    cacheBase,
    codexHome: path.join(parsed.root, ...parts.slice(0, cacheIndex - 1)),
    marketplace,
    pluginName,
  };
}

function inferCodexCacheBaseDir(pluginRoot) {
  return inferCodexCacheInfo(pluginRoot)?.cacheBase || '';
}

function stableBridgePathForDataRoot(dataRoot) {
  return path.join(dataRoot, STABLE_BRIDGE_RELATIVE_PATH);
}

function codexDataRootCandidates(pluginRoot) {
  const roots = [];
  const info = inferCodexCacheInfo(pluginRoot);
  const inferredDataBase = info ? path.join(info.codexHome, 'plugins', 'data') : '';
  for (const envName of PLUGIN_DATA_ENV_NAMES) {
    const value = trimText(process.env[envName]);
    if (value) {
      const resolved = path.resolve(value);
      if (!inferredDataBase || isPathInside(inferredDataBase, resolved)) {
        roots.push(resolved);
      }
    }
  }

  if (info) {
    roots.push(path.join(
      info.codexHome,
      'plugins',
      'data',
      `${info.pluginName}-${info.marketplace}`,
    ));
  }

  return roots.filter((root, index, all) => all.indexOf(root) === index);
}

function isLikelyEngramCodexDataRoot(dataRoot) {
  const resolved = path.resolve(dataRoot);
  const parsed = path.parse(resolved);
  const relative = resolved.slice(parsed.root.length);
  const parts = relative.split(path.sep).filter(Boolean);
  const dataIndex = parts.lastIndexOf('data');
  if (dataIndex < 1 || parts[dataIndex - 1] !== 'plugins') {
    return false;
  }
  return KNOWN_CODEX_DATA_DIRS.includes(parts[dataIndex + 1]);
}

function buildStableBridge() {
  return `#!/usr/bin/env node
'use strict';
// ${STABLE_BRIDGE_MARKER}
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');

function passThrough() {
  process.stdout.write(JSON.stringify({ continue: true }) + '\\n');
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

function dispatcherFromRoot(root) {
  if (!root) return null;
  return [
    path.join(root, 'hooks', 'dispatcher.cjs'),
    path.join(root, 'dispatcher.cjs'),
  ].find((file) => file && fs.existsSync(file)) || null;
}

function readPluginName(root) {
  for (const relPath of [
    path.join('.codex-plugin', 'plugin.json'),
    path.join('.claude-plugin', 'plugin.json'),
  ]) {
    try {
      const parsed = JSON.parse(fs.readFileSync(path.join(root, relPath), 'utf8'));
      if (parsed && typeof parsed.name === 'string') return parsed.name;
    } catch (_) {}
  }
  return '';
}

function isUsableCacheRoot(root) {
  return readPluginName(root) === 'engram' && !!dispatcherFromRoot(root);
}

function unique(values) {
  return values.filter((value, index, all) => value && all.indexOf(value) === index);
}

function codexHomeFromSelf() {
  const resolved = path.resolve(__filename);
  const parsed = path.parse(resolved);
  const relative = resolved.slice(parsed.root.length);
  const parts = relative.split(path.sep).filter(Boolean);
  const dataIndex = parts.lastIndexOf('data');
  if (dataIndex < 1 || parts[dataIndex - 1] !== 'plugins') {
    return '';
  }
  return path.join(parsed.root, ...parts.slice(0, dataIndex - 1));
}

function codexHomes() {
  const explicitHome = process.env.CODEX_HOME ? path.resolve(process.env.CODEX_HOME) : '';
  const dataHome = codexHomeFromSelf();
  const homes = [explicitHome, dataHome];
  if (!explicitHome && !dataHome) {
    homes.push(path.join(os.homedir(), '.codex'));
  }
  return unique(homes.map((home) => home ? path.resolve(home) : ''));
}

function cacheRoots() {
  const bases = codexHomes().flatMap((home) => [
    path.join(home, 'plugins', 'cache', 'engram-marketplace', 'engram'),
    path.join(home, 'plugins', 'cache', 'engram', 'engram'),
  ]);
  return bases.flatMap((base) => {
    let entries = [];
    try {
      entries = fs.readdirSync(base, { withFileTypes: true });
    } catch {
      return [];
    }
    return entries
      .filter((entry) => entry.isDirectory())
      .map((entry) => path.join(base, entry.name))
      .filter(isUsableCacheRoot);
  }).sort((a, b) => compareVersionNames(path.basename(b), path.basename(a)));
}

function main() {
  const eventName = process.env.ENGRAM_HOOK_EVENT || process.argv[2] || '';
  if (eventName && !process.env.ENGRAM_HOOK_EVENT) {
    process.env.ENGRAM_HOOK_EVENT = eventName;
  }
  const self = path.resolve(__filename);
  const dispatcher = cacheRoots()
    .map(dispatcherFromRoot)
    .find((file) => file && path.resolve(file) !== self);
  if (!dispatcher) {
    passThrough();
    return;
  }
  try {
    const mod = require(dispatcher);
    if (mod && typeof mod.main === 'function') {
      mod.main();
      return;
    }
  } catch (error) {
    try {
      process.stderr.write('engram stable hook bridge: dispatcher failed open: ' + error.message + '\\n');
    } catch (_) {}
  }
  passThrough();
}

if (require.main === module) {
  main();
}

module.exports = { main };
`;
}

function shouldWriteStableBridge(bridgePath, source) {
  if (!fs.existsSync(bridgePath)) {
    return true;
  }
  try {
    return fs.readFileSync(bridgePath, 'utf8') !== source;
  } catch {
    return false;
  }
}

function installStableCodexHookBridge(pluginRoot) {
  const source = buildStableBridge();
  const touched = [];
  for (const dataRoot of codexDataRootCandidates(pluginRoot)) {
    if (!isLikelyEngramCodexDataRoot(dataRoot)) {
      continue;
    }
    const bridgePath = stableBridgePathForDataRoot(dataRoot);
    try {
      if (!shouldWriteStableBridge(bridgePath, source)) {
        continue;
      }
      fs.mkdirSync(path.dirname(bridgePath), { recursive: true });
      fs.writeFileSync(bridgePath, source, { encoding: 'utf8', mode: 0o755 });
      touched.push(bridgePath);
    } catch (error) {
      process.stderr.write(`engram dispatcher: stable hook bridge repair skipped for ${bridgePath}: ${error.message}\n`);
    }
  }
  return touched;
}

function firstStableBridgeDispatcher(pluginRoot) {
  for (const dataRoot of codexDataRootCandidates(pluginRoot)) {
    const bridgePath = stableBridgePathForDataRoot(dataRoot);
    if (isLikelyEngramCodexDataRoot(dataRoot) && fs.existsSync(bridgePath)) {
      return bridgePath;
    }
  }
  return '';
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

function normalizeVersionDir(name) {
  const text = String(name || '').trim();
  if (!isSemverLikeVersionDir(text)) {
    return '';
  }
  return text.replace(/^v/, '');
}

function previousPatchVersion(name) {
  const normalized = normalizeVersionDir(name);
  if (!normalized) {
    return '';
  }
  const match = normalized.match(/^(\d+)\.(\d+)\.(\d+)$/);
  if (!match) {
    return '';
  }
  const patch = Number(match[3]);
  if (!Number.isInteger(patch) || patch <= 0) {
    return '';
  }
  return `${match[1]}.${match[2]}.${patch - 1}`;
}

function configuredCompatibilityVersions() {
  const raw = process.env[LEGACY_COMPATIBILITY_VERSION_ENV] || '';
  return raw.split(/[,\s;]+/).map(normalizeVersionDir).filter(Boolean);
}

function legacyCompatibilityVersionDirs(pluginRoot) {
  const current = normalizeVersionDir(readPluginVersion(pluginRoot));
  if (!current) {
    return new Set();
  }

  const candidates = new Set([
    ...LEGACY_COMPATIBILITY_VERSION_DIRS,
    ...configuredCompatibilityVersions(),
  ].map(normalizeVersionDir).filter(Boolean));
  const previousPatch = previousPatchVersion(current);
  if (previousPatch) {
    candidates.add(previousPatch);
  }

  return new Set(
    [...candidates].filter((version) => compareVersionNames(version, current) < 0),
  );
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

function shouldWriteLegacyShim(hookPath, dispatcherPath) {
  if (!fs.existsSync(hookPath)) {
    return true;
  }
  try {
    const content = fs.readFileSync(hookPath, 'utf8');
    const expectedDispatcher = dispatcherPath ? JSON.stringify(path.resolve(dispatcherPath)) : '';
    return (
      (content.includes(LEGACY_SHIM_MARKER) && expectedDispatcher && !content.includes(expectedDispatcher)) ||
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
  installStableCodexHookBridge(pluginRoot);
  const stableDispatcher = firstStableBridgeDispatcher(pluginRoot) || currentDispatcher;
  const currentVersion = normalizeVersionDir(readPluginVersion(pluginRoot));
  const compatibilityVersions = legacyCompatibilityVersionDirs(pluginRoot);
  let realCacheBase = '';
  try {
    realCacheBase = fs.realpathSync.native(cacheBase);
  } catch {
    return [];
  }
  const versions = entries
    .filter((entry) => entry.isDirectory())
    .map((entry) => entry.name)
    .concat([...compatibilityVersions])
    .filter((version, index, all) => all.indexOf(version) === index)
    .sort(compareVersionNames);
  const touched = [];

  for (const version of versions) {
    const normalizedVersion = normalizeVersionDir(version);
    if (!normalizedVersion) {
      continue;
    }
    if (currentVersion && compareVersionNames(normalizedVersion, currentVersion) >= 0) {
      continue;
    }
    const versionRoot = path.join(cacheBase, version);
    if (path.resolve(versionRoot) === currentRoot) {
      continue;
    }
    const hooksDir = path.join(versionRoot, 'hooks');
    try {
      if (!fs.existsSync(versionRoot)) {
        if (!compatibilityVersions.has(normalizeVersionDir(version))) {
          continue;
        }
        fs.mkdirSync(versionRoot, { recursive: true });
      }
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
        if (!shouldWriteLegacyShim(hookPath, stableDispatcher)) {
          continue;
        }
        fs.writeFileSync(hookPath, buildLegacyShim(eventName, stableDispatcher), { encoding: 'utf8', mode: 0o755 });
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
  const bridgeRepaired = installStableCodexHookBridge(pluginRoot);
  if (bridgeRepaired.length) {
    process.stderr.write(`engram dispatcher: repaired ${bridgeRepaired.length} stable Codex hook bridge entrypoints\n`);
  }
  const repaired = repairLegacyCodexCacheHooks(pluginRoot);
  if (repaired.length) {
    process.stderr.write(`engram dispatcher: repaired ${repaired.length} legacy Codex hook entrypoints\n`);
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
  buildStableBridge,
  codexDataRootCandidates,
  firstStableBridgeDispatcher,
  inferCodexCacheBaseDir,
  inferCodexCacheInfo,
  installStableCodexHookBridge,
  isSemverLikeVersionDir,
  legacyCompatibilityVersionDirs,
  main,
  repairLegacyCodexCacheHooks,
  stableBridgePathForDataRoot,
};
