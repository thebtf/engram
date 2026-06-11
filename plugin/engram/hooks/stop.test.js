const assert = require('node:assert/strict');
const test = require('node:test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const { handleStop, extractAgentOutput } = require('./stop');

test('handleStop resolves to an empty string for compatibility no-op behavior', async () => {
  await assert.doesNotReject(async () => {
    const result = await handleStop(
      {
        SessionID: 'stop-noop-session',
        RawInput: '',
        Project: 'engram',
      },
      {}
    );

    assert.equal(result, '');
  });
});

test('handleStop ignores optional input payload and remains a no-op', async () => {
  const result = await handleStop(
    {
      SessionID: 'stop-noop-session',
      RawInput: '{"some":"payload"}',
      Project: 'engram',
    },
    {
      transcript_path: '/tmp/nonexistent.jsonl',
      reason: 'session-end',
    }
  );

  assert.equal(result, '');
});

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
