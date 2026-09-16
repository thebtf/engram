import assert from 'node:assert/strict';
import test from 'node:test';

import { EngramRestClient } from '../dist/client.js';
import { handleSessionEnd } from '../dist/hooks/session-end.js';
import { createEngramOutcomeTool } from '../dist/tools/engram-outcome.js';

const retirement = {
  contract_version: 'engram.outcome-retirement.v1',
  code: 'OUTCOME_CALLBACK_RETIRED',
  action: 'upgrade_outcome_adapter',
};

function clientConfig() {
  return { url: 'http://engram.test:37777', token: 'test-token', timeoutMs: 1000 };
}

test('versioned outcome retirement is surfaced without poisoning availability', async (t) => {
  const originalFetch = globalThis.fetch;
  const originalError = console.error;
  const originalWarn = console.warn;
  t.after(() => {
    globalThis.fetch = originalFetch;
    console.error = originalError;
    console.warn = originalWarn;
  });
  globalThis.fetch = async () => new Response(JSON.stringify({ ...retirement, ignored: 'must-not-surface' }), {
    status: 410,
    statusText: 'Gone',
    headers: { 'Content-Type': 'application/json' },
  });
  console.error = () => { };
  console.warn = () => { };

  const client = new EngramRestClient(clientConfig());
  for (let attempt = 0; attempt < 3; attempt += 1) {
    const result = await client.setSessionOutcome('openclaw-session', 'partial', 'no durable outcome');
    assert.deepEqual(result, retirement);
    assert.equal(client.isAvailable(), true, `retirement attempt ${attempt + 1} must not trip availability`);
  }
});

test('automatic and explicit outcome callers report retirement rather than recording success', async () => {
  const warnings = [];
  const client = {
    isAvailable: () => true,
    registerAndResolveProject: async () => ({ ok: true, canonicalProject: 'canonical-project' }),
    setSessionOutcome: async () => retirement,
  };
  const logger = {
    debug() { },
    info() { },
    warn(message) { warnings.push(String(message)); },
    error() { },
  };

  await handleSessionEnd(
    { messages: [{ role: 'assistant', content: 'completed' }] },
    { agentId: 'agent-a', sessionId: 'openclaw-session' },
    client,
    { autoExtract: false },
    logger,
  );
  await new Promise((resolve) => setImmediate(resolve));

  const automatic = warnings.join('\n');
  assert.match(automatic, /contract_version=engram\.outcome-retirement\.v1/);
  assert.match(automatic, /code=OUTCOME_CALLBACK_RETIRED/);
  assert.match(automatic, /action=upgrade_outcome_adapter/);
  assert.doesNotMatch(automatic, /outcome=partial/);

  const tool = createEngramOutcomeTool(
    { sessionId: 'openclaw-session' },
    client,
    {},
  );
  const explicit = await tool.execute('outcome-retirement', { outcome: 'success', reason: 'must not be recorded' });
  assert.match(explicit, /contract_version=engram\.outcome-retirement\.v1/);
  assert.match(explicit, /code=OUTCOME_CALLBACK_RETIRED/);
  assert.match(explicit, /action=upgrade_outcome_adapter/);
  assert.doesNotMatch(explicit, /Session outcome recorded/);
});
