import assert from 'node:assert/strict';
import test from 'node:test';
import { createEngramMemoryExtension } from './engram-memory.mjs';

const projects = { '/session/a': '11111111-1111-4111-8111-111111111111', '/session/b': '22222222-2222-4222-8222-222222222222' };
function extension(overrides = {}) {
  const calls = [];
  const adapter = createEngramMemoryExtension({
    isQuiet: () => false,
    resolveEngramRuntimeConfig: async () => ({ serverURL: 'http://fixture', token: '', quiet: false }),
    resolveHookProjectContext: async cwd => ({ Project: projects[cwd] }),
    requestPost: async (route, body) => {
      calls.push({ route, body });
      return route === '/api/context/session-start'
        ? { memories: [{ content: `known memory for ${body.project}` }] }
        : { additional_context: `ambient for ${body.project}` };
    },
    ...overrides,
  });
  return { adapter, calls };
}

test('OMP automatic callbacks use current root and one UUID without identity registration', async () => {
  const { adapter, calls } = extension();
  const handlers = new Map();
  const sent = [];
  adapter.install({ on: (name, fn) => handlers.set(name, fn), sendMessage: (message, options) => sent.push({ message, options }) });
  await Promise.all([
    handlers.get('session_start')({ cwd: '/session/a', sessionId: 'a' }, {}),
    handlers.get('session_start')({ cwd: '/session/b', sessionId: 'b' }, {}),
  ]);
  assert.equal(sent.length, 2);
  assert.match(sent[0].message.content, /known memory for 11111111/);
  assert.deepEqual(sent[0].options, { deliverAs: 'nextTurn' });
  const result = await handlers.get('before_agent_start')({ cwd: '/session/b', sessionId: 'a', prompt: 'new root' }, {});
  assert.equal(result.message.content, `ambient for ${projects['/session/b']}`);
  assert.deepEqual(calls.map(call => call.body.project), [projects['/session/a'], projects['/session/b'], projects['/session/b']]);
  assert.deepEqual(calls.map(call => call.route), ['/api/context/session-start', '/api/context/session-start', '/api/hooks/ambient-candidates']);
  assert.equal(calls.some(call => 'project_descriptor' in call.body || 'sessionCapability' in call.body), false);
});

test('OMP missing context, quiet and resolution failure do not make scoped requests', async () => {
  const { adapter, calls } = extension();
  assert.equal(await adapter.sessionStartMessage({ sessionId: 'no-root' }, {}), null);
  assert.equal(await adapter.ambientMessage({ cwd: '/session/a', sessionId: 'a' }, {}), null);
  assert.equal(calls.length, 0);
  const quiet = extension({ isQuiet: () => true });
  assert.equal(await quiet.adapter.sessionStartMessage({ cwd: '/session/a', sessionId: 'a' }, {}), null);
  const failed = extension({ resolveHookProjectContext: async () => { throw new Error('PROJECT_IDENTITY_CONFLICT'); } });
  assert.equal(await failed.adapter.sessionStartMessage({ cwd: '/session/a', sessionId: 'a' }, {}), null);
  assert.equal(failed.calls.length, 0);
});
