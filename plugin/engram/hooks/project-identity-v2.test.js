const assert = require('node:assert/strict');
const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const lib = require('./lib');

const vectorsPath = path.resolve(__dirname, '../../../.agent/specs/security-project-identity/evidence/project-identity-v2-vectors.json');
const vectors = JSON.parse(fs.readFileSync(vectorsPath, 'utf8'));

const claudeIdentityChild = String.raw`
const fs = require('node:fs');
const path = require('node:path');
const [modulePath, workspace, barrier, id] = process.argv.slice(1);
fs.writeFileSync(path.join(barrier, 'ready-' + id), '');
const wait = new Int32Array(new SharedArrayBuffer(4));
while (!fs.existsSync(path.join(barrier, 'go'))) Atomics.wait(wait, 0, 0, 5);
try {
  const lib = require(modulePath);
  process.stdout.write(JSON.stringify({ ok: true, value: lib.resolveProjectIdentityV2(workspace) }));
} catch (error) {
  process.stdout.write(JSON.stringify({ ok: false, error: String(error && error.message || error) }));
}
`;

async function resolveClaudeIdentityInChildProcesses(workspace, count) {
  const barrier = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-barrier-'));
  const modulePath = require.resolve('./lib');
  const children = [];
  try {
    for (let id = 0; id < count; id++) {
      const child = spawn(process.execPath, ['-e', claudeIdentityChild, modulePath, workspace, barrier, String(id)], {
        stdio: ['ignore', 'pipe', 'pipe'],
        windowsHide: true,
      });
      let stdout = '';
      let stderr = '';
      const result = new Promise((resolve, reject) => {
        child.stdout.setEncoding('utf8');
        child.stderr.setEncoding('utf8');
        child.stdout.on('data', (chunk) => { stdout += chunk; });
        child.stderr.on('data', (chunk) => { stderr += chunk; });
        child.once('error', reject);
        child.once('close', (code) => {
          if (code !== 0) {
            reject(new Error(`identity child ${id} exited ${code}: ${stderr}`));
            return;
          }
          try { resolve(JSON.parse(stdout)); } catch (error) {
            reject(new Error(`identity child ${id} returned invalid JSON: ${stdout}\n${stderr}`, { cause: error }));
          }
        });
      });
      children.push({ child, result });
    }

    const deadline = Date.now() + 15000;
    while (fs.readdirSync(barrier).filter((name) => name.startsWith('ready-')).length !== count) {
      if (Date.now() >= deadline) throw new Error('identity children did not reach the concurrency barrier');
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    fs.writeFileSync(path.join(barrier, 'go'), '');
    return await Promise.all(children.map(({ result }) => result));
  } finally {
    for (const { child } of children) {
      if (child.exitCode === null) child.kill();
    }
    fs.rmSync(barrier, { recursive: true, force: true });
  }
}

function assertCompleteAnchorPublication(workspace, expectedAnchor) {
  const anchorPath = path.join(workspace, '.engram-project-v2.json');
  const parsed = JSON.parse(fs.readFileSync(anchorPath, 'utf8'));
  assert.deepEqual(Object.keys(parsed).sort(), ['anchor', 'shared', 'version']);
  assert.equal(parsed.version, 2);
  assert.equal(parsed.anchor, expectedAnchor);
  assert.equal(parsed.shared, false);
  if (process.platform !== 'win32') {
    assert.equal(fs.statSync(anchorPath).mode & 0o777, 0o600);
  }
  assert.deepEqual(
    fs.readdirSync(workspace).filter((name) => name.startsWith('.engram-project-v2.json.tmp-')),
    [],
  );
}

test('project identity v2 consumes the repository-wide vectors', () => {
  assert.equal(vectors.identity_version, lib.PROJECT_IDENTITY_VERSION_V2);
  for (const vector of vectors.vectors) {
    const identity = lib.buildProjectIdentityV2(vector);
    assert.equal(identity.version, 2, vector.name);
    assert.equal(identity.legacy_project_id, vector.legacy_project_id, vector.name);
    assert.equal(identity.git_remote, vector.git_remote, vector.name);
    assert.equal(identity.relative_path, vector.relative_path, vector.name);
    assert.equal(identity.non_git_anchor, vector.non_git_anchor, vector.name);
    assert.equal(identity.anchor_shared, vector.anchor_shared, vector.name);
    assert.doesNotThrow(() => lib.validateProjectIdentityV2(identity), vector.name);
  }
});

test('non-git v2 anchor is strict, high-entropy, stable, and child-process concurrent-safe', async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));

  const firstRun = await resolveClaudeIdentityInChildProcesses(dir, 16);
  assert.ok(firstRun.every((result) => result.ok), JSON.stringify(firstRun));
  const identities = firstRun.map((result) => result.value);
  const anchors = new Set(identities.map((identity) => identity.non_git_anchor));
  assert.equal(anchors.size, 1);
  assert.match(identities[0].non_git_anchor, /^[0-9a-f]{32}$/);
  assert.equal(identities[0].anchor_shared, false);
  assertCompleteAnchorPublication(dir, identities[0].non_git_anchor);

  const anchorPath = path.join(dir, '.engram-project-v2.json');
  const originalBytes = fs.readFileSync(anchorPath);
  const secondRun = await resolveClaudeIdentityInChildProcesses(dir, 8);
  assert.ok(secondRun.every((result) => result.ok), JSON.stringify(secondRun));
  assert.ok(secondRun.every((result) => result.value.non_git_anchor === identities[0].non_git_anchor));
  assert.deepEqual(fs.readFileSync(anchorPath), originalBytes, 'an existing anchor must remain byte-identical');
  assertCompleteAnchorPublication(dir, identities[0].non_git_anchor);

  const otherDir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-other-'));
  t.after(() => fs.rmSync(otherDir, { recursive: true, force: true }));
  const other = lib.resolveProjectIdentityV2(otherDir);
  assert.notEqual(other.non_git_anchor, identities[0].non_git_anchor,
    'independent projects must not receive the same anchor');

  const bad = { ...identities[0], non_git_anchor: 'path-derived' };
  assert.throws(() => lib.validateProjectIdentityV2(bad), /PROJECT_IDENTITY_INVALID/);
});

test('v2 metadata and anchor files reject non-normalized or unknown input without replacement', async (t) => {
  const malformed = lib.buildProjectIdentityV2({
    legacy_project_id: ' selector ',
    display_name: 'fixture',
    git_remote: 'https://example.invalid/acme/mono.git',
    relative_path: 'packages/core/',
  });
  assert.throws(() => lib.validateProjectIdentityV2(malformed), /PROJECT_IDENTITY_INVALID/);

  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-extra-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const anchorPath = path.join(dir, '.engram-project-v2.json');
  const malformedBytes = Buffer.from(JSON.stringify({
    version: 2,
    anchor: '00112233445566778899aabbccddeeff',
    shared: false,
    unexpected: true,
  }));
  fs.writeFileSync(anchorPath, malformedBytes, { mode: 0o600 });
  assert.throws(() => lib.resolveProjectIdentityV2(dir), /PROJECT_IDENTITY_INVALID/);
  const concurrent = await resolveClaudeIdentityInChildProcesses(dir, 8);
  assert.ok(concurrent.every((result) => !result.ok && /PROJECT_IDENTITY_INVALID/.test(result.error)), JSON.stringify(concurrent));
  assert.deepEqual(fs.readFileSync(anchorPath), malformedBytes, 'malformed existing bytes must never be regenerated');
  assert.deepEqual(fs.readdirSync(dir).filter((name) => name.startsWith('.engram-project-v2.json.tmp-')), []);
});

test('git execution failures do not mint a non-git identity anchor', (t) => {
  const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-git-failure-'));
  const emptyPath = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-empty-path-'));
  t.after(() => fs.rmSync(workspace, { recursive: true, force: true }));
  t.after(() => fs.rmSync(emptyPath, { recursive: true, force: true }));
  const childScript = `
    try {
      const lib = require(process.argv[1]);
      lib.resolveProjectIdentityV2(process.argv[2]);
      process.stdout.write('resolved');
    } catch (error) {
      process.stdout.write(String(error && error.message || error));
    }
  `;
  const env = { ...process.env, PATH: emptyPath, Path: emptyPath };
  const result = spawnSync(process.execPath, ['-e', childScript, require.resolve('./lib'), workspace], {
    encoding: 'utf8',
    timeout: 2000,
    windowsHide: true,
    env,
  });
  assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /PROJECT_IDENTITY_UNAVAILABLE/);
  assert.equal(fs.existsSync(path.join(workspace, '.engram-project-v2.json')), false);
});

test('git repositories without origin use the explicit anchor fallback', (t) => {
  const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-no-origin-'));
  t.after(() => fs.rmSync(workspace, { recursive: true, force: true }));
  const initialized = spawnSync('git', ['init', workspace], { encoding: 'utf8', windowsHide: true });
  assert.equal(initialized.status, 0, initialized.stderr);

  const identity = lib.resolveProjectIdentityV2(workspace);
  assert.equal(identity.git_remote, '');
  assert.match(identity.non_git_anchor, /^[0-9a-f]{32}$/);
});

test('hook identity resolution failure returns pass-through without running the handler', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-hook-failure-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  fs.writeFileSync(path.join(dir, '.engram-project-v2.json'), '{"version":2,"anchor":"invalid"}');
  const childScript = `
    const lib = require(process.argv[1]);
    lib.RunHook('SessionStart', async () => {
      process.stderr.write('HANDLER_RAN');
      return 'must-not-run';
    }).catch((error) => {
      process.stderr.write(String(error && error.stack || error));
      process.exitCode = 1;
    });
  `;
  const result = spawnSync(process.execPath, ['-e', childScript, require.resolve('./lib')], {
    input: JSON.stringify({ session_id: 'identity-failure', cwd: dir }),
    encoding: 'utf8',
    timeout: 2000,
    windowsHide: true,
    env: {
      ...process.env,
      ENGRAM_INTERNAL: '0',
      ENGRAM_QUIET: '0',
      ENGRAM_URL: 'http://127.0.0.1:9',
      ENGRAM_TOKEN: 'test-token',
    },
  });
  assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trim(), '{"continue":true}');
  assert.doesNotMatch(result.stderr, /HANDLER_RAN/);
});

test('shared invalid vectors and wrong-type anchor sharing are rejected exactly', () => {
  for (const vector of vectors.invalid_vectors) {
    if (vector.invalid_target !== 'identity') continue;
    const identity = lib.buildProjectIdentityV2(vector);
    assert.throws(() => lib.validateProjectIdentityV2(identity), /PROJECT_IDENTITY_INVALID/, vector.name);
  }
  assert.throws(() => lib.buildProjectIdentityV2({
    legacy_project_id: 'workspace',
    display_name: 'workspace',
    non_git_anchor: '00112233445566778899aabbccddeeff',
    anchor_shared: 'false',
  }), /PROJECT_IDENTITY_INVALID/);
});

test('capture hook registration transport failure still runs local handler', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-registration-failure-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const childScript = `
    const lib = require(process.argv[1]);
    lib.RunHook('UserPromptSubmit', async (context) => {
      if (!context.ProjectIdentityRegistrationOffline) throw new Error('OFFLINE_FLAG_MISSING');
      process.stderr.write('HANDLER_RAN');
      return '';
    }).catch((error) => {
      process.stderr.write(String(error && error.stack || error));
      process.exitCode = 1;
    });
  `;
  const result = spawnSync(process.execPath, ['-e', childScript, require.resolve('./lib')], {
    input: JSON.stringify({ session_id: 'registration-failure', cwd: dir }),
    encoding: 'utf8',
    timeout: 2000,
    windowsHide: true,
    env: {
      ...process.env,
      ENGRAM_INTERNAL: '0',
      ENGRAM_QUIET: '0',
      ENGRAM_URL: 'http://127.0.0.1:9',
      ENGRAM_TOKEN: 'test-token',
    },
  });
  assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trim(), '{"continue":true}');
  assert.match(result.stderr, /HANDLER_RAN/);
  assert.match(result.stderr, /continuing local capture\/cleanup/);
});

test('non-SessionStart injection hook registration transport failure stays fail closed', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-injection-registration-failure-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const childScript = `
    const lib = require(process.argv[1]);
    lib.RunHook('PreToolUse', async () => {
      process.stderr.write('HANDLER_RAN');
      return 'must-not-run';
    });
  `;
  const result = spawnSync(process.execPath, ['-e', childScript, require.resolve('./lib')], {
    input: JSON.stringify({ session_id: 'injection-registration-failure', cwd: dir }),
    encoding: 'utf8',
    timeout: 2000,
    windowsHide: true,
    env: {
      ...process.env,
      ENGRAM_INTERNAL: '0',
      ENGRAM_QUIET: '0',
      ENGRAM_URL: 'http://127.0.0.1:9',
      ENGRAM_TOKEN: 'test-token',
    },
  });
  assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trim(), '{"continue":true}');
  assert.doesNotMatch(result.stderr, /HANDLER_RAN/);
});

test('SessionStart registration transport failure renders cached payload without a live fetch', (t) => {
  const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-session-cache-'));
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-identity-v2-session-data-'));
  t.after(() => fs.rmSync(workspace, { recursive: true, force: true }));
  t.after(() => fs.rmSync(dataDir, { recursive: true, force: true }));

  const selector = lib.ProjectIDWithName(workspace);
  const cachePath = path.join(dataDir, 'cache', `session-start-${selector}.json`);
  fs.mkdirSync(path.dirname(cachePath), { recursive: true });
  fs.writeFileSync(cachePath, JSON.stringify({
    issues: [],
    rules: [],
    memories: [{ content: 'cached offline session context' }],
    generated_at: '2026-04-22T11:59:59Z',
  }));

  const result = spawnSync(process.execPath, [require.resolve('./session-start')], {
    input: JSON.stringify({ session_id: 'registration-offline-cache', cwd: workspace }),
    encoding: 'utf8',
    timeout: 3000,
    windowsHide: true,
    env: {
      ...process.env,
      ENGRAM_INTERNAL: '0',
      ENGRAM_QUIET: '0',
      ENGRAM_URL: 'http://127.0.0.1:9',
      ENGRAM_TOKEN: 'test-token',
      ENGRAM_DATA_DIR: dataDir,
    },
  });

  assert.equal(result.error, undefined, result.error ? result.error.message : result.stderr);
  assert.equal(result.status, 0, result.stderr);
  const response = JSON.parse(result.stdout.trim());
  const additionalContext = response.hookSpecificOutput && response.hookSpecificOutput.additionalContext || '';
  assert.match(additionalContext, /<engram-session-start-stale>/);
  assert.match(additionalContext, /cached offline session context/);
  assert.match(result.stderr, /Using cached session-start payload/);
  assert.match(result.stderr, /skipping live session-start requests/);
  assert.doesNotMatch(result.stderr, /static session-start fetch failed/);
});

test('project registration offline classifier includes abort timeouts', () => {
  assert.equal(lib.isProjectIdentityTransportOffline({ name: 'AbortError' }), true);
});

test('registration is synchronous, idempotent, and updates the hook canonical selector', async () => {
  const calls = [];
  const context = {
    Project: 'legacy-selector',
    ProjectIdentityV2: {
      version: 2,
      legacy_project_id: 'legacy-selector',
      display_name: 'fixture',
      git_remote: '',
      relative_path: '',
      non_git_anchor: '00112233445566778899aabbccddeeff',
      anchor_shared: false,
    },
  };
  const requestFn = async (_method, endpoint, body) => {
    calls.push({ endpoint, body });
    return { canonical_project: 'canonical-v2' };
  };

  await lib.registerProjectIdentityV2(context, requestFn);
  await lib.registerProjectIdentityV2(context, requestFn);

  assert.equal(context.Project, 'canonical-v2');
  assert.equal(calls.length, 2);
  assert.equal(calls[0].endpoint, '/api/context/inject');
  assert.equal(calls[0].body.identity_only, true);
});

test('registration rejects shared invalid selectors before transport', async () => {
  for (const vector of vectors.invalid_vectors) {
    if (vector.invalid_target !== 'selector') continue;
    let requests = 0;
    const context = {
      Project: vector.selector,
      ProjectIdentityV2: lib.buildProjectIdentityV2(vector),
    };
    await assert.rejects(
      () => lib.registerProjectIdentityV2(context, async () => {
        requests++;
        return { canonical_project: 'must-not-run' };
      }),
      /PROJECT_IDENTITY_INVALID/,
      vector.name,
    );
    assert.equal(requests, 0, vector.name);
  }
});

test('registration preserves legacy selector characters accepted by the HTTP boundary', async () => {
  const selector = 'legacy:C\\workspace';
  const context = {
    Project: selector,
    ProjectIdentityV2: {
      version: 2,
      legacy_project_id: selector,
      display_name: 'workspace',
      git_remote: 'https://example.invalid/acme/mono.git',
      relative_path: 'packages/core/',
      non_git_anchor: '',
      anchor_shared: null,
    },
  };
  let sentSelector = '';
  await lib.registerProjectIdentityV2(context, async (_method, _endpoint, body) => {
    sentSelector = body.project;
    return { canonical_project: selector };
  });
  assert.equal(sentSelector, selector);
  assert.equal(context.Project, selector);
});

test('registration fails closed on malformed canonical responses without raw fallback', async () => {
  const payloads = [
    {},
    { canonical_project: '' },
    { canonical_project: 42 },
    { canonical_project: ' invalid-canonical ' },
    { canonical_project: '../private' },
  ];
  for (const payload of payloads) {
    const context = {
      Project: 'legacy-selector',
      ProjectIdentityV2: {
        version: 2,
        legacy_project_id: 'legacy-selector',
        display_name: 'fixture',
        git_remote: 'https://example.invalid/acme/mono.git',
        relative_path: 'packages/core/',
        non_git_anchor: '',
        anchor_shared: null,
      },
    };
    let downstream = 0;
    await assert.rejects(async () => {
      await lib.registerProjectIdentityV2(context, async () => payload);
      downstream++;
    }, /PROJECT_IDENTITY_UNAVAILABLE/);
    assert.equal(context.Project, 'legacy-selector');
    assert.equal(downstream, 0);
  }
});
