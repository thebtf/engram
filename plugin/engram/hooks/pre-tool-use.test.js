const assert = require('node:assert/strict');
const test = require('node:test');

const preToolUse = require('./pre-tool-use');
const lib = require('./lib');

function cleanup(sessionID) {
  lib.clearSessionSignals(sessionID);
}

test('returns empty for unsupported tool', async () => {
  const originalRequestGet = lib.requestGet;
  lib.requestGet = async () => {
    throw new Error('requestGet should not be called');
  };

  try {
    const result = await preToolUse.handlePreToolUse({ Project: 'engram', SessionID: 's1' }, {
      tool_name: 'Grep',
      tool_input: { pattern: 'x' },
    });
    assert.equal(result, '');
  } finally {
    lib.requestGet = originalRequestGet;
  }
});

test('Read with repeated signal returns trigger context', async () => {
  const sessionID = 'pre-tool-read-1';
  cleanup(sessionID);
  const signalPath = require('path').join(require('os').tmpdir(), `engram-signals-${sessionID}.json`);
  require('fs').writeFileSync(signalPath, JSON.stringify({ read_counts: { 'internal/auth.go': 3 } }), 'utf8');

  const originalRequestGet = lib.requestGet;
  const originalRequestPost = lib.requestPost;
  lib.requestGet = async () => {
    throw new Error('requestGet should not be called for Read trigger path');
  };
  lib.requestPost = async (endpoint, body, timeoutMs) => {
    assert.equal(endpoint, '/api/memory/triggers');
    assert.equal(timeoutMs, 200);
    assert.deepEqual(body, {
      tool: 'Read',
      params: { file_path: 'internal/auth.go', read_counts: { 'internal/auth.go': 3 } },
      project: 'engram',
      session_id: sessionID,
    });
    return {
      matches: [
        { kind: 'context', observation_id: 11, blurb: 'Auth decision context' },
      ],
    };
  };

  try {
    const result = await preToolUse.handlePreToolUse({ Project: 'engram', SessionID: sessionID }, {
      tool_name: 'Read',
      tool_input: { file_path: 'internal/auth.go' },
    });
    assert.match(result, /Auth decision context/);
    assert.match(result, /<file-context>/);
  } finally {
    lib.requestGet = originalRequestGet;
    lib.requestPost = originalRequestPost;
    cleanup(sessionID);
  }
});

test('Bash returns warning context from trigger endpoint', async () => {
  const originalRequestGet = lib.requestGet;
  const originalRequestPost = lib.requestPost;
  lib.requestGet = async () => {
    throw new Error('requestGet should not be called for Bash trigger path');
  };
  lib.requestPost = async (endpoint, body, timeoutMs) => {
    assert.equal(endpoint, '/api/memory/triggers');
    assert.equal(timeoutMs, 200);
    assert.deepEqual(body, {
      tool: 'Bash',
      params: { command: 'git push --force origin main' },
      project: 'engram',
      session_id: 'bash-1',
    });
    return {
      matches: [
        { kind: 'warning', observation_id: 42, blurb: 'This command previously failed' },
      ],
    };
  };

  try {
    const result = await preToolUse.handlePreToolUse({ Project: 'engram', SessionID: 'bash-1' }, {
      tool_name: 'Bash',
      tool_input: { command: 'git push --force origin main' },
    });
    assert.match(result, /This command previously failed/);
    assert.match(result, /<file-context>/);
  } finally {
    lib.requestGet = originalRequestGet;
    lib.requestPost = originalRequestPost;
  }
});

test('Read preserves existing read_counts value for file path', async () => {
  const originalRequestPost = lib.requestPost;
  lib.requestPost = async (endpoint, body) => {
    assert.equal(endpoint, '/api/memory/triggers');
    assert.deepEqual(body.params.read_counts, { 'internal/auth.go': 1 });
    return {
      matches: [
        { kind: 'context', observation_id: 99, blurb: 'Existing count preserved' },
      ],
    };
  };

  try {
    const result = await preToolUse.handlePreToolUse({ Project: 'engram', SessionID: 'read-preserve-1' }, {
      tool_name: 'Read',
      tool_input: {
        file_path: 'internal/auth.go',
        read_counts: { 'internal/auth.go': 1 },
      },
    });
    assert.match(result, /Existing count preserved/);
  } finally {
    lib.requestPost = originalRequestPost;
  }
});

test('Edit skips Windows Temp paths case-insensitively', async () => {
  const originalRequestGet = lib.requestGet;
  const originalRequestPost = lib.requestPost;
  lib.requestGet = async () => {
    throw new Error('requestGet should not be called for temp path');
  };
  lib.requestPost = async () => {
    throw new Error('requestPost should not be called for temp path');
  };

  try {
    const result = await preToolUse.handlePreToolUse({ Project: 'engram', SessionID: 'temp-path-1' }, {
      tool_name: 'Edit',
      tool_input: { file_path: 'C:\\Users\\test\\Temp\\scratch.txt' },
    });
    assert.equal(result, '');
  } finally {
    lib.requestGet = originalRequestGet;
    lib.requestPost = originalRequestPost;
  }
});

