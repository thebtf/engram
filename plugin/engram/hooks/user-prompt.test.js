const assert = require('node:assert/strict');
const test = require('node:test');

const userPrompt = require('./user-prompt');
const lib = require('./lib');

function makeHint(id, title, reason, score, source = 's2.meta_index') {
  return { id, title, reason, score, source };
}

test('formats seeded ambient hints into additionalContext and caps to three', async () => {
  const originalRequestPost = lib.requestPost;
  const calls = [];
  lib.requestPost = async (endpoint, body, timeoutMs) => {
    calls.push({ endpoint, body, timeoutMs });
    if (endpoint === '/api/hooks/segment-check') {
      return {};
    }
    if (endpoint === '/api/hooks/ambient-candidates') {
      assert.equal(timeoutMs, 200, 'same-turn ambient call must stay inside the 200 ms budget');
      assert.deepEqual(body, {
        session_id: 'session-hook-ambient',
        project: 'engram',
        prompt_text: 'Need the strongest ambient hints for this handoff',
        limit: 3,
      });
      return {
        hints: [
          makeHint('1', 'Release handoff checklist', 'tag:handoff', 0.92),
          makeHint('2', 'Retry the failing command', 'outcome:repair', 0.87, 's6.outcome_policy'),
          makeHint('3', 'Review PM oracle drift', 'tag:oracle', 0.83),
          makeHint('4', 'Should be trimmed away', 'tag:overflow', 0.79),
        ],
      };
    }
    throw new Error(`unexpected endpoint ${endpoint}`);
  };

  try {
    const result = await userPrompt.handleUserPrompt(
      { Project: 'engram', SessionID: 'session-hook-ambient' },
      { user_message: 'Need the strongest ambient hints for this handoff' },
    );

    assert.match(result, /Memory suggests \(you may ignore\)/);
    assert.match(result, /Release handoff checklist/);
    assert.match(result, /Retry the failing command/);
    assert.match(result, /Review PM oracle drift/);
    assert.doesNotMatch(result, /Should be trimmed away/);

    const ambientCalls = calls.filter((call) => call.endpoint === '/api/hooks/ambient-candidates');
    assert.equal(ambientCalls.length, 1, 'hook must synchronously call /api/hooks/ambient-candidates once');
  } finally {
    lib.requestPost = originalRequestPost;
  }
});

test('fails open to empty output for disabled, timeout, missing-daemon, malformed, and no-content ambient responses', async () => {
  const cases = [
    {
      name: 'disabled',
      responder: async () => ({ disabled: true, reason: 's3 disabled' }),
    },
    {
      name: 'timeout',
      responder: async () => {
        const err = new Error('ambient timeout');
        err.name = 'AbortError';
        throw err;
      },
    },
    {
      name: 'missing daemon',
      responder: async () => {
        const err = new Error('connect ECONNREFUSED 127.0.0.1:37777');
        err.code = 'ECONNREFUSED';
        throw err;
      },
    },
    {
      name: 'malformed payload',
      responder: async () => ({ hints: 'not-an-array' }),
    },
    {
      name: 'no-content payload',
      responder: async () => ({ hints: [] }),
    },
  ];

  for (const tc of cases) {
    const originalRequestPost = lib.requestPost;
    const calls = [];
    lib.requestPost = async (endpoint, body, timeoutMs) => {
      calls.push({ endpoint, body, timeoutMs });
      if (endpoint === '/api/hooks/segment-check') {
        return {};
      }
      if (endpoint === '/api/hooks/ambient-candidates') {
        return tc.responder();
      }
      throw new Error(`unexpected endpoint ${endpoint}`);
    };

    try {
      const result = await userPrompt.handleUserPrompt(
        { Project: 'engram', SessionID: `session-hook-${tc.name}` },
        { user_message: `Need a safe ambient response for ${tc.name}` },
      );

      assert.equal(result, '', `${tc.name} must fail open to empty hook output`);
      const ambientCalls = calls.filter((call) => call.endpoint === '/api/hooks/ambient-candidates');
      assert.equal(ambientCalls.length, 1, `${tc.name} must still attempt the sync ambient route`);
      assert.equal(ambientCalls[0].timeoutMs, 200, `${tc.name} must preserve the 200 ms ambient budget`);
    } finally {
      lib.requestPost = originalRequestPost;
    }
  }
});

test('segment-check failure still allows ambient success', async () => {
  const originalRequestPost = lib.requestPost;
  lib.requestPost = async (endpoint) => {
    if (endpoint === '/api/hooks/segment-check') {
      throw new Error('segment-check offline');
    }
    if (endpoint === '/api/hooks/ambient-candidates') {
      return {
        hints: [
          makeHint('1', 'Ambient survives segment failure', 'tag:segment', 0.91),
        ],
      };
    }
    throw new Error(`unexpected endpoint ${endpoint}`);
  };

  try {
    const result = await userPrompt.handleUserPrompt(
      { Project: 'engram', SessionID: 'session-segment-failure' },
      { user_message: 'Prompt flow must survive segment-check failure' },
    );

    assert.match(result, /Ambient survives segment failure/);
  } finally {
    lib.requestPost = originalRequestPost;
  }
});

test('slow ambient endpoint and slow segment-check do not block prompt flow past budget', async () => {
  const originalRequestPost = lib.requestPost;
  lib.requestPost = async (endpoint) => {
    await new Promise((resolve) => setTimeout(resolve, 350));
    if (endpoint === '/api/hooks/segment-check') {
      return {};
    }
    if (endpoint === '/api/hooks/ambient-candidates') {
      return {
        hints: [makeHint('1', 'Too slow to keep', 'tag:slow', 0.77)],
      };
    }
    throw new Error(`unexpected endpoint ${endpoint}`);
  };

  try {
    const startedAt = Date.now();
    const result = await userPrompt.handleUserPrompt(
      { Project: 'engram', SessionID: 'session-slow-both' },
      { user_message: 'Slow support infrastructure must not block prompt flow' },
    );
    const elapsedMs = Date.now() - startedAt;

    assert.equal(result, '', 'slow ambient delivery must fail open to empty output');
    assert.ok(elapsedMs < 300, `hook must return within the ambient budget; took ${elapsedMs}ms`);
  } finally {
    lib.requestPost = originalRequestPost;
  }
});

test('both endpoints unavailable still fail open to empty output', async () => {
  const originalRequestPost = lib.requestPost;
  lib.requestPost = async (endpoint) => {
    if (endpoint === '/api/hooks/segment-check') {
      throw new Error('segment-check unavailable');
    }
    if (endpoint === '/api/hooks/ambient-candidates') {
      const err = new Error('ambient unavailable');
      err.code = 'ECONNREFUSED';
      throw err;
    }
    throw new Error(`unexpected endpoint ${endpoint}`);
  };

  try {
    const result = await userPrompt.handleUserPrompt(
      { Project: 'engram', SessionID: 'session-both-unavailable' },
      { user_message: 'Both support endpoints can be unavailable' },
    );

    assert.equal(result, '');
  } finally {
    lib.requestPost = originalRequestPost;
  }
});

test('ambient helpers forward an explicit remaining budget and signal', async () => {
  const originalRequestPost = lib.requestPost;
  const controller = new AbortController();
  const calls = [];
  lib.requestPost = async (...args) => {
    calls.push(args);
    return { hints: [makeHint('1', 'Forwarded deadline', 'tag:signal', 0.9)] };
  };
  try {
    const result = await userPrompt.fetchAmbientAdditionalContext(
      'engram', 'ambient-options', 'Need deadline propagation', 123, { signal: controller.signal },
    );
    assert.match(result, /Forwarded deadline/);
    assert.equal(calls.length, 1);
    assert.equal(calls[0][2], 123);
    assert.strictEqual(calls[0][3].signal, controller.signal);
  } finally {
    lib.requestPost = originalRequestPost;
  }
});

function hasLoneSurrogate(value) {
  for (let i = 0; i < value.length; i += 1) {
    const code = value.charCodeAt(i);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(i + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return true;
      i += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true;
    }
  }
  return false;
}

test('formats multi-byte ambient hints without splitting code points during truncation', async () => {
  const originalRequestPost = lib.requestPost;
  lib.requestPost = async (endpoint) => {
    if (endpoint === '/api/hooks/segment-check') {
      return {};
    }
    if (endpoint === '/api/hooks/ambient-candidates') {
      return {
        hints: [
          makeHint('unicode', '😀'.repeat(81), '🧠'.repeat(121), 0.91),
        ],
      };
    }
    throw new Error(`unexpected endpoint ${endpoint}`);
  };

  try {
    const result = await userPrompt.handleUserPrompt(
      { Project: 'engram', SessionID: 'session-hook-unicode' },
      { user_message: 'Unicode ambient hints must truncate safely' },
    );

    assert.match(result, /Memory suggests \(you may ignore\)/);
    assert.equal(hasLoneSurrogate(result), false, 'formatted hints must not contain split surrogate pairs after truncating multi-byte text');
    assert.equal(Buffer.from(result, 'utf8').includes(Buffer.from('\uFFFD', 'utf8')), false, 'formatted UTF-8 output must not emit Unicode replacement characters');
  } finally {
    lib.requestPost = originalRequestPost;
  }
});
