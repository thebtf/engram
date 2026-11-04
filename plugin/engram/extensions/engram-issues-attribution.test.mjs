import assert from 'node:assert/strict';
import test from 'node:test';
import { createEngramMemoryExtension } from './engram-memory.mjs';

// The default fixture uses the configured server's generated name. Selection is
// by the actual event.toolName and its unique marked ToolInfo, not this spelling.
// The marker opts into reported provenance; it does not authenticate a server.
const issuesToolName = 'mcp__engram_issues';
const project = '11111111-1111-4111-8111-111111111111';
const targetProject = '22222222-2222-4222-8222-222222222222';
const sessionA = '0192a352-1111-7111-8111-111111111111';
const sessionB = '0192a352-2222-7222-8222-222222222222';
const untrustedSession = '33333333-3333-4333-8333-333333333333';
const cwd = '/fixture/same-project';

function hostContext(session) {
  return { cwd, sessionManager: { getSessionId: () => session } };
}

function issueEvent(input, toolCallId = 'fixture-issue-call', toolName = issuesToolName) {
  return { type: 'tool_call', toolName, toolCallId, input };
}

function advertisedIssueTool(name = issuesToolName) {
  return {
    name,
    description: 'Synthetic issue tool advertising reported caller provenance',
    parameters: {
      type: 'object',
      required: ['action'],
      'x-engram-caller-context': 'issues/v1',
      properties: {
        action: { type: 'string' },
        session_id: { type: 'string' },
        client: { type: 'string' },
      },
    },
    sourceInfo: { path: '<fixture:mcp-opt-in>', source: 'mcp', scope: 'temporary', origin: 'top-level' },
  };
}

async function registeredHook(hostOverrides = {}) {
  const handlers = new Map();
  const adapter = createEngramMemoryExtension({
    isQuiet: () => false,
    resolveEngramRuntimeConfig: async () => ({ serverURL: 'http://fixture.invalid', token: '', quiet: false }),
    prepareHookClient: async () => { },
    resolveHookProjectContext: async () => assert.fail('issue attribution must not resolve project identity'),
    requestPost: async () => assert.fail('issue attribution must not request memory enrichment'),
    onDiagnostic: message => assert.fail(`unexpected readiness diagnostic: ${message}`),
  });
  const pi = {
    on(name, handler) {
      assert.equal(handlers.has(name), false, `duplicate ${name} registration`);
      handlers.set(name, handler);
    },
    sendMessage() { assert.fail('issue attribution must not deliver hidden context'); },
    getAllTools: () => [advertisedIssueTool()],
    ...hostOverrides,
  };
  await adapter.install(pi);
  await adapter.install(pi);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(typeof handlers.get('session_start'), 'function');
  assert.equal(typeof handlers.get('before_agent_start'), 'function');
  const handler = handlers.get('tool_call');
  assert.equal(typeof handler, 'function', 'real extension installation must register the issue tool_call hook');
  return handler;
}

function assertReportedInput(result, input, session) {
  assert.deepEqual(result, { input: { ...input, session_id: session, client: 'omp' } },
    'only reported session/client may change; project, action, body and target are not authority inputs');
}

test('OMP registered issue hook stamps current native session and honest host client for every issues action', async () => {
  const handler = await registeredHook();
  const actions = [
    { action: 'create', project, target_project: targetProject, title: 'Fixture issue', body: 'User body: session_id=do-not-rewrite', type: 'bug', priority: 'high', labels: ['fixture'] },
    { action: 'list', project: targetProject, source_project: project, source_agent: 'reported-actor', limit: 5 },
    { action: 'get', id: 42 },
    { action: 'update', id: 42, status: 'resolved', comment: 'User resolution text' },
    { action: 'comment', id: 42, body: 'User comment with Unicode: проверка' },
    { action: 'reopen', id: 42, body: 'User reason', comment: 'Retain both supplied fields' },
    { action: 'close', id: 42, project },
  ];
  for (const fields of actions) {
    const input = Object.freeze({ ...fields, ...(fields.labels ? { labels: Object.freeze([...fields.labels]) } : {}) });
    const before = structuredClone(input);
    const result = await handler(issueEvent(input), hostContext(sessionA));
    assertReportedInput(result, before, sessionA);
    assert.deepEqual(input, before, 'a replacement must not mutate the original host input');
  }
});

test('OMP current host session overrides conflicting reported metadata without trusting event or context aliases', async () => {
  const handler = await registeredHook();
  const input = Object.freeze({
    action: 'comment', id: 42, project, body: 'Keep the exact user comment',
    session_id: untrustedSession, client: 'claimed-other-client',
  });
  const event = {
    ...issueEvent(input), sessionId: untrustedSession, session_id: project,
    agent: 'untrusted-agent', sessionKey: 'untrusted-session-key',
  };
  const ctx = {
    ...hostContext(sessionA), sessionId: sessionB, session_id: untrustedSession,
    project, agent: 'context-agent', sessionKey: 'context-session-key',
    connection: { session_id: untrustedSession }, auth: { session_id: project },
  };
  assertReportedInput(await handler(event, ctx), input, sessionA);
  assert.equal(input.session_id, untrustedSession);
  assert.equal(input.client, 'claimed-other-client');
});

test('OMP two interleaved host sessions in one project never reuse the preceding call session', async () => {
  const handler = await registeredHook();
  let currentSession = sessionA;
  let sessionReads = 0;
  const ctx = { cwd, sessionManager: { getSessionId() { sessionReads++; return currentSession; } } };
  const previousResults = [];
  for (const [index, session] of [sessionA, sessionB, sessionA, sessionB, sessionA].entries()) {
    currentSession = session;
    const input = Object.freeze({ action: 'comment', id: 42, project, body: `Interleaved user comment ${index}` });
    const result = await handler(issueEvent(input, `interleaved-${index}`), ctx);
    assertReportedInput(result, input, session);
    previousResults.push({ result, input, session });
  }
  assert.ok(sessionReads >= previousResults.length, 'the host session must be read again for each call');
  currentSession = undefined;
  assert.equal(await handler(issueEvent({ action: 'get', id: 42, project }, 'after-session-loss'), ctx), undefined,
    'losing the native session must not reuse the last valid one');
  const inputA = Object.freeze({ action: 'comment', id: 42, project, body: 'Concurrent A' });
  const inputB = Object.freeze({ action: 'comment', id: 42, project, body: 'Concurrent B' });
  const [resultB, resultA] = await Promise.all([
    handler(issueEvent(inputB, 'concurrent-b'), hostContext(sessionB)),
    handler(issueEvent(inputA, 'concurrent-a'), hostContext(sessionA)),
  ]);
  assertReportedInput(resultA, inputA, sessionA);
  assertReportedInput(resultB, inputB, sessionB);
  for (const { result, input, session } of previousResults) assertReportedInput(result, input, session);
});

test('OMP missing or invalid native session never falls back to project, agent, auth, connection or prior metadata', async t => {
  const handler = await registeredHook();
  const invalid = [undefined, null, '', ' ', 'not-a-session-uuid', 42, {}, [],
    '0192a352-1111-7111-8111-11111111111', `${sessionA}\n`, `${sessionA}/lane`];
  const cases = [
    ['no manager', {}],
    ['no getter', { sessionManager: {} }],
    ['throwing getter', { sessionManager: { getSessionId() { throw new Error('host session unavailable'); } } }],
    ...invalid.map((value, index) => [`invalid native session ${index}`, hostContext(value)]),
  ];
  for (const [name, native] of cases) {
    await t.test(name, async () => {
      const input = Object.freeze({ action: 'comment', id: 42, project, body: 'Unchanged without native identity' });
      const event = { ...issueEvent(input), sessionId: sessionA, session_id: sessionB, project, sessionKey: sessionA };
      const ctx = {
        cwd, project, sessionId: sessionA, session_id: sessionB, agent: sessionA,
        sessionKey: sessionB, lane: sessionA, keycard: sessionB, cookie: sessionA,
        daemonSessionId: sessionA, globalSessionId: sessionB,
        auth: { session_id: sessionA, cookie: sessionB }, connection: { session_id: sessionB },
        ...native,
      };
      assert.equal(await handler(event, ctx), undefined,
        'without a valid host getter result the hook must not claim a host session or client');
      assert.equal('session_id' in input, false);
      assert.equal('client' in input, false);
    });
  }
  assert.equal(await handler(issueEvent({ action: 'get', id: 42 })), undefined,
    'an absent host context cannot establish reported host provenance');
  const reported = Object.freeze({ action: 'get', id: 42, session_id: untrustedSession, client: 'reported-manually' });
  assert.equal(await handler(issueEvent(reported), hostContext(undefined)), undefined,
    'existing caller-reported fields are not proof of a native host session');
  assert.deepEqual(reported, { action: 'get', id: 42, session_id: untrustedSession, client: 'reported-manually' });
});

test('OMP unadvertised tools, servers and name variants pass through without reading host identity', async () => {
  const handler = await registeredHook();
  const input = Object.freeze({ action: 'comment', id: 42, project, body: 'Unrelated call', session_id: untrustedSession, client: 'reported-client' });
  const ctx = { cwd, sessionManager: { getSessionId() { assert.fail('unrelated tools must not read session identity'); } } };
  for (const toolName of ['issues', 'engram_issues', 'mcp__other_issues', 'mcp__engram_recall_memory',
    'mcp__engram_issues_extra', 'mcp__engram_engram_issues', 'mcp__engram_issues_proxy', 'read']) {
    assert.equal(await handler({ type: 'tool_call', toolName, toolCallId: `unrelated-${toolName}`, input }, ctx), undefined, toolName);
  }
  assert.deepEqual(input, { action: 'comment', id: 42, project, body: 'Unrelated call', session_id: untrustedSession, client: 'reported-client' });
});

test('OMP outer write events remain untouched; the documented inner canonical issue event is stamped', async () => {
  const handler = await registeredHook();
  const args = { action: 'comment', id: 42, project, body: 'Preserve JSON body and target', session_id: untrustedSession, client: 'caller-label' };
  const content = JSON.stringify(args, null, 2);
  // xdev.ts dispatches parsed content through the canonical tool wrapper, which
  // emits a separate tool_call (wrapper.ts:184-221), not a write-shaped MCP event.
  for (const path of ['xd://mcp__engram_issues', 'xd://mcp__engram_engram_issues', 'xd://mcp__other_issues',
    'local://issue-draft.json', '/fixture/issues.json', 'agent://Main']) {
    const input = Object.freeze({ path, content });
    const result = await handler({ type: 'tool_call', toolName: 'write', toolCallId: 'outer-write', input }, hostContext(sessionA));
    assert.equal(result, undefined, `outer write must remain unchanged: ${path}`);
    assert.deepEqual(input, { path, content });
  }
  const innerInput = Object.freeze(JSON.parse(content));
  assertReportedInput(await handler(issueEvent(innerInput, 'nested-canonical-issue'), hostContext(sessionB)), args, sessionB);
  assert.equal(content, JSON.stringify(args, null, 2), 'no JSON body rewrite at the outer write layer');
});

test('OMP issue attribution requires the unique selected MCP tool to advertise the versioned string-field capability', async t => {
  const withoutMarker = advertisedIssueTool();
  delete withoutMarker.parameters['x-engram-caller-context'];
  const wrongMarker = advertisedIssueTool();
  wrongMarker.parameters['x-engram-caller-context'] = 'issues/v2';
  const nonStringMarker = advertisedIssueTool();
  nonStringMarker.parameters['x-engram-caller-context'] = { version: 'issues/v1' };
  const missingSession = advertisedIssueTool();
  delete missingSession.parameters.properties.session_id;
  const missingClient = advertisedIssueTool();
  delete missingClient.parameters.properties.client;
  const numericSession = advertisedIssueTool();
  numericSession.parameters.properties.session_id = { type: 'integer' };
  const objectClient = advertisedIssueTool();
  objectClient.parameters.properties.client = { type: 'object' };
  const foreignCollision = advertisedIssueTool();
  delete foreignCollision.parameters['x-engram-caller-context'];
  foreignCollision.description = 'Foreign tool with the same normalized public name';
  foreignCollision.sourceInfo.path = '<fixture:foreign-normalized-name-collision>';
  const differentName = advertisedIssueTool('mcp__other_issues');
  const cases = [
    ['missing getAllTools', { getAllTools: undefined }],
    ['tool metadata unavailable', { getAllTools() { throw new Error('host tool list unavailable'); } }],
    ['no matching advertisement', { getAllTools: () => [] }],
    ['marker only on another tool name', { getAllTools: () => [differentName] }],
    ['unmarked exact name beside marked other name', { getAllTools: () => [withoutMarker, differentName] }],
    ['missing marker', { getAllTools: () => [withoutMarker] }],
    ['mismatched marker version', { getAllTools: () => [wrongMarker] }],
    ['non-string marker', { getAllTools: () => [nonStringMarker] }],
    ['missing session_id property', { getAllTools: () => [missingSession] }],
    ['missing client property', { getAllTools: () => [missingClient] }],
    ['session_id is not a string property', { getAllTools: () => [numericSession] }],
    ['client is not a string property', { getAllTools: () => [objectClient] }],
    ['missing source metadata', { getAllTools: () => [{ ...advertisedIssueTool(), sourceInfo: undefined }] }],
    ...['builtin', 'sdk', 'extension'].map(source => [`non-MCP source ${source}`, {
      getAllTools: () => [{ ...advertisedIssueTool(), sourceInfo: { ...advertisedIssueTool().sourceInfo, source } }],
    }]),
    ['duplicate marked matching names', { getAllTools: () => [advertisedIssueTool(), advertisedIssueTool()] }],
    ['ambiguous marked and unmarked matching names', { getAllTools: () => [advertisedIssueTool(), foreignCollision] }],
    ['foreign normalized-name collision without marker', { getAllTools: () => [foreignCollision] }],
  ];
  for (const [name, overrides] of cases) {
    await t.test(name, async () => {
      const handler = await registeredHook(overrides);
      const input = Object.freeze({
        action: 'comment', id: 42, project, body: 'Not opted into host provenance',
        session_id: untrustedSession, client: 'caller-reported'
      });
      const before = structuredClone(input);
      assert.equal(await handler(issueEvent(input), hostContext(sessionA)), undefined,
        'name resemblance alone must never opt a call into provenance rewriting');
      assert.deepEqual(input, before, 'declining the capability must preserve the original call');
    });
  }
});

test('OMP issue hook uses the current advertisement rather than a prior normalized-name winner', async () => {
  let tools = [];
  const handler = await registeredHook({ getAllTools: () => tools });
  const input = Object.freeze({ action: 'comment', id: 42, project, body: 'Current opt-in capability only' });
  assert.equal(await handler(issueEvent(input, 'before-discovery'), hostContext(sessionA)), undefined);
  tools = [advertisedIssueTool()];
  assertReportedInput(await handler(issueEvent(input, 'marked-a'), hostContext(sessionA)), input, sessionA);
  const foreignCollision = advertisedIssueTool();
  delete foreignCollision.parameters['x-engram-caller-context'];
  foreignCollision.sourceInfo.path = '<fixture:foreign-normalized-name-collision>';
  tools = [foreignCollision];
  assert.equal(await handler(issueEvent(input, 'foreign-winner'), hostContext(sessionB)), undefined,
    'a former opt-in must not survive replacement by an unmarked tool with the same public name');
  tools = [advertisedIssueTool(), foreignCollision];
  assert.equal(await handler(issueEvent(input, 'ambiguous-current-name'), hostContext(sessionB)), undefined);
  tools = [advertisedIssueTool()];
  assertReportedInput(await handler(issueEvent(input, 'marked-b'), hostContext(sessionB)), input, sessionB);
});

test('OMP separately named marked issue instances match the selected event without global uniqueness or event mutation', async () => {
  const secondName = 'mcp__engram_secondary_issues';
  const tools = [advertisedIssueTool(secondName), advertisedIssueTool()];
  const handler = await registeredHook({ getAllTools: () => tools });
  const selections = [
    [issuesToolName, sessionA],
    [secondName, sessionB],
    [secondName, sessionA],
    [issuesToolName, sessionB],
  ];
  for (const [index, [toolName, session]] of selections.entries()) {
    const input = Object.freeze({ action: 'comment', id: 42, project, body: `Selected instance ${index}` });
    const event = Object.freeze(issueEvent(input, `selected-instance-${index}`, toolName));
    const before = structuredClone(event);
    assertReportedInput(await handler(event, hostContext(session)), input, session);
    assert.deepEqual(event, before, 'only replacement input may be returned; the raw event must not change');
  }
  tools.push(advertisedIssueTool(secondName));
  const input = Object.freeze({ action: 'get', id: 42, project });
  assertReportedInput(await handler(issueEvent(input, 'unique-first-instance'), hostContext(sessionA)), input, sessionA);
  const ambiguous = Object.freeze(issueEvent(input, 'ambiguous-second-instance', secondName));
  const before = structuredClone(ambiguous);
  assert.equal(await handler(ambiguous, hostContext(sessionB)), undefined,
    'duplicates are ambiguous only for their exact selected name, not for another marked instance');
  assert.deepEqual(ambiguous, before);
});
