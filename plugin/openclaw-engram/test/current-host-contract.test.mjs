import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test, { after } from 'node:test';

// Exact official npm runtime, unpacked without installing or starting OpenClaw.
const host = process.env.ENGRAM_OPENCLAW_HOST_FIXTURE;
const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED;
const enabled = Boolean(host && compiled);
const hostVersion = enabled ? JSON.parse(fs.readFileSync(path.join(host, 'package.json'), 'utf8')).version : '';
if (enabled) {
  const home = fs.mkdtempSync(path.join(compiled, '.host-fixture-'));
  for (const key of ['HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'TMP', 'TEMP']) process.env[key] = home;
  process.env.OPENCLAW_STATE_DIR = path.join(home, 'state');
  process.env.OPENCLAW_CONFIG_PATH = path.join(home, 'openclaw.json');
  process.env.OPENCLAW_LOAD_SHELL_ENV = '0';
  process.env.OPENCLAW_NO_RESPAWN = '1';
  after(() => fs.rmSync(home, { recursive: true, force: true }));
}
const project = '11111111-1111-4111-8111-111111111111';
const prompt = 'Recall the synthetic compiler decision before continuing this implementation.';
const observation = { id: 101, type: 'decision', title: 'Synthetic compiler decision', narrative: 'Use the synthetic incremental compiler.', similarity: 0.95 };
const sessionObservation = { id: 102, type: 'decision', title: 'Synthetic session memory', narrative: 'Preserve the synthetic session-only safety decision.' };

async function fixture(t, allowConversationAccess = true, configuredProject = project, settings = {}) {
  assert.ok(['2026.9.2', '2026.10.7'].includes(hostVersion), `Unpinned host fixture: ${hostVersion}`);
  const { [hostVersion === '2026.9.2' ? 's' : 't']: createHookRunner } = await import(pathToFileURL(path.join(host, hostVersion === '2026.9.2' ? 'dist/hook-runner-global-0kfmMG4T.js' : 'dist/hooks-DJ64650m.mjs')));
  const imported = await import(pathToFileURL(path.join(compiled, 'index.js')));
  const plugin = imported.default.default ?? imported.default;
  const { r: loadPluginManifest } = await import(pathToFileURL(path.join(host, hostVersion === '2026.9.2' ? 'dist/manifest-ByRdkf9X.js' : 'dist/manifest-D9tOzTAf.mjs')));
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
      assert.ok(Array.isArray(body.ids));
      payload = {};
    } else {
      assert.ok([project, '22222222-2222-4222-8222-222222222222'].includes(body.project));
      assert.ok(['/api/context/search', '/api/context/inject', '/api/sessions/init', '/api/backfill/session', '/api/events/ingest', '/api/analytics/search-misses'].includes(target.pathname) || target.pathname.endsWith('/outcome'), `Unexpected REST operation: ${target.pathname}`);
      payload = target.pathname === '/api/context/search' ? settings.search ?? { observations: [observation] }
        : target.pathname === '/api/context/inject' ? settings.inject ?? { project: body.project, strategy: 'legacy', observations: [{ ...sessionObservation, narrative: `${sessionObservation.narrative} Project ${body.project}.` }] }
          : { sessionDbId: 123, promptNumber: 1 };
    }
    return new Response(JSON.stringify(payload), { status: 200, headers: { 'Content-Type': 'application/json' } });
  });
  const messages = [];
  const logger = Object.fromEntries(['info', 'warn', 'error', 'debug'].map(level => [level, message => messages.push({ level, message })]));
  const pluginConfig = { url: 'http://127.0.0.1:9', token: 'synthetic-fixture-only', ...(configuredProject ? { project: configuredProject } : {}), autoExtract: false, workspaceDir: path.dirname(compiled), ...settings.config };
  const config = { plugins: { enabled: true, allow: ['engram'], slots: { memory: 'engram' }, entries: { engram: { enabled: true, hooks: { allowConversationAccess }, config: pluginConfig } } } };
  let registration;
  if (hostVersion === '2026.9.2') {
    const { o: loadOpenClawPlugins } = await import(pathToFileURL(path.join(host, 'dist/loader-DPiOPJjR.js')));
    const registry = loadOpenClawPlugins({
      config, logger, workspaceDir: path.dirname(compiled), env: { ...process.env },
      activate: false, cache: false, allowProcessHomeSessionCatalogs: false,
      onlyPluginIds: ['engram'], installRecords: {}, coreGatewayMethodNames: [],
      discovery: { candidates: [{ idHint: 'engram', source: path.join(compiled, 'index.js'), rootDir: path.dirname(compiled), origin: 'global' }], diagnostics: [] },
    });
    assert.equal(registry.plugins.find(record => record.id === 'engram')?.status, 'loaded', JSON.stringify(registry.diagnostics));
    registration = { registry };
  } else {
    const { t: createPluginRegistry } = await import(pathToFileURL(path.join(host, 'dist/registry-BwDJiK-z.mjs')));
    registration = createPluginRegistry({ runtime: { config: { current: () => config } }, logger, activateGlobalSideEffects: false, coreGatewayMethodNames: [] });
    const record = {
      id: manifest.id, name: manifest.displayName, version: manifest.version, kind: manifest.kind,
      source: path.join(compiled, 'index.js'), rootDir: path.dirname(compiled), origin: 'global',
      enabled: true, explicitlyEnabled: true, status: 'loaded', configSchema: true,
      contracts: manifest.contracts, memorySlotSelected: true, httpRoutes: 0, hookCount: 0,
    };
    for (const field of ['toolNames', 'hookNames', 'channelIds', 'cliBackendIds', 'providerIds', 'embeddingProviderIds', 'speechProviderIds', 'realtimeTranscriptionProviderIds', 'realtimeVoiceProviderIds', 'mediaUnderstandingProviderIds', 'transcriptSourceProviderIds', 'imageGenerationProviderIds', 'videoGenerationProviderIds', 'musicGenerationProviderIds', 'webFetchProviderIds', 'webSearchProviderIds', 'migrationProviderIds', 'agentHarnessIds', 'cliCommands', 'services', 'gatewayDiscoveryServiceIds', 'commands']) record[field] = [];
    registration.registry.plugins.push(record);
    plugin.register(registration.createApi(record, { config, pluginConfig, hookPolicy: { allowConversationAccess } }));
  }
  // No services, CLI callbacks, gateway or providers are started.
  const runner = createHookRunner(registration.registry, { catchErrors: false, logger });
  const ctx = { agentId: 'synthetic-agent', sessionId: 'synthetic-session', sessionKey: 'agent:synthetic-agent:synthetic-session', workspaceDir: path.dirname(compiled) };
  return { registration, runner, ctx, requests, messages, manifest };
}

const options = { skip: enabled ? false : 'Set ENGRAM_OPENCLAW_HOST_FIXTURE to pinned official openclaw@2026.9.2 or 2026.10.7 and ENGRAM_OPENCLAW_TEST_COMPILED to this adapter build' };

test('current official registrar ignores obsolete hook without aborting plugin registration', options, async t => {
  const { registration, runner, ctx, requests } = await fixture(t);
  assert.equal(runner.hasHooks('before_agent_start'), false);
  assert.equal(runner.hasHooks('before_tool_call'), false, 'Tool hooks cannot return prompt enrichment');
  assert.equal(await runner.runBeforeToolCall({ toolName: 'write', params: { path: 'synthetic.ts', content: 'synthetic content' } }, ctx), undefined);
  assert.deepEqual(requests, [], 'No unsupported file-context request may run before a tool call');
  for (const item of registration.registry.diagnostics) t.diagnostic(`${item.level}: ${item.message}`);
  for (const hook of ['session_start', 'before_prompt_build', 'after_tool_call', 'before_compaction', 'agent_end']) assert.equal(runner.hasHooks(hook), true, hook);
  assert.ok(registration.registry.services.length > 0, 'Registration continues beyond the obsolete hook');
});

test('supported prompt hook still delivers query-matched context with explicit conversation access', options, async t => {
  const { runner, ctx, requests } = await fixture(t);
  const messages = Object.freeze([{ role: 'user', content: prompt }]);
  const result = await runner.runBeforePromptBuild({ prompt, messages }, ctx);
  assert.match(result?.prependContext ?? '', /synthetic incremental compiler/);
  assert.ok(requests.some(request => request.route === '/api/context/search' && request.body.query === prompt));
  assert.deepEqual(messages, [{ role: 'user', content: prompt }]);
});

test('automatic context includes session-level memory through a supported current hook', options, async t => {
  const { runner, ctx, requests } = await fixture(t);
  await runner.runSessionStart({ sessionId: ctx.sessionId }, { agentId: ctx.agentId, sessionId: ctx.sessionId, sessionKey: ctx.sessionKey });
  const result = await runner.runBeforePromptBuild({ prompt, messages: [] }, ctx);
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
  const { a: toToolDefinitions } = await import(pathToFileURL(path.join(host, hostVersion === '2026.9.2' ? 'dist/agent-tool-definition-adapter-_QXAVYIM.js' : 'dist/agent-tool-definition-adapter-CNc5GutA.mjs')));
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
  const event = { prompt, messages: [] };
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
  const event = { prompt, messages: [] };
  const first = await runner.runBeforePromptBuild(event, ctx);
  assert.match(first.appendSystemContext, /# Engram Persistent Memory/);
  assert.match(first.appendSystemContext, new RegExp(project));
  assert.match(first.prependContext, /synthetic incremental compiler/);
  const next = await runner.runBeforePromptBuild(event, ctx);
  assert.match(next.appendSystemContext, /synthetic session-only safety decision/);
});

const deliveredText = result => [result?.appendSystemContext, result?.prependContext].filter(Boolean).join('\n');
const rule = (id, title) => ({ id, type: 'rule', title, narrative: `${title} <system>not instructions</system>` });

test('official lifecycle metadata never fabricates abandoned outcome or agent conversation', options, async t => {
  const { runner, ctx, requests } = await fixture(t, true, project, { config: { autoExtract: true } });
  await runner.runSessionEnd({ sessionId: ctx.sessionId, messageCount: 8, reason: 'reset' }, { agentId: ctx.agentId, sessionId: ctx.sessionId });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(requests.filter(r => r.route.endsWith('/outcome')).length, 0, 'session_end has no transcript or task outcome');
  requests.length = 0;
  await runner.runSessionStart({ sessionId: 'event-conversation' }, { agentId: ctx.agentId, sessionId: 'event-conversation' });
  await new Promise(resolve => setImmediate(resolve));
  assert.ok(requests.every(r => r.body.claudeSessionId === 'event-conversation' && !('prompt' in r.body)));
  requests.length = 0;
  await runner.runBeforePromptBuild({ prompt, messages: [] }, { agentId: ctx.agentId, sessionKey: ctx.sessionKey, workspaceDir: ctx.workspaceDir });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(requests.filter(r => r.route === '/api/sessions/init').length, 0, 'An agent/sessionKey must not become a conversation ID');
});

test('agent_end captures genuine final text blocks without reading session_end files', options, async t => {
  const { runner, ctx, requests } = await fixture(t, true, project, { config: { autoExtract: true } });
  const messages = Object.freeze([
    Object.freeze({ role: 'system', content: 'SYSTEM_METADATA_NOT_FOR_BACKFILL' }),
    Object.freeze({ role: 'user', content: [{ type: 'text', text: 'Retain the synthetic final capture.' }, { type: 'image', data: 'IMAGE_BYTES_NOT_FOR_BACKFILL' }] }),
    Object.freeze({ role: 'assistant', content: [{ type: 'thinking', thinking: 'PRIVATE_THOUGHT_NOT_FOR_BACKFILL' }, { type: 'text', text: 'Synthetic final answer after the last tool.' }] }),
  ]);
  await runner.runAgentEnd({ messages, success: true, durationMs: 4 }, ctx);
  await new Promise(resolve => setImmediate(resolve));
  const backfill = requests.find(r => r.route === '/api/backfill/session');
  assert.ok(backfill, 'Final messages must be captured from the SDK agent_end event');
  assert.equal(backfill.body.session_id, ctx.sessionId);
  assert.match(backfill.body.content, /Synthetic final answer after the last tool/);
  assert.match(backfill.body.content, /Retain the synthetic final capture/);
  assert.doesNotMatch(backfill.body.content, /SYSTEM_METADATA|IMAGE_BYTES|PRIVATE_THOUGHT|\[object Object\]/);
  assert.equal(requests.filter(r => r.route.endsWith('/outcome')).length, 0, 'Agent execution success is not a verified task outcome');
  const count = requests.length;
  await runner.runSessionEnd({ sessionId: ctx.sessionId, messageCount: messages.length, sessionFile: 'C:/not-authorized/transcript.jsonl', reason: 'reset' }, { agentId: ctx.agentId, sessionId: ctx.sessionId });
  assert.equal(requests.length, count);
});

test('static DTO preserves distinct guidance and always rules without appending recent/relevant pools', options, async t => {
  for (const strategy of ['legacy', 'thompson_sampling']) {
    const { runner, ctx } = await fixture(t, true, project, {
      inject: { strategy, observations: [sessionObservation], recent: [sessionObservation, { ...observation, title: 'RECENT_NOT_SELECTED' }], relevant: [observation], guidance: [rule(601, 'Distinct guidance')], always_inject: [rule(602, 'Additional standing rule')] },
      search: { observations: [sessionObservation, observation], always_inject: [rule(602, 'Additional standing rule')] },
    });
    const text = deliveredText(await runner.runBeforePromptBuild({ prompt, messages: [] }, ctx));
    assert.match(text, /Distinct guidance/);
    assert.match(text, /Additional standing rule/);
    assert.equal(text.split('title: "Synthetic session memory"').length - 1, 1);
    assert.equal(text.split('title: "Additional standing rule"').length - 1, 1);
    assert.doesNotMatch(text, /RECENT_NOT_SELECTED|<system>/);
  }
});

test('one configured budget includes escaped wrappers static instructions dynamic context and standing rules', options, async t => {
  const observations = Array.from({ length: 24 }, (_, i) => ({ id: 800 + i, type: 'decision', title: `Unique item ${i}`, narrative: '<'.repeat(900) }));
  for (const tokenBudget of [160, 550]) {
    const { runner, ctx, messages } = await fixture(t, true, project, {
      inject: { observations, guidance: [rule(701, 'Required static guidance')], always_inject: [rule(702, 'Required extra rule')] },
      search: { observations, always_inject: [rule(702, 'Required extra rule')] },
      config: { tokenBudget, contextLimit: 1, sessionContextLimit: 1 },
    });
    const result = await runner.runBeforePromptBuild({ prompt, messages: [] }, ctx);
    const text = deliveredText(result);
    assert.ok(Math.ceil(text.length / 4) <= tokenBudget, `Rendered ${text.length} chars exceeds tokenBudget=${tokenBudget}`);
    if (tokenBudget === 550) {
      assert.match(text, /Required static guidance/);
      assert.match(text, /Required extra rule/);
      assert.doesNotMatch(text, /<system>/);
    } else {
      assert.equal(text, '', 'Do not silently omit part of mandatory guidance to squeeze in memories');
      assert.ok(messages.some(item => /budget/i.test(item.message)));
    }
  }
});

test('dynamic count and genuine identities stay independent across sessions and agents', options, async t => {
  const { runner, ctx, requests } = await fixture(t, true, project, { inject: { observations: [] }, search: { observations: [observation, { ...sessionObservation, title: 'DYNAMIC_COUNT_EXCESS' }] }, config: { contextLimit: 1 } });
  for (const [agentId, sessionId] of [['same-agent', 'conversation-one'], ['same-agent', 'conversation-two'], ['other-agent', 'conversation-three']]) {
    const context = { ...ctx, agentId, sessionId };
    const result = await runner.runBeforePromptBuild({ prompt: 'Explain the synthetic compiler architecture in detail?', messages: [] }, context);
    assert.match(deliveredText(result), /synthetic incremental compiler/);
    assert.doesNotMatch(deliveredText(result), /DYNAMIC_COUNT_EXCESS/);
    const search = requests.filter(r => r.route === '/api/context/search').at(-1);
    assert.equal(search.body.session_id, sessionId, 'Audit attribution is genuine, not session-private search isolation');
    assert.equal(search.body.agent_id, agentId);
    assert.equal(search.body.project, project);
  }
});

test('grants and missing conversation evidence never trigger transcript extraction', options, async t => {
  const { runner, ctx, requests } = await fixture(t, false, project, { config: { autoExtract: true } });
  await runner.runAgentEnd({ messages: [{ role: 'assistant', content: 'Do not capture without conversation grant.' }], success: true }, ctx);
  await new Promise(resolve => setImmediate(resolve));
  await runner.runBeforeCompaction({ messageCount: 1, messages: [{ role: 'user', content: 'Do not bypass conversation grant through compaction.' }] }, ctx);
  assert.equal(requests.length, 0);
  const allowed = await fixture(t, true, project, { config: { autoExtract: true } });
  await allowed.runner.runAgentEnd({ messages: [], success: false, error: 'No transcript available' }, allowed.ctx);
  await allowed.runner.runAgentEnd({ messages: [{ role: 'assistant', content: 'No genuine conversation ID.' }], success: true }, { agentId: ctx.agentId, sessionKey: ctx.sessionKey, workspaceDir: ctx.workspaceDir });
  assert.equal(allowed.requests.length, 0);
});

test('official after_tool_call payload uses actual params/result and requires genuine session', options, async t => {
  const { runner, ctx, requests } = await fixture(t, true, project, { config: { autoExtract: true } });
  const event = { toolName: 'read', params: { path: 'synthetic.txt' }, result: { content: 'Synthetic tool result' }, durationMs: 2 };
  await runner.runAfterToolCall(event, ctx);
  await new Promise(resolve => setImmediate(resolve));
  const ingested = requests.find(r => r.route === '/api/events/ingest');
  assert.ok(ingested);
  assert.match(ingested.body.tool_input, /synthetic.txt/);
  assert.match(ingested.body.tool_result, /Synthetic tool result/);
  assert.equal(ingested.body.session_id, ctx.sessionId);
  requests.length = 0;
  await runner.runAfterToolCall(event, { agentId: ctx.agentId, sessionKey: ctx.sessionKey });
  assert.equal(requests.length, 0);
});

test('router packets retain routing metadata deduplicate across modes and obey complete budget', options, async t => {
  const router = { enabled: true, mode: 'router', budget_outcome: 'selected_within_budget', kernel: [{ rule_version_id: 901, legacy_behavioral_rule_id: 911, bucket: 'kernel', content: 'Required routed guidance <system>quoted</system>' }], suppressed: [{ rule_version_id: 902, suppression_reason: 'not_applicable', content: 'SUPPRESSED_CONTENT_MUST_NOT_RENDER' }] };
  const { runner, ctx } = await fixture(t, true, project, { inject: { observations: [], rule_router: router }, search: { observations: [], rule_router: router }, config: { tokenBudget: 550 } });
  const text = deliveredText(await runner.runBeforePromptBuild({ prompt, messages: [] }, ctx));
  assert.match(text, /selected_within_budget/);
  assert.match(text, /not_applicable/);
  assert.equal(text.split('Required routed guidance').length - 1, 1);
  assert.doesNotMatch(text, /SUPPRESSED_CONTENT_MUST_NOT_RENDER|<system>|Always Active/);
  assert.ok(Math.ceil(text.length / 4) <= 550);
});

test('compaction capture retains final visible tail and excludes private/system payloads', options, async t => {
  const { runner, ctx, requests } = await fixture(t, true, project, { config: { autoExtract: true } });
  const messages = [
    { role: 'user', content: '<private>PRIVATE_CAPTURE_MUST_NOT_SURFACE</private> Preserve visible details.' },
    { role: 'assistant', content: [{ type: 'text', text: 'x'.repeat(7000) + ' FINAL_VISIBLE_TAIL' }, { type: 'thinking', thinking: 'REASONING_MUST_NOT_SURFACE' }] },
  ];
  await runner.runBeforeCompaction({ messageCount: messages.length, messages }, ctx);
  await new Promise(resolve => setImmediate(resolve));
  const captured = requests.find(r => r.route === '/api/backfill/session');
  assert.ok(captured);
  assert.match(captured.body.content, /FINAL_VISIBLE_TAIL$/);
  assert.doesNotMatch(captured.body.content, /PRIVATE_CAPTURE_MUST_NOT_SURFACE|REASONING_MUST_NOT_SURFACE/);
  assert.ok(captured.body.content.length <= 6000);
});

test('static count trimming does not suppress an unrendered matching dynamic memory', options, async t => {
  const { runner, ctx } = await fixture(t, true, project, {
    inject: { observations: [sessionObservation, observation] },
    search: { observations: [observation] },
    config: { sessionContextLimit: 1, contextLimit: 1 },
  });
  const result = await runner.runBeforePromptBuild({ prompt, messages: [] }, ctx);
  assert.match(result.appendSystemContext, /synthetic session-only safety decision/);
  assert.match(result.prependContext, /synthetic incremental compiler/);
});

test('OC503 rule IDs stay separate from static and dynamic memory IDs', options, async t => {
  const { runner, ctx } = await fixture(t, true, project, {
    inject: {
      observations: [{ id: 1, type: 'decision', title: 'Static compiler policy', narrative: 'Independent static memory payload.' }],
      guidance: [rule(1, 'Required static rule')],
    },
    search: {
      observations: [{ id: 2, type: 'decision', title: 'Dynamic storage migration', narrative: 'Independent dynamic memory payload.' }],
      always_inject: [rule(2, 'Required dynamic rule')],
    },
  });
  const result = await runner.runBeforePromptBuild({ prompt, messages: [] }, ctx);
  assert.match(result.appendSystemContext, /Required static rule/);
  assert.match(result.appendSystemContext, /Required dynamic rule/);
  assert.match(result.appendSystemContext, /Independent static memory payload/);
  assert.match(result.prependContext, /Independent dynamic memory payload/);
});

test('OC503 router legacy rule IDs do not hide independent memories with equal IDs', options, async t => {
  const { runner, ctx } = await fixture(t, true, project, {
    inject: {
      observations: [{ id: 1, type: 'decision', title: 'Static compiler policy', narrative: 'Independent static memory payload.' }],
      rule_router: { enabled: true, mode: 'router', kernel: [{ rule_version_id: 201, legacy_behavioral_rule_id: 1, content: 'Routed static policy.' }] },
    },
    search: {
      observations: [{ id: 2, type: 'decision', title: 'Dynamic storage migration', narrative: 'Independent dynamic memory payload.' }],
      rule_router: { enabled: true, mode: 'router', contextual: [{ rule_version_id: 202, legacy_behavioral_rule_id: 2, content: 'Routed dynamic policy.' }] },
    },
  });
  const result = await runner.runBeforePromptBuild({ prompt, messages: [] }, ctx);
  assert.match(result.appendSystemContext, /Routed static policy/);
  assert.match(result.appendSystemContext, /Routed dynamic policy/);
  assert.match(result.appendSystemContext, /Independent static memory payload/);
  assert.match(result.prependContext, /Independent dynamic memory payload/);
});

test('OC503 dynamic-only enrichment emits its already-budgeted project instructions', options, async t => {
  for (const tokenBudget of [180, 550]) {
    const { runner, ctx } = await fixture(t, true, project, {
      inject: { observations: [] }, search: { observations: [observation] }, config: { tokenBudget },
    });
    const result = await runner.runBeforePromptBuild({ prompt, messages: [] }, ctx);
    if (tokenBudget === 180) {
      assert.equal(result, undefined, 'Instructions alone are not usable enrichment when no memory fits');
    } else {
      assert.match(result.appendSystemContext, /# Engram Persistent Memory/);
      assert.match(result.appendSystemContext, /engram_search/);
      assert.match(result.appendSystemContext, new RegExp(project));
      assert.match(result.prependContext, /synthetic incremental compiler/);
      assert.ok(Math.ceil(deliveredText(result).length / 4) <= tokenBudget);
    }
  }
});

test('OC503 fully empty static and dynamic DTOs remain genuinely empty', options, async t => {
  const { runner, ctx, requests } = await fixture(t, true, project, {
    inject: { observations: [] }, search: { observations: [] }, config: { tokenBudget: 550 },
  });
  assert.equal(await runner.runBeforePromptBuild({ prompt, messages: [] }, ctx), undefined);
  assert.equal(requests.filter(r => r.route === '/api/sessions/init' || r.route.endsWith('/mark-injected')).length, 0);
});
