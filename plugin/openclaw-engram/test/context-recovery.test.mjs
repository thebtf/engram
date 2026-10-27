import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';

const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED || fileURLToPath(new URL('../dist/', import.meta.url));
const imported = await import(pathToFileURL(path.join(compiled, 'tools/engram-issues.js')));
const { createEngramIssuesTool } = imported.default ?? imported;
const target = '11111111-1111-4111-8111-111111111111';

function fixture() {
  const calls = [];
  const client = {
    isAvailable: () => true,
    async resolveWorkspaceProject() { calls.push('resolve'); throw new Error('PROJECT_MIGRATION_REQUIRED'); },
    async listIssues(input) { calls.push(['list', input]); return { issues: [], total: 0 }; },
    async getIssue(id) { calls.push(['get', id]); return { issue: { id, priority: 'high', status: 'open', title: 'Read retained recovery issue', source_project: target, target_project: target }, comments: [] }; },
    async createIssue(input) { calls.push(['create', input]); throw new Error('A held write must not reach REST'); },
  };
  return { calls, client, tool: createEngramIssuesTool({ agentId: 'recovery-fixture', workspaceDir: '/synthetic/unpinned-repo' }, client, {}) };
}

test('context recovery: explicit issue list preflight does not resolve or replace caller identity', async t => {
  const { tool, calls } = fixture();
  const result = await tool.execute('recovery-list', { action: 'list', target_project: target });
  t.diagnostic(result);
  assert.equal(calls.length, 1, 'Project-free inspection must not invoke canonical resolution');
  assert.equal(calls[0][0], 'list');
  assert.equal(calls[0][1].project, target, 'Preserve the explicit target filter');
  assert.match(result, /No issues found/);
});

test('context recovery: issue get remains usable during canonical migration hold', async t => {
  const { tool, calls } = fixture();
  const result = await tool.execute('recovery-get', { action: 'get', id: 508 });
  t.diagnostic(result);
  assert.deepEqual(calls, [['get', 508]]);
  assert.match(result, /Read retained recovery issue/);
});

test('context recovery: failed preflight is not relabelled an empty successful inbox', async t => {
  const { tool, client } = fixture();
  client.listIssues = async () => null;
  const result = await tool.execute('recovery-list-failed', { action: 'list', target_project: target });
  t.diagnostic(result);
  assert.match(result, /Failed to list issues/);
  assert.doesNotMatch(result, /No issues found/);
});

test('context recovery: migration blocks issue writes and supplies the real owner inspection boundary', async t => {
  const { tool, calls } = fixture();
  const result = await tool.execute('recovery-create', { action: 'create', title: 'Must remain pending', target_project: target, project: target });
  t.diagnostic(result);
  assert.deepEqual(calls, ['resolve'], 'Destination/caller arguments cannot substitute for resolved actor identity');
  assert.match(result, /PROJECT_MIGRATION_REQUIRED/);
  assert.match(result, /\/api\/projects\/consolidation\/census/, 'Essential supported inspection cannot depend on a relative guide in the foreign repository');
  assert.match(tool.description, /list.*get|get.*list/, 'Shipped tool description must expose the project-free preflight actions');
});

test('context recovery: default workspace inbox remains scoped and held on unknown identity', async () => {
  const { tool, calls } = fixture();
  const result = await tool.execute('recovery-default-inbox', { action: 'list' });
  assert.deepEqual(calls, ['resolve']);
  assert.match(result, /PROJECT_MIGRATION_REQUIRED/);
  assert.match(result, /--check/);
  assert.doesNotMatch(result, /No issues found/);
});

test('context recovery: historical migration names the existing encoded legacy reader', async () => {
  const { tool } = fixture();
  const result = await tool.execute('legacy-held', { action: 'create', title: 'Held', target_project: target });
  assert.match(result, /GET \/api\/projects\/resolve\?legacy_id=/);
  assert.match(result, /URI-encoded/);
  assert.match(result, /not a verified server binding/);
  assert.match(result, /unresolved is not a mapping/);
  assert.match(tool.description, /legacy_marker/);
  assert.match(tool.description, /legacy_id=/);
});

for (const code of ['PROJECT_IDENTITY_INVALID', 'PROJECT_FUTURE_REFUSAL']) {
  test(`context recovery: ${code} protects invalid input without a repeated check loop`, async () => {
    const { tool, client, calls } = fixture();
    client.resolveWorkspaceProject = async () => { calls.push('resolve'); throw new Error(code); };
    const result = await tool.execute('invalid-held', { action: 'create', title: 'Held', target_project: target });
    assert.deepEqual(calls, ['resolve']);
    assert.match(result, new RegExp(code));
    assert.match(result, /Preserve/);
    assert.match(result, /4096/);
    assert.match(result, /owner/);
    assert.doesNotMatch(result, /--check/);
    assert.doesNotMatch(result, /No issues found/);
  });
}
