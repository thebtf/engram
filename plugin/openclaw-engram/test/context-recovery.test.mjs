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
  assert.deepEqual(calls, [['list', { project: target, status: 'open,acknowledged,reopened', limit: 20 }]], 'Project-free authorized inspection must not hit the failing canonical resolver');
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
