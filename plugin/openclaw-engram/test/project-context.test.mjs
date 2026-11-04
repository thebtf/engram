import assert from 'node:assert/strict';
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';

const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED || fileURLToPath(new URL('../dist/', import.meta.url));
const imported = await import(pathToFileURL(path.join(compiled, 'index.js')));
const plugin = imported.default.default ?? imported.default;
const manifest = JSON.parse(fs.readFileSync(new URL('../openclaw.plugin.json', import.meta.url), 'utf8'));
const sessionId = '44444444-4444-4444-8444-444444444444';
const environmentSession = '55555555-5555-4555-8555-555555555555';
const token = 'synthetic-project-context-keycard-only';
const choices = [
  { project_id: '11111111-1111-4111-8111-111111111111', name: 'Retained service', repository_qualifier: 'example.test/team/retained', selection_ref: `engram-project-choice-v1:${'a'.repeat(64)}` },
  { project_id: '22222222-2222-4222-8222-222222222222', name: 'Recovered service', repository_qualifier: 'example.test/team/recovered', selection_ref: `engram-project-choice-v1:${'b'.repeat(64)}` },
];
const text = result => typeof result === 'string' ? result : Array.isArray(result?.content)
  ? result.content.filter(item => item.type === 'text').map(item => item.text).join('\n') : JSON.stringify(result);

// This is the real registration/factory and REST client, with a controlled Node
// executable standing in for the shared Go CLI. It proves adapter behavior only;
// it neither starts an installed OpenClaw host nor proves Go authorization.
async function fixture(t, { workspace = true, hostSession = sessionId, catalog = choices } = {}) {
  const root = fs.mkdtempSync(path.join(compiled, '.project-context-fixture-'));
  const workspaceDir = path.join(root, 'actual caller workspace');
  const configuredWorkspace = path.join(root, 'not the caller workspace');
  fs.mkdirSync(workspaceDir);
  fs.mkdirSync(configuredWorkspace);
  const marker = '{ "id": "historical-service", "name": "Ambiguous old label" }\n';
  fs.writeFileSync(path.join(workspaceDir, '.engram-project'), marker);
  const cli = [], issues = [], errors = [], messages = [], registrations = [];
  const state = { selected: undefined, denied: false, stale: false };
  const previousEnv = new Map();
  let server;
  t.after(async () => {
    for (const [key, value] of previousEnv) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
    if (server?.listening) await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
    t.diagnostic(JSON.stringify({ cli: cli.map(call => ({ action: call.args[1], actualWorkspace: call.root === workspaceDir })), issueRequests: issues.length, fixtureErrors: errors }));
    fs.rmSync(root, { recursive: true, force: true });
  });
  const fail = code => ({ exitCode: 1, payload: { code, guidance: 'Use engram_project_context choices, then select an offered selection_ref and retry the original operation.' } });
  server = http.createServer(async (request, response) => {
    response.setHeader('Content-Type', 'application/json');
    try {
      assert.equal(request.headers.authorization, `Bearer ${token}`, 'Only the synthetic keycard may reach the fixture');
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : undefined;
      const url = new URL(request.url, 'http://127.0.0.1');
      if (url.pathname === '/fixture/project' && request.method === 'POST') {
        cli.push(body);
        assert.equal(body.root, workspaceDir, 'CLI must use ctx.workspaceDir, not cwd, config.workspaceDir or a tool argument');
        assert.equal(body.args[0], 'project');
        assert.ok(['resolve', 'choices', 'select'].includes(body.args[1]), 'No shell, init or migration command is allowed');
        assert.ok(body.args.includes('--json'));
        const allowed = new Set(['project', body.args[1], '--root', workspaceDir, '--json', '--selection-ref', body.selection_ref]);
        assert.ok(body.args.every(value => allowed.has(value)), 'CLI args cannot carry a project override, credentials or invented session');
        let outcome;
        if (body.args[1] === 'resolve') outcome = state.selected
          ? { exitCode: 0, payload: { project_id: state.selected.project_id, name: state.selected.name } }
          : fail('PROJECT_MIGRATION_REQUIRED');
        else if (state.denied) outcome = fail('PROJECT_RESOLUTION_UNAVAILABLE');
        else if (body.args[1] === 'choices') outcome = { exitCode: 0, payload: { choices: catalog } };
        else if (state.stale || !catalog.some(choice => choice.selection_ref === body.selection_ref)) outcome = fail('PROJECT_SELECTION_STALE');
        else {
          state.selected = catalog.find(choice => choice.selection_ref === body.selection_ref);
          outcome = { exitCode: 0, payload: { project_id: state.selected.project_id, name: state.selected.name } };
        }
        response.end(JSON.stringify(outcome));
        return;
      }
      assert.equal(url.pathname, '/api/issues', 'Only the original issue operation may reach REST');
      assert.equal(request.method, 'POST');
      assert.ok(state.selected, 'The held issue must not be sent before explicit selection');
      assert.equal(body.source_project, state.selected.project_id);
      issues.push(body);
      response.end(JSON.stringify({ id: 508, message: 'created' }));
    } catch (error) {
      errors.push(error.message);
      response.statusCode = 500;
      response.end(JSON.stringify({ code: 'FIXTURE_CONTRACT_ERROR' }));
    }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const url = `http://127.0.0.1:${server.address().port}`;
  const executable = path.join(root, 'synthetic-engram-cli.mjs');
  fs.writeFileSync(executable, `
import path from 'node:path';
const args = process.argv.slice(1);
args[0] = path.basename(args[0]);
const value = flag => args[args.indexOf(flag) + 1];
const endpoint = new URL('/fixture/project', process.env.ENGRAM_PROJECT_CONTEXT_FIXTURE_URL);
if (endpoint.hostname !== '127.0.0.1' || endpoint.protocol !== 'http:') throw new Error('Fixture requires loopback');
const response = await fetch(endpoint, {
  method: 'POST', redirect: 'error',
  headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + process.env.ENGRAM_TOKEN },
  body: JSON.stringify({ args, root: value('--root'), ...(args.includes('--selection-ref') ? { selection_ref: value('--selection-ref') } : {}) }),
});
const result = await response.json();
process.stdout.write(JSON.stringify(result.payload));
process.exit(result.exitCode ?? 2);
`);
  for (const [key, value] of Object.entries({
    ENGRAM_EXECUTABLE: process.execPath,
    NODE_OPTIONS: `--import=${pathToFileURL(executable).href}`,
    ENGRAM_PROJECT_CONTEXT_FIXTURE_URL: url,
    ENGRAM_URL: url,
    ENGRAM_TOKEN: token,
    ENGRAM_SESSION_ID: environmentSession,
    OPENCLAW_SESSION_ID: environmentSession,
    PI_SESSION_ID: environmentSession,
  })) {
    previousEnv.set(key, process.env[key]);
    process.env[key] = value;
  }
  const logger = Object.fromEntries(['info', 'warn', 'error', 'debug'].map(level => [level, message => messages.push(message)]));
  plugin.register({
    config: {}, pluginConfig: { url, token, autoExtract: false, workspaceDir: configuredWorkspace }, logger,
    on() { }, registerTool(factory, options) { registrations.push({ factory, options }); },
    registerCommand() { }, registerService() { }, registerCli() { },
  });
  const ctx = {
    agentId: 'project-context-fixture', sessionKey: 'agent:project-context-fixture:main',
    ...(hostSession ? { sessionId: hostSession } : {}), ...(workspace ? { workspaceDir } : {})
  };
  const tools = (await Promise.all(registrations.map(entry => entry.factory(ctx)))).flat().filter(Boolean);
  const find = name => {
    const tool = tools.find(item => item.name === name);
    assert.ok(tool, `Ordinary registered OpenClaw tool is missing: ${name}`);
    return tool;
  };
  function privateOutput(result) {
    const visible = text(result);
    for (const secret of [token, 'Bearer ', workspaceDir, configuredWorkspace, environmentSession]) {
      assert.ok(!visible.includes(secret), 'User-visible recovery must not disclose credentials, private paths or environment-derived session');
    }
    assert.deepEqual(errors, [], 'A fixture contract error is not behavioral RED');
    return visible;
  }
  return { find, tools, registrations, state, cli, issues, messages, workspaceDir, marker, privateOutput };
}

test('project context: declared contract and real factory expose the ordinary choices/select tool', async t => {
  const f = await fixture(t);
  const tool = f.find('engram_project_context');
  assert.ok(manifest.contracts.tools.includes(tool.name), 'The host manifest must declare the public recovery tool');
  assert.ok(f.registrations.some(entry => entry.options?.names.includes(tool.name)), 'The registrar must advertise the tool to ordinary callers');
  const actions = tool.parameters.properties.action.anyOf.map(option => option.const);
  for (const action of ['choices', 'select']) assert.ok(actions.includes(action), `Missing public action: ${action}`);
  assert.ok(tool.parameters.properties.selection_ref, 'Selection consumes the opaque catalog reference');
  for (const field of ['project', 'project_id', 'workspaceDir', 'root', 'token', 'session_id']) {
    assert.equal(tool.parameters.properties[field], undefined, `Caller context must not be invented through ${field}`);
  }
  assert.match(tool.description, /choices/);
  assert.match(tool.description, /select/);
  assert.match(tool.description, /workspace|caller/i);
});

test('project context: ambiguous issue caller chooses an existing project and retries its unchanged operation', async t => {
  const f = await fixture(t);
  const issue = f.find('engram_issues');
  const original = Object.freeze({ action: 'create', title: 'Resume retained issue after explicit project choice', body: 'Preserve the original operation.', priority: 'high' });
  const held = f.privateOutput(await issue.execute('held-original-issue', original));
  assert.match(held, /PROJECT_MIGRATION_REQUIRED/, 'The real adapter must encounter CLI ambiguity before recovery');
  assert.equal(f.cli.length, 1, 'The controlled executable was actually invoked');
  assert.deepEqual(f.issues, [], 'A held operation must not send an issue write');
  t.diagnostic('Real registered engram_issues observed PROJECT_MIGRATION_REQUIRED through the controlled executable; no issue POST was sent.');
  const context = f.find('engram_project_context');
  assert.match(held, /engram_project_context/, 'The refusal must name an ordinary supported host recovery tool');
  const listed = f.privateOutput(await context.execute('project-choices', { action: 'choices' }));
  for (const choice of choices) for (const value of Object.values(choice)) assert.ok(listed.includes(value), 'Choices must retain each authorized name, UUID, repository qualifier and opaque reference');
  assert.equal(f.state.selected, undefined, 'Listing choices is read-only');
  const selected = f.privateOutput(await context.execute('project-select', { action: 'select', selection_ref: choices[1].selection_ref }));
  assert.doesNotMatch(selected, /PROJECT_(?:CONTEXT_REQUIRED|MIGRATION_REQUIRED|SELECTION_STALE|SELECTION_INVALID|RESOLUTION_UNAVAILABLE)/);
  assert.doesNotMatch(selected, /failed|invalid|unavailable/i);
  assert.equal(f.state.selected?.project_id, choices[1].project_id);
  const retried = f.privateOutput(await issue.execute('retry-original-issue', original));
  assert.match(retried, /Issue created/);
  assert.equal(f.issues.length, 1, 'Only the retried original operation may write');
  assert.equal(f.issues[0].source_project, choices[1].project_id);
  assert.equal(f.issues[0].target_project, choices[1].project_id);
  assert.equal(f.issues[0].created_by_session, sessionId, 'Attribution stays the actual per-call host session');
  assert.equal(f.issues[0].client, 'openclaw');
  for (const key of ['title', 'body', 'priority']) assert.equal(f.issues[0][key], original[key]);
  assert.deepEqual(f.cli.map(call => call.args[1]), ['resolve', 'choices', 'select', 'resolve']);
  assert.equal(fs.readFileSync(path.join(f.workspaceDir, '.engram-project'), 'utf8'), f.marker, 'Selection must not replace the historical marker');
});

test('project context: one-letter repository host with a nondefault port retains its full authorized choice and usable selection_ref', async t => {
  const portChoice = { ...choices[1], repository_qualifier: 'a:2222/team/repo' };
  const catalog = [choices[0], portChoice];
  const f = await fixture(t, { catalog });
  const context = f.find('engram_project_context');
  const listed = f.privateOutput(await context.execute('one-letter-host-choices', { action: 'choices' }));
  assert.ok(listed.includes(portChoice.repository_qualifier),
    `Canonical repository qualifier a:2222/team/repo must not be mistaken for a Windows drive prefix: ${listed}`);
  for (const choice of catalog) {
    for (const value of Object.values(choice)) {
      assert.ok(listed.includes(value), 'The full authorized name, UUID, repository qualifier and exact selection_ref must remain visible');
    }
  }
  assert.equal(f.state.selected, undefined, 'Offering the valid qualifier must not automatically choose its project');
  assert.deepEqual(f.cli.map(call => call.args[1]), ['choices']);
  const selected = f.privateOutput(await context.execute('one-letter-host-select', {
    action: 'select', selection_ref: portChoice.selection_ref,
  }));
  assert.match(selected, /ok: true/);
  assert.ok(selected.includes(portChoice.project_id), 'Selection must confirm the existing project associated with the displayed qualifier');
  assert.ok(selected.includes(portChoice.name));
  assert.deepEqual(f.state.selected, portChoice, 'The offered full choice must be selectable, not merely accepted by a generic schema check');
  assert.deepEqual(f.cli.map(call => call.args[1]), ['choices', 'select']);
  assert.equal(f.cli[1].selection_ref, portChoice.selection_ref, 'Forward the exact opaque reference shown to the caller');
  assert.deepEqual(f.issues, [], 'Context choices/select must not invent or replay an issue operation');
  assert.equal(fs.readFileSync(path.join(f.workspaceDir, '.engram-project'), 'utf8'), f.marker);
});

for (const action of ['choices', 'select']) {
  test(`project context: ${action} without ctx.workspaceDir refuses without cwd or configured-workspace fallback`, async t => {
    const f = await fixture(t, { workspace: false });
    const result = f.privateOutput(await f.find('engram_project_context').execute('no-workspace', { action, ...(action === 'select' ? { selection_ref: choices[0].selection_ref } : {}) }));
    assert.match(result, /PROJECT_CONTEXT_REQUIRED/);
    assert.deepEqual(f.cli, [], 'Absent actual workspace must fail before launching a resolver');
    assert.deepEqual(f.issues, []);
  });
}

for (const scenario of ['stale', 'unknown']) {
  test(`project context: ${scenario} opaque selection stays explicit and leaves the original issue held`, async t => {
    const f = await fixture(t);
    const context = f.find('engram_project_context');
    const listed = f.privateOutput(await context.execute('choices-before-refusal', { action: 'choices' }));
    assert.ok(listed.includes(choices[0].selection_ref));
    f.state.stale = scenario === 'stale';
    const selection_ref = scenario === 'stale' ? choices[0].selection_ref : `engram-project-choice-v1:${'e'.repeat(64)}`;
    const refused = f.privateOutput(await context.execute('rejected-selection', { action: 'select', selection_ref }));
    assert.match(refused, /PROJECT_SELECTION_(?:STALE|INVALID)/);
    assert.equal(f.state.selected, undefined, 'A stale/unknown selector must not choose a project');
    const held = f.privateOutput(await f.find('engram_issues').execute('still-held', { action: 'create', title: 'Still held after rejected selection' }));
    assert.match(held, /PROJECT_MIGRATION_REQUIRED/);
    assert.deepEqual(f.issues, []);
  });
}

test('project context: revoked catalog authorization does not reuse visible choices or claim selection success', async t => {
  const f = await fixture(t, { hostSession: null });
  const context = f.find('engram_project_context');
  f.privateOutput(await context.execute('authorized-choices', { action: 'choices' }));
  f.state.denied = true;
  for (const params of [{ action: 'choices' }, { action: 'select', selection_ref: choices[1].selection_ref }]) {
    const refused = f.privateOutput(await context.execute('catalog-now-denied', params));
    assert.match(refused, /PROJECT_RESOLUTION_UNAVAILABLE|unauthori[sz]ed|forbidden|permission denied/i);
    for (const choice of choices) assert.ok(!refused.includes(choice.name), 'A denied catalog must not expose previously cached labels');
  }
  assert.equal(f.state.selected, undefined);
  assert.deepEqual(f.issues, []);
});
