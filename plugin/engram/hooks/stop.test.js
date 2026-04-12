const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const lib = require('./lib');
const { handleStop } = require('./stop');

function makeTranscriptFile(lines) {
  const filePath = path.join(
    os.tmpdir(),
    `engram-stop-hook-test-${process.pid}-${Date.now()}-${Math.random().toString(16).slice(2)}.jsonl`
  );
  fs.writeFileSync(filePath, `${lines.join('\n')}\n`, 'utf8');
  return filePath;
}

test('handleStop does not early-return when numeric session lookup fails', async () => {
  const claudeSessionID = 'stop-lookup-fail-session';
  const getCalls = [];
  const postCalls = [];

  const originalRequestGet = lib.requestGet;
  const originalRequestPost = lib.requestPost;
  const originalRequestUpload = lib.requestUpload;

  lib.requestGet = async (endpoint) => {
    getCalls.push(endpoint);
    if (endpoint === '/api/health') return {};
    if (endpoint.startsWith('/api/sessions?claudeSessionId=')) {
      throw new Error('session not found');
    }
    if (endpoint === `/api/sessions/${encodeURIComponent(claudeSessionID)}/injections`) {
      return { injections: [] };
    }
    if (endpoint === '/api/observations?limit=100&offset=0') {
      return { observations: [] };
    }
    throw new Error(`Unexpected GET endpoint in test: ${endpoint}`);
  };

  lib.requestPost = async (endpoint, body) => {
    postCalls.push({ endpoint, body });
    return {};
  };

  lib.requestUpload = async (endpoint) => {
    throw new Error(`Unexpected upload endpoint in test: ${endpoint}`);
  };

  try {
    await handleStop(
      {
        SessionID: claudeSessionID,
        RawInput: '',
        Project: '',
      },
      {}
    );

    assert.ok(
      postCalls.some((call) => call.endpoint === `/api/sessions/${claudeSessionID}/outcome`),
      'Expected outcome endpoint to be called even when DB lookup fails'
    );

    assert.ok(
      getCalls.includes(`/api/sessions/${encodeURIComponent(claudeSessionID)}/injections`),
      'Expected Claude-session-keyed injections endpoint to be called'
    );

    assert.equal(
      postCalls.some((call) => call.endpoint.includes('/summarize')),
      false,
      'Should skip summarize when numeric DB session ID is unavailable'
    );
    assert.equal(
      postCalls.some((call) => call.endpoint.includes('/extract-learnings')),
      false,
      'Should skip extract-learnings when numeric DB session ID is unavailable'
    );
  } finally {
    lib.requestGet = originalRequestGet;
    lib.requestPost = originalRequestPost;
    lib.requestUpload = originalRequestUpload;
    lib.clearSessionSignals(claudeSessionID);
  }
});

test('handleStop uses Claude session ID for injections and mark-cited endpoints', async () => {
  const claudeSessionID = 'stop-claude-keyed-session';
  const numericSessionID = 123;

  const transcriptPath = makeTranscriptFile([
    JSON.stringify({
      type: 'assistant',
      message: { content: 'engram__search used due to missing injected context' },
    }),
  ]);

  const getCalls = [];
  const postCalls = [];

  const originalRequestGet = lib.requestGet;
  const originalRequestPost = lib.requestPost;
  const originalRequestUpload = lib.requestUpload;

  lib.requestGet = async (endpoint) => {
    getCalls.push(endpoint);
    if (endpoint === '/api/health') return {};
    if (endpoint.startsWith('/api/sessions?claudeSessionId=')) {
      return { id: numericSessionID };
    }
    if (endpoint === `/api/sessions/${encodeURIComponent(claudeSessionID)}/injections`) {
      return {
        injections: [
          {
            observation_id: 7,
            title: 'A title that does not need citation for this test',
            facts: [],
          },
        ],
      };
    }
    if (endpoint === '/api/observations?limit=100&offset=0') {
      return { observations: [] };
    }
    throw new Error(`Unexpected GET endpoint in test: ${endpoint}`);
  };

  lib.requestPost = async (endpoint, body) => {
    postCalls.push({ endpoint, body });
    return {};
  };

  lib.requestUpload = async () => {
    throw new Error('requestUpload should not be called in this test');
  };

  try {
    await handleStop(
      {
        SessionID: claudeSessionID,
        RawInput: '',
        Project: 'engram',
      },
      { transcript_path: transcriptPath }
    );

    assert.ok(
      getCalls.includes(`/api/sessions/${encodeURIComponent(claudeSessionID)}/injections`),
      'Expected Claude-session-keyed injections endpoint'
    );
    assert.equal(
      getCalls.includes(`/api/sessions/${numericSessionID}/injections`),
      false,
      'Should not call numeric-session-keyed injections endpoint'
    );

    assert.ok(
      postCalls.some((call) => call.endpoint === `/api/sessions/${encodeURIComponent(claudeSessionID)}/mark-cited`),
      'Expected Claude-session-keyed mark-cited endpoint'
    );
    assert.equal(
      postCalls.some((call) => call.endpoint === `/api/sessions/${numericSessionID}/mark-cited`),
      false,
      'Should not call numeric-session-keyed mark-cited endpoint'
    );

    const insufficientCall = postCalls.find(
      (call) => call.endpoint === '/api/observations/feedback/insufficient-injection'
    );
    assert.ok(insufficientCall, 'Expected insufficient-injection feedback call');
    assert.equal(insufficientCall.body.session_id, claudeSessionID);

    assert.ok(
      postCalls.some((call) => call.endpoint === `/api/sessions/${numericSessionID}/summarize`),
      'Expected summarize to keep numeric DB session ID'
    );
    assert.ok(
      postCalls.some((call) => call.endpoint === `/api/sessions/${numericSessionID}/extract-learnings`),
      'Expected extract-learnings to keep numeric DB session ID'
    );
  } finally {
    lib.requestGet = originalRequestGet;
    lib.requestPost = originalRequestPost;
    lib.requestUpload = originalRequestUpload;
    lib.clearSessionSignals(claudeSessionID);
    fs.unlinkSync(transcriptPath);
  }
});
