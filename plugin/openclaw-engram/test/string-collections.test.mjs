import assert from 'node:assert/strict';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import test from 'node:test';

const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED;
const rememberModule = await import(compiled ? pathToFileURL(path.join(compiled, 'tools/engram-remember.js')).href : '../dist/tools/engram-remember.js');
const issuesModule = await import(compiled ? pathToFileURL(path.join(compiled, 'tools/engram-issues.js')).href : '../dist/tools/engram-issues.js');

const project = 'b42c9854-bf02-4d1b-867d-a2a096f9e4d0';
const ctx = { agentId: 'collection-agent', workspaceDir: 'synthetic-workspace', sessionId: 'collection-session' };

function createClient() {
  const calls = [];
  return {
    calls,
    client: {
      isAvailable() {
        calls.push({ method: 'isAvailable' });
        return true;
      },
      async resolveWorkspaceProject(agentId, workspaceDir) {
        calls.push({ method: 'resolveWorkspaceProject', agentId, workspaceDir });
        return { projectId: project };
      },
      async bulkImport(observations, sessionId) {
        calls.push({ method: 'bulkImport', observations, sessionId });
        return { imported: 1, skipped_duplicates: 0 };
      },
      async createIssue(issue) {
        calls.push({ method: 'createIssue', issue });
        return { id: 1 };
      },
    },
  };
}

const cases = [
  {
    name: 'engram_remember',
    create: rememberModule.createEngramRememberTool,
    field: 'tags',
    params: { title: 'Collection title', content: 'Collection content' },
    success: /^Stored:/,
    expected: (tags) => ({
      method: 'bulkImport',
      observations: [{ title: 'Collection title', content: 'Collection content', type: 'change', scope: 'project', project, tags }],
      sessionId: ctx.sessionId,
    }),
  },
  {
    name: 'memory_store',
    create: rememberModule.createMemoryStoreTool,
    field: 'tags',
    params: { text: 'Collection content' },
    success: /^Stored:/,
    expected: (tags) => ({
      method: 'bulkImport',
      observations: [{ title: 'Collection content', content: 'Collection content', type: 'change', scope: 'project', project, tags }],
      sessionId: ctx.sessionId,
    }),
  },
  {
    name: 'engram_issues',
    create: issuesModule.createEngramIssuesTool,
    field: 'labels',
    params: { action: 'create', title: 'Collection title' },
    success: /^Issue created:/,
    expected: (labels) => ({
      method: 'createIssue',
      issue: { title: 'Collection title', body: undefined, priority: 'medium', source_project: project, target_project: project, source_agent: ctx.agentId, labels },
    }),
  },
];

for (const scenario of cases) {
  test(`${scenario.name}.${scenario.field} stops reading after the first invalid element`, async (t) => {
    const { client, calls } = createClient();
    let followingReads = 0;
    const values = [42];
    Object.defineProperty(values, 1, {
      enumerable: true,
      get() {
        followingReads++;
        return 43;
      },
    });

    const tool = scenario.create(ctx, client, {});
    const result = await tool.execute('invalid-collection', { ...scenario.params, [scenario.field]: values });

    assert.match(result, /^Invalid parameters:/);
    assert.deepEqual(calls, [], 'invalid collection must not check backend availability, resolve a project or send a request');
    t.diagnostic(JSON.stringify({ followingReads, validationResponse: result }));
    assert.equal(followingReads, 0, 'validation must stop before reading the element after the first invalid type');
  });

  for (const [name, values] of [['omitted', undefined], ['empty', []], ['ordinary', ['alpha', '', 'alpha']]]) {
    test(`${scenario.name}.${scenario.field} preserves ${name} values and default payload`, async () => {
      const { client, calls } = createClient();
      const tool = scenario.create(ctx, client, {});
      const params = values === undefined ? scenario.params : { ...scenario.params, [scenario.field]: values };

      assert.match(await tool.execute('valid-collection', params), scenario.success);
      assert.deepEqual(calls, [
        { method: 'isAvailable' },
        { method: 'resolveWorkspaceProject', agentId: ctx.agentId, workspaceDir: ctx.workspaceDir },
        scenario.expected(values),
      ]);
    });
  }
}
