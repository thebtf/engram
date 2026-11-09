import assert from 'node:assert/strict';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import test from 'node:test';
import { Value } from '@sinclair/typebox/value';

const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED;
const load = file => import(compiled ? pathToFileURL(path.join(compiled, file)).href : new URL(`../dist/${file}`, import.meta.url));
const { EngramRestClient } = await load('client.js');
const { createEngramIssuesTool, formatIssueListRecord, formatIssueDetailRecord } = await load('tools/engram-issues.js');

const origin = 'http://127.0.0.1:9';
const source = '11111111-1111-4111-8111-111111111111';
const target = '22222222-2222-4222-8222-222222222222';
const actor = { kind: 'operator', reference: 'operator:auth-disabled' };
const context = { agentId: 'synthetic-513-agent', sessionId: 'synthetic-513-session', workspaceDir: '/synthetic/513-workspace' };
const created = { id: 701, revision: '1', replayed: false, message: 'issue created' };
const updated = { id: 701, revision: '2', changed: true, comment_id: null, message: 'issue updated' };
const createInput = { action: 'create', title: 'Synthetic resumable task', target_project: target };
const metadataLimit = 1024 * 1024;

function issue(id = 701) {
  return {
    id, revision: '2', title: `Synthetic task ${id}`, body: 'Synthetic task detail',
    status: 'open', priority: 'medium', type: 'task', source_project: source,
    target_project: target, source_agent: context.agentId, labels: ['internal'],
    owner_label: null, recipient_label: null, metadata: { acceptance: ['resume later'] },
    actor, comment_count: 0, created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-02T00:00:00Z',
  };
}

function detail(overrides = {}) {
  const detailIssue = issue();
  delete detailIssue.comment_count;
  return {
    issue: detailIssue, comments: [], history: [], comment_count: 0, history_count: 0,
    comments_offset: 0, comments_limit: 50, history_offset: 0, history_limit: 50,
    comments_next_offset: null, history_next_offset: null,
    comments_complete: true, history_complete: true,
    comments_oversized: false, history_oversized: false,
    ...overrides,
  };
}

function comment(id, body = `Synthetic comment ${id}`) {
  return { id, issue_id: 701, author_project: source, author_agent: context.agentId, body, metadata: {}, actor, created_at: '2026-10-02T00:00:00Z' };
}

function history(revision, changes = {}) {
  return {
    revision: String(revision), previous_revision: revision === 1 ? null : String(revision - 1),
    action: 'update', changed_fields: Object.keys(changes), actor,
    attribution: { source_project: source, source_agent: context.agentId, session: context.sessionId },
    created_at: '2026-10-02T00:00:00Z', changes,
  };
}

function fixture(t, responses = [], options = {}) {
  const requests = [];
  const diagnostics = [];
  t.mock.method(console, 'error', (...args) => diagnostics.push(args));
  t.mock.method(globalThis, 'fetch', async (url, init = {}) => {
    const request = {
      url: new URL(String(url)), method: init.method ?? 'GET', headers: new Headers(init.headers),
      rawBody: init.body, body: init.body === undefined ? undefined : JSON.parse(init.body),
    };
    requests.push(request);
    // This replaces the network edge only; responses are not backend/idempotency/auth proof.
    const response = typeof responses === 'function' ? responses(request, requests.length - 1) : responses[requests.length - 1];
    const { status = 200, body = {} } = response ?? { status: 599, body: { error: { code: 'UNPLANNED_FIXTURE_REQUEST', message: 'Unexpected synthetic request' } } };
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  });
  t.after(() => {
    for (const request of requests) assert.equal(request.url.origin, origin, 'Only the synthetic fetch endpoint is allowed');
  });
  const config = { url: origin, token: 'synthetic-513-token', timeoutMs: 1000, ...(options.projectFree ? {} : { project: source }) };
  const client = new EngramRestClient(config);
  const tool = createEngramIssuesTool(options.context ?? context, client, config);
  return { requests, diagnostics, client, tool };
}

function jsonResult(text) {
  let value;
  assert.doesNotThrow(() => { value = JSON.parse(text); }, 'format=json must return one serialized JSON result, not legacy prose');
  return value;
}

async function executeJson(tool, params) {
  return jsonResult(await tool.execute('synthetic-513-call', { ...params, format: 'json' }));
}

function firstRequest(f, method, pathname) {
  assert.equal(f.requests.length, 1, 'One explicit tool invocation must make one issue request');
  const request = f.requests[0];
  assert.equal(request.method, method);
  assert.equal(request.url.pathname, pathname);
  return request;
}

test('issues JSON create and replay preserve server identity, revision and replay state with the original intent', async t => {
  const replay = { ...created, revision: '3', replayed: true, message: 'issue replayed' };
  const f = fixture(t, [{ status: 201, body: created }, { body: replay }]);
  const pending = { ...createInput, request_key: 'synthetic-intent-513', target_session: 'recipient & 日本語', created_by_session: 'synthetic-explicit-creator', session_id: 'synthetic-forged-session', client: 'synthetic-forged-client', metadata: { acceptance: ['same ID after retry'] } };
  const original = structuredClone(pending);
  const firstText = await f.tool.execute('first', { ...pending, format: 'json' });
  assert.equal(f.requests[0].body.request_key, pending.request_key, 'The actual HTTP create must retain the pending key');
  assert.equal(f.requests[0].body.target_session, original.target_session);
  assert.equal(f.requests[0].body.source_project, source);
  assert.equal(f.requests[0].body.source_agent, context.agentId);
  assert.equal(f.requests[0].headers.get('authorization'), 'Bearer synthetic-513-token');
  assert.deepEqual(f.requests[0].body.metadata, pending.metadata);
  assert.equal(f.requests[0].body.session_id, context.sessionId);
  assert.equal(f.requests[0].body.client, 'openclaw');
  assert.equal(f.requests[0].body.created_by_session, pending.created_by_session);
  assert.deepEqual(jsonResult(firstText), created);
  const retryText = await f.tool.execute('retry', { ...pending, format: 'json' });
  assert.equal(f.requests.length, 2, 'No implicit extra create is allowed');
  assert.deepEqual(f.requests[1].body, f.requests[0].body, 'A retry must not rewrite the original intent or key');
  assert.deepEqual(pending, original, 'A retry must not mutate caller-owned intent');
  assert.deepEqual(jsonResult(retryText), replay);
});

test('issues default text identifies a replay instead of reporting a new creation', async t => {
  const f = fixture(t, [{ body: { ...created, replayed: true, message: 'issue replayed' } }]);
  const text = await f.tool.execute('replay-text', { ...createInput, request_key: 'synthetic-replay' });
  firstRequest(f, 'POST', '/api/issues');
  assert.match(text, /replay/i);
  assert.match(text, /701/);
});

test('issues unkeyed create stays single-shot and does not invent a request key', async t => {
  const f = fixture(t, [{ status: 201, body: created }], { context: { ...context, sessionId: undefined, sessionKey: 'synthetic-not-a-session' } });
  const result = await f.tool.execute('unkeyed', { ...createInput, session_id: 'synthetic-forged-session', client: 'synthetic-forged-client', format: 'json' });
  const request = firstRequest(f, 'POST', '/api/issues');
  assert.equal(Object.hasOwn(request.body, 'request_key'), false);
  assert.equal(Object.hasOwn(request.body, 'session_id'), false, 'Never substitute sessionKey, project or installation for an absent host session');
  assert.equal(Object.hasOwn(request.body, 'created_by_session'), false);
  assert.equal(request.body.client, 'openclaw');
  assert.equal(jsonResult(result).replayed, false);
});

const errors = [
  [400, 'INVALID_ARGUMENT'], [413, 'PAYLOAD_TOO_LARGE'], [401, 'UNAUTHENTICATED'],
  [403, 'FORBIDDEN'], [404, 'ISSUE_NOT_FOUND'], [409, 'IDEMPOTENCY_CONFLICT'],
  [409, 'REVISION_CONFLICT'], [409, 'INVALID_TRANSITION'], [428, 'REVISION_REQUIRED'],
  [410, 'ISSUE_GONE'], [409, 'DETAIL_PAGING_REQUIRED'], [400, 'PRIVACY_REJECTED'],
  [413, 'TRANSPORT_RECORD_TOO_LARGE'], [503, 'UNAVAILABLE'],
  [400, 'PROJECT_IDENTITY_INVALID'], [409, 'PROJECT_MIGRATION_REQUIRED'],
];
for (const [status, code] of errors) {
  test(`issues real HTTP ${status} preserves ${code} without a fallback write`, async t => {
    const error = { error: { code, message: 'Synthetic refusal; reconcile before another submission', ...(code === 'REVISION_CONFLICT' ? { current_revision: '7' } : {}) } };
    const f = fixture(t, [{ status, body: error }]);
    const text = await f.tool.execute('refused', { ...createInput, request_key: 'synthetic-refused-intent', format: 'json' });
    firstRequest(f, 'POST', '/api/issues');
    assert.deepEqual(jsonResult(text), error, 'A typed HTTP refusal must not become null, success or generic prose');
  });
}

for (const [code, status, recovery] of [
  ['REVISION_CONFLICT', 409, /read|reconcil/i],
  ['REVISION_REQUIRED', 428, /revision|read/i],
  ['DETAIL_PAGING_REQUIRED', 409, /detail_mode[\s:=\"']+paged/],
  ['TRANSPORT_RECORD_TOO_LARGE', 413, /REST/],
]) {
  test(`issues text preserves ${code} and its recovery boundary`, async t => {
    const guidance = code === 'DETAIL_PAGING_REQUIRED' ? 'Use detail_mode=paged with comments_offset and history_offset'
      : code === 'TRANSPORT_RECORD_TOO_LARGE' ? 'Use authorized REST detail access'
        : 'Read current revision and explicitly reconcile the retained draft';
    const f = fixture(t, [{ status, body: { error: { code, message: guidance } } }]);
    const text = await f.tool.execute('text-refusal', { action: 'get', id: 701 });
    firstRequest(f, 'GET', '/api/issues/701');
    assert.ok(text.includes(code), 'Text must expose the stable error code');
    assert.match(text, recovery);
  });
}

test('issues transport refusal supplies its own REST recovery action when the server gives no instruction', async t => {
  const code = 'TRANSPORT_RECORD_TOO_LARGE';
  const f = fixture(t, [{ status: 413, body: { error: { code, message: 'Record exceeds transport capacity' } } }]);
  const text = await f.tool.execute('own-recovery', { action: 'get', id: 701 });
  firstRequest(f, 'GET', '/api/issues/701');
  assert.ok(text.includes(code), 'Text must expose the stable transport error code');
  assert.match(text, /\b(?:use|read|retrieve|access|request|fetch|open)\b[\s\S]*\bREST\b|\bREST\b[\s\S]*\b(?:use|read|retrieve|access|request|fetch|open)\b/i,
    'The adapter must provide a REST recovery action, not merely repeat the code and instruction-free server message');
});

test('issues direct client preserves a typed HTTP refusal rather than returning null', async t => {
  const error = { error: { code: 'REVISION_CONFLICT', message: 'Read and reconcile', current_revision: '9' } };
  const f = fixture(t, [{ status: 409, body: error }]);
  const result = await f.client.updateIssue(701, { expected_revision: '2', body: 'Synthetic losing draft' });
  firstRequest(f, 'PATCH', '/api/issues/701');
  assert.deepEqual(result, error);
});

test('issues a later denied read cannot reuse a previous successful detail', async t => {
  const denied = { error: { code: 'ISSUE_NOT_FOUND', message: 'Issue unavailable' } };
  const f = fixture(t, [{ body: detail() }, { status: 404, body: denied }]);
  const first = await f.tool.execute('readable', { action: 'get', id: 701, format: 'json' });
  assert.deepEqual(jsonResult(first).issue.metadata, { acceptance: ['resume later'] });
  const second = await f.tool.execute('unreadable', { action: 'get', id: 702, format: 'json' });
  assert.equal(f.requests.length, 2);
  assert.deepEqual(jsonResult(second), denied);
  assert.equal(second.includes('resume later'), false);
});

test('issues admitted metadata is wire data and cannot override source, target or credentials', async t => {
  const metadata = {
    acceptance: ['synthetic check'], outcome: { state: 'pending', ratio: 0.1, zero: 0, max: Number.MAX_SAFE_INTEGER, min: -Number.MAX_SAFE_INTEGER },
    unknown: [true, false, null, '日本語'], action: 'close', target_project: source,
    actor: { kind: 'operator', id: 'not-an-authority-grant' },
  };
  const f = fixture(t, [{ status: 201, body: created }]);
  const text = await f.tool.execute('metadata', { ...createInput, metadata, type: 'task', owner_label: 'docs', recipient_label: 'next-agent', format: 'json', project: target, source_project: target });
  const request = firstRequest(f, 'POST', '/api/issues');
  assert.deepEqual(request.body.metadata, metadata);
  assert.equal(request.body.source_project, source);
  assert.equal(request.body.target_project, target);
  assert.equal(request.body.source_agent, context.agentId);
  assert.equal(request.body.type, 'task');
  assert.equal(request.body.owner_label, 'docs');
  assert.equal(request.body.recipient_label, 'next-agent');
  assert.equal(request.headers.get('authorization'), 'Bearer synthetic-513-token');
  assert.deepEqual(jsonResult(text), created);
});

function nestedMetadata(containers) {
  let value = {};
  for (let i = 1; i < containers; i++) value = { nested: value };
  return value;
}

const invalidMetadata = [
  ['null root', null, 'INVALID_ARGUMENT'], ['array root', [], 'INVALID_ARGUMENT'],
  ['scalar root', 'metadata', 'INVALID_ARGUMENT'], ['NaN', { n: NaN }, 'INVALID_ARGUMENT'],
  ['positive infinity', { n: Infinity }, 'INVALID_ARGUMENT'], ['negative infinity', { n: -Infinity }, 'INVALID_ARGUMENT'],
  ['unsafe integer', { n: 2 ** 53 }, 'INVALID_ARGUMENT'], ['nested unsafe integer', { child: [-(2 ** 53)] }, 'INVALID_ARGUMENT'],
  ['257 containers', nestedMetadata(257), 'PAYLOAD_TOO_LARGE'],
];
for (const [name, metadata, code] of invalidMetadata) {
  test(`issues rejects metadata ${name} before sending a mutation`, async t => {
    const f = fixture(t);
    const text = await f.tool.execute('invalid-metadata', { ...createInput, metadata, format: 'json' });
    assert.equal(f.requests.length, 0, 'Invalid metadata must not be dropped and sent as a different create');
    assert.equal(jsonResult(text).error.code, code);
  });
}

for (const extra of [0, 1]) {
  test(`issues metadata ${extra === 0 ? 'at' : 'above'} the 1 MiB compact UTF-8 boundary`, async t => {
    const metadata = { padding: 'x'.repeat(metadataLimit - Buffer.byteLength(JSON.stringify({ padding: '' }), 'utf8') + extra) };
    assert.equal(Buffer.byteLength(JSON.stringify(metadata), 'utf8'), metadataLimit + extra);
    const f = fixture(t, extra === 0 ? [{ status: 201, body: created }] : []);
    const text = await f.tool.execute('metadata-bytes', { ...createInput, metadata, format: 'json' });
    if (extra === 0) {
      assert.deepEqual(firstRequest(f, 'POST', '/api/issues').body.metadata, metadata, 'Admitted metadata must not be truncated');
      assert.deepEqual(jsonResult(text), created);
    } else {
      assert.equal(f.requests.length, 0);
      assert.equal(jsonResult(text).error.code, 'PAYLOAD_TOO_LARGE');
    }
  });
}

test('issues admits 256 metadata containers without silently dropping nested data', async t => {
  const metadata = nestedMetadata(256);
  const f = fixture(t, [{ status: 201, body: created }]);
  const text = await f.tool.execute('metadata-depth', { ...createInput, metadata, format: 'json' });
  assert.deepEqual(firstRequest(f, 'POST', '/api/issues').body.metadata, metadata);
  assert.deepEqual(jsonResult(text), created);
});

for (const [name, request_key, code] of [
  ['empty', '', 'INVALID_ARGUMENT'], ['NUL', 'intent\u0000suffix', 'INVALID_ARGUMENT'],
  ['lone high surrogate', 'intent\uD800suffix', 'INVALID_ARGUMENT'],
  ['lone low surrogate', 'intent\uDC00suffix', 'INVALID_ARGUMENT'],
  ['513 ASCII bytes', 'a'.repeat(513), 'PAYLOAD_TOO_LARGE'],
  ['513 UTF-8 bytes', `${'é'.repeat(256)}a`, 'PAYLOAD_TOO_LARGE'],
]) {
  test(`issues rejects a request key with ${name} without an HTTP create`, async t => {
    const f = fixture(t);
    const text = await f.tool.execute('invalid-key', { ...createInput, request_key, format: 'json' });
    assert.equal(f.requests.length, 0, 'The key must not be stripped or replaced by an unkeyed create');
    assert.equal(jsonResult(text).error.code, code);
  });
}

for (const request_key of ['a'.repeat(512), 'é'.repeat(256)]) {
  test(`issues preserves an opaque 512-byte ${request_key[0] === 'a' ? 'ASCII' : 'Unicode'} key`, async t => {
    const f = fixture(t, [{ status: 201, body: created }]);
    const text = await f.tool.execute('valid-key', { ...createInput, request_key, format: 'json' });
    assert.equal(Buffer.byteLength(request_key, 'utf8'), 512);
    assert.equal(firstRequest(f, 'POST', '/api/issues').body.request_key, request_key);
    assert.deepEqual(jsonResult(text), created);
  });
}

test('issues validates only the available parsed numeric value, not an unrecoverable earlier spelling', async t => {
  const metadata = JSON.parse('{"fraction":0.10000000000000001,"integer":9007199254740991,"duplicate":"first","duplicate":"last"}');
  const f = fixture(t, [{ status: 201, body: created }]);
  const text = await f.tool.execute('already-parsed', { ...createInput, metadata, format: 'json' });
  const request = firstRequest(f, 'POST', '/api/issues');
  // The pre-parse duplicate and fraction spelling are gone; this is not raw-input admission proof.
  assert.deepEqual(request.body.metadata, { fraction: 0.1, integer: Number.MAX_SAFE_INTEGER, duplicate: 'last' });
  assert.deepEqual(jsonResult(text), created);
});

test('issues material refinement and an optional comment share one revision-bound PATCH', async t => {
  const changes = {
    expected_revision: '1', title: 'Synthetic refined task', body: '', type: 'improvement', priority: 'high',
    labels: [], metadata: {}, owner_label: null, recipient_label: 'reviewer', target_session: 'recipient / next',
    comment: 'Synthetic refinement note', comment_metadata: { outcome: { state: 'partial' } },
  };
  const result = { ...updated, comment_id: 901 };
  const f = fixture(t, [{ body: result }]);
  const text = await f.tool.execute('refine', { action: 'update', id: 701, ...changes, format: 'json' });
  const request = firstRequest(f, 'PATCH', '/api/issues/701');
  for (const [field, value] of Object.entries(changes)) assert.deepEqual(request.body[field], value, `PATCH must preserve ${field}`);
  assert.equal(request.body.source_project, source);
  assert.equal(request.body.session_id, context.sessionId);
  assert.equal(request.body.client, 'openclaw');
  assert.deepEqual(jsonResult(text), result);
});

test('issues an omitted material field stays omitted rather than becoming a default replacement', async t => {
  const f = fixture(t, [{ body: updated }], { context: { ...context, sessionId: '', sessionKey: 'synthetic-not-a-session' } });
  const text = await f.tool.execute('partial', { action: 'update', id: 701, expected_revision: '1', body: 'Synthetic next action', session_id: 'synthetic-forged-session', client: 'synthetic-forged-client', format: 'json' });
  const request = firstRequest(f, 'PATCH', '/api/issues/701');
  for (const field of ['title', 'priority', 'type', 'labels', 'metadata', 'owner_label', 'recipient_label', 'target_session', 'status', 'comment']) {
    assert.equal(Object.hasOwn(request.body, field), false, `${field} was not part of this edit`);
  }
  assert.equal(Object.hasOwn(request.body, 'session_id'), false, 'An empty host session must not be replaced by sessionKey, project or installation');
  assert.equal(request.body.client, 'openclaw');
  assert.deepEqual(jsonResult(text), updated);
});

for (const [name, target_session] of [['set', 'recipient & 日本語'], ['clear', null]]) {
  test(`issues target session ${name} retains the revision and trusted provenance`, async t => {
    const f = fixture(t, [{ body: updated }]);
    const input = { action: 'update', id: 701, expected_revision: '1', target_session, source_project: target, source_agent: 'forged', session_id: 'forged', client: 'forged', format: 'json' };
    const original = structuredClone(input);
    const result = await f.tool.execute('target-session', input);
    const request = firstRequest(f, 'PATCH', '/api/issues/701');
    assert.equal(Object.hasOwn(request.body, 'target_session'), true);
    assert.equal(request.body.target_session, target_session);
    assert.equal(request.body.expected_revision, '1');
    assert.equal(request.body.source_project, source);
    assert.equal(request.body.source_agent, context.agentId);
    assert.equal(request.body.session_id, context.sessionId);
    assert.equal(request.body.client, 'openclaw');
    assert.deepEqual(input, original);
    assert.deepEqual(jsonResult(result), updated);
  });
}

for (const [name, params, code] of [
  ['missing revision', { action: 'update', body: 'explicit edit' }, 'REVISION_REQUIRED'],
  ['target session without revision', { action: 'update', target_session: 'recipient' }, 'REVISION_REQUIRED'],
  ['target session clear without revision', { action: 'update', target_session: null }, 'REVISION_REQUIRED'],
  ['lifecycle with field edit without revision', { action: 'update', status: 'resolved', metadata: { outcome: 'needs-cas' } }, 'REVISION_REQUIRED'],
  ['numeric revision', { action: 'update', status: 'resolved', expected_revision: 1 }, 'INVALID_ARGUMENT'],
  ['zero revision', { action: 'update', status: 'resolved', expected_revision: '0' }, 'INVALID_ARGUMENT'],
  ['signed revision', { action: 'update', status: 'resolved', expected_revision: '+1' }, 'INVALID_ARGUMENT'],
  ['noncanonical revision', { action: 'update', status: 'resolved', expected_revision: '01' }, 'INVALID_ARGUMENT'],
  ['fractional revision', { action: 'update', status: 'resolved', expected_revision: '1.0' }, 'INVALID_ARGUMENT'],
  ['overflow revision', { action: 'update', status: 'resolved', expected_revision: '9223372036854775808' }, 'INVALID_ARGUMENT'],
  ['empty edit', { action: 'update', expected_revision: '1' }, 'INVALID_ARGUMENT'],
  ['orphan comment metadata', { action: 'update', expected_revision: '1', status: 'resolved', comment_metadata: {} }, 'INVALID_ARGUMENT'],
  ['empty owner', { action: 'update', expected_revision: '1', owner_label: '' }, 'INVALID_ARGUMENT'],
  ['empty recipient', { action: 'update', expected_revision: '1', recipient_label: '' }, 'INVALID_ARGUMENT'],
  ['empty comment', { action: 'comment', body: '' }, 'INVALID_ARGUMENT'],
]) {
  test(`issues rejects ${name} without a mutation`, async t => {
    const f = fixture(t);
    const text = await f.tool.execute('invalid-edit', { id: 701, ...params, format: 'json' });
    assert.equal(f.requests.length, 0);
    assert.equal(jsonResult(text).error.code, code);
  });
}

for (const params of [
  { ...createInput, target_session: '' },
  { action: 'list', target_project: target, target_session: '' },
]) {
  test(`issues rejects an empty target session on ${params.action} without dropping it`, async t => {
    const failure = { error: { code: 'INVALID_ARGUMENT', message: 'Invalid target session' } };
    const f = fixture(t, [{ status: 400, body: failure }]);
    const result = await executeJson(f.tool, params);
    assert.equal(result.error.code, 'INVALID_ARGUMENT');
    if (f.requests.length !== 0) {
      const request = firstRequest(f, params.action === 'create' ? 'POST' : 'GET', '/api/issues');
      if (params.action === 'create') assert.equal(request.body.target_session, '');
      else assert.equal(request.url.searchParams.get('target_session'), '');
    }
  });
}

for (const [action, status] of [['update', 'resolved'], ['reopen', 'reopened'], ['close', 'closed']]) {
  test(`issues ${action} preserves a maximum int64 revision as a decimal string`, async t => {
    const f = fixture(t, [{ body: updated }]);
    const text = await f.tool.execute('lifecycle', { action, id: 701, status, expected_revision: '9223372036854775807', format: 'json' });
    const request = firstRequest(f, 'PATCH', '/api/issues/701');
    assert.equal(request.body.expected_revision, '9223372036854775807');
    assert.equal(request.body.status, status);
    assert.equal(request.body.session_id, context.sessionId);
    assert.equal(request.body.client, 'openclaw');
    assert.deepEqual(jsonResult(text), updated);
  });
}

for (const [action, status] of [['update', 'resolved'], ['reopen', 'reopened'], ['close', 'closed']]) {
  test(`issues ${action} accepts an unconditioned lifecycle-only request`, async t => {
    const f = fixture(t, [{ body: updated }]);
    const result = await executeJson(f.tool, { action, id: 701, ...(action === 'update' ? { status } : {}) });
    const request = firstRequest(f, 'PATCH', '/api/issues/701');
    assert.equal(request.body.status, status);
    assert.equal(Object.hasOwn(request.body, 'expected_revision'), false);
    assert.equal(request.body.session_id, context.sessionId);
    assert.deepEqual(result, updated);
  });
}

test('issues stale compound update preserves the conflict and never rebases or retries', async t => {
  const conflict = { error: { code: 'REVISION_CONFLICT', message: 'Read and explicitly reconcile', current_revision: '7' } };
  const f = fixture(t, [{ status: 409, body: conflict }]);
  const input = { action: 'update', id: 701, expected_revision: '2', status: 'resolved', comment: 'Synthetic losing draft', comment_metadata: { outcome: 'uncommitted' }, format: 'json' };
  const before = structuredClone(input);
  const text = await f.tool.execute('stale', input);
  const request = firstRequest(f, 'PATCH', '/api/issues/701');
  assert.equal(request.body.expected_revision, '2');
  assert.equal(request.body.comment, before.comment);
  assert.deepEqual(input, before, 'The caller-owned losing intent must remain unchanged');
  assert.deepEqual(jsonResult(text), conflict);
});

test('issues standalone comment maps metadata without requiring a material revision', async t => {
  const response = { ...updated, revision: '2', changed: false, comment_id: 901 };
  const f = fixture(t, [{ body: response }]);
  const metadata = { acceptance: ['reviewed'], outcome: { state: 'observed' } };
  const text = await f.tool.execute('comment', { action: 'comment', id: 701, body: 'Synthetic append', metadata, format: 'json' });
  const request = firstRequest(f, 'PATCH', '/api/issues/701');
  assert.equal(request.body.comment, 'Synthetic append');
  assert.deepEqual(request.body.comment_metadata, metadata);
  assert.equal(request.body.session_id, context.sessionId);
  assert.equal(request.body.client, 'openclaw');
  assert.equal(Object.hasOwn(request.body, 'expected_revision'), false);
  assert.equal(Object.hasOwn(request.body, 'status'), false);
  assert.deepEqual(jsonResult(text), response);
});

test('issues standalone comment forwards advertised comment_metadata without changing task metadata', async t => {
  const response = { ...updated, changed: false, comment_id: 901 };
  const f = fixture(t, [{ body: response }]);
  const metadata = { outcome: { state: 'observed' }, acceptance: ['reviewed'] };
  const result = await executeJson(f.tool, { action: 'comment', id: 701, body: 'Synthetic append', comment_metadata: metadata });
  const request = firstRequest(f, 'PATCH', '/api/issues/701');
  assert.equal(request.body.comment, 'Synthetic append');
  assert.deepEqual(request.body.comment_metadata, metadata);
  assert.equal(Object.hasOwn(request.body, 'metadata'), false);
  assert.deepEqual(result, response);
});

test('issues standalone comment rejects both metadata aliases without sending private values', async t => {
  const f = fixture(t);
  const secret = 'synthetic-private-comment-metadata';
  const text = await f.tool.execute('ambiguous-comment', {
    action: 'comment', id: 701, body: 'Synthetic append',
    metadata: { legacy: secret }, comment_metadata: { current: secret }, format: 'json',
  });
  assert.equal(jsonResult(text).error.code, 'INVALID_ARGUMENT');
  assert.equal(f.requests.length, 0);
  assert.equal(text.includes(secret), false);
  assert.equal(JSON.stringify(f.diagnostics).includes(secret), false);
});

test('issues a supplied standalone-comment revision is honored in the HTTP request', async t => {
  const f = fixture(t, [{ body: { ...updated, changed: false, comment_id: 901 } }]);
  const text = await f.tool.execute('conditional-comment', { action: 'comment', id: 701, body: 'Synthetic conditional append', expected_revision: '2', metadata: {}, format: 'json' });
  assert.equal(firstRequest(f, 'PATCH', '/api/issues/701').body.expected_revision, '2');
  assert.equal(jsonResult(text).changed, false);
});

test('issues list keeps all AND filters and encodes literal search as query data', async t => {
  const page = { issues: [issue()], total: 1, limit: 200, offset: 0, next_offset: null, project_names: { [target]: 'Synthetic target' } };
  const f = fixture(t, [{ body: page }]);
  const filters = { target_project: target, source_project: source, source_agent: 'agent & reviewer', source_session: 'source & 日本語', target_session: 'recipient & 日本語', participant_session: 'participant / synthetic', status: 'open,reopened', type: 'task', q: 'literal %_ & 日本語', owner_label: 'docs', recipient_label: 'next-agent', limit: 200, offset: 0 };
  const text = await f.tool.execute('filtered-list', { action: 'list', ...filters, format: 'json' });
  const request = firstRequest(f, 'GET', '/api/issues');
  for (const [field, value] of Object.entries(filters)) assert.equal(request.url.searchParams.get(field === 'target_project' ? 'project' : field), String(value), `Preserve ${field}`);
  assert.deepEqual(jsonResult(text), page);
});

test('issues list preserves nullable historical labels without rejecting the page', async t => {
  const page = { issues: [{ ...issue(), labels: null }], total: 1, limit: 20, offset: 0, next_offset: null, project_names: {} };
  const f = fixture(t, [{ body: page }]);
  const result = await executeJson(f.tool, { action: 'list', target_project: target });
  firstRequest(f, 'GET', '/api/issues');
  assert.deepEqual(result, page);
});

test('issues detail preserves nullable historical labels without losing comments or history', async t => {
  const page = detail({ issue: { ...issue(), labels: null }, comments: [comment(901)], comment_count: 1, history: [history(2)], history_count: 1 });
  const f = fixture(t, [{ body: page }]);
  const result = await executeJson(f.tool, { action: 'get', id: 701 });
  firstRequest(f, 'GET', '/api/issues/701');
  assert.deepEqual(result, page);
});

test('issues rejects non-string labels rather than treating malformed data as nullable labels', async t => {
  const page = { issues: [{ ...issue(), labels: ['valid', 42] }], total: 1, limit: 20, offset: 0, next_offset: null, project_names: {} };
  const f = fixture(t, [{ body: page }]);
  const result = await executeJson(f.tool, { action: 'list', target_project: target });
  firstRequest(f, 'GET', '/api/issues');
  assert.equal(result.error.code, 'UNAVAILABLE');
});

for (const [input, wire] of [
  ['1970-01-02T00:00:00.000Z', '86400000'],
  ['1970-01-02T01:00:00+01:00', '86400000'],
  ['-9223372036854775808', '-9223372036854775808'],
  ['9223372036854775807', '9223372036854775807'],
  ['-1000', '-1000'],
]) {
  test(`issues list sends resolved_since ${input} as signed epoch milliseconds`, async t => {
    const page = { issues: [], total: 0, limit: 20, offset: 0, next_offset: null, project_names: {} };
    const f = fixture(t, [{ body: page }]);
    const params = { action: 'list', target_project: target, resolved_since: input, format: 'json' };
    const original = structuredClone(params);
    const text = await f.tool.execute('resolved-since', params);
    const request = firstRequest(f, 'GET', '/api/issues');
    assert.equal(request.url.searchParams.get('resolved_since'), wire);
    assert.deepEqual(params, original);
    assert.deepEqual(jsonResult(text), page);
  });
}

for (const input of [
  '-9223372036854775809', '9223372036854775808',
  'not-a-timestamp', '2026-02-30T00:00:00Z',
  '1970-01-02T00:00:00', '1970-01-02T00:00:00.0001Z',
]) {
  test(`issues rejects invalid resolved_since ${input} without a request`, async t => {
    const f = fixture(t);
    const result = await executeJson(f.tool, { action: 'list', target_project: target, resolved_since: input });
    assert.equal(f.requests.length, 0);
    assert.equal(result.error.code, 'INVALID_ARGUMENT');
  });
}

test('issues direct client carries new list filters without going through the tool', async t => {
  const page = { issues: [], total: 0, limit: 20, offset: 5, next_offset: null, project_names: {} };
  const f = fixture(t, [{ body: page }]);
  const filters = { project: target, source_project: source, source_agent: 'synthetic-agent', source_session: 'source & synthetic', target_session: 'recipient & synthetic', participant_session: 'participant / synthetic', type: 'task', q: 'literal %_&', owner_label: 'docs', recipient_label: 'reviewer', limit: 20, offset: 5 };
  const result = await f.client.listIssues(filters);
  const request = firstRequest(f, 'GET', '/api/issues');
  for (const [field, value] of Object.entries(filters)) assert.equal(request.url.searchParams.get(field), String(value));
  assert.deepEqual(result, page);
});

for (const [field, invalid] of [
  ['limit', [0, -1, 1.5, 201, Infinity, NaN, '20']],
  ['offset', [-1, 0.5, Infinity, NaN, '0', 2 ** 53]],
  ['comments_limit', [0, -1, 1.5, 201, Infinity, NaN, '50']],
  ['history_limit', [0, -1, 1.5, 201, Infinity, NaN, '50']],
  ['comments_offset', [-1, 0.5, Infinity, NaN, '0', 2 ** 53]],
  ['history_offset', [-1, 0.5, Infinity, NaN, '0', 2 ** 53]],
]) {
  test(`issues ${field} rejects invalid bounds and types before an HTTP request`, async t => {
    const f = fixture(t);
    for (const value of invalid) {
      const base = ['limit', 'offset'].includes(field) ? { action: 'list', target_project: target } : { action: 'get', id: 701 };
      const text = await f.tool.execute('invalid-page', { ...base, [field]: value, format: 'json' });
      assert.equal(f.requests.length, 0, `${field}=${String(value)} must not be silently defaulted`);
      assert.equal(jsonResult(text).error.code, 'INVALID_ARGUMENT');
    }
  });
}

test('issues default inbox uses 20 rows and static 205-card browsing reaches every later page explicitly', async t => {
  const rows = Array.from({ length: 205 }, (_, index) => issue(index + 1));
  const f = fixture(t, request => {
    const limit = Number(request.url.searchParams.get('limit') ?? 20);
    const offset = Number(request.url.searchParams.get('offset') ?? 0);
    return { body: { issues: rows.slice(offset, offset + limit), total: rows.length, limit, offset, next_offset: offset + limit < rows.length ? offset + limit : null, project_names: { [target]: 'Synthetic target' } } };
  });
  const defaultText = await f.tool.execute('default-list', { action: 'list', format: 'json' });
  assert.equal(f.requests[0].url.searchParams.get('limit'), '20');
  assert.equal(f.requests[0].url.searchParams.get('project'), source);
  assert.equal(jsonResult(defaultText).issues.length, 20);
  const ids = [];
  let offset = 0;
  for (let pageIndex = 0; pageIndex < 3; pageIndex++) {
    const text = await f.tool.execute('later-page', { action: 'list', target_project: target, limit: 100, offset, format: 'json' });
    const request = f.requests[pageIndex + 1];
    assert.equal(request.url.searchParams.get('offset'), String(offset));
    assert.equal(f.requests.length, pageIndex + 2, 'No automatic page draining is allowed');
    const page = jsonResult(text);
    ids.push(...page.issues.map(row => row.id));
    assert.equal(page.total, 205);
    offset = page.next_offset;
  }
  assert.equal(offset, null);
  assert.deepEqual(ids, rows.map(row => row.id));
});

test('issues get requests explicit paged detail with independent defaults', async t => {
  const page = detail();
  const f = fixture(t, [{ body: page }]);
  const text = await f.tool.execute('default-detail', { action: 'get', id: 701, format: 'json' });
  const query = firstRequest(f, 'GET', '/api/issues/701').url.searchParams;
  assert.equal(query.get('detail_mode'), 'paged');
  for (const [field, value] of Object.entries({ comments_offset: 0, comments_limit: 50, history_offset: 0, history_limit: 50 })) assert.equal(query.get(field), String(value));
  assert.deepEqual(jsonResult(text), page);
});

test('issues direct get client preserves independent paging query and response fields', async t => {
  const page = detail({ comments_offset: 2, comments_limit: 200, history_offset: 7, history_limit: 1 });
  const f = fixture(t, [{ body: page }]);
  const params = { detail_mode: 'paged', comments_offset: 2, comments_limit: 200, history_offset: 7, history_limit: 1 };
  const result = await f.client.getIssue(701, params);
  const query = firstRequest(f, 'GET', '/api/issues/701').url.searchParams;
  for (const [field, value] of Object.entries(params)) assert.equal(query.get(field), String(value));
  assert.deepEqual(result, page);
});

test('issues detail keeps independent continuations and does not treat a nonzero complete page as earlier-page coverage', async t => {
  const first = detail({
    comments: [comment(103)], history: [history(8)], comment_count: 4, history_count: 10,
    comments_offset: 2, comments_limit: 200, comments_next_offset: 3, comments_complete: false,
    history_offset: 7, history_limit: 200, history_next_offset: 8, history_complete: false,
  });
  const second = detail({
    comments: [comment(104)], history: [history(9), history(10)], comment_count: 4, history_count: 10,
    comments_offset: 3, comments_limit: 200, history_offset: 8, history_limit: 200,
  });
  const f = fixture(t, [{ body: first }, { body: second }]);
  let params = { action: 'get', id: 701, comments_offset: 2, comments_limit: 200, history_offset: 7, history_limit: 200, format: 'json' };
  const firstText = await f.tool.execute('independent-pages', params);
  assert.equal(f.requests.length, 1);
  const firstQuery = f.requests[0].url.searchParams;
  assert.equal(firstQuery.get('comments_offset'), '2');
  assert.equal(firstQuery.get('history_offset'), '7');
  const firstResult = jsonResult(firstText);
  assert.deepEqual(firstResult, first);
  assert.equal(firstResult.comments_next_offset, firstResult.comments_offset + firstResult.comments.length);
  assert.equal(firstResult.history_next_offset, firstResult.history_offset + firstResult.history.length);
  params = { ...params, comments_offset: firstResult.comments_next_offset, history_offset: firstResult.history_next_offset };
  const secondResult = jsonResult(await f.tool.execute('continue-pages', params));
  assert.equal(f.requests.length, 2);
  assert.equal(f.requests[1].url.searchParams.get('comments_offset'), '3');
  assert.equal(f.requests[1].url.searchParams.get('history_offset'), '8');
  assert.deepEqual(secondResult, second);
  assert.equal(secondResult.comments_complete, true);
  assert.equal(secondResult.history_complete, true);
  assert.equal(secondResult.comments_offset, 3);
  assert.equal(secondResult.history_offset, 8);
  assert.deepEqual(secondResult.comments.map(row => row.id), [104], 'A final page must not invent earlier comments');
});

test('issues empty nonzero detail pages retain effective offsets, limits and completeness', async t => {
  const page = detail({ comment_count: 4, history_count: 10, comments_offset: 4, comments_limit: 1, history_offset: 10, history_limit: 200 });
  const f = fixture(t, [{ body: page }]);
  const result = await executeJson(f.tool, { action: 'get', id: 701, comments_offset: 4, comments_limit: 1, history_offset: 10, history_limit: 200 });
  firstRequest(f, 'GET', '/api/issues/701');
  assert.deepEqual(result, page);
  assert.equal(result.comments_offset + result.comments.length, 4);
  assert.equal(result.history_offset + result.history.length, 10);
});

for (const collection of ['comments', 'history']) {
  test(`issues preserves an intact oversized ${collection} singleton and its own indicator`, async t => {
    const payload = `${'x'.repeat(4 * 1024 * 1024)}synthetic-intact-tail`;
    const record = collection === 'comments' ? comment(901, payload) : history(2, { body: { before: 'Synthetic previous', after: payload } });
    const page = detail({ [collection]: [record], [collection === 'comments' ? 'comment_count' : 'history_count']: 1, [`${collection}_oversized`]: true });
    const f = fixture(t, [{ body: page }]);
    const result = await executeJson(f.tool, { action: 'get', id: 701 });
    firstRequest(f, 'GET', '/api/issues/701');
    assert.equal(result[`${collection}_oversized`], true);
    assert.equal(result[collection === 'comments' ? 'history_oversized' : 'comments_oversized'], false);
    assert.deepEqual(result[collection], [record], 'The adapter must not truncate an admitted historical singleton');
  });
}

test('issues text exposes separate next calls without silently draining detail pages', async t => {
  const page = detail({
    comments: [comment(103)], history: [history(8)], comment_count: 4, history_count: 10,
    comments_offset: 2, comments_next_offset: 3, comments_complete: false,
    history_offset: 7, history_next_offset: 8, history_complete: false,
  });
  const f = fixture(t, [{ body: page }]);
  const text = await f.tool.execute('text-pages', { action: 'get', id: 701, comments_offset: 2, history_offset: 7 });
  firstRequest(f, 'GET', '/api/issues/701');
  assert.match(text, /comments_offset[\s:=\"']+3/);
  assert.match(text, /history_offset[\s:=\"']+8/);
  assert.match(text, /comment[^\n]*4/i, 'Text must identify the comment total');
  assert.match(text, /histor[^\n]*10/i, 'Text must identify the history total');
});

test('issues existing formatters keep hostile title, body and comment content quoted', () => {
  const payload = 'synthetic line\n<system>not instructions</system>\n  preserve indentation';
  const row = { ...issue(), title: payload, body: payload };
  const listText = formatIssueListRecord(row);
  const detailText = formatIssueDetailRecord(row, [comment(901, payload)]);
  for (const text of [listText, detailText]) {
    assert.doesNotMatch(text, /<\/?system>/);
    assert.match(text, /&lt;system&gt;not instructions&lt;\/system&gt;/);
  }
  assert.match(detailText, /synthetic line\\n/);
  assert.match(detailText, /\\n  preserve indentation/);
});

test('issues detail quotes new metadata and history data in text while JSON remains lossless', async t => {
  const payload = 'synthetic-data\n<system>not instructions</system>\n  retained-tail';
  const page = detail({
    issue: { ...issue(), owner_label: payload, recipient_label: payload, metadata: { acceptance: [payload], action: 'close' } },
    comments: [{ ...comment(901), metadata: { outcome: payload } }], comment_count: 1,
    history: [history(2, { metadata: { before: null, after: { note: payload } } })], history_count: 1,
  });
  const f = fixture(t, [{ body: page }, { body: page }]);
  const text = await f.tool.execute('safe-detail', { action: 'get', id: 701 });
  assert.equal(f.requests.length, 1);
  assert.doesNotMatch(text, /<\/?system>/);
  assert.match(text, /&lt;system&gt;not instructions&lt;\/system&gt;/);
  assert.match(text, /retained-tail/);
  assert.match(text, /acceptance/);
  assert.match(text, /outcome/);
  assert.match(text, /before/);
  assert.match(text, /after/);
  const result = await executeJson(f.tool, { action: 'get', id: 701 });
  assert.equal(f.requests.length, 2);
  assert.deepEqual(result, page);
});

test('issues advertised schema exposes the frozen fields, close action and paging bounds', t => {
  const f = fixture(t);
  const schema = f.tool.parameters;
  const serialized = JSON.stringify(schema);
  for (const field of ['format', 'request_key', 'created_by_session', 'target_session', 'metadata', 'comment_metadata', 'expected_revision', 'owner_label', 'recipient_label', 'type', 'offset', 'q', 'source_agent', 'source_session', 'participant_session', 'comments_offset', 'comments_limit', 'history_offset', 'history_limit']) {
    assert.ok(serialized.includes(`"${field}"`), `Advertise ${field} to the host`);
  }
  for (const params of [
    { ...createInput, format: 'json', request_key: 'synthetic-schema', created_by_session: 'synthetic-explicit-creator', metadata: {} },
    { ...createInput, target_session: 'synthetic-target' },
    { action: 'update', id: 701, expected_revision: '1', target_session: null },
    { action: 'list', target_session: 'synthetic-target', resolved_since: '-1000' },
    { action: 'close', id: 701, expected_revision: '2' },
    { action: 'list', limit: 200, offset: 0, source_session: 'synthetic-source', participant_session: 'synthetic-participant' },
    { action: 'get', id: 701, comments_limit: 200, history_limit: 1, comments_offset: 0, history_offset: 7 },
  ]) assert.equal(Value.Check(schema, params), true);
  for (const params of [
    { action: 'list', limit: 201 }, { action: 'list', offset: -1 },
    { action: 'get', id: 701, comments_limit: 0 }, { action: 'get', id: 701, history_limit: 201 },
    { action: 'get', id: 701, comments_offset: -1 }, { ...createInput, format: 'yaml' },
  ]) assert.equal(Value.Check(schema, params), false, `Advertised schema must reject ${JSON.stringify(params)}`);
});

test('issues advertises metadata destinations and replacement semantics to the host', t => {
  const f = fixture(t);
  const metadata = f.tool.parameters.properties.metadata.description;
  const commentMetadata = f.tool.parameters.properties.comment_metadata.description;
  for (const action of ['create', 'update', 'reopen', 'close', 'comment']) assert.match(metadata, new RegExp(`\\b${action}\\b`, 'i'));
  assert.match(metadata, /replac/i);
  assert.match(metadata, /task/i);
  for (const action of ['update', 'reopen', 'close', 'comment']) assert.match(commentMetadata, new RegExp(`\\b${action}\\b`, 'i'));
  assert.match(commentMetadata, /comment/i);
});

test('issues project-free reads survive identity hold but writes and default inbox do not substitute caller labels', async t => {
  const page = { issues: [], total: 0, limit: 20, offset: 0, next_offset: null, project_names: {} };
  const f = fixture(t, [{ body: page }, { body: detail() }], { projectFree: true });
  let resolutions = 0;
  t.mock.method(f.client, 'resolveWorkspaceProject', async () => { resolutions++; throw new Error('PROJECT_MIGRATION_REQUIRED'); });
  const listText = await f.tool.execute('project-free-list', { action: 'list', target_project: target, format: 'json' });
  assert.equal(resolutions, 0);
  assert.deepEqual(jsonResult(listText), page);
  const getText = await f.tool.execute('project-free-get', { action: 'get', id: 701, format: 'json' });
  assert.equal(resolutions, 0);
  assert.equal(jsonResult(getText).issue.id, 701);
  for (const params of [{ ...createInput, project: target, source_project: target }, { action: 'list' }]) {
    const text = await f.tool.execute('held', { ...params, format: 'json' });
    assert.equal(f.requests.length, 2, 'Identity hold must prevent additional issue requests');
    assert.equal(jsonResult(text).error.code, 'PROJECT_MIGRATION_REQUIRED');
  }
  assert.equal(resolutions, 2);
});

test('issues material no-op preserves unchanged revision, changed=false and a null comment ID', async t => {
  const response = { ...updated, revision: '2', changed: false, comment_id: null };
  const f = fixture(t, [{ body: response }]);
  const text = await f.tool.execute('no-op', { action: 'update', id: 701, expected_revision: '2', body: 'Synthetic unchanged body', format: 'json' });
  const request = firstRequest(f, 'PATCH', '/api/issues/701');
  assert.equal(request.body.expected_revision, '2');
  assert.deepEqual(jsonResult(text), response);
});

for (const [name, metadata] of [['null', null], ['array', []], ['scalar', 'invalid'], ['unsafe number', { n: 2 ** 53 }]]) {
  for (const action of ['comment', 'update']) {
    test(`issues ${action} rejects ${name} comment metadata before mutation`, async t => {
      const f = fixture(t);
      const params = action === 'comment'
        ? { action, id: 701, body: 'Synthetic comment', metadata }
        : { action, id: 701, expected_revision: '2', status: 'resolved', comment: 'Synthetic comment', comment_metadata: metadata };
      const text = await f.tool.execute('invalid-comment-metadata', { ...params, format: 'json' });
      assert.equal(f.requests.length, 0, 'Invalid comment metadata must not be dropped while committing the comment');
      assert.equal(jsonResult(text).error.code, 'INVALID_ARGUMENT');
    });
  }
}

test('issues text marks oversized detail data explicitly while preserving the record tail', async t => {
  const body = `${'x'.repeat(4 * 1024 * 1024)}synthetic-oversized-tail`;
  const page = detail({ comments: [comment(901, body)], comment_count: 1, comments_oversized: true });
  const f = fixture(t, [{ body: page }]);
  const text = await f.tool.execute('oversized-text', { action: 'get', id: 701 });
  firstRequest(f, 'GET', '/api/issues/701');
  assert.match(text, /oversiz/i, 'Historical over-target data must be identified rather than silently shortened');
  assert.match(text, /synthetic-oversized-tail/);
});

for (const expected_actor of [
  { kind: 'client', reference: 'synthetic-policy-approved-client' },
  { kind: 'operator', reference: 'operator:session' },
  { kind: 'unknown', reference: null },
]) {
  test(`issues keyed create preserves optional ${expected_actor.kind} expectation on initial and explicit repeat POST`, async t => {
    // Responses are scripted: comparison/authentication remain server responsibilities, including unknown/null.
    const refused = { error: { code: 'ACTOR_CHANGED', message: 'Actor precondition failed' } };
    const responses = expected_actor.kind === 'unknown'
      ? [{ status: 409, body: refused }, { status: 409, body: refused }]
      : [{ status: 201, body: created }, { body: { ...created, replayed: true, message: 'issue replayed' } }];
    const f = fixture(t, responses);
    const pending = { ...createInput, request_key: 'synthetic-actor-bound-intent', expected_actor, metadata: { outcome: 'pending' }, format: 'json' };
    const original = structuredClone(pending);
    for (let attempt = 0; attempt < 2; attempt++) {
      const text = await f.tool.execute('explicit-actor-attempt', pending);
      assert.equal(f.requests.length, attempt + 1, 'Only the explicit caller attempt may dispatch; no auth-me fetch or automatic retry');
      const request = f.requests[attempt];
      assert.equal(request.method, 'POST');
      assert.equal(request.url.pathname, '/api/issues');
      assert.deepEqual(request.body.expected_actor, original.expected_actor);
      assert.equal(request.body.request_key, original.request_key);
      assert.equal(request.body.source_project, source, 'Expectation is not the source/namespace selector');
      assert.equal(request.headers.get('authorization'), 'Bearer synthetic-513-token', 'Expectation is not a credential');
      assert.deepEqual(jsonResult(text), responses[attempt].body);
      assert.deepEqual(pending, original, 'Caller-owned original expectation and intent must remain unchanged');
    }
    assert.deepEqual(f.requests[1].body, f.requests[0].body);
  });
}

for (const format of ['json', 'text']) {
  test(`issues ACTOR_CHANGED ${format} retains the original actor-bound intent without retry or reassignment`, async t => {
    const refused = { error: { code: 'ACTOR_CHANGED', message: 'Actor precondition failed' } };
    const f = fixture(t, [{ status: 409, body: refused }]);
    const pending = {
      ...createInput, request_key: 'synthetic-retained-actor-key',
      expected_actor: { kind: 'operator', reference: 'operator:master' },
      metadata: { outcome: { state: 'pending' } }, format,
    };
    const original = structuredClone(pending);
    const text = await f.tool.execute('actor-changed', pending);
    const request = firstRequest(f, 'POST', '/api/issues');
    assert.equal(request.body.request_key, original.request_key);
    assert.deepEqual(request.body.expected_actor, original.expected_actor);
    assert.deepEqual(request.body.metadata, original.metadata);
    assert.equal(request.body.source_project, source);
    assert.deepEqual(pending, original, 'A refusal must not delete/change the saved key, actor or original payload');
    if (format === 'json') {
      assert.deepEqual(jsonResult(text), refused);
    } else {
      assert.ok(text.includes('ACTOR_CHANGED'));
      assert.match(text, /reconcil|explicit.*(?:decis|review)|(?:preserv|retain|keep).*(?:intent|draft)/i,
        'Adapter text must supply an explicit reconciliation/intent-preservation action beyond the short server message');
      assert.equal(text.includes(original.request_key), false, 'Generic actor refusal must not echo the saved intent key');
      assert.equal(text.includes(original.expected_actor.reference), false, 'Generic actor refusal must not echo actor identity');
    }
  });
}

test('issues keyed create without optional expected_actor keeps the existing path without auto-fetching identity', async t => {
  const f = fixture(t, [{ status: 201, body: created }]);
  const pending = { ...createInput, request_key: 'synthetic-no-actor-expectation', format: 'json' };
  const original = structuredClone(pending);
  const text = await f.tool.execute('optional-actor-omitted', pending);
  const request = firstRequest(f, 'POST', '/api/issues');
  assert.equal(Object.hasOwn(request.body, 'expected_actor'), false);
  assert.equal(request.body.request_key, original.request_key);
  assert.deepEqual(pending, original);
  assert.deepEqual(jsonResult(text), created);
});

test('issues advertised schema includes the optional authenticated actor expectation subset', t => {
  const f = fixture(t);
  assert.ok(JSON.stringify(f.tool.parameters).includes('"expected_actor"'), 'The optional create precondition must be discoverable');
  for (const expected_actor of [
    { kind: 'client', reference: 'synthetic-client' },
    { kind: 'operator', reference: 'operator:session' },
    { kind: 'unknown', reference: null },
  ]) assert.equal(Value.Check(f.tool.parameters, { ...createInput, request_key: 'synthetic-schema-actor', expected_actor }), true);
});

for (const [name, params, code, field, recovery] of [
  ['invalid metadata', { ...createInput, metadata: null }, 'INVALID_ARGUMENT', 'metadata', /\b(?:correct|fix|revise)\b/i],
  ['oversized key', { ...createInput, request_key: 'x'.repeat(513) }, 'PAYLOAD_TOO_LARGE', 'request_key', /\b(?:reduce|shorten|lower|use)\b/i],
  ['missing material revision', { action: 'update', id: 701, body: 'explicit edit' }, 'REVISION_REQUIRED', 'expected_revision', /\b(?:get|read|fetch)\b/i],
]) {
  test(`issues default-text ${name} keeps the legacy JSON array and adds ${code} recovery`, async t => {
    const f = fixture(t);
    const text = await f.tool.execute('legacy-validation-recovery', params);
    assert.equal(f.requests.length, 0, 'Runtime validation must reject the input before HTTP');
    const prefix = 'Invalid parameters: ';
    assert.ok(text.startsWith(prefix), 'Retain the legacy validation prefix');
    let entries;
    assert.doesNotThrow(() => { entries = JSON.parse(text.slice(prefix.length)); }, 'The entire suffix must remain parseable by the existing legacy consumer');
    assert.ok(Array.isArray(entries));
    assert.equal(entries.length, 1);
    assert.deepEqual(entries[0].path, [field]);
    assert.ok(text.includes(code), 'Default text must expose the stable domain code, not only a Zod code');
    const recoveryData = JSON.stringify(entries);
    assert.match(recoveryData, recovery, 'Validation text must include an actionable recovery step');
    if (code === 'PAYLOAD_TOO_LARGE') assert.match(recoveryData, /\b(?:bounds?|limits?|bytes?|size)\b/i);
    if (code === 'REVISION_REQUIRED') assert.match(recoveryData, /expected_revision|current\s+(?:issue\s+)?revision/i);
  });
}

for (const format of ['json', 'text']) {
  test(`issues untyped HTTP 409 ${format} preserves uncertainty and reconciliation without inventing a transition failure`, async t => {
    const f = fixture(t, [{ status: 409, body: { message: 'Conflict' } }]);
    const pending = {
      ...createInput, request_key: 'synthetic-untyped-conflict-intent',
      expected_actor: { kind: 'operator', reference: 'operator:session' },
      metadata: { outcome: 'pending' }, format,
    };
    const original = structuredClone(pending);
    const text = await f.tool.execute('untyped-conflict', pending);
    const request = firstRequest(f, 'POST', '/api/issues');
    assert.equal(request.body.request_key, original.request_key);
    assert.deepEqual(request.body.expected_actor, original.expected_actor);
    assert.deepEqual(request.body.metadata, original.metadata);
    assert.deepEqual(pending, original, 'An untyped conflict must preserve the caller-owned pending intent');
    const result = format === 'json' ? jsonResult(text) : null;
    if (result !== null) {
      assert.ok(result.error && typeof result.error === 'object', 'JSON must remain an error result without claiming success');
      assert.equal(Object.hasOwn(result, 'id'), false);
      assert.equal(Object.hasOwn(result, 'replayed'), false);
    }
    const rendered = result === null ? text : JSON.stringify(result.error);
    assert.doesNotMatch(rendered, /INVALID_TRANSITION/, 'HTTP status alone does not identify the conflict reason');
    assert.doesNotMatch(rendered, /\bchoose\b[^\n]*\bpermitted\b[^\n]*\btransition\b/i, 'Do not invent lifecycle-transition recovery for an untyped conflict');
    assert.match(rendered, /unknown|uncertain|unconfirmed|undetermined|unspecified|untyped|cannot\s+(?:determine|confirm)/i, 'Explicitly retain the unknown conflict reason/outcome');
    assert.match(rendered, /reconcil/i, 'Offer reconciliation instead of an automatic resubmission');
  });
}
