const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const test = require('node:test');

const hooksDir = __dirname;
const pluginRoot = path.resolve(hooksDir, '..');
const repoRoot = path.resolve(pluginRoot, '..', '..');
const dispatcher = require('./dispatcher.cjs');

function runHook(scriptName, input) {
  const scriptPath = path.join(hooksDir, scriptName);
  const result = spawnSync(process.execPath, [scriptPath], {
    input,
    encoding: 'utf8',
    timeout: 2000,
    killSignal: 'SIGKILL',
    maxBuffer: 1024 * 1024,
    env: {
      ...process.env,
      ENGRAM_INTERNAL: '1',
    },
  });
  assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
  return result;
}

function readHooksJson() {
  return JSON.parse(fs.readFileSync(path.join(hooksDir, 'hooks.json'), 'utf8'));
}

function hookCommandFor(eventName) {
  const config = readHooksJson();
  const entries = config.hooks[eventName];
  assert.ok(Array.isArray(entries), `${eventName} hook entry should exist`);
  assert.equal(entries.length, 1, `${eventName} should have one hook entry`);
  assert.equal(entries[0].hooks.length, 1, `${eventName} should dispatch through one launcher`);
  return entries[0].hooks[0].command;
}

function readRepoFile(...segments) {
  return fs.readFileSync(path.join(repoRoot, ...segments), 'utf8');
}

function withProcessEnv(values, fn) {
  const previous = new Map(Object.keys(values).map((name) => [name, process.env[name]]));
  try {
    for (const [name, value] of Object.entries(values)) {
      if (value === undefined) {
        delete process.env[name];
      } else {
        process.env[name] = value;
      }
    }
    return fn();
  } finally {
    for (const [name, value] of previous.entries()) {
      if (value === undefined) {
        delete process.env[name];
      } else {
        process.env[name] = value;
      }
    }
  }
}

function withOnlyPluginDataEnv(dataRoot, fn) {
  return withProcessEnv({
    PLUGIN_DATA: dataRoot,
    CLAUDE_PLUGIN_DATA: undefined,
    CODEX_PLUGIN_DATA: undefined,
  }, fn);
}

function runLauncher(command, input, env) {
  const match = command.match(/^node -e "([\s\S]*)"$/);
  assert.ok(match, `expected node -e launcher, got ${command}`);
  return spawnSync(process.execPath, ['-e', match[1]], {
    input,
    encoding: 'utf8',
    timeout: 2000,
    killSignal: 'SIGKILL',
    maxBuffer: 1024 * 1024,
    env,
  });
}

test('pre-compact hook emits JSON continue envelope', () => {
  const result = runHook('pre-compact.js', JSON.stringify({
    session_id: 'test-session',
    cwd: process.cwd(),
  }));

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, '{"continue":true}\n');
});

test('stop hook emits JSON continue envelope', () => {
  const result = runHook('stop.js', JSON.stringify({
    session_id: 'test-session',
    cwd: process.cwd(),
  }));

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, '{"continue":true}\n');
});

test('statusline hook emits static status text through shared wrapper', () => {
  const result = runHook('statusline.js', JSON.stringify({
    session_id: 'test-session',
    cwd: process.cwd(),
  }));

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, '[engram] ○ v5 cleanup in progress\n');
});

test('hooks.json dispatches through root-resolving launchers, not raw CLAUDE_PLUGIN_ROOT paths', () => {
  const config = readHooksJson();
  for (const [eventName, entries] of Object.entries(config.hooks)) {
    for (const entry of entries) {
      for (const hook of entry.hooks) {
        assert.ok(
          !hook.command.startsWith('node ${CLAUDE_PLUGIN_ROOT}/'),
          `${eventName} must not hard-code CLAUDE_PLUGIN_ROOT in the hook command`,
        );
        assert.match(hook.command, /^node -e "/, `${eventName} should use a Codex-safe launcher`);
      }
    }
  }
});

test('launcher resolves latest Codex cache before stale CLAUDE_PLUGIN_ROOT', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-root-'));
  try {
    const codexHome = path.join(tempRoot, '.codex');
    const staleRoot = path.join(tempRoot, 'stale-plugin-root');
    const staleHooks = path.join(staleRoot, 'hooks');
    const staleCacheHooks = path.join(codexHome, 'plugins', 'cache', 'engram-marketplace', 'engram', '6.25.1', 'hooks');
    const latestHooks = path.join(codexHome, 'plugins', 'cache', 'engram', 'engram', '6.26.2', 'hooks');
    const staleCacheManifest = path.join(codexHome, 'plugins', 'cache', 'engram-marketplace', 'engram', '6.25.1', '.codex-plugin');
    const latestManifest = path.join(codexHome, 'plugins', 'cache', 'engram', 'engram', '6.26.2', '.codex-plugin');

    fs.mkdirSync(staleHooks, { recursive: true });
    fs.mkdirSync(staleCacheHooks, { recursive: true });
    fs.mkdirSync(latestHooks, { recursive: true });
    fs.mkdirSync(staleCacheManifest, { recursive: true });
    fs.mkdirSync(latestManifest, { recursive: true });
    fs.writeFileSync(path.join(staleCacheManifest, 'plugin.json'), JSON.stringify({ name: 'engram' }), 'utf8');
    fs.writeFileSync(path.join(latestManifest, 'plugin.json'), JSON.stringify({ name: 'engram' }), 'utf8');

    fs.writeFileSync(
      path.join(staleHooks, 'dispatcher.cjs'),
      "module.exports.main=function(){process.stdout.write(JSON.stringify({root:'stale',event:process.env.ENGRAM_HOOK_EVENT})+'\\n')};\n",
      'utf8',
    );
    fs.writeFileSync(
      path.join(staleCacheHooks, 'dispatcher.cjs'),
      "module.exports.main=function(){process.stdout.write(JSON.stringify({root:'old-cache',event:process.env.ENGRAM_HOOK_EVENT})+'\\n')};\n",
      'utf8',
    );
    fs.writeFileSync(
      path.join(latestHooks, 'dispatcher.cjs'),
      "module.exports.main=function(){process.stdout.write(JSON.stringify({root:'latest',event:process.env.ENGRAM_HOOK_EVENT})+'\\n')};\n",
      'utf8',
    );

    const result = runLauncher(hookCommandFor('PreCompact'), JSON.stringify({ session_id: 's1' }), {
      ...process.env,
      CODEX_HOME: codexHome,
      CODEX_PLUGIN_ROOT: '',
      CODEX_PLUGIN_DIR: '',
      PLUGIN_ROOT: '',
      CLAUDE_PLUGIN_ROOT: staleRoot,
      CLAUDE_PLUGIN_DIR: '',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(JSON.parse(result.stdout), { root: 'latest', event: 'PreCompact' });
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('launcher prefers stable Codex data bridge before versioned cache roots', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-launcher-data-'));
  try {
    const codexHome = path.join(tempRoot, '.codex');
    const dataHooks = path.join(codexHome, 'plugins', 'data', 'engram-engram-marketplace', 'hooks');
    const latestHooks = path.join(codexHome, 'plugins', 'cache', 'engram-marketplace', 'engram', '6.28.2', 'hooks');

    fs.mkdirSync(dataHooks, { recursive: true });
    fs.mkdirSync(latestHooks, { recursive: true });
    fs.writeFileSync(
      path.join(dataHooks, 'dispatcher.cjs'),
      "module.exports.main=function(){process.stdout.write(JSON.stringify({root:'data',event:process.env.ENGRAM_HOOK_EVENT})+'\\n')};\n",
      'utf8',
    );
    fs.writeFileSync(
      path.join(latestHooks, 'dispatcher.cjs'),
      "module.exports.main=function(){process.stdout.write(JSON.stringify({root:'cache',event:process.env.ENGRAM_HOOK_EVENT})+'\\n')};\n",
      'utf8',
    );

    const result = runLauncher(hookCommandFor('PreCompact'), JSON.stringify({ session_id: 's1' }), {
      ...process.env,
      CODEX_HOME: codexHome,
      CODEX_PLUGIN_ROOT: '',
      CODEX_PLUGIN_DIR: '',
      PLUGIN_ROOT: '',
      PLUGIN_DATA: path.join(codexHome, 'plugins', 'data', 'engram-engram-marketplace'),
      CLAUDE_PLUGIN_DATA: '',
      CODEX_PLUGIN_DATA: '',
      CLAUDE_PLUGIN_ROOT: '',
      CLAUDE_PLUGIN_DIR: '',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(JSON.parse(result.stdout), { root: 'data', event: 'PreCompact' });
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('launcher executes exported dispatcher main when resolved from plugin root', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-dispatcher-'));
  try {
    const pluginRoot = path.resolve(hooksDir, '..');
    const staleRoot = path.join(tempRoot, 'stale-plugin-root');
    fs.mkdirSync(staleRoot, { recursive: true });

    const result = runLauncher(hookCommandFor('PreCompact'), JSON.stringify({
      session_id: 's1',
      cwd: process.cwd(),
    }), {
      ...process.env,
      CODEX_HOME: path.join(tempRoot, '.codex'),
      CODEX_PLUGIN_ROOT: pluginRoot,
      CODEX_PLUGIN_DIR: '',
      PLUGIN_ROOT: '',
      PLUGIN_DATA: '',
      CLAUDE_PLUGIN_DATA: '',
      CODEX_PLUGIN_DATA: '',
      CLAUDE_PLUGIN_ROOT: staleRoot,
      CLAUDE_PLUGIN_DIR: '',
      ENGRAM_INTERNAL: '1',
    });

    assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, '{"continue":true}\n');
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('launcher fails open when resolved Codex cache dispatcher has missing child hooks', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-incomplete-cache-'));
  try {
    const codexHome = path.join(tempRoot, '.codex');
    const latestHooks = path.join(codexHome, 'plugins', 'cache', 'engram-marketplace', 'engram', '6.26.4', 'hooks');
    const latestManifest = path.join(codexHome, 'plugins', 'cache', 'engram-marketplace', 'engram', '6.26.4', '.codex-plugin');
    fs.mkdirSync(latestHooks, { recursive: true });
    fs.mkdirSync(latestManifest, { recursive: true });
    fs.writeFileSync(path.join(latestManifest, 'plugin.json'), JSON.stringify({ name: 'engram' }), 'utf8');
    fs.copyFileSync(path.join(hooksDir, 'dispatcher.cjs'), path.join(latestHooks, 'dispatcher.cjs'));

    const result = runLauncher(hookCommandFor('PreCompact'), JSON.stringify({ session_id: 's1' }), {
      ...process.env,
      CODEX_HOME: codexHome,
      CODEX_PLUGIN_ROOT: '',
      CODEX_PLUGIN_DIR: '',
      PLUGIN_ROOT: '',
      PLUGIN_DATA: '',
      CLAUDE_PLUGIN_DATA: '',
      CODEX_PLUGIN_DATA: '',
      CLAUDE_PLUGIN_ROOT: '',
      CLAUDE_PLUGIN_DIR: '',
    });

    assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stderr, /hook file is missing/);
    assert.equal(result.stdout, '{"continue":true}\n');
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('dispatcher installs stable Codex hook bridge in plugin data root', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-stable-bridge-'));
  try {
    const codexHome = path.join(tempRoot, '.codex');
    const latestRoot = path.join(codexHome, 'plugins', 'cache', 'engram-marketplace', 'engram', '6.28.2');
    const latestManifest = path.join(latestRoot, '.codex-plugin');
    const dataRoot = path.join(codexHome, 'plugins', 'data', 'engram-engram-marketplace');

    fs.mkdirSync(latestManifest, { recursive: true });
    fs.writeFileSync(path.join(latestManifest, 'plugin.json'), JSON.stringify({
      name: 'engram',
      version: '6.28.2',
    }), 'utf8');

    withOnlyPluginDataEnv(dataRoot, () => {
      const bridgePath = dispatcher.stableBridgePathForDataRoot(dataRoot);
      const repaired = dispatcher.installStableCodexHookBridge(latestRoot);

      assert.deepEqual(repaired, [bridgePath]);
      assert.ok(fs.existsSync(bridgePath));
      assert.match(fs.readFileSync(bridgePath, 'utf8'), /ENGRAM_STABLE_HOOK_BRIDGE/);
      assert.deepEqual(dispatcher.installStableCodexHookBridge(latestRoot), []);
    });
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('legacy Codex shims point to stable bridge when plugin data is available', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-stable-legacy-'));
  try {
    const codexHome = path.join(tempRoot, '.codex');
    const cacheBase = path.join(codexHome, 'plugins', 'cache', 'engram-marketplace', 'engram');
    const oldRoot = path.join(cacheBase, '6.27.1');
    const latestRoot = path.join(cacheBase, '6.28.2');
    const oldHooks = path.join(oldRoot, 'hooks');
    const latestHooks = path.join(latestRoot, 'hooks');
    const latestManifest = path.join(latestRoot, '.codex-plugin');
    const dataRoot = path.join(codexHome, 'plugins', 'data', 'engram-engram-marketplace');
    const bridgePath = dispatcher.stableBridgePathForDataRoot(dataRoot);
    const latestDispatcher = path.join(latestHooks, 'dispatcher.cjs');

    fs.mkdirSync(oldHooks, { recursive: true });
    fs.mkdirSync(latestHooks, { recursive: true });
    fs.mkdirSync(latestManifest, { recursive: true });
    fs.writeFileSync(path.join(latestManifest, 'plugin.json'), JSON.stringify({
      name: 'engram',
      version: '6.28.2',
    }), 'utf8');
    fs.writeFileSync(
      latestDispatcher,
      "module.exports.main=function(){process.stdout.write(JSON.stringify({root:'latest',event:process.env.ENGRAM_HOOK_EVENT})+'\\n')};\n",
      'utf8',
    );

    withOnlyPluginDataEnv(dataRoot, () => {
      const repaired = dispatcher.repairLegacyCodexCacheHooks(latestRoot);
      const legacyPreCompact = path.join(oldHooks, 'pre-compact.js');
      const shim = fs.readFileSync(legacyPreCompact, 'utf8');

      assert.ok(repaired.includes(legacyPreCompact));
      assert.match(fs.readFileSync(bridgePath, 'utf8'), /ENGRAM_STABLE_HOOK_BRIDGE/);
      assert.match(shim, /ENGRAM_LEGACY_HOOK_SHIM/);
      assert.ok(shim.includes(JSON.stringify(path.resolve(bridgePath))));
      assert.ok(!shim.includes(JSON.stringify(path.resolve(latestDispatcher))));

      const result = spawnSync(process.execPath, [legacyPreCompact], {
        input: JSON.stringify({ session_id: 's1' }),
        encoding: 'utf8',
        timeout: 2000,
        killSignal: 'SIGKILL',
        maxBuffer: 1024 * 1024,
        env: {
          ...process.env,
          CODEX_HOME: '',
          CODEX_PLUGIN_ROOT: '',
          CODEX_PLUGIN_DIR: '',
          PLUGIN_ROOT: '',
          PLUGIN_DATA: '',
          CLAUDE_PLUGIN_DATA: '',
          CODEX_PLUGIN_DATA: '',
          CLAUDE_PLUGIN_ROOT: '',
          CLAUDE_PLUGIN_DIR: '',
        },
      });

      assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
      assert.equal(result.status, 0, result.stderr);
      assert.deepEqual(JSON.parse(result.stdout), { root: 'latest', event: 'PreCompact' });
    });
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('stable Codex bridge survives cache version prune and dispatches newest slot', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-stable-prune-'));
  try {
    const codexHome = path.join(tempRoot, '.codex');
    const cacheBase = path.join(codexHome, 'plugins', 'cache', 'engram-marketplace', 'engram');
    const oldRoot = path.join(cacheBase, '6.28.2');
    const newerRoot = path.join(cacheBase, '6.28.3');
    const oldHooks = path.join(oldRoot, 'hooks');
    const oldManifest = path.join(oldRoot, '.codex-plugin');
    const newerHooks = path.join(newerRoot, 'hooks');
    const newerManifest = path.join(newerRoot, '.codex-plugin');
    const dataRoot = path.join(codexHome, 'plugins', 'data', 'engram-engram-marketplace');
    const bridgePath = dispatcher.stableBridgePathForDataRoot(dataRoot);

    fs.mkdirSync(oldHooks, { recursive: true });
    fs.mkdirSync(oldManifest, { recursive: true });
    fs.writeFileSync(path.join(oldManifest, 'plugin.json'), JSON.stringify({
      name: 'engram',
      version: '6.28.2',
    }), 'utf8');

    withOnlyPluginDataEnv(dataRoot, () => {
      assert.deepEqual(dispatcher.installStableCodexHookBridge(oldRoot), [bridgePath]);
    });

    fs.rmSync(oldRoot, { recursive: true, force: true });
    fs.mkdirSync(newerHooks, { recursive: true });
    fs.mkdirSync(newerManifest, { recursive: true });
    fs.writeFileSync(path.join(newerManifest, 'plugin.json'), JSON.stringify({
      name: 'engram',
      version: '6.28.3',
    }), 'utf8');
    fs.writeFileSync(
      path.join(newerHooks, 'dispatcher.cjs'),
      "module.exports.main=function(){process.stdout.write(JSON.stringify({root:'newer',event:process.env.ENGRAM_HOOK_EVENT})+'\\n')};\n",
      'utf8',
    );

    const result = spawnSync(process.execPath, [bridgePath, 'PreCompact'], {
      input: JSON.stringify({ session_id: 's1' }),
      encoding: 'utf8',
      timeout: 2000,
      killSignal: 'SIGKILL',
      maxBuffer: 1024 * 1024,
      env: {
        ...process.env,
        CODEX_HOME: '',
        ENGRAM_HOOK_EVENT: '',
        PLUGIN_DATA: '',
        CLAUDE_PLUGIN_DATA: '',
        CODEX_PLUGIN_DATA: '',
      },
    });

    assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(JSON.parse(result.stdout), { root: 'newer', event: 'PreCompact' });
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('dispatcher repairs missing legacy Codex cache hook entrypoints with latest-dispatcher shims', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-legacy-cache-'));
  try {
    const codexHome = path.join(tempRoot, '.codex');
    const cacheBase = path.join(codexHome, 'plugins', 'cache', 'engram-marketplace', 'engram');
    const oldRoot = path.join(cacheBase, '6.25.1');
    const latestRoot = path.join(cacheBase, '6.27.1');
    const bogusRoot = path.join(cacheBase, '999.0.0');
    const oldHooks = path.join(oldRoot, 'hooks');
    const latestHooks = path.join(latestRoot, 'hooks');
    const latestManifest = path.join(latestRoot, '.codex-plugin');
    const bogusHooks = path.join(bogusRoot, 'hooks');

    fs.mkdirSync(oldHooks, { recursive: true });
    fs.mkdirSync(latestHooks, { recursive: true });
    fs.mkdirSync(latestManifest, { recursive: true });
    fs.mkdirSync(bogusHooks, { recursive: true });
    fs.writeFileSync(path.join(latestManifest, 'plugin.json'), JSON.stringify({ name: 'engram' }), 'utf8');
    fs.writeFileSync(
      path.join(latestHooks, 'dispatcher.cjs'),
      "module.exports.main=function(){process.stdout.write(JSON.stringify({root:'latest',event:process.env.ENGRAM_HOOK_EVENT})+'\\n')};\n",
      'utf8',
    );
    fs.writeFileSync(
      path.join(bogusHooks, 'dispatcher.cjs'),
      "module.exports.main=function(){process.stdout.write('bogus\\n')};\n",
      'utf8',
    );
    fs.writeFileSync(
      path.join(oldHooks, 'pre-compact.js'),
      "function cacheRoots(){const home=process.env.CODEX_HOME;return []} process.stdout.write('old-bad\\n');\n",
      'utf8',
    );

    const repaired = dispatcher.repairLegacyCodexCacheHooks(latestRoot);
    const legacyPreCompact = path.join(oldRoot, 'hooks', 'pre-compact.js');
    const bogusPreCompact = path.join(bogusRoot, 'hooks', 'pre-compact.js');

    assert.ok(repaired.includes(legacyPreCompact));
    assert.ok(fs.existsSync(legacyPreCompact));
    assert.equal(fs.existsSync(bogusPreCompact), false, 'newer cache slots must not be repaired as legacy');

    const result = spawnSync(process.execPath, [legacyPreCompact], {
      input: JSON.stringify({ session_id: 's1' }),
      encoding: 'utf8',
      timeout: 2000,
      killSignal: 'SIGKILL',
      maxBuffer: 1024 * 1024,
      env: {
        ...process.env,
        CODEX_HOME: codexHome,
        CODEX_PLUGIN_ROOT: '',
        CODEX_PLUGIN_DIR: '',
        PLUGIN_ROOT: '',
        CLAUDE_PLUGIN_ROOT: '',
        CLAUDE_PLUGIN_DIR: '',
      },
    });

    assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(JSON.parse(result.stdout), { root: 'latest', event: 'PreCompact' });

    assert.deepEqual(dispatcher.repairLegacyCodexCacheHooks(latestRoot), []);
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('dispatcher repair omits the retired tool-result entrypoint', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-deleted-cache-'));
  try {
    const codexHome = path.join(tempRoot, '.codex');
    const cacheBase = path.join(codexHome, 'plugins', 'cache', 'engram-marketplace', 'engram');
    const deletedRoot = path.join(cacheBase, '6.27.1');
    const latestRoot = path.join(cacheBase, '6.28.0');
    const latestHooks = path.join(latestRoot, 'hooks');
    const latestManifest = path.join(latestRoot, '.codex-plugin');
    const retiredEvent = ['Post', 'Tool', 'Use'].join('');
    const retiredEntry = ['post', 'tool', 'use.js'].join('-');

    fs.mkdirSync(latestHooks, { recursive: true });
    fs.mkdirSync(latestManifest, { recursive: true });
    fs.writeFileSync(path.join(latestManifest, 'plugin.json'), JSON.stringify({
      name: 'engram',
      version: '6.28.0',
    }), 'utf8');
    fs.writeFileSync(
      path.join(latestHooks, 'dispatcher.cjs'),
      "module.exports.main=function(){process.stdout.write(JSON.stringify({root:'latest',event:process.env.ENGRAM_HOOK_EVENT})+'\\n')};\n",
      'utf8',
    );

    const repaired = dispatcher.repairLegacyCodexCacheHooks(latestRoot);
    const retiredHook = path.join(deletedRoot, 'hooks', retiredEntry);

    assert.ok(repaired.length > 0, 'repair must preserve supported legacy hook entrypoints');
    assert.equal(Object.hasOwn(dispatcher.EVENT_HOOKS, retiredEvent), false);
    assert.equal(repaired.includes(retiredHook), false);
    assert.equal(fs.existsSync(retiredHook), false);
    const dispatch = spawnSync(process.execPath, [path.join(hooksDir, 'dispatcher.cjs'), retiredEvent], {
      input: JSON.stringify({ session_id: 's1' }),
      encoding: 'utf8',
      timeout: 2000,
      env: { ...process.env, CODEX_HOME: '', PLUGIN_DATA: '', CLAUDE_PLUGIN_DATA: '', CODEX_PLUGIN_DATA: '' },
    });
    assert.equal(dispatch.status, 0, dispatch.stderr);
    assert.match(dispatch.stderr, /unknown hook event/);
    assert.equal(dispatch.stdout, '{"continue":true}\n');
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('dispatcher main repairs deleted cache slots before non-session hooks', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-nonsession-repair-'));
  try {
    const codexHome = path.join(tempRoot, '.codex');
    const cacheBase = path.join(codexHome, 'plugins', 'cache', 'engram-marketplace', 'engram');
    const deletedRoot = path.join(cacheBase, '6.27.1');
    const latestRoot = path.join(cacheBase, '6.28.0');
    const latestHooks = path.join(latestRoot, 'hooks');
    const latestManifest = path.join(latestRoot, '.codex-plugin');
    const latestDispatcher = path.join(latestHooks, 'dispatcher.cjs');

    fs.mkdirSync(latestHooks, { recursive: true });
    fs.mkdirSync(latestManifest, { recursive: true });
    fs.writeFileSync(path.join(latestManifest, 'plugin.json'), JSON.stringify({
      name: 'engram',
      version: '6.28.0',
    }), 'utf8');
    fs.copyFileSync(path.join(hooksDir, 'dispatcher.cjs'), latestDispatcher);

    const result = spawnSync(process.execPath, [latestDispatcher, 'PreCompact'], {
      input: JSON.stringify({ session_id: 's1' }),
      encoding: 'utf8',
      timeout: 2000,
      killSignal: 'SIGKILL',
      maxBuffer: 1024 * 1024,
      env: {
        ...process.env,
        CODEX_HOME: codexHome,
        CODEX_PLUGIN_ROOT: '',
        CODEX_PLUGIN_DIR: '',
        PLUGIN_ROOT: '',
        CLAUDE_PLUGIN_ROOT: '',
        CLAUDE_PLUGIN_DIR: '',
      },
    });

    assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stderr, /repaired \d+ legacy Codex hook entrypoints/);
    assert.equal(result.stdout, '{"continue":true}\n');
    assert.equal(fs.existsSync(path.join(deletedRoot, 'hooks', ['post', 'tool', 'use.js'].join('-'))), false);
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('legacy shim fails open when its pinned dispatcher throws', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-shim-failopen-'));
  try {
    const badDispatcher = path.join(tempRoot, 'dispatcher.cjs');
    const legacyHook = path.join(tempRoot, 'pre-compact.js');

    fs.writeFileSync(badDispatcher, "throw new Error('broken dispatcher');\n", 'utf8');
    fs.writeFileSync(legacyHook, dispatcher.buildLegacyShim('PreCompact', badDispatcher), 'utf8');

    const result = spawnSync(process.execPath, [legacyHook], {
      input: JSON.stringify({ session_id: 's1' }),
      encoding: 'utf8',
      timeout: 2000,
      killSignal: 'SIGKILL',
      maxBuffer: 1024 * 1024,
      env: { ...process.env },
    });

    assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stderr, /dispatcher failed open: broken dispatcher/);
    assert.equal(result.stdout, '{"continue":true}\n');
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('dispatcher fails open when a child hook file is missing', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-missing-child-'));
  try {
    const tempHooks = path.join(tempRoot, 'plugin', 'hooks');
    fs.mkdirSync(tempHooks, { recursive: true });
    fs.copyFileSync(path.join(hooksDir, 'dispatcher.cjs'), path.join(tempHooks, 'dispatcher.cjs'));

    const result = spawnSync(process.execPath, [path.join(tempHooks, 'dispatcher.cjs'), 'PreCompact'], {
      input: JSON.stringify({ session_id: 's1' }),
      encoding: 'utf8',
      timeout: 2000,
      killSignal: 'SIGKILL',
      maxBuffer: 1024 * 1024,
      env: { ...process.env },
    });

    assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stderr, /hook file is missing/);
    assert.equal(result.stdout, '{"continue":true}\n');
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('dispatcher fails open when a child hook exits nonzero', () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-hook-nonzero-child-'));
  try {
    const tempHooks = path.join(tempRoot, 'plugin', 'hooks');
    fs.mkdirSync(tempHooks, { recursive: true });
    fs.copyFileSync(path.join(hooksDir, 'dispatcher.cjs'), path.join(tempHooks, 'dispatcher.cjs'));
    fs.writeFileSync(
      path.join(tempHooks, 'pre-compact.js'),
      "process.stdout.write('{\"continue\":false,\"stopReason\":\"partial\"}\\n'); process.stderr.write('simulated child failure\\n'); process.exit(7);\n",
      'utf8',
    );

    const result = spawnSync(process.execPath, [path.join(tempHooks, 'dispatcher.cjs'), 'PreCompact'], {
      input: JSON.stringify({ session_id: 's1' }),
      encoding: 'utf8',
      timeout: 2000,
      killSignal: 'SIGKILL',
      maxBuffer: 1024 * 1024,
      env: { ...process.env },
    });

    assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stderr, /simulated child failure/);
    assert.match(result.stderr, /pre-compact\.js exited with code 7/);
    assert.equal(result.stdout, '{"continue":true}\n');
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test('release archives include scripts required by hooks and MCP wrapper', () => {
  const goreleaser = readRepoFile('.goreleaser.yaml');
  const codexMcp = JSON.parse(readRepoFile('plugin', 'engram', '.mcp.json'));
  const claudeMcp = JSON.parse(readRepoFile('plugin', 'engram', 'claude', '.mcp.json'));
  const dispatcher = fs.readFileSync(path.join(hooksDir, 'dispatcher.cjs'), 'utf8');

  assert.match(goreleaser, /src:\s+plugin\/engram\/scripts\/\*\.js/);
  assert.match(goreleaser, /src:\s+plugin\/engram\/hooks\/\*\.cjs/);
  assert.match(goreleaser, /dst:\s+scripts/);
  assert.match(goreleaser, /src:\s+plugin\/engram\/bootstrap-targets\.json/);
  assert.match(goreleaser, /id:\s+client-raw[\s\S]*?ids:[\s\S]*?engram-client[\s\S]*?formats:[\s\S]*?binary/);
  assert.deepEqual(codexMcp.mcpServers.engram.args, ['./scripts/run-engram.js']);
  assert.deepEqual(claudeMcp.mcpServers.engram.args, ['${CLAUDE_PLUGIN_ROOT}/scripts/run-engram.js']);
  assert.match(dispatcher, /\.\.\/scripts\/ensure-binary\.js/);
});

test('release installers copy plugin scripts from archives', () => {
  const installSh = readRepoFile('scripts', 'install.sh');
  const installPs1 = readRepoFile('scripts', 'install.ps1');
  const registerPluginSh = readRepoFile('scripts', 'register-plugin.sh');

  assert.match(installSh, /mkdir -p "\$INSTALL_DIR\/hooks" "\$INSTALL_DIR\/scripts"/);
  assert.match(installSh, /cp "\$tmp_dir\/scripts\/"\*\.js "\$INSTALL_DIR\/scripts\/"/);
  assert.match(installSh, /cp "\$tmp_dir\/hooks\/"\*\.cjs "\$INSTALL_DIR\/hooks\/"/);
  assert.match(installSh, /Preserving old cache versions for running-session hook compatibility/);
  assert.doesNotMatch(installSh, /-exec rm -rf \{\}/);
  assert.match(installPs1, /New-Item -ItemType Directory -Path "\$InstallDir\\scripts"/);
  assert.match(installPs1, /Copy-Item "\$TempDir\\scripts\\\*\.js" "\$InstallDir\\scripts\\"/);
  assert.match(installPs1, /Copy-Item "\$TempDir\\hooks\\\*\.cjs" "\$InstallDir\\hooks\\"/);
  assert.match(installSh, /bootstrap-targets\.json/);
  assert.match(installPs1, /bootstrap-targets\.json/);
  assert.match(installPs1, /Preserving old cache versions for running-session hook compatibility/);
  assert.doesNotMatch(installPs1, /Get-ChildItem -Path \$CacheBase[\s\S]*Remove-Item -Recurse -Force/);
  assert.match(registerPluginSh, /Preserving old cache versions for running-session hook compatibility/);
  assert.doesNotMatch(registerPluginSh, /-exec rm -rf \{\}/);
});
