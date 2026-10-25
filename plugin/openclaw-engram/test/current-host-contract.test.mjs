import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';

// Exact official npm runtime, unpacked without installing or starting OpenClaw.
const host = process.env.ENGRAM_OPENCLAW_HOST_FIXTURE;
const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED;
const enabled = Boolean(host && compiled);
const project = '11111111-1111-4111-8111-111111111111';
const prompt = 'Recall the synthetic compiler decision before continuing this implementation.';
const observation = { id: 101, type: 'decision', title: 'Synthetic compiler decision', narrative: 'Use the synthetic incremental compiler.', similarity: 0.95 };
const sessionObservation = { id: 102, type: 'decision', title: 'Synthetic session memory', narrative: 'Preserve the synthetic session-only safety decision.' };

async function fixture(t, allowConversationAccess = true, configuredProject = project) {
  assert.equal(JSON.parse(fs.readFileSync(path.join(host, 'package.json'), 'utf8')).version, '2026.10.7');
  const { t: createPluginRegistry } = await import(pathToFileURL(path.join(host, 'dist/registry-BwDJiK-z.mjs')));
  const { t: createHookRunner } = await import(pathToFileURL(path.join(host, 'dist/hooks-DJ64650m.mjs')));
  const imported = await import(pathToFileURL(path.join(compiled, 'index.js')));
  const plugin = imported.default.default ?? imported.default;
  const { r: loadPluginManifest } = await import(pathToFileURL(path.join(host, 'dist/manifest-D9tOzTAf.mjs')));
  const loaded = loadPluginManifest(fileURLToPath(new URL('../', import.meta.url)));
  assert.equal(loaded.ok, true, loaded.error);
  const manifest = loaded.manifest;
  const requests = [];
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    const target = new URL(String(url));
    assert.equal(target.origin, 'http://127.0.0.1:9', 'No live Engram endpoint is allowed');
    const body = JSON.parse(options.body);
    requests.push({ route: target.pathname, body });
    let payload;
    if (target.pathname === '/api/sessions/123/mark-injected') {
      assert.deepEqual(body.ids, [observation.id], 'Only per-turn search uses mark-injected; static exposure belongs to context/inject');
      payload = {};
    } else {
      assert.ok([project, '22222222-2222-4222-8222-222222222222'].includes(body.project));
      assert.ok(['/api/context/search', '/api/context/inject', '/api/sessions/init'].includes(target.pathname), `Unexpected REST operation: ${target.pathname}`);
      payload = target.pathname === '/api/context/search' ? { observations: [observation] }
        : target.pathname === '/api/context/inject' ? { project: body.project, strategy: 'legacy', observations: [{ ...sessionObservation, narrative: `${sessionObservation.narrative} Project ${body.project}.` }] }
          : { sessionDbId: 123, promptNumber: 1 };
    }
    return new Response(JSON.stringify(payload), { status: 200, headers: { 'Content-Type': 'application/json' } });
  });
  const messages = [];
  const logger = Object.fromEntries(['info', 'warn', 'error', 'debug'].map(level => [level, message => messages.push({ level, message })]));
  const runtime = { config: { current: () => ({}) } };
  const registration = createPluginRegistry({ runtime, logger, activateGlobalSideEffects: false, coreGatewayMethodNames: [] });
  const record = {
    id: manifest.id, name: manifest.displayName, version: manifest.version, kind: manifest.kind,
    source: path.join(compiled, 'index.js'), rootDir: path.dirname(compiled), origin: 'global',
    enabled: true, explicitlyEnabled: true, status: 'loaded', configSchema: true,
    contracts: manifest.contracts, memorySlotSelected: true, httpRoutes: 0, hookCount: 0,
  };
  for (const field of ['toolNames', 'hookNames', 'channelIds', 'cliBackendIds', 'providerIds', 'embeddingProviderIds', 'speechProviderIds', 'realtimeTranscriptionProviderIds', 'realtimeVoiceProviderIds', 'mediaUnderstandingProviderIds', 'transcriptSourceProviderIds', 'imageGenerationProviderIds', 'videoGenerationProviderIds', 'musicGenerationProviderIds', 'webFetchProviderIds', 'webSearchProviderIds', 'migrationProviderIds', 'agentHarnessIds', 'cliCommands', 'services', 'gatewayDiscoveryServiceIds', 'commands']) record[field] = [];
  registration.registry.plugins.push(record);
  const api = registration.createApi(record, {
    config: {},
    pluginConfig: { url: 'http://127.0.0.1:9', token: 'synthetic-fixture-only', ...(configuredProject ? { project: configuredProject } : {}), autoExtract: false, workspaceDir: path.dirname(compiled) },
    hookPolicy: { allowConversationAccess },
  });
  // Real host API/registrars; services, CLI callbacks, gateway and providers are never started.
  plugin.register(api);
  const runner = createHookRunner(registration.registry, { catchErrors: false, logger });
  const ctx = { agentId: 'synthetic-agent', sessionId: 'synthetic-session', sessionKey: 'agent:synthetic-agent:synthetic-session', workspaceDir: path.dirname(compiled) };
  return { registration, runner, ctx, requests, messages, manifest };
}

const options = { skip: enabled ? false : 'Set ENGRAM_OPENCLAW_HOST_FIXTURE to official openclaw@2026.10.7 and ENGRAM_OPENCLAW_TEST_COMPILED to this adapter build' };

test('current official registrar ignores obsolete hook without aborting plugin registration', options, async t => {
  const { registration, runner } = await fixture(t);
  assert.equal(runner.hasHooks('before_agent_start'), false);
  for (const item of registration.registry.diagnostics) t.diagnostic(`${item.level}: ${item.message}`);
  for (const hook of ['session_start', 'before_prompt_build', 'before_tool_call', 'after_tool_call', 'before_compaction', 'session_end']) assert.equal(runner.hasHooks(hook), true, hook);
  assert.ok(registration.registry.services.length > 0, 'Registration continues beyond the obsolete hook');
});

test('supported prompt hook still delivers query-matched context with explicit conversation access', options, async t => {
  const { runner, ctx, requests } = await fixture(t);
  const messages = Object.freeze([{ role: 'user', content: prompt }]);
  const result = await runner.runBeforePromptBuild({ prompt, messages, currentUserMessage: prompt }, ctx);
  assert.match(result?.prependContext ?? '', /synthetic incremental compiler/);
  assert.ok(requests.some(request => request.route === '/api/context/search' && request.body.query === prompt));
  assert.deepEqual(messages, [{ role: 'user', content: prompt }]);
});

test('automatic context includes session-level memory through a supported current hook', options, async t => {
  const { runner, ctx, requests } = await fixture(t);
  await runner.runSessionStart({ sessionId: ctx.sessionId }, { agentId: ctx.agentId, sessionId: ctx.sessionId, sessionKey: ctx.sessionKey });
  const result = await runner.runBeforePromptBuild({ prompt, messages: [], currentUserMessage: prompt }, ctx);
  await new Promise(resolve => setImmediate(resolve));
  assert.ok(requests.some(request => request.route === '/api/sessions/init'), 'Session tracking must remain functional');
  const delivered = [result?.prependContext, result?.appendContext, result?.prependSystemContext, result?.appendSystemContext].filter(Boolean).join('\n');
  assert.match(delivered, /synthetic session-only safety decision/, 'Session-level memory is lost when before_agent_start is ignored');
  const inject = requests.find(request => request.route === '/api/context/inject');
  assert.deepEqual(inject.body, { agent_id: ctx.agentId, cwd: ctx.workspaceDir, project, session_id: ctx.sessionId });
  assert.ok(requests.filter(request => request.route === '/api/sessions/init').every(request => request.body.claudeSessionId === ctx.sessionId));
});

test('static context without a host session does not invent conversation attribution', options, async t => {
  const { runner, ctx, requests } = await fixture(t);
  delete ctx.sessionId;
  const result = await runner.runBeforePromptBuild({ prompt: '', messages: [] }, ctx);
  await new Promise(resolve => setImmediate(resolve));
  assert.match(result.appendSystemContext, /synthetic session-only safety decision/);
  assert.deepEqual(requests.map(request => request.route), ['/api/context/inject']);
  assert.deepEqual(requests[0].body, { agent_id: ctx.agentId, cwd: ctx.workspaceDir, project });
});

test('failed static context request stays nonblocking and retries with the actual host session', options, async t => {
  const { runner, ctx, requests } = await fixture(t);
  const fetch = globalThis.fetch;
  let failedBody;
  t.mock.method(globalThis, 'fetch', async (url, init) => {
    assert.equal(new URL(String(url)).pathname, '/api/context/inject');
    failedBody = JSON.parse(init.body);
    return new Response('synthetic unavailable', { status: 503 });
  });
  const event = { prompt: '', messages: [] };
  assert.equal(await runner.runBeforePromptBuild(event, ctx), undefined);
  assert.deepEqual(failedBody, { agent_id: ctx.agentId, cwd: ctx.workspaceDir, project, session_id: ctx.sessionId });
  assert.deepEqual(requests, [], 'A failed static request must not initialize or mark a session');
  t.mock.method(globalThis, 'fetch', fetch);
  const retried = await runner.runBeforePromptBuild(event, ctx);
  assert.match(retried.appendSystemContext, /synthetic session-only safety decision/);
});

test('current host exposes the registered memory search tool and its useful result', options, async t => {
  const { registration, ctx, manifest } = await fixture(t);
  const tools = (await Promise.all(registration.registry.tools.map(entry => entry.factory(ctx)))).flat().filter(Boolean);
  const search = tools.find(tool => tool.name === 'engram_search');
  assert.ok(search, `Memory search unavailable: ${registration.registry.diagnostics.map(item => item.message).join('; ')}`);
  assert.deepEqual(tools.map(tool => tool.name).sort(), [...manifest.contracts.tools].sort());
  assert.deepEqual(registration.registry.tools.flatMap(entry => entry.names).sort(), [...manifest.contracts.tools].sort());
  const { a: toToolDefinitions } = await import(pathToFileURL(path.join(host, 'dist/agent-tool-definition-adapter-CNc5GutA.mjs')));
  const [definition] = toToolDefinitions([search]);
  const result = await definition.execute('synthetic-call', { query: prompt });
  assert.ok(Array.isArray(result.content), 'The actual host adapter must produce an AgentToolResult content array');
  assert.match(result.content.filter(item => item.type === 'text').map(item => item.text).join('\n'), /synthetic incremental compiler/);
});

test('non-bundled prompt access is not silently granted by the host', options, async t => {
  const { runner, registration } = await fixture(t, false);
  assert.equal(runner.hasHooks('before_prompt_build'), false);
  assert.ok(registration.registry.diagnostics.some(item => item.message.includes('allowConversationAccess=true')));
});

test('same-session static context follows canonical binding changes and fails closed', options, async t => {
  const imported = await import(pathToFileURL(path.join(compiled, 'client.js')));
  const { EngramRestClient } = imported.default ?? imported;
  let resolvedProject = project;
  t.mock.method(EngramRestClient.prototype, 'resolveWorkspaceProject', async () => {
    if (!resolvedProject) throw new Error('PROJECT_RESOLUTION_UNAVAILABLE');
    return { projectId: resolvedProject, agentId: 'synthetic-agent' };
  });
  const { runner, ctx, requests } = await fixture(t, true, null);
  const event = { prompt, messages: [], currentUserMessage: prompt };
  const first = await runner.runBeforePromptBuild(event, ctx);
  assert.match(first.appendSystemContext, new RegExp(project));
  resolvedProject = '22222222-2222-4222-8222-222222222222';
  const next = await runner.runBeforePromptBuild(event, ctx);
  assert.match(next.appendSystemContext, new RegExp(resolvedProject));
  assert.doesNotMatch(next.appendSystemContext, new RegExp(project));
  resolvedProject = undefined;
  const requestCount = requests.length;
  const refused = await runner.runBeforePromptBuild(event, ctx);
  assert.equal(refused, undefined, 'No prior project context may survive failed current identity resolution');
  assert.equal(requests.length, requestCount);
});

test('empty session context is retried rather than permanently cached', options, async t => {
  const { runner, ctx } = await fixture(t);
  const fetch = globalThis.fetch;
  let empty = true;
  t.mock.method(globalThis, 'fetch', async (url, init) => {
    if (new URL(String(url)).pathname === '/api/context/inject' && empty) {
      empty = false;
      return new Response(JSON.stringify({ observations: [] }), { status: 200 });
    }
    return fetch(url, init);
  });
  const event = { prompt, messages: [], currentUserMessage: prompt };
  const first = await runner.runBeforePromptBuild(event, ctx);
  assert.equal(first.appendSystemContext, undefined);
  const next = await runner.runBeforePromptBuild(event, ctx);
  assert.match(next.appendSystemContext, /synthetic session-only safety decision/);
});
