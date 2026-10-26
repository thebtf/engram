import assert from 'node:assert/strict';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import test from 'node:test';

const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED;
const load = (file) => import(compiled ? pathToFileURL(path.join(compiled, file)).href : new URL(`../dist/${file}`, import.meta.url));
const { parseConfig } = await load('config.js');
const { validateProjectID } = await load('identity.js');
const pluginModule = await load('index.js');
const plugin = pluginModule.default.default ?? pluginModule.default;
const remember = await load('tools/engram-remember.js');
const issues = await load('tools/engram-issues.js');
const { createEngramRateTool } = await load('tools/engram-rate.js');
const { createEngramSuppressTool } = await load('tools/engram-suppress.js');
const { createEngramTimelineTool } = await load('tools/engram-timeline.js');
const { createEngramSearchTool } = await load('tools/engram-search.js');

const token = 'synthetic-validation-only';

test('config preserves exact defaults, nested heartbeat defaults and explicit values', () => {
  assert.deepEqual(parseConfig({ token }), {
    url: 'http://localhost:37777', token, contextLimit: 10, sessionContextLimit: 20,
    tokenBudget: 2000, timeoutMs: 5000, autoExtract: true, heartbeat: { ingest: false }, logLevel: 'warn',
  });
  assert.deepEqual(parseConfig({ token, heartbeat: {} }).heartbeat, { ingest: false });
  const explicit = {
    url: 'https://example.invalid/engram', token, contextLimit: 1, sessionContextLimit: 2,
    tokenBudget: 3, timeoutMs: 4, autoExtract: false, heartbeat: { ingest: true }, logLevel: 'debug',
  };
  assert.deepEqual(parseConfig(explicit), explicit);
});

test('config keeps general GUID project IDs and the separate nonzero identity guard', () => {
  for (const project of ['12345678-1234-1234-1234-123456789abc', 'B42C9854-BF02-4D1B-867D-A2A096F9E4D0']) {
    assert.equal(parseConfig({ token, project }).project, project);
    assert.equal(validateProjectID(project), project);
  }
  assert.throws(() => parseConfig({ token, project: 'legacy-project' }));
  const nil = '00000000-0000-0000-0000-000000000000';
  assert.throws(() => validateProjectID(parseConfig({ token, project: nil }).project), /PROJECT_IDENTITY_INVALID/);
});

test('config and the actual plugin register API reject invalid URL, token, enum and heartbeat values', () => {
  for (const raw of [
    {}, { token: '' }, { token, url: 'not-a-url' }, { token, url: 'ftp://example.invalid' },
    { token, logLevel: 'verbose' }, { token, heartbeat: { ingest: 'true' } }, { token, autoExtract: 'false' },
  ]) {
    assert.throws(() => parseConfig(raw));
    assert.throws(() => plugin.register({ pluginConfig: raw }), (error) => error.name === 'ZodError');
  }
});

for (const field of ['contextLimit', 'sessionContextLimit', 'tokenBudget', 'timeoutMs']) {
  test(`config.${field} preserves positive finite integers, including representable values above MAX_SAFE_INTEGER`, () => {
    for (const value of [1, Number.MAX_SAFE_INTEGER, 2 ** 53, 2 ** 54, Number.MAX_VALUE]) assert.equal(parseConfig({ token, [field]: value })[field], value);
    for (const value of [0, -1, 1.5, Infinity, NaN, '2']) {
      assert.throws(() => parseConfig({ token, [field]: value }));
    }
  });
}

const invalidTools = [
  [remember.createEngramRememberTool, { title: 'Title' }],
  [remember.createEngramRememberTool, { content: 'Content' }],
  [remember.createEngramRememberTool, { title: 'Title', content: '', type: 'change' }],
  [remember.createEngramRememberTool, { title: 'Title', content: 'Content', type: 'unknown' }],
  [remember.createEngramRememberTool, { title: 'Title', content: 'Content', scope: 'unknown' }],
  [remember.createMemoryStoreTool, {}],
  [remember.createMemoryStoreTool, { text: '' }],
  [remember.createMemoryStoreTool, { content: '' }],
  [remember.createMemoryStoreTool, { text: 'Content', category: 'unknown' }],
  [issues.createEngramIssuesTool, { action: 'unknown' }],
  [issues.createEngramIssuesTool, { action: 'create', title: '' }],
  [issues.createEngramIssuesTool, { action: 'create', title: 'Title', priority: 'unknown' }],
];

test('real tool execute preserves required text, enums, refine and issue discriminant rejection', async () => {
  for (const [create, params] of invalidTools) {
    let availabilityReads = 0;
    const client = { isAvailable() { availabilityReads++; return false; } };
    assert.match(await create({}, client, {}).execute('invalid-scalar', params), /^Invalid parameters:/);
    assert.equal(availabilityReads, 0);
  }
});

const numericTools = [
  [createEngramRateTool, { rating: 'useful' }, 'id'],
  [createEngramSuppressTool, {}, 'id'],
  [createEngramTimelineTool, { mode: 'anchor' }, 'anchor_id'],
  ...['get', 'update', 'comment', 'reopen'].map(action => [issues.createEngramIssuesTool, { action, status: 'resolved', body: 'Comment' }, 'id']),
  [issues.createEngramIssuesTool, { action: 'list' }, 'limit'],
  [createEngramTimelineTool, {}, 'limit', 50],
  [createEngramSearchTool, { query: 'Query' }, 'limit', 50],
];

for (const [create, params, field, maximum] of numericTools) {
  test(`${create.name} ${params.action ?? params.mode ?? ''}.${field} preserves finite integer semantics and existing limits`, async () => {
    let availabilityReads = 0;
    const client = { isAvailable() { availabilityReads++; return false; } };
    const tool = create({}, client, {});
    for (const value of [0, -1, 1.5, Infinity, NaN, '2', ...(maximum ? [maximum + 1, 2 ** 53] : [])]) {
      assert.match(await tool.execute('invalid-number', { ...params, [field]: value }), /^Invalid parameters:/);
      assert.equal(availabilityReads, 0);
    }
    const valid = maximum ? [1, maximum] : [1, Number.MAX_SAFE_INTEGER, 2 ** 53, 2 ** 54, Number.MAX_VALUE];
    for (const value of valid) {
      assert.match(await tool.execute('valid-number', { ...params, [field]: value }), /^engram is currently unreachable/);
    }
    assert.equal(availabilityReads, valid.length);
  });
}
