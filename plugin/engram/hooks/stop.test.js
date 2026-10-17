const assert = require('node:assert/strict');
const test = require('node:test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const { randomUUID } = require('node:crypto');
const { once } = require('node:events');
const { spawn } = require('node:child_process');
const NODE_CHILD_TIMEOUT_MS = process.platform === 'win32' ? 10000 : 3000;

const lib = require('./lib');
const { handleStop, extractAgentOutput } = require('./stop');

test('handleStop does not post without a session and project', async (t) => {
  const originalPost = lib.requestPost;
  const requests = [];
  lib.requestPost = async (...args) => { requests.push(args); };
  t.after(() => { lib.requestPost = originalPost; });

  for (const context of [{}, { SessionID: 'session' }, { Project: 'project' }]) {
    await handleStop(context, {});
  }
  assert.deepEqual(requests, []);
});

test('handleStop clears the marker before attempting transcript capture offline', async (t) => {
  const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-stop-capture-'));
  const sessionID = `stop-capture-${randomUUID()}`;
  const project = randomUUID();
  const markerPath = path.join(os.tmpdir(), `.engram-pending-${sessionID}`);
  const transcriptPath = path.join(workspace, 'transcript.jsonl');
  const originalPost = lib.requestPost;
  const requests = [];
  t.after(() => {
    lib.requestPost = originalPost;
    fs.rmSync(workspace, { recursive: true, force: true });
    fs.rmSync(markerPath, { force: true });
  });
  fs.writeFileSync(markerPath, 'pending', 'utf8');
  fs.writeFileSync(transcriptPath, [
    { role: 'user', content: 'private user input' },
    { type: 'assistant', message: { role: 'assistant', content: [{ type: 'text', text: 'assistant output' }] } },
  ].map((record) => JSON.stringify(record)).join('\n'));
  lib.requestPost = async (endpoint, body) => {
    requests.push({ endpoint, body, markerPresent: fs.existsSync(markerPath) });
    throw new Error('fixture transport offline');
  };

  await handleStop({ SessionID: sessionID, Project: project }, { transcript_path: transcriptPath });

  assert.deepEqual(requests, [{
    endpoint: '/api/hooks/session-end',
    body: { session_id: sessionID, project, agent_output_text: 'assistant output' },
    markerPresent: false,
  }]);
  assert.equal(fs.existsSync(markerPath), false);
});

for (const resolutionUnavailable of [false, true]) {
  test(resolutionUnavailable
    ? 'Stop does not post when native project resolution is unavailable'
    : 'Stop clears the pending marker and exits when the session-end transport is offline', async (t) => {
      const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-stop-offline-'));
      const sessionID = `stop-offline-${randomUUID()}`;
      const project = randomUUID();
      const markerPath = path.join(workspace, `.engram-pending-${sessionID}`);
      const preloadPath = path.join(workspace, 'native-result.cjs');
      const requests = [];
      const server = http.createServer((request) => {
        let body = '';
        request.setEncoding('utf8');
        request.on('data', (chunk) => { body += chunk; });
        request.on('end', () => {
          requests.push({ method: request.method, path: request.url, body, markerPresent: fs.existsSync(markerPath) });
          request.socket.destroy();
        });
      });
      let child;
      t.after(async () => {
        if (child && child.exitCode === null && child.signalCode === null) {
          child.kill();
          await once(child, 'close');
        }
        server.closeAllConnections();
        await new Promise((resolve) => server.close(resolve));
        fs.rmSync(workspace, { recursive: true, force: true });
      });
      await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
      fs.writeFileSync(markerPath, 'pending', 'utf8');
      // Keep native acquisition outside this hook test, as in run-engram.test.js.
      fs.writeFileSync(preloadPath, `
      require('node:child_process').execFile = (_file, _args, _options, callback) => {
        if (${resolutionUnavailable}) {
          callback(new Error('fixture native resolution offline'), '{"code":"PROJECT_RESOLUTION_UNAVAILABLE"}', '');
          return;
        }
        callback(null, ${JSON.stringify(JSON.stringify({ project_id: project }))}, '');
      };
      require.cache[${JSON.stringify(require.resolve('../scripts/ensure-binary.js'))}] = { exports: {
        loadPolicy: () => ({ target: { desired: {}, predecessor: null } }),
        resolveForLaunch: async () => ({ path: 'fixture-client', target: {} }),
        hashFile: () => true,
        objectRoots: () => ({ objects: 'fixture-objects' }),
        assertSafeDirectory: (directory) => directory,
      } };
    `);

      let stdout = '';
      let stderr = '';
      let timedOut = false;
      child = spawn(process.execPath, ['--require', preloadPath, require.resolve('./stop')], {
        cwd: workspace,
        windowsHide: true,
        env: {
          PATH: process.env.PATH,
          SystemRoot: process.env.SystemRoot,
          TMP: workspace, TEMP: workspace, TMPDIR: workspace,
          HOME: workspace, USERPROFILE: workspace, APPDATA: workspace, LOCALAPPDATA: workspace,
          PLUGIN_ROOT: path.resolve(__dirname, '..'),
          PLUGIN_DATA: path.join(workspace, 'data'),
          ENGRAM_INTERNAL: '0',
          ENGRAM_QUIET: '0',
          ENGRAM_URL: `http://127.0.0.1:${server.address().port}`,
          ENGRAM_TOKEN: 'test-token',
          ENGRAM_CLIENT_INSTANCE_ID: 'hook-install-alpha',
        },
        stdio: ['pipe', 'pipe', 'pipe'],
      });
      child.stdout.setEncoding('utf8');
      child.stderr.setEncoding('utf8');
      child.stdout.on('data', (chunk) => { stdout += chunk; });
      child.stderr.on('data', (chunk) => { stderr += chunk; });
      child.stdin.end(JSON.stringify({ session_id: sessionID, cwd: workspace }));
      const timeout = setTimeout(() => {
        timedOut = true;
        child.kill();
      }, NODE_CHILD_TIMEOUT_MS);
      let status;
      let signal;
      try {
        [status, signal] = await once(child, 'close');
      } finally {
        clearTimeout(timeout);
      }
      const evidence = JSON.stringify({ timedOut, status, signal, stdout, stderr, requests, markerPresent: fs.existsSync(markerPath) });

      assert.equal(timedOut, false, `child exceeded ${NODE_CHILD_TIMEOUT_MS}ms: ${evidence}`);
      assert.equal(signal, null, evidence);
      assert.equal(status, 0, evidence);
      assert.deepEqual(JSON.parse(stdout), { continue: true }, evidence);
      if (resolutionUnavailable) {
        assert.deepEqual(requests, [], evidence);
      } else {
        assert.equal(fs.existsSync(markerPath), false, evidence);
        assert.deepEqual(requests.map(({ body, ...request }) => ({ ...request, body: JSON.parse(body) })), [{
          method: 'POST', path: '/api/hooks/session-end',
          body: { session_id: sessionID, project, agent_output_text: '' },
          markerPresent: false,
        }], evidence);
      }
    });
}

// ---------------------------------------------------------------------------
// extractAgentOutput — nested JSONL transcript shape (real Claude Code format)
// {"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"..."}]}}
// ---------------------------------------------------------------------------

test('extractAgentOutput: extracts text from nested message.content blocks (real CC transcript shape)', () => {
  const lines = [
    JSON.stringify({
      type: 'user',
      message: { role: 'user', content: [{ type: 'text', text: 'Hello' }] },
      uuid: 'u-1',
      sessionId: 'sess-1',
    }),
    JSON.stringify({
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [
          { type: 'text', text: 'Understood, I will help.' },
          { type: 'tool_use', name: 'Bash', input: {} },
        ],
      },
      uuid: 'a-1',
      sessionId: 'sess-1',
    }),
    JSON.stringify({
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [
          { type: 'text', text: ' Done.' },
        ],
      },
      uuid: 'a-2',
      sessionId: 'sess-1',
    }),
  ];

  const tmpFile = path.join(os.tmpdir(), `stop-test-nested-${Date.now()}.jsonl`);
  try {
    fs.writeFileSync(tmpFile, lines.join('\n'), 'utf8');
    const result = extractAgentOutput(tmpFile);
    assert.equal(result, 'Understood, I will help.\n Done.');
  } finally {
    try { fs.unlinkSync(tmpFile); } catch { /* ignore */ }
  }
});

test('extractAgentOutput: user messages are excluded from output (nested shape)', () => {
  const lines = [
    JSON.stringify({
      type: 'user',
      message: { role: 'user', content: [{ type: 'text', text: 'User text should not appear' }] },
      uuid: 'u-1',
      sessionId: 'sess-2',
    }),
    JSON.stringify({
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [{ type: 'text', text: 'Only assistant text' }],
      },
      uuid: 'a-1',
      sessionId: 'sess-2',
    }),
  ];

  const tmpFile = path.join(os.tmpdir(), `stop-test-user-excl-${Date.now()}.jsonl`);
  try {
    fs.writeFileSync(tmpFile, lines.join('\n'), 'utf8');
    const result = extractAgentOutput(tmpFile);
    assert.ok(!result.includes('User text should not appear'), 'user text must not appear in output');
    assert.ok(result.includes('Only assistant text'), 'assistant text must appear in output');
  } finally {
    try { fs.unlinkSync(tmpFile); } catch { /* ignore */ }
  }
});

test('extractAgentOutput: tool_use blocks are skipped, only text blocks extracted (nested shape)', () => {
  const lines = [
    JSON.stringify({
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [
          { type: 'text', text: 'First text' },
          { type: 'tool_use', name: 'Read', input: { file_path: '/secret' } },
          { type: 'text', text: 'Second text' },
        ],
      },
      uuid: 'a-1',
      sessionId: 'sess-3',
    }),
  ];

  const tmpFile = path.join(os.tmpdir(), `stop-test-tool-skip-${Date.now()}.jsonl`);
  try {
    fs.writeFileSync(tmpFile, lines.join('\n'), 'utf8');
    const result = extractAgentOutput(tmpFile);
    assert.ok(result.includes('First text'), 'first text block must be included');
    assert.ok(result.includes('Second text'), 'second text block must be included');
    assert.ok(!result.includes('/secret'), 'tool_use input must not appear in output');
  } finally {
    try { fs.unlinkSync(tmpFile); } catch { /* ignore */ }
  }
});

test('extractAgentOutput: flat-role shape (record.role=assistant) still works', () => {
  const lines = [
    JSON.stringify({ role: 'assistant', content: 'Flat role text' }),
  ];

  const tmpFile = path.join(os.tmpdir(), `stop-test-flat-role-${Date.now()}.jsonl`);
  try {
    fs.writeFileSync(tmpFile, lines.join('\n'), 'utf8');
    const result = extractAgentOutput(tmpFile);
    assert.equal(result, 'Flat role text');
  } finally {
    try { fs.unlinkSync(tmpFile); } catch { /* ignore */ }
  }
});

test('extractAgentOutput: empty transcript returns empty string', () => {
  const tmpFile = path.join(os.tmpdir(), `stop-test-empty-${Date.now()}.jsonl`);
  try {
    fs.writeFileSync(tmpFile, '', 'utf8');
    const result = extractAgentOutput(tmpFile);
    assert.equal(result, '');
  } finally {
    try { fs.unlinkSync(tmpFile); } catch { /* ignore */ }
  }
});

test('extractAgentOutput: non-existent file returns empty string without throwing', () => {
  const result = extractAgentOutput('/tmp/engram-nonexistent-transcript-xyz.jsonl');
  assert.equal(result, '');
});
