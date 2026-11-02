import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

// Run only as a child of the real-router registered writer/lifecycle tests.
const input = JSON.parse(fs.readFileSync(0, 'utf8'));
const host = process.env.ENGRAM_OPENCLAW_HOST_FIXTURE;
const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED;
assert.ok(host && compiled, 'Provide the pinned official host and freshly compiled candidate adapter');
assert.equal(JSON.parse(fs.readFileSync(path.join(host, 'package.json'), 'utf8')).version, '2026.10.7');
const target = new URL(input.url);
assert.equal(target.protocol, 'http:');
assert.equal(target.hostname, '127.0.0.1');
assert.notEqual(target.port, '', 'The Go httptest server must own an ephemeral loopback port');

// Do not fake responses or rewrite routes. Every plugin HTTP request reaches Go.
const networkFetch = globalThis.fetch;
const requests = [];
globalThis.fetch = async (url, options) => {
  assert.equal(new URL(String(url)).origin, target.origin, 'Only the isolated Go router may receive traffic');
  const response = await networkFetch(url, { ...options, redirect: 'error' });
  requests.push({ method: (options?.method ?? 'GET').toUpperCase(), status: response.status });
  return response;
};

const { t: createPluginRegistry } = await import(pathToFileURL(path.join(host, 'dist/registry-BwDJiK-z.mjs')));
const { r: loadPluginManifest } = await import(pathToFileURL(path.join(host, 'dist/manifest-D9tOzTAf.mjs')));
const { a: toToolDefinitions } = await import(pathToFileURL(path.join(host, 'dist/agent-tool-definition-adapter-CNc5GutA.mjs')));
const imported = await import(pathToFileURL(path.join(compiled, 'index.js')));
const plugin = imported.default.default ?? imported.default;
const loaded = loadPluginManifest(fileURLToPath(new URL('../', import.meta.url)));
assert.equal(loaded.ok, true, loaded.error);
const manifest = loaded.manifest;
const logger = Object.fromEntries(['info', 'warn', 'error', 'debug'].map(level => [level, message => console.error(`[host:${level}] ${message}`)]));
const pluginConfig = {
  url: input.url, token: input.token, project: input.project,
  autoExtract: false, workspaceDir: process.env.HOME,
};
const config = { plugins: { enabled: true, allow: ['engram'], slots: { memory: 'engram' }, entries: { engram: { enabled: true, hooks: { allowConversationAccess: false }, config: pluginConfig } } } };
const registration = createPluginRegistry({ runtime: { config: { current: () => config } }, logger, activateGlobalSideEffects: false, coreGatewayMethodNames: [] });
const record = {
  id: manifest.id, name: manifest.displayName, version: manifest.version, kind: manifest.kind,
  source: path.join(compiled, 'index.js'), rootDir: path.dirname(compiled), origin: 'global',
  enabled: true, explicitlyEnabled: true, status: 'loaded', configSchema: true,
  contracts: manifest.contracts, memorySlotSelected: true, httpRoutes: 0, hookCount: 0,
};
for (const field of ['toolNames', 'hookNames', 'channelIds', 'cliBackendIds', 'providerIds', 'embeddingProviderIds', 'speechProviderIds', 'realtimeTranscriptionProviderIds', 'realtimeVoiceProviderIds', 'mediaUnderstandingProviderIds', 'transcriptSourceProviderIds', 'imageGenerationProviderIds', 'videoGenerationProviderIds', 'musicGenerationProviderIds', 'webFetchProviderIds', 'webSearchProviderIds', 'migrationProviderIds', 'agentHarnessIds', 'cliCommands', 'services', 'gatewayDiscoveryServiceIds', 'commands']) record[field] = [];
registration.registry.plugins.push(record);
plugin.register(registration.createApi(record, { config, pluginConfig, hookPolicy: { allowConversationAccess: false } }));

// No service, watcher, CLI callback, gateway, provider or lifecycle hook starts.
const ctx = { agentId: input.agentId, sessionId: input.sessionId, sessionKey: `agent:${input.agentId}:${input.sessionId}`, workspaceDir: process.env.HOME };
const tools = (await Promise.all(registration.registry.tools.map(entry => entry.factory(ctx)))).flat().filter(Boolean);
for (const name of ['memory_store', 'engram_remember', input.tool]) {
  assert.ok(manifest.contracts.tools.includes(name), `Manifest must expose ${name}`);
  assert.ok(registration.registry.tools.some(entry => entry.names.includes(name)), `Official registrar must expose ${name}`);
}
const tool = tools.find(entry => entry.name === input.tool);
assert.ok(tool, `Registered tool unavailable: ${input.tool}; ${JSON.stringify(registration.registry.diagnostics)}`);
const [definition] = toToolDefinitions([tool]);
const result = await definition.execute('synthetic-memory-store-call', input.params);
assert.ok(Array.isArray(result.content), 'The official host adapter must return AgentToolResult');
const text = result.content.filter(item => item.type === 'text').map(item => item.text).join('\n');
console.log(JSON.stringify({ tool: input.tool, text }));
if (input.operation) {
  assert.ok(['suppress', 'unsuppress', 'archive'].includes(input.operation), 'Unknown lifecycle fixture operation');
  assert.ok(text.trim(), 'The registered lifecycle tool must return an outcome');
  const mutations = requests.filter(request => !['GET', 'HEAD'].includes(request.method));
  assert.ok(mutations.length > 0, 'The registered lifecycle tool must send a real mutation request');
  if (input.expectConflict) {
    assert.equal(mutations.length, 1, 'A confirmed conflict must not be automatically retried');
    assert.ok(mutations.every(request => request.status === 409), 'A repeated suppress must return the actual no-mutation conflict');
    assert.match(text, /fail|conflict|rejected/i, 'The registered tool must report the confirmed conflict');
    assert.doesNotMatch(text, /outcome\s+(?:is\s+)?unknown/i, 'A confirmed rollback conflict is not an unknown outcome');
    assert.doesNotMatch(text, /^(suppressed|restored|permanently archived)/i, 'A conflict must not be reported as success');
  } else if (input.expectRefusal) {
    assert.ok(mutations.every(request => [403, 404].includes(request.status)), 'A denied lifecycle operation must receive an authorization/not-found response');
    assert.match(text, /fail|not found|unavailable|denied|forbidden|not authorized|permission/i, 'The registered tool must report refusal rather than success');
  } else {
    assert.notEqual(result.isError, true, 'The official host must not report a lifecycle error');
    assert.ok(mutations.every(request => request.status >= 200 && request.status < 300), `Registered ${input.tool} did not complete ${input.operation}: ${JSON.stringify(mutations)}`);
    assert.doesNotMatch(text, /fail|unavailable|denied|forbidden|invalid parameter/i, 'The registered tool must not report failure for a successful transition');
  }
} else if (input.expectRefusal) {
  assert.match(text, /^Failed to store memory:/, 'Agent-private write without a principal must refuse');
  assert.doesNotMatch(text, /^Stored:/);
} else {
  assert.match(text, /^Stored:/, `Registered ${input.tool} did not persist the memory: ${text}`);
}
