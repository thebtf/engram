import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';

const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED || fileURLToPath(new URL('../dist/', import.meta.url));
const imported = await import(pathToFileURL(path.join(compiled, 'tools/engram-issues.js')));
const { createEngramIssuesTool } = imported.default ?? imported;
const { Value } = createRequire(path.join(compiled, 'tools/engram-issues.js'))('@sinclair/typebox/value');
const sourceProject = '11111111-1111-4111-8111-111111111111';
const targetProject = '22222222-2222-4222-8222-222222222222';
const participantProject = '33333333-3333-4333-8333-333333333333';
const sessionA = '44444444-4444-4444-8444-444444444444';
const sessionB = '55555555-5555-4555-8555-555555555555';
const agentId = 'issue-lifecycle-fixture';
const workspaceDir = '/synthetic/issue-lifecycle-workspace';
const sessionKey = `agent:${agentId}:main`;
const issueId = 503;
const retainedIssue = Object.freeze({
  id: issueId,
  title: 'Retained cross-project issue',
  body: 'A solution awaiting participant verification.',
  priority: 'high',
  status: 'resolved',
  source_project: sourceProject,
  target_project: targetProject,
  source_agent: agentId,
});

// Real adapter factory with the lightweight REST seam from context-recovery.test.mjs.
// Successful doubles prove adapter requests, not backend authorization or installed host behavior.
function fixture(actorProject = sourceProject) {
  const calls = [];
  const client = {
    isAvailable: () => true,
    async resolveWorkspaceProject(...args) {
      calls.push(['resolve', ...args]);
      return { projectId: actorProject };
    },
    async createIssue(input) {
      calls.push(['create', input]);
      return { id: issueId, message: 'created' };
    },
    async updateIssue(id, input) {
      calls.push(['update', id, input]);
      return { message: 'updated' };
    },
    async listIssues(input) {
      calls.push(['list', input]);
      return { issues: [retainedIssue], total: 1 };
    },
    async getIssue(id, options) {
      calls.push(options === undefined ? ['get', id] : ['get', id, options]);
      return { issue: retainedIssue, comments: [] };
    },
  };
  function toolFor(context = {}) {
    return createEngramIssuesTool({ agentId, workspaceDir, sessionKey, ...context }, client, {});
  }
  return { calls, client, toolFor };
}

function mutations(calls, method) {
  return calls.filter(call => call[0] === method);
}

test('issue lifecycle: the registered action schema exposes close and preserves existing actions', () => {
  const { toolFor } = fixture();
  const tool = toolFor({ sessionId: sessionA });
  assert.equal(tool.name, 'engram_issues');
  const actions = tool.parameters.properties.action.anyOf.map(option => option.const);
  for (const action of ['create', 'list', 'get', 'update', 'comment', 'reopen', 'close']) {
    assert.ok(actions.includes(action), `Registered engram_issues action is missing: ${action}`);
  }
});

test('issue reads: registered host schema admits list status filters', () => {
  const { toolFor } = fixture();
  const tool = toolFor({ sessionId: sessionA });
  for (const status of ['resolved', 'resolved,closed', 'open,acknowledged,reopened']) {
    const request = { action: 'list', target_project: targetProject, status };
    assert.equal(Value.Check(tool.parameters, request), true,
      `The registered host schema must admit list status=${status} before execute is called`);
  }
});

for (const [role, actorProject] of [
  ['creator', sourceProject],
  ['other authorized participant', participantProject],
]) {
  test(`issue lifecycle: close forwards exactly one closed update for the ${role} context`, async () => {
    const { calls, toolFor } = fixture(actorProject);
    const tool = toolFor({ sessionId: sessionA });
    const result = await tool.execute(`close-${role}`, {
      action: 'close',
      id: issueId,
      project: targetProject,
      source_project: targetProject,
      target_project: sourceProject,
      source_agent: 'untrusted-argument-agent',
    });

    const updates = mutations(calls, 'update');
    assert.equal(updates.length, 1, `close must reach updateIssue once, not refuse or pretend success: ${result}`);
    const [, id, input] = updates[0];
    assert.equal(id, issueId);
    assert.equal(input.status, 'closed');
    assert.equal(input.source_project, actorProject, 'The canonical caller project is not the issue source, target or a supplied override');
    assert.equal(input.source_agent, agentId, 'Keep the host agent separate from project and conversation identity');
    assert.equal(input.session_id, sessionA, 'Close records the exact host conversation separately from caller project');
    assert.equal(input.target_project, undefined, 'A lifecycle action must not retarget the issue');
    assert.equal(mutations(calls, 'create').length, 0, 'Closing updates the existing issue, never creates a replacement');
    assert.deepEqual(calls.filter(call => call[0] === 'resolve'), [['resolve', agentId, workspaceDir]]);
    assert.match(result, /closed/i);
    assert.doesNotMatch(result, /Invalid parameters|Failed to|unavailable/i);
  });
}

test('issue lifecycle: reopen keeps the existing status, body and canonical caller attribution', async () => {
  const { calls, toolFor } = fixture(participantProject);
  const result = await toolFor({ sessionId: sessionB }).execute('reopen-participant', {
    action: 'reopen',
    id: issueId,
    body: 'Verification found the original failure again.',
    source_project: sourceProject,
    target_project: targetProject,
    source_agent: 'untrusted-argument-agent',
  });
  const updates = mutations(calls, 'update');
  assert.equal(updates.length, 1);
  const [, id, input] = updates[0];
  assert.equal(id, issueId);
  assert.equal(input.status, 'reopened');
  assert.equal(input.comment, 'Verification found the original failure again.');
  assert.equal(input.source_project, participantProject);
  assert.equal(input.source_agent, agentId);
  assert.equal(input.session_id, sessionB, 'Reopen records the exact host conversation separately from caller project');
  assert.equal(input.target_project, undefined);
  assert.equal(mutations(calls, 'create').length, 0);
  assert.match(result, /reopened/i);
});

test('issue lifecycle: reopen retains the default comment when no body is supplied', async () => {
  const { calls, toolFor } = fixture();
  await toolFor().execute('reopen-default', { action: 'reopen', id: issueId });
  const updates = mutations(calls, 'update');
  assert.equal(updates.length, 1);
  assert.equal(updates[0][2].status, 'reopened');
  assert.equal(updates[0][2].comment, 'Reopened');
  assert.equal(updates[0][2].session_id, undefined, 'Reopen without host sessionId must not use sessionKey');
});

test('issue session: distinct conversation factories preserve their own actual sessionId under the same agent and project', async () => {
  const { calls, toolFor } = fixture();
  const first = toolFor({ sessionId: sessionA });
  const second = toolFor({ sessionId: sessionB });
  const params = {
    action: 'create',
    title: 'Conversation-attributed issue',
    body: 'Keep attribution distinct from routing.',
    priority: 'high',
    labels: ['bug'],
    target_project: targetProject,
    source_project: participantProject,
    source_agent: 'untrusted-argument-agent',
    created_by_session: participantProject,
    sessionId: participantProject,
  };
  for (const [tool, callId] of [[first, 'create-a'], [second, 'create-b'], [first, 'create-a-again']]) {
    const result = await tool.execute(callId, params);
    assert.match(result, /Issue created/);
  }

  const creates = mutations(calls, 'create');
  assert.equal(creates.length, 3);
  assert.deepEqual(creates.map(([, input]) => input.created_by_session), [sessionA, sessionB, sessionA],
    'Use each factory host sessionId, not sessionKey, tool-call ID, cached session, project or caller-supplied attribution');
  for (const [, input] of creates) {
    assert.equal(input.source_project, sourceProject);
    assert.equal(input.target_project, targetProject);
    assert.equal(input.source_agent, agentId);
    assert.equal(input.title, params.title);
    assert.equal(input.body, params.body);
    assert.equal(input.priority, params.priority);
    assert.deepEqual(input.labels, params.labels);
  }
  assert.equal(mutations(calls, 'update').length, 0);
});

test('issue session: absent sessionId does not become sessionKey, project or supplied session attribution', async () => {
  const { calls, toolFor } = fixture();
  const result = await toolFor().execute('create-without-host-session', {
    action: 'create',
    title: 'No host conversation ID was supplied',
    created_by_session: sessionB,
    target_project: targetProject,
  });
  const creates = mutations(calls, 'create');
  assert.equal(creates.length, 1);
  const input = creates[0][1];
  assert.equal(input.created_by_session, undefined, 'Missing host session attribution stays absent');
  assert.equal(input.source_project, sourceProject);
  assert.equal(input.target_project, targetProject);
  assert.equal(input.source_agent, agentId);
  assert.match(result, /Issue created/);
});

for (const [action, params, expected] of [
  ['comment', { body: 'Participant verification feedback.' }, { comment: 'Participant verification feedback.' }],
  ['update', { status: 'resolved', comment: 'The solution awaits verification.' }, { status: 'resolved', comment: 'The solution awaits verification.' }],
  ['close', {}, { status: 'closed' }],
  ['reopen', { body: 'The failure is reproducible again.' }, { status: 'reopened', comment: 'The failure is reproducible again.' }],
]) {
  test(`issue session: ${action} forwards the exact per-factory host session_id on each updateIssue call`, async () => {
    const { calls, toolFor } = fixture(participantProject);
    const first = toolFor({ sessionId: sessionA });
    const second = toolFor({ sessionId: sessionB });
    const request = {
      action, id: issueId, ...params,
      source_project: sourceProject,
      target_project: targetProject,
      source_agent: 'untrusted-argument-agent',
      session_id: participantProject,
      sessionId: targetProject,
      created_by_session: sourceProject,
    };
    for (const [tool, callId] of [[first, `${action}-a`], [second, `${action}-b`], [first, `${action}-a-again`]]) {
      await tool.execute(callId, request);
    }

    const updates = mutations(calls, 'update');
    assert.equal(updates.length, 3, 'Each lifecycle invocation must update the existing issue exactly once');
    assert.deepEqual(updates.map(([, , input]) => input.session_id), [sessionA, sessionB, sessionA],
      'Use the factory host sessionId, never supplied attribution, sessionKey, project or a different conversation');
    for (const [, id, input] of updates) {
      assert.equal(id, issueId);
      assert.equal(input.source_project, participantProject);
      assert.equal(input.source_agent, agentId);
      assert.equal(input.target_project, undefined);
      assert.equal(input.status, expected.status);
      if (expected.comment !== undefined) assert.equal(input.comment, expected.comment);
    }
    assert.equal(mutations(calls, 'create').length, 0);
  });

  test(`issue session: ${action} without host sessionId supplies no session_id despite sessionKey and supplied attribution`, async () => {
    const { calls, toolFor } = fixture(participantProject);
    await toolFor().execute(`${action}-without-host-session`, {
      action, id: issueId, ...params,
      session_id: sessionB,
      sessionId: sessionA,
      created_by_session: sourceProject,
    });

    const updates = mutations(calls, 'update');
    assert.equal(updates.length, 1);
    const [, id, input] = updates[0];
    assert.equal(id, issueId);
    assert.equal(input.session_id, undefined, 'Missing host conversation identity stays absent, never a sessionKey or project fallback');
    assert.equal(input.source_project, participantProject);
    assert.equal(input.source_agent, agentId);
    assert.equal(input.status, expected.status);
    if (expected.comment !== undefined) assert.equal(input.comment, expected.comment);
    assert.equal(mutations(calls, 'create').length, 0);
  });
}

test('issue reads: get remains a project-free read of the retained issue', async () => {
  const { calls, client, toolFor } = fixture();
  client.resolveWorkspaceProject = async () => { assert.fail('get must not resolve a workspace project'); };
  const result = await toolFor().execute('read-issue', { action: 'get', id: issueId });
  assert.deepEqual(calls, [['get', issueId]]);
  assert.match(result, /Retained cross-project issue/);
  assert.match(result, /resolved/);
  assert.match(result, new RegExp(sourceProject));
  assert.match(result, new RegExp(targetProject));
});

test('issue reads: default inbox retains canonical scope and active backlog statuses', async () => {
  const { calls, toolFor } = fixture();
  const result = await toolFor().execute('default-inbox', { action: 'list' });
  assert.deepEqual(calls, [
    ['resolve', agentId, workspaceDir],
    ['list', { project: sourceProject, status: 'open,acknowledged,reopened', limit: 20 }],
  ]);
  assert.match(result, /Retained cross-project issue/);
});

for (const filter of [
  { target_project: targetProject },
  { source_project: sourceProject },
]) {
  test(`issue reads: explicit ${Object.keys(filter)[0]} remains project-free with the requested filters`, async () => {
    const { calls, client, toolFor } = fixture();
    client.resolveWorkspaceProject = async () => { assert.fail('Explicit issue filters must not resolve or replace caller identity'); };
    const result = await toolFor().execute('filtered-inbox', {
      action: 'list', ...filter, status: 'resolved,closed', limit: 7,
    });
    assert.deepEqual(calls, [['list', {
      ...(filter.source_project ? { source_project: sourceProject } : { project: targetProject }),
      status: 'resolved,closed',
      limit: 7,
    }]]);
    assert.match(result, /Retained cross-project issue/);
  });
}

test('issue routing: registered optional recipient, session filters and history cursor are available to consumers', () => {
  const { toolFor } = fixture();
  const { properties, required = [] } = toolFor().parameters;
  for (const name of ['target_session', 'source_session', 'participant_session', 'history_limit', 'history_after_id']) {
    assert.ok(Object.hasOwn(properties, name), `Registered issue schema must expose ${name}`);
    assert.ok(!required.includes(name), `${name} must remain optional`);
  }
});

test('issue routing: create forwards the explicit recipient separately from the creator host session', async () => {
  const { calls, toolFor } = fixture();
  const result = await toolFor({ sessionId: sessionA }).execute('create-recipient', {
    action: 'create', title: 'An issue for another conversation',
    target_project: targetProject, target_session: sessionB,
  });
  const creates = mutations(calls, 'create');
  assert.equal(creates.length, 1);
  const input = creates[0][1];
  assert.equal(input.target_session, sessionB);
  assert.equal(input.created_by_session, sessionA);
  assert.equal(input.source_project, sourceProject);
  assert.equal(input.target_project, targetProject);
  assert.equal(input.source_agent, agentId);
  assert.match(result, /Issue created/);
});

test('issue routing: create without a recipient does not infer one from the host session or project', async () => {
  const { calls, toolFor } = fixture();
  await toolFor({ sessionId: sessionA }).execute('create-no-recipient', {
    action: 'create', title: 'A project-wide issue', target_project: targetProject,
  });
  const creates = mutations(calls, 'create');
  assert.equal(creates.length, 1);
  assert.equal(creates[0][1].target_session, undefined);
  assert.equal(creates[0][1].created_by_session, sessionA);
});

for (const [label, recipient] of [
  ['assigns', sessionB],
  ['assigns an opaque conversation reference', 'openclaw-conversation/recipient-02'],
  ['clears with empty string', ''],
  ['clears with null', null],
]) {
  test(`issue routing: creator recipient-only update ${label} target_session without inventing a status change`, async () => {
    const { calls, toolFor } = fixture();
    const result = await toolFor({ sessionId: sessionA }).execute(`recipient-${label}`, {
      action: 'update', id: issueId, target_session: recipient,
    });
    const updates = mutations(calls, 'update');
    assert.equal(updates.length, 1, `Recipient-only update must reach server policy: ${result}`);
    const [, id, input] = updates[0];
    assert.equal(id, issueId);
    assert.equal(input.target_session, recipient, 'Preserve explicit empty-string/null clearing separately from omission');
    assert.equal(input.status, undefined, 'Recipient routing alone must not resolve, close or reopen an issue');
    assert.equal(input.comment, undefined);
    assert.equal(input.source_project, sourceProject);
    assert.equal(input.source_agent, agentId);
    assert.equal(input.session_id, sessionA);
    assert.equal(input.target_project, undefined);
    assert.equal(mutations(calls, 'create').length, 0);
    assert.doesNotMatch(result, /Invalid parameters|Failed to|unavailable/i);
    assert.doesNotMatch(result, /Issue resolved|Issue closed|Issue reopened/i);
  });
}

test('issue routing: ordinary status update omits target_session instead of clearing or borrowing host identity', async () => {
  const { calls, toolFor } = fixture();
  await toolFor({ sessionId: sessionA }).execute('status-without-recipient', {
    action: 'update', id: issueId, status: 'resolved',
  });
  const updates = mutations(calls, 'update');
  assert.equal(updates.length, 1);
  assert.equal(updates[0][2].status, 'resolved');
  assert.equal(updates[0][2].target_session, undefined);
  assert.equal(updates[0][2].session_id, sessionA);
});

test('issue routing: backend refusal of recipient-only update is not relabelled successful', async () => {
  const { calls, client, toolFor } = fixture(participantProject);
  client.updateIssue = async (id, input) => { calls.push(['update', id, input]); return null; };
  const result = await toolFor({ sessionId: sessionA }).execute('recipient-server-refusal', {
    action: 'update', id: issueId, target_session: sessionB,
  });
  const updates = mutations(calls, 'update');
  assert.equal(updates.length, 1, 'The adapter delegates recipient permission to the backend, not a guessed creator rule');
  assert.equal(updates[0][2].target_session, sessionB);
  assert.equal(updates[0][2].source_project, participantProject);
  assert.equal(updates[0][2].session_id, sessionA);
  assert.match(result, /Failed to update issue/i);
});

for (const [label, input] of [
  ['no changed field', {}],
  ['unknown field only', { recipient: sessionB }],
  ['nil UUID recipient', { status: 'resolved', target_session: '00000000-0000-0000-0000-000000000000' }],
  ['newline in recipient', { status: 'resolved', target_session: 'unsafe\nrecipient' }],
  ['NUL in recipient', { status: 'resolved', target_session: 'unsafe\u0000recipient' }],
  ['DEL in recipient', { status: 'resolved', target_session: 'unsafe\u007frecipient' }],
  ['oversized recipient', { status: 'resolved', target_session: 's'.repeat(16384) }],
  ['numeric recipient', { status: 'resolved', target_session: 7 }],
  ['invalid status', { status: 'not-a-status' }],
]) {
  test(`issue routing: ${label} cannot become a successful no-op update`, async () => {
    const { calls, toolFor } = fixture();
    const result = await toolFor({ sessionId: sessionA }).execute('invalid-recipient-update', {
      action: 'update', id: issueId, ...input,
    });
    assert.equal(mutations(calls, 'update').length, 0, 'Malformed or empty updates must not reach REST');
    assert.match(result, /Invalid parameters/i);
  });
}

for (const filter of [
  { source_session: sessionA },
  { target_session: sessionB },
  { participant_session: sessionA },
  { source_session: sessionA, target_session: sessionB, participant_session: sessionA },
]) {
  test(`issue reads: session-only ${Object.keys(filter).join('+')} filtering preserves the canonical default inbox`, async () => {
    const { calls, toolFor } = fixture();
    const result = await toolFor({ sessionId: sessionB }).execute('session-inbox', {
      action: 'list', ...filter,
    });
    assert.deepEqual(calls, [
      ['resolve', agentId, workspaceDir],
      ['list', { project: sourceProject, status: 'open,acknowledged,reopened', limit: 20, ...filter }],
    ]);
    assert.match(result, /Retained cross-project issue/);
  });
}

test('issue reads: explicit project and session filters are forwarded together without resolving another caller', async () => {
  const { calls, client, toolFor } = fixture();
  client.resolveWorkspaceProject = async () => { assert.fail('Explicit project filter remains a project-free read'); };
  await toolFor({ sessionId: sessionA }).execute('explicit-session-inbox', {
    action: 'list', source_project: sourceProject,
    source_session: sessionA, target_session: sessionB, participant_session: sessionA,
    status: 'closed', limit: 3,
  });
  assert.deepEqual(calls, [['list', {
    source_project: sourceProject, source_session: sessionA,
    target_session: sessionB, participant_session: sessionA,
    status: 'closed', limit: 3,
  }]]);
});

test('issue history: get exposes the exact next bounded call and fetches only on consumer continuation', async () => {
  const { calls, client, toolFor } = fixture();
  client.resolveWorkspaceProject = async () => { assert.fail('History inspection remains project-free'); };
  client.getIssue = async (id, options) => {
    calls.push(['get', id, options]);
    const firstPage = options?.history_after_id === 41;
    return {
      issue: retainedIssue, comments: [],
      history_events: [{ id: firstPage ? 44 : 47, issue_id: issueId, status: firstPage ? 'resolved' : 'closed' }],
      history_has_more: firstPage,
      history_next_after_id: firstPage ? 44 : null,
    };
  };
  const tool = toolFor({ sessionId: sessionA });
  const first = await tool.execute('history-page-one', { action: 'get', id: issueId, history_limit: 3, history_after_id: 41 });
  assert.deepEqual(calls, [['get', issueId, { history_limit: 3, history_after_id: 41 }]],
    'One explicit get must not drain history pages automatically');
  assert.match(first, /history_events/);
  assert.match(first, /\bget\b/, 'Expose an executable next get call, not an instruction to fetch everything');
  assert.match(first, /\bid["']?\s*[:=]\s*["']?503\b/);
  assert.match(first, /history_limit["']?\s*[:=]\s*["']?3\b/);
  assert.match(first, /history_after_id["']?\s*[:=]\s*["']?44\b/, 'Use the server cursor exactly, not cursor+1 or the previous request');
  await tool.execute('history-page-two', { action: 'get', id: issueId, history_limit: 3, history_after_id: 44 });
  assert.deepEqual(calls, [
    ['get', issueId, { history_limit: 3, history_after_id: 41 }],
    ['get', issueId, { history_limit: 3, history_after_id: 44 }],
  ], 'Only the explicit continuation advances the caller cursor');
});

test('issue history: absent pagination metadata from an older server stays unknown rather than complete', async () => {
  const { calls, client, toolFor } = fixture();
  client.getIssue = async (id, options) => {
    calls.push(['get', id, options]);
    return { issue: retainedIssue, comments: [], history_events: [] };
  };
  const result = await toolFor().execute('legacy-history-metadata', { action: 'get', id: issueId, history_limit: 3, history_after_id: 0 });
  assert.deepEqual(calls, [['get', issueId, { history_limit: 3, history_after_id: 0 }]]);
  assert.match(result, /unknown/i, 'No history_events rows do not prove the history is complete');
  assert.doesNotMatch(result, /history_has_more["']?\s*[:=]\s*false/);
});

for (const [label, cursor] of [['missing', undefined], ['null', null], ['negative', -1], ['fractional', 4.5], ['string', '44']]) {
  test(`issue history: has_more with ${label} next cursor reports continuation unavailable without another fetch`, async () => {
    const { calls, client, toolFor } = fixture();
    client.getIssue = async (id, options) => {
      calls.push(['get', id, options]);
      return {
        issue: retainedIssue, comments: [], history_events: [],
        history_has_more: true,
        ...(cursor === undefined ? {} : { history_next_after_id: cursor }),
      };
    };
    const result = await toolFor().execute('unavailable-history-continuation', {
      action: 'get', id: issueId, history_limit: 3, history_after_id: 0,
    });
    assert.deepEqual(calls, [['get', issueId, { history_limit: 3, history_after_id: 0 }]]);
    assert.match(result, /continuation[^\n]*unavailable|unavailable[^\n]*continuation/i);
    assert.doesNotMatch(result, /history_after_id["']?\s*[:=]/, 'Do not manufacture an executable next cursor');
  });
}

for (const [label, options] of [
  ['minimum page and zero exclusive cursor', { history_limit: 1, history_after_id: 0 }],
  ['maximum page', { history_limit: 100, history_after_id: 41 }],
]) {
  test(`issue history: ${label} is forwarded without clamping or incrementing`, async () => {
    const { calls, toolFor } = fixture();
    await toolFor().execute('history-bounds', { action: 'get', id: issueId, ...options });
    assert.deepEqual(calls, [['get', issueId, options]]);
  });
}

for (const [label, options] of [
  ['zero history_limit', { history_limit: 0 }],
  ['oversize history_limit', { history_limit: 101 }],
  ['fractional history_limit', { history_limit: 1.5 }],
  ['negative history_after_id', { history_after_id: -1 }],
  ['fractional history_after_id', { history_after_id: 1.5 }],
]) {
  test(`issue history: invalid ${label} is rejected before fetching history`, async () => {
    const { calls, toolFor } = fixture();
    const result = await toolFor().execute('invalid-history-bounds', { action: 'get', id: issueId, ...options });
    assert.deepEqual(calls, []);
    assert.match(result, /Invalid parameters/i);
  });
}
