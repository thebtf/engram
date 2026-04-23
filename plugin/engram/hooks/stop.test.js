const assert = require('node:assert/strict');
const test = require('node:test');

const { handleStop } = require('./stop');

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
