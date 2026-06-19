const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const test = require('node:test');

const hooksDir = __dirname;

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

    fs.mkdirSync(staleHooks, { recursive: true });
    fs.mkdirSync(staleCacheHooks, { recursive: true });
    fs.mkdirSync(latestHooks, { recursive: true });

    fs.writeFileSync(
      path.join(staleHooks, 'dispatcher.cjs'),
      "process.stdout.write(JSON.stringify({root:'stale',event:process.env.ENGRAM_HOOK_EVENT})+'\\n');\n",
      'utf8',
    );
    fs.writeFileSync(
      path.join(staleCacheHooks, 'dispatcher.cjs'),
      "process.stdout.write(JSON.stringify({root:'old-cache',event:process.env.ENGRAM_HOOK_EVENT})+'\\n');\n",
      'utf8',
    );
    fs.writeFileSync(
      path.join(latestHooks, 'dispatcher.cjs'),
      "process.stdout.write(JSON.stringify({root:'latest',event:process.env.ENGRAM_HOOK_EVENT})+'\\n');\n",
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
